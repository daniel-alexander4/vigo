// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0
#![cfg(windows)]

//! Windows reboot executor — manages pending reboots and triggers reboots when
//! needed, idempotently.
//!
//! Parameters:
//! - `when_pending`: "true" (default) — only reboot if a reboot is pending
//! - `message`: Optional message shown to users before reboot
//! - `delay_secs`: Seconds to wait before reboot (default "60")
//!
//! Checks these registry keys for pending reboot state:
//! - `HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Component Based Servicing\RebootPending`
//! - `HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\WindowsUpdate\Auto Update\RebootRequired`
//! - `HKLM:\SYSTEM\CurrentControlSet\Control\Session Manager` → `PendingFileRenameOperations`
//!
//! If `when_pending` is true and no reboot is pending, the resource is skipped.
//! Otherwise: `shutdown.exe /r /t <delay_secs> /c "<message>"`.

use super::{Action, Executor, ResourceResult, run_command};
use std::collections::HashMap;
use std::time::Instant;

pub struct RebootWindowsExecutor;

impl RebootWindowsExecutor {
    pub fn new() -> Self {
        Self
    }

    /// Check if a reboot is pending by inspecting well-known registry locations.
    fn reboot_pending() -> bool {
        let ps = r#"
$pending = $false
if (Test-Path 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Component Based Servicing\RebootPending') {
    $pending = $true
}
if (Test-Path 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\WindowsUpdate\Auto Update\RebootRequired') {
    $pending = $true
}
try {
    $val = Get-ItemProperty -Path 'HKLM:\SYSTEM\CurrentControlSet\Control\Session Manager' -Name 'PendingFileRenameOperations' -ErrorAction SilentlyContinue
    if ($val) { $pending = $true }
} catch {}
if ($pending) { Write-Output 'PENDING' } else { Write-Output 'CLEAR' }
"#;
        match run_command("powershell.exe", &["-NoProfile", "-NonInteractive", "-Command", ps]) {
            Ok(o) if o.status.success() => {
                let stdout = String::from_utf8_lossy(&o.stdout).trim().to_string();
                stdout == "PENDING"
            }
            _ => false,
        }
    }
}

impl Executor for RebootWindowsExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let when_pending = params
            .get("when_pending")
            .map(|s| s != "false")
            .unwrap_or(true);
        let message = params
            .get("message")
            .cloned()
            .unwrap_or_else(|| "Hobnob: scheduled reboot".to_string());
        let delay_secs = params
            .get("delay_secs")
            .cloned()
            .unwrap_or_else(|| "60".to_string());

        let mut result = ResourceResult::new("reboot_windows", "reboot");

        // Validate delay_secs is a number.
        if delay_secs.parse::<u32>().is_err() {
            result.error = Some(format!(
                "reboot_windows executor: delay_secs must be a positive integer, got {:?}",
                delay_secs
            ));
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        if when_pending {
            let pending = Self::reboot_pending();
            if !pending {
                result.action = Action::Skipped;
                result.changed = false;
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

        let mut args = vec!["/r", "/t", &delay_secs, "/c", &message];
        // Force close running applications.
        args.push("/f");

        match run_command("shutdown.exe", &args) {
            Ok(o) if !o.status.success() => {
                let stderr = String::from_utf8_lossy(&o.stderr);
                let stdout = String::from_utf8_lossy(&o.stdout);
                result.error = Some(format!(
                    "shutdown.exe /r: {} {}",
                    stderr.trim(),
                    stdout.trim()
                ));
            }
            Err(e) => {
                result.error = Some(format!("shutdown.exe /r: {}", e));
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
    fn invalid_delay_secs() {
        let exec = RebootWindowsExecutor::new();
        let mut params = HashMap::new();
        params.insert("delay_secs".to_string(), "abc".to_string());
        params.insert("when_pending".to_string(), "false".to_string());
        let result = exec.execute(&params, true);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("delay_secs must be a positive integer"));
    }

    #[test]
    fn default_resource_name() {
        let exec = RebootWindowsExecutor::new();
        let mut params = HashMap::new();
        params.insert("when_pending".to_string(), "false".to_string());
        let result = exec.execute(&params, true);
        assert_eq!(result.resource_name, "reboot");
        // In dry_run with when_pending=false, it should plan to reboot.
        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);
        assert!(result.error.is_none());
    }
}
