// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0

//! Group executor for FreeBSD — manages groups via `pw groupadd/groupmod/groupdel`.

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::Instant;

pub struct GroupFreebsdExecutor {
    pub runner: Box<dyn CommandRunner>,
}

impl GroupFreebsdExecutor {
    pub fn new() -> Self {
        Self { runner: Box::new(DefaultRunner) }
    }

    fn group_exists(&self, name: &str) -> bool {
        self.runner.run("pw", &["groupshow", name])
            .map(|o| o.status.success())
            .unwrap_or(false)
    }
}

impl Executor for GroupFreebsdExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let name = params.get("group").cloned().unwrap_or_default();
        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");
        let mut result = ResourceResult::new("group", &name);

        if name.is_empty() {
            result.error = Some("group executor: group param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let exists = self.group_exists(&name);

        match state {
            "present" => {
                if exists {
                    result.action = Action::None;
                } else {
                    result.action = Action::Created;
                    result.changed = true;
                    if !dry_run {
                        let mut args = vec!["groupadd", &name];
                        let gid_str;
                        if let Some(gid) = params.get("gid") {
                            gid_str = gid.clone();
                            args.extend_from_slice(&["-g", &gid_str]);
                        }
                        match self.runner.run("pw", &args) {
                            Ok(o) if !o.status.success() => {
                                result.error = Some(format!("pw groupadd: {}", String::from_utf8_lossy(&o.stderr)));
                            }
                            Err(e) => result.error = Some(format!("pw groupadd: {}", e)),
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
                        match self.runner.run("pw", &["groupdel", &name]) {
                            Ok(o) if !o.status.success() => {
                                result.error = Some(format!("pw groupdel: {}", String::from_utf8_lossy(&o.stderr)));
                            }
                            Err(e) => result.error = Some(format!("pw groupdel: {}", e)),
                            _ => {}
                        }
                    }
                }
            }
            other => result.error = Some(format!("unknown group state: {}", other)),
        }

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn missing_name_errors() {
        let exec = GroupFreebsdExecutor::new();
        let result = exec.execute(&HashMap::new(), false);
        assert!(result.error.is_some());
    }
}
