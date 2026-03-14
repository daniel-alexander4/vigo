// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Service executor for OpenBSD — manages services via `rcctl`.

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::Instant;

pub struct ServiceOpenbsdExecutor {
    pub runner: Box<dyn CommandRunner>,
}

impl ServiceOpenbsdExecutor {
    pub fn new() -> Self {
        Self { runner: Box::new(DefaultRunner) }
    }

    fn is_running(&self, name: &str) -> bool {
        self.runner.run("rcctl", &["check", name])
            .map(|o| o.status.success())
            .unwrap_or(false)
    }

    fn is_enabled(&self, name: &str) -> bool {
        self.runner.run("rcctl", &["get", name, "status"])
            .ok()
            .filter(|o| o.status.success())
            .map(|o| {
                let val = String::from_utf8_lossy(&o.stdout).trim().to_string();
                val != "NO" && val != "no"
            })
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
                    if let Ok(o) = self.runner.run("rcctl", &["enable", name]) {
                        if !o.status.success() {
                            err = Some(format!("rcctl enable: {}", String::from_utf8_lossy(&o.stderr)));
                        }
                    }
                }
                if err.is_none() && !running {
                    match self.runner.run("rcctl", &["start", name]) {
                        Ok(o) if !o.status.success() => {
                            err = Some(format!("rcctl start: {}", String::from_utf8_lossy(&o.stderr)));
                        }
                        Err(e) => err = Some(format!("rcctl start: {}", e)),
                        _ => {}
                    }
                }
                (Action::Updated, true, err)
            }
            "stopped" => {
                if !running { return (Action::None, false, None); }
                if dry_run { return (Action::Updated, true, None); }
                match self.runner.run("rcctl", &["stop", name]) {
                    Ok(o) if o.status.success() => (Action::Updated, true, None),
                    Ok(o) => (Action::Updated, true, Some(format!("rcctl stop: {}", String::from_utf8_lossy(&o.stderr)))),
                    Err(e) => (Action::Updated, true, Some(format!("rcctl stop: {}", e))),
                }
            }
            "enabled" => {
                if enabled { return (Action::None, false, None); }
                if dry_run { return (Action::Updated, true, None); }
                match self.runner.run("rcctl", &["enable", name]) {
                    Ok(o) if o.status.success() => (Action::Updated, true, None),
                    Ok(o) => (Action::Updated, true, Some(format!("rcctl enable: {}", String::from_utf8_lossy(&o.stderr)))),
                    Err(e) => (Action::Updated, true, Some(format!("rcctl enable: {}", e))),
                }
            }
            "disabled" => {
                if !enabled { return (Action::None, false, None); }
                if dry_run { return (Action::Updated, true, None); }
                match self.runner.run("rcctl", &["disable", name]) {
                    Ok(o) if o.status.success() => (Action::Updated, true, None),
                    Ok(o) => (Action::Updated, true, Some(format!("rcctl disable: {}", String::from_utf8_lossy(&o.stderr)))),
                    Err(e) => (Action::Updated, true, Some(format!("rcctl disable: {}", e))),
                }
            }
            "restarted" => {
                if dry_run { return (Action::Updated, true, None); }
                match self.runner.run("rcctl", &["restart", name]) {
                    Ok(o) if o.status.success() => (Action::Updated, true, None),
                    Ok(o) => (Action::Updated, true, Some(format!("rcctl restart: {}", String::from_utf8_lossy(&o.stderr)))),
                    Err(e) => (Action::Updated, true, Some(format!("rcctl restart: {}", e))),
                }
            }
            other => (Action::None, false, Some(format!("unknown state: {}", other))),
        }
    }
}

impl Executor for ServiceOpenbsdExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let services = params.get("service").or_else(|| params.get("name")).cloned().unwrap_or_default();
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
        let exec = ServiceOpenbsdExecutor::new();
        let result = exec.execute(&HashMap::new(), false);
        assert!(result.error.is_some());
    }
}
