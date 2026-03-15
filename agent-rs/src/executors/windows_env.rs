// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0
#![cfg(windows)]

//! Windows environment variable executor — manages persistent environment
//! variables idempotently via PowerShell.
//!
//! Parameters:
//! - `variable` (required): Variable name (e.g., "JAVA_HOME", "PATH")
//! - `value` (required for present): Variable value
//! - `target`: "Machine" (default) or "User"
//! - `state`: "present" (default) or "absent"
//!
//! Check: `[Environment]::GetEnvironmentVariable('<name>', '<target>')`
//! Set: `[Environment]::SetEnvironmentVariable('<name>', '<value>', '<target>')`
//! Remove: `[Environment]::SetEnvironmentVariable('<name>', $null, '<target>')`

use super::{Action, Executor, ResourceResult, run_command};
use std::collections::HashMap;
use std::time::Instant;

pub struct WindowsEnvExecutor;

impl WindowsEnvExecutor {
    pub fn new() -> Self {
        Self
    }

    /// Query the current value of a persistent environment variable.
    /// Returns None if not set or on error.
    fn get_value(variable: &str, target: &str) -> Option<String> {
        let ps = format!(
            "$v = [Environment]::GetEnvironmentVariable('{}', '{}'); if ($v -eq $null) {{ Write-Output '___NULL___' }} else {{ Write-Output $v }}",
            variable.replace('\'', "''"),
            target.replace('\'', "''"),
        );
        match run_command("powershell.exe", &["-NoProfile", "-NonInteractive", "-Command", &ps]) {
            Ok(o) if o.status.success() => {
                let val = String::from_utf8_lossy(&o.stdout).trim().to_string();
                if val == "___NULL___" {
                    None
                } else {
                    Some(val)
                }
            }
            _ => None,
        }
    }

    /// Validate the target parameter.
    fn validate_target(target: &str) -> Result<(), String> {
        match target {
            "Machine" | "User" => Ok(()),
            other => Err(format!(
                "windows_env executor: unknown target {:?} (must be Machine or User)",
                other
            )),
        }
    }
}

impl Executor for WindowsEnvExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let variable = params.get("variable").cloned().unwrap_or_default();
        let value = params.get("value").cloned().unwrap_or_default();
        let target = params.get("target").map(|s| s.as_str()).unwrap_or("Machine");
        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");

        let mut result = ResourceResult::new("windows_env", &variable);

        if variable.is_empty() {
            result.error = Some(
                "windows_env executor: variable param is required".to_string(),
            );
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        if let Err(e) = Self::validate_target(target) {
            result.error = Some(e);
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        match state {
            "present" => {
                if value.is_empty() {
                    result.error = Some(
                        "windows_env executor: value param is required when state=present"
                            .to_string(),
                    );
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                let current = Self::get_value(&variable, target);

                if current.as_deref() == Some(&value) {
                    result.action = Action::None;
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                result.action = if current.is_some() {
                    Action::Updated
                } else {
                    Action::Created
                };
                result.changed = true;

                if dry_run {
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                let ps = format!(
                    "[Environment]::SetEnvironmentVariable('{}', '{}', '{}')",
                    variable.replace('\'', "''"),
                    value.replace('\'', "''"),
                    target,
                );
                match run_command("powershell.exe", &["-NoProfile", "-NonInteractive", "-Command", &ps]) {
                    Ok(o) if !o.status.success() => {
                        let stderr = String::from_utf8_lossy(&o.stderr);
                        result.error = Some(format!(
                            "windows_env set {} ({}): {}",
                            variable, target, stderr.trim()
                        ));
                    }
                    Err(e) => {
                        result.error = Some(format!(
                            "windows_env set {} ({}): {}",
                            variable, target, e
                        ));
                    }
                    _ => {}
                }
            }
            "absent" => {
                let current = Self::get_value(&variable, target);

                if current.is_none() {
                    result.action = Action::None;
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                result.action = Action::Deleted;
                result.changed = true;

                if dry_run {
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                let ps = format!(
                    "[Environment]::SetEnvironmentVariable('{}', $null, '{}')",
                    variable.replace('\'', "''"),
                    target,
                );
                match run_command("powershell.exe", &["-NoProfile", "-NonInteractive", "-Command", &ps]) {
                    Ok(o) if !o.status.success() => {
                        let stderr = String::from_utf8_lossy(&o.stderr);
                        result.error = Some(format!(
                            "windows_env remove {} ({}): {}",
                            variable, target, stderr.trim()
                        ));
                    }
                    Err(e) => {
                        result.error = Some(format!(
                            "windows_env remove {} ({}): {}",
                            variable, target, e
                        ));
                    }
                    _ => {}
                }
            }
            other => {
                result.error = Some(format!(
                    "windows_env executor: unknown state {:?} (must be present or absent)",
                    other
                ));
            }
        }

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn missing_variable_param() {
        let exec = WindowsEnvExecutor::new();
        let params = HashMap::new();
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("variable param is required"));
    }

    #[test]
    fn invalid_target() {
        let exec = WindowsEnvExecutor::new();
        let mut params = HashMap::new();
        params.insert("variable".to_string(), "JAVA_HOME".to_string());
        params.insert("value".to_string(), "C:\\Java".to_string());
        params.insert("target".to_string(), "System".to_string());
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("unknown target"));
    }

    #[test]
    fn missing_value_for_present() {
        let exec = WindowsEnvExecutor::new();
        let mut params = HashMap::new();
        params.insert("variable".to_string(), "JAVA_HOME".to_string());
        params.insert("state".to_string(), "present".to_string());
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result
            .error
            .unwrap()
            .contains("value param is required when state=present"));
    }

    #[test]
    fn validate_target_accepts_valid() {
        assert!(WindowsEnvExecutor::validate_target("Machine").is_ok());
        assert!(WindowsEnvExecutor::validate_target("User").is_ok());
    }

    #[test]
    fn validate_target_rejects_invalid() {
        assert!(WindowsEnvExecutor::validate_target("Process").is_err());
        assert!(WindowsEnvExecutor::validate_target("").is_err());
    }
}
