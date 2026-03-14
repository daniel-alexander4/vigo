// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! File executor — manages file content, mode, and ownership idempotently.
//!
//! Uses SHA-256 content hashing to detect drift and atomic write-to-temp-then-rename
//! for safe updates. Supports `stream_edit` pipelines that transform content through
//! external scripts before writing.

use super::{Action, Executor, ResourceResult};
use crate::snapshots;
use sha2::{Digest, Sha256};
use std::collections::HashMap;
use std::fs;
use std::io::{Read, Write};
use std::path::Path;
use std::sync::Mutex;
use std::time::Instant;

/// Cached result of a stream_edit pipeline run.
#[derive(Clone)]
struct StreamEditCacheEntry {
    /// SHA-256 of (input content + script paths + script mtimes).
    input_fingerprint: [u8; 32],
    /// SHA-256 hex of the pipeline output.
    output_hash: String,
}

/// Manages file resources idempotently.
///
/// Optionally saves snapshots of existing files before overwriting them
/// (for rollback support). The `run_id` for snapshots is injected into
/// params as `_run_id` by the runner.
///
/// When `stream_edit` is specified, content is piped through one or more
/// scripts before writing. Pipeline results are cached in memory to skip
/// re-execution when input content and scripts haven't changed.
pub struct FileExecutor {
    pub snapshot_mgr: Option<snapshots::Manager>,
    stream_edit_cache: Mutex<HashMap<String, StreamEditCacheEntry>>,
}

impl FileExecutor {
    pub fn new(snapshot_mgr: Option<snapshots::Manager>) -> Self {
        Self {
            snapshot_mgr,
            stream_edit_cache: Mutex::new(HashMap::new()),
        }
    }
}

impl Executor for FileExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let path_str = params.get("targetpath").map(|s| s.as_str()).unwrap_or("");
        let mut result = ResourceResult::new("file", path_str);

        let res = execute_file(
            params,
            dry_run,
            &mut result,
            &self.snapshot_mgr,
            &self.stream_edit_cache,
        );
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
    stream_edit_cache: &Mutex<HashMap<String, StreamEditCacheEntry>>,
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
    handle_present(path, params, dry_run, result, snapshot_mgr, run_id, stream_edit_cache)
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
    stream_edit_cache: &Mutex<HashMap<String, StreamEditCacheEntry>>,
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

    // Parse stream_edit pipeline (single path or JSON array of paths).
    let scripts = params
        .get("stream_edit")
        .map(|v| parse_stream_edit(v))
        .unwrap_or_default();

    // If stream_edit is specified, run content through the pipeline.
    if !scripts.is_empty() {
        let fingerprint = stream_edit_fingerprint(&content, &scripts);
        let p = Path::new(path);
        let file_existed = p.exists();

        // Fast path: if we've seen this exact input+scripts before and the
        // file on disk matches the cached output, skip the pipeline entirely.
        if file_existed {
            let mode_str = params.get("mode").map(|s| s.as_str()).unwrap_or("0644");
            let mode = u32::from_str_radix(mode_str.trim_start_matches('0'), 8)
                .map_err(|e| format!("parse mode {:?}: {}", mode_str, e))?;
            let desired_uid = params.get("owner").and_then(|o| lookup_uid(o));
            let desired_gid = params.get("group").and_then(|g| lookup_gid(g));

            if let Some(entry) = stream_edit_cache
                .lock()
                .ok()
                .and_then(|c| c.get(path).cloned())
            {
                if entry.input_fingerprint == fingerprint {
                    let current_hash = file_hash(path)
                        .map_err(|e| format!("hash existing file {}: {}", path, e))?;
                    let current_mode = crate::platform::permissions::get_mode(p)
                        .map_err(|e| format!("stat {}: {}", path, e))?;
                    let ownership_match = check_ownership(path, desired_uid, desired_gid);

                    if current_hash == entry.output_hash
                        && current_mode == mode
                        && ownership_match
                    {
                        result.action = Action::None;
                        result.changed = false;
                        return Ok(());
                    }
                }
            }
        }

        // Cache miss or file changed — run the pipeline.
        content = run_stream_edit_pipeline(&content, &scripts)?;

        // Update cache with the new output hash.
        let output_hash = sha256_hex(content.as_bytes());
        if let Ok(mut cache) = stream_edit_cache.lock() {
            cache.insert(
                path.to_string(),
                StreamEditCacheEntry {
                    input_fingerprint: fingerprint,
                    output_hash,
                },
            );
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

        let current_mode = crate::platform::permissions::get_mode(p)
            .map_err(|e| format!("stat {}: {}", path, e))?;

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
    crate::platform::permissions::set_mode(&tmp_path, mode)
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

// ---------------------------------------------------------------------------
// stream_edit pipeline
// ---------------------------------------------------------------------------

/// Parse the `stream_edit` attribute value into a list of script paths.
///
/// Accepts either a single path string or a JSON array of path strings
/// (produced by the server when the YAML value is a list).
fn parse_stream_edit(value: &str) -> Vec<String> {
    let trimmed = value.trim();
    if trimmed.is_empty() {
        return vec![];
    }
    if trimmed.starts_with('[') {
        serde_json::from_str::<Vec<String>>(trimmed)
            .unwrap_or_else(|_| vec![trimmed.to_string()])
    } else {
        vec![trimmed.to_string()]
    }
}

/// Run content through a pipeline of scripts, piping stdin→stdout at each stage.
///
/// Each script must exist, be a file, and exit 0. Non-zero exit is an error
/// (the resource fails, no file is written). The final stdout becomes the
/// file content.
fn run_stream_edit_pipeline(content: &str, scripts: &[String]) -> Result<String, String> {
    let mut data = content.as_bytes().to_vec();

    for script in scripts {
        let script_path = Path::new(script);
        if !script_path.exists() {
            return Err(format!("stream_edit: script not found: {}", script));
        }
        if !script_path.is_file() {
            return Err(format!("stream_edit: not a regular file: {}", script));
        }

        let output = super::run_command_with_stdin(script, &[], &data)
            .map_err(|e| format!("stream_edit: running {}: {}", script, e))?;

        if !output.status.success() {
            let stderr = String::from_utf8_lossy(&output.stderr);
            return Err(format!(
                "stream_edit: {} exited with {}: {}",
                script,
                output.status.code().unwrap_or(-1),
                stderr.trim()
            ));
        }

        data = output.stdout;
    }

    String::from_utf8(data)
        .map_err(|e| format!("stream_edit: pipeline output is not valid UTF-8: {}", e))
}

/// Compute a fingerprint over the input content + script paths + script mtimes.
///
/// Used as the cache key: if the fingerprint matches a cached entry, the
/// pipeline output would be identical and can be skipped.
fn stream_edit_fingerprint(content: &str, scripts: &[String]) -> [u8; 32] {
    let mut hasher = Sha256::new();
    hasher.update(content.as_bytes());
    for script in scripts {
        hasher.update(script.as_bytes());
        // Include script mtime so edits to the script invalidate the cache.
        if let Ok(meta) = fs::metadata(script) {
            if let Ok(mtime) = meta.modified() {
                let secs = mtime
                    .duration_since(std::time::UNIX_EPOCH)
                    .unwrap_or_default()
                    .as_secs();
                hasher.update(&secs.to_le_bytes());
            }
        }
    }
    let result = hasher.finalize();
    let mut arr = [0u8; 32];
    arr.copy_from_slice(&result);
    arr
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

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
pub(crate) fn lookup_uid(owner: &str) -> Option<u32> {
    crate::platform::users::lookup_user(owner).map(|u| u.uid)
}

/// Look up GID by group name.
pub(crate) fn lookup_gid(group: &str) -> Option<u32> {
    crate::platform::users::lookup_group(group).map(|g| g.gid)
}

/// Check if file ownership matches desired.
pub(crate) fn check_ownership(path: &str, desired_uid: Option<u32>, desired_gid: Option<u32>) -> bool {
    #[cfg(unix)]
    {
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
    #[cfg(not(unix))]
    {
        // On Windows, ownership checks are skipped (ACLs are a different model).
        let _ = (path, desired_uid, desired_gid);
        true
    }
}

/// Apply ownership via chown (Unix) or no-op (Windows).
pub(crate) fn apply_ownership(
    path: &str,
    uid: Option<u32>,
    gid: Option<u32>,
) -> Result<(), String> {
    #[cfg(unix)]
    {
        use nix::unistd::{chown, Gid, Uid};
        chown(
            path,
            uid.map(Uid::from_raw),
            gid.map(Gid::from_raw),
        )
        .map_err(|e| format!("chown {}: {}", path, e))
    }
    #[cfg(not(unix))]
    {
        // On Windows, ownership is managed differently (icacls).
        // Skip silently for now.
        let _ = (path, uid, gid);
        Ok(())
    }
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
#[cfg(unix)]
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

    // --- stream_edit tests ---

    /// Helper: create an executable script that transforms stdin.
    fn make_script(dir: &Path, name: &str, body: &str) -> String {
        let script_path = dir.join(name);
        fs::write(&script_path, format!("#!/bin/sh\n{}", body)).unwrap();
        fs::set_permissions(&script_path, fs::Permissions::from_mode(0o755)).unwrap();
        script_path.to_str().unwrap().to_string()
    }

    #[test]
    fn parse_stream_edit_single() {
        let scripts = parse_stream_edit("/usr/local/bin/envsubst");
        assert_eq!(scripts, vec!["/usr/local/bin/envsubst"]);
    }

    #[test]
    fn parse_stream_edit_json_array() {
        let scripts = parse_stream_edit(r#"["/usr/local/bin/strip","/usr/local/bin/envsubst"]"#);
        assert_eq!(scripts, vec!["/usr/local/bin/strip", "/usr/local/bin/envsubst"]);
    }

    #[test]
    fn parse_stream_edit_empty() {
        let scripts = parse_stream_edit("");
        assert!(scripts.is_empty());
    }

    #[test]
    fn stream_edit_single_script() {
        let dir = TempDir::new().unwrap();
        let target = dir.path().join("output.conf");
        let target_str = target.to_str().unwrap();

        // Script that uppercases input.
        let script = make_script(dir.path(), "upper.sh", "tr '[:lower:]' '[:upper:]'");

        let fe = FileExecutor::new(None);
        let result = fe.execute(
            &make_params(&[
                ("targetpath", target_str),
                ("content", "hello world"),
                ("stream_edit", &script),
            ]),
            false,
        );

        assert!(result.error.is_none(), "error: {:?}", result.error);
        assert_eq!(result.action, Action::Created);
        assert!(result.changed);
        assert_eq!(fs::read_to_string(&target).unwrap(), "HELLO WORLD");
    }

    #[test]
    fn stream_edit_chained_scripts() {
        let dir = TempDir::new().unwrap();
        let target = dir.path().join("output.conf");
        let target_str = target.to_str().unwrap();

        // Script 1: add a prefix to each line.
        let script1 = make_script(dir.path(), "prefix.sh", "sed 's/^/PREFIX: /'");
        // Script 2: uppercase everything.
        let script2 = make_script(dir.path(), "upper.sh", "tr '[:lower:]' '[:upper:]'");

        let scripts_json = serde_json::to_string(&vec![&script1, &script2]).unwrap();

        let fe = FileExecutor::new(None);
        let result = fe.execute(
            &make_params(&[
                ("targetpath", target_str),
                ("content", "hello\nworld"),
                ("stream_edit", &scripts_json),
            ]),
            false,
        );

        assert!(result.error.is_none(), "error: {:?}", result.error);
        assert_eq!(result.action, Action::Created);
        assert_eq!(
            fs::read_to_string(&target).unwrap(),
            "PREFIX: HELLO\nPREFIX: WORLD"
        );
    }

    #[test]
    fn stream_edit_script_failure_is_error() {
        let dir = TempDir::new().unwrap();
        let target = dir.path().join("output.conf");
        let target_str = target.to_str().unwrap();

        // Script that exits non-zero.
        let script = make_script(dir.path(), "fail.sh", "echo 'something broke' >&2; exit 1");

        let fe = FileExecutor::new(None);
        let result = fe.execute(
            &make_params(&[
                ("targetpath", target_str),
                ("content", "hello"),
                ("stream_edit", &script),
            ]),
            false,
        );

        assert!(result.error.is_some());
        assert!(result.error.as_ref().unwrap().contains("exited with 1"));
        assert!(result.error.as_ref().unwrap().contains("something broke"));
        // Target should NOT be created on failure.
        assert!(!target.exists());
    }

    #[test]
    fn stream_edit_missing_script_is_error() {
        let dir = TempDir::new().unwrap();
        let target = dir.path().join("output.conf");
        let target_str = target.to_str().unwrap();

        let fe = FileExecutor::new(None);
        let result = fe.execute(
            &make_params(&[
                ("targetpath", target_str),
                ("content", "hello"),
                ("stream_edit", "/nonexistent/script.sh"),
            ]),
            false,
        );

        assert!(result.error.is_some());
        assert!(result.error.as_ref().unwrap().contains("not found"));
    }

    #[test]
    fn stream_edit_idempotent_no_change() {
        let dir = TempDir::new().unwrap();
        let target = dir.path().join("output.conf");
        let target_str = target.to_str().unwrap();

        let script = make_script(dir.path(), "upper.sh", "tr '[:lower:]' '[:upper:]'");

        let fe = FileExecutor::new(None);

        // First run: creates the file.
        let result1 = fe.execute(
            &make_params(&[
                ("targetpath", target_str),
                ("content", "hello"),
                ("stream_edit", &script),
            ]),
            false,
        );
        assert_eq!(result1.action, Action::Created);
        assert_eq!(fs::read_to_string(&target).unwrap(), "HELLO");

        // Second run: content and script unchanged, file matches → no-op.
        let result2 = fe.execute(
            &make_params(&[
                ("targetpath", target_str),
                ("content", "hello"),
                ("stream_edit", &script),
                ("mode", "0644"),
            ]),
            false,
        );
        assert_eq!(result2.action, Action::None);
        assert!(!result2.changed);
    }

    #[test]
    fn stream_edit_cache_hit_skips_pipeline() {
        let dir = TempDir::new().unwrap();
        let target = dir.path().join("output.conf");
        let target_str = target.to_str().unwrap();

        // Script that writes to a counter file each time it runs.
        let counter_path = dir.path().join("counter");
        fs::write(&counter_path, "0").unwrap();
        let counter_str = counter_path.to_str().unwrap();

        let script = make_script(
            dir.path(),
            "counting_upper.sh",
            &format!(
                "n=$(cat {}); echo $((n+1)) > {}; tr '[:lower:]' '[:upper:]'",
                counter_str, counter_str
            ),
        );

        let fe = FileExecutor::new(None);

        // First run: pipeline executes, counter=1.
        fe.execute(
            &make_params(&[
                ("targetpath", target_str),
                ("content", "hello"),
                ("stream_edit", &script),
                ("mode", "0644"),
            ]),
            false,
        );
        assert_eq!(fs::read_to_string(&counter_path).unwrap().trim(), "1");

        // Second run: cache hit, pipeline should NOT execute, counter stays 1.
        let result = fe.execute(
            &make_params(&[
                ("targetpath", target_str),
                ("content", "hello"),
                ("stream_edit", &script),
                ("mode", "0644"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert_eq!(fs::read_to_string(&counter_path).unwrap().trim(), "1");
    }

    #[test]
    fn stream_edit_with_baseindent() {
        let dir = TempDir::new().unwrap();
        let target = dir.path().join("output.conf");
        let target_str = target.to_str().unwrap();

        let script = make_script(dir.path(), "upper.sh", "tr '[:lower:]' '[:upper:]'");

        let fe = FileExecutor::new(None);
        let result = fe.execute(
            &make_params(&[
                ("targetpath", target_str),
                ("content", "hello\nworld"),
                ("baseindent", "2"),
                ("stream_edit", &script),
            ]),
            false,
        );

        assert!(result.error.is_none(), "error: {:?}", result.error);
        // baseindent applied first, then stream_edit.
        assert_eq!(fs::read_to_string(&target).unwrap(), "  HELLO\n  WORLD");
    }

    #[test]
    fn stream_edit_ignored_on_absent() {
        let dir = TempDir::new().unwrap();
        let target = dir.path().join("output.conf");
        let target_str = target.to_str().unwrap();
        fs::write(&target, "content").unwrap();

        let fe = FileExecutor::new(None);
        let result = fe.execute(
            &make_params(&[
                ("targetpath", target_str),
                ("state", "absent"),
                ("stream_edit", "/nonexistent/script.sh"),
            ]),
            false,
        );

        assert_eq!(result.action, Action::Deleted);
        assert!(result.error.is_none());
        assert!(!target.exists());
    }
}
