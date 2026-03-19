// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0
#![cfg(windows)]

//! Windows firewall executor — manages Windows Firewall rules via
//! `netsh advfirewall firewall`.
//!
//! Supports adding, removing, and checking firewall rules with configurable
//! port, protocol, direction, and action parameters.

use super::{Action, Executor, ResourceResult, run_command};
use std::collections::HashMap;
use std::time::Instant;

/// Manages Windows Firewall rules using `netsh advfirewall firewall`.
pub struct FirewallWindowsExecutor;

impl FirewallWindowsExecutor {
    pub fn new() -> Self {
        Self
    }

    /// Check if a firewall rule with the given name exists.
    fn rule_exists(rule_name: &str) -> bool {
        let output = run_command(
            "netsh",
            &["advfirewall", "firewall", "show", "rule", &format!("name={}", rule_name)],
        );
        match output {
            Ok(o) => {
                if !o.status.success() {
                    return false;
                }
                let stdout = String::from_utf8_lossy(&o.stdout);
                // netsh returns "No rules match the specified criteria." if not found.
                !stdout.contains("No rules match")
            }
            Err(_) => false,
        }
    }
}

impl Executor for FirewallWindowsExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();

        let rule_name = params
            .get("name")
            .cloned()
            .unwrap_or_default();

        // Derive a display name from the rule name or port.
        let port = params.get("port").cloned().unwrap_or_default();
        let display_name = if rule_name.is_empty() {
            port.clone()
        } else {
            rule_name.clone()
        };

        let mut result = ResourceResult::new("firewall_windows", &display_name);

        if rule_name.is_empty() && port.is_empty() {
            result.error = Some(
                "firewall_windows executor: name or port param is required".to_string(),
            );
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let state = params
            .get("state")
            .map(|s| s.as_str())
            .unwrap_or("present");
        let protocol = params
            .get("protocol")
            .map(|s| s.as_str())
            .unwrap_or("tcp");
        let fw_action = params
            .get("action")
            .map(|s| s.as_str())
            .unwrap_or("allow");
        let direction = params
            .get("direction")
            .map(|s| s.as_str())
            .unwrap_or("in");

        // Construct the actual rule name for netsh. If the user provided a name,
        // use it; otherwise generate one from port/protocol.
        let effective_rule_name = if !rule_name.is_empty() {
            rule_name.clone()
        } else {
            format!("hobnob-{}-{}-{}", port, protocol, direction)
        };

        let dir_value = match direction {
            "in" => "in",
            "out" => "out",
            _ => {
                result.error = Some(format!(
                    "firewall_windows executor: unknown direction {:?}, expected \"in\" or \"out\"",
                    direction
                ));
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
        };

        match state {
            "present" => {
                if Self::rule_exists(&effective_rule_name) {
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

                let name_arg = format!("name={}", effective_rule_name);
                let dir_arg = format!("dir={}", dir_value);
                let action_arg = format!("action={}", fw_action);
                let protocol_arg = format!("protocol={}", protocol);

                let mut args = vec![
                    "advfirewall",
                    "firewall",
                    "add",
                    "rule",
                    &name_arg,
                    &dir_arg,
                    &action_arg,
                    &protocol_arg,
                ];

                let port_arg;
                if !port.is_empty() {
                    port_arg = format!("localport={}", port);
                    args.push(&port_arg);
                }

                match run_command("netsh", &args) {
                    Ok(o) if !o.status.success() => {
                        let stderr = String::from_utf8_lossy(&o.stderr);
                        let stdout = String::from_utf8_lossy(&o.stdout);
                        result.error = Some(format!(
                            "netsh add rule {}: {} {}",
                            effective_rule_name, stderr, stdout
                        ));
                    }
                    Err(e) => {
                        result.error = Some(format!(
                            "netsh add rule {}: {}",
                            effective_rule_name, e
                        ));
                    }
                    _ => {}
                }
            }
            "absent" => {
                if !Self::rule_exists(&effective_rule_name) {
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

                let name_arg = format!("name={}", effective_rule_name);
                match run_command(
                    "netsh",
                    &["advfirewall", "firewall", "delete", "rule", &name_arg],
                ) {
                    Ok(o) if !o.status.success() => {
                        let stderr = String::from_utf8_lossy(&o.stderr);
                        let stdout = String::from_utf8_lossy(&o.stdout);
                        result.error = Some(format!(
                            "netsh delete rule {}: {} {}",
                            effective_rule_name, stderr, stdout
                        ));
                    }
                    Err(e) => {
                        result.error = Some(format!(
                            "netsh delete rule {}: {}",
                            effective_rule_name, e
                        ));
                    }
                    _ => {}
                }
            }
            _ => {
                result.error = Some(format!(
                    "firewall_windows executor: unknown state {:?}",
                    state
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
    fn missing_name_and_port() {
        let exec = FirewallWindowsExecutor::new();
        let params = HashMap::new();
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result
            .error
            .unwrap()
            .contains("name or port param is required"));
    }

    #[test]
    fn invalid_direction() {
        let exec = FirewallWindowsExecutor::new();
        let mut params = HashMap::new();
        params.insert("name".to_string(), "AllowHTTP".to_string());
        params.insert("direction".to_string(), "sideways".to_string());
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("unknown direction"));
    }

    #[test]
    fn unknown_state() {
        let exec = FirewallWindowsExecutor::new();
        let mut params = HashMap::new();
        params.insert("name".to_string(), "AllowHTTP".to_string());
        params.insert("state".to_string(), "bogus".to_string());
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("unknown state"));
    }
}
