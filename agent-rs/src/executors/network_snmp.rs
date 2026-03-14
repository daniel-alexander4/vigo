// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

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

use crate::executors::{Action, Executor, ResourceResult};
use crate::transport::ssh::SshSession;
use std::collections::HashMap;
use std::time::{Duration, Instant};
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
        let device_type = match params.get("device_type") {
            Some(dt) => dt.clone(),
            None => return result.with_error("missing required param: device_type".into()),
        };
        let community = match params.get("community") {
            Some(c) => c.clone(),
            None => return result.with_error("missing required param: community".into()),
        };

        let port: u16 = params
            .get("port")
            .and_then(|p| p.parse().ok())
            .unwrap_or(22);
        let permission = params
            .get("permission")
            .map_or("RO", |s| s.as_str())
            .to_string();
        let acl = params.get("acl").cloned();
        let state = params.get("state").map_or("present", |s| s.as_str());
        let timeout = Duration::from_secs(30);

        let prompt_pattern = match device_type.as_str() {
            "cisco_ios" | "cisco_nxos" => "#",
            "juniper_junos" => ">",
            _ => return result.with_error(format!("unsupported device_type: {}", device_type)),
        };

        let rt = match tokio::runtime::Handle::try_current() {
            Ok(h) => h,
            Err(_) => return result.with_error("no tokio runtime available".into()),
        };

        let exec_result = tokio::task::block_in_place(|| {
            rt.block_on(async {
                let session =
                    SshSession::connect(&host, port, &username, &credential, None).await?;

                // Check current SNMP configuration.
                let check_command = match device_type.as_str() {
                    "cisco_ios" | "cisco_nxos" => {
                        "show running-config | include snmp-server community".to_string()
                    }
                    "juniper_junos" => "show configuration snmp".to_string(),
                    _ => unreachable!(),
                };

                let current = session.exec(&check_command, timeout).await?;
                let current_config = current.stdout;

                let community_present = current_config.contains(&community);

                let needs_change = match state {
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
                let commands: Vec<String> = match (device_type.as_str(), state) {
                    ("cisco_ios" | "cisco_nxos", "present") => {
                        let mut snmp_cmd =
                            format!("snmp-server community {} {}", community, permission);
                        if let Some(ref acl_name) = acl {
                            snmp_cmd.push(' ');
                            snmp_cmd.push_str(acl_name);
                        }
                        let cmds = vec![
                            "configure terminal".to_string(),
                            snmp_cmd,
                            "end".to_string(),
                            "write memory".to_string(),
                        ];
                        cmds
                    }
                    ("cisco_ios" | "cisco_nxos", "absent") => {
                        vec![
                            "configure terminal".to_string(),
                            format!("no snmp-server community {}", community),
                            "end".to_string(),
                            "write memory".to_string(),
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
                    _ => unreachable!(),
                };

                let cmd_refs: Vec<&str> = commands.iter().map(|s| s.as_str()).collect();
                let _output = session
                    .exec_interactive(&cmd_refs, prompt_pattern, timeout)
                    .await?;

                debug!(
                    host = host.as_str(),
                    device_type = device_type.as_str(),
                    community = community.as_str(),
                    state = state,
                    "SNMP community configured"
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
