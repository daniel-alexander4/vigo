// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Timezone executor for BSD — copies zoneinfo file to `/etc/localtime`.

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::Instant;

pub struct TimezoneBsdExecutor {
    pub runner: Box<dyn CommandRunner>,
}

impl TimezoneBsdExecutor {
    pub fn new() -> Self {
        Self { runner: Box::new(DefaultRunner) }
    }

    fn current_timezone(&self) -> String {
        // Try reading /etc/timezone first.
        if let Ok(tz) = std::fs::read_to_string("/etc/timezone") {
            let trimmed = tz.trim().to_string();
            if !trimmed.is_empty() {
                return trimmed;
            }
        }
        // Try readlink on /etc/localtime.
        if let Ok(target) = std::fs::read_link("/etc/localtime") {
            let s = target.to_string_lossy();
            if let Some(pos) = s.find("/zoneinfo/") {
                return s[pos + "/zoneinfo/".len()..].to_string();
            }
        }
        String::new()
    }
}

impl Executor for TimezoneBsdExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let desired = params.get("timezone").cloned().unwrap_or_default();
        let mut result = ResourceResult::new("timezone", &desired);

        if desired.is_empty() {
            result.error = Some("timezone executor: timezone param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        if self.current_timezone() == desired {
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        if dry_run {
            result.action = Action::Updated;
            result.changed = true;
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let zoneinfo = format!("/usr/share/zoneinfo/{}", desired);
        if !std::path::Path::new(&zoneinfo).exists() {
            result.error = Some(format!("timezone {} not found in /usr/share/zoneinfo/", desired));
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        // Copy zoneinfo file to /etc/localtime (BSD convention).
        if let Err(e) = std::fs::copy(&zoneinfo, "/etc/localtime") {
            result.error = Some(format!("copy zoneinfo: {}", e));
        } else {
            result.action = Action::Updated;
            result.changed = true;
        }

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn missing_timezone_errors() {
        let exec = TimezoneBsdExecutor::new();
        let result = exec.execute(&HashMap::new(), false);
        assert!(result.error.is_some());
    }
}
