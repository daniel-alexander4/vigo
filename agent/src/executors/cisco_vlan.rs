// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! cisco_vlan executor — manages VLANs on Cisco IOS/NX-OS switches over SSH.
//!
//! Params:
//! - host (required): switch hostname or IP
//! - username (required): SSH username
//! - credential (required): SSH password or key
//! - port (optional): SSH port, default 22
//! - vlan_id (required): VLAN number (1-4094)
//! - name (required): VLAN name
//! - state (optional): present (default) or absent

use crate::executors::{Executor, ResourceResult};
use crate::executors::network_common::{self, require_param, SshParams, DEFAULT_TIMEOUT};
use std::collections::HashMap;
use std::time::Instant;
use tracing::debug;

pub struct CiscoVlanExecutor;

impl CiscoVlanExecutor {
    pub fn new() -> Self {
        Self
    }
}

/// Parse `show vlan brief` output to find a VLAN by ID.
/// Returns Some(name) if the VLAN exists, None otherwise.
///
/// Expected format (Cisco IOS/NX-OS):
/// ```text
/// VLAN Name                             Status    Ports
/// ---- -------------------------------- --------- ---
/// 1    default                          active    Gi0/1, Gi0/2
/// 100  engineering                      active    Gi0/3
/// ```
fn parse_vlan_brief(output: &str, vlan_id: &str) -> Option<String> {
    for line in output.lines() {
        let trimmed = line.trim();
        if trimmed.is_empty() {
            continue;
        }
        let fields: Vec<&str> = trimmed.split_whitespace().collect();
        if fields.len() >= 2 && fields[0] == vlan_id {
            return Some(fields[1].to_string());
        }
    }
    None
}

impl Executor for CiscoVlanExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let mut result = ResourceResult::new(
            "cisco_vlan",
            params.get("name").map_or("", |s| s),
        );

        let ssh = match SshParams::from_params(params, result) {
            Ok(s) => s,
            Err(r) => return r,
        };
        result = ResourceResult::new("cisco_vlan", params.get("name").map_or("", |s| s));
        let vlan_id = require_param!(params, "vlan_id", result);
        let vlan_name = require_param!(params, "name", result);
        let state = network_common::parse_state(params).to_string();
        let timeout = DEFAULT_TIMEOUT;

        if state != "present" && state != "absent" {
            return result.with_error(format!("invalid state: {} (must be present or absent)", state));
        }

        let host_log = ssh.host.clone();
        network_common::run_ssh_executor(&mut result, start, &ssh, |session| {
            Box::pin(async move {
                // Check current VLAN state.
                let output = session.exec("show vlan brief", timeout).await?;
                let existing_name = parse_vlan_brief(&output.stdout, &vlan_id);

                match state.as_str() {
                    "present" => {
                        if let Some(ref current_name) = existing_name {
                            if current_name == &vlan_name {
                                // VLAN exists with correct name — no change needed.
                                return Ok(false);
                            }
                        }
                        // VLAN missing or name mismatch — need to create/update.
                        if dry_run {
                            return Ok(true);
                        }

                        let commands = [
                            "configure terminal".to_string(),
                            format!("vlan {}", vlan_id),
                            format!("name {}", vlan_name),
                            "end".to_string(),
                            "write memory".to_string(),
                        ];
                        let cmd_refs: Vec<&str> = commands.iter().map(|s| s.as_str()).collect();
                        let _output = session.exec_interactive(&cmd_refs, "#", timeout).await?;

                        debug!(
                            host = host_log.as_str(),
                            vlan_id = vlan_id.as_str(),
                            vlan_name = vlan_name.as_str(),
                            "vlan configured"
                        );
                        Ok(true)
                    }
                    "absent" => {
                        if existing_name.is_none() {
                            // VLAN doesn't exist — no change needed.
                            return Ok(false);
                        }
                        if dry_run {
                            return Ok(true);
                        }

                        let commands = [
                            "configure terminal".to_string(),
                            format!("no vlan {}", vlan_id),
                            "end".to_string(),
                            "write memory".to_string(),
                        ];
                        let cmd_refs: Vec<&str> = commands.iter().map(|s| s.as_str()).collect();
                        let _output = session.exec_interactive(&cmd_refs, "#", timeout).await?;

                        debug!(
                            host = host_log.as_str(),
                            vlan_id = vlan_id.as_str(),
                            "vlan removed"
                        );
                        Ok(true)
                    }
                    _ => Err(anyhow::anyhow!("invalid state: {}", state)),
                }
            })
        });

        result
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_parse_vlan_brief_found() {
        let output = "\
VLAN Name                             Status    Ports
---- -------------------------------- --------- ---
1    default                          active    Gi0/1, Gi0/2
100  engineering                      active    Gi0/3
200  marketing                        active    Gi0/4";
        assert_eq!(parse_vlan_brief(output, "100"), Some("engineering".to_string()));
        assert_eq!(parse_vlan_brief(output, "200"), Some("marketing".to_string()));
    }

    #[test]
    fn test_parse_vlan_brief_not_found() {
        let output = "\
VLAN Name                             Status    Ports
---- -------------------------------- --------- ---
1    default                          active    Gi0/1";
        assert_eq!(parse_vlan_brief(output, "100"), None);
    }

    #[test]
    fn test_cisco_vlan_missing_params() {
        let executor = CiscoVlanExecutor::new();
        let params = HashMap::new();
        let result = executor.execute(&params, false);
        assert!(result.error.is_some());
    }

    #[test]
    fn test_cisco_vlan_missing_vlan_id() {
        let executor = CiscoVlanExecutor::new();
        let mut params = HashMap::new();
        params.insert("host".to_string(), "10.0.0.1".to_string());
        params.insert("username".to_string(), "admin".to_string());
        params.insert("credential".to_string(), "secret".to_string());
        let result = executor.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("vlan_id"));
    }
}
