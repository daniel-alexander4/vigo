// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

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
/// Preserves the original file's permissions when updating.
pub fn atomic_write(path: &str, new_content: &str) -> Result<bool, String> {
    let target = Path::new(path);

    // Read existing content for comparison.
    let existing = fs::read_to_string(target).ok();
    if existing.as_deref() == Some(new_content) {
        return Ok(false);
    }

    // Capture permissions before writing.
    #[cfg(unix)]
    let orig_perms = target.metadata().ok().map(|m| m.permissions());

    // Write to a temp file in the same directory (same filesystem for rename).
    let parent = target.parent().unwrap_or(Path::new("/tmp"));
    let tmp_path = parent.join(format!(
        ".vigo-tmp-{}-{}",
        std::process::id(),
        uuid::Uuid::new_v4().as_simple()
    ));

    {
        let mut f = fs::File::create(&tmp_path)
            .map_err(|e| format!("create temp file {:?}: {}", tmp_path, e))?;
        f.write_all(new_content.as_bytes())
            .map_err(|e| format!("write temp file {:?}: {}", tmp_path, e))?;
        f.sync_all()
            .map_err(|e| format!("sync temp file {:?}: {}", tmp_path, e))?;
    }

    // Restore original permissions on the temp file before rename.
    #[cfg(unix)]
    if let Some(perms) = orig_perms {
        let _ = fs::set_permissions(&tmp_path, perms);
    }

    // Atomic rename.
    fs::rename(&tmp_path, target)
        .map_err(|e| format!("rename {:?} → {}: {}", tmp_path, path, e))?;

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
