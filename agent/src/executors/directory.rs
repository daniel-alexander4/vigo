// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Directory executor — manages directories with mode and ownership idempotently.

use super::file::{apply_ownership, check_ownership, lookup_gid, lookup_uid};
use super::{Action, Executor, ResourceResult};
use std::collections::HashMap;
use std::fs;
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
        let path_str = params.get("target_path").map(|s| s.as_str()).unwrap_or("");
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
        .get("target_path")
        .filter(|s| !s.is_empty())
        .ok_or_else(|| "directory executor: target_path param is required".to_string())?;

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

    let recurse = params.get("recurse").map(|s| s.as_str()) == Some("true");

    if p.is_dir() {
        let current_mode = crate::platform::permissions::get_mode(p)
            .map_err(|e| format!("stat {}: {}", path, e))?;
        let ownership_match = check_ownership(path, desired_uid, desired_gid);
        let top_match = current_mode == mode && ownership_match;
        let children_match = if recurse && top_match {
            check_recursive(p, mode, desired_uid, desired_gid)
        } else {
            true // irrelevant when top doesn't match or recurse is off
        };

        if top_match && children_match {
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
            crate::platform::permissions::set_mode(p, mode)
                .map_err(|e| format!("chmod {}: {}", path, e))?;
        }
        if !ownership_match {
            apply_ownership(path, desired_uid, desired_gid)?;
        }
        if recurse {
            apply_recursive(p, mode, desired_uid, desired_gid)?;
        }
        return Ok(());
    }

    result.action = Action::Created;
    result.changed = true;

    if dry_run {
        return Ok(());
    }

    fs::create_dir_all(p).map_err(|e| format!("create directory {}: {}", path, e))?;
    crate::platform::permissions::set_mode(p, mode)
        .map_err(|e| format!("chmod {}: {}", path, e))?;
    if desired_uid.is_some() || desired_gid.is_some() {
        apply_ownership(path, desired_uid, desired_gid)?;
    }

    Ok(())
}

/// Check whether all entries under `dir` already match the desired mode and ownership.
fn check_recursive(dir: &Path, mode: u32, uid: Option<u32>, gid: Option<u32>) -> bool {
    let entries = match fs::read_dir(dir) {
        Ok(e) => e,
        Err(_) => return false,
    };
    for entry in entries.flatten() {
        let p = entry.path();
        let entry_mode = match crate::platform::permissions::get_mode(&p) {
            Ok(m) => m,
            Err(_) => return false,
        };
        if entry_mode != mode {
            return false;
        }
        if !check_ownership(p.to_str().unwrap_or(""), uid, gid) {
            return false;
        }
        if p.is_dir() && !check_recursive(&p, mode, uid, gid) {
            return false;
        }
    }
    true
}

/// Apply mode and ownership to all entries under `dir` recursively.
fn apply_recursive(dir: &Path, mode: u32, uid: Option<u32>, gid: Option<u32>) -> Result<(), String> {
    let entries = fs::read_dir(dir)
        .map_err(|e| format!("read_dir {}: {}", dir.display(), e))?;
    for entry in entries {
        let entry = entry.map_err(|e| format!("read entry in {}: {}", dir.display(), e))?;
        let p = entry.path();
        let current_mode = crate::platform::permissions::get_mode(&p)
            .map_err(|e| format!("stat {}: {}", p.display(), e))?;
        if current_mode != mode {
            crate::platform::permissions::set_mode(&p, mode)
                .map_err(|e| format!("chmod {}: {}", p.display(), e))?;
        }
        if !check_ownership(p.to_str().unwrap_or(""), uid, gid) {
            apply_ownership(p.to_str().unwrap_or(""), uid, gid)?;
        }
        if p.is_dir() {
            apply_recursive(&p, mode, uid, gid)?;
        }
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
            &make_params(&[("target_path", path_str), ("mode", "0755")]),
            false,
        );

        assert_eq!(result.action, Action::Created);
        assert!(result.changed);
        assert!(result.error.is_none());
        assert!(path.is_dir());
        #[cfg(unix)]
        {
            let mode = crate::platform::permissions::get_mode(&path).unwrap();
            assert_eq!(mode, 0o755);
        }
    }

    #[test]
    fn no_change() {
        let dir = TempDir::new().unwrap();
        let path = dir.path().join("existing_dir");
        fs::create_dir(&path).unwrap();
        crate::platform::permissions::set_mode(&path, 0o755).unwrap();
        let path_str = path.to_str().unwrap();

        let de = DirectoryExecutor::new();
        let result = de.execute(
            &make_params(&[("target_path", path_str), ("mode", "0755")]),
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
        crate::platform::permissions::set_mode(&path, 0o700).unwrap();
        let path_str = path.to_str().unwrap();

        let de = DirectoryExecutor::new();
        let result = de.execute(
            &make_params(&[("target_path", path_str), ("mode", "0755")]),
            false,
        );

        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);
        #[cfg(unix)]
        {
            let mode = crate::platform::permissions::get_mode(&path).unwrap();
            assert_eq!(mode, 0o755);
        }
    }

    #[test]
    fn creates_nested() {
        let dir = TempDir::new().unwrap();
        let path = dir.path().join("a/b/c");
        let path_str = path.to_str().unwrap();

        let de = DirectoryExecutor::new();
        let result = de.execute(
            &make_params(&[("target_path", path_str), ("mode", "0750")]),
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
            &make_params(&[("target_path", path_str), ("state", "absent")]),
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
            &make_params(&[("target_path", path_str), ("state", "absent")]),
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
            &make_params(&[("target_path", path_str), ("mode", "0755")]),
            true,
        );

        assert_eq!(result.action, Action::Created);
        assert!(result.changed);
        assert!(!path.exists());
    }

    #[test]
    fn missing_target_path_errors() {
        let de = DirectoryExecutor::new();
        let result = de.execute(&make_params(&[("state", "present")]), false);
        assert!(result.error.is_some());
        assert!(result.error.as_ref().unwrap().contains("target_path"));
    }

    #[test]
    fn recurse_sets_mode_on_children() {
        let dir = TempDir::new().unwrap();
        let root = dir.path().join("recurse_root");
        let sub = root.join("subdir");
        let file1 = root.join("file1.txt");
        let file2 = sub.join("file2.txt");

        fs::create_dir_all(&sub).unwrap();
        fs::write(&file1, "a").unwrap();
        fs::write(&file2, "b").unwrap();

        // Set everything to 0o700 initially
        crate::platform::permissions::set_mode(&root, 0o700).unwrap();
        crate::platform::permissions::set_mode(&sub, 0o700).unwrap();
        crate::platform::permissions::set_mode(&file1, 0o600).unwrap();
        crate::platform::permissions::set_mode(&file2, 0o600).unwrap();

        let path_str = root.to_str().unwrap();
        let de = DirectoryExecutor::new();
        let result = de.execute(
            &make_params(&[("target_path", path_str), ("mode", "0755"), ("recurse", "true")]),
            false,
        );

        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);
        assert!(result.error.is_none());

        // Verify all entries got mode 0755
        assert_eq!(crate::platform::permissions::get_mode(&root).unwrap(), 0o755);
        assert_eq!(crate::platform::permissions::get_mode(&sub).unwrap(), 0o755);
        assert_eq!(crate::platform::permissions::get_mode(&file1).unwrap(), 0o755);
        assert_eq!(crate::platform::permissions::get_mode(&file2).unwrap(), 0o755);
    }

    #[test]
    fn recurse_no_change_when_all_match() {
        let dir = TempDir::new().unwrap();
        let root = dir.path().join("recurse_ok");
        let sub = root.join("inner");
        let file1 = root.join("a.txt");
        let file2 = sub.join("b.txt");

        fs::create_dir_all(&sub).unwrap();
        fs::write(&file1, "x").unwrap();
        fs::write(&file2, "y").unwrap();

        // Set everything to the desired mode 0755
        crate::platform::permissions::set_mode(&root, 0o755).unwrap();
        crate::platform::permissions::set_mode(&sub, 0o755).unwrap();
        crate::platform::permissions::set_mode(&file1, 0o755).unwrap();
        crate::platform::permissions::set_mode(&file2, 0o755).unwrap();

        let path_str = root.to_str().unwrap();
        let de = DirectoryExecutor::new();
        let result = de.execute(
            &make_params(&[("target_path", path_str), ("mode", "0755"), ("recurse", "true")]),
            false,
        );

        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());
    }
}
