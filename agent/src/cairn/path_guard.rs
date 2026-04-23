// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Path canonicalization and containment for cairn's scoped
//! `~<user>/cairn/<project>/` directory layout.
//!
//! Every source path is canonicalized before use and re-checked
//! inside the setuid child just before `git bundle create` runs
//! (TOCTOU mitigation). Project names are validated for safety
//! before being used as label segments, filesystem components, or
//! swarm blob identifiers.

use std::path::{Path, PathBuf};
use thiserror::Error;

/// Maximum length of a project name (the subdirectory under
/// `~<user>/cairn/`) in bytes.
pub const MAX_PROJECT_NAME_LEN: usize = 64;

#[derive(Debug, Error)]
pub enum PathError {
    #[error("path does not exist: {0}")]
    NotFound(String),
    #[error("path cannot be canonicalized: {path} ({source})")]
    Canonicalize {
        path: String,
        #[source]
        source: std::io::Error,
    },
    #[error("path {path} escapes expected root {root}")]
    Escape { path: String, root: String },
    #[error("project name is invalid: {0:?}")]
    InvalidProjectName(String),
}

/// Validate that a project name is safe to use as a label segment
/// and filesystem component.
///
/// Rejects empty, `.`, `..`, dotfiles, path separators, null bytes,
/// control characters, the [`LABEL_SEP`][super::LABEL_SEP] character
/// (`:`), and names longer than [`MAX_PROJECT_NAME_LEN`] bytes.
///
/// `:` is forbidden because cairn swarm labels are
/// `cairn:<host>:<user>:<project>` and the label is also the seed
/// filename in `default_seeds_dir()`. Allowing `:` inside any
/// segment would make labels ambiguous to parse on the server-side
/// selector.
pub fn validate_project_name(name: &str) -> Result<(), PathError> {
    if name.is_empty()
        || name == "."
        || name == ".."
        || name.len() > MAX_PROJECT_NAME_LEN
        || name.starts_with('.')
    {
        return Err(PathError::InvalidProjectName(name.to_string()));
    }
    for c in name.chars() {
        if c == '/' || c == '\\' || c == '\0' || c == super::LABEL_SEP || c.is_control() {
            return Err(PathError::InvalidProjectName(name.to_string()));
        }
    }
    Ok(())
}

/// Canonicalize a source path and verify it lives under
/// `<user_home>/cairn/`. Returns the canonical path on success.
///
/// Callers should call this both at scan time (initial containment
/// check) and inside the setuid child just before bundling (TOCTOU
/// re-check).
pub fn check_source_path(path: &Path, user_home: &Path) -> Result<PathBuf, PathError> {
    let canon = path.canonicalize().map_err(|e| {
        if e.kind() == std::io::ErrorKind::NotFound {
            PathError::NotFound(path.display().to_string())
        } else {
            PathError::Canonicalize {
                path: path.display().to_string(),
                source: e,
            }
        }
    })?;

    let cairn_root = user_home.join("cairn");
    let canon_root = cairn_root
        .canonicalize()
        .map_err(|e| PathError::Canonicalize {
            path: cairn_root.display().to_string(),
            source: e,
        })?;

    if !canon.starts_with(&canon_root) {
        return Err(PathError::Escape {
            path: canon.display().to_string(),
            root: canon_root.display().to_string(),
        });
    }

    Ok(canon)
}

/// Derive the receiver-side working tree path from a user's home
/// directory and a validated project name, and verify it stays
/// under `<user_home>/cairn/`.
///
/// The target directory may not exist yet (first materialization);
/// this function does not require it to. It *does* require
/// `<user_home>/cairn/` itself to exist or be creatable, so the
/// root can be canonicalized.
pub fn check_receiver_path(user_home: &Path, project: &str) -> Result<PathBuf, PathError> {
    validate_project_name(project)?;

    let cairn_root = user_home.join("cairn");
    std::fs::create_dir_all(&cairn_root).map_err(|e| PathError::Canonicalize {
        path: cairn_root.display().to_string(),
        source: e,
    })?;
    let canon_root = cairn_root
        .canonicalize()
        .map_err(|e| PathError::Canonicalize {
            path: cairn_root.display().to_string(),
            source: e,
        })?;

    let target = canon_root.join(project);
    // If target exists, canonicalize it to catch symlink escapes.
    // Otherwise the joined path (with an already-canonical root) is
    // safe by construction because project has been validated not to
    // contain path separators.
    let final_path = match target.canonicalize() {
        Ok(p) => p,
        Err(_) => target,
    };

    if !final_path.starts_with(&canon_root) {
        return Err(PathError::Escape {
            path: final_path.display().to_string(),
            root: canon_root.display().to_string(),
        });
    }

    Ok(final_path)
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;
    use tempfile::tempdir;

    // --- project name validation ------------------------------------

    #[test]
    fn validates_normal_names() {
        assert!(validate_project_name("myproject").is_ok());
        assert!(validate_project_name("orangutan").is_ok());
        assert!(validate_project_name("my-project").is_ok());
        assert!(validate_project_name("my_project").is_ok());
        assert!(validate_project_name("proj123").is_ok());
    }

    #[test]
    fn rejects_empty() {
        assert!(matches!(
            validate_project_name(""),
            Err(PathError::InvalidProjectName(_))
        ));
    }

    #[test]
    fn rejects_dot_and_dotdot() {
        assert!(matches!(
            validate_project_name("."),
            Err(PathError::InvalidProjectName(_))
        ));
        assert!(matches!(
            validate_project_name(".."),
            Err(PathError::InvalidProjectName(_))
        ));
    }

    #[test]
    fn rejects_dotfiles() {
        assert!(matches!(
            validate_project_name(".hidden"),
            Err(PathError::InvalidProjectName(_))
        ));
        assert!(matches!(
            validate_project_name("..hidden"),
            Err(PathError::InvalidProjectName(_))
        ));
    }

    #[test]
    fn rejects_path_separators() {
        assert!(matches!(
            validate_project_name("foo/bar"),
            Err(PathError::InvalidProjectName(_))
        ));
        assert!(matches!(
            validate_project_name("foo\\bar"),
            Err(PathError::InvalidProjectName(_))
        ));
        assert!(matches!(
            validate_project_name("../etc/passwd"),
            Err(PathError::InvalidProjectName(_))
        ));
    }

    #[test]
    fn rejects_null_byte() {
        assert!(matches!(
            validate_project_name("foo\0bar"),
            Err(PathError::InvalidProjectName(_))
        ));
    }

    #[test]
    fn rejects_label_separator() {
        // `:` is the cairn label separator
        // (cairn:host:user:project) and must not appear inside any
        // segment, or label parsing on the server selector would be
        // ambiguous.
        assert!(matches!(
            validate_project_name("foo:bar"),
            Err(PathError::InvalidProjectName(_))
        ));
        assert!(matches!(
            validate_project_name(":leading"),
            Err(PathError::InvalidProjectName(_))
        ));
        assert!(matches!(
            validate_project_name("trailing:"),
            Err(PathError::InvalidProjectName(_))
        ));
    }

    #[test]
    fn rejects_control_chars() {
        assert!(matches!(
            validate_project_name("foo\nbar"),
            Err(PathError::InvalidProjectName(_))
        ));
        assert!(matches!(
            validate_project_name("foo\tbar"),
            Err(PathError::InvalidProjectName(_))
        ));
    }

    #[test]
    fn rejects_too_long() {
        let long = "x".repeat(MAX_PROJECT_NAME_LEN + 1);
        assert!(matches!(
            validate_project_name(&long),
            Err(PathError::InvalidProjectName(_))
        ));
    }

    #[test]
    fn accepts_exactly_max_len() {
        let max = "x".repeat(MAX_PROJECT_NAME_LEN);
        assert!(validate_project_name(&max).is_ok());
    }

    // --- source path containment ------------------------------------

    #[test]
    fn source_path_accepts_valid() {
        let home = tempdir().unwrap();
        let project = home.path().join("cairn").join("myproject");
        fs::create_dir_all(&project).unwrap();

        let result = check_source_path(&project, home.path());
        assert!(result.is_ok());
    }

    #[test]
    fn source_path_rejects_outside_cairn() {
        let home = tempdir().unwrap();
        let outside = home.path().join("other").join("myproject");
        fs::create_dir_all(&outside).unwrap();
        fs::create_dir_all(home.path().join("cairn")).unwrap();

        let result = check_source_path(&outside, home.path());
        assert!(matches!(result, Err(PathError::Escape { .. })));
    }

    #[test]
    #[cfg(unix)]
    fn source_path_rejects_symlink_escape() {
        let home = tempdir().unwrap();
        fs::create_dir_all(home.path().join("cairn")).unwrap();
        let target = home.path().join("secret");
        fs::create_dir_all(&target).unwrap();

        let link = home.path().join("cairn").join("escape");
        std::os::unix::fs::symlink(&target, &link).unwrap();

        let result = check_source_path(&link, home.path());
        assert!(matches!(result, Err(PathError::Escape { .. })));
    }

    #[test]
    fn source_path_rejects_nonexistent() {
        let home = tempdir().unwrap();
        fs::create_dir_all(home.path().join("cairn")).unwrap();
        let missing = home.path().join("cairn").join("nope");

        let result = check_source_path(&missing, home.path());
        assert!(matches!(result, Err(PathError::NotFound(_))));
    }

    // --- receiver path derivation -----------------------------------

    #[test]
    fn receiver_path_derives_correctly() {
        let home = tempdir().unwrap();
        let result = check_receiver_path(home.path(), "orangutan").unwrap();
        assert!(result.ends_with("cairn/orangutan"));
    }

    #[test]
    fn receiver_path_rejects_bad_project_name() {
        let home = tempdir().unwrap();
        assert!(matches!(
            check_receiver_path(home.path(), "../escape"),
            Err(PathError::InvalidProjectName(_))
        ));
        assert!(matches!(
            check_receiver_path(home.path(), ".."),
            Err(PathError::InvalidProjectName(_))
        ));
        assert!(matches!(
            check_receiver_path(home.path(), ""),
            Err(PathError::InvalidProjectName(_))
        ));
    }

    #[test]
    fn receiver_path_works_when_target_missing() {
        let home = tempdir().unwrap();
        // cairn/ dir doesn't exist yet; function should create it.
        let result = check_receiver_path(home.path(), "fresh-project");
        assert!(result.is_ok());
    }
}
