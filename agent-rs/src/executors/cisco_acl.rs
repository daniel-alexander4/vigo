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

use crate::executors::{Executor, ResourceResult};
use crate::executors::network_common::{self, require_param, SshParams, DEFAULT_TIMEOUT};
use std::collections::HashMap;
use std::time::Instant;
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

        let ssh = match SshParams::from_params(params, result) {
            Ok(s) => s,
            Err(r) => return r,
        };
        result = ResourceResult::new("cisco_acl", params.get("name").map_or("", |s| s));
        let acl_name = require_param!(params, "acl_name", result);
        let entries = require_param!(params, "entries", result);
        let state = network_common::parse_state(params).to_string();
        let timeout = DEFAULT_TIMEOUT;
        let prompt_pattern = "#";

        let entry_lines: Vec<String> = entries
            .lines()
            .map(|l| l.trim().to_string())
            .filter(|l| !l.is_empty())
            .collect();

        let host_log = ssh.host.clone();
        network_common::run_ssh_executor(&mut result, start, &ssh, |session| {
            Box::pin(async move {
                // Check current ACL state.
                let show_cmd = format!("show access-lists {}", acl_name);
                let current = session
                    .exec_interactive(&[&show_cmd], prompt_pattern, timeout)
                    .await?;
                let current_output = current;

                let needs_change = match state.as_str() {
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

                match state.as_str() {
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
                    host = host_log.as_str(),
                    acl_name = acl_name.as_str(),
                    state = state.as_str(),
                    "ACL config applied"
                );
                Ok(true)
            })
        });

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
