// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary
#![cfg(windows)]

//! PowerShell executor — runs PowerShell scripts with proper error handling
//! and idempotency guards.
//!
//! Different from the generic `exec` executor: always uses
//! `powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass`, prepends
//! `$ErrorActionPreference = 'Stop'`, and maps PowerShell exceptions to failures.
//!
//! Parameters:
//! - `command` (required): PowerShell script or command
//! - `unless`: Skip if this PowerShell command succeeds (exit 0)
//! - `onlyif`: Skip if this PowerShell command fails (non-zero)
//! - `cwd`: Working directory
//! - `environment`: Comma-separated KEY=VALUE environment variables

use super::{Action, Executor, ResourceResult};
use std::collections::HashMap;
use std::path::Path;
use std::time::Instant;

pub struct PowershellExecutor;

impl PowershellExecutor {
    pub fn new() -> Self {
        Self
    }

    /// Run a PowerShell command and return (stdout, success).
    fn run_ps(command: &str, cwd: Option<&str>, env_vars: &[(String, String)]) -> (String, bool) {
        let wrapped = format!("$ErrorActionPreference = 'Stop'; {}", command);
        let mut cmd = std::process::Command::new("powershell.exe");
        cmd.args([
            "-NoProfile",
            "-NonInteractive",
            "-ExecutionPolicy",
            "Bypass",
            "-Command",
            &wrapped,
        ]);
        if let Some(dir) = cwd {
            cmd.current_dir(dir);
        }
        for (key, val) in env_vars {
            cmd.env(key, val);
        }
        match cmd.output() {
            Ok(o) => {
                let stdout = String::from_utf8_lossy(&o.stdout).trim().to_string();
                let stderr = String::from_utf8_lossy(&o.stderr).trim().to_string();
                let combined = if stderr.is_empty() {
                    stdout
                } else if stdout.is_empty() {
                    stderr
                } else {
                    format!("{}\n{}", stdout, stderr)
                };
                (combined, o.status.success())
            }
            Err(e) => (e.to_string(), false),
        }
    }

    /// Parse environment string "KEY1=VAL1,KEY2=VAL2" into pairs.
    fn parse_env(env_str: &str) -> Vec<(String, String)> {
        if env_str.is_empty() {
            return Vec::new();
        }
        env_str
            .split(',')
            .filter_map(|pair| {
                let pair = pair.trim();
                if pair.is_empty() {
                    return None;
                }
                let mut parts = pair.splitn(2, '=');
                let key = parts.next()?.trim().to_string();
                let val = parts.next().unwrap_or("").trim().to_string();
                if key.is_empty() {
                    None
                } else {
                    Some((key, val))
                }
            })
            .collect()
    }
}

impl Executor for PowershellExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let command = params.get("command").cloned().unwrap_or_default();
        let unless = params.get("unless").cloned().unwrap_or_default();
        let onlyif = params.get("onlyif").cloned().unwrap_or_default();
        let cwd = params.get("cwd").cloned().unwrap_or_default();
        let env_str = params.get("environment").cloned().unwrap_or_default();

        let display = if command.len() > 60 {
            format!("{}...", &command[..57])
        } else {
            command.clone()
        };
        let mut result = ResourceResult::new("powershell", &display);

        if command.is_empty() {
            result.error = Some(
                "powershell executor: command param is required".to_string(),
            );
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let env_vars = Self::parse_env(&env_str);
        let cwd_opt = if cwd.is_empty() { None } else { Some(cwd.as_str()) };

        // Check idempotency guard: unless — skip if command succeeds.
        if !unless.is_empty() {
            let (_, success) = Self::run_ps(&unless, cwd_opt, &env_vars);
            if success {
                result.action = Action::Skipped;
                result.changed = false;
                result.skip_reason = Some("unless guard".into());
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
        }

        // Check idempotency guard: onlyif — skip if command fails.
        if !onlyif.is_empty() {
            let (_, success) = Self::run_ps(&onlyif, cwd_opt, &env_vars);
            if !success {
                result.action = Action::Skipped;
                result.changed = false;
                result.skip_reason = Some("onlyif guard".into());
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
        }

        result.action = Action::Updated;
        result.changed = true;

        if dry_run {
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let (output, success) = Self::run_ps(&command, cwd_opt, &env_vars);
        if !success {
            result.error = Some(format!(
                "powershell executor: command failed: {}",
                output
            ));
        }

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn missing_command_param() {
        let exec = PowershellExecutor::new();
        let params = HashMap::new();
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("command param is required"));
    }

    #[test]
    fn parse_env_empty() {
        assert!(PowershellExecutor::parse_env("").is_empty());
    }

    #[test]
    fn parse_env_single() {
        let pairs = PowershellExecutor::parse_env("FOO=bar");
        assert_eq!(pairs.len(), 1);
        assert_eq!(pairs[0], ("FOO".to_string(), "bar".to_string()));
    }

    #[test]
    fn parse_env_multiple() {
        let pairs = PowershellExecutor::parse_env("FOO=bar,BAZ=qux");
        assert_eq!(pairs.len(), 2);
        assert_eq!(pairs[0], ("FOO".to_string(), "bar".to_string()));
        assert_eq!(pairs[1], ("BAZ".to_string(), "qux".to_string()));
    }

    #[test]
    fn parse_env_with_equals_in_value() {
        let pairs = PowershellExecutor::parse_env("DSN=host=db;port=5432");
        assert_eq!(pairs.len(), 1);
        assert_eq!(
            pairs[0],
            ("DSN".to_string(), "host=db;port=5432".to_string())
        );
    }

    #[test]
    fn dry_run_reports_change() {
        let exec = PowershellExecutor::new();
        let mut params = HashMap::new();
        params.insert("command".to_string(), "Write-Output 'test'".to_string());
        let result = exec.execute(&params, true);
        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);
        assert!(result.error.is_none());
    }

    #[test]
    fn long_command_truncated_in_name() {
        let exec = PowershellExecutor::new();
        let long_cmd = "x".repeat(100);
        let mut params = HashMap::new();
        params.insert("command".to_string(), long_cmd);
        let result = exec.execute(&params, true);
        assert!(result.resource_name.len() <= 63);
        assert!(result.resource_name.ends_with("..."));
    }
}
