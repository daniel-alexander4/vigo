// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Host executor — manages /etc/hosts entries idempotently.

use super::{Action, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::Instant;

pub struct HostExecutor {
    pub hosts_file: String,
}

impl HostExecutor {
    pub fn new() -> Self {
        Self {
            hosts_file: "/etc/hosts".into(),
        }
    }
}

impl Executor for HostExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let name = params
            .get("hostname")
            .or_else(|| params.get("name"))
            .cloned()
            .unwrap_or_default();

        let mut result = ResourceResult::new("host", &name);

        if name.is_empty() {
            result.error = Some("host executor: hostname param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");
        let ip = params.get("ip").cloned().unwrap_or_default();
        let aliases = params.get("aliases").cloned().unwrap_or_default();

        let content = match std::fs::read_to_string(&self.hosts_file) {
            Ok(c) => c,
            Err(e) => {
                result.error = Some(format!("read {}: {}", self.hosts_file, e));
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
        };

        match state {
            "present" => {
                if ip.is_empty() {
                    result.error = Some("host executor: ip param is required for state=present".into());
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                let desired_line = if aliases.is_empty() {
                    format!("{}\t{}", ip, name)
                } else {
                    format!("{}\t{} {}", ip, name, aliases)
                };

                // Filter out existing lines for this hostname.
                let mut lines: Vec<String> = Vec::new();
                let mut had_entry = false;
                for line in content.lines() {
                    let fields: Vec<&str> = line.split_whitespace().collect();
                    if fields.len() >= 2 && fields[1..].iter().any(|f| *f == name) {
                        had_entry = true;
                        // Check if the existing line matches.
                        let normalized: String = fields.join(" ");
                        let desired_norm: String = desired_line.split_whitespace().collect::<Vec<_>>().join(" ");
                        if normalized == desired_norm {
                            // Already correct — put it back.
                            lines.push(line.to_string());
                        }
                        // Otherwise skip (will append new line below).
                    } else {
                        lines.push(line.to_string());
                    }
                }

                // Check if the desired entry already exists.
                let desired_norm: String = desired_line.split_whitespace().collect::<Vec<_>>().join(" ");
                let exists = lines.iter().any(|l| {
                    let norm: String = l.split_whitespace().collect::<Vec<_>>().join(" ");
                    norm == desired_norm
                });

                if exists && had_entry {
                    result.action = Action::None;
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                if !exists {
                    lines.push(desired_line);
                }

                result.action = if had_entry {
                    Action::Updated
                } else {
                    Action::Created
                };
                result.changed = true;

                if !dry_run {
                    let mut output = lines.join("\n");
                    if !output.ends_with('\n') {
                        output.push('\n');
                    }
                    if let Err(e) = std::fs::write(&self.hosts_file, output) {
                        result.error = Some(format!("write {}: {}", self.hosts_file, e));
                    }
                }
            }
            "absent" => {
                let filtered: Vec<&str> = content
                    .lines()
                    .filter(|line| {
                        let fields: Vec<&str> = line.split_whitespace().collect();
                        !(fields.len() >= 2 && fields[1..].iter().any(|f| *f == name))
                    })
                    .collect();

                if filtered.len() == content.lines().count() {
                    result.action = Action::None;
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                result.action = Action::Deleted;
                result.changed = true;

                if !dry_run {
                    let mut output = filtered.join("\n");
                    if !output.ends_with('\n') && !output.is_empty() {
                        output.push('\n');
                    }
                    if let Err(e) = std::fs::write(&self.hosts_file, output) {
                        result.error = Some(format!("write {}: {}", self.hosts_file, e));
                    }
                }
            }
            _ => {
                result.error = Some(format!("host executor: unknown state {:?}", state));
            }
        }

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}
