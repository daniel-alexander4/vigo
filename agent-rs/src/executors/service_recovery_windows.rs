// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0
#![cfg(windows)]

//! Windows service recovery executor — manages per-service failure recovery
//! actions idempotently via `sc.exe`.
//!
//! Parameters:
//! - `service` (required): Service name
//! - `first_failure`: Action on first failure: "restart" (default), "run", "reboot", "none"
//! - `second_failure`: Action on second failure (default: same as first_failure)
//! - `subsequent_failure`: Action on third+ failure (default: same as first_failure)
//! - `reset_period`: Seconds before resetting the failure counter (default "86400" = 24h)
//! - `restart_delay`: Milliseconds before restart action (default "60000" = 60s)
//! - `command`: Command to run for "run" action (required when any action is "run")
//!
//! Check: `sc.exe qfailure <service>` — parse current recovery settings.
//! Set: `sc.exe failure <service> reset= <period> actions= <first>/<delay>/<second>/<delay>/<third>/<delay>`

use super::{Action, Executor, ResourceResult, run_command};
use std::collections::HashMap;
use std::time::Instant;

pub struct ServiceRecoveryWindowsExecutor;

/// Valid failure action keywords.
const VALID_ACTIONS: &[&str] = &["restart", "run", "reboot", "none"];

impl ServiceRecoveryWindowsExecutor {
    pub fn new() -> Self {
        Self
    }

    /// Map our action name to the sc.exe action keyword.
    fn sc_action(action: &str) -> &str {
        match action {
            "restart" => "restart",
            "run" => "run",
            "reboot" => "reboot",
            "none" => "",
            _ => "",
        }
    }

    /// Query current failure recovery settings via `sc.exe qfailure`.
    /// Returns (reset_period, Vec<(action, delay_ms)>).
    fn query_failure(service: &str) -> Option<(u64, Vec<(String, u64)>)> {
        let output = match run_command("sc.exe", &["qfailure", service]) {
            Ok(o) if o.status.success() => o,
            _ => return None,
        };

        let stdout = String::from_utf8_lossy(&output.stdout);
        let mut reset_period: u64 = 0;
        let mut actions: Vec<(String, u64)> = Vec::new();

        for line in stdout.lines() {
            let trimmed = line.trim();

            // RESET_PERIOD (in seconds) : 86400
            if trimmed.starts_with("RESET_PERIOD") {
                if let Some(val) = trimmed.split(':').nth(1) {
                    reset_period = val.trim().parse().unwrap_or(0);
                }
            }

            // FAILURE_ACTIONS lines:
            //         RESTART -- Delay = 60000 milliseconds.
            //         RUN PROCESS -- Delay = 60000 milliseconds.
            //         REBOOT -- Delay = 60000 milliseconds.
            //         <empty> -- Delay = 0 milliseconds.
            if trimmed.contains("-- Delay =") {
                let action_name = if trimmed.starts_with("RESTART") {
                    "restart"
                } else if trimmed.starts_with("RUN PROCESS") {
                    "run"
                } else if trimmed.starts_with("REBOOT") {
                    "reboot"
                } else {
                    "none"
                };

                let delay = trimmed
                    .split("Delay =")
                    .nth(1)
                    .and_then(|s| {
                        s.trim()
                            .trim_end_matches("milliseconds.")
                            .trim()
                            .parse::<u64>()
                            .ok()
                    })
                    .unwrap_or(0);

                actions.push((action_name.to_string(), delay));
            }
        }

        Some((reset_period, actions))
    }
}

impl Executor for ServiceRecoveryWindowsExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let service = params.get("service").cloned().unwrap_or_default();

        let mut result = ResourceResult::new("service_recovery_windows", &service);

        if service.is_empty() {
            result.error = Some(
                "service_recovery_windows executor: service param is required".to_string(),
            );
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let first = params
            .get("first_failure")
            .map(|s| s.as_str())
            .unwrap_or("restart");
        let second = params
            .get("second_failure")
            .map(|s| s.as_str())
            .unwrap_or(first);
        let subsequent = params
            .get("subsequent_failure")
            .map(|s| s.as_str())
            .unwrap_or(first);

        let reset_period: u64 = params
            .get("reset_period")
            .and_then(|s| s.parse().ok())
            .unwrap_or(86400);

        let restart_delay: u64 = params
            .get("restart_delay")
            .and_then(|s| s.parse().ok())
            .unwrap_or(60000);

        let command = params.get("command").cloned().unwrap_or_default();

        // Validate action values.
        for (label, action) in [
            ("first_failure", first),
            ("second_failure", second),
            ("subsequent_failure", subsequent),
        ] {
            if !VALID_ACTIONS.contains(&action) {
                result.error = Some(format!(
                    "service_recovery_windows executor: invalid {} {:?} (must be restart, run, reboot, or none)",
                    label, action
                ));
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
        }

        // If any action is "run", command is required.
        if (first == "run" || second == "run" || subsequent == "run") && command.is_empty() {
            result.error = Some(
                "service_recovery_windows executor: command param is required when any failure action is \"run\"".to_string(),
            );
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        // Query current failure settings.
        let current = Self::query_failure(&service);

        // Check if already in desired state.
        if let Some((cur_reset, ref cur_actions)) = current {
            let desired_actions = [first, second, subsequent];
            let mut matches = cur_reset == reset_period && cur_actions.len() >= 3;

            if matches {
                for (i, desired) in desired_actions.iter().enumerate() {
                    if i < cur_actions.len() {
                        let desired_delay = if *desired == "none" { 0 } else { restart_delay };
                        if cur_actions[i].0 != *desired || cur_actions[i].1 != desired_delay {
                            matches = false;
                            break;
                        }
                    } else {
                        matches = false;
                        break;
                    }
                }
            }

            if matches {
                result.action = Action::None;
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

        // Build sc.exe failure command.
        // Format: sc.exe failure <svc> reset= <seconds> actions= <action1>/<delay>/<action2>/<delay>/<action3>/<delay>
        let build_action_entry = |action: &str| -> String {
            let sc_act = Self::sc_action(action);
            let delay = if action == "none" {
                0
            } else {
                restart_delay
            };
            format!("{}/{}", sc_act, delay)
        };

        let actions_value = format!(
            "{}/{}/{}",
            build_action_entry(first),
            build_action_entry(second),
            build_action_entry(subsequent),
        );

        let reset_str = reset_period.to_string();
        let mut args: Vec<&str> = vec![
            "failure",
            &service,
            "reset=",
            &reset_str,
            "actions=",
            &actions_value,
        ];

        // If command is specified, add it.
        let command_arg;
        if !command.is_empty() {
            command_arg = format!("\"{}\"", command);
            args.push("command=");
            args.push(&command_arg);
        }

        match run_command("sc.exe", &args) {
            Ok(o) if !o.status.success() => {
                let stderr = String::from_utf8_lossy(&o.stderr);
                let stdout = String::from_utf8_lossy(&o.stdout);
                result.error = Some(format!(
                    "sc.exe failure {}: {} {}",
                    service,
                    stderr.trim(),
                    stdout.trim()
                ));
            }
            Err(e) => {
                result.error = Some(format!("sc.exe failure {}: {}", service, e));
            }
            _ => {}
        }

        // Set failure flag if "run" action is used (enables recovery for non-crash exits).
        if first == "run" || second == "run" || subsequent == "run" {
            match run_command("sc.exe", &["failureflag", &service, "1"]) {
                Ok(o) if !o.status.success() => {
                    let stderr = String::from_utf8_lossy(&o.stderr);
                    result.error = Some(format!(
                        "sc.exe failureflag {}: {}",
                        service,
                        stderr.trim()
                    ));
                }
                Err(e) => {
                    result.error = Some(format!("sc.exe failureflag {}: {}", service, e));
                }
                _ => {}
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
    fn missing_service_param() {
        let exec = ServiceRecoveryWindowsExecutor::new();
        let params = HashMap::new();
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("service param is required"));
    }

    #[test]
    fn invalid_failure_action() {
        let exec = ServiceRecoveryWindowsExecutor::new();
        let mut params = HashMap::new();
        params.insert("service".to_string(), "MyService".to_string());
        params.insert("first_failure".to_string(), "explode".to_string());
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("invalid first_failure"));
    }

    #[test]
    fn run_action_requires_command() {
        let exec = ServiceRecoveryWindowsExecutor::new();
        let mut params = HashMap::new();
        params.insert("service".to_string(), "MyService".to_string());
        params.insert("first_failure".to_string(), "run".to_string());
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(
            result
                .error
                .unwrap()
                .contains("command param is required")
        );
    }
}
