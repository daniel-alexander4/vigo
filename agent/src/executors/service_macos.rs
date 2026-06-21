// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Service executor for macOS — manages launchd services via `launchctl`.

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::Instant;

pub struct ServiceMacosExecutor {
    pub runner: Box<dyn CommandRunner>,
}

impl ServiceMacosExecutor {
    pub fn new() -> Self {
        Self { runner: Box::new(DefaultRunner) }
    }

    /// Check if service is loaded and running.
    fn service_status(&self, label: &str) -> (bool, bool) {
        // launchctl list <label> — exit 0 means loaded
        let loaded = self.runner.run("launchctl", &["list", label])
            .map(|o| o.status.success())
            .unwrap_or(false);

        // Check if actually running by parsing launchctl list output
        let running = if loaded {
            self.runner.run("launchctl", &["list", label])
                .ok()
                .map(|o| {
                    let stdout = String::from_utf8_lossy(&o.stdout);
                    // If PID line shows a number (not "-" or "0"), it's running
                    for line in stdout.lines() {
                        if let Some(rest) = line.strip_prefix("\"PID\"") {
                            return !rest.contains("-");
                        }
                    }
                    // Simple heuristic: if launchctl list succeeds, check first column
                    // Format: PID Status Label (tab separated)
                    stdout.lines().any(|l| {
                        let parts: Vec<&str> = l.split('\t').collect();
                        parts.first().map(|p| p.trim() != "-" && !p.is_empty() && p.chars().all(|c| c.is_ascii_digit())).unwrap_or(false)
                    })
                })
                .unwrap_or(false)
        } else {
            false
        };

        (loaded, running)
    }

    fn execute_one(&self, label: &str, desired_state: &str, dry_run: bool) -> (Action, bool, Option<String>) {
        let (loaded, running) = self.service_status(label);

        match desired_state {
            "running" | "started" => {
                if running { return (Action::None, false, None); }
                if dry_run { return (Action::Updated, true, None); }
                if !loaded {
                    // Try to load/bootstrap
                    let plist = format!("/Library/LaunchDaemons/{}.plist", label);
                    if std::path::Path::new(&plist).exists() {
                        if let Ok(o) = self.runner.run("launchctl", &["load", &plist]) {
                            if !o.status.success() {
                                let stderr = String::from_utf8_lossy(&o.stderr);
                                return (Action::Updated, true, Some(format!("launchctl load: {}", stderr)));
                            }
                        }
                    }
                }
                match self.runner.run("launchctl", &["start", label]) {
                    Ok(o) if o.status.success() => (Action::Updated, true, None),
                    Ok(o) => {
                        let stderr = String::from_utf8_lossy(&o.stderr);
                        (Action::Updated, true, Some(format!("launchctl start: {}", stderr)))
                    }
                    Err(e) => (Action::Updated, true, Some(format!("launchctl start: {}", e))),
                }
            }
            "stopped" => {
                if !running { return (Action::None, false, None); }
                if dry_run { return (Action::Updated, true, None); }
                match self.runner.run("launchctl", &["stop", label]) {
                    Ok(o) if o.status.success() => (Action::Updated, true, None),
                    Ok(o) => {
                        let stderr = String::from_utf8_lossy(&o.stderr);
                        (Action::Updated, true, Some(format!("launchctl stop: {}", stderr)))
                    }
                    Err(e) => (Action::Updated, true, Some(format!("launchctl stop: {}", e))),
                }
            }
            "enabled" => {
                if loaded { return (Action::None, false, None); }
                if dry_run { return (Action::Updated, true, None); }
                let plist = format!("/Library/LaunchDaemons/{}.plist", label);
                match self.runner.run("launchctl", &["load", "-w", &plist]) {
                    Ok(o) if o.status.success() => (Action::Updated, true, None),
                    Ok(o) => {
                        let stderr = String::from_utf8_lossy(&o.stderr);
                        (Action::Updated, true, Some(format!("launchctl load: {}", stderr)))
                    }
                    Err(e) => (Action::Updated, true, Some(format!("launchctl load: {}", e))),
                }
            }
            "disabled" => {
                if !loaded { return (Action::None, false, None); }
                if dry_run { return (Action::Updated, true, None); }
                let plist = format!("/Library/LaunchDaemons/{}.plist", label);
                match self.runner.run("launchctl", &["unload", "-w", &plist]) {
                    Ok(o) if o.status.success() => (Action::Updated, true, None),
                    Ok(o) => {
                        let stderr = String::from_utf8_lossy(&o.stderr);
                        (Action::Updated, true, Some(format!("launchctl unload: {}", stderr)))
                    }
                    Err(e) => (Action::Updated, true, Some(format!("launchctl unload: {}", e))),
                }
            }
            "restarted" => {
                if dry_run { return (Action::Updated, true, None); }
                if running {
                    let _ = self.runner.run("launchctl", &["stop", label]);
                }
                match self.runner.run("launchctl", &["start", label]) {
                    Ok(o) if o.status.success() => (Action::Updated, true, None),
                    Ok(o) => {
                        let stderr = String::from_utf8_lossy(&o.stderr);
                        (Action::Updated, true, Some(format!("launchctl restart: {}", stderr)))
                    }
                    Err(e) => (Action::Updated, true, Some(format!("launchctl restart: {}", e))),
                }
            }
            other => (Action::None, false, Some(format!("unknown service state: {}", other))),
        }
    }
}

impl Executor for ServiceMacosExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let services = params.get("service").cloned().unwrap_or_default();
        let state = params.get("state").map(|s| s.as_str()).unwrap_or("running");

        let mut result = ResourceResult::new("service", &services);

        if services.is_empty() {
            result.error = Some("service executor: service param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        for svc in services.split(',').map(|s| s.trim()) {
            if svc.is_empty() { continue; }
            let (action, changed, error) = self.execute_one(svc, state, dry_run);
            if changed { result.changed = true; }
            if result.action == Action::None && action != Action::None {
                result.action = action;
            }
            if let Some(e) = error {
                result.error = Some(e);
                break;
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
    fn missing_service_errors() {
        let exec = ServiceMacosExecutor::new();
        let result = exec.execute(&HashMap::new(), false);
        assert!(result.error.is_some());
    }
}
