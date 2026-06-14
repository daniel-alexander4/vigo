// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Exec executor — runs commands with idempotency guards.
//!
//! Supports:
//! - `onlyif`: skip if guard command fails (non-zero)
//! - `unless`: skip if guard command succeeds (zero)
//! - `stdin`: pipe value to command's stdin (keeps secrets out of process args)
//! - `secret_file`: write to 0600 temp file, replace `{{secret_file}}` in command
//! - `cwd`: set working directory before running the command (and guards)
//! - `user`: run command as a different user via `su` (Unix only, and guards)
//! - `environment`: comma-separated `KEY=VALUE` env vars, prepended via `env` (and guards)
//! - `returns`: comma-separated acceptable exit codes (default: `"0"`)
//! - Use `when: "changed"` on the resource to only execute when triggered by notify/subscribes/watch_secret
//! - `logoutput`: when `"false"`, suppress stdout/stderr from error messages
//! - `timeout`: per-process execution ceiling (no default — unset means no timeout).
//!   Accepts a bare integer (seconds) or a duration suffix: `s`, `m`, `h`.
//!   On expiry the agent SIGTERMs the child's process group, waits 5s, then
//!   SIGKILLs — leaked children of a shell-spawned scan are killed too.
//!   Background execs clear their bg_pids slot on timeout so the next
//!   convergence cycle is free to respawn.
//! - `retries`: number of retry attempts on failure (default 0)
//! - `retry_delay`: seconds between retries (default 5)
//! - `sensitive`: when `"true"`, suppress command from result names and error messages
//! - `shell`: override default shell (e.g., `bash`, `zsh`; default `sh` on Unix, `cmd` on Windows)

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use std::collections::HashMap;
use std::fs;
use std::io::Write;
use std::path::Path;
use std::sync::{Arc, Mutex};
use std::time::Instant;
use tracing::warn;

/// Runs commands with idempotency guards.
pub struct ExecExecutor {
    pub runner: Box<dyn CommandRunner>,
    /// Tracks running background processes by resource name → child PID.
    /// Prevents re-spawning a background exec while a previous instance is still running.
    bg_pids: Arc<Mutex<HashMap<String, u32>>>,
}

impl ExecExecutor {
    pub fn new() -> Self {
        Self {
            runner: Box::new(DefaultRunner),
            bg_pids: Arc::new(Mutex::new(HashMap::new())),
        }
    }
}

impl Executor for ExecExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let command = params.get("command").map(|s| s.as_str()).unwrap_or("");
        let mut result = ResourceResult::new("exec", command);

        let res = execute_exec(
            self.runner.as_ref(),
            params,
            dry_run,
            &mut result,
            &self.bg_pids,
        );
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

/// Build the command arguments to run a command string through a shell,
/// with optional cwd, user, environment wrapping, and shell override.
///
/// Returns (program, args) to pass to runner.run().
fn build_wrapped_command(
    command: &str,
    cwd: Option<&str>,
    user: Option<&str>,
    env_pairs: &[(String, String)],
    shell: Option<&str>,
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

    // Default shell: sh on Unix, cmd on Windows.
    let default_shell = if cfg!(windows) { "cmd" } else { "sh" };
    let shell_prog = shell.unwrap_or(default_shell);

    // Windows cmd uses /C instead of -c.
    let flag = if shell_prog == "cmd" { "/C" } else { "-c" };

    (shell_prog.to_string(), vec![flag.to_string(), shell_cmd])
}

/// Check if an exit code is in the acceptable list.
fn exit_code_acceptable(output: &std::process::Output, acceptable: &[i32]) -> bool {
    match output.status.code() {
        Some(code) => acceptable.contains(&code),
        None => false, // killed by signal — never acceptable
    }
}

/// Parse a duration string for the `timeout:` exec parameter.
///
/// Accepts:
///   - bare integer (`"30"`) → seconds
///   - integer with trailing unit suffix: `s` seconds, `m` minutes,
///     `h` hours (`"30s"`, `"45m"`, `"4h"`).
///
/// Returns `None` on parse failure so the caller treats the parameter
/// as unset rather than failing the whole run — same ergonomics as
/// `nice:`/`retries:`/etc. in this file. A zero or negative value
/// also returns `None` to avoid zero-timeout logic traps.
pub(crate) fn parse_duration(s: &str) -> Option<std::time::Duration> {
    let s = s.trim();
    if s.is_empty() {
        return None;
    }
    let (num_part, multiplier_secs): (&str, u64) = if let Some(rest) = s.strip_suffix('h') {
        (rest, 3600)
    } else if let Some(rest) = s.strip_suffix('m') {
        (rest, 60)
    } else if let Some(rest) = s.strip_suffix('s') {
        (rest, 1)
    } else {
        (s, 1)
    };
    let n: u64 = num_part.trim().parse().ok()?;
    if n == 0 {
        return None;
    }
    n.checked_mul(multiplier_secs).map(std::time::Duration::from_secs)
}

fn execute_exec(
    runner: &dyn CommandRunner,
    params: &HashMap<String, String>,
    dry_run: bool,
    result: &mut ResourceResult,
    bg_pids: &Arc<Mutex<HashMap<String, u32>>>,
) -> Result<(), String> {
    let command = params
        .get("command")
        .filter(|s| !s.is_empty())
        .ok_or_else(|| "exec executor: command param is required".to_string())?;

    // sensitive: suppress command string from result names and error messages.
    let sensitive = params
        .get("sensitive")
        .map(|s| s == "true")
        .unwrap_or(false);

    // report_settled: when true and the command runs successfully, report
    // Action::None instead of Action::Created and set changed=false. The
    // operator's escape hatch for exec resources whose successful execution
    // is maintenance / cache-warming rather than a real state change —
    // keeps daily refreshers (apt-get update, etc.) from contaminating
    // ChangedCount and tripping drift detection. Failure path is unaffected:
    // a non-zero exit still produces a normal failure with error_message
    // and FailedCount > 0.
    let report_settled = params
        .get("report_settled")
        .map(|s| s == "true")
        .unwrap_or(false);
    if sensitive {
        result.resource_name = "[sensitive]".to_string();
    }

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

    // Retry params: retries = max retry attempts (0 = no retry), retry_delay = seconds between.
    let retries: u32 = params
        .get("retries")
        .and_then(|s| s.parse().ok())
        .unwrap_or(0);
    let retry_delay: u64 = params
        .get("retry_delay")
        .and_then(|s| s.parse().ok())
        .unwrap_or(5);

    // Shell override: e.g., "bash", "zsh", "fish".
    let shell = params.get("shell").filter(|s| !s.is_empty()).map(|s| s.as_str());

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

    // Skip all guards when triggered by notify/subscribes/watch_secret.
    let triggered = params.get("_triggered").map(|s| s == "true").unwrap_or(false);

    // Exec resources with subscribes but no guards only run when triggered.
    let has_guards = params.contains_key("onlyif")
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
        result.skip_reason = Some("not triggered".into());
        return Ok(());
    }

    // Evaluate guards (skipped when triggered).
    if !triggered {
        // Guards require running commands. Skip during dry-run.
        if !dry_run {
            // Guard 1: onlyif — skip if command fails.
            // Note: split_whitespace() is intentional here — guards run as direct
            // exec (not shell), matching the main command path. Arguments with
            // spaces are not supported in guard commands; use shell-wrapped
            // commands via the main `command:` param if needed.
            if let Some(onlyif) = params.get("onlyif") {
                if !onlyif.is_empty() {
                    let (prog, args) =
                        build_wrapped_command(onlyif, cwd, user, &env_pairs, shell);
                    let arg_refs: Vec<&str> = args.iter().map(|s| s.as_str()).collect();
                    let out = runner.run(&prog, &arg_refs);
                    match out {
                        Ok(o) if o.status.success() => {} // guard passed
                        _ => {
                            result.action = Action::Skipped;
                            result.changed = false;
                            result.skip_reason = Some("onlyif guard".into());
                            return Ok(());
                        }
                    }
                }
            }

            // Guard 2: unless — skip if command succeeds.
            if let Some(unless) = params.get("unless") {
                if !unless.is_empty() {
                    let (prog, args) =
                        build_wrapped_command(unless, cwd, user, &env_pairs, shell);
                    let arg_refs: Vec<&str> = args.iter().map(|s| s.as_str()).collect();
                    let out = runner.run(&prog, &arg_refs);
                    if let Ok(o) = out {
                        if o.status.success() {
                            result.action = Action::Skipped;
                            result.changed = false;
                            result.skip_reason = Some("unless guard".into());
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

    // Parse nice level: background execs default to 19 (lowest priority),
    // foreground execs default to 0 (normal). The `nice:` param overrides.
    let background = params.get("background").map(|s| s == "true").unwrap_or(false);
    let nice_default = if background { 19 } else { 0 };
    let nice_level = params
        .get("nice")
        .and_then(|s| s.parse::<i32>().ok())
        .unwrap_or(nice_default);

    // Parse optional per-process timeout. The operator-facing `timeout:` has
    // no default — realistic scan durations span three orders of magnitude
    // (seconds for chkrootkit, hours for aide on a big disk). When set,
    // foreground execs get killed on expiry; a background exec gets killed by
    // its reaper thread, which then clears the bg_pids slot so the next cycle
    // respawns.
    //
    // When `timeout:` is unset, foreground execs fall back to the runner's
    // computed per-resource default, injected as `_default_timeout_secs`. This
    // is the kill-on-timeout backstop for the hot convergence path: without it
    // a hung child under `Command::output()` would block the whole cycle (the
    // runner's recv_timeout detects the timeout but cannot kill the child).
    // Background execs are exempt — they return immediately and self-reap.
    let timeout_dur = params
        .get("timeout")
        .and_then(|s| parse_duration(s))
        .or_else(|| {
            if background {
                None
            } else {
                params
                    .get("_default_timeout_secs")
                    .and_then(|s| s.parse::<u64>().ok())
                    .filter(|&n| n > 0)
                    .map(std::time::Duration::from_secs)
            }
        });

    // Background mode: spawn the command without waiting for completion.
    if background {
        let resource_name = params.get("name").cloned().unwrap_or_default();

        // Skip if a previous background instance of this resource is still running.
        if !resource_name.is_empty() {
            if let Ok(pids) = bg_pids.lock() {
                if let Some(&pid) = pids.get(&resource_name) {
                    // Check if process is still alive (signal 0 = existence check).
                    #[cfg(unix)]
                    let alive = unsafe { libc::kill(pid as i32, 0) } == 0;
                    #[cfg(not(unix))]
                    let alive = false;
                    if alive {
                        result.action = Action::Skipped;
                        result.changed = false;
                        result.skip_reason =
                            Some(format!("background process still running (pid {})", pid));
                        return Ok(());
                    }
                }
            }
        }

        let (prog, args) = build_wrapped_command(command, cwd, user, &env_pairs, shell);
        let mut cmd = std::process::Command::new(&prog);
        cmd.args(&args)
            .stdin(std::process::Stdio::null())
            .stdout(std::process::Stdio::null())
            .stderr(std::process::Stdio::null());

        crate::platform::process::apply_nice(&mut cmd, nice_level);

        match cmd.spawn() {
            Ok(child) => {
                let pid = child.id();

                // Track the PID so we can skip duplicates on next cycle.
                if !resource_name.is_empty() {
                    if let Ok(mut pids) = bg_pids.lock() {
                        pids.insert(resource_name.clone(), pid);
                    }
                }

                // Reap the child in a background thread and clear the
                // tracking entry. When `timeout:` is set, the reaper
                // hands child.wait() to a helper thread and races the
                // wait against the timeout — on expiry it kills the
                // process tree and the bg_pids slot is released so the
                // next convergence cycle can respawn. No respawn happens
                // inside this tick; convergence already separates detection
                // from action.
                let bg_pids = Arc::clone(bg_pids);
                let timeout_for_reaper = timeout_dur;
                std::thread::spawn(move || {
                    let mut child = child;
                    match timeout_for_reaper {
                        Some(dur) => {
                            use std::sync::mpsc;
                            let (tx, rx) = mpsc::channel();
                            let pid_copy = pid;
                            std::thread::spawn(move || {
                                let _ = tx.send(child.wait());
                            });
                            if rx.recv_timeout(dur).is_err() {
                                // Fire-and-forget kill; the waiter thread above
                                // drains the zombie once the kernel reaps it.
                                let grace = std::time::Duration::from_secs(5);
                                crate::platform::process::kill_process_tree(pid_copy, grace);
                            }
                        }
                        None => {
                            let _ = child.wait();
                        }
                    }
                    if !resource_name.is_empty() {
                        if let Ok(mut pids) = bg_pids.lock() {
                            // Only remove if PID still matches (guard against rapid respawn race).
                            if pids.get(&resource_name) == Some(&pid) {
                                pids.remove(&resource_name);
                            }
                        }
                    }
                });
                return Ok(());
            }
            Err(e) => {
                let cmd_label = if sensitive { "[sensitive]" } else { command };
                return Err(format!("exec background {}: {}", cmd_label, e));
            }
        }
    }

    // Helper: format error messages, respecting sensitive + logoutput.
    let cmd_label = if sensitive { "[sensitive]" } else { command };
    let format_exec_error = |output: &std::process::Output| -> String {
        if sensitive {
            let code = output.status.code().map(|c| c.to_string()).unwrap_or_else(|| "signal".into());
            format!("exec command failed (exit {}) — command suppressed (sensitive: true)", code)
        } else if logoutput {
            let stderr = String::from_utf8_lossy(&output.stderr);
            let stdout = String::from_utf8_lossy(&output.stdout);
            format!("exec {}: {}{}", command, stderr, stdout)
        } else {
            format!("exec {}: [output suppressed]", command)
        }
    };

    // Execute the actual command, with optional retries.
    // Guards are NOT re-evaluated on retry — only the command itself.
    let max_attempts = 1 + retries;
    let mut last_error: Option<String> = None;

    for attempt in 0..max_attempts {
        if attempt > 0 {
            warn!(
                command = cmd_label,
                attempt,
                retries,
                retry_delay,
                "exec: retrying command"
            );
            std::thread::sleep(std::time::Duration::from_secs(retry_delay));
        }

        let run_result = if has_stdin {
            let stdin_val = params.get("stdin")
                .ok_or_else(|| format!("exec {}: stdin param missing", cmd_label))?;
            let (prog, args) = build_wrapped_command(command, cwd, user, &env_pairs, shell);
            let arg_refs: Vec<&str> = args.iter().map(|s| s.as_str()).collect();
            let r = match timeout_dur {
                Some(dur) => runner.run_with_stdin_niced_timeout(
                    stdin_val.as_bytes(),
                    &prog,
                    &arg_refs,
                    nice_level,
                    dur,
                ),
                None => runner.run_with_stdin_niced(stdin_val.as_bytes(), &prog, &arg_refs, nice_level),
            };
            r.map_err(|e| {
                if sensitive {
                    "exec [sensitive]: command failed to start".to_string()
                } else {
                    format!("exec {}: {}", command, e)
                }
            })
        } else if has_secret_file {
            let sf = params.get("secret_file")
                .ok_or_else(|| format!("exec {}: secret_file param missing", cmd_label))?;
            run_with_secret_file(runner, command, sf, cwd, user, &env_pairs, shell)
        } else {
            let (prog, args) = build_wrapped_command(command, cwd, user, &env_pairs, shell);
            let arg_refs: Vec<&str> = args.iter().map(|s| s.as_str()).collect();
            let r = match timeout_dur {
                Some(dur) => runner.run_niced_timeout(&prog, &arg_refs, nice_level, dur),
                None => runner.run_niced(&prog, &arg_refs, nice_level),
            };
            r.map_err(|e| {
                if sensitive {
                    "exec [sensitive]: command failed to start".to_string()
                } else {
                    format!("exec {}: {}", command, e)
                }
            })
        };

        match run_result {
            Ok(output) => {
                if exit_code_acceptable(&output, &acceptable_codes) {
                    if report_settled {
                        // Operator-declared no-op: command ran cleanly but
                        // its side effects don't constitute a state change
                        // for the dashboard's purposes.
                        result.action = Action::None;
                        result.changed = false;
                    }
                    return Ok(());
                }
                last_error = Some(format_exec_error(&output));
            }
            Err(e) => {
                last_error = Some(e);
            }
        }
    }

    Err(last_error.unwrap_or_else(|| format!("exec {}: unknown error", cmd_label)))
}

/// Write secret to 0600 temp file, replace {{secret_file}} in command, run, cleanup.
fn run_with_secret_file(
    runner: &dyn CommandRunner,
    command: &str,
    secret: &str,
    cwd: Option<&str>,
    user: Option<&str>,
    env_pairs: &[(String, String)],
    shell: Option<&str>,
) -> Result<std::process::Output, String> {
    let tmp_dir = std::env::temp_dir();
    let tmp_path = tmp_dir.join(format!("vigo-secret-{}", uuid::Uuid::new_v4()));
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
    let (prog, args) = build_wrapped_command(&resolved, cwd, user, env_pairs, shell);
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
    use std::time::Duration;

    #[test]
    fn parse_duration_bare_int_is_seconds() {
        assert_eq!(parse_duration("30"), Some(Duration::from_secs(30)));
    }

    #[test]
    fn parse_duration_accepts_s_m_h_suffixes() {
        assert_eq!(parse_duration("30s"), Some(Duration::from_secs(30)));
        assert_eq!(parse_duration("45m"), Some(Duration::from_secs(45 * 60)));
        assert_eq!(parse_duration("4h"), Some(Duration::from_secs(4 * 3600)));
    }

    #[test]
    fn parse_duration_rejects_junk_and_zero() {
        // A malformed value must not silently pick up some default — it
        // must read as "no timeout" so operators notice when tailing
        // logs that their typo isn't being enforced.
        assert_eq!(parse_duration(""), None);
        assert_eq!(parse_duration("abc"), None);
        assert_eq!(parse_duration("10x"), None);
        assert_eq!(parse_duration("-5"), None);
        // Zero is semantically meaningless as a timeout; parse as None.
        assert_eq!(parse_duration("0"), None);
        assert_eq!(parse_duration("0s"), None);
    }

    #[test]
    fn parse_duration_trims_whitespace() {
        // Hostcrates often get string-quoted values with accidental
        // spaces; accept them rather than fail.
        assert_eq!(parse_duration("  2h  "), Some(Duration::from_secs(2 * 3600)));
    }

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
    fn report_settled_suppresses_change_on_success() {
        // A command that exits 0 with report_settled: true must report
        // Action::None and changed=false — operator-declared no-op for
        // dashboard purposes (e.g., apt-get update cache refresh).
        let exec = ExecExecutor::new();
        let result = exec.execute(
            &make_params(&[
                ("command", "echo hello"),
                ("report_settled", "true"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
    }

    #[test]
    fn report_settled_does_not_suppress_failure() {
        // report_settled only affects the success path. A non-zero exit
        // still surfaces as a real failure; the flag must NOT swallow it.
        let exec = ExecExecutor::new();
        let result = exec.execute(
            &make_params(&[
                ("command", "false"),
                ("report_settled", "true"),
            ]),
            false,
        );
        // Failure path: error populated, action stays Created (the
        // pre-run default), changed stays true. The dashboard reports
        // this as failed via failed_count from the result envelope.
        assert!(result.error.is_some(), "expected error on failure");
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
            bg_pids: Arc::new(Mutex::new(HashMap::new())),
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
            bg_pids: Arc::new(Mutex::new(HashMap::new())),
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
            bg_pids: Arc::new(Mutex::new(HashMap::new())),
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
            bg_pids: Arc::new(Mutex::new(HashMap::new())),
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
            bg_pids: Arc::new(Mutex::new(HashMap::new())),
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
            bg_pids: Arc::new(Mutex::new(HashMap::new())),
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
            bg_pids: Arc::new(Mutex::new(HashMap::new())),
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
            bg_pids: Arc::new(Mutex::new(HashMap::new())),
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
            bg_pids: Arc::new(Mutex::new(HashMap::new())),
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

    // ---- Phase 3A tests ----

    #[test]
    fn logoutput_false_suppresses_output() {
        let mock = MockRunner::new(vec![make_output_with_code(1, "secret data", "secret error")]);

        let exec = ExecExecutor {
            runner: Box::new(mock),
            bg_pids: Arc::new(Mutex::new(HashMap::new())),
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
            bg_pids: Arc::new(Mutex::new(HashMap::new())),
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
            bg_pids: Arc::new(Mutex::new(HashMap::new())),
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
            bg_pids: Arc::new(Mutex::new(HashMap::new())),
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
            bg_pids: Arc::new(Mutex::new(HashMap::new())),
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
        let (prog, args) = build_wrapped_command("echo hello", None, None, &[], None);
        assert_eq!(prog, "sh");
        assert_eq!(args, vec!["-c", "echo hello"]);
    }

    #[test]
    fn build_wrapped_command_with_cwd() {
        let (prog, args) = build_wrapped_command("echo hello", Some("/tmp"), None, &[], None);
        assert_eq!(prog, "sh");
        assert_eq!(args[0], "-c");
        assert!(args[1].contains("cd '/tmp'"));
        assert!(args[1].contains("echo hello"));
    }

    #[test]
    fn build_wrapped_command_with_user() {
        let (prog, args) = build_wrapped_command("whoami", None, Some("root"), &[], None);
        assert_eq!(prog, "su");
        assert_eq!(args[0], "-");
        assert_eq!(args[1], "root");
        assert_eq!(args[2], "-c");
        assert!(args[3].contains("whoami"));
    }

    #[test]
    fn build_wrapped_command_with_env() {
        let env = vec![("FOO".to_string(), "bar".to_string())];
        let (prog, args) = build_wrapped_command("env", None, None, &env, None);
        assert_eq!(prog, "sh");
        assert!(args[1].contains("export 'FOO'='bar'"));
        assert!(args[1].contains("env"));
    }

    #[test]
    fn build_wrapped_command_all_options() {
        let env = vec![("X".to_string(), "1".to_string())];
        let (prog, args) =
            build_wrapped_command("make", Some("/build"), Some("builder"), &env, None);
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
            bg_pids: Arc::new(Mutex::new(HashMap::new())),
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

    // ---- sensitive tests ----

    #[test]
    fn sensitive_suppresses_resource_name() {
        let mock = MockRunner::new(vec![ok_output("")]);

        let exec = ExecExecutor {
            runner: Box::new(mock),
            bg_pids: Arc::new(Mutex::new(HashMap::new())),
        };
        let result = exec.execute(
            &make_params(&[
                ("command", "echo secret-stuff"),
                ("sensitive", "true"),
            ]),
            false,
        );

        assert!(result.error.is_none(), "error: {:?}", result.error);
        assert_eq!(result.resource_name, "[sensitive]");
    }

    #[test]
    fn sensitive_suppresses_command_in_error() {
        let mock = MockRunner::new(vec![make_output_with_code(1, "out", "err")]);

        let exec = ExecExecutor {
            runner: Box::new(mock),
            bg_pids: Arc::new(Mutex::new(HashMap::new())),
        };
        let result = exec.execute(
            &make_params(&[
                ("command", "echo super-secret"),
                ("sensitive", "true"),
            ]),
            false,
        );

        assert!(result.error.is_some());
        let err = result.error.unwrap();
        assert!(!err.contains("super-secret"), "command should be suppressed, got: {}", err);
        assert!(err.contains("sensitive: true"), "should mention sensitive, got: {}", err);
        assert!(err.contains("exit 1"), "should include exit code, got: {}", err);
        assert!(!err.contains("out"), "stdout should be suppressed, got: {}", err);
        assert!(!err.contains("err"), "stderr should be suppressed, got: {}", err);
    }

    #[test]
    fn sensitive_false_shows_command() {
        let mock = MockRunner::new(vec![ok_output("")]);

        let exec = ExecExecutor {
            runner: Box::new(mock),
            bg_pids: Arc::new(Mutex::new(HashMap::new())),
        };
        let result = exec.execute(
            &make_params(&[
                ("command", "echo visible"),
                ("sensitive", "false"),
            ]),
            false,
        );

        assert!(result.error.is_none());
        assert_eq!(result.resource_name, "echo visible");
    }

    // ---- retry tests ----

    #[test]
    fn retries_succeed_on_second_attempt() {
        // First call fails, second succeeds.
        let mock = MockRunner::new(vec![
            make_output_with_code(1, "", "fail"),
            ok_output("ok"),
        ]);

        let exec = ExecExecutor {
            runner: Box::new(mock),
            bg_pids: Arc::new(Mutex::new(HashMap::new())),
        };
        let result = exec.execute(
            &make_params(&[
                ("command", "flaky-command"),
                ("retries", "1"),
                ("retry_delay", "0"),
            ]),
            false,
        );

        assert!(result.error.is_none(), "error: {:?}", result.error);
        assert_eq!(result.action, Action::Created);
    }

    #[test]
    fn retries_exhausted_returns_error() {
        // All 3 attempts fail (1 initial + 2 retries).
        let mock = MockRunner::new(vec![
            make_output_with_code(1, "", "fail1"),
            make_output_with_code(1, "", "fail2"),
            make_output_with_code(1, "", "fail3"),
        ]);

        let exec = ExecExecutor {
            runner: Box::new(mock),
            bg_pids: Arc::new(Mutex::new(HashMap::new())),
        };
        let result = exec.execute(
            &make_params(&[
                ("command", "always-fails"),
                ("retries", "2"),
                ("retry_delay", "0"),
            ]),
            false,
        );

        assert!(result.error.is_some());
    }

    #[test]
    fn retries_default_zero_no_retry() {
        // Single failure, no retries.
        let mock = MockRunner::new(vec![make_output_with_code(1, "", "fail")]);

        let exec = ExecExecutor {
            runner: Box::new(mock),
            bg_pids: Arc::new(Mutex::new(HashMap::new())),
        };
        let result = exec.execute(
            &make_params(&[("command", "one-shot-fail")]),
            false,
        );

        assert!(result.error.is_some());
    }

    #[test]
    fn retries_with_acceptable_codes() {
        // First attempt exits 2 which is in returns list — no retry needed.
        let mock = MockRunner::new(vec![make_output_with_code(2, "ok", "")]);

        let exec = ExecExecutor {
            runner: Box::new(mock),
            bg_pids: Arc::new(Mutex::new(HashMap::new())),
        };
        let result = exec.execute(
            &make_params(&[
                ("command", "returns-two"),
                ("returns", "0,2"),
                ("retries", "3"),
                ("retry_delay", "0"),
            ]),
            false,
        );

        assert!(result.error.is_none(), "error: {:?}", result.error);
    }

    // ---- shell override tests ----

    #[test]
    fn shell_override_uses_bash() {
        let mock = MockRunner::new(vec![ok_output("")]);
        let calls = mock.calls.clone();

        let exec = ExecExecutor {
            runner: Box::new(mock),
            bg_pids: Arc::new(Mutex::new(HashMap::new())),
        };
        let result = exec.execute(
            &make_params(&[
                ("command", "echo hello"),
                ("shell", "bash"),
            ]),
            false,
        );

        assert!(result.error.is_none(), "error: {:?}", result.error);

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1);
        assert_eq!(calls[0].0, "bash");
        assert_eq!(calls[0].1[0], "-c");
        assert!(calls[0].1[1].contains("echo hello"));
    }

    #[test]
    fn shell_override_applies_to_guards() {
        // onlyif guard + main command, both should use bash.
        let mock = MockRunner::new(vec![ok_output(""), ok_output("")]);
        let calls = mock.calls.clone();

        let exec = ExecExecutor {
            runner: Box::new(mock),
            bg_pids: Arc::new(Mutex::new(HashMap::new())),
        };
        let result = exec.execute(
            &make_params(&[
                ("command", "echo hello"),
                ("shell", "bash"),
                ("onlyif", "true"),
            ]),
            false,
        );

        assert!(result.error.is_none(), "error: {:?}", result.error);

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 2);
        for call in calls.iter() {
            assert_eq!(call.0, "bash", "expected bash shell, got: {}", call.0);
            assert_eq!(call.1[0], "-c");
        }
    }

    #[test]
    fn shell_default_is_sh() {
        let mock = MockRunner::new(vec![ok_output("")]);
        let calls = mock.calls.clone();

        let exec = ExecExecutor {
            runner: Box::new(mock),
            bg_pids: Arc::new(Mutex::new(HashMap::new())),
        };
        let result = exec.execute(
            &make_params(&[("command", "echo hello")]),
            false,
        );

        assert!(result.error.is_none(), "error: {:?}", result.error);

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1);
        assert_eq!(calls[0].0, "sh");
    }

    #[test]
    fn build_wrapped_command_with_shell_override() {
        let (prog, args) = build_wrapped_command("echo hi", None, None, &[], Some("zsh"));
        assert_eq!(prog, "zsh");
        assert_eq!(args[0], "-c");
        assert_eq!(args[1], "echo hi");
    }

    #[test]
    fn build_wrapped_command_shell_with_user_ignores_shell() {
        // When user is set, su is used regardless of shell override.
        let (prog, _args) = build_wrapped_command("whoami", None, Some("root"), &[], Some("bash"));
        assert_eq!(prog, "su");
    }

    // ---- combined param tests ----

    #[test]
    fn sensitive_with_retries() {
        // Fails twice, succeeds on third (retries=2). Verify sensitive hides command in result.
        let mock = MockRunner::new(vec![
            make_output_with_code(1, "", ""),
            make_output_with_code(1, "", ""),
            ok_output("done"),
        ]);

        let exec = ExecExecutor {
            runner: Box::new(mock),
            bg_pids: Arc::new(Mutex::new(HashMap::new())),
        };
        let result = exec.execute(
            &make_params(&[
                ("command", "secret-deploy-cmd"),
                ("sensitive", "true"),
                ("retries", "2"),
                ("retry_delay", "0"),
            ]),
            false,
        );

        assert!(result.error.is_none(), "error: {:?}", result.error);
        assert_eq!(result.resource_name, "[sensitive]");
    }

    #[test]
    fn sensitive_with_retries_exhausted() {
        let mock = MockRunner::new(vec![
            make_output_with_code(1, "", "secret-err"),
            make_output_with_code(1, "", "secret-err"),
        ]);

        let exec = ExecExecutor {
            runner: Box::new(mock),
            bg_pids: Arc::new(Mutex::new(HashMap::new())),
        };
        let result = exec.execute(
            &make_params(&[
                ("command", "secret-cmd"),
                ("sensitive", "true"),
                ("retries", "1"),
                ("retry_delay", "0"),
            ]),
            false,
        );

        assert!(result.error.is_some());
        let err = result.error.unwrap();
        assert!(!err.contains("secret-cmd"), "command should be suppressed: {}", err);
        assert!(!err.contains("secret-err"), "output should be suppressed: {}", err);
        assert_eq!(result.resource_name, "[sensitive]");
    }

    // B10: a hung subprocess with no explicit `timeout:` must be killed by the
    // runner-injected `_default_timeout_secs` ceiling rather than blocking
    // forever under Command::output(). Uses a real DefaultRunner so an actual
    // child is spawned and killed.
    #[cfg(unix)]
    #[test]
    fn default_timeout_kills_hung_subprocess() {
        let exec = ExecExecutor::new();
        let start = Instant::now();
        let result = exec.execute(
            &make_params(&[
                ("command", "sleep 60"),
                // Simulates the runner injecting its computed per-resource
                // default for an exec resource that set no `timeout:` itself.
                ("_default_timeout_secs", "1"),
            ]),
            false,
        );
        let elapsed = start.elapsed();
        // Must have returned well before the 60s sleep — killed at ~1s + a
        // 5s SIGTERM→SIGKILL grace, so allow generous slack for CI.
        assert!(
            elapsed < std::time::Duration::from_secs(20),
            "hung subprocess should be killed via default timeout, took {:?}",
            elapsed
        );
        assert!(result.error.is_some(), "timed-out exec should report an error");
        let err = result.error.unwrap();
        assert!(
            err.contains("timed out"),
            "error should mention timeout, got: {}",
            err
        );
    }

    // B10 precedence: an explicit `timeout:` still wins over the injected
    // runner default. With timeout:1 the command is killed at ~1s regardless
    // of a larger injected default.
    #[cfg(unix)]
    #[test]
    fn explicit_timeout_wins_over_default() {
        let exec = ExecExecutor::new();
        let start = Instant::now();
        let result = exec.execute(
            &make_params(&[
                ("command", "sleep 60"),
                ("timeout", "1"),
                ("_default_timeout_secs", "300"),
            ]),
            false,
        );
        let elapsed = start.elapsed();
        assert!(
            elapsed < std::time::Duration::from_secs(20),
            "explicit timeout:1 should kill regardless of larger default, took {:?}",
            elapsed
        );
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("timed out"));
    }
}
