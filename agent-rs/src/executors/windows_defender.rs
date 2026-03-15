// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0
#![cfg(windows)]

//! Windows Defender executor — manages Windows Defender settings and exclusions
//! idempotently via PowerShell.
//!
//! Parameters:
//! - `exclusion_path`: Comma-separated paths to exclude from scanning
//! - `exclusion_process`: Comma-separated process names to exclude
//! - `exclusion_extension`: Comma-separated file extensions to exclude
//! - `real_time_protection`: "true"/"false" — enable/disable real-time monitoring
//! - `scan_schedule_day`: Day for scheduled scan (0=daily, 1=Sunday...7=Saturday)
//! - `scan_schedule_time`: Time for scheduled scan in HH:MM format (e.g., "02:00")
//! - `state`: "present" (default) — applies settings. "absent" — removes exclusions.
//!
//! Check: `Get-MpPreference` — parse current settings.
//! Set exclusions: `Add-MpPreference -ExclusionPath '<path>'`
//! Remove exclusions: `Remove-MpPreference -ExclusionPath '<path>'`
//! Set real-time: `Set-MpPreference -DisableRealtimeMonitoring $false` (or $true)
//! Set scan schedule: `Set-MpPreference -ScanScheduleDay <day> -ScanScheduleQuickScanTime <time>`

use super::{Action, Executor, ResourceResult, run_command};
use std::collections::HashMap;
use std::time::Instant;

pub struct WindowsDefenderExecutor;

impl WindowsDefenderExecutor {
    pub fn new() -> Self {
        Self
    }

    /// Query current Defender preferences. Returns a map of key → value strings.
    fn get_preferences() -> HashMap<String, String> {
        let ps = "Get-MpPreference | Format-List -Property DisableRealtimeMonitoring,ExclusionPath,ExclusionProcess,ExclusionExtension,ScanScheduleDay,ScanScheduleQuickScanTime";
        let mut prefs = HashMap::new();

        let output = match run_command(
            "powershell.exe",
            &["-NoProfile", "-NonInteractive", "-Command", ps],
        ) {
            Ok(o) if o.status.success() => o,
            _ => return prefs,
        };

        let stdout = String::from_utf8_lossy(&output.stdout);
        for line in stdout.lines() {
            let trimmed = line.trim();
            if let Some((key, value)) = trimmed.split_once(':') {
                let key = key.trim().to_string();
                let value = value.trim().to_string();
                if !key.is_empty() {
                    prefs.insert(key, value);
                }
            }
        }

        prefs
    }

    /// Parse a PowerShell array/list value like "{a, b, c}" into a Vec of trimmed strings.
    fn parse_list(val: &str) -> Vec<String> {
        let trimmed = val.trim().trim_start_matches('{').trim_end_matches('}');
        if trimmed.is_empty() {
            return Vec::new();
        }
        trimmed
            .split(',')
            .map(|s| s.trim().to_string())
            .filter(|s| !s.is_empty())
            .collect()
    }

    /// Split a comma-separated param value into a vec of trimmed strings.
    fn split_csv(val: &str) -> Vec<String> {
        val.split(',')
            .map(|s| s.trim().to_string())
            .filter(|s| !s.is_empty())
            .collect()
    }
}

impl Executor for WindowsDefenderExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");

        let exclusion_path = params.get("exclusion_path").cloned().unwrap_or_default();
        let exclusion_process = params.get("exclusion_process").cloned().unwrap_or_default();
        let exclusion_extension = params.get("exclusion_extension").cloned().unwrap_or_default();
        let real_time = params.get("real_time_protection").cloned();
        let scan_day = params.get("scan_schedule_day").cloned();
        let scan_time = params.get("scan_schedule_time").cloned();

        // Build a display name from whatever params are set.
        let display = if !exclusion_path.is_empty() {
            format!("exclusions:{}", exclusion_path)
        } else if real_time.is_some() {
            "real_time_protection".to_string()
        } else if scan_day.is_some() || scan_time.is_some() {
            "scan_schedule".to_string()
        } else {
            "windows_defender".to_string()
        };

        let mut result = ResourceResult::new("windows_defender", &display);

        // Validate state.
        if state != "present" && state != "absent" {
            result.error = Some(format!(
                "windows_defender executor: unknown state {:?} (must be present or absent)",
                state
            ));
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        // Must have at least one parameter to act on.
        if exclusion_path.is_empty()
            && exclusion_process.is_empty()
            && exclusion_extension.is_empty()
            && real_time.is_none()
            && scan_day.is_none()
            && scan_time.is_none()
        {
            result.error = Some(
                "windows_defender executor: at least one setting parameter is required".to_string(),
            );
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        // Validate real_time_protection if provided.
        if let Some(ref rt) = real_time {
            if rt != "true" && rt != "false" {
                result.error = Some(format!(
                    "windows_defender executor: real_time_protection must be \"true\" or \"false\", got {:?}",
                    rt
                ));
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
        }

        // Validate scan_schedule_day if provided.
        if let Some(ref day) = scan_day {
            match day.parse::<u32>() {
                Ok(d) if d <= 7 => {}
                _ => {
                    result.error = Some(format!(
                        "windows_defender executor: scan_schedule_day must be 0-7, got {:?}",
                        day
                    ));
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }
            }
        }

        // Validate scan_schedule_time if provided.
        if let Some(ref time) = scan_time {
            let parts: Vec<&str> = time.split(':').collect();
            let valid = parts.len() == 2
                && parts[0].parse::<u32>().map(|h| h < 24).unwrap_or(false)
                && parts[1].parse::<u32>().map(|m| m < 60).unwrap_or(false);
            if !valid {
                result.error = Some(format!(
                    "windows_defender executor: scan_schedule_time must be HH:MM format, got {:?}",
                    time
                ));
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
        }

        // Query current preferences.
        let prefs = Self::get_preferences();

        let current_excl_paths = Self::parse_list(
            prefs.get("ExclusionPath").map(|s| s.as_str()).unwrap_or(""),
        );
        let current_excl_procs = Self::parse_list(
            prefs.get("ExclusionProcess").map(|s| s.as_str()).unwrap_or(""),
        );
        let current_excl_exts = Self::parse_list(
            prefs.get("ExclusionExtension").map(|s| s.as_str()).unwrap_or(""),
        );

        let desired_paths = Self::split_csv(&exclusion_path);
        let desired_procs = Self::split_csv(&exclusion_process);
        let desired_exts = Self::split_csv(&exclusion_extension);

        // Collect PowerShell commands to execute.
        let mut commands: Vec<String> = Vec::new();

        match state {
            "present" => {
                // Add missing exclusion paths.
                for p in &desired_paths {
                    if !current_excl_paths.iter().any(|c| c.eq_ignore_ascii_case(p)) {
                        commands.push(format!(
                            "Add-MpPreference -ExclusionPath '{}'",
                            p.replace('\'', "''")
                        ));
                    }
                }

                // Add missing exclusion processes.
                for p in &desired_procs {
                    if !current_excl_procs.iter().any(|c| c.eq_ignore_ascii_case(p)) {
                        commands.push(format!(
                            "Add-MpPreference -ExclusionProcess '{}'",
                            p.replace('\'', "''")
                        ));
                    }
                }

                // Add missing exclusion extensions.
                for e in &desired_exts {
                    if !current_excl_exts.iter().any(|c| c.eq_ignore_ascii_case(e)) {
                        commands.push(format!(
                            "Add-MpPreference -ExclusionExtension '{}'",
                            e.replace('\'', "''")
                        ));
                    }
                }

                // Real-time protection.
                if let Some(ref rt) = real_time {
                    let current_disabled = prefs
                        .get("DisableRealtimeMonitoring")
                        .map(|v| v == "True")
                        .unwrap_or(false);
                    let want_enabled = rt == "true";
                    // DisableRealtimeMonitoring=True means protection is OFF.
                    if (want_enabled && current_disabled) || (!want_enabled && !current_disabled) {
                        let ps_val = if want_enabled { "$false" } else { "$true" };
                        commands.push(format!(
                            "Set-MpPreference -DisableRealtimeMonitoring {}",
                            ps_val
                        ));
                    }
                }

                // Scan schedule.
                if scan_day.is_some() || scan_time.is_some() {
                    let mut scan_cmd = "Set-MpPreference".to_string();
                    let mut needs_change = false;

                    if let Some(ref day) = scan_day {
                        let current_day = prefs
                            .get("ScanScheduleDay")
                            .cloned()
                            .unwrap_or_default();
                        if current_day != *day {
                            scan_cmd.push_str(&format!(" -ScanScheduleDay {}", day));
                            needs_change = true;
                        }
                    }

                    if let Some(ref time) = scan_time {
                        let current_time = prefs
                            .get("ScanScheduleQuickScanTime")
                            .cloned()
                            .unwrap_or_default();
                        // The time from Get-MpPreference may be in different format.
                        if !current_time.contains(time) {
                            scan_cmd.push_str(&format!(" -ScanScheduleQuickScanTime {}", time));
                            needs_change = true;
                        }
                    }

                    if needs_change {
                        commands.push(scan_cmd);
                    }
                }
            }
            "absent" => {
                // Remove matching exclusion paths.
                for p in &desired_paths {
                    if current_excl_paths.iter().any(|c| c.eq_ignore_ascii_case(p)) {
                        commands.push(format!(
                            "Remove-MpPreference -ExclusionPath '{}'",
                            p.replace('\'', "''")
                        ));
                    }
                }

                // Remove matching exclusion processes.
                for p in &desired_procs {
                    if current_excl_procs.iter().any(|c| c.eq_ignore_ascii_case(p)) {
                        commands.push(format!(
                            "Remove-MpPreference -ExclusionProcess '{}'",
                            p.replace('\'', "''")
                        ));
                    }
                }

                // Remove matching exclusion extensions.
                for e in &desired_exts {
                    if current_excl_exts.iter().any(|c| c.eq_ignore_ascii_case(e)) {
                        commands.push(format!(
                            "Remove-MpPreference -ExclusionExtension '{}'",
                            e.replace('\'', "''")
                        ));
                    }
                }
            }
            _ => unreachable!(),
        }

        // No changes needed.
        if commands.is_empty() {
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

        // Execute all commands.
        let joined = commands.join("; ");
        match run_command(
            "powershell.exe",
            &["-NoProfile", "-NonInteractive", "-Command", &joined],
        ) {
            Ok(o) if !o.status.success() => {
                let stderr = String::from_utf8_lossy(&o.stderr);
                let stdout = String::from_utf8_lossy(&o.stdout);
                result.error = Some(format!(
                    "windows_defender: {} {}",
                    stderr.trim(),
                    stdout.trim()
                ));
            }
            Err(e) => {
                result.error = Some(format!("windows_defender: {}", e));
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
    fn missing_all_params() {
        let exec = WindowsDefenderExecutor::new();
        let params = HashMap::new();
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(
            result
                .error
                .unwrap()
                .contains("at least one setting parameter is required")
        );
    }

    #[test]
    fn invalid_state() {
        let exec = WindowsDefenderExecutor::new();
        let mut params = HashMap::new();
        params.insert("exclusion_path".to_string(), "C:\\Temp".to_string());
        params.insert("state".to_string(), "bogus".to_string());
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("unknown state"));
    }

    #[test]
    fn invalid_real_time_protection() {
        let exec = WindowsDefenderExecutor::new();
        let mut params = HashMap::new();
        params.insert("real_time_protection".to_string(), "maybe".to_string());
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(
            result
                .error
                .unwrap()
                .contains("real_time_protection must be")
        );
    }

    #[test]
    fn invalid_scan_schedule_day() {
        let exec = WindowsDefenderExecutor::new();
        let mut params = HashMap::new();
        params.insert("scan_schedule_day".to_string(), "9".to_string());
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(
            result
                .error
                .unwrap()
                .contains("scan_schedule_day must be 0-7")
        );
    }

    #[test]
    fn invalid_scan_schedule_time() {
        let exec = WindowsDefenderExecutor::new();
        let mut params = HashMap::new();
        params.insert("scan_schedule_time".to_string(), "25:00".to_string());
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(
            result
                .error
                .unwrap()
                .contains("scan_schedule_time must be HH:MM")
        );
    }

    #[test]
    fn parse_list_empty() {
        assert!(WindowsDefenderExecutor::parse_list("").is_empty());
        assert!(WindowsDefenderExecutor::parse_list("{}").is_empty());
    }

    #[test]
    fn parse_list_values() {
        let result = WindowsDefenderExecutor::parse_list("{C:\\App, D:\\Data}");
        assert_eq!(result, vec!["C:\\App", "D:\\Data"]);
    }
}
