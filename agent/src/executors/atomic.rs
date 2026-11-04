// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Atomic file writing — read→modify in memory→write to temp→compare→rename.
//!
//! Prevents partial edits and avoids unnecessary mtime changes when content is
//! identical. Preserves original file permissions.

use std::fs;
use std::io::Write;
use std::path::Path;

/// Atomically write `new_content` to `path`.
///
/// Returns `Ok(true)` if the file was changed, `Ok(false)` if content was
/// identical (no disk write performed). Creates the file if it doesn't exist.
/// When updating, the replacement keeps the original file's mode **and its
/// owner and group** — an edit to an app-owned config (`postgres:postgres
/// 0640`) must not turn it into `root:root`. The temp is created
/// `O_EXCL|O_NOFOLLOW` at 0600 and gets its final owner/mode on the
/// descriptor before the rename, so it is never readable by another user
/// mid-write and never a planted symlink.
pub fn atomic_write(path: &str, new_content: &str) -> Result<bool, String> {
    let target = Path::new(path);

    // Read existing content for comparison.
    let existing = fs::read_to_string(target).ok();
    if existing.as_deref() == Some(new_content) {
        return Ok(false);
    }

    // Capture mode + ownership before writing.
    #[cfg(unix)]
    let orig = {
        use std::os::unix::fs::MetadataExt;
        target.metadata().ok().map(|m| (m.mode() & 0o7777, m.uid(), m.gid()))
    };

    // Write to a temp file in the same directory (same filesystem for rename).
    let parent = target.parent().unwrap_or(Path::new("/tmp"));
    let tmp_path = parent.join(format!(
        ".vigo-tmp-{}-{}",
        std::process::id(),
        uuid::Uuid::new_v4().as_simple()
    ));

    let staged = (|| -> Result<(), String> {
        let mut f = super::file::create_private_temp(&tmp_path)
            .map_err(|e| format!("create temp file {:?}: {}", tmp_path, e))?;
        f.write_all(new_content.as_bytes())
            .map_err(|e| format!("write temp file {:?}: {}", tmp_path, e))?;
        // Restore the original owner, group and mode on the temp's
        // descriptor. A brand-new file gets the conventional 0644.
        #[cfg(unix)]
        match orig {
            Some((mode, uid, gid)) => {
                super::file::set_owner_mode_fd(&f, Some(uid), Some(gid), mode)?
            }
            None => super::file::set_owner_mode_fd(&f, None, None, 0o644)?,
        }
        f.sync_all()
            .map_err(|e| format!("sync temp file {:?}: {}", tmp_path, e))
    })();
    if let Err(e) = staged {
        let _ = fs::remove_file(&tmp_path);
        return Err(e);
    }

    // Atomic rename.
    fs::rename(&tmp_path, target).map_err(|e| {
        let _ = fs::remove_file(&tmp_path);
        format!("rename {:?} → {}: {}", tmp_path, path, e)
    })?;

    Ok(true)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn creates_new_file() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("new.txt");
        let p = path.to_str().unwrap();

        let changed = atomic_write(p, "hello\n").unwrap();
        assert!(changed);
        assert_eq!(fs::read_to_string(&path).unwrap(), "hello\n");
    }

    #[test]
    fn updates_changed_content() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("test.txt");
        fs::write(&path, "old content\n").unwrap();

        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            fs::set_permissions(&path, fs::Permissions::from_mode(0o600)).unwrap();
        }

        let changed = atomic_write(path.to_str().unwrap(), "new content\n").unwrap();
        assert!(changed);
        assert_eq!(fs::read_to_string(&path).unwrap(), "new content\n");

        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let mode = fs::metadata(&path).unwrap().permissions().mode() & 0o777;
            assert_eq!(mode, 0o600, "permissions should be preserved");
        }
    }

    /// A group the test user belongs to other than its primary one, if any —
    /// lets a non-root test observe ownership being carried over.
    #[cfg(unix)]
    fn secondary_gid() -> Option<u32> {
        let primary = nix::unistd::getgid();
        nix::unistd::getgroups()
            .ok()?
            .into_iter()
            .find(|g| *g != primary)
            .map(|g| g.as_raw())
    }

    #[cfg(unix)]
    #[test]
    fn preserves_owner_and_group() {
        use std::os::unix::fs::MetadataExt;
        let Some(gid) = secondary_gid() else { return };
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("app.conf");
        fs::write(&path, "a=1\n").unwrap();
        nix::unistd::chown(&path, None, Some(nix::unistd::Gid::from_raw(gid))).unwrap();
        {
            use std::os::unix::fs::PermissionsExt;
            fs::set_permissions(&path, fs::Permissions::from_mode(0o640)).unwrap();
        }
        assert!(atomic_write(path.to_str().unwrap(), "a=2\n").unwrap());
        let m = fs::metadata(&path).unwrap();
        assert_eq!(m.gid(), gid, "group must survive the replace");
        assert_eq!(m.uid(), nix::unistd::getuid().as_raw());
        assert_eq!(m.mode() & 0o7777, 0o640);
    }

    #[cfg(unix)]
    #[test]
    fn new_file_is_0644_and_temp_is_private() {
        use std::os::unix::fs::PermissionsExt;
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("fresh.conf");
        assert!(atomic_write(path.to_str().unwrap(), "x\n").unwrap());
        assert_eq!(fs::metadata(&path).unwrap().permissions().mode() & 0o777, 0o644);
        // The temp primitive itself: exclusive, no-follow, 0600.
        let tmp = dir.path().join(".t");
        let f = super::super::file::create_private_temp(&tmp).unwrap();
        assert_eq!(f.metadata().unwrap().permissions().mode() & 0o777, 0o600);
        let outside = dir.path().join("outside");
        fs::write(&outside, "keep").unwrap();
        std::os::unix::fs::symlink(&outside, dir.path().join(".l")).unwrap();
        assert!(super::super::file::create_private_temp(&dir.path().join(".l")).is_err());
        assert_eq!(fs::read_to_string(&outside).unwrap(), "keep");
    }

    #[test]
    fn noop_identical_content() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("test.txt");
        let content = "same content\n";
        fs::write(&path, content).unwrap();

        let changed = atomic_write(path.to_str().unwrap(), content).unwrap();
        assert!(!changed, "identical content should return false");
    }
}
