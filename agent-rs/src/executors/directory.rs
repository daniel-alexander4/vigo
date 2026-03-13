// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Directory executor — manages directories with mode and ownership idempotently.

use super::file::{apply_ownership, check_ownership, lookup_gid, lookup_uid};
use super::{Action, Executor, ResourceResult};
use std::collections::HashMap;
use std::fs;
use std::os::unix::fs::PermissionsExt;
use std::path::Path;
use std::time::Instant;

pub struct DirectoryExecutor;

impl DirectoryExecutor {
    pub fn new() -> Self {
        Self
    }
}

impl Executor for DirectoryExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let path_str = params.get("targetpath").map(|s| s.as_str()).unwrap_or("");
        let mut result = ResourceResult::new("directory", path_str);

        let res = execute_directory(params, dry_run, &mut result);
        result.duration_ms = start.elapsed().as_millis() as i32;
        if let Err(e) = res {
            result.error = Some(e);
        }
        result
    }
}

fn execute_directory(
    params: &HashMap<String, String>,
    dry_run: bool,
    result: &mut ResourceResult,
) -> Result<(), String> {
    let path = params
        .get("targetpath")
        .filter(|s| !s.is_empty())
        .ok_or_else(|| "directory executor: targetpath param is required".to_string())?;

    let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");

    if state == "absent" {
        handle_absent(path, dry_run, result);
        return Ok(());
    }

    let mode_str = params.get("mode").map(|s| s.as_str()).unwrap_or("0755");
    let mode = u32::from_str_radix(mode_str.trim_start_matches('0'), 8)
        .map_err(|e| format!("parse mode {:?}: {}", mode_str, e))?;

    let desired_uid = params.get("owner").and_then(|o| lookup_uid(o));
    let desired_gid = params.get("group").and_then(|g| lookup_gid(g));

    let p = Path::new(path);

    if p.is_dir() {
        let meta = fs::metadata(p).map_err(|e| format!("stat {}: {}", path, e))?;
        let current_mode = meta.permissions().mode() & 0o7777;
        let ownership_match = check_ownership(path, desired_uid, desired_gid);

        if current_mode == mode && ownership_match {
            result.action = Action::None;
            result.changed = false;
            return Ok(());
        }

        result.action = Action::Updated;
        result.changed = true;

        if dry_run {
            return Ok(());
        }

        if current_mode != mode {
            fs::set_permissions(p, fs::Permissions::from_mode(mode))
                .map_err(|e| format!("chmod {}: {}", path, e))?;
        }
        if !ownership_match {
            apply_ownership(path, desired_uid, desired_gid)?;
        }
        return Ok(());
    }

    result.action = Action::Created;
    result.changed = true;

    if dry_run {
        return Ok(());
    }

    fs::create_dir_all(p).map_err(|e| format!("create directory {}: {}", path, e))?;
    fs::set_permissions(p, fs::Permissions::from_mode(mode))
        .map_err(|e| format!("chmod {}: {}", path, e))?;
    if desired_uid.is_some() || desired_gid.is_some() {
        apply_ownership(path, desired_uid, desired_gid)?;
    }

    Ok(())
}

fn handle_absent(path: &str, dry_run: bool, result: &mut ResourceResult) {
    let p = Path::new(path);
    if !p.exists() {
        result.action = Action::None;
        result.changed = false;
        return;
    }

    result.action = Action::Deleted;
    result.changed = true;

    if dry_run {
        return;
    }

    if let Err(e) = fs::remove_dir_all(p) {
        result.error = Some(format!("remove directory {}: {}", path, e));
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::os::unix::fs::PermissionsExt;
    use tempfile::TempDir;

    fn make_params(pairs: &[(&str, &str)]) -> HashMap<String, String> {
        pairs
            .iter()
            .map(|(k, v)| (k.to_string(), v.to_string()))
            .collect()
    }

    #[test]
    fn creates_directory() {
        let dir = TempDir::new().unwrap();
        let path = dir.path().join("mydir");
        let path_str = path.to_str().unwrap();

        let de = DirectoryExecutor::new();
        let result = de.execute(
            &make_params(&[("targetpath", path_str), ("mode", "0755")]),
            false,
        );

        assert_eq!(result.action, Action::Created);
        assert!(result.changed);
        assert!(result.error.is_none());
        assert!(path.is_dir());
        let meta = fs::metadata(&path).unwrap();
        assert_eq!(meta.permissions().mode() & 0o7777, 0o755);
    }

    #[test]
    fn no_change() {
        let dir = TempDir::new().unwrap();
        let path = dir.path().join("existing_dir");
        fs::create_dir(&path).unwrap();
        fs::set_permissions(&path, fs::Permissions::from_mode(0o755)).unwrap();
        let path_str = path.to_str().unwrap();

        let de = DirectoryExecutor::new();
        let result = de.execute(
            &make_params(&[("targetpath", path_str), ("mode", "0755")]),
            false,
        );

        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
    }

    #[test]
    fn mode_update() {
        let dir = TempDir::new().unwrap();
        let path = dir.path().join("modedir");
        fs::create_dir(&path).unwrap();
        fs::set_permissions(&path, fs::Permissions::from_mode(0o700)).unwrap();
        let path_str = path.to_str().unwrap();

        let de = DirectoryExecutor::new();
        let result = de.execute(
            &make_params(&[("targetpath", path_str), ("mode", "0755")]),
            false,
        );

        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);
        let meta = fs::metadata(&path).unwrap();
        assert_eq!(meta.permissions().mode() & 0o7777, 0o755);
    }

    #[test]
    fn creates_nested() {
        let dir = TempDir::new().unwrap();
        let path = dir.path().join("a/b/c");
        let path_str = path.to_str().unwrap();

        let de = DirectoryExecutor::new();
        let result = de.execute(
            &make_params(&[("targetpath", path_str), ("mode", "0750")]),
            false,
        );

        assert_eq!(result.action, Action::Created);
        assert!(result.changed);
        assert!(path.is_dir());
    }

    #[test]
    fn absent_removes() {
        let dir = TempDir::new().unwrap();
        let path = dir.path().join("removeme");
        fs::create_dir(&path).unwrap();
        fs::write(path.join("child.txt"), "data").unwrap();
        let path_str = path.to_str().unwrap();

        let de = DirectoryExecutor::new();
        let result = de.execute(
            &make_params(&[("targetpath", path_str), ("state", "absent")]),
            false,
        );

        assert_eq!(result.action, Action::Deleted);
        assert!(result.changed);
        assert!(!path.exists());
    }

    #[test]
    fn absent_nonexistent() {
        let dir = TempDir::new().unwrap();
        let path = dir.path().join("noexist");
        let path_str = path.to_str().unwrap();

        let de = DirectoryExecutor::new();
        let result = de.execute(
            &make_params(&[("targetpath", path_str), ("state", "absent")]),
            false,
        );

        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
    }

    #[test]
    fn dry_run() {
        let dir = TempDir::new().unwrap();
        let path = dir.path().join("drydir");
        let path_str = path.to_str().unwrap();

        let de = DirectoryExecutor::new();
        let result = de.execute(
            &make_params(&[("targetpath", path_str), ("mode", "0755")]),
            true,
        );

        assert_eq!(result.action, Action::Created);
        assert!(result.changed);
        assert!(!path.exists());
    }

    #[test]
    fn missing_targetpath_errors() {
        let de = DirectoryExecutor::new();
        let result = de.execute(&make_params(&[("state", "present")]), false);
        assert!(result.error.is_some());
        assert!(result.error.as_ref().unwrap().contains("targetpath"));
    }
}
