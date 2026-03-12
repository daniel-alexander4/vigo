// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! File executor — manages file content, mode, and ownership idempotently.
//!
//! Uses SHA-256 content hashing to detect drift and atomic write-to-temp-then-rename
//! for safe updates.

use super::{Action, Executor, ResourceResult};
use crate::snapshots;
use sha2::{Digest, Sha256};
use std::collections::HashMap;
use std::fs;
use std::io::{Read, Write};
use std::os::unix::fs::PermissionsExt;
use std::path::Path;
use std::time::Instant;

/// Manages file resources idempotently.
///
/// Optionally saves snapshots of existing files before overwriting them
/// (for rollback support). The `run_id` for snapshots is injected into
/// params as `_run_id` by the runner.
pub struct FileExecutor {
    pub snapshot_mgr: Option<snapshots::Manager>,
}

impl FileExecutor {
    pub fn new(snapshot_mgr: Option<snapshots::Manager>) -> Self {
        Self { snapshot_mgr }
    }
}

impl Executor for FileExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let path_str = params.get("targetpath").map(|s| s.as_str()).unwrap_or("");
        let mut result = ResourceResult::new("file", path_str);

        let res = execute_file(params, dry_run, &mut result, &self.snapshot_mgr);
        result.duration_ms = start.elapsed().as_millis() as i32;
        if let Err(e) = res {
            result.error = Some(e);
        }
        result
    }
}

fn execute_file(
    params: &HashMap<String, String>,
    dry_run: bool,
    result: &mut ResourceResult,
    snapshot_mgr: &Option<snapshots::Manager>,
) -> Result<(), String> {
    let path = params
        .get("targetpath")
        .filter(|s| !s.is_empty())
        .ok_or_else(|| "file executor: targetpath param is required".to_string())?;

    let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");

    if state == "absent" {
        handle_absent(path, dry_run, result);
        return Ok(());
    }

    let run_id = params.get("_run_id").map(|s| s.as_str()).unwrap_or("");
    handle_present(path, params, dry_run, result, snapshot_mgr, run_id)
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

    if let Err(e) = fs::remove_file(p) {
        result.error = Some(format!("remove {}: {}", path, e));
    }
}

fn handle_present(
    path: &str,
    params: &HashMap<String, String>,
    dry_run: bool,
    result: &mut ResourceResult,
    snapshot_mgr: &Option<snapshots::Manager>,
    run_id: &str,
) -> Result<(), String> {
    let mut content = params.get("content").cloned().unwrap_or_default();

    // Apply base indentation if specified.
    if let Some(bi_str) = params.get("baseindent") {
        let bi: usize = bi_str
            .parse()
            .map_err(|e| format!("parse baseindent {:?}: {}", bi_str, e))?;
        if bi > 0 {
            content = apply_base_indent(&content, bi);
        }
    }

    let mode_str = params.get("mode").map(|s| s.as_str()).unwrap_or("0644");
    let mode = u32::from_str_radix(mode_str.trim_start_matches('0'), 8)
        .map_err(|e| format!("parse mode {:?}: {}", mode_str, e))?;

    let desired_hash = sha256_hex(content.as_bytes());
    let p = Path::new(path);
    let file_existed = p.exists();

    // Resolve desired ownership.
    let desired_uid = params.get("owner").and_then(|o| lookup_uid(o));
    let desired_gid = params.get("group").and_then(|g| lookup_gid(g));

    if file_existed {
        let current_hash =
            file_hash(path).map_err(|e| format!("hash existing file {}: {}", path, e))?;

        let meta = fs::metadata(p).map_err(|e| format!("stat {}: {}", path, e))?;
        let current_mode = meta.permissions().mode() & 0o7777;

        let ownership_match = check_ownership(path, desired_uid, desired_gid);

        if current_hash == desired_hash && current_mode == mode && ownership_match {
            result.action = Action::None;
            result.changed = false;
            return Ok(());
        }

        // Only ownership differs — chown without rewriting.
        if current_hash == desired_hash && current_mode == mode && !ownership_match {
            result.action = Action::Updated;
            result.changed = true;
            if dry_run {
                return Ok(());
            }
            apply_ownership(path, desired_uid, desired_gid)?;
            return Ok(());
        }

        result.action = Action::Updated;
        result.changed = true;
    } else {
        result.action = Action::Created;
        result.changed = true;
    }

    if dry_run {
        return Ok(());
    }

    // Snapshot before overwrite (if manager is available and file existed).
    if file_existed {
        if let Some(ref mgr) = snapshot_mgr {
            if !run_id.is_empty() {
                if let Err(e) = mgr.save(path, run_id) {
                    return Err(format!("snapshot save {}: {}", path, e));
                }
            }
        }
    }

    // Ensure parent directory exists.
    if let Some(parent) = Path::new(path).parent() {
        fs::create_dir_all(parent)
            .map_err(|e| format!("create parent dir {}: {}", parent.display(), e))?;
    }

    // Atomic write: temp file in same dir → chmod → rename.
    let dir = Path::new(path)
        .parent()
        .unwrap_or_else(|| Path::new("."));
    let tmp_path = dir.join(format!(".hobnob-{}", uuid::Uuid::new_v4()));
    let tmp_path_str = tmp_path.display().to_string();

    {
        let mut tmp = fs::File::create(&tmp_path)
            .map_err(|e| format!("create temp file in {}: {}", dir.display(), e))?;
        tmp.write_all(content.as_bytes())
            .map_err(|e| format!("write temp file: {}", e))?;
    }

    // Set permissions before rename.
    fs::set_permissions(&tmp_path, fs::Permissions::from_mode(mode))
        .map_err(|e| {
            let _ = fs::remove_file(&tmp_path);
            format!("chmod temp file: {}", e)
        })?;

    fs::rename(&tmp_path, path).map_err(|e| {
        let _ = fs::remove_file(&tmp_path);
        format!("rename temp to {}: {}", path, e)
    })?;

    // Apply ownership if specified.
    if desired_uid.is_some() || desired_gid.is_some() {
        apply_ownership(path, desired_uid, desired_gid).map_err(|e| {
            // Clean up temp on failure — but file is already renamed, so just report.
            let _ = fs::remove_file(&tmp_path_str);
            e
        })?;
    }

    Ok(())
}

fn sha256_hex(data: &[u8]) -> String {
    let hash = Sha256::digest(data);
    hex::encode(hash)
}

fn file_hash(path: &str) -> Result<String, std::io::Error> {
    let mut f = fs::File::open(path)?;
    let mut hasher = Sha256::new();
    let mut buf = [0u8; 8192];
    loop {
        let n = f.read(&mut buf)?;
        if n == 0 {
            break;
        }
        hasher.update(&buf[..n]);
    }
    Ok(hex::encode(hasher.finalize()))
}

fn apply_base_indent(content: &str, n: usize) -> String {
    let prefix = " ".repeat(n);
    content
        .split('\n')
        .map(|line| {
            if line.is_empty() {
                line.to_string()
            } else {
                format!("{}{}", prefix, line)
            }
        })
        .collect::<Vec<_>>()
        .join("\n")
}

/// Look up UID by username.
fn lookup_uid(owner: &str) -> Option<u32> {
    nix::unistd::User::from_name(owner)
        .ok()
        .flatten()
        .map(|u| u.uid.as_raw())
}

/// Look up GID by group name.
fn lookup_gid(group: &str) -> Option<u32> {
    nix::unistd::Group::from_name(group)
        .ok()
        .flatten()
        .map(|g| g.gid.as_raw())
}

/// Check if file ownership matches desired.
fn check_ownership(path: &str, desired_uid: Option<u32>, desired_gid: Option<u32>) -> bool {
    use std::os::unix::fs::MetadataExt;
    let meta = match fs::metadata(path) {
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

/// Apply ownership via chown.
fn apply_ownership(
    path: &str,
    uid: Option<u32>,
    gid: Option<u32>,
) -> Result<(), String> {
    use nix::unistd::{chown, Gid, Uid};
    chown(
        path,
        uid.map(Uid::from_raw),
        gid.map(Gid::from_raw),
    )
    .map_err(|e| format!("chown {}: {}", path, e))
}

// We need hex encoding — use sha2's output directly.
mod hex {
    pub fn encode(data: impl AsRef<[u8]>) -> String {
        data.as_ref()
            .iter()
            .map(|b| format!("{:02x}", b))
            .collect()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::os::unix::fs::PermissionsExt;
    use tempfile::TempDir;

    fn make_params(pairs: &[(&str, &str)]) -> HashMap<String, String> {
        pairs.iter().map(|(k, v)| (k.to_string(), v.to_string())).collect()
    }

    #[test]
    fn creates_new_file() {
        let dir = TempDir::new().unwrap();
        let path = dir.path().join("new.txt");
        let path_str = path.to_str().unwrap();

        let fe = FileExecutor::new(None);
        let result = fe.execute(
            &make_params(&[
                ("targetpath", path_str),
                ("content", "hello world\n"),
                ("mode", "0644"),
                ("state", "present"),
            ]),
            false,
        );

        assert_eq!(result.action, Action::Created);
        assert!(result.changed);
        assert!(result.error.is_none());
        assert_eq!(fs::read_to_string(&path).unwrap(), "hello world\n");
    }

    #[test]
    fn no_change_when_content_matches() {
        let dir = TempDir::new().unwrap();
        let path = dir.path().join("existing.txt");
        let path_str = path.to_str().unwrap();
        let content = "hello world\n";

        fs::write(&path, content).unwrap();
        fs::set_permissions(&path, fs::Permissions::from_mode(0o644)).unwrap();

        let fe = FileExecutor::new(None);
        let result = fe.execute(
            &make_params(&[
                ("targetpath", path_str),
                ("content", content),
                ("mode", "0644"),
                ("state", "present"),
            ]),
            false,
        );

        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
    }

    #[test]
    fn updates_existing_file() {
        let dir = TempDir::new().unwrap();
        let path = dir.path().join("update.txt");
        let path_str = path.to_str().unwrap();

        fs::write(&path, "old content\n").unwrap();
        fs::set_permissions(&path, fs::Permissions::from_mode(0o644)).unwrap();

        let fe = FileExecutor::new(None);
        let result = fe.execute(
            &make_params(&[
                ("targetpath", path_str),
                ("content", "new content\n"),
                ("mode", "0644"),
                ("state", "present"),
            ]),
            false,
        );

        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);
        assert_eq!(fs::read_to_string(&path).unwrap(), "new content\n");
    }

    #[test]
    fn dry_run_does_not_create() {
        let dir = TempDir::new().unwrap();
        let path = dir.path().join("dryrun.txt");
        let path_str = path.to_str().unwrap();

        let fe = FileExecutor::new(None);
        let result = fe.execute(
            &make_params(&[
                ("targetpath", path_str),
                ("content", "should not exist\n"),
                ("state", "present"),
            ]),
            true,
        );

        assert_eq!(result.action, Action::Created);
        assert!(result.changed);
        assert!(!path.exists());
    }

    #[test]
    fn deletes_file() {
        let dir = TempDir::new().unwrap();
        let path = dir.path().join("todelete.txt");
        let path_str = path.to_str().unwrap();

        fs::write(&path, "bye\n").unwrap();

        let fe = FileExecutor::new(None);
        let result = fe.execute(
            &make_params(&[("targetpath", path_str), ("state", "absent")]),
            false,
        );

        assert_eq!(result.action, Action::Deleted);
        assert!(result.changed);
        assert!(!path.exists());
    }

    #[test]
    fn delete_nonexistent_is_noop() {
        let dir = TempDir::new().unwrap();
        let path = dir.path().join("noexist.txt");
        let path_str = path.to_str().unwrap();

        let fe = FileExecutor::new(None);
        let result = fe.execute(
            &make_params(&[("targetpath", path_str), ("state", "absent")]),
            false,
        );

        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
    }

    #[test]
    fn mode_change_triggers_update() {
        let dir = TempDir::new().unwrap();
        let path = dir.path().join("modefile.txt");
        let path_str = path.to_str().unwrap();
        let content = "same content\n";

        fs::write(&path, content).unwrap();
        fs::set_permissions(&path, fs::Permissions::from_mode(0o600)).unwrap();

        let fe = FileExecutor::new(None);
        let result = fe.execute(
            &make_params(&[
                ("targetpath", path_str),
                ("content", content),
                ("mode", "0644"),
                ("state", "present"),
            ]),
            false,
        );

        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);
    }

    #[test]
    fn default_mode_is_0644() {
        let dir = TempDir::new().unwrap();
        let path = dir.path().join("defaultmode.txt");
        let path_str = path.to_str().unwrap();

        let fe = FileExecutor::new(None);
        let result = fe.execute(
            &make_params(&[
                ("targetpath", path_str),
                ("content", "test\n"),
                ("state", "present"),
            ]),
            false,
        );

        assert!(result.error.is_none());
        let meta = fs::metadata(&path).unwrap();
        assert_eq!(meta.permissions().mode() & 0o7777, 0o644);
    }

    #[test]
    fn creates_parent_dirs() {
        let dir = TempDir::new().unwrap();
        let path = dir.path().join("sub/dir/test.txt");
        let path_str = path.to_str().unwrap();

        let fe = FileExecutor::new(None);
        let result = fe.execute(
            &make_params(&[
                ("targetpath", path_str),
                ("content", "nested\n"),
                ("state", "present"),
            ]),
            false,
        );

        assert!(result.error.is_none());
        assert_eq!(result.action, Action::Created);
        assert_eq!(fs::read_to_string(&path).unwrap(), "nested\n");
    }

    #[test]
    fn missing_targetpath_errors() {
        let fe = FileExecutor::new(None);
        let result = fe.execute(&make_params(&[("state", "present")]), false);
        assert!(result.error.is_some());
        assert!(result.error.as_ref().unwrap().contains("targetpath"));
    }

    #[test]
    fn base_indent() {
        assert_eq!(apply_base_indent("line1\nline2\n", 2), "  line1\n  line2\n");
        assert_eq!(apply_base_indent("a\n\nb\n", 2), "  a\n\n  b\n");
    }

    #[test]
    fn base_indent_executor() {
        let dir = TempDir::new().unwrap();
        let path = dir.path().join("indent.conf");
        let path_str = path.to_str().unwrap();

        let fe = FileExecutor::new(None);
        let result = fe.execute(
            &make_params(&[
                ("targetpath", path_str),
                ("content", "key = val\n  sub = nested\n"),
                ("baseindent", "2"),
            ]),
            false,
        );

        assert!(result.error.is_none());
        assert_eq!(
            fs::read_to_string(&path).unwrap(),
            "  key = val\n    sub = nested\n"
        );
    }
}
