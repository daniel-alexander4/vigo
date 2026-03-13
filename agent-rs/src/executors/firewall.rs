// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Firewall executor — manages UFW rules idempotently.

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::Instant;

pub struct FirewallExecutor {
    pub runner: Box<dyn CommandRunner>,
}

impl FirewallExecutor {
    pub fn new() -> Self {
        Self {
            runner: Box::new(DefaultRunner),
        }
    }

    fn rule_exists(&self, port: &str, proto: &str) -> bool {
        let output = match self.runner.run("ufw", &["status", "verbose"]) {
            Ok(o) if o.status.success() => String::from_utf8_lossy(&o.stdout).to_string(),
            _ => return false,
        };

        let needle = if proto.is_empty() || proto == "any" {
            port.to_string()
        } else {
            format!("{}/{}", port, proto)
        };

        output.lines().any(|line| line.contains(&needle))
    }
}

impl Executor for FirewallExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let port = params
            .get("port")
            .or_else(|| params.get("name"))
            .cloned()
            .unwrap_or_default();

        let mut result = ResourceResult::new("firewall", &port);

        if port.is_empty() {
            result.error = Some("firewall executor: port param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");
        let proto = params.get("proto").map(|s| s.as_str()).unwrap_or("any");
        let action = params.get("action").cloned().unwrap_or_default();
        let from = params.get("from").map(|s| s.as_str()).unwrap_or("any");
        let direction = params.get("direction").map(|s| s.as_str()).unwrap_or("in");
        let comment = params.get("comment").cloned().unwrap_or_default();

        let proto_str = if proto == "any" { "" } else { proto };

        match state {
            "present" => {
                if action.is_empty() {
                    result.error = Some("firewall executor: action param is required for state=present".into());
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                if self.rule_exists(&port, proto_str) {
                    result.action = Action::None;
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                result.action = Action::Created;
                result.changed = true;

                if dry_run {
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                let mut args = vec!["--force".to_string(), action.clone(), direction.to_string()];
                if from != "any" {
                    args.extend(["from".into(), from.into()]);
                }
                args.extend(["to".into(), "any".into(), "port".into(), port.clone()]);
                if !proto_str.is_empty() {
                    args.extend(["proto".into(), proto_str.into()]);
                }
                if !comment.is_empty() {
                    args.extend(["comment".into(), comment.clone()]);
                }

                let arg_refs: Vec<&str> = args.iter().map(|s| s.as_str()).collect();
                match self.runner.run("ufw", &arg_refs) {
                    Ok(o) if !o.status.success() => {
                        let stderr = String::from_utf8_lossy(&o.stderr);
                        result.error = Some(format!("ufw: {}", stderr));
                    }
                    Err(e) => result.error = Some(format!("ufw: {}", e)),
                    _ => {}
                }
            }
            "absent" => {
                if !self.rule_exists(&port, proto_str) {
                    result.action = Action::None;
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                result.action = Action::Deleted;
                result.changed = true;

                if dry_run {
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                let mut args = vec!["--force".to_string(), "delete".into(), action.clone(), direction.to_string()];
                args.extend(["to".into(), "any".into(), "port".into(), port.clone()]);
                if !proto_str.is_empty() {
                    args.extend(["proto".into(), proto_str.into()]);
                }

                let arg_refs: Vec<&str> = args.iter().map(|s| s.as_str()).collect();
                match self.runner.run("ufw", &arg_refs) {
                    Ok(o) if !o.status.success() => {
                        let stderr = String::from_utf8_lossy(&o.stderr);
                        result.error = Some(format!("ufw delete: {}", stderr));
                    }
                    Err(e) => result.error = Some(format!("ufw delete: {}", e)),
                    _ => {}
                }
            }
            _ => {
                result.error = Some(format!("firewall executor: unknown state {:?}", state));
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
            self.calls
                .lock()
                .unwrap()
                .push((name.to_string(), args.iter().map(|s| s.to_string()).collect()));
            self.responses.lock().unwrap().remove(0)
        }

        fn run_with_stdin(&self, _: &[u8], name: &str, args: &[&str]) -> std::io::Result<Output> {
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
    fn missing_port_errors() {
        let mock = MockRunner::new(vec![]);
        let exec = FirewallExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(&make_params(&[]), false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("port param is required"));
    }

    #[test]
    fn present_missing_action_errors() {
        let mock = MockRunner::new(vec![]);
        let exec = FirewallExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(&make_params(&[("port", "443"), ("state", "present")]), false);
        assert!(result.error.is_some());
        assert!(
            result
                .error
                .unwrap()
                .contains("action param is required")
        );
    }

    #[test]
    fn present_rule_already_exists() {
        let ufw_status = MockRunner::ok_output(
            "Status: active\n\
             22/tcp                     ALLOW IN    Anywhere\n\
             443/tcp                    ALLOW IN    Anywhere\n",
        );
        let mock = MockRunner::new(vec![ufw_status]);
        let exec = FirewallExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("port", "443"),
                ("proto", "tcp"),
                ("action", "allow"),
                ("state", "present"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());
    }

    #[test]
    fn present_rule_created() {
        let ufw_status = MockRunner::ok_output(
            "Status: active\n\
             22/tcp                     ALLOW IN    Anywhere\n",
        );
        let ufw_allow = MockRunner::ok_output("Rule added");
        let mock = MockRunner::new(vec![ufw_status, ufw_allow]);
        let calls = mock.calls.clone();
        let exec = FirewallExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("port", "443"),
                ("proto", "tcp"),
                ("action", "allow"),
                ("state", "present"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::Created);
        assert!(result.changed);
        assert!(result.error.is_none());

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 2);
        assert_eq!(calls[1].0, "ufw");
        assert!(calls[1].1.contains(&"443".to_string()));
    }

    #[test]
    fn absent_rule_not_found() {
        let ufw_status = MockRunner::ok_output(
            "Status: active\n\
             22/tcp                     ALLOW IN    Anywhere\n",
        );
        let mock = MockRunner::new(vec![ufw_status]);
        let exec = FirewallExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("port", "443"), ("proto", "tcp"), ("state", "absent")]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
    }

    #[test]
    fn absent_rule_deleted() {
        let ufw_status = MockRunner::ok_output(
            "Status: active\n\
             22/tcp                     ALLOW IN    Anywhere\n\
             443/tcp                    ALLOW IN    Anywhere\n",
        );
        let ufw_delete = MockRunner::ok_output("Rule deleted");
        let mock = MockRunner::new(vec![ufw_status, ufw_delete]);
        let calls = mock.calls.clone();
        let exec = FirewallExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("port", "443"),
                ("proto", "tcp"),
                ("action", "allow"),
                ("state", "absent"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::Deleted);
        assert!(result.changed);
        assert!(result.error.is_none());

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 2);
        assert_eq!(calls[1].0, "ufw");
        assert!(calls[1].1.contains(&"delete".to_string()));
    }

    #[test]
    fn dry_run_does_not_call_ufw_mutate() {
        let ufw_status = MockRunner::ok_output(
            "Status: active\n\
             22/tcp                     ALLOW IN    Anywhere\n",
        );
        let mock = MockRunner::new(vec![ufw_status]);
        let calls = mock.calls.clone();
        let exec = FirewallExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("port", "443"),
                ("proto", "tcp"),
                ("action", "allow"),
                ("state", "present"),
            ]),
            true,
        );
        assert_eq!(result.action, Action::Created);
        assert!(result.changed);

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1, "dry_run should only call ufw status, not ufw allow");
    }

    #[test]
    fn unknown_state_errors() {
        let mock = MockRunner::new(vec![]);
        let exec = FirewallExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("port", "443"),
                ("action", "allow"),
                ("state", "bogus"),
            ]),
            false,
        );
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("unknown state"));
    }
}
