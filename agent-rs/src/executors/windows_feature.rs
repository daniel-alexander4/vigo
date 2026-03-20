// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary
#![cfg(windows)]

//! Windows Feature executor — manages Windows Features (IIS, Hyper-V, .NET,
//! RSAT, etc.) idempotently via PowerShell.
//!
//! Parameters:
//! - `feature` (required): Feature name (e.g., "IIS-WebServer", "NET-Framework-45-Core")
//! - `state`: "present" (default) or "absent"
//! - `include_management_tools`: "true"/"false" (default "false") — adds -IncludeManagementTools
//!
//! Check: Tries `Get-WindowsFeature` (Server) first, falls back to
//! `Get-WindowsOptionalFeature -Online` (client/workstation).
//! Install: `Install-WindowsFeature` (Server) or `Enable-WindowsOptionalFeature` (client).
//! Remove: `Remove-WindowsFeature` (Server) or `Disable-WindowsOptionalFeature` (client).

use super::{Action, Executor, ResourceResult, run_command};
use std::collections::HashMap;
use std::time::Instant;

pub struct WindowsFeatureExecutor;

impl WindowsFeatureExecutor {
    pub fn new() -> Self {
        Self
    }

    /// Check feature state. Returns (installed, is_server_os).
    /// Tries Get-WindowsFeature first (Server SKU), falls back to
    /// Get-WindowsOptionalFeature (client SKU).
    fn check_feature(name: &str) -> (bool, bool) {
        // Try server cmdlet first.
        let ps_server = format!(
            "$f = Get-WindowsFeature -Name '{}' -ErrorAction SilentlyContinue; if ($f) {{ if ($f.Installed) {{ 'INSTALLED' }} else {{ 'NOT_INSTALLED' }} }} else {{ 'UNKNOWN' }}",
            name.replace('\'', "''")
        );
        if let Ok(o) = run_command("powershell.exe", &["-NoProfile", "-NonInteractive", "-Command", &ps_server]) {
            if o.status.success() {
                let stdout = String::from_utf8_lossy(&o.stdout).trim().to_string();
                if stdout == "INSTALLED" {
                    return (true, true);
                } else if stdout == "NOT_INSTALLED" {
                    return (false, true);
                }
                // UNKNOWN means cmdlet exists but feature name not found — still server OS.
                // Fall through to client check only if the cmdlet itself failed.
            }
        }

        // Fall back to client cmdlet.
        let ps_client = format!(
            "$f = Get-WindowsOptionalFeature -Online -FeatureName '{}' -ErrorAction SilentlyContinue; if ($f) {{ $f.State }} else {{ 'NOTFOUND' }}",
            name.replace('\'', "''")
        );
        if let Ok(o) = run_command("powershell.exe", &["-NoProfile", "-NonInteractive", "-Command", &ps_client]) {
            if o.status.success() {
                let stdout = String::from_utf8_lossy(&o.stdout).trim().to_string();
                if stdout == "Enabled" {
                    return (true, false);
                } else if stdout == "Disabled" || stdout == "DisabledWithPayloadRemoved" {
                    return (false, false);
                }
            }
        }

        (false, false)
    }
}

impl Executor for WindowsFeatureExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let feature = params.get("feature").cloned().unwrap_or_default();
        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");
        let include_mgmt = params
            .get("include_management_tools")
            .map(|s| s == "true")
            .unwrap_or(false);

        let mut result = ResourceResult::new("windows_feature", &feature);

        if feature.is_empty() {
            result.error = Some(
                "windows_feature executor: feature param is required".to_string(),
            );
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let (installed, is_server) = Self::check_feature(&feature);

        match state {
            "present" => {
                if installed {
                    result.action = Action::None;
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                result.action = Action::Created;
                result.changed = true;

                if dry_run {
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                // Try server cmdlet first, fall back to client.
                let ps = if is_server {
                    let mut cmd = format!(
                        "Install-WindowsFeature -Name '{}'",
                        feature.replace('\'', "''")
                    );
                    if include_mgmt {
                        cmd.push_str(" -IncludeManagementTools");
                    }
                    cmd.push_str(" -ErrorAction Stop");
                    cmd
                } else {
                    format!(
                        "Enable-WindowsOptionalFeature -Online -FeatureName '{}' -All -NoRestart -ErrorAction Stop",
                        feature.replace('\'', "''")
                    )
                };

                match run_command("powershell.exe", &["-NoProfile", "-NonInteractive", "-Command", &ps]) {
                    Ok(o) if !o.status.success() => {
                        let stderr = String::from_utf8_lossy(&o.stderr);
                        let stdout = String::from_utf8_lossy(&o.stdout);
                        result.error = Some(format!(
                            "windows_feature install {}: {} {}",
                            feature, stderr.trim(), stdout.trim()
                        ));
                    }
                    Err(e) => {
                        result.error = Some(format!(
                            "windows_feature install {}: {}",
                            feature, e
                        ));
                    }
                    _ => {}
                }
            }
            "absent" => {
                if !installed {
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

                let ps = if is_server {
                    format!(
                        "Remove-WindowsFeature -Name '{}' -ErrorAction Stop",
                        feature.replace('\'', "''")
                    )
                } else {
                    format!(
                        "Disable-WindowsOptionalFeature -Online -FeatureName '{}' -NoRestart -ErrorAction Stop",
                        feature.replace('\'', "''")
                    )
                };

                match run_command("powershell.exe", &["-NoProfile", "-NonInteractive", "-Command", &ps]) {
                    Ok(o) if !o.status.success() => {
                        let stderr = String::from_utf8_lossy(&o.stderr);
                        let stdout = String::from_utf8_lossy(&o.stdout);
                        result.error = Some(format!(
                            "windows_feature remove {}: {} {}",
                            feature, stderr.trim(), stdout.trim()
                        ));
                    }
                    Err(e) => {
                        result.error = Some(format!(
                            "windows_feature remove {}: {}",
                            feature, e
                        ));
                    }
                    _ => {}
                }
            }
            other => {
                result.error = Some(format!(
                    "windows_feature executor: unknown state {:?} (must be present or absent)",
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
    fn missing_feature_param() {
        let exec = WindowsFeatureExecutor::new();
        let params = HashMap::new();
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("feature param is required"));
    }

    #[test]
    fn invalid_state() {
        let exec = WindowsFeatureExecutor::new();
        let mut params = HashMap::new();
        params.insert("feature".to_string(), "IIS-WebServer".to_string());
        params.insert("state".to_string(), "bogus".to_string());
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("unknown state"));
    }
}
