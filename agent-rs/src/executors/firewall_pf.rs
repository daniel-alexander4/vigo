// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Firewall executor using pf (Packet Filter) — shared by macOS and FreeBSD.

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::Instant;

pub struct FirewallPfExecutor {
    pub runner: Box<dyn CommandRunner>,
}

impl FirewallPfExecutor {
    pub fn new() -> Self {
        Self { runner: Box::new(DefaultRunner) }
    }

    fn pf_enabled(&self) -> bool {
        self.runner.run("pfctl", &["-s", "info"])
            .ok()
            .map(|o| {
                let stdout = String::from_utf8_lossy(&o.stdout);
                stdout.contains("Status: Enabled")
            })
            .unwrap_or(false)
    }

    fn rule_exists(&self, rule: &str) -> bool {
        self.runner.run("pfctl", &["-s", "rules"])
            .ok()
            .filter(|o| o.status.success())
            .map(|o| {
                let stdout = String::from_utf8_lossy(&o.stdout);
                stdout.lines().any(|line| line.trim() == rule.trim())
            })
            .unwrap_or(false)
    }
}

impl Executor for FirewallPfExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let name = params.get("name").cloned().unwrap_or_else(|| "pf-rule".to_string());
        let mut result = ResourceResult::new("firewall", &name);

        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");

        // Handle enable/disable of pf itself.
        if let Some(enabled_str) = params.get("enabled") {
            let desired_enabled = enabled_str == "true" || enabled_str == "yes";
            let current_enabled = self.pf_enabled();

            if desired_enabled != current_enabled {
                result.action = Action::Updated;
                result.changed = true;
                if !dry_run {
                    let flag = if desired_enabled { "-e" } else { "-d" };
                    match self.runner.run("pfctl", &[flag]) {
                        Ok(o) if o.status.success() => {}
                        Ok(o) => {
                            let stderr = String::from_utf8_lossy(&o.stderr);
                            result.error = Some(format!("pfctl {}: {}", flag, stderr));
                        }
                        Err(e) => result.error = Some(format!("pfctl: {}", e)),
                    }
                }
            }
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        // Handle individual rules via the "rule" param and pf.conf anchor.
        if let Some(rule) = params.get("rule") {
            match state {
                "present" => {
                    if self.rule_exists(rule) {
                        result.action = Action::None;
                    } else {
                        result.action = Action::Created;
                        result.changed = true;
                        if !dry_run {
                            // Add rule via echo | pfctl -f -
                            match self.runner.run_with_stdin(
                                rule.as_bytes(),
                                "pfctl",
                                &["-a", "hobnob", "-f", "-"],
                            ) {
                                Ok(o) if o.status.success() => {}
                                Ok(o) => {
                                    let stderr = String::from_utf8_lossy(&o.stderr);
                                    result.error = Some(format!("pfctl add rule: {}", stderr));
                                }
                                Err(e) => result.error = Some(format!("pfctl: {}", e)),
                            }
                        }
                    }
                }
                "absent" => {
                    if !self.rule_exists(rule) {
                        result.action = Action::None;
                    } else {
                        result.action = Action::Deleted;
                        result.changed = true;
                        if !dry_run {
                            // Flush the hobnob anchor to remove the rule.
                            match self.runner.run("pfctl", &["-a", "hobnob", "-F", "rules"]) {
                                Ok(o) if o.status.success() => {}
                                Ok(o) => {
                                    let stderr = String::from_utf8_lossy(&o.stderr);
                                    result.error = Some(format!("pfctl flush: {}", stderr));
                                }
                                Err(e) => result.error = Some(format!("pfctl: {}", e)),
                            }
                        }
                    }
                }
                other => result.error = Some(format!("unknown firewall state: {}", other)),
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
    fn missing_rule_no_panic() {
        let exec = FirewallPfExecutor::new();
        let result = exec.execute(&HashMap::new(), true);
        // No rule or enabled param — should return cleanly
        assert!(result.error.is_none());
    }
}
