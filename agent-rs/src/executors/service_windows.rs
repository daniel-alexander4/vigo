// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1
#![cfg(windows)]

//! Windows service executor — manages Windows services idempotently via `sc.exe`.
//!
//! Checks service state via `sc.exe query`, manages start/stop/restart via
//! `sc.exe start`/`sc.exe stop`, and configures startup type via `sc.exe config`.

use super::{Action, Executor, ResourceResult, run_command};
use std::collections::HashMap;
use std::time::Instant;

/// Manages Windows service resources using `sc.exe`.
pub struct ServiceWindowsExecutor;

impl ServiceWindowsExecutor {
    pub fn new() -> Self {
        Self
    }

    /// Parse the STATE line from `sc.exe query` output.
    /// Returns (is_running, found).
    fn query_service_state(name: &str) -> (bool, bool) {
        let output = match run_command("sc.exe", &["query", name]) {
            Ok(o) => o,
            Err(_) => return (false, false),
        };

        if !output.status.success() {
            return (false, false);
        }

        let stdout = String::from_utf8_lossy(&output.stdout);
        for line in stdout.lines() {
            let trimmed = line.trim();
            if trimmed.starts_with("STATE") {
                // STATE              : 4  RUNNING
                // STATE              : 1  STOPPED
                if trimmed.contains("RUNNING") {
                    return (true, true);
                } else {
                    return (false, true);
                }
            }
        }

        (false, false)
    }

    /// Query the startup type via `sc.exe qc`.
    /// Returns the start type string (e.g., "AUTO_START", "DEMAND_START", "DISABLED").
    fn query_start_type(name: &str) -> String {
        let output = match run_command("sc.exe", &["qc", name]) {
            Ok(o) if o.status.success() => o,
            _ => return String::new(),
        };

        let stdout = String::from_utf8_lossy(&output.stdout);
        for line in stdout.lines() {
            let trimmed = line.trim();
            if trimmed.starts_with("START_TYPE") {
                // START_TYPE         : 2   AUTO_START
                if trimmed.contains("AUTO_START") {
                    return "auto".to_string();
                } else if trimmed.contains("DEMAND_START") {
                    return "demand".to_string();
                } else if trimmed.contains("DISABLED") {
                    return "disabled".to_string();
                }
            }
        }

        String::new()
    }

    fn execute_one(name: &str, state: &str, enable_param: &str, dry_run: bool) -> ResourceResult {
        let mut result = ResourceResult::new("service_windows", name);

        let (running, found) = Self::query_service_state(name);
        if !found {
            result.error = Some(format!("service_windows executor: service {:?} not found", name));
            return result;
        }

        let mut changed = false;
        let mut state_actions: Vec<&str> = Vec::new();

        match state {
            "running" => {
                if !running {
                    state_actions.push("start");
                    changed = true;
                }
            }
            "stopped" => {
                if running {
                    state_actions.push("stop");
                    changed = true;
                }
            }
            "restarted" => {
                if running {
                    state_actions.push("stop");
                }
                state_actions.push("start");
                changed = true;
            }
            _ => {
                result.error = Some(format!(
                    "service_windows executor: unknown state {:?}",
                    state
                ));
                return result;
            }
        }

        // Check enable/disable.
        let mut enable_action: Option<&str> = None;
        if enable_param == "true" {
            let start_type = Self::query_start_type(name);
            if start_type != "auto" {
                enable_action = Some("auto");
                changed = true;
            }
        } else if enable_param == "false" {
            let start_type = Self::query_start_type(name);
            if start_type != "disabled" {
                enable_action = Some("disabled");
                changed = true;
            }
        }

        if !changed {
            result.action = Action::None;
            result.changed = false;
            return result;
        }

        result.action = Action::Updated;
        result.changed = true;

        if dry_run {
            return result;
        }

        // Execute state transitions.
        for action in &state_actions {
            match *action {
                "stop" => {
                    match run_command("sc.exe", &["stop", name]) {
                        Ok(o) if !o.status.success() => {
                            let stderr = String::from_utf8_lossy(&o.stderr);
                            let stdout = String::from_utf8_lossy(&o.stdout);
                            result.error = Some(format!(
                                "sc.exe stop {}: {} {}",
                                name, stderr, stdout
                            ));
                            return result;
                        }
                        Err(e) => {
                            result.error =
                                Some(format!("sc.exe stop {}: {}", name, e));
                            return result;
                        }
                        _ => {}
                    }
                    // Wait briefly for the service to stop before starting again.
                    if state_actions.contains(&"start") {
                        std::thread::sleep(std::time::Duration::from_secs(2));
                    }
                }
                "start" => {
                    match run_command("sc.exe", &["start", name]) {
                        Ok(o) if !o.status.success() => {
                            let stderr = String::from_utf8_lossy(&o.stderr);
                            let stdout = String::from_utf8_lossy(&o.stdout);
                            result.error = Some(format!(
                                "sc.exe start {}: {} {}",
                                name, stderr, stdout
                            ));
                            return result;
                        }
                        Err(e) => {
                            result.error =
                                Some(format!("sc.exe start {}: {}", name, e));
                            return result;
                        }
                        _ => {}
                    }
                }
                _ => {}
            }
        }

        // Apply enable/disable.
        if let Some(start_type) = enable_action {
            let start_arg = format!("start={}", start_type);
            match run_command("sc.exe", &["config", name, &start_arg]) {
                Ok(o) if !o.status.success() => {
                    let stderr = String::from_utf8_lossy(&o.stderr);
                    let stdout = String::from_utf8_lossy(&o.stdout);
                    result.error = Some(format!(
                        "sc.exe config {} {}: {} {}",
                        name, start_arg, stderr, stdout
                    ));
                }
                Err(e) => {
                    result.error = Some(format!(
                        "sc.exe config {} {}: {}",
                        name, start_arg, e
                    ));
                }
                _ => {}
            }
        }

        result
    }
}

impl Executor for ServiceWindowsExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let name_param = params
            .get("service")
            .or_else(|| params.get("name"))
            .cloned()
            .unwrap_or_default();

        let mut result = ResourceResult::new("service_windows", &name_param);

        if name_param.is_empty() {
            result.error = Some(
                "service_windows executor: name param is required".to_string(),
            );
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let state = params
            .get("state")
            .map(|s| s.as_str())
            .unwrap_or("running");
        let enable_param = params
            .get("enabled")
            .map(|s| s.as_str())
            .unwrap_or("");

        // Support comma-separated service names.
        let names: Vec<&str> = name_param
            .split(',')
            .map(|s| s.trim())
            .filter(|s| !s.is_empty())
            .collect();

        for name in &names {
            let res = Self::execute_one(name, state, enable_param, dry_run);
            if res.error.is_some() {
                result.error = res.error;
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
            if res.changed {
                result.changed = true;
                if result.action == Action::None {
                    result.action = res.action;
                }
            }
        }

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}
