// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Timezone executor — sets the system timezone idempotently.

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::Instant;

pub struct TimezoneExecutor {
    pub runner: Box<dyn CommandRunner>,
}

impl TimezoneExecutor {
    pub fn new() -> Self {
        Self {
            runner: Box::new(DefaultRunner),
        }
    }

    fn current_timezone(&self) -> String {
        // Try /etc/timezone.
        if let Ok(s) = std::fs::read_to_string("/etc/timezone") {
            let trimmed = s.trim().to_string();
            if !trimmed.is_empty() {
                return trimmed;
            }
        }
        // Fallback: readlink /etc/localtime.
        if let Ok(target) = std::fs::read_link("/etc/localtime") {
            let s = target.to_string_lossy().to_string();
            if let Some(pos) = s.find("/zoneinfo/") {
                return s[pos + 10..].to_string();
            }
        }
        String::new()
    }
}

impl Executor for TimezoneExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let desired = params
            .get("timezone")
            .or_else(|| params.get("name"))
            .cloned()
            .unwrap_or_default();

        let mut result = ResourceResult::new("timezone", &desired);

        if desired.is_empty() {
            result.error = Some("timezone executor: timezone param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let current = self.current_timezone();
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

        // Try timedatectl first.
        if let Ok(o) = self.runner.run("timedatectl", &["set-timezone", &desired]) {
            if o.status.success() {
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
        }

        // Fallback: symlink + /etc/timezone.
        let zoneinfo = format!("/usr/share/zoneinfo/{}", desired);
        let _ = std::fs::remove_file("/etc/localtime");
        #[cfg(unix)]
        {
            if let Err(e) = std::os::unix::fs::symlink(&zoneinfo, "/etc/localtime") {
                result.error = Some(format!("symlink /etc/localtime -> {}: {}", zoneinfo, e));
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
        }

        let _ = std::fs::write("/etc/timezone", format!("{}\n", desired));

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}
