// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! network_ntp executor — manages NTP server configuration on network devices over SSH.
//!
//! Params:
//! - host (required): auto-injected
//! - credential (required): auto-injected
//! - username (required): auto-injected
//! - port (optional): SSH port, default 22
//! - device_type (required): cisco_ios, cisco_nxos, juniper_junos
//! - server (required): NTP server address to configure
//! - state (optional): present (default) or absent

use crate::executors::{Executor, ResourceResult};
use crate::executors::network_common::{self, require_param, SshParams, DEFAULT_TIMEOUT};
use std::collections::HashMap;
use std::time::Instant;
use tracing::debug;

pub struct NetworkNtpExecutor;

impl NetworkNtpExecutor {
    pub fn new() -> Self {
        Self
    }
}

/// Returns the check command and prompt pattern for a given device type.
struct NtpDialect {
    check_command: &'static str,
    prompt_pattern: &'static str,
}

fn ntp_dialect(device_type: &str) -> Option<NtpDialect> {
    match device_type {
        "cisco_ios" | "cisco_nxos" => Some(NtpDialect {
            check_command: "show running-config | include ntp server",
            prompt_pattern: "#",
        }),
        "juniper_junos" => Some(NtpDialect {
            check_command: "show configuration system ntp",
            prompt_pattern: ">",
        }),
        _ => None,
    }
}

/// Returns true if the NTP server is present in the check output.
fn ntp_server_present(device_type: &str, output: &str, server: &str) -> bool {
    match device_type {
        "cisco_ios" | "cisco_nxos" => {
            let needle = format!("ntp server {}", server);
            output.lines().any(|line| line.trim().contains(&needle))
        }
        "juniper_junos" => {
            let needle = format!("server {}", server);
            output.lines().any(|line| line.trim().contains(&needle))
        }
        _ => false,
    }
}

/// Builds the config push commands for a given device type, server, and state.
fn build_ntp_commands(device_type: &str, server: &str, state: &str) -> Vec<String> {
    match device_type {
        "cisco_ios" | "cisco_nxos" => {
            let ntp_line = if state == "absent" {
                format!("no ntp server {}", server)
            } else {
                format!("ntp server {}", server)
            };
            vec![
                "configure terminal".to_string(),
                ntp_line,
                "end".to_string(),
                "write memory".to_string(),
            ]
        }
        "juniper_junos" => {
            let ntp_line = if state == "absent" {
                format!("delete system ntp server {}", server)
            } else {
                format!("set system ntp server {}", server)
            };
            vec![
                "configure".to_string(),
                ntp_line,
                "commit".to_string(),
                "exit".to_string(),
            ]
        }
        _ => vec![],
    }
}

impl Executor for NetworkNtpExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let mut result = ResourceResult::new(
            "network_ntp",
            params.get("name").map_or("", |s| s),
        );

        let ssh = match SshParams::from_params(params, result) {
            Ok(s) => s,
            Err(r) => return r,
        };
        result = ResourceResult::new("network_ntp", params.get("name").map_or("", |s| s));
        let device_type = require_param!(params, "device_type", result);
        let server = require_param!(params, "server", result);
        let state = network_common::parse_state(params).to_string();
        let timeout = DEFAULT_TIMEOUT;

        let dialect = match ntp_dialect(&device_type) {
            Some(d) => d,
            None => return result.with_error(format!("unsupported device_type: {}", device_type)),
        };

        let host_log = ssh.host.clone();
        network_common::run_ssh_executor(&mut result, start, &ssh, |session| {
            Box::pin(async move {
                let current = session.exec(dialect.check_command, timeout).await?;
                let is_present = ntp_server_present(&device_type, &current.stdout, &server);

                let needs_change = match state.as_str() {
                    "present" => !is_present,
                    "absent" => is_present,
                    _ => return Err(anyhow::anyhow!("invalid state: {}", state)),
                };

                if !needs_change {
                    return Ok(false);
                }
                if dry_run {
                    return Ok(true);
                }

                let commands = build_ntp_commands(&device_type, &server, &state);
                let cmd_refs: Vec<&str> = commands.iter().map(|s| s.as_str()).collect();
                session.exec_interactive(&cmd_refs, dialect.prompt_pattern, timeout).await?;

                debug!(
                    host = host_log.as_str(),
                    device_type = device_type.as_str(),
                    server = server.as_str(),
                    state = state.as_str(),
                    "ntp configuration applied"
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
    fn test_ntp_dialect_cisco() {
        let d = ntp_dialect("cisco_ios").unwrap();
        assert_eq!(d.check_command, "show running-config | include ntp server");
        assert_eq!(d.prompt_pattern, "#");
    }

    #[test]
    fn test_ntp_dialect_nxos() {
        let d = ntp_dialect("cisco_nxos").unwrap();
        assert_eq!(d.prompt_pattern, "#");
    }

    #[test]
    fn test_ntp_dialect_junos() {
        let d = ntp_dialect("juniper_junos").unwrap();
        assert_eq!(d.check_command, "show configuration system ntp");
        assert_eq!(d.prompt_pattern, ">");
    }

    #[test]
    fn test_ntp_dialect_unknown() {
        assert!(ntp_dialect("arista_eos").is_none());
    }

    #[test]
    fn test_ntp_server_present_cisco() {
        let output = "ntp server 10.0.0.1\nntp server 10.0.0.2\n";
        assert!(ntp_server_present("cisco_ios", output, "10.0.0.1"));
        assert!(!ntp_server_present("cisco_ios", output, "10.0.0.3"));
    }

    #[test]
    fn test_ntp_server_present_junos() {
        let output = "server 10.0.0.1;\nserver 10.0.0.2;\n";
        assert!(ntp_server_present("juniper_junos", output, "10.0.0.1"));
        assert!(!ntp_server_present("juniper_junos", output, "10.0.0.3"));
    }

    #[test]
    fn test_build_ntp_commands_cisco_present() {
        let cmds = build_ntp_commands("cisco_ios", "10.0.0.1", "present");
        assert_eq!(cmds, vec![
            "configure terminal",
            "ntp server 10.0.0.1",
            "end",
            "write memory",
        ]);
    }

    #[test]
    fn test_build_ntp_commands_cisco_absent() {
        let cmds = build_ntp_commands("cisco_nxos", "10.0.0.1", "absent");
        assert_eq!(cmds, vec![
            "configure terminal",
            "no ntp server 10.0.0.1",
            "end",
            "write memory",
        ]);
    }

    #[test]
    fn test_build_ntp_commands_junos_present() {
        let cmds = build_ntp_commands("juniper_junos", "10.0.0.1", "present");
        assert_eq!(cmds, vec![
            "configure",
            "set system ntp server 10.0.0.1",
            "commit",
            "exit",
        ]);
    }

    #[test]
    fn test_build_ntp_commands_junos_absent() {
        let cmds = build_ntp_commands("juniper_junos", "10.0.0.1", "absent");
        assert_eq!(cmds, vec![
            "configure",
            "delete system ntp server 10.0.0.1",
            "commit",
            "exit",
        ]);
    }

    #[test]
    fn test_network_ntp_missing_params() {
        let executor = NetworkNtpExecutor::new();
        let params = HashMap::new();
        let result = executor.execute(&params, false);
        assert!(result.error.is_some());
    }

    #[test]
    fn test_network_ntp_missing_server() {
        let executor = NetworkNtpExecutor::new();
        let mut params = HashMap::new();
        params.insert("host".to_string(), "10.0.0.1".to_string());
        params.insert("username".to_string(), "admin".to_string());
        params.insert("credential".to_string(), "secret".to_string());
        params.insert("device_type".to_string(), "cisco_ios".to_string());
        let result = executor.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("server"));
    }
}
