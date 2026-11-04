// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Apply reconciliation actions: fetch files from peers and write them to
//! the user's ~/longdrawer/ with correct ownership, or delete local files.

use anyhow::{bail, Context, Result};
use sha2::{Digest, Sha256};
use std::fs;
use std::io::{Read, Write};
use std::path::Path;

use super::reconciler::Action;
use super::scanner::effective_max_file_size;
use crate::platform::safe_path::SafeDir;
use crate::platform::users::lookup_user;

/// Chunk size for streaming reads — large enough to amortize syscall cost,
/// small enough not to pressure RAM on receivers.
const STREAM_CHUNK: usize = 256 * 1024;

/// Execute a single action against a user's ~/longdrawer/ directory.
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
    http: &crate::swarm::tls::SwarmAgent,
) -> Result<()> {
    match action {
        Action::Fetch { name, sha256, size, mtime } => {
            fetch_file(http, peer_addr, user, name, sha256, *size, *mtime, sync_dir)
        }
        Action::Delete { name } => delete_file(sync_dir, user, name),
    }
}

/// GET https://<peer>/longdrawer/<user>/<name> and write to sync_dir/name.
///
/// Streams the response body directly to a temp file, hashing and size-checking
/// as we go. Nothing is buffered in RAM beyond `STREAM_CHUNK` bytes. On any
/// mismatch (sha256, size, oversized, I/O error) the temp file is removed and
/// the operation fails without touching the target path.
fn fetch_file(
    http: &crate::swarm::tls::SwarmAgent,
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
        "https://{}/swarm/longdrawer/{}/{}",
        peer_addr,
        urlencode(user),
        urlencode(name)
    );

    let mut resp = http
        .get(&url)
        .send()
        .map_err(|e| anyhow::anyhow!("fetch {} from {} failed: {}", name, peer_addr, e))?;

    if resp.status() != reqwest::StatusCode::OK {
        bail!("fetch {} from {} returned {}", name, peer_addr, resp.status());
    }

    let (dir, owner) = open_sync_dir(sync_dir, user, true)?;
    // Short, fixed-shape temp name (never derived from `name`, so a
    // 255-byte filename can't overflow NAME_MAX) created O_EXCL|O_NOFOLLOW
    // inside the held dir fd.
    let tmp = format!(".longdrawer-{}.tmp", uuid::Uuid::new_v4().as_simple());
    let result = (|| -> Result<()> {
        let mut file = dir
            .create_new(&tmp, 0o600)
            .with_context(|| format!("creating temp in {}", sync_dir.display()))?;
        stream_body_to_file(&mut resp, &mut file, expected_sha256, expected_size, name)?;
        // Owner, mode and mtime go on the descriptor before the rename
        // publishes the file — no path-based chown/utimensat that a
        // swapped-in symlink could redirect.
        dir.finish_file(&file, 0o644, owner, Some(mtime))
            .with_context(|| format!("finalizing temp for {}", name))?;
        drop(file);
        dir.rename(&tmp, name)
            .with_context(|| format!("renaming to {}", sync_dir.join(name).display()))
    })();
    if result.is_err() {
        let _ = dir.unlink(&tmp);
    }
    result
}

/// Open the user's sync dir (`~<user>/longdrawer`) for a root write:
/// the parent (the passwd home) is the anchor, the sync dir itself is opened
/// no-follow and must be owned by the user or root. With `create`, a missing
/// sync dir is made 0700 and handed to the user through its descriptor.
/// Returns the dir and the `(uid, gid)` written files are chowned to
/// (`None` when the user can't be resolved — the file stays root-owned).
fn open_sync_dir(
    sync_dir: &Path,
    user: &str,
    create: bool,
) -> Result<(SafeDir, Option<(u32, u32)>)> {
    let info = lookup_user(user).with_context(|| format!("user {} not found", user))?;
    let parent = sync_dir.parent().context("sync dir has no parent")?;
    let leaf = sync_dir.file_name().context("sync dir has no name")?;
    let anchor = SafeDir::open_anchor(parent)
        .with_context(|| format!("opening {}", parent.display()))?;
    let created = create
        && anchor
            .mkdir(leaf, 0o700)
            .with_context(|| format!("creating {}", sync_dir.display()))?;
    let dir = anchor
        .subdir(leaf)
        .and_then(|d| d.require_owner(info.uid))
        .with_context(|| format!("{} is not a real directory owned by {}", sync_dir.display(), user))?;
    if created || dir.stat_self().map(|s| s.uid != info.uid).unwrap_or(false) {
        dir.chown_self(Some(info.uid), Some(info.gid))
            .with_context(|| format!("chown {} to {}", sync_dir.display(), user))?;
    }
    Ok((dir, Some((info.uid, info.gid))))
}

/// Stream a response body into a file, hashing as we write.
/// Enforces the expected size as an upper bound and rejects sha256 mismatch.
/// The caller fsyncs (via `finish_file`) before rename.
fn stream_body_to_file(
    resp: &mut impl Read,
    file: &mut fs::File,
    expected_sha256: &str,
    expected_size: u64,
    name: &str,
) -> Result<()> {
    // `reqwest::blocking::Response` implements `std::io::Read`, streaming
    // the body off the socket — nothing buffered beyond `STREAM_CHUNK`.
    let reader = resp;
    let mut hasher = Sha256::new();
    let mut buf = vec![0u8; STREAM_CHUNK];
    let mut total: u64 = 0;

    loop {
        let n = reader
            .read(&mut buf)
            .with_context(|| format!("reading longdrawer body for {}", name))?;
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
            .with_context(|| format!("writing temp for {}", name))?;
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
    Ok(())
}

fn delete_file(sync_dir: &Path, user: &str, name: &str) -> Result<()> {
    if !valid_filename(name) {
        bail!("invalid filename: {:?}", name);
    }
    let dir = match open_sync_dir(sync_dir, user, false) {
        Ok((d, _)) => d,
        // No sync dir → nothing to delete.
        Err(e)
            if e.chain().any(|c| {
                c.downcast_ref::<std::io::Error>()
                    .is_some_and(|io| io.kind() == std::io::ErrorKind::NotFound)
            }) =>
        {
            return Ok(())
        }
        Err(e) => return Err(e),
    };
    // unlinkat removes the entry itself — a symlink, never its target.
    match dir.unlink(name) {
        Ok(_) => Ok(()),
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(()),
        Err(e) => Err(e).with_context(|| format!("deleting {}", sync_dir.join(name).display())),
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
        libc::timespec { tv_sec: mtime as _, tv_nsec: 0 },
    ];
    let rc = unsafe {
        libc::utimensat(libc::AT_FDCWD, c_path.as_ptr(), times.as_ptr(), libc::AT_SYMLINK_NOFOLLOW)
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

    fn me() -> String {
        nix::unistd::User::from_uid(nix::unistd::getuid()).unwrap().unwrap().name
    }

    #[test]
    fn delete_missing_file_is_ok() {
        let dir = tempfile::tempdir().unwrap();
        std::fs::create_dir(dir.path().join("longdrawer")).unwrap();
        let r = delete_file(&dir.path().join("longdrawer"), &me(), "nope.txt");
        assert!(r.is_ok());
        // No sync dir at all is also fine.
        assert!(delete_file(&dir.path().join("absent"), &me(), "nope.txt").is_ok());
    }

    #[test]
    fn delete_through_symlinked_sync_dir_refused() {
        // ~/longdrawer -> outside dir: a peer-driven delete of `victim`
        // must not unlink the outside file.
        let outside = tempfile::tempdir().unwrap();
        std::fs::write(outside.path().join("victim"), b"keep").unwrap();
        let home = tempfile::tempdir().unwrap();
        std::os::unix::fs::symlink(outside.path(), home.path().join("longdrawer")).unwrap();
        assert!(delete_file(&home.path().join("longdrawer"), &me(), "victim").is_err());
        assert!(outside.path().join("victim").exists());
    }

    #[test]
    fn write_into_symlinked_sync_dir_refused_and_tmp_never_followed() {
        let outside = tempfile::tempdir().unwrap();
        let home = tempfile::tempdir().unwrap();
        std::os::unix::fs::symlink(outside.path(), home.path().join("longdrawer")).unwrap();
        assert!(open_sync_dir(&home.path().join("longdrawer"), &me(), true).is_err());
        assert_eq!(std::fs::read_dir(outside.path()).unwrap().count(), 0);

        // Real dir: the body lands via a fresh temp with mtime + mode set on
        // the descriptor.
        let home2 = tempfile::tempdir().unwrap();
        let sd = home2.path().join("longdrawer");
        let (dir, owner) = open_sync_dir(&sd, &me(), true).unwrap();
        let body = b"hello".to_vec();
        let sha = hex::encode(Sha256::digest(&body));
        let mut f = dir.create_new(".t.tmp", 0o600).unwrap();
        stream_body_to_file(&mut &body[..], &mut f, &sha, 5, "n").unwrap();
        dir.finish_file(&f, 0o644, owner, Some(1234)).unwrap();
        dir.rename(".t.tmp", "n").unwrap();
        let st = dir.stat("n").unwrap();
        assert_eq!((st.mtime, st.perm()), (1234, 0o644));
        assert_eq!(std::fs::read(sd.join("n")).unwrap(), b"hello");
    }
}
