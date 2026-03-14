// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! cisco_acl executor — manages Access Control Lists on Cisco IOS/NX-OS over SSH.
//!
//! Params:
//! - host (required): target device hostname/IP
//! - username (required): SSH username
//! - credential (required): SSH password or key
//! - port (optional): SSH port, default 22
//! - acl_name (required): name of the access list
//! - entries (required): newline-separated ACL entries (e.g. "permit ip 10.0.0.0 0.0.0.255 any\ndeny ip any any")
//! - state (optional): present (default) or absent

use crate::executors::{Action, Executor, ResourceResult};
use crate::transport::ssh::SshSession;
use std::collections::HashMap;
use std::time::{Duration, Instant};
use tracing::debug;

pub struct CiscoAclExecutor;

impl CiscoAclExecutor {
    pub fn new() -> Self {
        Self
    }
}

impl Executor for CiscoAclExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let mut result =
            ResourceResult::new("cisco_acl", params.get("name").map_or("", |s| s));

        let host = match params.get("host") {
            Some(h) => h.clone(),
            None => return result.with_error("missing required param: host".into()),
        };
        let username = match params.get("username") {
            Some(u) => u.clone(),
            None => return result.with_error("missing required param: username".into()),
        };
        let credential = match params.get("credential") {
            Some(c) => c.clone(),
            None => return result.with_error("missing required param: credential".into()),
        };
        let acl_name = match params.get("acl_name") {
            Some(a) => a.clone(),
            None => return result.with_error("missing required param: acl_name".into()),
        };
        let entries = match params.get("entries") {
            Some(e) => e.clone(),
            None => return result.with_error("missing required param: entries".into()),
        };

        let port: u16 = params
            .get("port")
            .and_then(|p| p.parse().ok())
            .unwrap_or(22);
        let state = params.get("state").map_or("present", |s| s.as_str());
        let timeout = Duration::from_secs(30);
        let prompt_pattern = "#";

        let entry_lines: Vec<&str> = entries
            .lines()
            .map(|l| l.trim())
            .filter(|l| !l.is_empty())
            .collect();

        let rt = match tokio::runtime::Handle::try_current() {
            Ok(h) => h,
            Err(_) => return result.with_error("no tokio runtime available".into()),
        };

        let exec_result = tokio::task::block_in_place(|| {
            rt.block_on(async {
                let session =
                    SshSession::connect(&host, port, &username, &credential, None).await?;

                // Check current ACL state.
                let show_cmd = format!("show access-lists {}", acl_name);
                let current = session
                    .exec_interactive(&[&show_cmd], prompt_pattern, timeout)
                    .await?;
                let current_output = current;

                let needs_change = match state {
                    "present" => {
                        // Check if all entries exist in the current ACL output.
                        entry_lines
                            .iter()
                            .any(|entry| !current_output.contains(entry))
                    }
                    "absent" => {
                        // Check if the ACL exists at all.
                        !current_output.trim().is_empty()
                            && current_output.contains(&acl_name)
                    }
                    _ => return Err(anyhow::anyhow!("invalid state: {}", state)),
                };

                if !needs_change {
                    return Ok(false);
                }

                if dry_run {
                    return Ok(true);
                }

                // Build config commands.
                let mut commands: Vec<String> = vec!["configure terminal".to_string()];

                match state {
                    "absent" => {
                        commands.push(format!("no ip access-list extended {}", acl_name));
                    }
                    _ => {
                        commands.push(format!("ip access-list extended {}", acl_name));
                        for entry in &entry_lines {
                            commands.push(entry.to_string());
                        }
                    }
                }

                commands.push("end".to_string());
                commands.push("write memory".to_string());

                let cmd_refs: Vec<&str> = commands.iter().map(|s| s.as_str()).collect();
                let _output = session
                    .exec_interactive(&cmd_refs, prompt_pattern, timeout)
                    .await?;

                debug!(
                    host = host.as_str(),
                    acl_name = acl_name.as_str(),
                    state = state,
                    "ACL config applied"
                );
                Ok(true)
            })
        });

        result.duration_ms = start.elapsed().as_millis() as i32;

        match exec_result {
            Ok(false) => {
                result.action = Action::None;
                result.changed = false;
            }
            Ok(true) => {
                result.action = Action::Updated;
                result.changed = true;
            }
            Err(e) => {
                result.error = Some(e.to_string());
            }
        }

        result
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_cisco_acl_missing_params() {
        let executor = CiscoAclExecutor::new();
        let params = HashMap::new();
        let result = executor.execute(&params, false);
        assert!(result.error.is_some());
    }

    #[test]
    fn test_cisco_acl_missing_acl_name() {
        let executor = CiscoAclExecutor::new();
        let mut params = HashMap::new();
        params.insert("host".to_string(), "10.0.0.1".to_string());
        params.insert("username".to_string(), "admin".to_string());
        params.insert("credential".to_string(), "secret".to_string());
        let result = executor.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("acl_name"));
    }
}
