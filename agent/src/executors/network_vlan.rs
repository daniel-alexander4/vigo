// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! network_vlan executor — manages VLANs on network devices over SSH.
//!
//! Params:
//! - host (required): auto-injected
//! - username (required): auto-injected
//! - credential (required): auto-injected
//! - port (optional): SSH port, default 22
//! - device_type (required): cisco_ios, cisco_nxos, juniper_junos, arista_eos, cisco_iosxr
//! - vlan (required): VLAN ID (1-4094)
//! - vlan_name (optional): VLAN name
//! - state (optional): present (default) or absent

use crate::executors::{Executor, ResourceResult};
use crate::executors::network_common::{self, require_param, SshParams, DEFAULT_TIMEOUT};
use std::collections::HashMap;
use std::time::Instant;
use tracing::debug;

pub struct NetworkVlanExecutor;

impl NetworkVlanExecutor {
    pub fn new() -> Self {
        Self
    }
}

struct VlanDialect {
    prompt_pattern: &'static str,
}

fn vlan_dialect(device_type: &str) -> Option<VlanDialect> {
    match device_type {
        "cisco_ios" | "cisco_nxos" | "arista_eos" | "cisco_iosxr" => Some(VlanDialect {
            prompt_pattern: "#",
        }),
        "juniper_junos" => Some(VlanDialect {
            prompt_pattern: ">",
        }),
        _ => None,
    }
}

/// Build the check command for the given device type and VLAN ID.
fn check_command(device_type: &str, vlan_id: &str) -> String {
    match device_type {
        "cisco_ios" | "cisco_nxos" | "arista_eos" => {
            format!("show running-config | include ^vlan {}", vlan_id)
        }
        "cisco_iosxr" => {
            format!("show running-config vlan {}", vlan_id)
        }
        "juniper_junos" => {
            "show configuration vlans".to_string()
        }
        _ => String::new(),
    }
}

/// Returns true if the VLAN is present in the check output.
fn vlan_present(device_type: &str, output: &str, vlan_id: &str) -> bool {
    match device_type {
        "cisco_ios" | "cisco_nxos" | "arista_eos" => {
            let needle = format!("vlan {}", vlan_id);
            output.lines().any(|line| line.trim().starts_with(&needle) || line.trim() == needle)
        }
        "cisco_iosxr" => {
            let needle = format!("vlan {}", vlan_id);
            output.contains(&needle)
        }
        "juniper_junos" => {
            let needle = format!("vlan-id {}", vlan_id);
            output.contains(&needle)
        }
        _ => false,
    }
}

/// Build the config commands for VLAN management.
fn build_commands(
    device_type: &str,
    vlan_id: &str,
    vlan_name: Option<&str>,
    state: &str,
) -> Vec<String> {
    match device_type {
        "cisco_ios" | "cisco_nxos" | "arista_eos" => {
            let mut commands = vec!["configure terminal".to_string()];
            match state {
                "absent" => {
                    commands.push(format!("no vlan {}", vlan_id));
                }
                _ => {
                    commands.push(format!("vlan {}", vlan_id));
                    if let Some(name) = vlan_name {
                        commands.push(format!("name {}", name));
                    }
                }
            }
            commands.push("end".to_string());
            commands.push("write memory".to_string());
            commands
        }
        "cisco_iosxr" => {
            let mut commands = vec!["configure terminal".to_string()];
            match state {
                "absent" => {
                    commands.push(format!("no vlan {}", vlan_id));
                }
                _ => {
                    commands.push(format!("vlan {}", vlan_id));
                    if let Some(name) = vlan_name {
                        commands.push(format!("name {}", name));
                    }
                }
            }
            commands.push("commit".to_string());
            commands.push("end".to_string());
            commands
        }
        "juniper_junos" => {
            let mut commands = vec!["configure".to_string()];
            // Junos requires a name for VLANs; use vlan_name or fallback to "vlan<id>".
            let junos_name = vlan_name.unwrap_or("").to_string();
            let junos_name = if junos_name.is_empty() {
                format!("vlan{}", vlan_id)
            } else {
                junos_name
            };
            match state {
                "absent" => {
                    commands.push(format!("delete vlans {}", junos_name));
                }
                _ => {
                    commands.push(format!("set vlans {} vlan-id {}", junos_name, vlan_id));
                }
            }
            commands.push("commit".to_string());
            commands.push("exit".to_string());
            commands
        }
        _ => vec![],
    }
}

impl Executor for NetworkVlanExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let mut result = ResourceResult::new(
            "network_vlan",
            params.get("name").map_or("", |s| s),
        );

        let ssh = match SshParams::from_params(params, result) {
            Ok(s) => s,
            Err(r) => return *r,
        };
        result = ResourceResult::new("network_vlan", params.get("name").map_or("", |s| s));
        let device_type = require_param!(params, "device_type", result);
        let vlan_id = require_param!(params, "vlan", result);
        let state = network_common::parse_state(params).to_string();
        let vlan_name = params.get("vlan_name").cloned();
        let timeout = DEFAULT_TIMEOUT;

        let dialect = match vlan_dialect(&device_type) {
            Some(d) => d,
            None => return result.with_error(format!("unsupported device_type: {}", device_type)),
        };

        // Validate VLAN ID range.
        match vlan_id.parse::<u16>() {
            Ok(id) if (1..=4094).contains(&id) => {}
            _ => return result.with_error(format!("invalid vlan: {} (must be 1-4094)", vlan_id)),
        }

        let host_log = ssh.host.clone();
        network_common::run_ssh_executor(&mut result, start, &ssh, |session| {
            Box::pin(async move {
                let check_cmd = check_command(&device_type, &vlan_id);
                let current = session.exec(&check_cmd, timeout).await?;
                let is_present = vlan_present(&device_type, &current.stdout, &vlan_id);

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

                let commands = build_commands(
                    &device_type,
                    &vlan_id,
                    vlan_name.as_deref(),
                    &state,
                );
                let cmd_refs: Vec<&str> = commands.iter().map(|s| s.as_str()).collect();
                session.exec_interactive(&cmd_refs, dialect.prompt_pattern, timeout).await?;

                debug!(
                    host = host_log.as_str(),
                    device_type = device_type.as_str(),
                    vlan_id = vlan_id.as_str(),
                    state = state.as_str(),
                    "VLAN configuration applied"
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
    fn test_vlan_dialect_cisco() {
        let d = vlan_dialect("cisco_ios").unwrap();
        assert_eq!(d.prompt_pattern, "#");
    }

    #[test]
    fn test_vlan_dialect_arista_eos() {
        let d = vlan_dialect("arista_eos").unwrap();
        assert_eq!(d.prompt_pattern, "#");
    }

    #[test]
    fn test_vlan_dialect_iosxr() {
        let d = vlan_dialect("cisco_iosxr").unwrap();
        assert_eq!(d.prompt_pattern, "#");
    }

    #[test]
    fn test_vlan_dialect_junos() {
        let d = vlan_dialect("juniper_junos").unwrap();
        assert_eq!(d.prompt_pattern, ">");
    }

    #[test]
    fn test_vlan_dialect_unknown() {
        assert!(vlan_dialect("unknown_vendor").is_none());
    }

    #[test]
    fn test_check_command_cisco() {
        assert_eq!(check_command("cisco_ios", "100"), "show running-config | include ^vlan 100");
    }

    #[test]
    fn test_check_command_iosxr() {
        assert_eq!(check_command("cisco_iosxr", "100"), "show running-config vlan 100");
    }

    #[test]
    fn test_check_command_junos() {
        assert_eq!(check_command("juniper_junos", "100"), "show configuration vlans");
    }

    #[test]
    fn test_vlan_present_cisco() {
        let output = "vlan 100\nvlan 200\n";
        assert!(vlan_present("cisco_ios", output, "100"));
        assert!(!vlan_present("cisco_ios", output, "300"));
    }

    #[test]
    fn test_vlan_present_arista_eos() {
        let output = "vlan 100\n";
        assert!(vlan_present("arista_eos", output, "100"));
        assert!(!vlan_present("arista_eos", output, "200"));
    }

    #[test]
    fn test_vlan_present_iosxr() {
        let output = "vlan 100\n name servers\n";
        assert!(vlan_present("cisco_iosxr", output, "100"));
        assert!(!vlan_present("cisco_iosxr", output, "200"));
    }

    #[test]
    fn test_vlan_present_junos() {
        let output = "servers {\n    vlan-id 100;\n}\n";
        assert!(vlan_present("juniper_junos", output, "100"));
        assert!(!vlan_present("juniper_junos", output, "200"));
    }

    #[test]
    fn test_build_commands_cisco_present() {
        let cmds = build_commands("cisco_ios", "100", Some("servers"), "present");
        assert_eq!(cmds, vec![
            "configure terminal",
            "vlan 100",
            "name servers",
            "end",
            "write memory",
        ]);
    }

    #[test]
    fn test_build_commands_cisco_absent() {
        let cmds = build_commands("cisco_nxos", "100", None, "absent");
        assert_eq!(cmds, vec![
            "configure terminal",
            "no vlan 100",
            "end",
            "write memory",
        ]);
    }

    #[test]
    fn test_build_commands_arista_eos_present() {
        let cmds = build_commands("arista_eos", "200", Some("mgmt"), "present");
        assert_eq!(cmds, vec![
            "configure terminal",
            "vlan 200",
            "name mgmt",
            "end",
            "write memory",
        ]);
    }

    #[test]
    fn test_build_commands_iosxr_present() {
        let cmds = build_commands("cisco_iosxr", "100", Some("servers"), "present");
        assert_eq!(cmds, vec![
            "configure terminal",
            "vlan 100",
            "name servers",
            "commit",
            "end",
        ]);
    }

    #[test]
    fn test_build_commands_iosxr_absent() {
        let cmds = build_commands("cisco_iosxr", "100", None, "absent");
        assert_eq!(cmds, vec![
            "configure terminal",
            "no vlan 100",
            "commit",
            "end",
        ]);
    }

    #[test]
    fn test_build_commands_junos_present() {
        let cmds = build_commands("juniper_junos", "100", Some("servers"), "present");
        assert_eq!(cmds, vec![
            "configure",
            "set vlans servers vlan-id 100",
            "commit",
            "exit",
        ]);
    }

    #[test]
    fn test_build_commands_junos_present_no_name() {
        let cmds = build_commands("juniper_junos", "100", None, "present");
        assert_eq!(cmds, vec![
            "configure",
            "set vlans vlan100 vlan-id 100",
            "commit",
            "exit",
        ]);
    }

    #[test]
    fn test_build_commands_junos_absent() {
        let cmds = build_commands("juniper_junos", "100", Some("servers"), "absent");
        assert_eq!(cmds, vec![
            "configure",
            "delete vlans servers",
            "commit",
            "exit",
        ]);
    }

    #[test]
    fn test_network_vlan_missing_params() {
        let executor = NetworkVlanExecutor::new();
        let params = HashMap::new();
        let result = executor.execute(&params, false);
        assert!(result.error.is_some());
    }

    #[test]
    fn test_network_vlan_missing_vlan() {
        let executor = NetworkVlanExecutor::new();
        let mut params = HashMap::new();
        params.insert("host".to_string(), "10.0.0.1".to_string());
        params.insert("username".to_string(), "admin".to_string());
        params.insert("credential".to_string(), "secret".to_string());
        params.insert("device_type".to_string(), "cisco_ios".to_string());
        let result = executor.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("vlan"));
    }

    #[test]
    fn test_network_vlan_invalid_id() {
        let executor = NetworkVlanExecutor::new();
        let mut params = HashMap::new();
        params.insert("host".to_string(), "10.0.0.1".to_string());
        params.insert("username".to_string(), "admin".to_string());
        params.insert("credential".to_string(), "secret".to_string());
        params.insert("device_type".to_string(), "cisco_ios".to_string());
        params.insert("vlan".to_string(), "9999".to_string());
        let result = executor.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("invalid vlan"));
    }
}
