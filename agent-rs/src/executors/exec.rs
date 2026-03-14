// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0

//! Exec executor — runs commands with idempotency guards.
//!
//! Supports:
//! - `creates`: skip if file exists
//! - `onlyif`: skip if guard command fails (non-zero)
//! - `unless`: skip if guard command succeeds (zero)
//! - `stdin`: pipe value to command's stdin (keeps secrets out of process args)
//! - `secret_file`: write to 0600 temp file, replace `{{secret_file}}` in command
//! - `cwd`: set working directory before running the command (and guards)
//! - `user`: run command as a different user via `su` (Unix only, and guards)
//! - `environment`: comma-separated `KEY=VALUE` env vars, prepended via `env` (and guards)
//! - `returns`: comma-separated acceptable exit codes (default: `"0"`)
//! - `refreshonly`: when `"true"`, only execute if triggered by notify/subscribes
//! - `logoutput`: when `"false"`, suppress stdout/stderr from error messages

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

/// Parse acceptable exit codes from a comma-separated string.
/// Returns `vec![0]` if the param is absent or empty.
fn parse_returns(params: &HashMap<String, String>) -> Result<Vec<i32>, String> {
    match params.get("returns").filter(|s| !s.is_empty()) {
        None => Ok(vec![0]),
        Some(s) => {
            let mut codes = Vec::new();
            for part in s.split(',') {
                let trimmed = part.trim();
                if trimmed.is_empty() {
                    continue;
                }
                let code: i32 = trimmed.parse().map_err(|_| {
                    format!(
                        "exec executor: invalid exit code '{}' in returns param",
                        trimmed
                    )
                })?;
                codes.push(code);
            }
            if codes.is_empty() {
                Ok(vec![0])
            } else {
                Ok(codes)
            }
        }
    }
}

/// Parse `environment` param into a list of (KEY, VALUE) pairs.
/// Format: comma-separated `KEY=VALUE` entries.
fn parse_environment(params: &HashMap<String, String>) -> Result<Vec<(String, String)>, String> {
    match params.get("environment").filter(|s| !s.is_empty()) {
        None => Ok(Vec::new()),
        Some(s) => {
            let mut pairs = Vec::new();
            for entry in s.split(',') {
                let trimmed = entry.trim();
                if trimmed.is_empty() {
                    continue;
                }
                if let Some(eq_pos) = trimmed.find('=') {
                    let key = trimmed[..eq_pos].trim();
                    let value = trimmed[eq_pos + 1..].trim();
                    if key.is_empty() {
                        return Err(format!(
                            "exec executor: empty key in environment entry '{}'",
                            trimmed
                        ));
                    }
                    pairs.push((key.to_string(), value.to_string()));
                } else {
                    return Err(format!(
                        "exec executor: invalid environment entry '{}' (expected KEY=VALUE)",
                        trimmed
                    ));
                }
            }
            Ok(pairs)
        }
    }
}

/// Shell-quote a string for safe inclusion in sh -c commands.
/// Wraps in single quotes, escaping any embedded single quotes.
fn shell_quote(s: &str) -> String {
    format!("'{}'", s.replace('\'', "'\\''"))
}

/// Build the command arguments to run a command string through `sh -c`,
/// with optional cwd, user, and environment wrapping.
///
/// Returns (program, args) to pass to runner.run().
fn build_wrapped_command(
    command: &str,
    cwd: Option<&str>,
    user: Option<&str>,
    env_pairs: &[(String, String)],
) -> (String, Vec<String>) {
    // Start with the bare command.
    let mut shell_cmd = String::new();

    // Prepend env vars if any.
    if !env_pairs.is_empty() {
        for (k, v) in env_pairs {
            shell_cmd.push_str(&format!("export {}={}; ", shell_quote(k), shell_quote(v)));
        }
    }

    // Prepend cd if cwd is set.
    if let Some(dir) = cwd {
        shell_cmd.push_str(&format!("cd {} && ", shell_quote(dir)));
    }

    shell_cmd.push_str(command);

    // Wrap with user if set.
    if let Some(u) = user {
        // Use su to run as the target user.
        return (
            "su".to_string(),
            vec![
                "-".to_string(),
                u.to_string(),
                "-c".to_string(),
                shell_cmd,
            ],
        );
    }

    ("sh".to_string(), vec!["-c".to_string(), shell_cmd])
}

/// Check if an exit code is in the acceptable list.
fn exit_code_acceptable(output: &std::process::Output, acceptable: &[i32]) -> bool {
    match output.status.code() {
        Some(code) => acceptable.contains(&code),
        None => false, // killed by signal — never acceptable
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

    // Parse new params early so we fail fast on bad input.
    let acceptable_codes = parse_returns(params)?;
    let env_pairs = parse_environment(params)?;
    let logoutput = params
        .get("logoutput")
        .map(|s| s != "false")
        .unwrap_or(true);

    // Validate cwd exists if specified.
    let cwd = params.get("cwd").filter(|s| !s.is_empty()).map(|s| s.as_str());
    if let Some(dir) = cwd {
        if !Path::new(dir).is_dir() {
            return Err(format!(
                "exec executor: cwd '{}' does not exist or is not a directory",
                dir
            ));
        }
    }

    // Validate user param: not supported on Windows.
    let user = params.get("user").filter(|s| !s.is_empty()).map(|s| s.as_str());
    if user.is_some() {
        #[cfg(windows)]
        {
            return Err("exec executor: user param is not supported on Windows".to_string());
        }
    }

    // Phase 2A: refreshonly — only execute when triggered.
    let refreshonly = params
        .get("refreshonly")
        .map(|s| s == "true")
        .unwrap_or(false);

    // Skip all guards when triggered by notify/subscribes.
    let triggered = params.get("_triggered").map(|s| s == "true").unwrap_or(false);

    if refreshonly {
        if !triggered {
            result.action = Action::Skipped;
            result.changed = false;
            return Ok(());
        }
        // When refreshonly and triggered, skip ALL guards and run the command.
    }

    // Exec resources with subscribes but no guards only run when triggered.
    let has_guards = params.contains_key("creates")
        || params.contains_key("onlyif")
        || params.contains_key("unless");
    let is_subscriber = params
        .get("_subscribes")
        .map(|s| s == "true")
        .unwrap_or(false);
    let is_secret_subscriber = params
        .get("_watch_secret")
        .map(|s| s == "true")
        .unwrap_or(false);

    if (is_subscriber || is_secret_subscriber) && !triggered && !has_guards {
        result.action = Action::Skipped;
        result.changed = false;
        return Ok(());
    }

    // Evaluate guards (skipped when triggered or refreshonly+triggered).
    if !triggered && !refreshonly {
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
            // Note: split_whitespace() is intentional here — guards run as direct
            // exec (not shell), matching the main command path. Arguments with
            // spaces are not supported in guard commands; use shell-wrapped
            // commands via the main `command:` param if needed.
            if let Some(onlyif) = params.get("onlyif") {
                if !onlyif.is_empty() {
                    let (prog, args) =
                        build_wrapped_command(onlyif, cwd, user, &env_pairs);
                    let arg_refs: Vec<&str> = args.iter().map(|s| s.as_str()).collect();
                    let out = runner.run(&prog, &arg_refs);
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

            // Guard 3: unless — skip if command succeeds.
            if let Some(unless) = params.get("unless") {
                if !unless.is_empty() {
                    let (prog, args) =
                        build_wrapped_command(unless, cwd, user, &env_pairs);
                    let arg_refs: Vec<&str> = args.iter().map(|s| s.as_str()).collect();
                    let out = runner.run(&prog, &arg_refs);
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

    // Command would run.
    result.action = Action::Created;
    result.changed = true;

    if dry_run {
        return Ok(());
    }

    // Execute the actual command.
    let output = if has_stdin {
        let stdin_val = params.get("stdin").unwrap();
        let (prog, args) = build_wrapped_command(command, cwd, user, &env_pairs);
        let arg_refs: Vec<&str> = args.iter().map(|s| s.as_str()).collect();
        runner
            .run_with_stdin(stdin_val.as_bytes(), &prog, &arg_refs)
            .map_err(|e| format!("exec {}: {}", command, e))?
    } else if has_secret_file {
        run_with_secret_file(runner, command, params.get("secret_file").unwrap(), cwd, user, &env_pairs)?
    } else {
        let (prog, args) = build_wrapped_command(command, cwd, user, &env_pairs);
        let arg_refs: Vec<&str> = args.iter().map(|s| s.as_str()).collect();
        runner
            .run(&prog, &arg_refs)
            .map_err(|e| format!("exec {}: {}", command, e))?
    };

    if !exit_code_acceptable(&output, &acceptable_codes) {
        if logoutput {
            let stderr = String::from_utf8_lossy(&output.stderr);
            let stdout = String::from_utf8_lossy(&output.stdout);
            return Err(format!("exec {}: {}{}", command, stderr, stdout));
        } else {
            return Err(format!("exec {}: [output suppressed]", command));
        }
    }

    Ok(())
}

/// Write secret to 0600 temp file, replace {{secret_file}} in command, run, cleanup.
fn run_with_secret_file(
    runner: &Box<dyn CommandRunner>,
    command: &str,
    secret: &str,
    cwd: Option<&str>,
    user: Option<&str>,
    env_pairs: &[(String, String)],
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
    let (prog, args) = build_wrapped_command(&resolved, cwd, user, env_pairs);
    let arg_refs: Vec<&str> = args.iter().map(|s| s.as_str()).collect();
    let result = runner.run(&prog, &arg_refs);

    // Always clean up.
    let _ = fs::remove_file(&tmp_path);

    result.map_err(|e| format!("exec {}: {}", command, e))
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::executors::test_support::mock::{ok_output, MockRunner};
    use std::os::unix::process::ExitStatusExt;

    fn make_params(pairs: &[(&str, &str)]) -> HashMap<String, String> {
        pairs
            .iter()
            .map(|(k, v)| (k.to_string(), v.to_string()))
            .collect()
    }

    fn make_output_with_code(code: i32, stdout: &str, stderr: &str) -> std::process::Output {
        std::process::Output {
            status: std::process::ExitStatus::from_raw(code * 256),
            stdout: stdout.as_bytes().to_vec(),
            stderr: stderr.as_bytes().to_vec(),
        }
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
                ("_watch_secret", "true"),
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
                ("_watch_secret", "true"),
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

    // ---- Phase 1A tests ----

    #[test]
    fn cwd_invalid_directory_errors() {
        let exec = ExecExecutor::new();
        let result = exec.execute(
            &make_params(&[
                ("command", "echo hello"),
                ("cwd", "/nonexistent/path/that/does/not/exist"),
            ]),
            false,
        );

        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("does not exist"));
    }

    #[test]
    fn cwd_wraps_command_with_cd() {
        let dir = tempfile::TempDir::new().unwrap();
        let dir_path = dir.path().to_str().unwrap().to_string();

        let mock = MockRunner::new(vec![ok_output("")]);
        let calls = mock.calls.clone();

        let exec = ExecExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("command", "pwd"),
                ("cwd", &dir_path),
            ]),
            false,
        );

        assert!(result.error.is_none(), "error: {:?}", result.error);
        assert_eq!(result.action, Action::Created);

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1);
        assert_eq!(calls[0].0, "sh");
        assert_eq!(calls[0].1[0], "-c");
        assert!(
            calls[0].1[1].contains(&format!("cd '{}'", dir_path)),
            "expected cd in command, got: {}",
            calls[0].1[1]
        );
        assert!(calls[0].1[1].contains("pwd"));
    }

    #[test]
    fn cwd_applied_to_guards() {
        let dir = tempfile::TempDir::new().unwrap();
        let dir_path = dir.path().to_str().unwrap().to_string();

        // First call is the onlyif guard (success), second is the main command (success).
        let mock = MockRunner::new(vec![ok_output(""), ok_output("")]);
        let calls = mock.calls.clone();

        let exec = ExecExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("command", "echo hello"),
                ("cwd", &dir_path),
                ("onlyif", "test -f something"),
            ]),
            false,
        );

        assert!(result.error.is_none(), "error: {:?}", result.error);

        let calls = calls.lock().unwrap();
        // Both guard and command should have cd prefix.
        assert_eq!(calls.len(), 2);
        for call in calls.iter() {
            assert_eq!(call.0, "sh");
            assert!(
                call.1[1].contains(&format!("cd '{}'", dir_path)),
                "expected cd in: {}",
                call.1[1]
            );
        }
    }

    #[test]
    fn user_wraps_command_with_su() {
        // Use the mock runner so we don't actually run su.
        let mock = MockRunner::new(vec![ok_output("")]);
        let calls = mock.calls.clone();

        let exec = ExecExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("command", "whoami"),
                ("user", "nobody"),
            ]),
            false,
        );

        assert!(result.error.is_none(), "error: {:?}", result.error);

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1);
        assert_eq!(calls[0].0, "su");
        assert_eq!(calls[0].1[0], "-");
        assert_eq!(calls[0].1[1], "nobody");
        assert_eq!(calls[0].1[2], "-c");
        assert!(calls[0].1[3].contains("whoami"));
    }

    #[test]
    fn user_applied_to_guards() {
        // onlyif guard + main command, both wrapped with su.
        let mock = MockRunner::new(vec![ok_output(""), ok_output("")]);
        let calls = mock.calls.clone();

        let exec = ExecExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("command", "echo hello"),
                ("user", "deploy"),
                ("onlyif", "test -f /tmp/go"),
            ]),
            false,
        );

        assert!(result.error.is_none(), "error: {:?}", result.error);

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 2);
        for call in calls.iter() {
            assert_eq!(call.0, "su");
            assert_eq!(call.1[1], "deploy");
        }
    }

    #[test]
    fn environment_wraps_command_with_exports() {
        let mock = MockRunner::new(vec![ok_output("")]);
        let calls = mock.calls.clone();

        let exec = ExecExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("command", "env"),
                ("environment", "FOO=bar,BAZ=qux"),
            ]),
            false,
        );

        assert!(result.error.is_none(), "error: {:?}", result.error);

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1);
        assert_eq!(calls[0].0, "sh");
        let cmd = &calls[0].1[1];
        assert!(cmd.contains("export 'FOO'='bar'"), "expected FOO export in: {}", cmd);
        assert!(cmd.contains("export 'BAZ'='qux'"), "expected BAZ export in: {}", cmd);
        assert!(cmd.contains("env"), "expected env command in: {}", cmd);
    }

    #[test]
    fn environment_invalid_entry_errors() {
        let exec = ExecExecutor::new();
        let result = exec.execute(
            &make_params(&[
                ("command", "echo hello"),
                ("environment", "BADENTRY"),
            ]),
            false,
        );

        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("expected KEY=VALUE"));
    }

    #[test]
    fn environment_empty_key_errors() {
        let exec = ExecExecutor::new();
        let result = exec.execute(
            &make_params(&[
                ("command", "echo hello"),
                ("environment", "=nokey"),
            ]),
            false,
        );

        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("empty key"));
    }

    #[test]
    fn environment_applied_to_guards() {
        // onlyif guard + main command, both should have env exports.
        let mock = MockRunner::new(vec![ok_output(""), ok_output("")]);
        let calls = mock.calls.clone();

        let exec = ExecExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("command", "echo hello"),
                ("environment", "APP_ENV=test"),
                ("onlyif", "test -f /tmp/go"),
            ]),
            false,
        );

        assert!(result.error.is_none(), "error: {:?}", result.error);

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 2);
        for call in calls.iter() {
            assert!(
                call.1[1].contains("export 'APP_ENV'='test'"),
                "expected env export in: {}",
                call.1[1]
            );
        }
    }

    #[test]
    fn returns_accepts_nonzero_exit_code() {
        // Command exits with code 2, which should be acceptable.
        let mock = MockRunner::new(vec![make_output_with_code(2, "ok", "")]);

        let exec = ExecExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("command", "some-command"),
                ("returns", "0,2"),
            ]),
            false,
        );

        assert!(result.error.is_none(), "error: {:?}", result.error);
        assert_eq!(result.action, Action::Created);
    }

    #[test]
    fn returns_rejects_unexpected_exit_code() {
        // Command exits with code 3, only 0 and 2 are acceptable.
        let mock = MockRunner::new(vec![make_output_with_code(3, "", "failed")]);

        let exec = ExecExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("command", "some-command"),
                ("returns", "0,2"),
            ]),
            false,
        );

        assert!(result.error.is_some());
    }

    #[test]
    fn returns_default_rejects_nonzero() {
        // No returns param, command exits with code 1.
        let mock = MockRunner::new(vec![make_output_with_code(1, "", "error")]);

        let exec = ExecExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("command", "failing-command")]),
            false,
        );

        assert!(result.error.is_some());
    }

    #[test]
    fn returns_invalid_code_errors() {
        let exec = ExecExecutor::new();
        let result = exec.execute(
            &make_params(&[
                ("command", "echo hello"),
                ("returns", "0,abc"),
            ]),
            false,
        );

        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("invalid exit code"));
    }

    // ---- Phase 2A tests ----

    #[test]
    fn refreshonly_skips_when_not_triggered() {
        let exec = ExecExecutor::new();
        let result = exec.execute(
            &make_params(&[
                ("command", "echo hello"),
                ("refreshonly", "true"),
            ]),
            false,
        );

        assert_eq!(result.action, Action::Skipped);
        assert!(!result.changed);
        assert!(result.error.is_none());
    }

    #[test]
    fn refreshonly_runs_when_triggered() {
        let mock = MockRunner::new(vec![ok_output("done")]);

        let exec = ExecExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("command", "systemctl restart app"),
                ("refreshonly", "true"),
                ("_triggered", "true"),
            ]),
            false,
        );

        assert_eq!(result.action, Action::Created);
        assert!(result.changed);
        assert!(result.error.is_none());
    }

    #[test]
    fn refreshonly_skips_guards_when_triggered() {
        // With refreshonly + triggered, guards should be skipped entirely.
        // Provide an unless guard that would normally cause a skip.
        let mock = MockRunner::new(vec![ok_output("done")]);
        let calls = mock.calls.clone();

        let exec = ExecExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("command", "echo hello"),
                ("refreshonly", "true"),
                ("_triggered", "true"),
                ("unless", "true"),  // would normally skip
            ]),
            false,
        );

        assert_eq!(result.action, Action::Created);
        assert!(result.changed);

        // Only one call (the main command), guard was skipped.
        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1);
    }

    // ---- Phase 3A tests ----

    #[test]
    fn logoutput_false_suppresses_output() {
        let mock = MockRunner::new(vec![make_output_with_code(1, "secret data", "secret error")]);

        let exec = ExecExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("command", "failing-command"),
                ("logoutput", "false"),
            ]),
            false,
        );

        assert!(result.error.is_some());
        let err = result.error.unwrap();
        assert!(
            err.contains("[output suppressed]"),
            "expected suppressed output, got: {}",
            err
        );
        assert!(!err.contains("secret data"), "stdout should be suppressed");
        assert!(
            !err.contains("secret error"),
            "stderr should be suppressed"
        );
    }

    #[test]
    fn logoutput_default_includes_output() {
        let mock = MockRunner::new(vec![make_output_with_code(1, "visible stdout", "visible stderr")]);

        let exec = ExecExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("command", "failing-command")]),
            false,
        );

        assert!(result.error.is_some());
        let err = result.error.unwrap();
        assert!(err.contains("visible stderr"), "stderr should be present");
        assert!(err.contains("visible stdout"), "stdout should be present");
    }

    #[test]
    fn logoutput_true_includes_output() {
        let mock = MockRunner::new(vec![make_output_with_code(1, "out", "err")]);

        let exec = ExecExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("command", "failing-command"),
                ("logoutput", "true"),
            ]),
            false,
        );

        assert!(result.error.is_some());
        let err = result.error.unwrap();
        assert!(err.contains("err"), "stderr should be present");
        assert!(err.contains("out"), "stdout should be present");
    }

    // ---- Combined param tests ----

    #[test]
    fn cwd_and_environment_combined() {
        let dir = tempfile::TempDir::new().unwrap();
        let dir_path = dir.path().to_str().unwrap().to_string();

        let mock = MockRunner::new(vec![ok_output("")]);
        let calls = mock.calls.clone();

        let exec = ExecExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("command", "make build"),
                ("cwd", &dir_path),
                ("environment", "CC=gcc,CFLAGS=-O2"),
            ]),
            false,
        );

        assert!(result.error.is_none(), "error: {:?}", result.error);

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1);
        let cmd = &calls[0].1[1];
        assert!(cmd.contains("export 'CC'='gcc'"), "expected CC in: {}", cmd);
        assert!(cmd.contains("cd"), "expected cd in: {}", cmd);
        assert!(cmd.contains("make build"), "expected command in: {}", cmd);
    }

    #[test]
    fn cwd_user_environment_combined() {
        let dir = tempfile::TempDir::new().unwrap();
        let dir_path = dir.path().to_str().unwrap().to_string();

        let mock = MockRunner::new(vec![ok_output("")]);
        let calls = mock.calls.clone();

        let exec = ExecExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("command", "make build"),
                ("cwd", &dir_path),
                ("user", "deploy"),
                ("environment", "HOME=/home/deploy"),
            ]),
            false,
        );

        assert!(result.error.is_none(), "error: {:?}", result.error);

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1);
        // When user is set, command is wrapped with su.
        assert_eq!(calls[0].0, "su");
        assert_eq!(calls[0].1[1], "deploy");
        let cmd = &calls[0].1[3];
        assert!(cmd.contains("export 'HOME'='/home/deploy'"), "expected env in: {}", cmd);
        assert!(cmd.contains(&format!("cd '{}'", dir_path)), "expected cd in: {}", cmd);
        assert!(cmd.contains("make build"), "expected command in: {}", cmd);
    }

    // ---- Helper function tests ----

    #[test]
    fn parse_returns_defaults_to_zero() {
        let params = make_params(&[]);
        let codes = parse_returns(&params).unwrap();
        assert_eq!(codes, vec![0]);
    }

    #[test]
    fn parse_returns_multiple_codes() {
        let params = make_params(&[("returns", "0,1,2")]);
        let codes = parse_returns(&params).unwrap();
        assert_eq!(codes, vec![0, 1, 2]);
    }

    #[test]
    fn parse_returns_with_spaces() {
        let params = make_params(&[("returns", "0, 2 , 5")]);
        let codes = parse_returns(&params).unwrap();
        assert_eq!(codes, vec![0, 2, 5]);
    }

    #[test]
    fn parse_environment_empty() {
        let params = make_params(&[]);
        let pairs = parse_environment(&params).unwrap();
        assert!(pairs.is_empty());
    }

    #[test]
    fn parse_environment_single() {
        let params = make_params(&[("environment", "FOO=bar")]);
        let pairs = parse_environment(&params).unwrap();
        assert_eq!(pairs, vec![("FOO".to_string(), "bar".to_string())]);
    }

    #[test]
    fn parse_environment_multiple() {
        let params = make_params(&[("environment", "A=1,B=2,C=3")]);
        let pairs = parse_environment(&params).unwrap();
        assert_eq!(pairs.len(), 3);
        assert_eq!(pairs[0], ("A".to_string(), "1".to_string()));
        assert_eq!(pairs[1], ("B".to_string(), "2".to_string()));
        assert_eq!(pairs[2], ("C".to_string(), "3".to_string()));
    }

    #[test]
    fn parse_environment_value_with_equals() {
        let params = make_params(&[("environment", "DSN=postgres://user:pass@host/db")]);
        let pairs = parse_environment(&params).unwrap();
        assert_eq!(pairs.len(), 1);
        assert_eq!(pairs[0].0, "DSN");
        assert_eq!(pairs[0].1, "postgres://user:pass@host/db");
    }

    #[test]
    fn shell_quote_simple() {
        assert_eq!(shell_quote("hello"), "'hello'");
    }

    #[test]
    fn shell_quote_with_single_quotes() {
        assert_eq!(shell_quote("it's"), "'it'\\''s'");
    }

    #[test]
    fn build_wrapped_command_basic() {
        let (prog, args) = build_wrapped_command("echo hello", None, None, &[]);
        assert_eq!(prog, "sh");
        assert_eq!(args, vec!["-c", "echo hello"]);
    }

    #[test]
    fn build_wrapped_command_with_cwd() {
        let (prog, args) = build_wrapped_command("echo hello", Some("/tmp"), None, &[]);
        assert_eq!(prog, "sh");
        assert_eq!(args[0], "-c");
        assert!(args[1].contains("cd '/tmp'"));
        assert!(args[1].contains("echo hello"));
    }

    #[test]
    fn build_wrapped_command_with_user() {
        let (prog, args) = build_wrapped_command("whoami", None, Some("root"), &[]);
        assert_eq!(prog, "su");
        assert_eq!(args[0], "-");
        assert_eq!(args[1], "root");
        assert_eq!(args[2], "-c");
        assert!(args[3].contains("whoami"));
    }

    #[test]
    fn build_wrapped_command_with_env() {
        let env = vec![("FOO".to_string(), "bar".to_string())];
        let (prog, args) = build_wrapped_command("env", None, None, &env);
        assert_eq!(prog, "sh");
        assert!(args[1].contains("export 'FOO'='bar'"));
        assert!(args[1].contains("env"));
    }

    #[test]
    fn build_wrapped_command_all_options() {
        let env = vec![("X".to_string(), "1".to_string())];
        let (prog, args) =
            build_wrapped_command("make", Some("/build"), Some("builder"), &env);
        assert_eq!(prog, "su");
        assert_eq!(args[1], "builder");
        let cmd = &args[3];
        assert!(cmd.contains("export 'X'='1'"));
        assert!(cmd.contains("cd '/build'"));
        assert!(cmd.contains("make"));
    }

    #[test]
    fn stdin_with_cwd_works() {
        let dir = tempfile::TempDir::new().unwrap();
        let dir_path = dir.path().to_str().unwrap().to_string();

        let mock = MockRunner::new(vec![ok_output("piped")]);
        let calls = mock.calls.clone();

        let exec = ExecExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("command", "cat"),
                ("stdin", "hello"),
                ("cwd", &dir_path),
            ]),
            false,
        );

        assert!(result.error.is_none(), "error: {:?}", result.error);

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1);
        let cmd = &calls[0].1[1];
        assert!(cmd.contains("cd"), "stdin with cwd should wrap with cd");
    }
}
