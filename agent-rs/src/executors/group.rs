// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Group executor — manages system groups via groupadd/groupmod/groupdel.

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use std::collections::HashMap;
use std::io::{BufRead, BufReader};
use std::time::Instant;

pub struct GroupExecutor {
    pub runner: Box<dyn CommandRunner>,
    pub group_file: String,
}

impl GroupExecutor {
    pub fn new() -> Self {
        Self {
            runner: Box::new(DefaultRunner),
            group_file: "/etc/group".into(),
        }
    }

    fn group_exists(&self, name: &str) -> (bool, String) {
        let f = match std::fs::File::open(&self.group_file) {
            Ok(f) => f,
            Err(_) => return (false, String::new()),
        };

        for line in BufReader::new(f).lines().flatten() {
            let parts: Vec<&str> = line.splitn(4, ':').collect();
            if parts.len() >= 3 && parts[0] == name {
                return (true, parts[2].to_string());
            }
        }
        (false, String::new())
    }
}

impl Executor for GroupExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let name = params
            .get("group")
            .or_else(|| params.get("name"))
            .cloned()
            .unwrap_or_default();

        let mut result = ResourceResult::new("group", &name);

        if name.is_empty() {
            result.error = Some("group executor: group or name param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");
        let gid = params.get("gid").cloned().unwrap_or_default();
        let (exists, current_gid) = self.group_exists(&name);

        match state {
            "present" => {
                if exists {
                    if !gid.is_empty() && gid != current_gid {
                        result.action = Action::Updated;
                        result.changed = true;
                        if !dry_run {
                            match self.runner.run("groupmod", &["-g", &gid, &name]) {
                                Ok(o) if !o.status.success() => {
                                    let stderr = String::from_utf8_lossy(&o.stderr);
                                    result.error = Some(format!("groupmod: {}", stderr));
                                }
                                Err(e) => result.error = Some(format!("groupmod: {}", e)),
                                _ => {}
                            }
                        }
                    } else {
                        result.action = Action::None;
                    }
                } else {
                    result.action = Action::Created;
                    result.changed = true;
                    if !dry_run {
                        let args: Vec<&str> = if gid.is_empty() {
                            vec![&name]
                        } else {
                            vec!["-g", &gid, &name]
                        };
                        match self.runner.run("groupadd", &args) {
                            Ok(o) if !o.status.success() => {
                                let stderr = String::from_utf8_lossy(&o.stderr);
                                result.error = Some(format!("groupadd: {}", stderr));
                            }
                            Err(e) => result.error = Some(format!("groupadd: {}", e)),
                            _ => {}
                        }
                    }
                }
            }
            "absent" => {
                if !exists {
                    result.action = Action::None;
                } else {
                    result.action = Action::Deleted;
                    result.changed = true;
                    if !dry_run {
                        match self.runner.run("groupdel", &[&name]) {
                            Ok(o) if !o.status.success() => {
                                let stderr = String::from_utf8_lossy(&o.stderr);
                                result.error = Some(format!("groupdel: {}", stderr));
                            }
                            Err(e) => result.error = Some(format!("groupdel: {}", e)),
                            _ => {}
                        }
                    }
                }
            }
            _ => {
                result.error = Some(format!(
                    "group executor: unknown state {:?} (want present or absent)",
                    state
                ));
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

        #[allow(dead_code)]
        fn fail_output(stderr: &str) -> Output {
            Output {
                status: std::process::ExitStatus::from_raw(256),
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
        let dir = tempfile::TempDir::new().unwrap();
        let group_file = dir.path().join("group");
        std::fs::write(&group_file, "root:x:0:\n").unwrap();

        let mock = MockRunner::empty();
        let exec = GroupExecutor {
            runner: Box::new(mock),
            group_file: group_file.to_str().unwrap().to_string(),
        };

        let result = exec.execute(&make_params(&[]), false);
        assert!(result.error.is_some());
        assert!(
            result.error.as_deref().unwrap().contains("param is required"),
            "unexpected error: {:?}",
            result.error
        );
    }

    #[test]
    fn present_creates_group() {
        let dir = tempfile::TempDir::new().unwrap();
        let group_file = dir.path().join("group");
        std::fs::write(&group_file, "root:x:0:\n").unwrap();

        // groupadd succeeds
        let mock = MockRunner::new(vec![MockRunner::ok_output("")]);
        let calls = Arc::clone(&mock.calls);
        let exec = GroupExecutor {
            runner: Box::new(mock),
            group_file: group_file.to_str().unwrap().to_string(),
        };

        let result = exec.execute(
            &make_params(&[("name", "deploy"), ("state", "present")]),
            false,
        );

        assert_eq!(result.action, Action::Created);
        assert!(result.changed);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1);
        assert_eq!(calls[0].0, "groupadd");
        assert_eq!(calls[0].1, vec!["deploy"]);
    }

    #[test]
    fn present_group_exists_matches() {
        let dir = tempfile::TempDir::new().unwrap();
        let group_file = dir.path().join("group");
        std::fs::write(&group_file, "root:x:0:\ndeploy:x:1001:\n").unwrap();

        let mock = MockRunner::empty();
        let calls = Arc::clone(&mock.calls);
        let exec = GroupExecutor {
            runner: Box::new(mock),
            group_file: group_file.to_str().unwrap().to_string(),
        };

        let result = exec.execute(
            &make_params(&[("name", "deploy"), ("gid", "1001"), ("state", "present")]),
            false,
        );

        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());
        assert!(calls.lock().unwrap().is_empty());
    }

    #[test]
    fn present_updates_gid() {
        let dir = tempfile::TempDir::new().unwrap();
        let group_file = dir.path().join("group");
        std::fs::write(&group_file, "root:x:0:\ndeploy:x:1001:\n").unwrap();

        // groupmod succeeds
        let mock = MockRunner::new(vec![MockRunner::ok_output("")]);
        let calls = Arc::clone(&mock.calls);
        let exec = GroupExecutor {
            runner: Box::new(mock),
            group_file: group_file.to_str().unwrap().to_string(),
        };

        let result = exec.execute(
            &make_params(&[("name", "deploy"), ("gid", "2000"), ("state", "present")]),
            false,
        );

        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1);
        assert_eq!(calls[0].0, "groupmod");
        assert_eq!(calls[0].1, vec!["-g", "2000", "deploy"]);
    }

    #[test]
    fn absent_group_not_found() {
        let dir = tempfile::TempDir::new().unwrap();
        let group_file = dir.path().join("group");
        std::fs::write(&group_file, "root:x:0:\n").unwrap();

        let mock = MockRunner::empty();
        let calls = Arc::clone(&mock.calls);
        let exec = GroupExecutor {
            runner: Box::new(mock),
            group_file: group_file.to_str().unwrap().to_string(),
        };

        let result = exec.execute(
            &make_params(&[("name", "ghost"), ("state", "absent")]),
            false,
        );

        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());
        assert!(calls.lock().unwrap().is_empty());
    }

    #[test]
    fn absent_deletes_group() {
        let dir = tempfile::TempDir::new().unwrap();
        let group_file = dir.path().join("group");
        std::fs::write(&group_file, "root:x:0:\ndeploy:x:1001:\n").unwrap();

        // groupdel succeeds
        let mock = MockRunner::new(vec![MockRunner::ok_output("")]);
        let calls = Arc::clone(&mock.calls);
        let exec = GroupExecutor {
            runner: Box::new(mock),
            group_file: group_file.to_str().unwrap().to_string(),
        };

        let result = exec.execute(
            &make_params(&[("name", "deploy"), ("state", "absent")]),
            false,
        );

        assert_eq!(result.action, Action::Deleted);
        assert!(result.changed);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1);
        assert_eq!(calls[0].0, "groupdel");
        assert_eq!(calls[0].1, vec!["deploy"]);
    }

    #[test]
    fn dry_run_does_not_call_groupadd() {
        let dir = tempfile::TempDir::new().unwrap();
        let group_file = dir.path().join("group");
        std::fs::write(&group_file, "root:x:0:\n").unwrap();

        let mock = MockRunner::empty();
        let calls = Arc::clone(&mock.calls);
        let exec = GroupExecutor {
            runner: Box::new(mock),
            group_file: group_file.to_str().unwrap().to_string(),
        };

        let result = exec.execute(
            &make_params(&[("name", "deploy"), ("state", "present")]),
            true,
        );

        assert_eq!(result.action, Action::Created);
        assert!(result.changed);
        assert!(result.error.is_none());
        assert!(
            calls.lock().unwrap().is_empty(),
            "dry_run should not invoke any commands"
        );
    }
}
