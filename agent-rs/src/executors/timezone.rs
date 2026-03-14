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

#[cfg(test)]
mod tests {
    use super::*;
    use crate::executors::test_support::mock::*;

    /// Helper to detect current system timezone the same way the executor does.
    fn detect_system_timezone() -> String {
        if let Ok(s) = std::fs::read_to_string("/etc/timezone") {
            let trimmed = s.trim().to_string();
            if !trimmed.is_empty() {
                return trimmed;
            }
        }
        if let Ok(target) = std::fs::read_link("/etc/localtime") {
            let s = target.to_string_lossy().to_string();
            if let Some(pos) = s.find("/zoneinfo/") {
                return s[pos + 10..].to_string();
            }
        }
        String::new()
    }

    #[test]
    fn missing_timezone_errors() {
        let mock = MockRunner::new(vec![]);
        let exec = TimezoneExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(&make_params(&[]), false);
        assert!(result.error.is_some());
        assert!(
            result
                .error
                .unwrap()
                .contains("timezone param is required")
        );
    }

    #[test]
    fn already_correct() {
        let current = detect_system_timezone();
        if current.is_empty() {
            // Cannot determine system timezone — skip test gracefully.
            return;
        }
        // No commands should be called since timezone already matches.
        let mock = MockRunner::new(vec![]);
        let calls = mock.calls.clone();
        let exec = TimezoneExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(&make_params(&[("timezone", &current)]), false);
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 0, "no commands should be run when timezone already matches");
    }

    #[test]
    fn updates_timezone() {
        // Use a timezone that definitely differs from any real system timezone.
        let desired = "Fake/Nonexistent_Zone_For_Test";
        let timedatectl = ok_output("");
        let mock = MockRunner::new(vec![timedatectl]);
        let calls = mock.calls.clone();
        let exec = TimezoneExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(&make_params(&[("timezone", desired)]), false);
        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);
        assert!(result.error.is_none());

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1);
        assert_eq!(calls[0].0, "timedatectl");
        assert_eq!(calls[0].1, vec!["set-timezone", desired]);
    }

    #[test]
    fn dry_run_does_not_call_timedatectl_set() {
        let desired = "Fake/Nonexistent_Zone_For_Test";
        let mock = MockRunner::new(vec![]);
        let calls = mock.calls.clone();
        let exec = TimezoneExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(&make_params(&[("timezone", desired)]), true);
        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);
        assert!(result.error.is_none());

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 0, "dry_run should not call timedatectl");
    }

    #[test]
    fn unknown_state_uses_name_alias() {
        // The "name" param is accepted as an alias for "timezone".
        let desired = "Fake/Another_Test_Zone";
        let timedatectl = ok_output("");
        let mock = MockRunner::new(vec![timedatectl]);
        let exec = TimezoneExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(&make_params(&[("name", desired)]), false);
        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);
        assert!(result.error.is_none());
    }
}
