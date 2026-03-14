// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0

//! Cross-platform file permission abstractions.

use std::path::Path;

/// Set a file to be readable/writable only by the owner (0600 on Unix).
/// On Windows, this is a best-effort no-op — NTFS ACLs are not modified.
pub fn set_owner_only(path: &Path) -> std::io::Result<()> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(path, std::fs::Permissions::from_mode(0o600))
    }
    #[cfg(not(unix))]
    {
        let _ = path;
        Ok(())
    }
}

/// Set a file to be executable (0755 on Unix).
/// On Windows, executability is determined by file extension, so this is a no-op.
pub fn set_executable(path: &Path) -> std::io::Result<()> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(path, std::fs::Permissions::from_mode(0o755))
    }
    #[cfg(not(unix))]
    {
        let _ = path;
        Ok(())
    }
}

/// Set a directory to be owner-only accessible (0700 on Unix).
/// On Windows, this is a best-effort no-op.
pub fn set_dir_owner_only(path: &Path) -> std::io::Result<()> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(path, std::fs::Permissions::from_mode(0o700))
    }
    #[cfg(not(unix))]
    {
        let _ = path;
        Ok(())
    }
}

/// Set a file to a specific numeric mode (Unix octal).
/// On Windows, this is a no-op.
pub fn set_mode(path: &Path, mode: u32) -> std::io::Result<()> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(path, std::fs::Permissions::from_mode(mode))
    }
    #[cfg(not(unix))]
    {
        let _ = (path, mode);
        Ok(())
    }
}

/// Set a file to be world-readable (0644 on Unix).
/// On Windows, this is a no-op.
pub fn set_world_readable(path: &Path) -> std::io::Result<()> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(path, std::fs::Permissions::from_mode(0o644))
    }
    #[cfg(not(unix))]
    {
        let _ = path;
        Ok(())
    }
}

/// Check if a file is executable.
/// On Unix, checks the executable bits. On Windows, checks the file extension.
pub fn is_executable(path: &Path) -> bool {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        match path.metadata() {
            Ok(meta) => meta.permissions().mode() & 0o111 != 0,
            Err(_) => false,
        }
    }
    #[cfg(not(unix))]
    {
        const EXECUTABLE_EXTENSIONS: &[&str] = &["exe", "ps1", "bat", "cmd"];
        path.extension()
            .and_then(|ext| ext.to_str())
            .map(|ext| EXECUTABLE_EXTENSIONS.iter().any(|e| e.eq_ignore_ascii_case(ext)))
            .unwrap_or(false)
    }
}

/// Get the numeric mode of a file (Unix only). Returns 0 on non-Unix.
pub fn get_mode(path: &Path) -> std::io::Result<u32> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let meta = std::fs::metadata(path)?;
        Ok(meta.permissions().mode() & 0o7777)
    }
    #[cfg(not(unix))]
    {
        let _ = path;
        Ok(0)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;

    #[test]
    fn test_set_owner_only() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("secret.key");
        fs::write(&path, "secret").unwrap();
        set_owner_only(&path).unwrap();

        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let mode = fs::metadata(&path).unwrap().permissions().mode() & 0o777;
            assert_eq!(mode, 0o600);
        }
    }

    #[test]
    fn test_set_executable() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("script.sh");
        fs::write(&path, "#!/bin/sh").unwrap();
        set_executable(&path).unwrap();

        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let mode = fs::metadata(&path).unwrap().permissions().mode() & 0o777;
            assert_eq!(mode, 0o755);
        }
    }

    #[test]
    fn test_is_executable() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("script.sh");
        fs::write(&path, "#!/bin/sh").unwrap();

        #[cfg(unix)]
        {
            // Not executable by default.
            assert!(!is_executable(&path));
            set_executable(&path).unwrap();
            assert!(is_executable(&path));
        }
    }

    #[test]
    fn test_set_mode() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("file.txt");
        fs::write(&path, "content").unwrap();
        set_mode(&path, 0o644).unwrap();

        #[cfg(unix)]
        {
            let mode = get_mode(&path).unwrap();
            assert_eq!(mode, 0o644);
        }
    }
}
