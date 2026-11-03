// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Custom executor — runs external scripts with a JSON protocol.
//!
//! The script receives `{"params": {...}, "dry_run": bool}` on stdin and must
//! write `{"action": "...", "changed": bool, "error": "..."}` to stdout.

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::Instant;

/// Runs an external script with JSON stdin/stdout protocol.
pub struct CustomExecutor {
    pub script_path: String,
    /// Command runner — routes the spawn through the kill-capable
    /// `run_with_stdin_niced_timeout` so a hung operator script can't
    /// block the convergence cycle. `DefaultRunner` in production;
    /// overridable in tests.
    pub runner: Box<dyn CommandRunner>,
}

impl CustomExecutor {
    /// Construct with the real, kill-capable runner.
    pub fn new(script_path: String) -> Self {
        Self {
            script_path,
            runner: Box::new(DefaultRunner),
        }
    }
}

#[derive(serde::Serialize)]
struct CustomInput {
    params: HashMap<String, String>,
    dry_run: bool,
}

#[derive(serde::Deserialize)]
struct CustomOutput {
    #[serde(default)]
    action: String,
    #[serde(default)]
    changed: bool,
    #[serde(default)]
    error: String,
}

impl Executor for CustomExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let name = params.get("name").map(|s| s.as_str()).unwrap_or("");
        let mut result = ResourceResult::new("custom", name);

        let input = CustomInput {
            params: params.clone(),
            dry_run,
        };

        let input_json = match serde_json::to_vec(&input) {
            Ok(j) => j,
            Err(e) => {
                result.error = Some(format!("marshaling input: {}", e));
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
        };

        // Per-process timeout — the kill path. A hung operator script
        // must not block the convergence cycle, so route the spawn through
        // the runner's `run_with_stdin_niced_timeout` (setpgid + SIGTERM→
        // grace→SIGKILL of the whole process tree on expiry). Honor an
        // explicit `timeout:`/`timeout_secs:` on the resource, else fall
        // back to the runner-injected `_default_timeout_secs` — same
        // precedence the exec executor uses (see exec.rs).
        let timeout = params
            .get("timeout")
            .or_else(|| params.get("timeout_secs"))
            .and_then(|s| super::exec::parse_duration(s))
            .or_else(|| {
                params
                    .get("_default_timeout_secs")
                    .and_then(|s| s.parse::<u64>().ok())
                    .filter(|&n| n > 0)
                    .map(std::time::Duration::from_secs)
            });

        // Retry on ETXTBSY — a kernel race on tmpfs where the executable
        // inode is briefly held after a recent write/rename.
        let mut last_err = None;
        let mut output = None;
        for attempt in 0..5 {
            let res = match timeout {
                Some(dur) => self.runner.run_with_stdin_niced_timeout(
                    &input_json,
                    &self.script_path,
                    &[],
                    0,
                    dur,
                ),
                None => self
                    .runner
                    .run_with_stdin(&input_json, &self.script_path, &[]),
            };
            match res {
                Ok(o) => {
                    output = Some(o);
                    break;
                }
                Err(e) if e.raw_os_error() == Some(26) && attempt < 4 => {
                    // ETXTBSY (26) — brief retry.
                    last_err = Some(e);
                    std::thread::sleep(std::time::Duration::from_millis(10));
                }
                Err(e) => {
                    // Includes the timeout case: the runner returns
                    // io::ErrorKind::TimedOut after killing the process
                    // tree, which surfaces here as a reported error.
                    result.error = Some(format!("running {}: {}", self.script_path, e));
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }
            }
        }
        let output = match output {
            Some(o) => o,
            None => {
                let e = last_err
                    .map(|e| e.to_string())
                    .unwrap_or_else(|| "unknown".into());
                result.error =
                    Some(format!("running {} after 5 retries: {}", self.script_path, e));
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
        };

        if !output.status.success() {
            let stderr = String::from_utf8_lossy(&output.stderr);
            result.error = Some(format!("running {}: exit {}: {}", self.script_path, output.status, stderr));
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let parsed: CustomOutput = match serde_json::from_slice(&output.stdout) {
            Ok(o) => o,
            Err(e) => {
                let stdout = String::from_utf8_lossy(&output.stdout);
                result.error = Some(format!(
                    "parsing output from {}: {}\nstdout: {}",
                    self.script_path, e, stdout
                ));
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
        };

        if !parsed.error.is_empty() {
            result.error = Some(parsed.error);
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        result.changed = parsed.changed;
        result.action = if parsed.action.is_empty() {
            Action::None
        } else {
            Action::from_str_lossy(&parsed.action)
        };

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}

#[cfg(test)]
#[cfg(unix)]
mod tests {
    use super::*;
    use std::os::unix::fs::{OpenOptionsExt, PermissionsExt};

    fn make_params(pairs: &[(&str, &str)]) -> HashMap<String, String> {
        pairs
            .iter()
            .map(|(k, v)| (k.to_string(), v.to_string()))
            .collect()
    }

    fn write_script(dir: &std::path::Path, name: &str, body: &str) -> String {
        // Write as a non-executable temp file, rename, then chmod.
        // Writing with mode 0o644 first avoids ETXTBSY on Linux tmpfs
        // where the kernel can race between write-close and exec if the
        // file is already executable during the write.
        use std::io::Write;
        let tmp_path = dir.join(format!(".{}.tmp", name));
        let final_path = dir.join(name);
        let content = format!("#!/bin/bash\n{}", body);
        let mut f = std::fs::OpenOptions::new()
            .write(true)
            .create(true)
            .truncate(true)
            .mode(0o644)
            .open(&tmp_path)
            .unwrap();
        f.write_all(content.as_bytes()).unwrap();
        f.sync_all().unwrap();
        drop(f);
        std::fs::rename(&tmp_path, &final_path).unwrap();
        std::fs::set_permissions(&final_path, std::fs::Permissions::from_mode(0o755)).unwrap();
        final_path.to_str().unwrap().to_string()
    }

    #[test]
    fn missing_script_errors() {
        let exec = CustomExecutor::new("/tmp/vigo-test-nonexistent-script-12345".into());
        let result = exec.execute(&make_params(&[("name", "test")]), false);
        assert!(result.error.is_some());
        assert!(
            result.error.as_deref().unwrap().contains("running /tmp/vigo-test-nonexistent-script-12345"),
            "unexpected error: {:?}",
            result.error
        );
    }

    #[test]
    fn script_returns_changed() {
        let dir = tempfile::TempDir::new().unwrap();
        let script = write_script(
            dir.path(),
            "changed.sh",
            r#"echo '{"action":"created","changed":true,"error":""}'"#,
        );
        let exec = CustomExecutor::new(script);
        let result = exec.execute(&make_params(&[("name", "test")]), false);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        assert!(result.changed);
        assert_eq!(result.action, Action::Created);
    }

    #[test]
    fn script_returns_unchanged() {
        let dir = tempfile::TempDir::new().unwrap();
        let script = write_script(
            dir.path(),
            "unchanged.sh",
            r#"echo '{"action":"none","changed":false,"error":""}'"#,
        );
        let exec = CustomExecutor::new(script);
        let result = exec.execute(&make_params(&[("name", "test")]), false);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        assert!(!result.changed);
        assert_eq!(result.action, Action::None);
    }

    #[test]
    fn script_returns_error() {
        let dir = tempfile::TempDir::new().unwrap();
        let script = write_script(
            dir.path(),
            "error.sh",
            r#"echo '{"action":"","changed":false,"error":"something failed"}'"#,
        );
        let exec = CustomExecutor::new(script);
        let result = exec.execute(&make_params(&[("name", "test")]), false);
        assert!(result.error.is_some());
        assert_eq!(result.error.as_deref().unwrap(), "something failed");
    }

    #[test]
    fn script_exit_nonzero() {
        let dir = tempfile::TempDir::new().unwrap();
        let script = write_script(
            dir.path(),
            "fail.sh",
            "echo 'boom' >&2\nexit 1\n",
        );
        let exec = CustomExecutor::new(script);
        let result = exec.execute(&make_params(&[("name", "test")]), false);
        assert!(result.error.is_some());
        assert!(
            result.error.as_deref().unwrap().contains("exit"),
            "unexpected error: {:?}",
            result.error
        );
    }

    // A hung custom script (no output, never exits) must be killed by the
    // runner-injected default timeout rather than blocking the convergence
    // cycle. Mirrors exec.rs's `default_timeout_kills_hung_subprocess`.
    #[test]
    fn default_timeout_kills_hung_subprocess() {
        let dir = tempfile::TempDir::new().unwrap();
        // Reads stdin then sleeps forever — simulates an operator script
        // that wedges after receiving its JSON input.
        let script = write_script(dir.path(), "hang.sh", "cat >/dev/null\nsleep 60\n");
        let exec = CustomExecutor::new(script);
        let start = Instant::now();
        let result = exec.execute(
            &make_params(&[
                ("name", "test"),
                // Simulates the runner injecting its per-resource default
                // for a custom resource that set no timeout itself.
                ("_default_timeout_secs", "1"),
            ]),
            false,
        );
        let elapsed = start.elapsed();
        // Killed at ~1s + a 5s SIGTERM→SIGKILL grace; generous slack for CI.
        assert!(
            elapsed < std::time::Duration::from_secs(20),
            "hung custom script should be killed via default timeout, took {:?}",
            elapsed
        );
        assert!(
            result.error.is_some(),
            "timed-out custom script should report an error"
        );
        let err = result.error.unwrap();
        assert!(
            err.contains("timed out"),
            "error should mention timeout, got: {}",
            err
        );
    }

    // An explicit `timeout:` on the resource wins over the injected default.
    #[test]
    fn explicit_timeout_kills_hung_subprocess() {
        let dir = tempfile::TempDir::new().unwrap();
        let script = write_script(dir.path(), "hang2.sh", "cat >/dev/null\nsleep 60\n");
        let exec = CustomExecutor::new(script);
        let start = Instant::now();
        let result = exec.execute(
            &make_params(&[
                ("name", "test"),
                ("timeout", "1"),
                ("_default_timeout_secs", "300"),
            ]),
            false,
        );
        assert!(
            start.elapsed() < std::time::Duration::from_secs(20),
            "explicit timeout should win and kill the hung script"
        );
        assert!(result.error.as_deref().unwrap().contains("timed out"));
    }
}
