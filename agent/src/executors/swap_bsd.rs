// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Swap executor for BSD — manages swap devices via `swapon`/`swapoff`.

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::Instant;

pub struct SwapBsdExecutor {
    pub runner: Box<dyn CommandRunner>,
}

impl SwapBsdExecutor {
    pub fn new() -> Self {
        Self { runner: Box::new(DefaultRunner) }
    }

    fn is_active(&self, device: &str) -> bool {
        // swapinfo (FreeBSD) or swapctl -l (OpenBSD/NetBSD)
        let output = if std::env::consts::OS == "freebsd" {
            self.runner.run("swapinfo", &[])
        } else {
            self.runner.run("swapctl", &["-l"])
        };
        output.ok()
            .filter(|o| o.status.success())
            .map(|o| {
                let stdout = String::from_utf8_lossy(&o.stdout);
                stdout.lines().any(|line| line.starts_with(device))
            })
            .unwrap_or(false)
    }
}

impl Executor for SwapBsdExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let device = params.get("device").cloned().unwrap_or_default();
        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");
        let mut result = ResourceResult::new("swap", &device);

        if device.is_empty() {
            result.error = Some("swap executor: device param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let active = self.is_active(&device);

        match state {
            "present" | "on" => {
                if active { return { result.duration_ms = start.elapsed().as_millis() as i32; result }; }
                result.action = Action::Created;
                result.changed = true;
                if !dry_run {
                    match self.runner.run("swapon", &[&device]) {
                        Ok(o) if !o.status.success() => {
                            result.error = Some(format!("swapon: {}", String::from_utf8_lossy(&o.stderr)));
                        }
                        Err(e) => result.error = Some(format!("swapon: {}", e)),
                        _ => {}
                    }
                }
            }
            "absent" | "off" => {
                if !active { return { result.duration_ms = start.elapsed().as_millis() as i32; result }; }
                result.action = Action::Deleted;
                result.changed = true;
                if !dry_run {
                    match self.runner.run("swapoff", &[&device]) {
                        Ok(o) if !o.status.success() => {
                            result.error = Some(format!("swapoff: {}", String::from_utf8_lossy(&o.stderr)));
                        }
                        Err(e) => result.error = Some(format!("swapoff: {}", e)),
                        _ => {}
                    }
                }
            }
            other => result.error = Some(format!("unknown swap state: {}", other)),
        }

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn missing_device_errors() {
        let exec = SwapBsdExecutor::new();
        let result = exec.execute(&HashMap::new(), false);
        assert!(result.error.is_some());
    }
}
