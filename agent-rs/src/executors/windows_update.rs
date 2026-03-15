// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0
#![cfg(windows)]

//! Windows Update / WSUS settings executor — manages Windows Update configuration
//! via registry values idempotently.
//!
//! Parameters:
//! - `wsus_server`: WSUS server URL (e.g., "https://wsus.example.com:8531").
//!   Empty = use Microsoft Update.
//! - `auto_update`: "enabled" (default), "disabled", "notify", "download", "scheduled"
//! - `scheduled_day`: Day for scheduled installs (0=every day, 1=Sunday...7=Saturday).
//!   Only used when auto_update=scheduled.
//! - `scheduled_time`: Hour for scheduled installs (0-23). Only used when
//!   auto_update=scheduled.
//! - `state`: "present" (default) or "absent" (removes WSUS config, reverts to
//!   Microsoft Update)
//!
//! Registry paths:
//!   `HKLM:\SOFTWARE\Policies\Microsoft\Windows\WindowsUpdate`
//!   `HKLM:\SOFTWARE\Policies\Microsoft\Windows\WindowsUpdate\AU`

use super::{Action, Executor, ResourceResult, run_command};
use std::collections::HashMap;
use std::time::Instant;

const WU_KEY: &str = r"HKLM:\SOFTWARE\Policies\Microsoft\Windows\WindowsUpdate";
const AU_KEY: &str = r"HKLM:\SOFTWARE\Policies\Microsoft\Windows\WindowsUpdate\AU";

pub struct WindowsUpdateExecutor;

impl WindowsUpdateExecutor {
    pub fn new() -> Self {
        Self
    }

    /// Run a PowerShell command. Returns stdout on success, error message on failure.
    fn ps(script: &str) -> Result<String, String> {
        match run_command(
            "powershell.exe",
            &["-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script],
        ) {
            Ok(o) if o.status.success() => {
                Ok(String::from_utf8_lossy(&o.stdout).trim().to_string())
            }
            Ok(o) => {
                let stderr = String::from_utf8_lossy(&o.stderr).trim().to_string();
                let stdout = String::from_utf8_lossy(&o.stdout).trim().to_string();
                Err(format!("{} {}", stderr, stdout))
            }
            Err(e) => Err(e.to_string()),
        }
    }

    /// Map auto_update param to AUOptions registry value.
    fn au_options(auto_update: &str) -> Option<u32> {
        match auto_update {
            "notify" => Some(2),
            "download" => Some(3),
            "scheduled" => Some(4),
            "enabled" => Some(5), // Managed by admin / auto install
            _ => None,
        }
    }

    /// Read a DWORD registry value. Returns None if not found.
    fn read_dword(key: &str, name: &str) -> Option<u32> {
        let script = format!(
            "try {{ (Get-ItemProperty -Path '{}' -Name '{}' -ErrorAction Stop).'{}' }} catch {{ 'NOTFOUND' }}",
            key.replace('\'', "''"),
            name.replace('\'', "''"),
            name.replace('\'', "''"),
        );
        match Self::ps(&script) {
            Ok(s) if s == "NOTFOUND" || s.is_empty() => None,
            Ok(s) => s.parse::<u32>().ok(),
            Err(_) => None,
        }
    }

    /// Read a string registry value.
    fn read_string(key: &str, name: &str) -> Option<String> {
        let script = format!(
            "try {{ (Get-ItemProperty -Path '{}' -Name '{}' -ErrorAction Stop).'{}' }} catch {{ 'NOTFOUND' }}",
            key.replace('\'', "''"),
            name.replace('\'', "''"),
            name.replace('\'', "''"),
        );
        match Self::ps(&script) {
            Ok(s) if s == "NOTFOUND" || s.is_empty() => None,
            Ok(s) => Some(s),
            Err(_) => None,
        }
    }

    /// Check if current state matches desired state. Returns true if already correct.
    fn is_current(
        wsus_server: &str,
        auto_update: &str,
        scheduled_day: &str,
        scheduled_time: &str,
    ) -> bool {
        // Check WSUS config.
        if !wsus_server.is_empty() {
            let current_wu = Self::read_string(WU_KEY, "WUServer");
            if current_wu.as_deref() != Some(wsus_server) {
                return false;
            }
            let use_wu = Self::read_dword(AU_KEY, "UseWUServer");
            if use_wu != Some(1) {
                return false;
            }
        } else {
            let use_wu = Self::read_dword(AU_KEY, "UseWUServer");
            if use_wu == Some(1) {
                return false;
            }
        }

        // Check auto-update mode.
        if auto_update == "disabled" {
            let no_auto = Self::read_dword(AU_KEY, "NoAutoUpdate");
            if no_auto != Some(1) {
                return false;
            }
        } else if let Some(desired_au) = Self::au_options(auto_update) {
            let no_auto = Self::read_dword(AU_KEY, "NoAutoUpdate");
            if no_auto != Some(0) {
                return false;
            }
            let current_au = Self::read_dword(AU_KEY, "AUOptions");
            if current_au != Some(desired_au) {
                return false;
            }
        }

        // Check scheduled install params.
        if auto_update == "scheduled" {
            if !scheduled_day.is_empty() {
                if let Ok(day) = scheduled_day.parse::<u32>() {
                    let current = Self::read_dword(AU_KEY, "ScheduledInstallDay");
                    if current != Some(day) {
                        return false;
                    }
                }
            }
            if !scheduled_time.is_empty() {
                if let Ok(hour) = scheduled_time.parse::<u32>() {
                    let current = Self::read_dword(AU_KEY, "ScheduledInstallTime");
                    if current != Some(hour) {
                        return false;
                    }
                }
            }
        }

        true
    }

    /// Apply desired state via registry writes.
    fn apply(
        wsus_server: &str,
        auto_update: &str,
        scheduled_day: &str,
        scheduled_time: &str,
    ) -> Result<(), String> {
        let mut commands = Vec::new();

        // Ensure registry keys exist.
        commands.push(format!(
            "New-Item -Path '{}' -Force | Out-Null",
            WU_KEY.replace('\'', "''"),
        ));
        commands.push(format!(
            "New-Item -Path '{}' -Force | Out-Null",
            AU_KEY.replace('\'', "''"),
        ));

        // WSUS server settings.
        if !wsus_server.is_empty() {
            commands.push(format!(
                "Set-ItemProperty -Path '{}' -Name 'WUServer' -Value '{}' -Type String -Force",
                WU_KEY.replace('\'', "''"),
                wsus_server.replace('\'', "''"),
            ));
            commands.push(format!(
                "Set-ItemProperty -Path '{}' -Name 'WUStatusServer' -Value '{}' -Type String -Force",
                WU_KEY.replace('\'', "''"),
                wsus_server.replace('\'', "''"),
            ));
            commands.push(format!(
                "Set-ItemProperty -Path '{}' -Name 'UseWUServer' -Value 1 -Type DWord -Force",
                AU_KEY.replace('\'', "''"),
            ));
        } else {
            commands.push(format!(
                "Set-ItemProperty -Path '{}' -Name 'UseWUServer' -Value 0 -Type DWord -Force",
                AU_KEY.replace('\'', "''"),
            ));
        }

        // Auto-update mode.
        if auto_update == "disabled" {
            commands.push(format!(
                "Set-ItemProperty -Path '{}' -Name 'NoAutoUpdate' -Value 1 -Type DWord -Force",
                AU_KEY.replace('\'', "''"),
            ));
        } else {
            commands.push(format!(
                "Set-ItemProperty -Path '{}' -Name 'NoAutoUpdate' -Value 0 -Type DWord -Force",
                AU_KEY.replace('\'', "''"),
            ));
            if let Some(au_val) = Self::au_options(auto_update) {
                commands.push(format!(
                    "Set-ItemProperty -Path '{}' -Name 'AUOptions' -Value {} -Type DWord -Force",
                    AU_KEY.replace('\'', "''"),
                    au_val,
                ));
            }
        }

        // Scheduled install params.
        if auto_update == "scheduled" {
            if !scheduled_day.is_empty() {
                commands.push(format!(
                    "Set-ItemProperty -Path '{}' -Name 'ScheduledInstallDay' -Value {} -Type DWord -Force",
                    AU_KEY.replace('\'', "''"),
                    scheduled_day,
                ));
            }
            if !scheduled_time.is_empty() {
                commands.push(format!(
                    "Set-ItemProperty -Path '{}' -Name 'ScheduledInstallTime' -Value {} -Type DWord -Force",
                    AU_KEY.replace('\'', "''"),
                    scheduled_time,
                ));
            }
        }

        let full_script = commands.join("; ");
        Self::ps(&full_script).map(|_| ())
    }

    /// Remove WSUS policy keys (revert to Microsoft Update).
    fn remove_config() -> Result<(), String> {
        let script = format!(
            "Remove-Item -Path '{}' -Recurse -Force -ErrorAction SilentlyContinue",
            WU_KEY.replace('\'', "''"),
        );
        Self::ps(&script).map(|_| ())
    }

    /// Check if the WindowsUpdate policy key exists.
    fn key_exists() -> bool {
        let script = format!(
            "if (Test-Path '{}') {{ 'EXISTS' }} else {{ 'MISSING' }}",
            WU_KEY.replace('\'', "''"),
        );
        matches!(Self::ps(&script), Ok(s) if s == "EXISTS")
    }
}

impl Executor for WindowsUpdateExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let wsus_server = params.get("wsus_server").cloned().unwrap_or_default();
        let auto_update = params.get("auto_update").map(|s| s.as_str()).unwrap_or("enabled");
        let scheduled_day = params.get("scheduled_day").cloned().unwrap_or_default();
        let scheduled_time = params.get("scheduled_time").cloned().unwrap_or_default();
        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");

        let display = if !wsus_server.is_empty() {
            format!("WSUS:{}", wsus_server)
        } else {
            format!("auto_update={}", auto_update)
        };
        let mut result = ResourceResult::new("windows_update", &display);

        if !matches!(state, "present" | "absent") {
            result.error = Some(format!(
                "windows_update executor: unknown state {:?} (must be present or absent)",
                state
            ));
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        if !matches!(
            auto_update,
            "enabled" | "disabled" | "notify" | "download" | "scheduled"
        ) {
            result.error = Some(format!(
                "windows_update executor: unknown auto_update {:?} (must be enabled, disabled, notify, download, or scheduled)",
                auto_update
            ));
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        match state {
            "present" => {
                if Self::is_current(&wsus_server, auto_update, &scheduled_day, &scheduled_time) {
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

                if let Err(e) =
                    Self::apply(&wsus_server, auto_update, &scheduled_day, &scheduled_time)
                {
                    result.error = Some(format!("windows_update apply: {}", e));
                }
            }
            "absent" => {
                if !Self::key_exists() {
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

                if let Err(e) = Self::remove_config() {
                    result.error = Some(format!("windows_update remove: {}", e));
                }
            }
            _ => unreachable!(),
        }

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn invalid_state() {
        let exec = WindowsUpdateExecutor::new();
        let mut params = HashMap::new();
        params.insert("state".to_string(), "bogus".to_string());
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("unknown state"));
    }

    #[test]
    fn invalid_auto_update() {
        let exec = WindowsUpdateExecutor::new();
        let mut params = HashMap::new();
        params.insert("auto_update".to_string(), "bogus".to_string());
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("unknown auto_update"));
    }

    #[test]
    fn au_options_mapping() {
        assert_eq!(WindowsUpdateExecutor::au_options("notify"), Some(2));
        assert_eq!(WindowsUpdateExecutor::au_options("download"), Some(3));
        assert_eq!(WindowsUpdateExecutor::au_options("scheduled"), Some(4));
        assert_eq!(WindowsUpdateExecutor::au_options("enabled"), Some(5));
        assert_eq!(WindowsUpdateExecutor::au_options("disabled"), None);
        assert_eq!(WindowsUpdateExecutor::au_options("bogus"), None);
    }
}
