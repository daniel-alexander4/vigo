// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! network_acl executor — manages access control list entries on network devices over SSH.
//!
//! Params:
//! - host (required): auto-injected
//! - username (required): auto-injected
//! - credential (required): auto-injected
//! - port (optional): SSH port, default 22
//! - device_type (required): cisco_ios, cisco_nxos, juniper_junos, arista_eos, cisco_iosxr
//! - acl (required): ACL name
//! - entry (required): ACL entry (e.g., "permit ip 10.0.0.0 0.255.255.255 any")
//! - state (optional): present (default) or absent

use crate::executors::{Executor, ResourceResult};
use crate::executors::network_common::{self, require_param, SshParams, DEFAULT_TIMEOUT};
use std::collections::HashMap;
use std::time::Instant;
use tracing::debug;

pub struct NetworkAclExecutor;

impl NetworkAclExecutor {
    pub fn new() -> Self {
        Self
    }
}

struct AclDialect {
    prompt_pattern: &'static str,
}

fn acl_dialect(device_type: &str) -> Option<AclDialect> {
    match device_type {
        "cisco_ios" | "cisco_nxos" | "arista_eos" | "cisco_iosxr" => Some(AclDialect {
            prompt_pattern: "#",
        }),
        "juniper_junos" => Some(AclDialect {
            prompt_pattern: ">",
        }),
        _ => None,
    }
}

/// Build the check command to inspect ACL configuration.
fn check_command(device_type: &str, acl_name: &str) -> String {
    match device_type {
        "cisco_ios" | "arista_eos" => {
            format!("show running-config | section access-list {}", acl_name)
        }
        "cisco_nxos" => {
            format!("show running-config | section ip access-list {}", acl_name)
        }
        "cisco_iosxr" => {
            format!("show running-config ipv4 access-list {}", acl_name)
        }
        "juniper_junos" => {
            "show configuration firewall".to_string()
        }
        _ => String::new(),
    }
}

/// Returns true if the ACL entry is present in the check output.
fn acl_entry_present(device_type: &str, output: &str, entry: &str) -> bool {
    match device_type {
        "cisco_ios" | "cisco_nxos" | "arista_eos" | "cisco_iosxr" => {
            output.lines().any(|line| line.trim().contains(entry))
        }
        "juniper_junos" => {
            // For Junos, the entry is a set command fragment — check if the
            // essential parts appear in the config.
            output.contains(entry)
        }
        _ => false,
    }
}

/// Build config commands for ACL management.
fn build_commands(
    device_type: &str,
    acl_name: &str,
    entry: &str,
    state: &str,
) -> Vec<String> {
    match device_type {
        "cisco_ios" | "cisco_nxos" | "arista_eos" => {
            let mut commands = vec![
                "configure terminal".to_string(),
                format!("ip access-list extended {}", acl_name),
            ];
            match state {
                "absent" => commands.push(format!("no {}", entry)),
                _ => commands.push(entry.to_string()),
            }
            commands.push("end".to_string());
            commands.push("write memory".to_string());
            commands
        }
        "cisco_iosxr" => {
            let mut commands = vec![
                "configure terminal".to_string(),
                format!("ipv4 access-list {}", acl_name),
            ];
            match state {
                "absent" => commands.push(format!("no {}", entry)),
                _ => commands.push(entry.to_string()),
            }
            commands.push("commit".to_string());
            commands.push("end".to_string());
            commands
        }
        "juniper_junos" => {
            let mut commands = vec!["configure".to_string()];
            match state {
                "absent" => {
                    commands.push(format!("delete firewall filter {} {}", acl_name, entry));
                }
                _ => {
                    commands.push(format!("set firewall filter {} {}", acl_name, entry));
                }
            }
            commands.push("commit".to_string());
            commands.push("exit".to_string());
            commands
        }
        _ => vec![],
    }
}

impl Executor for NetworkAclExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let mut result = ResourceResult::new(
            "network_acl",
            params.get("name").map_or("", |s| s),
        );

        let ssh = match SshParams::from_params(params, result) {
            Ok(s) => s,
            Err(r) => return *r,
        };
        result = ResourceResult::new("network_acl", params.get("name").map_or("", |s| s));
        let device_type = require_param!(params, "device_type", result);
        let acl_name = require_param!(params, "acl", result);
        let entry = require_param!(params, "entry", result);
        let state = network_common::parse_state(params).to_string();
        let timeout = DEFAULT_TIMEOUT;

        let dialect = match acl_dialect(&device_type) {
            Some(d) => d,
            None => return result.with_error(format!("unsupported device_type: {}", device_type)),
        };

        let host_log = ssh.host.clone();
        network_common::run_ssh_executor(&mut result, start, &ssh, |session| {
            Box::pin(async move {
                let check_cmd = check_command(&device_type, &acl_name);
                let current = session.exec(&check_cmd, timeout).await?;
                let is_present = acl_entry_present(&device_type, &current.stdout, &entry);

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

                let commands = build_commands(&device_type, &acl_name, &entry, &state);
                let cmd_refs: Vec<&str> = commands.iter().map(|s| s.as_str()).collect();
                session.exec_interactive(&cmd_refs, dialect.prompt_pattern, timeout).await?;

                debug!(
                    host = host_log.as_str(),
                    device_type = device_type.as_str(),
                    acl = acl_name.as_str(),
                    state = state.as_str(),
                    "ACL configuration applied"
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
    fn test_acl_dialect_cisco() {
        let d = acl_dialect("cisco_ios").unwrap();
        assert_eq!(d.prompt_pattern, "#");
    }

    #[test]
    fn test_acl_dialect_arista_eos() {
        let d = acl_dialect("arista_eos").unwrap();
        assert_eq!(d.prompt_pattern, "#");
    }

    #[test]
    fn test_acl_dialect_iosxr() {
        let d = acl_dialect("cisco_iosxr").unwrap();
        assert_eq!(d.prompt_pattern, "#");
    }

    #[test]
    fn test_acl_dialect_junos() {
        let d = acl_dialect("juniper_junos").unwrap();
        assert_eq!(d.prompt_pattern, ">");
    }

    #[test]
    fn test_acl_dialect_unknown() {
        assert!(acl_dialect("unknown_vendor").is_none());
    }

    #[test]
    fn test_check_command_cisco_ios() {
        assert_eq!(
            check_command("cisco_ios", "BLOCK_SSH"),
            "show running-config | section access-list BLOCK_SSH"
        );
    }

    #[test]
    fn test_check_command_nxos() {
        assert_eq!(
            check_command("cisco_nxos", "BLOCK_SSH"),
            "show running-config | section ip access-list BLOCK_SSH"
        );
    }

    #[test]
    fn test_check_command_arista_eos() {
        assert_eq!(
            check_command("arista_eos", "BLOCK_SSH"),
            "show running-config | section access-list BLOCK_SSH"
        );
    }

    #[test]
    fn test_check_command_iosxr() {
        assert_eq!(
            check_command("cisco_iosxr", "BLOCK_SSH"),
            "show running-config ipv4 access-list BLOCK_SSH"
        );
    }

    #[test]
    fn test_check_command_junos() {
        assert_eq!(check_command("juniper_junos", "BLOCK_SSH"), "show configuration firewall");
    }

    #[test]
    fn test_acl_entry_present_cisco() {
        let output = " permit ip 10.0.0.0 0.255.255.255 any\n deny tcp any any eq 22\n";
        assert!(acl_entry_present("cisco_ios", output, "permit ip 10.0.0.0 0.255.255.255 any"));
        assert!(!acl_entry_present("cisco_ios", output, "permit ip 192.168.0.0 0.0.255.255 any"));
    }

    #[test]
    fn test_acl_entry_present_iosxr() {
        let output = " 10 permit ipv4 10.0.0.0/8 any\n";
        assert!(acl_entry_present("cisco_iosxr", output, "10 permit ipv4 10.0.0.0/8 any"));
    }

    #[test]
    fn test_build_commands_cisco_present() {
        let cmds = build_commands("cisco_ios", "BLOCK_SSH", "deny tcp any any eq 22", "present");
        assert_eq!(cmds, vec![
            "configure terminal",
            "ip access-list extended BLOCK_SSH",
            "deny tcp any any eq 22",
            "end",
            "write memory",
        ]);
    }

    #[test]
    fn test_build_commands_cisco_absent() {
        let cmds = build_commands("cisco_ios", "BLOCK_SSH", "deny tcp any any eq 22", "absent");
        assert_eq!(cmds, vec![
            "configure terminal",
            "ip access-list extended BLOCK_SSH",
            "no deny tcp any any eq 22",
            "end",
            "write memory",
        ]);
    }

    #[test]
    fn test_build_commands_arista_eos_present() {
        let cmds = build_commands("arista_eos", "ALLOW_WEB", "permit tcp any any eq 80", "present");
        assert_eq!(cmds, vec![
            "configure terminal",
            "ip access-list extended ALLOW_WEB",
            "permit tcp any any eq 80",
            "end",
            "write memory",
        ]);
    }

    #[test]
    fn test_build_commands_iosxr_present() {
        let cmds = build_commands("cisco_iosxr", "BLOCK_SSH", "10 deny tcp any any eq 22", "present");
        assert_eq!(cmds, vec![
            "configure terminal",
            "ipv4 access-list BLOCK_SSH",
            "10 deny tcp any any eq 22",
            "commit",
            "end",
        ]);
    }

    #[test]
    fn test_build_commands_iosxr_absent() {
        let cmds = build_commands("cisco_iosxr", "BLOCK_SSH", "10 deny tcp any any eq 22", "absent");
        assert_eq!(cmds, vec![
            "configure terminal",
            "ipv4 access-list BLOCK_SSH",
            "no 10 deny tcp any any eq 22",
            "commit",
            "end",
        ]);
    }

    #[test]
    fn test_build_commands_junos_present() {
        let entry = "term BLOCK_SSH from protocol tcp destination-port 22";
        let cmds = build_commands("juniper_junos", "PROTECT", entry, "present");
        assert_eq!(cmds[0], "configure");
        assert_eq!(cmds[1], format!("set firewall filter PROTECT {}", entry));
        assert_eq!(cmds[2], "commit");
        assert_eq!(cmds[3], "exit");
        assert_eq!(cmds.len(), 4);
    }

    #[test]
    fn test_build_commands_junos_absent() {
        let entry = "term BLOCK_SSH";
        let cmds = build_commands("juniper_junos", "PROTECT", entry, "absent");
        assert_eq!(cmds[0], "configure");
        assert_eq!(cmds[1], "delete firewall filter PROTECT term BLOCK_SSH");
        assert_eq!(cmds[2], "commit");
        assert_eq!(cmds[3], "exit");
        assert_eq!(cmds.len(), 4);
    }

    #[test]
    fn test_network_acl_missing_params() {
        let executor = NetworkAclExecutor::new();
        let params = HashMap::new();
        let result = executor.execute(&params, false);
        assert!(result.error.is_some());
    }

    #[test]
    fn test_network_acl_missing_acl() {
        let executor = NetworkAclExecutor::new();
        let mut params = HashMap::new();
        params.insert("host".to_string(), "10.0.0.1".to_string());
        params.insert("username".to_string(), "admin".to_string());
        params.insert("credential".to_string(), "secret".to_string());
        params.insert("device_type".to_string(), "cisco_ios".to_string());
        let result = executor.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("acl"));
    }

    #[test]
    fn test_network_acl_missing_entry() {
        let executor = NetworkAclExecutor::new();
        let mut params = HashMap::new();
        params.insert("host".to_string(), "10.0.0.1".to_string());
        params.insert("username".to_string(), "admin".to_string());
        params.insert("credential".to_string(), "secret".to_string());
        params.insert("device_type".to_string(), "cisco_ios".to_string());
        params.insert("acl".to_string(), "BLOCK_SSH".to_string());
        let result = executor.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("entry"));
    }
}
