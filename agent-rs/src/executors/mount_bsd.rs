// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Mount executor for BSD and macOS — manages mount points via `mount`/`umount`.

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::Instant;

pub struct MountBsdExecutor {
    pub runner: Box<dyn CommandRunner>,
}

impl MountBsdExecutor {
    pub fn new() -> Self {
        Self { runner: Box::new(DefaultRunner) }
    }

    fn is_mounted(&self, mountpoint: &str) -> bool {
        self.runner.run("mount", &[])
            .ok()
            .filter(|o| o.status.success())
            .map(|o| {
                let stdout = String::from_utf8_lossy(&o.stdout);
                stdout.lines().any(|line| {
                    // Format: /dev/disk1s1 on /mountpoint (type, options)
                    line.split(" on ").nth(1)
                        .and_then(|rest| rest.split_whitespace().next())
                        .map(|mp| mp == mountpoint)
                        .unwrap_or(false)
                })
            })
            .unwrap_or(false)
    }
}

impl Executor for MountBsdExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let mountpoint = params.get("mountpoint").or_else(|| params.get("path")).or_else(|| params.get("name")).cloned().unwrap_or_default();
        let state = params.get("state").map(|s| s.as_str()).unwrap_or("mounted");

        let mut result = ResourceResult::new("mount", &mountpoint);

        if mountpoint.is_empty() {
            result.error = Some("mount executor: mountpoint param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let mounted = self.is_mounted(&mountpoint);

        match state {
            "mounted" | "present" => {
                if mounted {
                    result.action = Action::None;
                } else {
                    result.action = Action::Created;
                    result.changed = true;
                    if !dry_run {
                        let device = params.get("device").or_else(|| params.get("src")).cloned().unwrap_or_default();
                        let fstype = params.get("fstype").cloned().unwrap_or_default();
                        let mut args = vec![];
                        if !fstype.is_empty() {
                            args.push("-t");
                            args.push(&fstype);
                        }
                        args.push(&device);
                        args.push(&mountpoint);

                        // Ensure mountpoint directory exists.
                        let _ = std::fs::create_dir_all(&mountpoint);

                        match self.runner.run("mount", &args.iter().map(|s| s.as_ref()).collect::<Vec<&str>>()) {
                            Ok(o) if o.status.success() => {}
                            Ok(o) => {
                                let stderr = String::from_utf8_lossy(&o.stderr);
                                result.error = Some(format!("mount: {}", stderr));
                            }
                            Err(e) => result.error = Some(format!("mount: {}", e)),
                        }
                    }
                }
            }
            "unmounted" | "absent" => {
                if !mounted {
                    result.action = Action::None;
                } else {
                    result.action = Action::Deleted;
                    result.changed = true;
                    if !dry_run {
                        match self.runner.run("umount", &[&mountpoint]) {
                            Ok(o) if o.status.success() => {}
                            Ok(o) => {
                                let stderr = String::from_utf8_lossy(&o.stderr);
                                result.error = Some(format!("umount: {}", stderr));
                            }
                            Err(e) => result.error = Some(format!("umount: {}", e)),
                        }
                    }
                }
            }
            other => result.error = Some(format!("unknown mount state: {}", other)),
        }

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn missing_mountpoint_errors() {
        let exec = MountBsdExecutor::new();
        let result = exec.execute(&HashMap::new(), false);
        assert!(result.error.is_some());
    }
}
