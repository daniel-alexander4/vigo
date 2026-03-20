// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary
#![cfg(windows)]

//! Windows timezone executor — sets the system timezone idempotently via `tzutil`.
//!
//! Reads the current timezone with `tzutil /g` and sets it with `tzutil /s`.

use super::{Action, Executor, ResourceResult, run_command};
use std::collections::HashMap;
use std::time::Instant;

/// Manages the Windows timezone using `tzutil.exe`.
pub struct TimezoneWindowsExecutor;

impl TimezoneWindowsExecutor {
    pub fn new() -> Self {
        Self
    }

    /// Read the current timezone via `tzutil /g`.
    fn current_timezone() -> String {
        match run_command("tzutil", &["/g"]) {
            Ok(o) if o.status.success() => {
                String::from_utf8_lossy(&o.stdout).trim().to_string()
            }
            _ => String::new(),
        }
    }
}

impl Executor for TimezoneWindowsExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let desired = params
            .get("timezone")
            .cloned()
            .unwrap_or_default();

        let mut result = ResourceResult::new("timezone_windows", &desired);

        if desired.is_empty() {
            result.error = Some(
                "timezone_windows executor: timezone param is required".to_string(),
            );
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let current = Self::current_timezone();
        if current == desired {
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

        match run_command("tzutil", &["/s", &desired]) {
            Ok(o) if !o.status.success() => {
                let stderr = String::from_utf8_lossy(&o.stderr);
                let stdout = String::from_utf8_lossy(&o.stdout);
                result.error = Some(format!(
                    "tzutil /s {:?}: {} {}",
                    desired, stderr, stdout
                ));
            }
            Err(e) => {
                result.error = Some(format!("tzutil /s {:?}: {}", desired, e));
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
    fn missing_timezone() {
        let exec = TimezoneWindowsExecutor::new();
        let params = HashMap::new();
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result
            .error
            .unwrap()
            .contains("timezone param is required"));
    }

    #[test]
    fn empty_timezone_string() {
        let exec = TimezoneWindowsExecutor::new();
        let mut params = HashMap::new();
        params.insert("timezone".to_string(), "".to_string());
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result
            .error
            .unwrap()
            .contains("timezone param is required"));
    }
}
