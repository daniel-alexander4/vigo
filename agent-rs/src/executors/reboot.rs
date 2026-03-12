// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

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
        let delay = params.get("delay").map(|s| s.as_str()).unwrap_or("0");
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
