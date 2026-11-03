// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! network_static_route executor — manages static routes on network devices over SSH.
//!
//! Params:
//! - host (required): auto-injected
//! - username (required): auto-injected
//! - credential (required): auto-injected
//! - port (optional): SSH port, default 22
//! - device_type (required): cisco_ios, cisco_nxos, juniper_junos, arista_eos, cisco_iosxr
//! - network (required): Destination network (e.g., "10.0.0.0/8" or "10.0.0.0 255.0.0.0")
//! - next_hop (required): Next-hop IP or interface
//! - name (optional): Route name/tag (IOS-XR/Junos)
//! - state (optional): present (default) or absent

use crate::executors::{Executor, ResourceResult};
use crate::executors::network_common::{self, require_param, SshParams, DEFAULT_TIMEOUT};
use std::collections::HashMap;
use std::time::Instant;
use tracing::debug;

pub struct NetworkStaticRouteExecutor;

impl NetworkStaticRouteExecutor {
    pub fn new() -> Self {
        Self
    }
}

struct RouteDialect {
    prompt_pattern: &'static str,
}

fn route_dialect(device_type: &str) -> Option<RouteDialect> {
    match device_type {
        "cisco_ios" | "cisco_nxos" | "arista_eos" | "cisco_iosxr" => Some(RouteDialect {
            prompt_pattern: "#",
        }),
        "juniper_junos" => Some(RouteDialect {
            prompt_pattern: ">",
        }),
        _ => None,
    }
}

/// Parse CIDR notation (e.g., "10.0.0.0/8") into network and mask (e.g., "10.0.0.0", "255.0.0.0").
/// If already in "network mask" format, returns as-is split.
fn parse_network(network: &str) -> (String, String) {
    if let Some(slash_pos) = network.find('/') {
        let ip = &network[..slash_pos];
        let prefix_len: u8 = network[slash_pos + 1..].parse().unwrap_or(32);
        let mask = prefix_to_mask(prefix_len);
        (ip.to_string(), mask)
    } else if network.contains(' ') {
        // Already "network mask" format.
        let parts: Vec<&str> = network.splitn(2, ' ').collect();
        (parts[0].to_string(), parts.get(1).unwrap_or(&"255.255.255.255").to_string())
    } else {
        // Host route.
        (network.to_string(), "255.255.255.255".to_string())
    }
}

/// Convert a prefix length to a dotted-decimal subnet mask.
fn prefix_to_mask(prefix: u8) -> String {
    let mask: u32 = if prefix == 0 {
        0
    } else {
        !0u32 << (32 - prefix)
    };
    format!(
        "{}.{}.{}.{}",
        (mask >> 24) & 0xFF,
        (mask >> 16) & 0xFF,
        (mask >> 8) & 0xFF,
        mask & 0xFF
    )
}

/// Build the check command for the given device type.
fn check_command(device_type: &str, network: &str) -> String {
    let (net_addr, _) = parse_network(network);
    match device_type {
        "cisco_ios" | "arista_eos" => {
            format!("show running-config | include ip route {}", net_addr)
        }
        "cisco_nxos" => {
            "show running-config | include ip route".to_string()
        }
        "cisco_iosxr" => {
            "show running-config router static".to_string()
        }
        "juniper_junos" => {
            "show configuration routing-options".to_string()
        }
        _ => String::new(),
    }
}

/// Returns true if the static route is present in the check output.
fn route_present(device_type: &str, output: &str, network: &str, next_hop: &str) -> bool {
    let (net_addr, mask) = parse_network(network);
    match device_type {
        "cisco_ios" | "cisco_nxos" | "arista_eos" => {
            let needle = format!("ip route {} {} {}", net_addr, mask, next_hop);
            output.lines().any(|line| line.trim().contains(&needle))
        }
        "cisco_iosxr" => {
            // IOS-XR uses CIDR notation in show output.
            output.contains(&net_addr) && output.contains(next_hop)
        }
        "juniper_junos" => {
            // Check for both network and next-hop in the output.
            output.contains(network) && output.contains(next_hop)
        }
        _ => false,
    }
}

/// Build config commands for static route management.
fn build_commands(
    device_type: &str,
    network: &str,
    next_hop: &str,
    _name: Option<&str>,
    state: &str,
) -> Vec<String> {
    let (net_addr, mask) = parse_network(network);
    match device_type {
        "cisco_ios" | "cisco_nxos" | "arista_eos" => {
            let route_line = if state == "absent" {
                format!("no ip route {} {} {}", net_addr, mask, next_hop)
            } else {
                format!("ip route {} {} {}", net_addr, mask, next_hop)
            };
            vec![
                "configure terminal".to_string(),
                route_line,
                "end".to_string(),
                "write memory".to_string(),
            ]
        }
        "cisco_iosxr" => {
            let mut commands = vec![
                "configure terminal".to_string(),
                "router static".to_string(),
                "address-family ipv4 unicast".to_string(),
            ];
            if state == "absent" {
                commands.push(format!("no {} {}", network, next_hop));
            } else {
                commands.push(format!("{} {}", network, next_hop));
            }
            commands.push("commit".to_string());
            commands.push("end".to_string());
            commands
        }
        "juniper_junos" => {
            let mut commands = vec!["configure".to_string()];
            if state == "absent" {
                commands.push(format!(
                    "delete routing-options static route {} next-hop {}",
                    network, next_hop
                ));
            } else {
                commands.push(format!(
                    "set routing-options static route {} next-hop {}",
                    network, next_hop
                ));
            }
            commands.push("commit".to_string());
            commands.push("exit".to_string());
            commands
        }
        _ => vec![],
    }
}

impl Executor for NetworkStaticRouteExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let mut result = ResourceResult::new(
            "network_static_route",
            params.get("name").map_or("", |s| s),
        );

        let ssh = match SshParams::from_params(params, result) {
            Ok(s) => s,
            Err(r) => return *r,
        };
        result = ResourceResult::new("network_static_route", params.get("name").map_or("", |s| s));
        let device_type = require_param!(params, "device_type", result);
        let network = require_param!(params, "network", result);
        let next_hop = require_param!(params, "next_hop", result);
        let state = network_common::parse_state(params).to_string();
        let route_name = params.get("route_name").cloned();
        let timeout = DEFAULT_TIMEOUT;

        let dialect = match route_dialect(&device_type) {
            Some(d) => d,
            None => return result.with_error(format!("unsupported device_type: {}", device_type)),
        };

        let host_log = ssh.host.clone();
        network_common::run_ssh_executor(&mut result, start, &ssh, |session| {
            Box::pin(async move {
                let check_cmd = check_command(&device_type, &network);
                let current = session.exec(&check_cmd, timeout).await?;
                let is_present = route_present(&device_type, &current.stdout, &network, &next_hop);

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
                    &network,
                    &next_hop,
                    route_name.as_deref(),
                    &state,
                );
                let cmd_refs: Vec<&str> = commands.iter().map(|s| s.as_str()).collect();
                session.exec_interactive(&cmd_refs, dialect.prompt_pattern, timeout).await?;

                debug!(
                    host = host_log.as_str(),
                    device_type = device_type.as_str(),
                    network = network.as_str(),
                    next_hop = next_hop.as_str(),
                    state = state.as_str(),
                    "static route configuration applied"
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
    fn test_route_dialect_cisco() {
        let d = route_dialect("cisco_ios").unwrap();
        assert_eq!(d.prompt_pattern, "#");
    }

    #[test]
    fn test_route_dialect_arista_eos() {
        let d = route_dialect("arista_eos").unwrap();
        assert_eq!(d.prompt_pattern, "#");
    }

    #[test]
    fn test_route_dialect_iosxr() {
        let d = route_dialect("cisco_iosxr").unwrap();
        assert_eq!(d.prompt_pattern, "#");
    }

    #[test]
    fn test_route_dialect_junos() {
        let d = route_dialect("juniper_junos").unwrap();
        assert_eq!(d.prompt_pattern, ">");
    }

    #[test]
    fn test_route_dialect_unknown() {
        assert!(route_dialect("unknown_vendor").is_none());
    }

    #[test]
    fn test_prefix_to_mask() {
        assert_eq!(prefix_to_mask(8), "255.0.0.0");
        assert_eq!(prefix_to_mask(16), "255.255.0.0");
        assert_eq!(prefix_to_mask(24), "255.255.255.0");
        assert_eq!(prefix_to_mask(32), "255.255.255.255");
        assert_eq!(prefix_to_mask(0), "0.0.0.0");
    }

    #[test]
    fn test_parse_network_cidr() {
        let (net, mask) = parse_network("10.0.0.0/8");
        assert_eq!(net, "10.0.0.0");
        assert_eq!(mask, "255.0.0.0");
    }

    #[test]
    fn test_parse_network_spaced() {
        let (net, mask) = parse_network("10.0.0.0 255.0.0.0");
        assert_eq!(net, "10.0.0.0");
        assert_eq!(mask, "255.0.0.0");
    }

    #[test]
    fn test_parse_network_host() {
        let (net, mask) = parse_network("10.0.0.1");
        assert_eq!(net, "10.0.0.1");
        assert_eq!(mask, "255.255.255.255");
    }

    #[test]
    fn test_route_present_cisco() {
        let output = "ip route 10.0.0.0 255.0.0.0 192.168.1.1\n";
        assert!(route_present("cisco_ios", output, "10.0.0.0/8", "192.168.1.1"));
        assert!(!route_present("cisco_ios", output, "10.0.0.0/8", "192.168.1.2"));
    }

    #[test]
    fn test_route_present_arista_eos() {
        let output = "ip route 10.0.0.0 255.0.0.0 192.168.1.1\n";
        assert!(route_present("arista_eos", output, "10.0.0.0/8", "192.168.1.1"));
    }

    #[test]
    fn test_route_present_iosxr() {
        let output = "address-family ipv4 unicast\n 10.0.0.0/8 192.168.1.1\n";
        assert!(route_present("cisco_iosxr", output, "10.0.0.0/8", "192.168.1.1"));
    }

    #[test]
    fn test_route_present_junos() {
        let output = "static {\n    route 10.0.0.0/8 next-hop 192.168.1.1;\n}\n";
        assert!(route_present("juniper_junos", output, "10.0.0.0/8", "192.168.1.1"));
    }

    #[test]
    fn test_build_commands_cisco_present() {
        let cmds = build_commands("cisco_ios", "10.0.0.0/8", "192.168.1.1", None, "present");
        assert_eq!(cmds, vec![
            "configure terminal",
            "ip route 10.0.0.0 255.0.0.0 192.168.1.1",
            "end",
            "write memory",
        ]);
    }

    #[test]
    fn test_build_commands_cisco_absent() {
        let cmds = build_commands("cisco_ios", "10.0.0.0/8", "192.168.1.1", None, "absent");
        assert_eq!(cmds, vec![
            "configure terminal",
            "no ip route 10.0.0.0 255.0.0.0 192.168.1.1",
            "end",
            "write memory",
        ]);
    }

    #[test]
    fn test_build_commands_arista_eos_present() {
        let cmds = build_commands("arista_eos", "10.0.0.0/8", "192.168.1.1", None, "present");
        assert_eq!(cmds, vec![
            "configure terminal",
            "ip route 10.0.0.0 255.0.0.0 192.168.1.1",
            "end",
            "write memory",
        ]);
    }

    #[test]
    fn test_build_commands_iosxr_present() {
        let cmds = build_commands("cisco_iosxr", "10.0.0.0/8", "192.168.1.1", None, "present");
        assert_eq!(cmds, vec![
            "configure terminal",
            "router static",
            "address-family ipv4 unicast",
            "10.0.0.0/8 192.168.1.1",
            "commit",
            "end",
        ]);
    }

    #[test]
    fn test_build_commands_iosxr_absent() {
        let cmds = build_commands("cisco_iosxr", "10.0.0.0/8", "192.168.1.1", None, "absent");
        assert_eq!(cmds, vec![
            "configure terminal",
            "router static",
            "address-family ipv4 unicast",
            "no 10.0.0.0/8 192.168.1.1",
            "commit",
            "end",
        ]);
    }

    #[test]
    fn test_build_commands_junos_present() {
        let cmds = build_commands("juniper_junos", "10.0.0.0/8", "192.168.1.1", None, "present");
        assert_eq!(cmds, vec![
            "configure",
            "set routing-options static route 10.0.0.0/8 next-hop 192.168.1.1",
            "commit",
            "exit",
        ]);
    }

    #[test]
    fn test_build_commands_junos_absent() {
        let cmds = build_commands("juniper_junos", "10.0.0.0/8", "192.168.1.1", None, "absent");
        assert_eq!(cmds, vec![
            "configure",
            "delete routing-options static route 10.0.0.0/8 next-hop 192.168.1.1",
            "commit",
            "exit",
        ]);
    }

    #[test]
    fn test_network_static_route_missing_params() {
        let executor = NetworkStaticRouteExecutor::new();
        let params = HashMap::new();
        let result = executor.execute(&params, false);
        assert!(result.error.is_some());
    }

    #[test]
    fn test_network_static_route_missing_network() {
        let executor = NetworkStaticRouteExecutor::new();
        let mut params = HashMap::new();
        params.insert("host".to_string(), "10.0.0.1".to_string());
        params.insert("username".to_string(), "admin".to_string());
        params.insert("credential".to_string(), "secret".to_string());
        params.insert("device_type".to_string(), "cisco_ios".to_string());
        let result = executor.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("network"));
    }

    #[test]
    fn test_network_static_route_missing_next_hop() {
        let executor = NetworkStaticRouteExecutor::new();
        let mut params = HashMap::new();
        params.insert("host".to_string(), "10.0.0.1".to_string());
        params.insert("username".to_string(), "admin".to_string());
        params.insert("credential".to_string(), "secret".to_string());
        params.insert("device_type".to_string(), "cisco_ios".to_string());
        params.insert("network".to_string(), "10.0.0.0/8".to_string());
        let result = executor.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("next_hop"));
    }
}
