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
