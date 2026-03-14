// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Exec executor — runs commands with idempotency guards.
//!
//! Supports:
//! - `creates`: skip if file exists
//! - `onlyif`: skip if guard command fails (non-zero)
//! - `unless`: skip if guard command succeeds (zero)
//! - `stdin`: pipe value to command's stdin (keeps secrets out of process args)
//! - `secret_file`: write to 0600 temp file, replace `{{secret_file}}` in command

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use std::collections::HashMap;
use std::fs;
use std::io::Write;
use std::path::Path;
use std::time::Instant;

/// Runs commands with idempotency guards.
pub struct ExecExecutor {
    pub runner: Box<dyn CommandRunner>,
}

impl ExecExecutor {
    pub fn new() -> Self {
        Self {
            runner: Box::new(DefaultRunner),
        }
    }
}

impl Executor for ExecExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let command = params.get("command").map(|s| s.as_str()).unwrap_or("");
        let mut result = ResourceResult::new("exec", command);

        let res = execute_exec(&self.runner, params, dry_run, &mut result);
        result.duration_ms = start.elapsed().as_millis() as i32;
        if let Err(e) = res {
            result.error = Some(e);
        }
        result
    }
}

fn execute_exec(
    runner: &Box<dyn CommandRunner>,
    params: &HashMap<String, String>,
    dry_run: bool,
    result: &mut ResourceResult,
) -> Result<(), String> {
    let command = params
        .get("command")
        .filter(|s| !s.is_empty())
        .ok_or_else(|| "exec executor: command param is required".to_string())?;

    let has_stdin = params.get("stdin").map(|s| !s.is_empty()).unwrap_or(false);
    let has_secret_file = params
        .get("secret_file")
        .map(|s| !s.is_empty())
        .unwrap_or(false);

    if has_stdin && has_secret_file {
        return Err("exec executor: stdin and secret_file are mutually exclusive".to_string());
    }

    // Skip all guards when triggered by notify/subscribes.
    let triggered = params.get("_triggered").map(|s| s == "true").unwrap_or(false);

    // Exec resources with subscribes but no guards only run when triggered.
    let has_guards = params.contains_key("creates")
        || params.contains_key("onlyif")
        || params.contains_key("unless");
    let is_subscriber = params
        .get("_subscribes")
        .map(|s| s == "true")
        .unwrap_or(false);
    let is_secret_subscriber = params
        .get("_subscribes_secret")
        .map(|s| s == "true")
        .unwrap_or(false);

    if (is_subscriber || is_secret_subscriber) && !triggered && !has_guards {
        result.action = Action::Skipped;
        result.changed = false;
        return Ok(());
    }

    if !triggered {
        // Guard 1: creates — skip if file exists.
        if let Some(creates) = params.get("creates") {
            if !creates.is_empty() && Path::new(creates).exists() {
                result.action = Action::Skipped;
                result.changed = false;
                return Ok(());
            }
        }

        // Guards 2 and 3 require running commands. Skip during dry-run.
        if !dry_run {
            // Guard 2: onlyif — skip if command fails.
            if let Some(onlyif) = params.get("onlyif") {
                if !onlyif.is_empty() {
                    let parts: Vec<&str> = onlyif.split_whitespace().collect();
                    if !parts.is_empty() {
                        let out = runner.run(parts[0], &parts[1..]);
                        match out {
                            Ok(o) if o.status.success() => {} // guard passed
                            _ => {
                                result.action = Action::Skipped;
                                result.changed = false;
                                return Ok(());
                            }
                        }
                    }
                }
            }

            // Guard 3: unless — skip if command succeeds.
            if let Some(unless) = params.get("unless") {
                if !unless.is_empty() {
                    let parts: Vec<&str> = unless.split_whitespace().collect();
                    if !parts.is_empty() {
                        let out = runner.run(parts[0], &parts[1..]);
                        if let Ok(o) = out {
                            if o.status.success() {
                                result.action = Action::Skipped;
                                result.changed = false;
                                return Ok(());
                            }
                        }
                    }
                }
            }
        }
    }

    // Command would run.
    result.action = Action::Created;
    result.changed = true;

    if dry_run {
        return Ok(());
    }

    // Execute the actual command.
    let output = if has_stdin {
        let stdin_val = params.get("stdin").unwrap();
        let parts: Vec<&str> = command.split_whitespace().collect();
        runner
            .run_with_stdin(stdin_val.as_bytes(), parts[0], &parts[1..])
            .map_err(|e| format!("exec {}: {}", command, e))?
    } else if has_secret_file {
        run_with_secret_file(runner, command, params.get("secret_file").unwrap())?
    } else {
        let parts: Vec<&str> = command.split_whitespace().collect();
        runner
            .run(parts[0], &parts[1..])
            .map_err(|e| format!("exec {}: {}", command, e))?
    };

    if !output.status.success() {
        let stderr = String::from_utf8_lossy(&output.stderr);
        let stdout = String::from_utf8_lossy(&output.stdout);
        return Err(format!("exec {}: {}{}", command, stderr, stdout));
    }

    Ok(())
}

/// Write secret to 0600 temp file, replace {{secret_file}} in command, run, cleanup.
fn run_with_secret_file(
    runner: &Box<dyn CommandRunner>,
    command: &str,
    secret: &str,
) -> Result<std::process::Output, String> {
    let tmp_dir = std::env::temp_dir();
    let tmp_path = tmp_dir.join(format!("hobnob-secret-{}", uuid::Uuid::new_v4()));
    let tmp_path_str = tmp_path.to_string_lossy().to_string();

    {
        let mut f = fs::File::create(&tmp_path)
            .map_err(|e| format!("creating secret temp file: {}", e))?;
        crate::platform::permissions::set_owner_only(&tmp_path)
            .map_err(|e| format!("setting secret file permissions: {}", e))?;
        f.write_all(secret.as_bytes())
            .map_err(|e| format!("writing secret file: {}", e))?;
    }

    let resolved = command.replace("{{secret_file}}", &tmp_path_str);
    let parts: Vec<&str> = resolved.split_whitespace().collect();
    let result = runner.run(parts[0], &parts[1..]);

    // Always clean up.
    let _ = fs::remove_file(&tmp_path);

    result.map_err(|e| format!("exec {}: {}", command, e))
}

#[cfg(test)]
mod tests {
    use super::*;
    fn make_params(pairs: &[(&str, &str)]) -> HashMap<String, String> {
        pairs
            .iter()
            .map(|(k, v)| (k.to_string(), v.to_string()))
            .collect()
    }

    #[test]
    fn missing_command_errors() {
        let exec = ExecExecutor::new();
        let result = exec.execute(&make_params(&[]), false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("command"));
    }

    #[test]
    fn creates_guard_skips_when_file_exists() {
        let dir = tempfile::TempDir::new().unwrap();
        let guard_file = dir.path().join("guard.txt");
        fs::write(&guard_file, "exists").unwrap();

        let exec = ExecExecutor::new();
        let result = exec.execute(
            &make_params(&[
                ("command", "echo hello"),
                ("creates", guard_file.to_str().unwrap()),
            ]),
            false,
        );

        assert_eq!(result.action, Action::Skipped);
        assert!(!result.changed);
    }

    #[test]
    fn subscriber_without_guards_skips() {
        let exec = ExecExecutor::new();
        let result = exec.execute(
            &make_params(&[
                ("command", "echo hello"),
                ("_subscribes", "true"),
            ]),
            false,
        );

        assert_eq!(result.action, Action::Skipped);
        assert!(!result.changed);
    }

    #[test]
    fn subscriber_triggered_runs() {
        // When triggered, subscriber should attempt to run.
        // With the default runner this will try to execute "echo" which should work.
        let exec = ExecExecutor::new();
        let result = exec.execute(
            &make_params(&[
                ("command", "echo hello"),
                ("_subscribes", "true"),
                ("_triggered", "true"),
            ]),
            false,
        );

        assert_eq!(result.action, Action::Created);
        assert!(result.changed);
    }

    #[test]
    fn secret_subscriber_without_guards_skips() {
        let exec = ExecExecutor::new();
        let result = exec.execute(
            &make_params(&[
                ("command", "echo hello"),
                ("_subscribes_secret", "true"),
            ]),
            false,
        );

        assert_eq!(result.action, Action::Skipped);
        assert!(!result.changed);
    }

    #[test]
    fn secret_subscriber_triggered_runs() {
        let exec = ExecExecutor::new();
        let result = exec.execute(
            &make_params(&[
                ("command", "echo hello"),
                ("_subscribes_secret", "true"),
                ("_triggered", "true"),
            ]),
            false,
        );

        assert_eq!(result.action, Action::Created);
        assert!(result.changed);
    }

    #[test]
    fn dry_run_reports_action() {
        let exec = ExecExecutor::new();
        let result = exec.execute(
            &make_params(&[("command", "echo hello")]),
            true,
        );

        assert_eq!(result.action, Action::Created);
        assert!(result.changed);
    }

    #[test]
    fn stdin_and_secret_file_mutually_exclusive() {
        let exec = ExecExecutor::new();
        let result = exec.execute(
            &make_params(&[
                ("command", "cat"),
                ("stdin", "secret_value"),
                ("secret_file", "also_secret"),
            ]),
            false,
        );

        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("mutually exclusive"));
    }
}
