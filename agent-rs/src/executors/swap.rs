// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Swap executor — manages swap files idempotently.

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::Instant;

pub struct SwapExecutor {
    pub runner: Box<dyn CommandRunner>,
}

impl SwapExecutor {
    pub fn new() -> Self {
        Self {
            runner: Box::new(DefaultRunner),
        }
    }

    fn is_active(&self, path: &str) -> bool {
        let content = std::fs::read_to_string("/proc/swaps").unwrap_or_default();
        content.lines().any(|line| {
            line.split_whitespace().next() == Some(path)
        })
    }

    fn in_fstab(&self, path: &str) -> bool {
        let content = std::fs::read_to_string("/etc/fstab").unwrap_or_default();
        content.lines().any(|line| {
            let trimmed = line.trim();
            if trimmed.is_empty() || trimmed.starts_with('#') {
                return false;
            }
            let fields: Vec<&str> = trimmed.split_whitespace().collect();
            fields.len() >= 3 && fields[0] == path && fields[2] == "swap"
        })
    }

    fn add_to_fstab(&self, path: &str) -> Result<(), String> {
        let entry = format!("{}\tnone\tswap\tsw\t0\t0\n", path);
        let mut content = std::fs::read_to_string("/etc/fstab").unwrap_or_default();
        if !content.ends_with('\n') && !content.is_empty() {
            content.push('\n');
        }
        content.push_str(&entry);
        std::fs::write("/etc/fstab", content)
            .map_err(|e| format!("write /etc/fstab: {}", e))
    }

    fn remove_from_fstab(&self, path: &str) -> Result<(), String> {
        let content = std::fs::read_to_string("/etc/fstab").unwrap_or_default();
        let filtered: Vec<&str> = content
            .lines()
            .filter(|line| {
                let fields: Vec<&str> = line.split_whitespace().collect();
                !(fields.len() >= 3 && fields[0] == path && fields[2] == "swap")
            })
            .collect();
        let mut output = filtered.join("\n");
        if !output.ends_with('\n') && !output.is_empty() {
            output.push('\n');
        }
        std::fs::write("/etc/fstab", output)
            .map_err(|e| format!("write /etc/fstab: {}", e))
    }
}

impl Executor for SwapExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let path = params
            .get("path")
            .or_else(|| params.get("name"))
            .cloned()
            .unwrap_or_default();

        let mut result = ResourceResult::new("swap", &path);

        if path.is_empty() {
            result.error = Some("swap executor: path param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");
        let size = params.get("size").cloned().unwrap_or_default();
        let priority = params.get("priority").cloned().unwrap_or_default();

        let file_exists = std::path::Path::new(&path).exists();
        let active = self.is_active(&path);
        let fstab = self.in_fstab(&path);

        match state {
            "present" => {
                if file_exists && active && fstab {
                    result.action = Action::None;
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                if size.is_empty() && !file_exists {
                    result.error = Some("swap executor: size param is required for new swap files".into());
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                result.action = Action::Created;
                result.changed = true;

                if dry_run {
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                // Create swap file if needed.
                if !file_exists {
                    let size_arg = format!("{}M", size);
                    for (cmd, args) in [
                        ("fallocate", vec!["-l", &size_arg, &path]),
                        ("chmod", vec!["0600", &path]),
                        ("mkswap", vec![path.as_str()]),
                    ] {
                        let arg_refs: Vec<&str> = args.iter().map(|s| &**s).collect();
                        match self.runner.run(cmd, &arg_refs) {
                            Ok(o) if !o.status.success() => {
                                let stderr = String::from_utf8_lossy(&o.stderr);
                                result.error = Some(format!("{}: {}", cmd, stderr));
                                result.duration_ms = start.elapsed().as_millis() as i32;
                                return result;
                            }
                            Err(e) => {
                                result.error = Some(format!("{}: {}", cmd, e));
                                result.duration_ms = start.elapsed().as_millis() as i32;
                                return result;
                            }
                            _ => {}
                        }
                    }
                }

                // Activate.
                if !active {
                    let mut args = vec![];
                    if !priority.is_empty() {
                        args.push("-p");
                        args.push(&priority);
                    }
                    args.push(&path);
                    match self.runner.run("swapon", &args) {
                        Ok(o) if !o.status.success() => {
                            let stderr = String::from_utf8_lossy(&o.stderr);
                            result.error = Some(format!("swapon: {}", stderr));
                            result.duration_ms = start.elapsed().as_millis() as i32;
                            return result;
                        }
                        Err(e) => {
                            result.error = Some(format!("swapon: {}", e));
                            result.duration_ms = start.elapsed().as_millis() as i32;
                            return result;
                        }
                        _ => {}
                    }
                }

                // Add to fstab.
                if !fstab {
                    if let Err(e) = self.add_to_fstab(&path) {
                        result.error = Some(e);
                    }
                }
            }
            "absent" => {
                if !file_exists && !active && !fstab {
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

                if active {
                    match self.runner.run("swapoff", &[&path]) {
                        Ok(o) if !o.status.success() => {
                            let stderr = String::from_utf8_lossy(&o.stderr);
                            result.error = Some(format!("swapoff: {}", stderr));
                            result.duration_ms = start.elapsed().as_millis() as i32;
                            return result;
                        }
                        Err(e) => {
                            result.error = Some(format!("swapoff: {}", e));
                            result.duration_ms = start.elapsed().as_millis() as i32;
                            return result;
                        }
                        _ => {}
                    }
                }

                if fstab {
                    if let Err(e) = self.remove_from_fstab(&path) {
                        result.error = Some(e);
                        result.duration_ms = start.elapsed().as_millis() as i32;
                        return result;
                    }
                }

                if file_exists {
                    if let Err(e) = std::fs::remove_file(&path) {
                        result.error = Some(format!("remove {}: {}", path, e));
                    }
                }
            }
            _ => {
                result.error = Some(format!("swap executor: unknown state {:?}", state));
            }
        }

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}
