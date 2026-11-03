// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Service executor for illumos — manages services via SMF (`svcadm`/`svcs`).

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::Instant;

pub struct ServiceIllumosExecutor {
    pub runner: Box<dyn CommandRunner>,
}

impl ServiceIllumosExecutor {
    pub fn new() -> Self {
        Self { runner: Box::new(DefaultRunner) }
    }

    fn service_state(&self, name: &str) -> Option<String> {
        self.runner.run("svcs", &["-H", "-o", "STATE", name])
            .ok()
            .filter(|o| o.status.success())
            .map(|o| String::from_utf8_lossy(&o.stdout).trim().to_string())
    }

    fn is_running(&self, name: &str) -> bool {
        self.service_state(name)
            .map(|s| s == "online")
            .unwrap_or(false)
    }

    fn is_enabled(&self, name: &str) -> bool {
        self.service_state(name)
            .map(|s| s != "disabled")
            .unwrap_or(false)
    }

    fn execute_one(&self, name: &str, desired: &str, dry_run: bool) -> (Action, bool, Option<String>) {
        let running = self.is_running(name);
        let enabled = self.is_enabled(name);

        match desired {
            "running" | "started" => {
                if running && enabled { return (Action::None, false, None); }
                if dry_run { return (Action::Updated, true, None); }
                let mut err = None;
                if !enabled {
                    if let Ok(o) = self.runner.run("svcadm", &["enable", name]) {
                        if !o.status.success() {
                            err = Some(format!("svcadm enable: {}", String::from_utf8_lossy(&o.stderr)));
                        }
                    }
                } else if !running {
                    // Enabled but not online — try clear (in case of maintenance) then restart.
                    let state = self.service_state(name).unwrap_or_default();
                    if state == "maintenance" {
                        if let Ok(o) = self.runner.run("svcadm", &["clear", name]) {
                            if !o.status.success() {
                                err = Some(format!("svcadm clear: {}", String::from_utf8_lossy(&o.stderr)));
                            }
                        }
                    } else {
                        match self.runner.run("svcadm", &["enable", "-t", name]) {
                            Ok(o) if !o.status.success() => {
                                err = Some(format!("svcadm enable: {}", String::from_utf8_lossy(&o.stderr)));
                            }
                            Err(e) => err = Some(format!("svcadm enable: {}", e)),
                            _ => {}
                        }
                    }
                }
                (Action::Updated, true, err)
            }
            "stopped" => {
                if !running { return (Action::None, false, None); }
                if dry_run { return (Action::Updated, true, None); }
                match self.runner.run("svcadm", &["disable", "-t", name]) {
                    Ok(o) if o.status.success() => (Action::Updated, true, None),
                    Ok(o) => (Action::Updated, true, Some(format!("svcadm disable: {}", String::from_utf8_lossy(&o.stderr)))),
                    Err(e) => (Action::Updated, true, Some(format!("svcadm disable: {}", e))),
                }
            }
            "enabled" => {
                if enabled { return (Action::None, false, None); }
                if dry_run { return (Action::Updated, true, None); }
                match self.runner.run("svcadm", &["enable", name]) {
                    Ok(o) if o.status.success() => (Action::Updated, true, None),
                    Ok(o) => (Action::Updated, true, Some(format!("svcadm enable: {}", String::from_utf8_lossy(&o.stderr)))),
                    Err(e) => (Action::Updated, true, Some(format!("svcadm enable: {}", e))),
                }
            }
            "disabled" => {
                if !enabled { return (Action::None, false, None); }
                if dry_run { return (Action::Updated, true, None); }
                match self.runner.run("svcadm", &["disable", name]) {
                    Ok(o) if o.status.success() => (Action::Updated, true, None),
                    Ok(o) => (Action::Updated, true, Some(format!("svcadm disable: {}", String::from_utf8_lossy(&o.stderr)))),
                    Err(e) => (Action::Updated, true, Some(format!("svcadm disable: {}", e))),
                }
            }
            "restarted" => {
                if dry_run { return (Action::Updated, true, None); }
                match self.runner.run("svcadm", &["restart", name]) {
                    Ok(o) if o.status.success() => (Action::Updated, true, None),
                    Ok(o) => (Action::Updated, true, Some(format!("svcadm restart: {}", String::from_utf8_lossy(&o.stderr)))),
                    Err(e) => (Action::Updated, true, Some(format!("svcadm restart: {}", e))),
                }
            }
            other => (Action::None, false, Some(format!("unknown state: {}", other))),
        }
    }
}

impl Executor for ServiceIllumosExecutor {
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
            if result.action == Action::None && action != Action::None { result.action = action; }
            if let Some(e) = error { result.error = Some(e); break; }
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
        let exec = ServiceIllumosExecutor::new();
        let result = exec.execute(&HashMap::new(), false);
        assert!(result.error.is_some());
    }
}
