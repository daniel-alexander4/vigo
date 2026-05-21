// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! network_interface executor — manages interface state and description on network devices over SSH.
//!
//! Params:
//! - host (required): auto-injected
//! - username (required): auto-injected
//! - credential (required): auto-injected
//! - port (optional): SSH port, default 22
//! - device_type (required): cisco_ios, cisco_nxos, juniper_junos, arista_eos, cisco_iosxr
//! - interface (required): Interface name (e.g., "GigabitEthernet0/1", "ge-0/0/1", "Ethernet1")
//! - description (optional): Interface description
//! - state (optional): present (default), up, down, or absent

use crate::executors::{Executor, ResourceResult};
use crate::executors::network_common::{self, require_param, SshParams, DEFAULT_TIMEOUT};
use std::collections::HashMap;
use std::time::Instant;
use tracing::debug;

pub struct NetworkInterfaceExecutor;

impl NetworkInterfaceExecutor {
    pub fn new() -> Self {
        Self
    }
}

struct InterfaceDialect {
    prompt_pattern: &'static str,
}

fn interface_dialect(device_type: &str) -> Option<InterfaceDialect> {
    match device_type {
        "cisco_ios" | "cisco_nxos" | "arista_eos" | "cisco_iosxr" => Some(InterfaceDialect {
            prompt_pattern: "#",
        }),
        "juniper_junos" => Some(InterfaceDialect {
            prompt_pattern: ">",
        }),
        _ => None,
    }
}

/// Build the check command for the given device type and interface name.
fn check_command(device_type: &str, interface: &str) -> String {
    match device_type {
        "cisco_ios" | "cisco_nxos" | "arista_eos" | "cisco_iosxr" => {
            format!("show running-config interface {}", interface)
        }
        "juniper_junos" => {
            format!("show configuration interfaces {}", interface)
        }
        _ => String::new(),
    }
}

/// Returns true if the interface configuration exists in the check output.
fn interface_present(device_type: &str, output: &str, interface: &str) -> bool {
    match device_type {
        "cisco_ios" | "cisco_nxos" | "arista_eos" | "cisco_iosxr" => {
            let needle = format!("interface {}", interface);
            output.lines().any(|line| line.trim().starts_with(&needle))
                || output.contains(&needle)
        }
        "juniper_junos" => {
            // Junos output will contain interface config if it exists
            !output.trim().is_empty()
                && !output.contains("error")
                && !output.contains("not found")
        }
        _ => false,
    }
}

/// Returns true if the interface is shut down in the check output.
fn interface_shutdown(device_type: &str, output: &str) -> bool {
    match device_type {
        "cisco_ios" | "cisco_nxos" | "arista_eos" | "cisco_iosxr" => {
            output.lines().any(|line| line.trim() == "shutdown")
        }
        "juniper_junos" => {
            output.contains("disable")
        }
        _ => false,
    }
}

/// Build the config commands to apply the desired interface state.
fn build_commands(
    device_type: &str,
    interface: &str,
    description: Option<&str>,
    state: &str,
) -> Vec<String> {
    match device_type {
        "cisco_ios" | "cisco_nxos" | "arista_eos" => {
            let mut commands = vec![
                "configure terminal".to_string(),
            ];
            match state {
                "absent" => {
                    commands.push(format!("no interface {}", interface));
                }
                _ => {
                    commands.push(format!("interface {}", interface));
                    if let Some(desc) = description {
                        commands.push(format!("description {}", desc));
                    }
                    match state {
                        "up" => commands.push("no shutdown".to_string()),
                        "down" => commands.push("shutdown".to_string()),
                        _ => {} // "present" — just ensure config exists
                    }
                }
            }
            commands.push("end".to_string());
            commands.push("write memory".to_string());
            commands
        }
        "cisco_iosxr" => {
            let mut commands = vec![
                "configure terminal".to_string(),
            ];
            match state {
                "absent" => {
                    commands.push(format!("no interface {}", interface));
                }
                _ => {
                    commands.push(format!("interface {}", interface));
                    if let Some(desc) = description {
                        commands.push(format!("description {}", desc));
                    }
                    match state {
                        "up" => commands.push("no shutdown".to_string()),
                        "down" => commands.push("shutdown".to_string()),
                        _ => {}
                    }
                }
            }
            commands.push("commit".to_string());
            commands.push("end".to_string());
            commands
        }
        "juniper_junos" => {
            let mut commands = vec![
                "configure".to_string(),
            ];
            match state {
                "absent" => {
                    commands.push(format!("delete interfaces {}", interface));
                }
                _ => {
                    if let Some(desc) = description {
                        commands.push(format!("set interfaces {} description \"{}\"", interface, desc));
                    }
                    match state {
                        "up" => commands.push(format!("delete interfaces {} disable", interface)),
                        "down" => commands.push(format!("set interfaces {} disable", interface)),
                        _ => {}
                    }
                }
            }
            commands.push("commit".to_string());
            commands.push("exit".to_string());
            commands
        }
        _ => vec![],
    }
}

impl Executor for NetworkInterfaceExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let mut result = ResourceResult::new(
            "network_interface",
            params.get("name").map_or("", |s| s),
        );

        let ssh = match SshParams::from_params(params, result) {
            Ok(s) => s,
            Err(r) => return *r,
        };
        result = ResourceResult::new("network_interface", params.get("name").map_or("", |s| s));
        let device_type = require_param!(params, "device_type", result);
        let interface = require_param!(params, "interface", result);
        let state = params.get("state").map_or("present", |s| s.as_str()).to_string();
        let description = params.get("description").cloned();
        let timeout = DEFAULT_TIMEOUT;

        let dialect = match interface_dialect(&device_type) {
            Some(d) => d,
            None => return result.with_error(format!("unsupported device_type: {}", device_type)),
        };

        if !["present", "up", "down", "absent"].contains(&state.as_str()) {
            return result.with_error(format!("invalid state: {} (must be present, up, down, or absent)", state));
        }

        let host_log = ssh.host.clone();
        network_common::run_ssh_executor(&mut result, start, &ssh, |session| {
            Box::pin(async move {
                let check_cmd = check_command(&device_type, &interface);
                let current = session.exec(&check_cmd, timeout).await?;
                let current_output = current.stdout;

                let is_present = interface_present(&device_type, &current_output, &interface);
                let is_shutdown = interface_shutdown(&device_type, &current_output);

                let needs_change = match state.as_str() {
                    "present" => !is_present,
                    "up" => !is_present || is_shutdown,
                    "down" => !is_present || !is_shutdown,
                    "absent" => is_present,
                    _ => return Err(anyhow::anyhow!("invalid state: {}", state)),
                };

                // Also check description drift for present/up/down states.
                let needs_change = needs_change || {
                    if state != "absent" {
                        if let Some(ref desc) = description {
                            !current_output.contains(desc)
                        } else {
                            false
                        }
                    } else {
                        false
                    }
                };

                if !needs_change {
                    return Ok(false);
                }
                if dry_run {
                    return Ok(true);
                }

                let commands = build_commands(
                    &device_type,
                    &interface,
                    description.as_deref(),
                    &state,
                );
                let cmd_refs: Vec<&str> = commands.iter().map(|s| s.as_str()).collect();
                session.exec_interactive(&cmd_refs, dialect.prompt_pattern, timeout).await?;

                debug!(
                    host = host_log.as_str(),
                    device_type = device_type.as_str(),
                    interface = interface.as_str(),
                    state = state.as_str(),
                    "interface configuration applied"
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
    fn test_interface_dialect_cisco() {
        let d = interface_dialect("cisco_ios").unwrap();
        assert_eq!(d.prompt_pattern, "#");
    }

    #[test]
    fn test_interface_dialect_nxos() {
        let d = interface_dialect("cisco_nxos").unwrap();
        assert_eq!(d.prompt_pattern, "#");
    }

    #[test]
    fn test_interface_dialect_arista_eos() {
        let d = interface_dialect("arista_eos").unwrap();
        assert_eq!(d.prompt_pattern, "#");
    }

    #[test]
    fn test_interface_dialect_iosxr() {
        let d = interface_dialect("cisco_iosxr").unwrap();
        assert_eq!(d.prompt_pattern, "#");
    }

    #[test]
    fn test_interface_dialect_junos() {
        let d = interface_dialect("juniper_junos").unwrap();
        assert_eq!(d.prompt_pattern, ">");
    }

    #[test]
    fn test_interface_dialect_unknown() {
        assert!(interface_dialect("unknown_vendor").is_none());
    }

    #[test]
    fn test_check_command_cisco() {
        assert_eq!(
            check_command("cisco_ios", "GigabitEthernet0/1"),
            "show running-config interface GigabitEthernet0/1"
        );
    }

    #[test]
    fn test_check_command_junos() {
        assert_eq!(
            check_command("juniper_junos", "ge-0/0/1"),
            "show configuration interfaces ge-0/0/1"
        );
    }

    #[test]
    fn test_interface_present_cisco() {
        let output = "interface GigabitEthernet0/1\n description Uplink\n no shutdown\n";
        assert!(interface_present("cisco_ios", output, "GigabitEthernet0/1"));
        assert!(!interface_present("cisco_ios", output, "GigabitEthernet0/2"));
    }

    #[test]
    fn test_interface_present_arista_eos() {
        let output = "interface Ethernet1\n description Server link\n";
        assert!(interface_present("arista_eos", output, "Ethernet1"));
    }

    #[test]
    fn test_interface_shutdown_cisco() {
        let output = "interface GigabitEthernet0/1\n shutdown\n";
        assert!(interface_shutdown("cisco_ios", output));

        let output_up = "interface GigabitEthernet0/1\n no shutdown\n description test\n";
        assert!(!interface_shutdown("cisco_ios", output_up));
    }

    #[test]
    fn test_interface_shutdown_junos() {
        let output = "description \"test\";\ndisable;\n";
        assert!(interface_shutdown("juniper_junos", output));

        let output_up = "description \"test\";\n";
        assert!(!interface_shutdown("juniper_junos", output_up));
    }

    #[test]
    fn test_build_commands_cisco_up() {
        let cmds = build_commands("cisco_ios", "GigabitEthernet0/1", Some("Uplink"), "up");
        assert_eq!(cmds, vec![
            "configure terminal",
            "interface GigabitEthernet0/1",
            "description Uplink",
            "no shutdown",
            "end",
            "write memory",
        ]);
    }

    #[test]
    fn test_build_commands_cisco_down() {
        let cmds = build_commands("cisco_nxos", "Ethernet1/1", None, "down");
        assert_eq!(cmds, vec![
            "configure terminal",
            "interface Ethernet1/1",
            "shutdown",
            "end",
            "write memory",
        ]);
    }

    #[test]
    fn test_build_commands_cisco_absent() {
        let cmds = build_commands("cisco_ios", "GigabitEthernet0/1", None, "absent");
        assert_eq!(cmds, vec![
            "configure terminal",
            "no interface GigabitEthernet0/1",
            "end",
            "write memory",
        ]);
    }

    #[test]
    fn test_build_commands_arista_eos_up() {
        let cmds = build_commands("arista_eos", "Ethernet1", Some("Server link"), "up");
        assert_eq!(cmds, vec![
            "configure terminal",
            "interface Ethernet1",
            "description Server link",
            "no shutdown",
            "end",
            "write memory",
        ]);
    }

    #[test]
    fn test_build_commands_iosxr_up() {
        let cmds = build_commands("cisco_iosxr", "GigabitEthernet0/0/0/0", Some("WAN"), "up");
        assert_eq!(cmds, vec![
            "configure terminal",
            "interface GigabitEthernet0/0/0/0",
            "description WAN",
            "no shutdown",
            "commit",
            "end",
        ]);
    }

    #[test]
    fn test_build_commands_iosxr_absent() {
        let cmds = build_commands("cisco_iosxr", "GigabitEthernet0/0/0/0", None, "absent");
        assert_eq!(cmds, vec![
            "configure terminal",
            "no interface GigabitEthernet0/0/0/0",
            "commit",
            "end",
        ]);
    }

    #[test]
    fn test_build_commands_junos_up() {
        let cmds = build_commands("juniper_junos", "ge-0/0/1", Some("Uplink"), "up");
        assert_eq!(cmds, vec![
            "configure",
            "set interfaces ge-0/0/1 description \"Uplink\"",
            "delete interfaces ge-0/0/1 disable",
            "commit",
            "exit",
        ]);
    }

    #[test]
    fn test_build_commands_junos_down() {
        let cmds = build_commands("juniper_junos", "ge-0/0/1", None, "down");
        assert_eq!(cmds, vec![
            "configure",
            "set interfaces ge-0/0/1 disable",
            "commit",
            "exit",
        ]);
    }

    #[test]
    fn test_build_commands_junos_absent() {
        let cmds = build_commands("juniper_junos", "ge-0/0/1", None, "absent");
        assert_eq!(cmds, vec![
            "configure",
            "delete interfaces ge-0/0/1",
            "commit",
            "exit",
        ]);
    }

    #[test]
    fn test_network_interface_missing_params() {
        let executor = NetworkInterfaceExecutor::new();
        let params = HashMap::new();
        let result = executor.execute(&params, false);
        assert!(result.error.is_some());
    }

    #[test]
    fn test_network_interface_missing_interface() {
        let executor = NetworkInterfaceExecutor::new();
        let mut params = HashMap::new();
        params.insert("host".to_string(), "10.0.0.1".to_string());
        params.insert("username".to_string(), "admin".to_string());
        params.insert("credential".to_string(), "secret".to_string());
        params.insert("device_type".to_string(), "cisco_ios".to_string());
        let result = executor.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("interface"));
    }

    #[test]
    fn test_network_interface_invalid_state() {
        let executor = NetworkInterfaceExecutor::new();
        let mut params = HashMap::new();
        params.insert("host".to_string(), "10.0.0.1".to_string());
        params.insert("username".to_string(), "admin".to_string());
        params.insert("credential".to_string(), "secret".to_string());
        params.insert("device_type".to_string(), "cisco_ios".to_string());
        params.insert("interface".to_string(), "GigabitEthernet0/1".to_string());
        params.insert("state".to_string(), "bogus".to_string());
        let result = executor.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("invalid state"));
    }
}
