// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Apply reconciliation actions: fetch files from peers and write them to
//! the user's ~/filesync/ with correct ownership, or delete local files.

use anyhow::{bail, Context, Result};
use sha2::{Digest, Sha256};
use std::fs;
use std::io::{Read, Write};
use std::path::Path;

use super::reconciler::Action;
use super::scanner::effective_max_file_size;
use crate::platform::users::chown_to_user;

/// Chunk size for streaming reads — large enough to amortize syscall cost,
/// small enough not to pressure RAM on receivers.
const STREAM_CHUNK: usize = 256 * 1024;

/// Execute a single action against a user's ~/filesync/ directory.
///
/// For `Fetch`, opens an mTLS connection to the peer, downloads the file,
/// verifies the sha256, writes it to the target path, sets ownership to
/// `uid`/`gid`, and sets the mtime.
///
/// For `Delete`, removes the file (best-effort — missing file is a success).
pub fn apply(
    action: &Action,
    sync_dir: &Path,
    user: &str,
    peer_addr: &str,
    http: &ureq::Agent,
) -> Result<()> {
    match action {
        Action::Fetch { name, sha256, size, mtime } => {
            fetch_file(http, peer_addr, user, name, sha256, *size, *mtime, sync_dir)
        }
        Action::Delete { name } => delete_file(sync_dir, name),
    }
}

/// GET https://<peer>/filesync/<user>/<name> and write to sync_dir/name.
///
/// Streams the response body directly to a temp file, hashing and size-checking
/// as we go. Nothing is buffered in RAM beyond `STREAM_CHUNK` bytes. On any
/// mismatch (sha256, size, oversized, I/O error) the temp file is removed and
/// the operation fails without touching the target path.
fn fetch_file(
    http: &ureq::Agent,
    peer_addr: &str,
    user: &str,
    name: &str,
    expected_sha256: &str,
    expected_size: u64,
    mtime: i64,
    sync_dir: &Path,
) -> Result<()> {
    if expected_size > effective_max_file_size() {
        bail!("file {} exceeds max size ({} bytes)", name, expected_size);
    }
    if !valid_filename(name) {
        bail!("invalid filename: {:?}", name);
    }

    let url = format!(
        "https://{}/swarm/filesync/{}/{}",
        peer_addr,
        urlencode(user),
        urlencode(name)
    );

    let mut resp = http
        .get(&url)
        .call()
        .map_err(|e| anyhow::anyhow!("fetch {} from {} failed: {}", name, peer_addr, e))?;

    if resp.status() != 200 {
        bail!("fetch {} from {} returned {}", name, peer_addr, resp.status());
    }

    fs::create_dir_all(sync_dir).with_context(|| format!("creating {}", sync_dir.display()))?;
    let target = sync_dir.join(name);
    let tmp = sync_dir.join(format!(".{}.filesync.tmp", name));

    let result = stream_body_to_file(
        &mut resp,
        &tmp,
        expected_sha256,
        expected_size,
        name,
    );

    if let Err(e) = result {
        let _ = fs::remove_file(&tmp);
        return Err(e);
    }

    fs::rename(&tmp, &target).with_context(|| format!("renaming to {}", target.display()))?;

    // Set ownership + mtime (Unix).
    #[cfg(unix)]
    {
        chown_to_user(&target, user);
        chown_to_user(sync_dir, user);
        set_mtime(&target, mtime);
    }
    #[cfg(not(unix))]
    {
        let _ = mtime;
    }

    Ok(())
}

/// Stream a ureq response body into a file, hashing as we write.
/// Enforces the expected size as an upper bound and rejects sha256 mismatch.
/// Fsyncs on success so the temp file is durable before rename.
fn stream_body_to_file(
    resp: &mut ureq::http::Response<ureq::Body>,
    tmp: &Path,
    expected_sha256: &str,
    expected_size: u64,
    name: &str,
) -> Result<()> {
    let mut reader = resp.body_mut().as_reader();
    let mut file = fs::File::create(tmp)
        .with_context(|| format!("creating {}", tmp.display()))?;
    let mut hasher = Sha256::new();
    let mut buf = vec![0u8; STREAM_CHUNK];
    let mut total: u64 = 0;

    loop {
        let n = reader
            .read(&mut buf)
            .with_context(|| format!("reading filesync body for {}", name))?;
        if n == 0 {
            break;
        }
        total = total.saturating_add(n as u64);
        if total > expected_size {
            bail!(
                "body for {} exceeded expected size {} (read {})",
                name,
                expected_size,
                total
            );
        }
        hasher.update(&buf[..n]);
        file.write_all(&buf[..n])
            .with_context(|| format!("writing {}", tmp.display()))?;
    }

    if total != expected_size {
        bail!(
            "size mismatch for {}: got {}, expected {}",
            name,
            total,
            expected_size
        );
    }

    let got = hex::encode(hasher.finalize());
    if got != expected_sha256 {
        bail!(
            "sha256 mismatch for {}: got {}, expected {}",
            name,
            got,
            expected_sha256
        );
    }

    file.sync_all()
        .with_context(|| format!("fsync {}", tmp.display()))?;
    Ok(())
}

fn delete_file(sync_dir: &Path, name: &str) -> Result<()> {
    if !valid_filename(name) {
        bail!("invalid filename: {:?}", name);
    }
    let target = sync_dir.join(name);
    match fs::remove_file(&target) {
        Ok(_) => Ok(()),
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(()),
        Err(e) => Err(e).with_context(|| format!("deleting {}", target.display())),
    }
}

/// Reject path traversal and weird filenames. Matches the scanner's filter
/// (non-dot, no separators, no NUL). Must stay in sync with the server-side
/// route handler.
pub fn valid_filename(name: &str) -> bool {
    if name.is_empty() || name.len() > 255 {
        return false;
    }
    if name.starts_with('.') {
        return false;
    }
    for c in name.chars() {
        if c == '/' || c == '\\' || c == '\0' {
            return false;
        }
    }
    !matches!(name, "." | "..")
}

fn urlencode(s: &str) -> String {
    // Percent-encode anything that isn't an unreserved URL character.
    let mut out = String::with_capacity(s.len());
    for b in s.as_bytes() {
        match *b {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'_' | b'.' | b'~' => {
                out.push(*b as char);
            }
            _ => {
                out.push_str(&format!("%{:02X}", b));
            }
        }
    }
    out
}

/// Set a file's mtime to the given unix-second timestamp. Returns true on
/// success, false on any failure (path conversion, syscall error, or
/// non-unix build). Shared with the scanner's resurrection path — when a
/// previously-tombstoned file reappears, the scanner bumps its mtime so
/// peers' tombstones can't out-rank it on the next reconcile.
#[cfg(unix)]
pub(super) fn set_mtime(path: &Path, mtime: i64) -> bool {
    use std::ffi::CString;
    let c_path = match CString::new(path.to_string_lossy().as_bytes()) {
        Ok(p) => p,
        Err(_) => return false,
    };
    let times = [
        libc::timespec { tv_sec: 0, tv_nsec: libc::UTIME_OMIT },
        libc::timespec { tv_sec: mtime as libc::time_t, tv_nsec: 0 },
    ];
    let rc = unsafe {
        libc::utimensat(libc::AT_FDCWD, c_path.as_ptr(), times.as_ptr(), 0)
    };
    rc == 0
}

#[cfg(not(unix))]
pub(super) fn set_mtime(_path: &Path, _mtime: i64) -> bool {
    false
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn filename_validation() {
        assert!(valid_filename("notes.md"));
        assert!(valid_filename("file with spaces.txt"));
        assert!(!valid_filename(""));
        assert!(!valid_filename("."));
        assert!(!valid_filename(".."));
        assert!(!valid_filename(".hidden"));
        assert!(!valid_filename("a/b"));
        assert!(!valid_filename("a\\b"));
        assert!(!valid_filename("a\0b"));
    }

    #[test]
    fn urlencode_escapes_special_chars() {
        assert_eq!(urlencode("dan"), "dan");
        assert_eq!(urlencode("file with space"), "file%20with%20space");
        assert_eq!(urlencode("foo/bar"), "foo%2Fbar");
    }

    #[test]
    fn delete_missing_file_is_ok() {
        let dir = tempfile::tempdir().unwrap();
        let r = delete_file(dir.path(), "nope.txt");
        assert!(r.is_ok());
    }
}
