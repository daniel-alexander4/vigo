// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! network_dns executor — manages DNS configuration on network devices over SSH.
//!
//! Params:
//! - host (required): auto-injected
//! - username (required): auto-injected
//! - credential (required): auto-injected
//! - port (optional): SSH port, default 22
//! - device_type (required): cisco_ios, cisco_nxos, juniper_junos, arista_eos, cisco_iosxr
//! - server (optional): DNS server IP (for adding/removing nameservers)
//! - domain (optional): Domain name (for setting the domain)
//! - state (optional): present (default) or absent
//!
//! At least one of `server` or `domain` is required.

use crate::executors::{Executor, ResourceResult};
use crate::executors::network_common::{self, SshParams, DEFAULT_TIMEOUT};
use std::collections::HashMap;
use std::time::Instant;
use tracing::debug;

pub struct NetworkDnsExecutor;

impl NetworkDnsExecutor {
    pub fn new() -> Self {
        Self
    }
}

struct DnsDialect {
    prompt_pattern: &'static str,
}

fn dns_dialect(device_type: &str) -> Option<DnsDialect> {
    match device_type {
        "cisco_ios" | "cisco_nxos" | "arista_eos" | "cisco_iosxr" => Some(DnsDialect {
            prompt_pattern: "#",
        }),
        "juniper_junos" => Some(DnsDialect {
            prompt_pattern: ">",
        }),
        _ => None,
    }
}

/// Build the check command for DNS configuration.
fn check_command(device_type: &str) -> &'static str {
    match device_type {
        "cisco_ios" | "cisco_nxos" | "arista_eos" => {
            "show running-config | include name-server|domain"
        }
        "cisco_iosxr" => {
            "show running-config | include domain"
        }
        "juniper_junos" => {
            "show configuration system"
        }
        _ => "",
    }
}

/// Returns true if the DNS server is configured.
fn dns_server_present(device_type: &str, output: &str, server: &str) -> bool {
    match device_type {
        "cisco_ios" | "cisco_nxos" | "arista_eos" => {
            let needle = format!("name-server {}", server);
            output.lines().any(|line| line.trim().contains(&needle))
        }
        "cisco_iosxr" => {
            let needle = format!("name-server {}", server);
            output.lines().any(|line| line.trim().contains(&needle))
        }
        "juniper_junos" => {
            let needle = format!("name-server {}", server);
            output.contains(&needle)
        }
        _ => false,
    }
}

/// Returns true if the domain name is configured.
fn dns_domain_present(device_type: &str, output: &str, domain: &str) -> bool {
    match device_type {
        "cisco_ios" | "cisco_nxos" | "arista_eos" => {
            let needle = format!("domain-name {}", domain);
            output.lines().any(|line| line.trim().contains(&needle))
                || output.lines().any(|line| line.trim().contains(&format!("domain name {}", domain)))
        }
        "cisco_iosxr" => {
            let needle = format!("domain name {}", domain);
            output.lines().any(|line| line.trim().contains(&needle))
        }
        "juniper_junos" => {
            let needle = format!("domain-name {}", domain);
            output.contains(&needle)
        }
        _ => false,
    }
}

/// Build config commands for DNS management.
fn build_commands(
    device_type: &str,
    server: Option<&str>,
    domain: Option<&str>,
    state: &str,
) -> Vec<String> {
    match device_type {
        "cisco_ios" | "arista_eos" => {
            let mut commands = vec!["configure terminal".to_string()];
            if let Some(srv) = server {
                if state == "absent" {
                    commands.push(format!("no ip name-server {}", srv));
                } else {
                    commands.push(format!("ip name-server {}", srv));
                }
            }
            if let Some(dom) = domain {
                if state == "absent" {
                    commands.push(format!("no ip domain-name {}", dom));
                } else {
                    commands.push(format!("ip domain-name {}", dom));
                }
            }
            commands.push("end".to_string());
            commands.push("write memory".to_string());
            commands
        }
        "cisco_nxos" => {
            let mut commands = vec!["configure terminal".to_string()];
            if let Some(srv) = server {
                if state == "absent" {
                    commands.push(format!("no ip name-server {}", srv));
                } else {
                    commands.push(format!("ip name-server {}", srv));
                }
            }
            if let Some(dom) = domain {
                if state == "absent" {
                    commands.push(format!("no ip domain-name {}", dom));
                } else {
                    commands.push(format!("ip domain-name {}", dom));
                }
            }
            commands.push("end".to_string());
            commands.push("write memory".to_string());
            commands
        }
        "cisco_iosxr" => {
            let mut commands = vec!["configure terminal".to_string()];
            if let Some(srv) = server {
                if state == "absent" {
                    commands.push(format!("no domain name-server {}", srv));
                } else {
                    commands.push(format!("domain name-server {}", srv));
                }
            }
            if let Some(dom) = domain {
                if state == "absent" {
                    commands.push(format!("no domain name {}", dom));
                } else {
                    commands.push(format!("domain name {}", dom));
                }
            }
            commands.push("commit".to_string());
            commands.push("end".to_string());
            commands
        }
        "juniper_junos" => {
            let mut commands = vec!["configure".to_string()];
            if let Some(srv) = server {
                if state == "absent" {
                    commands.push(format!("delete system name-server {}", srv));
                } else {
                    commands.push(format!("set system name-server {}", srv));
                }
            }
            if let Some(dom) = domain {
                if state == "absent" {
                    commands.push("delete system domain-name".to_string());
                } else {
                    commands.push(format!("set system domain-name {}", dom));
                }
            }
            commands.push("commit".to_string());
            commands.push("exit".to_string());
            commands
        }
        _ => vec![],
    }
}

impl Executor for NetworkDnsExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let mut result = ResourceResult::new(
            "network_dns",
            params.get("name").map_or("", |s| s),
        );

        let ssh = match SshParams::from_params(params, result) {
            Ok(s) => s,
            Err(r) => return r,
        };
        result = ResourceResult::new("network_dns", params.get("name").map_or("", |s| s));
        let device_type = match params.get("device_type") {
            Some(v) => v.clone(),
            None => return result.with_error("missing required param: device_type".into()),
        };
        let server = params.get("server").cloned();
        let domain = params.get("domain").cloned();
        let state = network_common::parse_state(params).to_string();
        let timeout = DEFAULT_TIMEOUT;

        if server.is_none() && domain.is_none() {
            return result.with_error("at least one of 'server' or 'domain' is required".into());
        }

        let dialect = match dns_dialect(&device_type) {
            Some(d) => d,
            None => return result.with_error(format!("unsupported device_type: {}", device_type)),
        };

        let host_log = ssh.host.clone();
        network_common::run_ssh_executor(&mut result, start, &ssh, |session| {
            Box::pin(async move {
                let check_cmd = check_command(&device_type);
                let current = session.exec(check_cmd, timeout).await?;
                let current_output = &current.stdout;

                // Determine if changes are needed for server and/or domain.
                let server_needs_change = if let Some(ref srv) = server {
                    let present = dns_server_present(&device_type, current_output, srv);
                    match state.as_str() {
                        "present" => !present,
                        "absent" => present,
                        _ => return Err(anyhow::anyhow!("invalid state: {}", state)),
                    }
                } else {
                    false
                };

                let domain_needs_change = if let Some(ref dom) = domain {
                    let present = dns_domain_present(&device_type, current_output, dom);
                    match state.as_str() {
                        "present" => !present,
                        "absent" => present,
                        _ => return Err(anyhow::anyhow!("invalid state: {}", state)),
                    }
                } else {
                    false
                };

                if !server_needs_change && !domain_needs_change {
                    return Ok(false);
                }
                if dry_run {
                    return Ok(true);
                }

                let commands = build_commands(
                    &device_type,
                    server.as_deref(),
                    domain.as_deref(),
                    &state,
                );
                let cmd_refs: Vec<&str> = commands.iter().map(|s| s.as_str()).collect();
                session.exec_interactive(&cmd_refs, dialect.prompt_pattern, timeout).await?;

                debug!(
                    host = host_log.as_str(),
                    device_type = device_type.as_str(),
                    state = state.as_str(),
                    "DNS configuration applied"
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
    fn test_dns_dialect_cisco() {
        let d = dns_dialect("cisco_ios").unwrap();
        assert_eq!(d.prompt_pattern, "#");
    }

    #[test]
    fn test_dns_dialect_arista_eos() {
        let d = dns_dialect("arista_eos").unwrap();
        assert_eq!(d.prompt_pattern, "#");
    }

    #[test]
    fn test_dns_dialect_iosxr() {
        let d = dns_dialect("cisco_iosxr").unwrap();
        assert_eq!(d.prompt_pattern, "#");
    }

    #[test]
    fn test_dns_dialect_junos() {
        let d = dns_dialect("juniper_junos").unwrap();
        assert_eq!(d.prompt_pattern, ">");
    }

    #[test]
    fn test_dns_dialect_unknown() {
        assert!(dns_dialect("unknown_vendor").is_none());
    }

    #[test]
    fn test_check_command_cisco() {
        assert_eq!(
            check_command("cisco_ios"),
            "show running-config | include name-server|domain"
        );
    }

    #[test]
    fn test_check_command_iosxr() {
        assert_eq!(
            check_command("cisco_iosxr"),
            "show running-config | include domain"
        );
    }

    #[test]
    fn test_check_command_junos() {
        assert_eq!(check_command("juniper_junos"), "show configuration system");
    }

    #[test]
    fn test_dns_server_present_cisco() {
        let output = "ip name-server 8.8.8.8\nip name-server 8.8.4.4\n";
        assert!(dns_server_present("cisco_ios", output, "8.8.8.8"));
        assert!(!dns_server_present("cisco_ios", output, "1.1.1.1"));
    }

    #[test]
    fn test_dns_server_present_arista_eos() {
        let output = "ip name-server 8.8.8.8\n";
        assert!(dns_server_present("arista_eos", output, "8.8.8.8"));
    }

    #[test]
    fn test_dns_server_present_iosxr() {
        let output = "domain name-server 8.8.8.8\n";
        assert!(dns_server_present("cisco_iosxr", output, "8.8.8.8"));
    }

    #[test]
    fn test_dns_server_present_junos() {
        let output = "name-server 8.8.8.8;\nname-server 8.8.4.4;\n";
        assert!(dns_server_present("juniper_junos", output, "8.8.8.8"));
        assert!(!dns_server_present("juniper_junos", output, "1.1.1.1"));
    }

    #[test]
    fn test_dns_domain_present_cisco() {
        let output = "ip domain-name example.com\n";
        assert!(dns_domain_present("cisco_ios", output, "example.com"));
        assert!(!dns_domain_present("cisco_ios", output, "other.com"));
    }

    #[test]
    fn test_dns_domain_present_iosxr() {
        let output = "domain name example.com\n";
        assert!(dns_domain_present("cisco_iosxr", output, "example.com"));
    }

    #[test]
    fn test_dns_domain_present_junos() {
        let output = "domain-name example.com;\n";
        assert!(dns_domain_present("juniper_junos", output, "example.com"));
    }

    #[test]
    fn test_build_commands_cisco_server_present() {
        let cmds = build_commands("cisco_ios", Some("8.8.8.8"), None, "present");
        assert_eq!(cmds, vec![
            "configure terminal",
            "ip name-server 8.8.8.8",
            "end",
            "write memory",
        ]);
    }

    #[test]
    fn test_build_commands_cisco_domain_present() {
        let cmds = build_commands("cisco_ios", None, Some("example.com"), "present");
        assert_eq!(cmds, vec![
            "configure terminal",
            "ip domain-name example.com",
            "end",
            "write memory",
        ]);
    }

    #[test]
    fn test_build_commands_cisco_both_present() {
        let cmds = build_commands("cisco_ios", Some("8.8.8.8"), Some("example.com"), "present");
        assert_eq!(cmds, vec![
            "configure terminal",
            "ip name-server 8.8.8.8",
            "ip domain-name example.com",
            "end",
            "write memory",
        ]);
    }

    #[test]
    fn test_build_commands_cisco_server_absent() {
        let cmds = build_commands("cisco_ios", Some("8.8.8.8"), None, "absent");
        assert_eq!(cmds, vec![
            "configure terminal",
            "no ip name-server 8.8.8.8",
            "end",
            "write memory",
        ]);
    }

    #[test]
    fn test_build_commands_arista_eos_present() {
        let cmds = build_commands("arista_eos", Some("8.8.8.8"), Some("example.com"), "present");
        assert_eq!(cmds, vec![
            "configure terminal",
            "ip name-server 8.8.8.8",
            "ip domain-name example.com",
            "end",
            "write memory",
        ]);
    }

    #[test]
    fn test_build_commands_iosxr_present() {
        let cmds = build_commands("cisco_iosxr", Some("8.8.8.8"), Some("example.com"), "present");
        assert_eq!(cmds, vec![
            "configure terminal",
            "domain name-server 8.8.8.8",
            "domain name example.com",
            "commit",
            "end",
        ]);
    }

    #[test]
    fn test_build_commands_iosxr_absent() {
        let cmds = build_commands("cisco_iosxr", Some("8.8.8.8"), None, "absent");
        assert_eq!(cmds, vec![
            "configure terminal",
            "no domain name-server 8.8.8.8",
            "commit",
            "end",
        ]);
    }

    #[test]
    fn test_build_commands_junos_present() {
        let cmds = build_commands("juniper_junos", Some("8.8.8.8"), Some("example.com"), "present");
        assert_eq!(cmds, vec![
            "configure",
            "set system name-server 8.8.8.8",
            "set system domain-name example.com",
            "commit",
            "exit",
        ]);
    }

    #[test]
    fn test_build_commands_junos_absent() {
        let cmds = build_commands("juniper_junos", Some("8.8.8.8"), Some("example.com"), "absent");
        assert_eq!(cmds, vec![
            "configure",
            "delete system name-server 8.8.8.8",
            "delete system domain-name",
            "commit",
            "exit",
        ]);
    }

    #[test]
    fn test_network_dns_missing_params() {
        let executor = NetworkDnsExecutor::new();
        let params = HashMap::new();
        let result = executor.execute(&params, false);
        assert!(result.error.is_some());
    }

    #[test]
    fn test_network_dns_missing_server_and_domain() {
        let executor = NetworkDnsExecutor::new();
        let mut params = HashMap::new();
        params.insert("host".to_string(), "10.0.0.1".to_string());
        params.insert("username".to_string(), "admin".to_string());
        params.insert("credential".to_string(), "secret".to_string());
        params.insert("device_type".to_string(), "cisco_ios".to_string());
        let result = executor.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("at least one"));
    }
}
