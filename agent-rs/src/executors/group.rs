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

    /// Parse the 4th field of /etc/group for the given group name to get current members.
    fn group_members(&self, name: &str) -> Vec<String> {
        let f = match std::fs::File::open(&self.group_file) {
            Ok(f) => f,
            Err(_) => return Vec::new(),
        };

        for line in BufReader::new(f).lines().flatten() {
            let parts: Vec<&str> = line.splitn(4, ':').collect();
            if parts.len() >= 4 && parts[0] == name {
                let members_field = parts[3].trim();
                if members_field.is_empty() {
                    return Vec::new();
                }
                return members_field
                    .split(',')
                    .map(|s| s.trim().to_string())
                    .filter(|s| !s.is_empty())
                    .collect();
            }
        }
        Vec::new()
    }
}

impl Executor for GroupExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let name = params
            .get("group")
            .cloned()
            .unwrap_or_default();

        let mut result = ResourceResult::new("group", &name);

        if name.is_empty() {
            result.error = Some("group executor: group param is required".into());
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
                        let system_flag =
                            params.get("system").map(|s| s == "true").unwrap_or(false);
                        let mut args: Vec<&str> = Vec::new();
                        if system_flag {
                            args.push("--system");
                        }
                        if !gid.is_empty() {
                            args.push("-g");
                            args.push(&gid);
                        }
                        args.push(&name);
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

                // Members management — only when members param is explicitly set.
                if let Some(members_param) = params.get("members") {
                    let desired: std::collections::HashSet<String> = members_param
                        .split(',')
                        .map(|s| s.trim().to_string())
                        .filter(|s| !s.is_empty())
                        .collect();
                    let current: std::collections::HashSet<String> =
                        self.group_members(&name).into_iter().collect();

                    let to_add: Vec<&String> = desired.difference(&current).collect();
                    let to_remove: Vec<&String> = current.difference(&desired).collect();

                    if !to_add.is_empty() || !to_remove.is_empty() {
                        if !result.changed {
                            result.action = Action::Updated;
                            result.changed = true;
                        }

                        if !dry_run {
                            for user in &to_add {
                                match self.runner.run("usermod", &["-aG", &name, user]) {
                                    Ok(o) if !o.status.success() => {
                                        let stderr = String::from_utf8_lossy(&o.stderr);
                                        result.error = Some(format!(
                                            "usermod -aG {} {}: {}",
                                            name, user, stderr
                                        ));
                                        result.duration_ms =
                                            start.elapsed().as_millis() as i32;
                                        return result;
                                    }
                                    Err(e) => {
                                        result.error = Some(format!(
                                            "usermod -aG {} {}: {}",
                                            name, user, e
                                        ));
                                        result.duration_ms =
                                            start.elapsed().as_millis() as i32;
                                        return result;
                                    }
                                    _ => {}
                                }
                            }
                            for user in &to_remove {
                                match self.runner.run("gpasswd", &["-d", user, &name]) {
                                    Ok(o) if !o.status.success() => {
                                        let stderr = String::from_utf8_lossy(&o.stderr);
                                        result.error = Some(format!(
                                            "gpasswd -d {} {}: {}",
                                            user, name, stderr
                                        ));
                                        result.duration_ms =
                                            start.elapsed().as_millis() as i32;
                                        return result;
                                    }
                                    Err(e) => {
                                        result.error = Some(format!(
                                            "gpasswd -d {} {}: {}",
                                            user, name, e
                                        ));
                                        result.duration_ms =
                                            start.elapsed().as_millis() as i32;
                                        return result;
                                    }
                                    _ => {}
                                }
                            }
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
    use crate::executors::test_support::mock::*;
    use std::sync::Arc;

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
        let mock = MockRunner::new(vec![ok_output("")]);
        let calls = Arc::clone(&mock.calls);
        let exec = GroupExecutor {
            runner: Box::new(mock),
            group_file: group_file.to_str().unwrap().to_string(),
        };

        let result = exec.execute(
            &make_params(&[("group", "deploy"), ("state", "present")]),
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
            &make_params(&[("group", "deploy"), ("gid", "1001"), ("state", "present")]),
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
        let mock = MockRunner::new(vec![ok_output("")]);
        let calls = Arc::clone(&mock.calls);
        let exec = GroupExecutor {
            runner: Box::new(mock),
            group_file: group_file.to_str().unwrap().to_string(),
        };

        let result = exec.execute(
            &make_params(&[("group", "deploy"), ("gid", "2000"), ("state", "present")]),
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
            &make_params(&[("group", "ghost"), ("state", "absent")]),
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
        let mock = MockRunner::new(vec![ok_output("")]);
        let calls = Arc::clone(&mock.calls);
        let exec = GroupExecutor {
            runner: Box::new(mock),
            group_file: group_file.to_str().unwrap().to_string(),
        };

        let result = exec.execute(
            &make_params(&[("group", "deploy"), ("state", "absent")]),
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
    fn system_flag_on_groupadd() {
        let dir = tempfile::TempDir::new().unwrap();
        let group_file = dir.path().join("group");
        std::fs::write(&group_file, "root:x:0:\n").unwrap();

        let mock = MockRunner::new(vec![ok_output("")]);
        let calls = Arc::clone(&mock.calls);
        let exec = GroupExecutor {
            runner: Box::new(mock),
            group_file: group_file.to_str().unwrap().to_string(),
        };

        let result = exec.execute(
            &make_params(&[("group", "sysgroup"), ("state", "present"), ("system", "true")]),
            false,
        );

        assert_eq!(result.action, Action::Created);
        assert!(result.changed);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1);
        assert_eq!(calls[0].0, "groupadd");
        assert!(
            calls[0].1.contains(&"--system".to_string()),
            "expected --system in args: {:?}",
            calls[0].1
        );
        assert!(
            calls[0].1.contains(&"sysgroup".to_string()),
            "expected group name in args: {:?}",
            calls[0].1
        );
    }

    #[test]
    fn members_adds_missing() {
        let dir = tempfile::TempDir::new().unwrap();
        let group_file = dir.path().join("group");
        std::fs::write(&group_file, "root:x:0:\ndeploy:x:1001:alice\n").unwrap();

        // usermod -aG for bob
        let mock = MockRunner::new(vec![ok_output("")]);
        let calls = Arc::clone(&mock.calls);
        let exec = GroupExecutor {
            runner: Box::new(mock),
            group_file: group_file.to_str().unwrap().to_string(),
        };

        let result = exec.execute(
            &make_params(&[
                ("group", "deploy"),
                ("state", "present"),
                ("members", "alice,bob"),
            ]),
            false,
        );

        assert!(result.changed);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1);
        assert_eq!(calls[0].0, "usermod");
        assert_eq!(calls[0].1, vec!["-aG", "deploy", "bob"]);
    }

    #[test]
    fn members_removes_extra() {
        let dir = tempfile::TempDir::new().unwrap();
        let group_file = dir.path().join("group");
        std::fs::write(&group_file, "root:x:0:\ndeploy:x:1001:alice,bob\n").unwrap();

        // gpasswd -d for bob
        let mock = MockRunner::new(vec![ok_output("")]);
        let calls = Arc::clone(&mock.calls);
        let exec = GroupExecutor {
            runner: Box::new(mock),
            group_file: group_file.to_str().unwrap().to_string(),
        };

        let result = exec.execute(
            &make_params(&[
                ("group", "deploy"),
                ("state", "present"),
                ("members", "alice"),
            ]),
            false,
        );

        assert!(result.changed);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1);
        assert_eq!(calls[0].0, "gpasswd");
        assert_eq!(calls[0].1, vec!["-d", "bob", "deploy"]);
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
            &make_params(&[("group", "deploy"), ("state", "present")]),
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
