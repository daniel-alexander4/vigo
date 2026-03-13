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

#[cfg(test)]
mod tests {
    use super::*;
    use std::os::unix::process::ExitStatusExt;
    use std::process::Output;
    use std::sync::{Arc, Mutex};

    struct MockRunner {
        responses: Mutex<Vec<std::io::Result<Output>>>,
        calls: Arc<Mutex<Vec<(String, Vec<String>)>>>,
    }

    impl MockRunner {
        fn new(responses: Vec<Output>) -> Self {
            Self {
                responses: Mutex::new(responses.into_iter().map(Ok).collect()),
                calls: Arc::new(Mutex::new(Vec::new())),
            }
        }

        fn empty() -> Self {
            Self {
                responses: Mutex::new(Vec::new()),
                calls: Arc::new(Mutex::new(Vec::new())),
            }
        }

        fn ok_output(stdout: &str) -> Output {
            Output {
                status: std::process::ExitStatus::from_raw(0),
                stdout: stdout.as_bytes().to_vec(),
                stderr: Vec::new(),
            }
        }

        fn fail_output(stderr: &str) -> Output {
            Output {
                status: std::process::ExitStatus::from_raw(256), // exit code 1
                stdout: Vec::new(),
                stderr: stderr.as_bytes().to_vec(),
            }
        }
    }

    impl CommandRunner for MockRunner {
        fn run(&self, name: &str, args: &[&str]) -> std::io::Result<Output> {
            self.calls.lock().unwrap().push((
                name.to_string(),
                args.iter().map(|s| s.to_string()).collect(),
            ));
            self.responses.lock().unwrap().remove(0)
        }

        fn run_with_stdin(
            &self,
            _: &[u8],
            name: &str,
            args: &[&str],
        ) -> std::io::Result<Output> {
            self.run(name, args)
        }
    }

    fn make_params(pairs: &[(&str, &str)]) -> HashMap<String, String> {
        pairs
            .iter()
            .map(|(k, v)| (k.to_string(), v.to_string()))
            .collect()
    }

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
            MockRunner::fail_output("no systemd"),
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

    #[test]
    fn name_alias_works() {
        let current = current_hostname_value();
        let mock = MockRunner::empty();
        let exec = HostnameExecutor {
            runner: Box::new(mock),
        };
        // Using "name" key instead of "hostname".
        let result = exec.execute(
            &make_params(&[("name", &current)]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());
    }
}
