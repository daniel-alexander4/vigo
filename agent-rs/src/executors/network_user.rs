// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

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
use crate::transport::ssh::SshSession;
use std::collections::HashMap;
use std::time::{Duration, Instant};
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
        let device_username = match params.get("device_username") {
            Some(du) => du.clone(),
            None => return result.with_error("missing required param: device_username".into()),
        };

        let port: u16 = params
            .get("port")
            .and_then(|p| p.parse().ok())
            .unwrap_or(22);
        let state = params.get("state").map_or("present", |s| s.as_str());
        let privilege = params.get("privilege").cloned();
        let secret = params.get("secret").cloned();
        let timeout = Duration::from_secs(30);

        let (check_command, prompt_pattern) = match device_type.as_str() {
            "cisco_ios" | "cisco_nxos" => (
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

        let rt = match tokio::runtime::Handle::try_current() {
            Ok(h) => h,
            Err(_) => return result.with_error("no tokio runtime available".into()),
        };

        let exec_result = tokio::task::block_in_place(|| {
            rt.block_on(async {
                let session =
                    SshSession::connect(&host, port, &username, &credential, None).await?;

                // Check current state.
                let current = session.exec(&check_command, timeout).await?;
                let current_output = current.stdout;

                let user_exists = match device_type.as_str() {
                    "cisco_ios" | "cisco_nxos" => {
                        current_output.contains(&format!("username {}", device_username))
                    }
                    "juniper_junos" => {
                        current_output.contains(&format!("user {}", device_username))
                    }
                    _ => false,
                };

                let needs_change = match state {
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
                let commands: Vec<String> = match (device_type.as_str(), state) {
                    ("cisco_ios" | "cisco_nxos", "present") => {
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
                    ("cisco_ios" | "cisco_nxos", "absent") => {
                        vec![
                            "configure terminal".to_string(),
                            format!("no username {}", device_username),
                            "end".to_string(),
                            "write memory".to_string(),
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
                    host = host.as_str(),
                    device_type = device_type.as_str(),
                    device_username = device_username.as_str(),
                    state = state,
                    "network user config applied"
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
                result.action = if state == "absent" {
                    Action::Deleted
                } else {
                    Action::Updated
                };
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
