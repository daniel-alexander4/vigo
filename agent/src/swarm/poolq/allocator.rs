// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Per-topic monotonic seq allocator for poolq (ADR-029).
//!
//! Each poolq topic has a monotonic `seq` that totally orders its messages.
//! Seqs are monotonic but **not** strictly gap-free: `issue()` persists the
//! bumped counter and releases the flock before the caller writes the message
//! file, so a crash between allocation and the message landing burns that seq.
//! That's benign — consumers range-query by seq and an absent seq is just a
//! message that never landed, not a correctness violation.
//! Founder-only posting in v1 means the founder is the sole writer, so a
//! single-machine flock-protected counter is sufficient — there is no
//! cross-envoy consensus problem to solve. The flock serializes concurrent
//! `vigo swarm poolq post` invocations from the same user on the same envoy
//! (two terminals, two scripted publishers); cross-envoy concurrency under
//! the same puddle is the v2 multi-writer delegation problem and is
//! explicitly out of v1 scope (ADR-029).
//!
//! ### State
//!
//! `~/.vigo-poolq/<topic_id_hex>/allocator.json` carries one field —
//! `next_seq: u64` — and is flock-protected on every read-modify-write.
//! Missing file ⇒ next_seq starts at 1 (seq=0 is reserved as the
//! never-issued sentinel — no canonical message ever carries it).
//!
//! ### Recovery
//!
//! On boot the allocator resumes from `next_seq = max(local_seq) + 1` if it
//! reads a value; if the file is missing or corrupted, the caller's
//! [`SeqAllocator::issue_at`] starts fresh at 1. This is safe because
//! founder-only v1 means no other writer can have issued in between —
//! cross-envoy founder concurrency is the v2 delegation problem.
//!
//! ### Why not a process-wide in-memory counter
//!
//! Because two `vigo swarm poolq post` invocations are two separate processes
//! (multicall binary). The on-disk counter is the only place process-spanning
//! state can live; flock(2) is the lightest portable serialization primitive
//! that does what we need.

use std::fs::{self, OpenOptions};
use std::io::{Read, Seek, SeekFrom, Write};
use std::os::unix::io::{AsRawFd, RawFd};
use std::path::{Path, PathBuf};

use anyhow::{bail, Context, Result};
use serde::{Deserialize, Serialize};

/// First `seq` issued by a fresh allocator. seq=0 is reserved as the
/// "never issued" sentinel so callers that initialize a `last_seen_seq=0`
/// can range-query without an off-by-one.
pub const FIRST_SEQ: u64 = 1;

/// On-disk shape of the allocator file. One field today; new fields land
/// `#[serde(default)]` to keep on-disk files written by older agents
/// readable by newer ones.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize, Default)]
struct AllocatorState {
    /// Next `seq` to issue. After `issue()` returns `n`, the file holds
    /// `next_seq = n + 1`.
    #[serde(default)]
    next_seq: u64,
}

/// File-backed, flock-protected per-topic seq allocator.
///
/// Lifecycle: caller passes the on-disk path; [`Self::issue`] opens the
/// file, takes an exclusive blocking flock, reads + bumps + writes
/// `next_seq`, releases the lock by closing the fd, and returns the
/// issued `seq`. Concurrent callers from other processes block on the
/// flock and serialize cleanly.
pub struct SeqAllocator {
    path: PathBuf,
}

impl SeqAllocator {
    /// Bind to the allocator file at `path`. The file is created (with
    /// `next_seq = FIRST_SEQ`) on the first [`Self::issue`] call if it
    /// doesn't yet exist; the parent directory must already exist.
    pub fn at(path: impl Into<PathBuf>) -> Self {
        Self { path: path.into() }
    }

    pub fn path(&self) -> &Path {
        &self.path
    }

    /// Issue the next `seq`. Atomic under flock(2): open + lock + read +
    /// bump + write + release. Blocking (LOCK_EX without LOCK_NB) so
    /// concurrent posts from the same user on the same envoy serialize
    /// cleanly rather than fail.
    ///
    /// Returns the issued `seq` (the value the caller bakes into a
    /// `MessageBody` before signing).
    pub fn issue(&self) -> Result<u64> {
        if let Some(parent) = self.path.parent() {
            fs::create_dir_all(parent)
                .with_context(|| format!("creating {}", parent.display()))?;
        }
        let mut file = OpenOptions::new()
            .read(true)
            .write(true)
            .create(true)
            .truncate(false)
            .open(&self.path)
            .with_context(|| format!("opening allocator file {}", self.path.display()))?;
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let _ = fs::set_permissions(&self.path, fs::Permissions::from_mode(0o644));
        }
        let _guard = FlockGuard::acquire(file.as_raw_fd())
            .with_context(|| format!("flocking allocator file {}", self.path.display()))?;

        // Read current state (empty file ⇒ fresh allocator).
        let mut buf = String::new();
        file.read_to_string(&mut buf)
            .with_context(|| format!("reading allocator file {}", self.path.display()))?;
        let state: AllocatorState = if buf.trim().is_empty() {
            AllocatorState { next_seq: FIRST_SEQ }
        } else {
            serde_json::from_str(&buf)
                .with_context(|| format!("parsing allocator file {}", self.path.display()))?
        };
        if state.next_seq == 0 {
            // A v0 file or a corrupted one — treat as fresh. `seq=0` is the
            // "never issued" sentinel and is never returned by `issue`.
            bail!(
                "allocator file {} has next_seq=0 (reserved sentinel); \
                 remove it and the allocator will resume fresh at {}",
                self.path.display(),
                FIRST_SEQ
            );
        }

        let issued = state.next_seq;
        let new_state = AllocatorState { next_seq: issued + 1 };

        // Truncate and rewrite. Safe under flock: no other process can read
        // an intermediate state. We don't bother with temp+rename here
        // because the flock is the serialization point; rename would just
        // be ceremony.
        file.set_len(0)
            .with_context(|| format!("truncating allocator file {}", self.path.display()))?;
        file.seek(SeekFrom::Start(0))
            .with_context(|| format!("seeking allocator file {}", self.path.display()))?;
        let bytes = serde_json::to_vec_pretty(&new_state).context("serializing allocator state")?;
        file.write_all(&bytes)
            .with_context(|| format!("writing allocator file {}", self.path.display()))?;
        file.sync_data().ok();
        // FlockGuard drops on scope exit, which releases the flock; the
        // file fd closes when `file` drops a moment later.
        Ok(issued)
    }

    /// Peek at the next-to-be-issued seq without bumping. Used by tests +
    /// status surfaces. Returns [`FIRST_SEQ`] when the file is missing.
    pub fn peek(&self) -> Result<u64> {
        let bytes = match fs::read(&self.path) {
            Ok(b) => b,
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(FIRST_SEQ),
            Err(e) => {
                return Err(anyhow::Error::from(e))
                    .with_context(|| format!("reading allocator file {}", self.path.display()))
            }
        };
        if bytes.is_empty() {
            return Ok(FIRST_SEQ);
        }
        let state: AllocatorState = serde_json::from_slice(&bytes)
            .with_context(|| format!("parsing allocator file {}", self.path.display()))?;
        Ok(if state.next_seq == 0 { FIRST_SEQ } else { state.next_seq })
    }
}

/// RAII guard that releases the flock when dropped. Holds the raw fd
/// rather than a `&File` so the caller can still mutate the file (seek +
/// truncate + write) under the lock. The flock is per-open-file-
/// description (Linux semantics), so a second process opening the same
/// path gets its own fd and blocks on LOCK_EX until this guard drops.
struct FlockGuard {
    fd: RawFd,
}

impl FlockGuard {
    fn acquire(fd: RawFd) -> Result<Self> {
        // LOCK_EX (blocking exclusive) — we want concurrent posters to
        // serialize, not fail-fast. The kernel queues waiters; the runtime
        // wakes us when our turn arrives.
        let ret = unsafe { libc::flock(fd, libc::LOCK_EX) };
        if ret != 0 {
            return Err(std::io::Error::last_os_error()).context("flock(LOCK_EX) failed");
        }
        Ok(Self { fd })
    }
}

impl Drop for FlockGuard {
    fn drop(&mut self) {
        // LOCK_UN — explicit unlock. The kernel also unlocks on close, but
        // explicit unlock makes the release point obvious to anyone
        // reading this code.
        unsafe {
            libc::flock(self.fd, libc::LOCK_UN);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::TempDir;

    #[test]
    fn fresh_allocator_issues_from_one() {
        let dir = TempDir::new().unwrap();
        let path = dir.path().join("topic/allocator.json");
        let alloc = SeqAllocator::at(&path);
        assert_eq!(alloc.peek().unwrap(), FIRST_SEQ);
        assert_eq!(alloc.issue().unwrap(), 1);
        assert_eq!(alloc.issue().unwrap(), 2);
        assert_eq!(alloc.issue().unwrap(), 3);
        assert_eq!(alloc.peek().unwrap(), 4);
    }

    #[test]
    fn allocator_resumes_across_instances() {
        let dir = TempDir::new().unwrap();
        let path = dir.path().join("topic/allocator.json");
        {
            let alloc = SeqAllocator::at(&path);
            assert_eq!(alloc.issue().unwrap(), 1);
            assert_eq!(alloc.issue().unwrap(), 2);
        }
        // Fresh allocator binding to the same path reads the on-disk state.
        let alloc2 = SeqAllocator::at(&path);
        assert_eq!(alloc2.peek().unwrap(), 3);
        assert_eq!(alloc2.issue().unwrap(), 3);
    }

    #[test]
    fn corrupted_zero_seq_is_rejected() {
        let dir = TempDir::new().unwrap();
        let path = dir.path().join("topic/allocator.json");
        fs::create_dir_all(path.parent().unwrap()).unwrap();
        fs::write(&path, br#"{"next_seq":0}"#).unwrap();
        let alloc = SeqAllocator::at(&path);
        // Reading-side returns FIRST_SEQ defensively...
        assert_eq!(alloc.peek().unwrap(), FIRST_SEQ);
        // ...but issue() refuses, because someone has placed a sentinel
        // value in the file. Operator removes the file to recover; the
        // founder-only invariant means no message at seq=0 ever existed.
        assert!(alloc.issue().is_err());
    }

    #[test]
    fn concurrent_issuers_serialize_under_flock() {
        // Spawn N threads each issuing M times; assert the union is exactly
        // [1..=N*M] with no duplicates and no gaps. flock(2) serializes
        // across processes; threads in this test stand in for separate
        // processes. (Note: flock is per-fd, so each issuer opens its own
        // fd via SeqAllocator::at.)
        const THREADS: usize = 8;
        const PER_THREAD: usize = 25;

        let dir = TempDir::new().unwrap();
        let path = dir.path().join("topic/allocator.json");
        // Pre-create the parent so the threads don't race on create_dir_all.
        fs::create_dir_all(path.parent().unwrap()).unwrap();

        let path_arc: std::sync::Arc<PathBuf> = std::sync::Arc::new(path.clone());
        let handles: Vec<_> = (0..THREADS)
            .map(|_| {
                let p = path_arc.clone();
                std::thread::spawn(move || {
                    let alloc = SeqAllocator::at((*p).clone());
                    (0..PER_THREAD).map(|_| alloc.issue().unwrap()).collect::<Vec<_>>()
                })
            })
            .collect();

        let mut all: Vec<u64> = handles
            .into_iter()
            .flat_map(|h| h.join().unwrap())
            .collect();
        all.sort_unstable();
        let expected: Vec<u64> = (1..=(THREADS * PER_THREAD) as u64).collect();
        assert_eq!(all, expected, "concurrent issuers must produce a gap-free sequence");
    }
}
