// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0

//! Reboot executor — triggers system reboot.

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::Instant;

pub struct RebootExecutor {
    pub runner: Box<dyn CommandRunner>,
}

impl RebootExecutor {
    pub fn new() -> Self {
        Self {
            runner: Box::new(DefaultRunner),
        }
    }
}

impl Executor for RebootExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let mut result = ResourceResult::new("reboot", "system");

        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");
        let delay = params.get("delay_secs").map(|s| s.as_str()).unwrap_or("0");
        let message = params
            .get("message")
            .map(|s| s.as_str())
            .unwrap_or("System reboot triggered by Hobnob");
        let reboot_required_file = params.get("reboot_required_file").cloned().unwrap_or_default();

        match state {
            "present" => {
                // Check if reboot_required_file exists.
                if !reboot_required_file.is_empty()
                    && !std::path::Path::new(&reboot_required_file).exists()
                {
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

                let delay_arg = format!("+{}", delay);
                match self.runner.run("shutdown", &["-r", &delay_arg, message]) {
                    Ok(o) if !o.status.success() => {
                        let stderr = String::from_utf8_lossy(&o.stderr);
                        result.error = Some(format!("shutdown -r: {}", stderr));
                    }
                    Err(e) => result.error = Some(format!("shutdown -r: {}", e)),
                    _ => {}
                }
            }
            "absent" => {
                // Cancel pending reboot.
                result.action = Action::Updated;
                result.changed = true;

                if !dry_run {
                    match self.runner.run("shutdown", &["-c"]) {
                        Ok(o) if !o.status.success() => {
                            // May fail if no reboot is pending — not an error.
                            result.action = Action::None;
                            result.changed = false;
                        }
                        Err(_) => {
                            result.action = Action::None;
                            result.changed = false;
                        }
                        _ => {}
                    }
                }
            }
            _ => {
                result.error = Some(format!("reboot executor: unknown state {:?}", state));
            }
        }

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}

#[cfg(test)]
#[cfg(unix)]
mod tests {
    use super::*;
    use crate::executors::test_support::mock::*;

    #[test]
    fn reboot_required_file_missing_skips() {
        // When reboot_required_file is set but doesn't exist, no reboot happens.
        let mock = MockRunner::new(vec![]);
        let calls = mock.calls.clone();
        let exec = RebootExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("state", "present"),
                ("reboot_required_file", "/tmp/nonexistent_hobnob_reboot_test_file"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 0, "no commands when reboot_required_file is absent");
    }

    #[test]
    fn reboot_dry_run() {
        // state=present with no reboot_required_file -> triggers reboot, but dry_run skips the command.
        let mock = MockRunner::new(vec![]);
        let calls = mock.calls.clone();
        let exec = RebootExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(&make_params(&[("state", "present")]), true);
        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);
        assert!(result.error.is_none());

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 0, "dry_run should not call shutdown");
    }

    #[test]
    fn reboot_calls_shutdown() {
        // state=present with no file guard -> calls shutdown -r
        let shutdown = ok_output("");
        let mock = MockRunner::new(vec![shutdown]);
        let calls = mock.calls.clone();
        let exec = RebootExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("state", "present"), ("delay_secs", "5"), ("message", "maintenance")]),
            false,
        );
        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);
        assert!(result.error.is_none());

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1);
        assert_eq!(calls[0].0, "shutdown");
        assert_eq!(calls[0].1, vec!["-r", "+5", "maintenance"]);
    }

    #[test]
    fn reboot_shutdown_failure() {
        // shutdown fails -> error is reported
        let shutdown = fail_output("not permitted");
        let mock = MockRunner::new(vec![shutdown]);
        let exec = RebootExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(&make_params(&[("state", "present")]), false);
        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("shutdown -r"));
    }

    #[test]
    fn cancel_reboot() {
        // state=absent -> calls shutdown -c to cancel
        let shutdown_cancel = ok_output("");
        let mock = MockRunner::new(vec![shutdown_cancel]);
        let calls = mock.calls.clone();
        let exec = RebootExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(&make_params(&[("state", "absent")]), false);
        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);
        assert!(result.error.is_none());

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1);
        assert_eq!(calls[0].0, "shutdown");
        assert_eq!(calls[0].1, vec!["-c"]);
    }

    #[test]
    fn unknown_state_errors() {
        let mock = MockRunner::new(vec![]);
        let exec = RebootExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(&make_params(&[("state", "bogus")]), false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("unknown state"));
    }
}
