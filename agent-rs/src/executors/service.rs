// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Service executor — manages systemd services idempotently.
//!
//! Checks active/enabled state via `systemctl show` before taking action.

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::Instant;

/// Manages systemd service resources.
pub struct ServiceExecutor {
    pub runner: Box<dyn CommandRunner>,
}

impl ServiceExecutor {
    pub fn new() -> Self {
        Self {
            runner: Box::new(DefaultRunner),
        }
    }

    /// Query service active/enabled state in a single systemctl call.
    fn service_status(&self, name: &str) -> (bool, bool) {
        let output = self.runner.run(
            "systemctl",
            &["show", "--no-pager", "--property=ActiveState,UnitFileState", name],
        );

        let stdout = match output {
            Ok(o) if o.status.success() => String::from_utf8_lossy(&o.stdout).to_string(),
            _ => return (false, false),
        };

        let mut active = false;
        let mut enabled = false;

        for line in stdout.lines() {
            if let Some((key, val)) = line.split_once('=') {
                match key {
                    "ActiveState" => {
                        active = val == "active" || val == "activating";
                    }
                    "UnitFileState" => {
                        enabled = val == "enabled" || val == "enabled-runtime";
                    }
                    _ => {}
                }
            }
        }

        (active, enabled)
    }

    fn execute_one(
        &self,
        name: &str,
        state: &str,
        enabled_param: &str,
        dry_run: bool,
    ) -> ResourceResult {
        let mut result = ResourceResult::new("service", name);

        let (active, enabled) = self.service_status(name);

        let mut changed = false;
        let mut state_cmd = None;

        match state {
            "running" => {
                if !active {
                    state_cmd = Some("start");
                    changed = true;
                }
            }
            "stopped" => {
                if active {
                    state_cmd = Some("stop");
                    changed = true;
                }
            }
            "restarted" => {
                state_cmd = Some("restart");
                changed = true;
            }
            "reloaded" => {
                state_cmd = Some("reload");
                changed = true;
            }
            _ => {
                result.error = Some(format!("service executor: unknown state {:?}", state));
                return result;
            }
        }

        let mut enable_cmd = None;
        if enabled_param == "true" && !enabled {
            enable_cmd = Some("enable");
            changed = true;
        } else if enabled_param == "false" && enabled {
            enable_cmd = Some("disable");
            changed = true;
        }

        if !changed {
            result.action = Action::None;
            result.changed = false;
            return result;
        }

        result.action = Action::Updated;
        result.changed = true;

        if dry_run {
            return result;
        }

        if let Some(cmd) = state_cmd {
            let output = self.runner.run("systemctl", &[cmd, name]);
            if let Err(e) = output {
                result.error = Some(format!("systemctl {} {}: {}", cmd, name, e));
                return result;
            }
            let output = output.unwrap();
            if !output.status.success() {
                let stderr = String::from_utf8_lossy(&output.stderr);
                result.error =
                    Some(format!("systemctl {} {}: {}", cmd, name, stderr));
                return result;
            }
        }

        if let Some(cmd) = enable_cmd {
            let output = self.runner.run("systemctl", &[cmd, name]);
            if let Err(e) = output {
                result.error = Some(format!("systemctl {} {}: {}", cmd, name, e));
                return result;
            }
            let output = output.unwrap();
            if !output.status.success() {
                let stderr = String::from_utf8_lossy(&output.stderr);
                result.error =
                    Some(format!("systemctl {} {}: {}", cmd, name, stderr));
                return result;
            }
        }

        result
    }
}

impl Executor for ServiceExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let name_param = params
            .get("service")
            .or_else(|| params.get("name"))
            .cloned()
            .unwrap_or_default();

        let mut result = ResourceResult::new("service", &name_param);

        if name_param.is_empty() {
            result.error = Some("service executor: name param is required".to_string());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let state = params.get("state").map(|s| s.as_str()).unwrap_or("running");
        let enabled_param = params.get("enabled").map(|s| s.as_str()).unwrap_or("");

        // Support comma-separated service names.
        let names: Vec<&str> = name_param
            .split(',')
            .map(|s| s.trim())
            .filter(|s| !s.is_empty())
            .collect();

        for name in &names {
            let res = self.execute_one(name, state, enabled_param, dry_run);
            if res.error.is_some() {
                result.error = res.error;
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
            if res.changed {
                result.changed = true;
                if result.action == Action::None {
                    result.action = res.action;
                }
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

    #[test]
    fn missing_name_errors() {
        let mock = MockRunner::new(vec![]);
        let exec = ServiceExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(&make_params(&[]), false);
        assert!(result.error.is_some());
        assert!(
            result.error.as_deref().unwrap().contains("name param is required"),
            "unexpected error: {:?}",
            result.error
        );
    }

    #[test]
    fn running_already_active() {
        let mock = MockRunner::new(vec![
            MockRunner::ok_output("ActiveState=active\nUnitFileState=enabled\n"),
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = ServiceExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("name", "nginx"), ("state", "running")]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());
        // Only the status query should have been made.
        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1);
        assert_eq!(calls[0].0, "systemctl");
        assert!(calls[0].1.contains(&"show".to_string()));
    }

    #[test]
    fn running_starts_stopped_service() {
        let mock = MockRunner::new(vec![
            MockRunner::ok_output("ActiveState=inactive\nUnitFileState=enabled\n"),
            MockRunner::ok_output(""), // systemctl start
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = ServiceExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("name", "nginx"), ("state", "running")]),
            false,
        );
        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);
        assert!(result.error.is_none());
        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 2);
        // Second call should be "systemctl start nginx".
        assert_eq!(calls[1].0, "systemctl");
        assert_eq!(calls[1].1, vec!["start", "nginx"]);
    }

    #[test]
    fn stopped_already_stopped() {
        let mock = MockRunner::new(vec![
            MockRunner::ok_output("ActiveState=inactive\nUnitFileState=disabled\n"),
        ]);
        let exec = ServiceExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("name", "nginx"), ("state", "stopped")]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());
    }

    #[test]
    fn stopped_stops_running_service() {
        let mock = MockRunner::new(vec![
            MockRunner::ok_output("ActiveState=active\nUnitFileState=enabled\n"),
            MockRunner::ok_output(""), // systemctl stop
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = ServiceExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("name", "nginx"), ("state", "stopped")]),
            false,
        );
        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);
        assert!(result.error.is_none());
        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 2);
        assert_eq!(calls[1].1, vec!["stop", "nginx"]);
    }

    #[test]
    fn enable_service() {
        let mock = MockRunner::new(vec![
            MockRunner::ok_output("ActiveState=active\nUnitFileState=disabled\n"),
            MockRunner::ok_output(""), // systemctl enable
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = ServiceExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("name", "nginx"),
                ("state", "running"),
                ("enabled", "true"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);
        assert!(result.error.is_none());
        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 2);
        // Only the enable call (no start needed since already active).
        assert_eq!(calls[1].1, vec!["enable", "nginx"]);
    }

    #[test]
    fn dry_run_does_not_call_systemctl_actions() {
        let mock = MockRunner::new(vec![
            MockRunner::ok_output("ActiveState=inactive\nUnitFileState=disabled\n"),
            // No further responses — if start/enable were called, it would panic.
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = ServiceExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("name", "nginx"),
                ("state", "running"),
                ("enabled", "true"),
            ]),
            true,
        );
        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);
        assert!(result.error.is_none());
        // Only the status query — no start or enable.
        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1, "dry_run should only query status, got {:?}", *calls);
    }

    #[test]
    fn unknown_state_errors() {
        let mock = MockRunner::new(vec![
            MockRunner::ok_output("ActiveState=active\nUnitFileState=enabled\n"),
        ]);
        let exec = ServiceExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("name", "nginx"), ("state", "bogus")]),
            false,
        );
        assert!(result.error.is_some());
        assert!(
            result.error.as_deref().unwrap().contains("unknown state"),
            "unexpected error: {:?}",
            result.error
        );
    }
}
