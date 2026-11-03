// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0
#![cfg(windows)]

//! Windows hostname executor — sets the system hostname idempotently.
//!
//! Reads the current hostname via the `hostname` command and sets it using
//! PowerShell's `Rename-Computer` cmdlet.

use super::{Action, Executor, ResourceResult, run_command};
use std::collections::HashMap;
use std::time::Instant;

/// Manages the Windows hostname using `hostname` and PowerShell.
pub struct HostnameWindowsExecutor;

impl HostnameWindowsExecutor {
    pub fn new() -> Self {
        Self
    }

    /// Read the current hostname.
    fn current_hostname() -> String {
        match run_command("hostname", &[]) {
            Ok(o) if o.status.success() => {
                String::from_utf8_lossy(&o.stdout).trim().to_string()
            }
            _ => String::new(),
        }
    }
}

impl Executor for HostnameWindowsExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let desired = params
            .get("hostname")
            .cloned()
            .unwrap_or_default();

        let mut result = ResourceResult::new("hostname_windows", &desired);

        if desired.is_empty() {
            result.error = Some(
                "hostname_windows executor: hostname param is required".to_string(),
            );
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let current = Self::current_hostname();
        if current.eq_ignore_ascii_case(&desired) {
            result.action = Action::None;
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        result.action = Action::Updated;
        result.changed = true;

        if dry_run {
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        // Use PowerShell Rename-Computer to set the hostname.
        let ps_command = format!(
            "Rename-Computer -NewName '{}' -Force",
            desired.replace('\'', "''")
        );
        match run_command("powershell", &["-Command", &ps_command]) {
            Ok(o) if !o.status.success() => {
                let stderr = String::from_utf8_lossy(&o.stderr);
                let stdout = String::from_utf8_lossy(&o.stdout);
                result.error = Some(format!(
                    "Rename-Computer to {:?}: {} {}",
                    desired, stderr, stdout
                ));
            }
            Err(e) => {
                result.error = Some(format!(
                    "Rename-Computer to {:?}: {}",
                    desired, e
                ));
            }
            _ => {}
        }

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn missing_hostname() {
        let exec = HostnameWindowsExecutor::new();
        let params = HashMap::new();
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result
            .error
            .unwrap()
            .contains("hostname param is required"));
    }

    #[test]
    fn empty_hostname_string() {
        let exec = HostnameWindowsExecutor::new();
        let mut params = HashMap::new();
        params.insert("hostname".to_string(), "".to_string());
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result
            .error
            .unwrap()
            .contains("hostname param is required"));
    }
}
