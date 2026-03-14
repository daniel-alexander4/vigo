// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! network_syslog executor — manages syslog server configuration on network devices over SSH.
//!
//! Params:
//! - host (required): auto-injected
//! - username (required): auto-injected
//! - credential (required): auto-injected
//! - port (optional): SSH port, default 22
//! - device_type (required): cisco_ios, cisco_nxos, juniper_junos
//! - syslog_host (required): syslog server address to configure
//! - syslog_port (optional): syslog server port
//! - facility (optional): syslog facility (Cisco only)
//! - state (optional): present (default) or absent

use crate::executors::{Action, Executor, ResourceResult};
use crate::transport::ssh::SshSession;
use std::collections::HashMap;
use std::time::{Duration, Instant};
use tracing::debug;

pub struct NetworkSyslogExecutor;

impl NetworkSyslogExecutor {
    pub fn new() -> Self {
        Self
    }
}

impl Executor for NetworkSyslogExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let mut result = ResourceResult::new(
            "network_syslog",
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
        let syslog_host = match params.get("syslog_host") {
            Some(sh) => sh.clone(),
            None => return result.with_error("missing required param: syslog_host".into()),
        };

        let port: u16 = params
            .get("port")
            .and_then(|p| p.parse().ok())
            .unwrap_or(22);
        let state = params.get("state").map_or("present", |s| s.as_str());
        let syslog_port = params.get("syslog_port").cloned();
        let facility = params.get("facility").cloned();
        let timeout = Duration::from_secs(30);

        // Validate device_type early.
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
                let session = SshSession::connect(&host, port, &username, &credential, None).await?;

                // Check current syslog configuration.
                let is_configured = match device_type.as_str() {
                    "cisco_ios" | "cisco_nxos" => {
                        let current = session
                            .exec("show running-config | include logging host", timeout)
                            .await?;
                        current.stdout.contains(&format!("logging host {}", syslog_host))
                    }
                    "juniper_junos" => {
                        let current = session
                            .exec("show configuration system syslog", timeout)
                            .await?;
                        current.stdout.contains(&format!("host {}", syslog_host))
                    }
                    _ => unreachable!(),
                };

                let needs_change = match state {
                    "present" => !is_configured,
                    "absent" => is_configured,
                    _ => return Err(anyhow::anyhow!("invalid state: {}", state)),
                };

                if !needs_change {
                    return Ok(false);
                }

                if dry_run {
                    return Ok(true);
                }

                // Build and execute config commands.
                let commands: Vec<String> = match (device_type.as_str(), state) {
                    ("cisco_ios" | "cisco_nxos", "present") => {
                        let mut cmds = vec![
                            "configure terminal".to_string(),
                        ];
                        if let Some(ref sp) = syslog_port {
                            cmds.push(format!("logging host {} port {}", syslog_host, sp));
                        } else {
                            cmds.push(format!("logging host {}", syslog_host));
                        }
                        if let Some(ref fac) = facility {
                            cmds.push(format!("logging facility {}", fac));
                        }
                        cmds.push("end".to_string());
                        cmds.push("write memory".to_string());
                        cmds
                    }
                    ("cisco_ios" | "cisco_nxos", "absent") => {
                        vec![
                            "configure terminal".to_string(),
                            format!("no logging host {}", syslog_host),
                            "end".to_string(),
                            "write memory".to_string(),
                        ]
                    }
                    ("juniper_junos", "present") => {
                        let mut cmds = vec![
                            "configure".to_string(),
                            format!("set system syslog host {} any any", syslog_host),
                        ];
                        if let Some(ref sp) = syslog_port {
                            cmds.push(format!("set system syslog host {} port {}", syslog_host, sp));
                        }
                        cmds.push("commit".to_string());
                        cmds.push("exit".to_string());
                        cmds
                    }
                    ("juniper_junos", "absent") => {
                        vec![
                            "configure".to_string(),
                            format!("delete system syslog host {}", syslog_host),
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
                    syslog_host = syslog_host.as_str(),
                    state,
                    "syslog configuration applied"
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
    fn test_network_syslog_missing_params() {
        let executor = NetworkSyslogExecutor::new();
        let params = HashMap::new();
        let result = executor.execute(&params, false);
        assert!(result.error.is_some());
    }

    #[test]
    fn test_network_syslog_missing_syslog_host() {
        let executor = NetworkSyslogExecutor::new();
        let mut params = HashMap::new();
        params.insert("host".to_string(), "10.0.0.1".to_string());
        params.insert("username".to_string(), "admin".to_string());
        params.insert("credential".to_string(), "secret".to_string());
        params.insert("device_type".to_string(), "cisco_ios".to_string());
        let result = executor.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("syslog_host"));
    }
}
