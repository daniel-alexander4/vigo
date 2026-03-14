// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0

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

#[cfg(test)]
mod tests {
    use super::*;
    use crate::executors::test_support::mock::*;
    use std::sync::Arc;

    fn current_hostname_value() -> String {
        if let Ok(s) = std::fs::read_to_string("/etc/hostname") {
            let trimmed = s.trim().to_string();
            if !trimmed.is_empty() {
                return trimmed;
            }
        }
        hostname::get()
            .map(|h| h.to_string_lossy().to_string())
            .unwrap_or_default()
    }

    #[test]
    fn missing_hostname_errors() {
        let mock = MockRunner::empty();
        let exec = HostnameExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(&make_params(&[]), false);
        assert!(result.error.is_some());
        assert!(
            result.error.as_deref().unwrap().contains("hostname param is required"),
            "unexpected error: {:?}",
            result.error
        );
    }

    #[test]
    fn already_matches() {
        let current = current_hostname_value();
        let mock = MockRunner::empty();
        let calls = Arc::clone(&mock.calls);
        let exec = HostnameExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("hostname", &current)]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());
        // No commands should have been called.
        assert!(calls.lock().unwrap().is_empty());
    }

    #[test]
    fn dry_run_does_not_call_commands() {
        // Use a hostname guaranteed to differ from current.
        let mock = MockRunner::empty();
        let calls = Arc::clone(&mock.calls);
        let exec = HostnameExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("hostname", "test-hostname-that-does-not-exist-98765")]),
            true,
        );
        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);
        assert!(result.error.is_none());
        // No runner calls in dry_run mode.
        assert!(
            calls.lock().unwrap().is_empty(),
            "dry_run should not invoke any commands"
        );
    }

    #[test]
    fn hostnamectl_failure_falls_through() {
        // When hostnamectl fails, the executor falls through to write /etc/hostname.
        // That write will fail in tests (no root), but we can verify the fallthrough
        // by checking that hostnamectl was called.
        let mock = MockRunner::new(vec![
            fail_output("no systemd"),
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = HostnameExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("hostname", "test-hostname-that-does-not-exist-98765")]),
            false,
        );
        // hostnamectl was attempted.
        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1);
        assert_eq!(calls[0].0, "hostnamectl");
        assert_eq!(calls[0].1, vec!["set-hostname", "test-hostname-that-does-not-exist-98765"]);
        // The write to /etc/hostname will fail (no root perms), so there should be an error.
        // That's expected — the point is verifying fallthrough.
        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);
    }

}
