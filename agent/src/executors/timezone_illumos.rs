// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Timezone executor for illumos — reads/writes `TZ=` in `/etc/default/init`.

use super::{Action, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::Instant;

pub struct TimezoneIllumosExecutor;

impl TimezoneIllumosExecutor {
    pub fn new() -> Self {
        Self
    }

    fn current_timezone(&self) -> String {
        if let Ok(content) = std::fs::read_to_string("/etc/default/init") {
            for line in content.lines() {
                let trimmed = line.trim();
                if let Some(tz) = trimmed.strip_prefix("TZ=") {
                    return tz.to_string();
                }
            }
        }
        String::new()
    }
}

impl Executor for TimezoneIllumosExecutor {
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

        result.action = Action::Updated;
        result.changed = true;
        if dry_run {
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        // Validate timezone exists.
        let zoneinfo = format!("/usr/share/lib/zoneinfo/{}", desired);
        if !std::path::Path::new(&zoneinfo).exists() {
            result.error = Some(format!("timezone {} not found in /usr/share/lib/zoneinfo/", desired));
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        // Update TZ= line in /etc/default/init.
        let content = std::fs::read_to_string("/etc/default/init").unwrap_or_default();
        let mut found = false;
        let mut lines: Vec<String> = content.lines().map(|line| {
            if line.trim().starts_with("TZ=") {
                found = true;
                format!("TZ={}", desired)
            } else {
                line.to_string()
            }
        }).collect();

        if !found {
            lines.push(format!("TZ={}", desired));
        }

        if let Err(e) = std::fs::write("/etc/default/init", lines.join("\n") + "\n") {
            result.error = Some(format!("write /etc/default/init: {}", e));
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
        let exec = TimezoneIllumosExecutor::new();
        let result = exec.execute(&HashMap::new(), false);
        assert!(result.error.is_some());
    }
}
