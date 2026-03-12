// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Custom executor — runs external scripts with a JSON protocol.
//!
//! The script receives `{"params": {...}, "dry_run": bool}` on stdin and must
//! write `{"action": "...", "changed": bool, "error": "..."}` to stdout.

use super::{Action, Executor, ResourceResult};
use std::collections::HashMap;
use std::io::Write;
use std::process::{Command, Stdio};
use std::time::Instant;

/// Runs an external script with JSON stdin/stdout protocol.
pub struct CustomExecutor {
    pub script_path: String,
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

        let child = Command::new(&self.script_path)
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .spawn();

        let mut child = match child {
            Ok(c) => c,
            Err(e) => {
                result.error = Some(format!("running {}: {}", self.script_path, e));
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
        };

        if let Some(ref mut stdin) = child.stdin {
            let _ = stdin.write_all(&input_json);
        }
        // Close stdin so the script knows input is complete.
        drop(child.stdin.take());

        let output = match child.wait_with_output() {
            Ok(o) => o,
            Err(e) => {
                result.error = Some(format!("running {}: {}", self.script_path, e));
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
