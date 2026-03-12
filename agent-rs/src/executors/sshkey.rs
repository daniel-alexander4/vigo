// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! SSH key executor — manages ssh_known_hosts entries idempotently.

use super::{Action, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::Instant;

pub struct SshKeyExecutor;

impl Executor for SshKeyExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let hostname = params
            .get("hostname")
            .or_else(|| params.get("name"))
            .cloned()
            .unwrap_or_default();

        let mut result = ResourceResult::new("sshkey", &hostname);

        if hostname.is_empty() {
            result.error = Some("sshkey executor: hostname param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");
        let key_type = params.get("type").cloned().unwrap_or_default();
        let key_data = params.get("key").cloned().unwrap_or_default();
        let aliases = params.get("aliases").cloned().unwrap_or_default();
        let file = params
            .get("file")
            .map(|s| s.as_str())
            .unwrap_or("/etc/ssh/ssh_known_hosts");

        let content = std::fs::read_to_string(file).unwrap_or_default();

        match state {
            "present" => {
                if key_type.is_empty() || key_data.is_empty() {
                    result.error = Some("sshkey executor: type and key params required for state=present".into());
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                let host_field = if aliases.is_empty() {
                    hostname.clone()
                } else {
                    format!("{},{}", hostname, aliases.replace(' ', ","))
                };
                let desired_line = format!("{} {} {}", host_field, key_type, key_data);

                // Check if exact line exists.
                if content.lines().any(|l| l.trim() == desired_line.trim()) {
                    result.action = Action::None;
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                // Remove old entries for this hostname+type, then append.
                let mut lines: Vec<&str> = content
                    .lines()
                    .filter(|line| {
                        let fields: Vec<&str> = line.split_whitespace().collect();
                        if fields.len() >= 2 {
                            let hosts = fields[0];
                            let ltype = fields[1];
                            !(hosts.split(',').any(|h| h == hostname) && ltype == key_type)
                        } else {
                            true
                        }
                    })
                    .collect();

                let had_old = lines.len() < content.lines().count();
                lines.push(&desired_line);

                result.action = if had_old {
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
                    if let Err(e) = std::fs::write(file, output) {
                        result.error = Some(format!("write {}: {}", file, e));
                    }
                }
            }
            "absent" => {
                let filtered: Vec<&str> = content
                    .lines()
                    .filter(|line| {
                        let fields: Vec<&str> = line.split_whitespace().collect();
                        if fields.is_empty() {
                            return true;
                        }
                        !fields[0].split(',').any(|h| h == hostname)
                    })
                    .collect();

                if filtered.len() == content.lines().count() {
                    result.action = Action::None;
                } else {
                    result.action = Action::Deleted;
                    result.changed = true;

                    if !dry_run {
                        let mut output = filtered.join("\n");
                        if !output.ends_with('\n') && !output.is_empty() {
                            output.push('\n');
                        }
                        if let Err(e) = std::fs::write(file, output) {
                            result.error = Some(format!("write {}: {}", file, e));
                        }
                    }
                }
            }
            _ => {
                result.error = Some(format!("sshkey executor: unknown state {:?}", state));
            }
        }

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}
