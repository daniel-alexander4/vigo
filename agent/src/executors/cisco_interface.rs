// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! cisco_interface executor — manages interface configuration on Cisco IOS/NX-OS
//! switches over SSH.
//!
//! Params:
//! - host (required): switch hostname or IP
//! - username (required): SSH username
//! - credential (required): SSH password or key
//! - port (optional): SSH port, default 22
//! - interface (required): interface name, e.g. "GigabitEthernet0/1"
//! - description (optional): interface description
//! - ip_address (optional): IP address with mask, e.g. "10.0.1.1 255.255.255.0"
//! - shutdown (optional): "true" or "false"
//! - switchport_mode (optional): "access" or "trunk"
//! - access_vlan (optional): VLAN ID for access ports

use crate::executors::{Executor, ResourceResult};
use crate::executors::network_common::{self, require_param, SshParams, DEFAULT_TIMEOUT};
use std::collections::HashMap;
use std::time::Instant;
use tracing::debug;

pub struct CiscoInterfaceExecutor;

impl CiscoInterfaceExecutor {
    pub fn new() -> Self {
        Self
    }
}

/// Parse a `show running-config interface` output into key-value pairs for
/// the fields we care about.
fn parse_interface_config(output: &str) -> HashMap<String, String> {
    let mut config = HashMap::new();
    for line in output.lines() {
        let trimmed = line.trim();
        if let Some(desc) = trimmed.strip_prefix("description ") {
            config.insert("description".to_string(), desc.to_string());
        } else if let Some(ip) = trimmed.strip_prefix("ip address ") {
            config.insert("ip_address".to_string(), ip.to_string());
        } else if trimmed == "shutdown" {
            config.insert("shutdown".to_string(), "true".to_string());
        } else if trimmed == "no shutdown" {
            config.insert("shutdown".to_string(), "false".to_string());
        } else if let Some(mode) = trimmed.strip_prefix("switchport mode ") {
            config.insert("switchport_mode".to_string(), mode.to_string());
        } else if let Some(vlan) = trimmed.strip_prefix("switchport access vlan ") {
            config.insert("access_vlan".to_string(), vlan.to_string());
        }
    }
    // If shutdown was never mentioned, Cisco defaults to no shutdown.
    if !config.contains_key("shutdown") {
        config.insert("shutdown".to_string(), "false".to_string());
    }
    config
}

/// Compare desired fields against current config. Returns the list of
/// config-mode commands needed to reach desired state. Empty vec means no
/// drift.
fn build_change_commands(
    desired: &HashMap<String, String>,
    current: &HashMap<String, String>,
    interface: &str,
) -> Vec<String> {
    let mut commands: Vec<String> = Vec::new();

    // Check each desired field against current.
    if let Some(desc) = desired.get("description") {
        if current.get("description") != Some(desc) {
            commands.push(format!("description {}", desc));
        }
    }

    if let Some(ip) = desired.get("ip_address") {
        if current.get("ip_address") != Some(ip) {
            commands.push(format!("ip address {}", ip));
        }
    }

    if let Some(mode) = desired.get("switchport_mode") {
        if current.get("switchport_mode") != Some(mode) {
            commands.push(format!("switchport mode {}", mode));
        }
    }

    if let Some(vlan) = desired.get("access_vlan") {
        if current.get("access_vlan") != Some(vlan) {
            commands.push(format!("switchport access vlan {}", vlan));
        }
    }

    if let Some(shut) = desired.get("shutdown") {
        let current_shut = current.get("shutdown").map_or("false", |s| s.as_str());
        if shut != current_shut {
            if shut == "true" {
                commands.push("shutdown".to_string());
            } else {
                commands.push("no shutdown".to_string());
            }
        }
    }

    if commands.is_empty() {
        return commands;
    }

    // Wrap with interface context.
    let mut full = Vec::with_capacity(commands.len() + 3);
    full.push("configure terminal".to_string());
    full.push(format!("interface {}", interface));
    full.extend(commands);
    full.push("end".to_string());
    full.push("write memory".to_string());
    full
}

impl Executor for CiscoInterfaceExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let mut result = ResourceResult::new(
            "cisco_interface",
            params.get("name").map_or("", |s| s),
        );

        let ssh = match SshParams::from_params(params, result) {
            Ok(s) => s,
            Err(r) => return *r,
        };
        result = ResourceResult::new("cisco_interface", params.get("name").map_or("", |s| s));
        let interface = require_param!(params, "interface", result);
        let timeout = DEFAULT_TIMEOUT;

        // Build desired state from optional params.
        let mut desired: HashMap<String, String> = HashMap::new();
        if let Some(v) = params.get("description") {
            desired.insert("description".to_string(), v.clone());
        }
        if let Some(v) = params.get("ip_address") {
            desired.insert("ip_address".to_string(), v.clone());
        }
        if let Some(v) = params.get("shutdown") {
            desired.insert("shutdown".to_string(), v.clone());
        }
        if let Some(v) = params.get("switchport_mode") {
            desired.insert("switchport_mode".to_string(), v.clone());
        }
        if let Some(v) = params.get("access_vlan") {
            desired.insert("access_vlan".to_string(), v.clone());
        }

        let host_log = ssh.host.clone();
        network_common::run_ssh_executor(&mut result, start, &ssh, |session| {
            Box::pin(async move {
                // Check current interface configuration.
                let check_cmd = format!("show running-config interface {}", interface);
                let current = session.exec(&check_cmd, timeout).await?;
                let current_config = parse_interface_config(&current.stdout);

                let commands = build_change_commands(&desired, &current_config, &interface);
                if commands.is_empty() {
                    return Ok(false); // no change needed
                }

                if dry_run {
                    return Ok(true); // would change
                }

                // Push config commands interactively.
                let cmd_refs: Vec<&str> = commands.iter().map(|s| s.as_str()).collect();
                let _output = session
                    .exec_interactive(&cmd_refs, "#", timeout)
                    .await?;

                debug!(
                    host = host_log.as_str(),
                    interface = interface.as_str(),
                    "interface config applied"
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
    fn test_parse_interface_config_full() {
        let output = r#"
Building configuration...

Current configuration : 150 bytes
!
interface GigabitEthernet0/1
 description Uplink to core
 ip address 10.0.1.1 255.255.255.0
 switchport mode access
 switchport access vlan 100
 shutdown
end
"#;
        let config = parse_interface_config(output);
        assert_eq!(config.get("description").unwrap(), "Uplink to core");
        assert_eq!(config.get("ip_address").unwrap(), "10.0.1.1 255.255.255.0");
        assert_eq!(config.get("switchport_mode").unwrap(), "access");
        assert_eq!(config.get("access_vlan").unwrap(), "100");
        assert_eq!(config.get("shutdown").unwrap(), "true");
    }

    #[test]
    fn test_parse_interface_config_no_shutdown() {
        let output = "interface GigabitEthernet0/1\n description Test\n";
        let config = parse_interface_config(output);
        assert_eq!(config.get("shutdown").unwrap(), "false");
    }

    #[test]
    fn test_build_change_commands_no_drift() {
        let mut desired = HashMap::new();
        desired.insert("description".to_string(), "Uplink".to_string());

        let mut current = HashMap::new();
        current.insert("description".to_string(), "Uplink".to_string());
        current.insert("shutdown".to_string(), "false".to_string());

        let commands = build_change_commands(&desired, &current, "GigabitEthernet0/1");
        assert!(commands.is_empty());
    }

    #[test]
    fn test_build_change_commands_with_drift() {
        let mut desired = HashMap::new();
        desired.insert("description".to_string(), "New uplink".to_string());
        desired.insert("shutdown".to_string(), "true".to_string());

        let mut current = HashMap::new();
        current.insert("description".to_string(), "Old uplink".to_string());
        current.insert("shutdown".to_string(), "false".to_string());

        let commands = build_change_commands(&desired, &current, "GigabitEthernet0/1");
        assert!(!commands.is_empty());
        assert_eq!(commands[0], "configure terminal");
        assert_eq!(commands[1], "interface GigabitEthernet0/1");
        assert!(commands.contains(&"description New uplink".to_string()));
        assert!(commands.contains(&"shutdown".to_string()));
        assert_eq!(commands[commands.len() - 2], "end");
        assert_eq!(commands[commands.len() - 1], "write memory");
    }

    #[test]
    fn test_cisco_interface_missing_params() {
        let executor = CiscoInterfaceExecutor::new();
        let params = HashMap::new();
        let result = executor.execute(&params, false);
        assert!(result.error.is_some());
    }

    #[test]
    fn test_cisco_interface_missing_interface() {
        let executor = CiscoInterfaceExecutor::new();
        let mut params = HashMap::new();
        params.insert("host".to_string(), "10.0.0.1".to_string());
        params.insert("username".to_string(), "admin".to_string());
        params.insert("credential".to_string(), "secret".to_string());
        let result = executor.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("interface"));
    }
}
