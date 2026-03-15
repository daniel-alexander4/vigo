// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0

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

use crate::executors::{Executor, ResourceResult};
use crate::executors::network_common::{self, require_param, SshParams, DEFAULT_TIMEOUT};
use std::collections::HashMap;
use std::time::Instant;
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

        let ssh = match SshParams::from_params(params, result) {
            Ok(s) => s,
            Err(r) => return r,
        };
        result = ResourceResult::new("network_syslog", params.get("name").map_or("", |s| s));
        let device_type = require_param!(params, "device_type", result);
        let syslog_host = require_param!(params, "syslog_host", result);
        let state = network_common::parse_state(params).to_string();
        let syslog_port = params.get("syslog_port").cloned();
        let facility = params.get("facility").cloned();
        let timeout = DEFAULT_TIMEOUT;

        // Validate device_type early.
        let prompt_pattern = match device_type.as_str() {
            "cisco_ios" | "cisco_nxos" | "arista_eos" | "cisco_iosxr" => "#",
            "juniper_junos" => ">",
            _ => return result.with_error(format!("unsupported device_type: {}", device_type)),
        };

        let host_log = ssh.host.clone();
        network_common::run_ssh_executor(&mut result, start, &ssh, |session| {
            Box::pin(async move {
                // Check current syslog configuration.
                let is_configured = match device_type.as_str() {
                    "cisco_ios" | "cisco_nxos" | "arista_eos" | "cisco_iosxr" => {
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
                    _ => return Err(anyhow::anyhow!("unsupported device_type: {}", device_type)),
                };

                let needs_change = match state.as_str() {
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
                let commands: Vec<String> = match (device_type.as_str(), state.as_str()) {
                    ("cisco_ios" | "cisco_nxos" | "arista_eos", "present") => {
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
                    ("cisco_ios" | "cisco_nxos" | "arista_eos", "absent") => {
                        vec![
                            "configure terminal".to_string(),
                            format!("no logging host {}", syslog_host),
                            "end".to_string(),
                            "write memory".to_string(),
                        ]
                    }
                    ("cisco_iosxr", "present") => {
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
                        cmds.push("commit".to_string());
                        cmds.push("end".to_string());
                        cmds
                    }
                    ("cisco_iosxr", "absent") => {
                        vec![
                            "configure terminal".to_string(),
                            format!("no logging host {}", syslog_host),
                            "commit".to_string(),
                            "end".to_string(),
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
                    _ => return Err(anyhow::anyhow!("unsupported device_type/state combo: {}/{}", device_type, state)),
                };

                let cmd_refs: Vec<&str> = commands.iter().map(|s| s.as_str()).collect();
                let _output = session
                    .exec_interactive(&cmd_refs, prompt_pattern, timeout)
                    .await?;

                debug!(
                    host = host_log.as_str(),
                    device_type = device_type.as_str(),
                    syslog_host = syslog_host.as_str(),
                    state = state.as_str(),
                    "syslog configuration applied"
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
