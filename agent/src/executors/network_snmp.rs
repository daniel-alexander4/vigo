// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! network_snmp executor — manages SNMP community configuration on network devices over SSH.
//!
//! Params:
//! - host (required): auto-injected
//! - username (required): auto-injected
//! - credential (required): auto-injected
//! - port (optional): SSH port, default 22
//! - device_type (required): cisco_ios, cisco_nxos, juniper_junos
//! - community (required): SNMP community string
//! - permission (optional): RO (default) or RW
//! - acl (optional): access list name (Cisco only)
//! - state (optional): present (default) or absent

use crate::executors::{Executor, ResourceResult};
use crate::executors::network_common::{self, require_param, SshParams, DEFAULT_TIMEOUT};
use std::collections::HashMap;
use std::time::Instant;
use tracing::debug;

pub struct NetworkSnmpExecutor;

impl NetworkSnmpExecutor {
    pub fn new() -> Self {
        Self
    }
}

impl Executor for NetworkSnmpExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let mut result = ResourceResult::new(
            "network_snmp",
            params.get("name").map_or("", |s| s),
        );

        let ssh = match SshParams::from_params(params, result) {
            Ok(s) => s,
            Err(r) => return *r,
        };
        result = ResourceResult::new("network_snmp", params.get("name").map_or("", |s| s));
        let device_type = require_param!(params, "device_type", result);
        let community = require_param!(params, "community", result);
        let permission = params
            .get("permission")
            .map_or("RO", |s| s.as_str())
            .to_string();
        let acl = params.get("acl").cloned();
        let state = network_common::parse_state(params).to_string();
        let timeout = DEFAULT_TIMEOUT;

        let prompt_pattern = match device_type.as_str() {
            "cisco_ios" | "cisco_nxos" | "arista_eos" | "cisco_iosxr" => "#",
            "juniper_junos" => ">",
            _ => return result.with_error(format!("unsupported device_type: {}", device_type)),
        };

        let host_log = ssh.host.clone();
        network_common::run_ssh_executor(&mut result, start, &ssh, |session| {
            Box::pin(async move {
                // Check current SNMP configuration.
                let check_command = match device_type.as_str() {
                    "cisco_ios" | "cisco_nxos" | "arista_eos" | "cisco_iosxr" => {
                        "show running-config | include snmp-server community".to_string()
                    }
                    "juniper_junos" => "show configuration snmp".to_string(),
                    _ => return Err(anyhow::anyhow!("unsupported device_type: {}", device_type)),
                };

                let current = session.exec(&check_command, timeout).await?;
                let current_config = current.stdout;

                let community_present = current_config.contains(&community);

                let needs_change = match state.as_str() {
                    "present" => !community_present,
                    "absent" => community_present,
                    _ => return Err(anyhow::anyhow!("invalid state: {}", state)),
                };

                if !needs_change {
                    return Ok(false);
                }

                if dry_run {
                    return Ok(true);
                }

                // Build config commands based on device type and desired state.
                let commands: Vec<String> = match (device_type.as_str(), state.as_str()) {
                    ("cisco_ios" | "cisco_nxos" | "arista_eos", "present") => {
                        let mut snmp_cmd =
                            format!("snmp-server community {} {}", community, permission);
                        if let Some(ref acl_name) = acl {
                            snmp_cmd.push(' ');
                            snmp_cmd.push_str(acl_name);
                        }
                        vec![
                            "configure terminal".to_string(),
                            snmp_cmd,
                            "end".to_string(),
                            "write memory".to_string(),
                        ]
                    }
                    ("cisco_ios" | "cisco_nxos" | "arista_eos", "absent") => {
                        vec![
                            "configure terminal".to_string(),
                            format!("no snmp-server community {}", community),
                            "end".to_string(),
                            "write memory".to_string(),
                        ]
                    }
                    ("cisco_iosxr", "present") => {
                        let mut snmp_cmd =
                            format!("snmp-server community {} {}", community, permission);
                        if let Some(ref acl_name) = acl {
                            snmp_cmd.push(' ');
                            snmp_cmd.push_str(acl_name);
                        }
                        vec![
                            "configure terminal".to_string(),
                            snmp_cmd,
                            "commit".to_string(),
                            "end".to_string(),
                        ]
                    }
                    ("cisco_iosxr", "absent") => {
                        vec![
                            "configure terminal".to_string(),
                            format!("no snmp-server community {}", community),
                            "commit".to_string(),
                            "end".to_string(),
                        ]
                    }
                    ("juniper_junos", "present") => {
                        vec![
                            "configure".to_string(),
                            format!(
                                "set snmp community {} authorization {}",
                                community, permission
                            ),
                            "commit".to_string(),
                            "exit".to_string(),
                        ]
                    }
                    ("juniper_junos", "absent") => {
                        vec![
                            "configure".to_string(),
                            format!("delete snmp community {}", community),
                            "commit".to_string(),
                            "exit".to_string(),
                        ]
                    }
                    _ => return Err(anyhow::anyhow!("unsupported device_type/state combo: {}/{}", device_type, state)),
                };

                let cmd_refs: Vec<&str> = commands.iter().map(|s| s.as_str()).collect();
                let _output = session
                    .exec_interactive(&cmd_refs, prompt_pattern, timeout)
                    .await?;

                debug!(
                    host = host_log.as_str(),
                    device_type = device_type.as_str(),
                    community = community.as_str(),
                    state = state.as_str(),
                    "SNMP community configured"
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
    fn test_network_snmp_missing_params() {
        let executor = NetworkSnmpExecutor::new();
        let params = HashMap::new();
        let result = executor.execute(&params, false);
        assert!(result.error.is_some());
    }

    #[test]
    fn test_network_snmp_missing_community() {
        let executor = NetworkSnmpExecutor::new();
        let mut params = HashMap::new();
        params.insert("host".to_string(), "10.0.0.1".to_string());
        params.insert("username".to_string(), "admin".to_string());
        params.insert("credential".to_string(), "secret".to_string());
        params.insert("device_type".to_string(), "cisco_ios".to_string());
        let result = executor.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("community"));
    }
}
