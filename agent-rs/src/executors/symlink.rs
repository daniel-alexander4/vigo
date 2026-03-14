// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0

//! Symlink executor — manages symbolic and hard links idempotently.

use super::file::{lookup_gid, lookup_uid};
use super::{Action, Executor, ResourceResult};
use std::collections::HashMap;
use std::fs;
use std::path::Path;
use std::time::Instant;

/// Create a symbolic link (cross-platform).
#[cfg(unix)]
fn create_symlink<P: AsRef<Path>, Q: AsRef<Path>>(target: P, link: Q) -> std::io::Result<()> {
    std::os::unix::fs::symlink(target, link)
}

#[cfg(windows)]
fn create_symlink<P: AsRef<Path>, Q: AsRef<Path>>(target: P, link: Q) -> std::io::Result<()> {
    let target_ref = target.as_ref();
    if target_ref.is_dir() {
        std::os::windows::fs::symlink_dir(target, link)
    } else {
        std::os::windows::fs::symlink_file(target, link)
    }
}

pub struct SymlinkExecutor;

impl SymlinkExecutor {
    pub fn new() -> Self {
        Self
    }
}

impl Executor for SymlinkExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let path_str = params.get("targetpath").map(|s| s.as_str()).unwrap_or("");
        let mut result = ResourceResult::new("symlink", path_str);

        let res = execute_symlink(params, dry_run, &mut result);
        result.duration_ms = start.elapsed().as_millis() as i32;
        if let Err(e) = res {
            result.error = Some(e);
        }
        result
    }
}

fn execute_symlink(
    params: &HashMap<String, String>,
    dry_run: bool,
    result: &mut ResourceResult,
) -> Result<(), String> {
    let path = params
        .get("targetpath")
        .filter(|s| !s.is_empty())
        .ok_or_else(|| "symlink executor: targetpath param is required".to_string())?;

    let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");

    if state == "absent" {
        return handle_absent(path, dry_run, result);
    }

    let target = params
        .get("to")
        .filter(|s| !s.is_empty())
        .ok_or_else(|| "symlink executor: \"to\" param is required".to_string())?;

    let hard = matches!(
        params.get("link_type").map(|s| s.as_str()),
        Some("hard")
    );
    let force = params.get("force").map(|s| s.as_str()) == Some("true");

    let desired_uid = params.get("owner").and_then(|o| lookup_uid(o));
    let desired_gid = params.get("group").and_then(|g| lookup_gid(g));

    let p = Path::new(path);

    if hard {
        handle_hard_link(p, target, force, dry_run, result)
    } else {
        handle_soft_link(p, target, force, dry_run, result, desired_uid, desired_gid)
    }
}

fn handle_soft_link(
    path: &Path,
    target: &str,
    force: bool,
    dry_run: bool,
    result: &mut ResourceResult,
    desired_uid: Option<u32>,
    desired_gid: Option<u32>,
) -> Result<(), String> {
    let path_str = path.display().to_string();

    // Check if a symlink already exists pointing to the right target.
    if path.is_symlink() {
        let current = fs::read_link(path)
            .map_err(|e| format!("readlink {}: {}", path_str, e))?;

        if current == Path::new(target) {
            // Link exists and points to correct target — check ownership.
            let ownership_match = check_symlink_ownership(&path_str, desired_uid, desired_gid);
            if ownership_match {
                result.action = Action::None;
                result.changed = false;
                return Ok(());
            }

            result.action = Action::Updated;
            result.changed = true;
            if dry_run {
                return Ok(());
            }
            apply_symlink_ownership(&path_str, desired_uid, desired_gid)?;
            return Ok(());
        }

        // Symlink exists but points elsewhere — replace it.
        result.action = Action::Updated;
        result.changed = true;
        if dry_run {
            return Ok(());
        }
        fs::remove_file(path)
            .map_err(|e| format!("remove old symlink {}: {}", path_str, e))?;
        create_symlink(target, path)
            .map_err(|e| format!("symlink {} -> {}: {}", path_str, target, e))?;
        apply_symlink_ownership(&path_str, desired_uid, desired_gid)?;
        return Ok(());
    }

    // Something else exists at path.
    if path.exists() || path.is_symlink() {
        if !force {
            return Err(format!(
                "{} exists and is not a symlink (use force: true to replace)",
                path_str,
            ));
        }
        result.action = Action::Updated;
        result.changed = true;
        if dry_run {
            return Ok(());
        }
        if path.is_dir() {
            fs::remove_dir_all(path)
                .map_err(|e| format!("remove {}: {}", path_str, e))?;
        } else {
            fs::remove_file(path)
                .map_err(|e| format!("remove {}: {}", path_str, e))?;
        }
        create_symlink(target, path)
            .map_err(|e| format!("symlink {} -> {}: {}", path_str, target, e))?;
        apply_symlink_ownership(&path_str, desired_uid, desired_gid)?;
        return Ok(());
    }

    // Nothing exists — create.
    result.action = Action::Created;
    result.changed = true;
    if dry_run {
        return Ok(());
    }

    // Ensure parent directory exists.
    if let Some(parent) = path.parent() {
        if !parent.exists() {
            fs::create_dir_all(parent)
                .map_err(|e| format!("create parent dir {}: {}", parent.display(), e))?;
        }
    }

    create_symlink(target, path)
        .map_err(|e| format!("symlink {} -> {}: {}", path_str, target, e))?;
    apply_symlink_ownership(&path_str, desired_uid, desired_gid)?;
    Ok(())
}

fn handle_hard_link(
    path: &Path,
    target: &str,
    force: bool,
    dry_run: bool,
    result: &mut ResourceResult,
) -> Result<(), String> {
    let path_str = path.display().to_string();
    let target_path = Path::new(target);

    // Hard link target must exist.
    if !target_path.exists() {
        return Err(format!(
            "hard link target {} does not exist",
            target,
        ));
    }

    // Check if path already exists and is a hard link to target.
    if path.exists() && !path.is_symlink() {
        if same_inode(path, target_path) {
            result.action = Action::None;
            result.changed = false;
            return Ok(());
        }

        // Exists but different inode.
        if !force {
            return Err(format!(
                "{} exists but is not a hard link to {} (use force: true to replace)",
                path_str, target,
            ));
        }

        result.action = Action::Updated;
        result.changed = true;
        if dry_run {
            return Ok(());
        }
        fs::remove_file(path)
            .map_err(|e| format!("remove {}: {}", path_str, e))?;
        fs::hard_link(target_path, path)
            .map_err(|e| format!("hard link {} -> {}: {}", path_str, target, e))?;
        return Ok(());
    }

    // Symlink at path — need force to replace.
    if path.is_symlink() {
        if !force {
            return Err(format!(
                "{} exists as a symlink (use force: true to replace)",
                path_str,
            ));
        }
        result.action = Action::Updated;
        result.changed = true;
        if dry_run {
            return Ok(());
        }
        fs::remove_file(path)
            .map_err(|e| format!("remove symlink {}: {}", path_str, e))?;
        fs::hard_link(target_path, path)
            .map_err(|e| format!("hard link {} -> {}: {}", path_str, target, e))?;
        return Ok(());
    }

    // Nothing exists — create.
    result.action = Action::Created;
    result.changed = true;
    if dry_run {
        return Ok(());
    }

    if let Some(parent) = path.parent() {
        if !parent.exists() {
            fs::create_dir_all(parent)
                .map_err(|e| format!("create parent dir {}: {}", parent.display(), e))?;
        }
    }

    fs::hard_link(target_path, path)
        .map_err(|e| format!("hard link {} -> {}: {}", path_str, target, e))?;
    Ok(())
}

fn handle_absent(path: &str, dry_run: bool, result: &mut ResourceResult) -> Result<(), String> {
    let p = Path::new(path);
    // Check symlink first (broken symlinks don't pass .exists()).
    if !p.exists() && !p.is_symlink() {
        result.action = Action::None;
        result.changed = false;
        return Ok(());
    }

    result.action = Action::Deleted;
    result.changed = true;
    if dry_run {
        return Ok(());
    }

    // remove_file works for both symlinks and hard links.
    fs::remove_file(p).map_err(|e| format!("remove {}: {}", path, e))?;
    Ok(())
}

/// Check if two paths share the same inode (i.e., are hard links to each other).
#[cfg(unix)]
fn same_inode(a: &Path, b: &Path) -> bool {
    use std::os::unix::fs::MetadataExt;
    let ma = match fs::metadata(a) {
        Ok(m) => m,
        Err(_) => return false,
    };
    let mb = match fs::metadata(b) {
        Ok(m) => m,
        Err(_) => return false,
    };
    ma.dev() == mb.dev() && ma.ino() == mb.ino()
}

#[cfg(not(unix))]
fn same_inode(_a: &Path, _b: &Path) -> bool {
    // No inode concept on Windows; report mismatch to force re-link.
    false
}

/// Check symlink ownership using lstat (doesn't follow the link).
#[cfg(unix)]
fn check_symlink_ownership(path: &str, desired_uid: Option<u32>, desired_gid: Option<u32>) -> bool {
    use std::os::unix::fs::MetadataExt;
    let meta = match fs::symlink_metadata(path) {
        Ok(m) => m,
        Err(_) => return true,
    };
    if let Some(uid) = desired_uid {
        if meta.uid() != uid {
            return false;
        }
    }
    if let Some(gid) = desired_gid {
        if meta.gid() != gid {
            return false;
        }
    }
    true
}

#[cfg(not(unix))]
fn check_symlink_ownership(_path: &str, _uid: Option<u32>, _gid: Option<u32>) -> bool {
    true // No ownership concept on Windows
}

/// Apply ownership to a symlink (lchown — doesn't follow the link).
#[cfg(unix)]
fn apply_symlink_ownership(
    path: &str,
    uid: Option<u32>,
    gid: Option<u32>,
) -> Result<(), String> {
    if uid.is_none() && gid.is_none() {
        return Ok(());
    }
    use std::ffi::CString;
    let c_path = CString::new(path).map_err(|e| format!("invalid path {}: {}", path, e))?;
    let owner = uid.map(|u| u as libc::uid_t).unwrap_or(u32::MAX);
    let group = gid.map(|g| g as libc::gid_t).unwrap_or(u32::MAX);
    // lchown changes the symlink itself, not the target.
    // -1 (u32::MAX) means "don't change".
    let rc = unsafe { libc::lchown(c_path.as_ptr(), owner, group) };
    if rc != 0 {
        return Err(format!(
            "lchown {}: {}",
            path,
            std::io::Error::last_os_error()
        ));
    }
    Ok(())
}

#[cfg(not(unix))]
fn apply_symlink_ownership(
    _path: &str,
    _uid: Option<u32>,
    _gid: Option<u32>,
) -> Result<(), String> {
    Ok(()) // No ownership concept on Windows
}

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::TempDir;

    fn make_params(pairs: &[(&str, &str)]) -> HashMap<String, String> {
        pairs
            .iter()
            .map(|(k, v)| (k.to_string(), v.to_string()))
            .collect()
    }

    // --- Soft links ---

    #[test]
    fn creates_symlink() {
        let dir = TempDir::new().unwrap();
        let target = dir.path().join("real_file");
        fs::write(&target, "data").unwrap();
        let link = dir.path().join("link");

        let se = SymlinkExecutor::new();
        let result = se.execute(
            &make_params(&[
                ("targetpath", link.to_str().unwrap()),
                ("to", target.to_str().unwrap()),
            ]),
            false,
        );

        assert_eq!(result.action, Action::Created);
        assert!(result.changed);
        assert!(result.error.is_none());
        assert!(link.is_symlink());
        assert_eq!(fs::read_link(&link).unwrap(), target);
    }

    #[test]
    fn symlink_no_change() {
        let dir = TempDir::new().unwrap();
        let target = dir.path().join("real_file");
        fs::write(&target, "data").unwrap();
        let link = dir.path().join("link");
        create_symlink(&target, &link).unwrap();

        let se = SymlinkExecutor::new();
        let result = se.execute(
            &make_params(&[
                ("targetpath", link.to_str().unwrap()),
                ("to", target.to_str().unwrap()),
            ]),
            false,
        );

        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
    }

    #[test]
    fn symlink_updates_target() {
        let dir = TempDir::new().unwrap();
        let old_target = dir.path().join("old");
        let new_target = dir.path().join("new");
        fs::write(&old_target, "old").unwrap();
        fs::write(&new_target, "new").unwrap();
        let link = dir.path().join("link");
        create_symlink(&old_target, &link).unwrap();

        let se = SymlinkExecutor::new();
        let result = se.execute(
            &make_params(&[
                ("targetpath", link.to_str().unwrap()),
                ("to", new_target.to_str().unwrap()),
            ]),
            false,
        );

        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);
        assert_eq!(fs::read_link(&link).unwrap(), new_target);
    }

    #[test]
    fn symlink_force_replaces_file() {
        let dir = TempDir::new().unwrap();
        let target = dir.path().join("real");
        fs::write(&target, "data").unwrap();
        let link_path = dir.path().join("link");
        fs::write(&link_path, "i am a regular file").unwrap();

        let se = SymlinkExecutor::new();
        let result = se.execute(
            &make_params(&[
                ("targetpath", link_path.to_str().unwrap()),
                ("to", target.to_str().unwrap()),
                ("force", "true"),
            ]),
            false,
        );

        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);
        assert!(link_path.is_symlink());
        assert_eq!(fs::read_link(&link_path).unwrap(), target);
    }

    #[test]
    fn symlink_no_force_errors_on_existing_file() {
        let dir = TempDir::new().unwrap();
        let target = dir.path().join("real");
        fs::write(&target, "data").unwrap();
        let link_path = dir.path().join("link");
        fs::write(&link_path, "i am a regular file").unwrap();

        let se = SymlinkExecutor::new();
        let result = se.execute(
            &make_params(&[
                ("targetpath", link_path.to_str().unwrap()),
                ("to", target.to_str().unwrap()),
            ]),
            false,
        );

        assert!(result.error.is_some());
        assert!(result.error.as_ref().unwrap().contains("force"));
    }

    #[test]
    fn symlink_creates_parent_dirs() {
        let dir = TempDir::new().unwrap();
        let target = dir.path().join("real");
        fs::write(&target, "data").unwrap();
        let link = dir.path().join("sub/dir/link");

        let se = SymlinkExecutor::new();
        let result = se.execute(
            &make_params(&[
                ("targetpath", link.to_str().unwrap()),
                ("to", target.to_str().unwrap()),
            ]),
            false,
        );

        assert_eq!(result.action, Action::Created);
        assert!(link.is_symlink());
    }

    #[test]
    fn symlink_relative_target() {
        let dir = TempDir::new().unwrap();
        let target = dir.path().join("sites-available/mysite");
        fs::create_dir_all(target.parent().unwrap()).unwrap();
        fs::write(&target, "server {}").unwrap();

        let link = dir.path().join("sites-enabled/mysite");
        fs::create_dir_all(link.parent().unwrap()).unwrap();

        let se = SymlinkExecutor::new();
        let result = se.execute(
            &make_params(&[
                ("targetpath", link.to_str().unwrap()),
                ("to", "../sites-available/mysite"),
            ]),
            false,
        );

        assert_eq!(result.action, Action::Created);
        assert!(result.changed);
        assert!(link.is_symlink());
        assert_eq!(
            fs::read_link(&link).unwrap(),
            Path::new("../sites-available/mysite")
        );
    }

    #[test]
    fn symlink_absent() {
        let dir = TempDir::new().unwrap();
        let target = dir.path().join("real");
        fs::write(&target, "data").unwrap();
        let link = dir.path().join("link");
        create_symlink(&target, &link).unwrap();

        let se = SymlinkExecutor::new();
        let result = se.execute(
            &make_params(&[
                ("targetpath", link.to_str().unwrap()),
                ("state", "absent"),
            ]),
            false,
        );

        assert_eq!(result.action, Action::Deleted);
        assert!(result.changed);
        assert!(!link.exists());
        // Original target untouched.
        assert!(target.exists());
    }

    #[test]
    fn symlink_absent_nonexistent() {
        let dir = TempDir::new().unwrap();
        let link = dir.path().join("noexist");

        let se = SymlinkExecutor::new();
        let result = se.execute(
            &make_params(&[
                ("targetpath", link.to_str().unwrap()),
                ("state", "absent"),
            ]),
            false,
        );

        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
    }

    #[test]
    fn symlink_absent_broken_link() {
        let dir = TempDir::new().unwrap();
        let link = dir.path().join("broken");
        create_symlink("/nonexistent/target", &link).unwrap();

        let se = SymlinkExecutor::new();
        let result = se.execute(
            &make_params(&[
                ("targetpath", link.to_str().unwrap()),
                ("state", "absent"),
            ]),
            false,
        );

        assert_eq!(result.action, Action::Deleted);
        assert!(result.changed);
        assert!(!link.is_symlink());
    }

    #[test]
    fn symlink_dry_run() {
        let dir = TempDir::new().unwrap();
        let target = dir.path().join("real");
        fs::write(&target, "data").unwrap();
        let link = dir.path().join("link");

        let se = SymlinkExecutor::new();
        let result = se.execute(
            &make_params(&[
                ("targetpath", link.to_str().unwrap()),
                ("to", target.to_str().unwrap()),
            ]),
            true,
        );

        assert_eq!(result.action, Action::Created);
        assert!(result.changed);
        assert!(!link.exists());
    }

    // --- Hard links ---

    #[test]
    fn creates_hard_link() {
        let dir = TempDir::new().unwrap();
        let target = dir.path().join("original");
        fs::write(&target, "data").unwrap();
        let link = dir.path().join("hardlink");

        let se = SymlinkExecutor::new();
        let result = se.execute(
            &make_params(&[
                ("targetpath", link.to_str().unwrap()),
                ("to", target.to_str().unwrap()),
                ("link_type", "hard"),
            ]),
            false,
        );

        assert_eq!(result.action, Action::Created);
        assert!(result.changed);
        assert!(result.error.is_none());
        assert!(same_inode(&link, &target));
    }

    #[test]
    fn hard_link_no_change() {
        let dir = TempDir::new().unwrap();
        let target = dir.path().join("original");
        fs::write(&target, "data").unwrap();
        let link = dir.path().join("hardlink");
        fs::hard_link(&target, &link).unwrap();

        let se = SymlinkExecutor::new();
        let result = se.execute(
            &make_params(&[
                ("targetpath", link.to_str().unwrap()),
                ("to", target.to_str().unwrap()),
                ("link_type", "hard"),
            ]),
            false,
        );

        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
    }

    #[test]
    fn hard_link_target_must_exist() {
        let dir = TempDir::new().unwrap();
        let link = dir.path().join("hardlink");

        let se = SymlinkExecutor::new();
        let result = se.execute(
            &make_params(&[
                ("targetpath", link.to_str().unwrap()),
                ("to", "/nonexistent/file"),
                ("link_type", "hard"),
            ]),
            false,
        );

        assert!(result.error.is_some());
        assert!(result.error.as_ref().unwrap().contains("does not exist"));
    }

    #[test]
    fn hard_link_force_replaces() {
        let dir = TempDir::new().unwrap();
        let target = dir.path().join("original");
        fs::write(&target, "data").unwrap();
        let link = dir.path().join("hardlink");
        fs::write(&link, "different file").unwrap();

        let se = SymlinkExecutor::new();
        let result = se.execute(
            &make_params(&[
                ("targetpath", link.to_str().unwrap()),
                ("to", target.to_str().unwrap()),
                ("link_type", "hard"),
                ("force", "true"),
            ]),
            false,
        );

        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);
        assert!(same_inode(&link, &target));
    }

    #[test]
    fn hard_link_no_force_errors() {
        let dir = TempDir::new().unwrap();
        let target = dir.path().join("original");
        fs::write(&target, "data").unwrap();
        let link = dir.path().join("hardlink");
        fs::write(&link, "different").unwrap();

        let se = SymlinkExecutor::new();
        let result = se.execute(
            &make_params(&[
                ("targetpath", link.to_str().unwrap()),
                ("to", target.to_str().unwrap()),
                ("link_type", "hard"),
            ]),
            false,
        );

        assert!(result.error.is_some());
        assert!(result.error.as_ref().unwrap().contains("force"));
    }

    #[test]
    fn missing_to_errors() {
        let se = SymlinkExecutor::new();
        let result = se.execute(
            &make_params(&[("targetpath", "/some/link")]),
            false,
        );
        assert!(result.error.is_some());
        assert!(result.error.as_ref().unwrap().contains("\"to\""));
    }

    #[test]
    fn missing_targetpath_errors() {
        let se = SymlinkExecutor::new();
        let result = se.execute(&make_params(&[("to", "/some/target")]), false);
        assert!(result.error.is_some());
        assert!(result.error.as_ref().unwrap().contains("targetpath"));
    }
}
