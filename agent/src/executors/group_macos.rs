// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Group executor for macOS — manages groups via `dscl`.

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::Instant;

pub struct GroupMacosExecutor {
    pub runner: Box<dyn CommandRunner>,
}

impl GroupMacosExecutor {
    pub fn new() -> Self {
        Self { runner: Box::new(DefaultRunner) }
    }

    fn group_exists(&self, name: &str) -> bool {
        self.runner.run("dscl", &[".", "-read", &format!("/Groups/{}", name)])
            .map(|o| o.status.success())
            .unwrap_or(false)
    }

    fn next_gid(&self) -> String {
        let output = self.runner.run("dscl", &[".", "-list", "/Groups", "PrimaryGroupID"]);
        let max = output.ok()
            .map(|o| {
                let stdout = String::from_utf8_lossy(&o.stdout);
                stdout.lines()
                    .filter_map(|line| line.split_whitespace().last()?.parse::<u32>().ok())
                    .filter(|&gid| gid >= 500)
                    .max()
                    .unwrap_or(500)
            })
            .unwrap_or(500);
        (max + 1).to_string()
    }
}

impl Executor for GroupMacosExecutor {
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
                        let gid = params.get("gid").cloned().unwrap_or_else(|| self.next_gid());
                        let group_path = format!("/Groups/{}", name);
                        let cmds: Vec<&[&str]> = vec![
                            &[".", "-create", &group_path],
                            &[".", "-create", &group_path, "PrimaryGroupID", &gid],
                        ];
                        for args in cmds {
                            match self.runner.run("dscl", args) {
                                Ok(o) if o.status.success() => {}
                                Ok(o) => {
                                    let stderr = String::from_utf8_lossy(&o.stderr);
                                    result.error = Some(format!("dscl create group: {}", stderr));
                                    break;
                                }
                                Err(e) => {
                                    result.error = Some(format!("dscl create group: {}", e));
                                    break;
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
                        match self.runner.run("dscl", &[".", "-delete", &format!("/Groups/{}", name)]) {
                            Ok(o) if o.status.success() => {}
                            Ok(o) => {
                                let stderr = String::from_utf8_lossy(&o.stderr);
                                result.error = Some(format!("dscl delete group: {}", stderr));
                            }
                            Err(e) => result.error = Some(format!("dscl delete group: {}", e)),
                        }
                    }
                }
            }
            other => {
                result.error = Some(format!("unknown group state: {}", other));
            }
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
        let exec = GroupMacosExecutor::new();
        let result = exec.execute(&HashMap::new(), false);
        assert!(result.error.is_some());
    }
}
