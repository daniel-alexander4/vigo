// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! network_user executor — manage local user accounts on network devices over SSH.
//!
//! Params:
//! - host (required): auto-injected
//! - username (required): auto-injected (SSH login user)
//! - credential (required): auto-injected
//! - port (optional): SSH port, default 22
//! - device_type (required): cisco_ios, cisco_nxos, juniper_junos
//! - device_username (required): the user account to manage on the device
//! - privilege (optional): privilege level, e.g. "15" for Cisco
//! - secret (optional): password for the device user
//! - state (optional): present (default) or absent

use crate::executors::{Action, Executor, ResourceResult};
use crate::executors::network_common::{self, require_param, SshParams, DEFAULT_TIMEOUT};
use std::collections::HashMap;
use std::time::Instant;
use tracing::debug;

pub struct NetworkUserExecutor;

impl NetworkUserExecutor {
    pub fn new() -> Self {
        Self
    }
}

impl Executor for NetworkUserExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let mut result = ResourceResult::new(
            "network_user",
            params.get("name").map_or("", |s| s),
        );

        let ssh = match SshParams::from_params(params, result) {
            Ok(s) => s,
            Err(r) => return r,
        };
        result = ResourceResult::new("network_user", params.get("name").map_or("", |s| s));
        let device_type = require_param!(params, "device_type", result);
        let device_username = require_param!(params, "device_username", result);
        let state = network_common::parse_state(params).to_string();
        let privilege = params.get("privilege").cloned();
        let secret = params.get("secret").cloned();
        let timeout = DEFAULT_TIMEOUT;

        let (check_command, prompt_pattern) = match device_type.as_str() {
            "cisco_ios" | "cisco_nxos" | "arista_eos" | "cisco_iosxr" => (
                "show running-config | section username".to_string(),
                "#",
            ),
            "juniper_junos" => (
                "show configuration system login".to_string(),
                ">",
            ),
            _ => {
                return result.with_error(format!("unsupported device_type: {}", device_type));
            }
        };

        let action_on_change = if state == "absent" {
            Action::Deleted
        } else {
            Action::Updated
        };

        let host_log = ssh.host.clone();
        network_common::run_ssh_executor_with_action(&mut result, start, &ssh, action_on_change, |session| {
            Box::pin(async move {
                // Check current state.
                let current = session.exec(&check_command, timeout).await?;
                let current_output = current.stdout;

                let user_exists = match device_type.as_str() {
                    "cisco_ios" | "cisco_nxos" | "arista_eos" | "cisco_iosxr" => {
                        current_output.contains(&format!("username {}", device_username))
                    }
                    "juniper_junos" => {
                        current_output.contains(&format!("user {}", device_username))
                    }
                    _ => false,
                };

                let needs_change = match state.as_str() {
                    "present" => !user_exists,
                    "absent" => user_exists,
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
                        let priv_level = privilege.as_deref().unwrap_or("15");
                        let mut cmds = vec![
                            "configure terminal".to_string(),
                        ];
                        if let Some(ref sec) = secret {
                            cmds.push(format!(
                                "username {} privilege {} secret {}",
                                device_username, priv_level, sec
                            ));
                        } else {
                            cmds.push(format!(
                                "username {} privilege {}",
                                device_username, priv_level
                            ));
                        }
                        cmds.push("end".to_string());
                        cmds.push("write memory".to_string());
                        cmds
                    }
                    ("cisco_ios" | "cisco_nxos" | "arista_eos", "absent") => {
                        vec![
                            "configure terminal".to_string(),
                            format!("no username {}", device_username),
                            "end".to_string(),
                            "write memory".to_string(),
                        ]
                    }
                    ("cisco_iosxr", "present") => {
                        let priv_level = privilege.as_deref().unwrap_or("15");
                        let mut cmds = vec![
                            "configure terminal".to_string(),
                        ];
                        if let Some(ref sec) = secret {
                            cmds.push(format!(
                                "username {} privilege {} secret {}",
                                device_username, priv_level, sec
                            ));
                        } else {
                            cmds.push(format!(
                                "username {} privilege {}",
                                device_username, priv_level
                            ));
                        }
                        cmds.push("commit".to_string());
                        cmds.push("end".to_string());
                        cmds
                    }
                    ("cisco_iosxr", "absent") => {
                        vec![
                            "configure terminal".to_string(),
                            format!("no username {}", device_username),
                            "commit".to_string(),
                            "end".to_string(),
                        ]
                    }
                    ("juniper_junos", "present") => {
                        let mut cmds = vec![
                            "configure".to_string(),
                            format!(
                                "set system login user {} class super-user",
                                device_username
                            ),
                        ];
                        if let Some(ref sec) = secret {
                            cmds.push(format!(
                                "set system login user {} authentication plain-text-password-value {}",
                                device_username, sec
                            ));
                        }
                        cmds.push("commit".to_string());
                        cmds.push("exit".to_string());
                        cmds
                    }
                    ("juniper_junos", "absent") => {
                        vec![
                            "configure".to_string(),
                            format!("delete system login user {}", device_username),
                            "commit".to_string(),
                            "exit".to_string(),
                        ]
                    }
                    _ => return Err(anyhow::anyhow!("unsupported device_type: {}", device_type)),
                };

                let cmd_refs: Vec<&str> = commands.iter().map(|s| s.as_str()).collect();
                let _output = session
                    .exec_interactive(&cmd_refs, prompt_pattern, timeout)
                    .await?;

                debug!(
                    host = host_log.as_str(),
                    device_type = device_type.as_str(),
                    device_username = device_username.as_str(),
                    state = state.as_str(),
                    "network user config applied"
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
    fn test_network_user_missing_params() {
        let executor = NetworkUserExecutor::new();
        let params = HashMap::new();
        let result = executor.execute(&params, false);
        assert!(result.error.is_some());
    }

    #[test]
    fn test_network_user_missing_device_username() {
        let executor = NetworkUserExecutor::new();
        let mut params = HashMap::new();
        params.insert("host".to_string(), "10.0.0.1".to_string());
        params.insert("username".to_string(), "admin".to_string());
        params.insert("credential".to_string(), "password123".to_string());
        params.insert("device_type".to_string(), "cisco_ios".to_string());
        let result = executor.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("device_username"));
    }
}
