// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Timezone executor for macOS — uses `systemsetup`.

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::Instant;

pub struct TimezoneMacosExecutor {
    pub runner: Box<dyn CommandRunner>,
}

impl TimezoneMacosExecutor {
    pub fn new() -> Self {
        Self { runner: Box::new(DefaultRunner) }
    }

    fn current_timezone(&self) -> String {
        self.runner.run("systemsetup", &["-gettimezone"])
            .ok()
            .filter(|o| o.status.success())
            .map(|o| {
                let stdout = String::from_utf8_lossy(&o.stdout);
                // Output: "Time Zone: America/New_York"
                stdout.trim()
                    .strip_prefix("Time Zone: ")
                    .unwrap_or("")
                    .to_string()
            })
            .unwrap_or_default()
    }
}

impl Executor for TimezoneMacosExecutor {
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

        match self.runner.run("systemsetup", &["-settimezone", &desired]) {
            Ok(o) if o.status.success() => {}
            Ok(o) => {
                let stderr = String::from_utf8_lossy(&o.stderr);
                result.error = Some(format!("systemsetup -settimezone: {}", stderr));
            }
            Err(e) => result.error = Some(format!("systemsetup: {}", e)),
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
        let exec = TimezoneMacosExecutor::new();
        let result = exec.execute(&HashMap::new(), false);
        assert!(result.error.is_some());
    }
}
