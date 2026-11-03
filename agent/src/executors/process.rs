// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Process executor — checks/manages OS processes via pgrep/kill.

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::Instant;

pub struct ProcessExecutor {
    pub runner: Box<dyn CommandRunner>,
}

impl ProcessExecutor {
    pub fn new() -> Self {
        Self {
            runner: Box::new(DefaultRunner),
        }
    }

    fn process_count(&self, name: &str, match_mode: &str) -> i32 {
        let flag = if match_mode == "pattern" { "-f" } else { "-x" };
        match self.runner.run("pgrep", &[flag, name]) {
            Ok(o) if o.status.success() => {
                let stdout = String::from_utf8_lossy(&o.stdout);
                stdout.lines().filter(|l| !l.trim().is_empty()).count() as i32
            }
            _ => 0,
        }
    }

    fn get_pids(&self, name: &str, match_mode: &str) -> Vec<String> {
        let flag = if match_mode == "pattern" { "-f" } else { "-x" };
        match self.runner.run("pgrep", &[flag, name]) {
            Ok(o) if o.status.success() => {
                let stdout = String::from_utf8_lossy(&o.stdout);
                stdout
                    .lines()
                    .map(|l| l.trim().to_string())
                    .filter(|l| !l.is_empty())
                    .collect()
            }
            _ => Vec::new(),
        }
    }
}

impl Executor for ProcessExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let name = params
            .get("process")
            .cloned()
            .unwrap_or_default();

        let mut result = ResourceResult::new("process", &name);

        if name.is_empty() {
            result.error = Some("process executor: process param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let state = params.get("state").map(|s| s.as_str()).unwrap_or("running");
        let signal = params.get("signal").map(|s| s.as_str()).unwrap_or("TERM");
        let match_mode = params.get("match_mode").map(|s| s.as_str()).unwrap_or("exact");

        // Parse count constraints.
        let count_str = params.get("count").cloned().unwrap_or_default();
        let min_str = params.get("min").cloned().unwrap_or_default();
        let max_str = params.get("max").cloned().unwrap_or_default();

        if !count_str.is_empty() && (!min_str.is_empty() || !max_str.is_empty()) {
            result.error = Some("process executor: count is mutually exclusive with min/max".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let exact_count = count_str.parse::<i32>().ok();
        let min_count = min_str.parse::<i32>().ok();
        let max_count = max_str.parse::<i32>().ok();

        if let (Some(mn), Some(mx)) = (min_count, max_count) {
            if mn > mx {
                result.error = Some(format!("process executor: min ({}) > max ({})", mn, mx));
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
        }

        let restart_method = params.get("restart_method").cloned().unwrap_or_default();
        let restart_unit = params.get("restart_unit").cloned().unwrap_or_default();
        let kill_timeout = params.get("kill_timeout").map(|s| s == "true").unwrap_or(false);

        let count = self.process_count(&name, match_mode);

        match state {
            "running" => {
                let has_constraints = exact_count.is_some() || min_count.is_some() || max_count.is_some();

                if !has_constraints {
                    if count > 0 {
                        result.action = Action::None;
                    } else if !restart_method.is_empty() {
                        result.action = Action::Updated;
                        result.changed = true;
                        if !dry_run {
                            if restart_method != "systemctl" {
                                result.error = Some(format!(
                                    "process executor: unknown restart method {:?}",
                                    restart_method
                                ));
                            } else {
                                let unit = if restart_unit.is_empty() {
                                    &name
                                } else {
                                    &restart_unit
                                };
                                match self.runner.run("systemctl", &["restart", unit]) {
                                    Ok(o) if !o.status.success() => {
                                        let stderr = String::from_utf8_lossy(&o.stderr);
                                        result.error = Some(format!("systemctl restart {}: {}", unit, stderr));
                                    }
                                    Err(e) => {
                                        result.error = Some(format!("systemctl restart {}: {}", unit, e));
                                    }
                                    _ => {}
                                }
                            }
                        }
                    } else {
                        result.error = Some(format!("process {:?} is not running", name));
                    }
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                // Count constraints.
                let too_few = if let Some(ec) = exact_count {
                    count < ec
                } else if let Some(mn) = min_count {
                    count < mn
                } else {
                    false
                };

                let too_many = if let Some(ec) = exact_count {
                    count > ec
                } else if let Some(mx) = max_count {
                    count > mx
                } else {
                    false
                };

                if !too_few && !too_many {
                    result.action = Action::None;
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                if too_few {
                    if !restart_method.is_empty() {
                        result.action = Action::Updated;
                        result.changed = true;
                        if !dry_run {
                            let unit = if restart_unit.is_empty() { &name } else { &restart_unit };
                            match self.runner.run("systemctl", &["restart", unit]) {
                                Ok(o) if !o.status.success() => {
                                    let stderr = String::from_utf8_lossy(&o.stderr);
                                    result.error = Some(format!("systemctl restart {}: {}", unit, stderr));
                                }
                                Err(e) => result.error = Some(format!("systemctl restart {}: {}", unit, e)),
                                _ => {}
                            }
                        }
                    } else if let Some(ec) = exact_count {
                        result.error = Some(format!("process {:?}: expected {}, found {}", name, ec, count));
                    } else if let Some(mn) = min_count {
                        result.error = Some(format!("process {:?}: expected min {}, found {}", name, mn, count));
                    }
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                if too_many && kill_timeout {
                    let target = max_count.or(exact_count).unwrap_or(0);
                    let excess = (count - target) as usize;

                    result.action = Action::Deleted;
                    result.changed = true;

                    if !dry_run {
                        let pids = self.get_pids(&name, match_mode);
                        let sig_arg = format!("-{}", signal);
                        for pid in pids.iter().take(excess) {
                            match self.runner.run("kill", &[&sig_arg, pid]) {
                                Ok(o) if !o.status.success() => {
                                    let stderr = String::from_utf8_lossy(&o.stderr);
                                    result.error = Some(format!("kill {} {}: {}", sig_arg, pid, stderr));
                                    result.duration_ms = start.elapsed().as_millis() as i32;
                                    return result;
                                }
                                Err(e) => {
                                    result.error = Some(format!("kill {} {}: {}", sig_arg, pid, e));
                                    result.duration_ms = start.elapsed().as_millis() as i32;
                                    return result;
                                }
                                _ => {}
                            }
                        }
                    }
                } else if let Some(ec) = exact_count {
                    result.error = Some(format!("process {:?}: expected {}, found {}", name, ec, count));
                } else if let Some(mx) = max_count {
                    result.error = Some(format!("process {:?}: expected max {}, found {}", name, mx, count));
                }
            }
            "stopped" => {
                if count == 0 {
                    result.action = Action::None;
                } else {
                    result.action = Action::Deleted;
                    result.changed = true;
                    if !dry_run {
                        let kill_flag = if match_mode == "pattern" { "-f" } else { "-x" };
                        let sig_arg = format!("-{}", signal);
                        match self.runner.run("pkill", &[&sig_arg, kill_flag, &name]) {
                            Ok(o) if !o.status.success() => {
                                let stderr = String::from_utf8_lossy(&o.stderr);
                                result.error = Some(format!("pkill: {}", stderr));
                            }
                            Err(e) => result.error = Some(format!("pkill: {}", e)),
                            _ => {}
                        }
                    }
                }
            }
            _ => {
                result.error = Some(format!("process executor: unknown state {:?}", state));
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
    fn missing_name_errors() {
        let mock = MockRunner::new(vec![]);
        let exec = ProcessExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(&make_params(&[]), false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("process param is required"));
    }

    #[test]
    fn running_process_exists() {
        // pgrep finds the process (exit 0, one PID)
        let pgrep = ok_output("1234\n");
        let mock = MockRunner::new(vec![pgrep]);
        let exec = ProcessExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("process", "nginx"), ("state", "running")]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());
    }

    #[test]
    fn running_process_missing_no_restart() {
        // pgrep finds no match (exit code 1)
        let pgrep = fail_output("");
        let mock = MockRunner::new(vec![pgrep]);
        let exec = ProcessExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("process", "nginx"), ("state", "running")]),
            false,
        );
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("not running"));
    }

    #[test]
    fn stopped_already_stopped() {
        // pgrep finds no match
        let pgrep = fail_output("");
        let mock = MockRunner::new(vec![pgrep]);
        let exec = ProcessExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("process", "nginx"), ("state", "stopped")]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());
    }

    #[test]
    fn stopped_kills_process() {
        // pgrep finds match, then pkill succeeds
        let pgrep = ok_output("1234\n");
        let pkill = ok_output("");
        let mock = MockRunner::new(vec![pgrep, pkill]);
        let calls = mock.calls.clone();
        let exec = ProcessExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("process", "nginx"), ("state", "stopped")]),
            false,
        );
        assert_eq!(result.action, Action::Deleted);
        assert!(result.changed);
        assert!(result.error.is_none());

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 2);
        assert_eq!(calls[1].0, "pkill");
        assert!(calls[1].1.contains(&"-TERM".to_string()));
        assert!(calls[1].1.contains(&"-x".to_string()));
    }

    #[test]
    fn count_constraint_satisfied() {
        // pgrep returns 3 PIDs, count=3 -> no change
        let pgrep = ok_output("100\n200\n300\n");
        let mock = MockRunner::new(vec![pgrep]);
        let exec = ProcessExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("process", "worker"), ("state", "running"), ("count", "3")]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());
    }

    #[test]
    fn dry_run_does_not_kill() {
        // Process running, state=stopped, dry_run -> changed but only pgrep called
        let pgrep = ok_output("1234\n");
        let mock = MockRunner::new(vec![pgrep]);
        let calls = mock.calls.clone();
        let exec = ProcessExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("process", "nginx"), ("state", "stopped")]),
            true,
        );
        assert_eq!(result.action, Action::Deleted);
        assert!(result.changed);
        assert!(result.error.is_none());

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1, "dry_run should only call pgrep, not pkill");
        assert_eq!(calls[0].0, "pgrep");
    }
}
