// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Sysctl executor — manages kernel parameters idempotently.

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::Instant;

pub struct SysctlExecutor {
    pub runner: Box<dyn CommandRunner>,
}

impl SysctlExecutor {
    pub fn new() -> Self {
        Self {
            runner: Box::new(DefaultRunner),
        }
    }

    fn current_value(&self, key: &str) -> Option<String> {
        let output = self.runner.run("sysctl", &["-n", key]).ok()?;
        if output.status.success() {
            Some(String::from_utf8_lossy(&output.stdout).trim().to_string())
        } else {
            None
        }
    }

    fn conf_path(key: &str) -> String {
        let safe = key.replace('.', "-");
        format!("/etc/sysctl.d/99-hobnob-{}.conf", safe)
    }
}

impl Executor for SysctlExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let key = params
            .get("key")
            .cloned()
            .unwrap_or_default();

        let mut result = ResourceResult::new("sysctl", &key);

        if key.is_empty() {
            result.error = Some("sysctl executor: key param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");
        let persist = params.get("persist").map(|s| s.as_str()).unwrap_or("true") == "true";

        if state == "absent" {
            let conf = Self::conf_path(&key);
            if std::path::Path::new(&conf).exists() {
                result.action = Action::Deleted;
                result.changed = true;
                if !dry_run {
                    if let Err(e) = std::fs::remove_file(&conf) {
                        result.error = Some(format!("remove {}: {}", conf, e));
                        result.duration_ms = start.elapsed().as_millis() as i32;
                        return result;
                    }
                    let _ = self.runner.run("sysctl", &["--system"]);
                }
            } else {
                result.action = Action::None;
            }
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let value = params.get("value").cloned().unwrap_or_default();
        if value.is_empty() {
            result.error = Some("sysctl executor: value param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let current = self.current_value(&key).unwrap_or_default();
        let conf = Self::conf_path(&key);
        let conf_exists = std::path::Path::new(&conf).exists();
        let conf_content = format!("{} = {}\n", key, value);
        let conf_matches = if conf_exists {
            std::fs::read_to_string(&conf)
                .map(|c| c == conf_content)
                .unwrap_or(false)
        } else {
            false
        };

        let runtime_match = current == value;
        let persist_match = !persist || conf_matches;

        if runtime_match && persist_match {
            result.action = Action::None;
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        result.action = if runtime_match || conf_exists {
            Action::Updated
        } else {
            Action::Created
        };
        result.changed = true;

        if dry_run {
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        // Set runtime value.
        if !runtime_match {
            let setting = format!("{}={}", key, value);
            let output = self.runner.run("sysctl", &["-w", &setting]);
            match output {
                Ok(o) if !o.status.success() => {
                    let stderr = String::from_utf8_lossy(&o.stderr);
                    result.error = Some(format!("sysctl -w {}: {}", setting, stderr));
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }
                Err(e) => {
                    result.error = Some(format!("sysctl -w {}: {}", setting, e));
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }
                _ => {}
            }
        }

        // Persist.
        if persist && !conf_matches {
            if let Err(e) = std::fs::write(&conf, conf_content) {
                result.error = Some(format!("write {}: {}", conf, e));
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

    #[test]
    fn missing_key_errors() {
        let mock = MockRunner::new(vec![]);
        let exec = SysctlExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(&make_params(&[]), false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("key param is required"));
    }

    #[test]
    fn missing_value_errors() {
        let sysctl_read = ok_output("1\n");
        let mock = MockRunner::new(vec![sysctl_read]);
        let exec = SysctlExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("key", "net.ipv4.ip_forward"), ("state", "present")]),
            false,
        );
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("value param is required"));
    }

    #[test]
    fn present_already_matches() {
        // current value matches desired, persist=false so conf check is skipped.
        let sysctl_read = ok_output("1\n");
        let mock = MockRunner::new(vec![sysctl_read]);
        let exec = SysctlExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("key", "net.ipv4.ip_forward"),
                ("value", "1"),
                ("persist", "false"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());
    }

    #[test]
    fn present_value_differs_dry_run() {
        // Current value is "0", desired is "1", persist=false, dry_run=true.
        // Should report changed but not call sysctl -w.
        let sysctl_read = ok_output("0\n");
        let mock = MockRunner::new(vec![sysctl_read]);
        let calls = mock.calls.clone();
        let exec = SysctlExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("key", "net.ipv4.ip_forward"),
                ("value", "1"),
                ("persist", "false"),
            ]),
            true,
        );
        assert!(result.changed);
        // Action should be Created (runtime doesn't match and no conf file exists).
        assert_eq!(result.action, Action::Created);
        assert!(result.error.is_none());

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1, "dry_run should only call sysctl -n, not sysctl -w");
        assert_eq!(calls[0].0, "sysctl");
        assert!(calls[0].1.contains(&"-n".to_string()));
    }

    #[test]
    fn absent_no_conf_file() {
        // state=absent, conf file doesn't exist => Action::None.
        let mock = MockRunner::new(vec![]);
        let exec = SysctlExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("key", "net.ipv4.ip_forward"), ("state", "absent")]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
    }

    #[test]
    fn unknown_state_implicitly_present() {
        // No explicit "state" param defaults to "present", which requires value.
        let sysctl_read = ok_output("0\n");
        let mock = MockRunner::new(vec![sysctl_read]);
        let exec = SysctlExecutor {
            runner: Box::new(mock),
        };
        // Omit state entirely, provide key + value + persist=false.
        let result = exec.execute(
            &make_params(&[
                ("key", "net.ipv4.ip_forward"),
                ("value", "1"),
                ("persist", "false"),
            ]),
            true,
        );
        // Should behave as state=present — value differs so changed=true.
        assert!(result.changed);
        assert!(result.error.is_none());
    }

    #[test]
    fn dry_run_does_not_call_sysctl_w() {
        // Verify that dry_run prevents the sysctl -w mutation call.
        let sysctl_read = ok_output("0\n");
        let mock = MockRunner::new(vec![sysctl_read]);
        let calls = mock.calls.clone();
        let exec = SysctlExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("key", "vm.swappiness"),
                ("value", "10"),
                ("persist", "false"),
            ]),
            true,
        );
        assert!(result.changed);

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1);
        // The only call should be the read (sysctl -n), not a write (sysctl -w).
        assert_eq!(calls[0].1[0], "-n");
    }
}
