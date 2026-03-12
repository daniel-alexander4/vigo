// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Hostname executor — sets the system hostname idempotently.

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::Instant;

pub struct HostnameExecutor {
    pub runner: Box<dyn CommandRunner>,
}

impl HostnameExecutor {
    pub fn new() -> Self {
        Self {
            runner: Box::new(DefaultRunner),
        }
    }

    fn current_hostname(&self) -> String {
        // Try /etc/hostname first.
        if let Ok(s) = std::fs::read_to_string("/etc/hostname") {
            let trimmed = s.trim().to_string();
            if !trimmed.is_empty() {
                return trimmed;
            }
        }
        // Fallback to hostname crate.
        hostname::get()
            .map(|h| h.to_string_lossy().to_string())
            .unwrap_or_default()
    }
}

impl Executor for HostnameExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let desired = params
            .get("hostname")
            .or_else(|| params.get("name"))
            .cloned()
            .unwrap_or_default();

        let mut result = ResourceResult::new("hostname", &desired);

        if desired.is_empty() {
            result.error = Some("hostname executor: hostname param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let current = self.current_hostname();
        if current == desired {
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

        // Try hostnamectl first (systemd).
        let output = self.runner.run("hostnamectl", &["set-hostname", &desired]);
        if let Ok(o) = &output {
            if o.status.success() {
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
        }

        // Fallback: write /etc/hostname + hostname command.
        if let Err(e) = std::fs::write("/etc/hostname", format!("{}\n", desired)) {
            result.error = Some(format!("write /etc/hostname: {}", e));
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        if let Ok(o) = self.runner.run("hostname", &[&desired]) {
            if !o.status.success() {
                let stderr = String::from_utf8_lossy(&o.stderr);
                result.error = Some(format!("hostname {}: {}", desired, stderr));
            }
        }

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}
