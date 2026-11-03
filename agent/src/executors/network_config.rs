// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! network_config executor — declarative config-block management over SSH.
//!
//! Params:
//! - host (required): auto-injected
//! - credential (required): auto-injected
//! - username (required): auto-injected
//! - port (optional): SSH port, default 22
//! - device_type (required): cisco_ios, cisco_nxos, juniper_junos
//! - config_lines (required): newline-separated config lines to ensure
//! - state (optional): present (default) or absent
//! - check_command (optional): command to check current state (default: show running-config)
//! - save (optional): whether to save config after change (default: true)

use crate::executors::{Executor, ResourceResult};
use crate::executors::network_common::{self, require_param, SshParams, DEFAULT_TIMEOUT};
use std::collections::HashMap;
use std::time::Instant;
use tracing::debug;

pub struct NetworkConfigExecutor;

impl NetworkConfigExecutor {
    pub fn new() -> Self {
        Self
    }
}

/// Device-specific config mode commands.
struct DeviceDialect {
    enter_config: &'static str,
    exit_config: &'static str,
    save_command: &'static str,
    negate_prefix: &'static str,
    prompt_pattern: &'static str,
}

fn dialect(device_type: &str) -> Option<DeviceDialect> {
    match device_type {
        "cisco_ios" => Some(DeviceDialect {
            enter_config: "configure terminal",
            exit_config: "end",
            save_command: "write memory",
            negate_prefix: "no ",
            prompt_pattern: "#",
        }),
        "cisco_nxos" => Some(DeviceDialect {
            enter_config: "configure terminal",
            exit_config: "end",
            save_command: "copy running-config startup-config",
            negate_prefix: "no ",
            prompt_pattern: "#",
        }),
        "arista_eos" => Some(DeviceDialect {
            enter_config: "configure terminal",
            exit_config: "end",
            save_command: "write memory",
            negate_prefix: "no ",
            prompt_pattern: "#",
        }),
        "cisco_iosxr" => Some(DeviceDialect {
            enter_config: "configure terminal",
            exit_config: "commit",
            save_command: "end",
            negate_prefix: "no ",
            prompt_pattern: "#",
        }),
        "juniper_junos" => Some(DeviceDialect {
            enter_config: "configure",
            exit_config: "commit",
            save_command: "exit", // commit is save for Junos
            negate_prefix: "delete ",
            prompt_pattern: ">",
        }),
        _ => None,
    }
}

impl Executor for NetworkConfigExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let mut result = ResourceResult::new(
            "network_config",
            params.get("name").map_or("", |s| s),
        );

        let ssh = match SshParams::from_params(params, result) {
            Ok(s) => s,
            Err(r) => return *r,
        };
        result = ResourceResult::new("network_config", params.get("name").map_or("", |s| s));
        let device_type = require_param!(params, "device_type", result);
        let config_lines = require_param!(params, "config_lines", result);
        let state = network_common::parse_state(params).to_string();
        let check_command = params
            .get("check_command")
            .map_or("show running-config", |s| s.as_str())
            .to_string();
        let save = params.get("save").is_none_or(|s| s != "false");
        let timeout = DEFAULT_TIMEOUT;

        let dialect = match dialect(&device_type) {
            Some(d) => d,
            None => return result.with_error(format!("unsupported device_type: {}", device_type)),
        };

        let lines: Vec<String> = config_lines.lines().map(|l| l.to_string()).collect();

        let host_log = ssh.host.clone();
        network_common::run_ssh_executor(&mut result, start, &ssh, |session| {
            Box::pin(async move {
                // Check current state.
                let current = session.exec(&check_command, timeout).await?;
                let current_config = current.stdout;

                let lines_present = lines.iter().all(|line| {
                    let trimmed = line.trim();
                    if trimmed.is_empty() {
                        return true;
                    }
                    current_config.contains(trimmed)
                });

                let needs_change = match state.as_str() {
                    "present" => !lines_present,
                    "absent" => {
                        lines.iter().any(|line| {
                            let trimmed = line.trim();
                            !trimmed.is_empty() && current_config.contains(trimmed)
                        })
                    }
                    _ => return Err(anyhow::anyhow!("invalid state: {}", state)),
                };

                if !needs_change {
                    return Ok(false); // no change needed
                }

                if dry_run {
                    return Ok(true); // would change
                }

                // Build config commands.
                let mut commands: Vec<String> = vec![dialect.enter_config.to_string()];
                for line in &lines {
                    let trimmed = line.trim();
                    if trimmed.is_empty() {
                        continue;
                    }
                    match state.as_str() {
                        "absent" => commands.push(format!("{}{}", dialect.negate_prefix, trimmed)),
                        _ => commands.push(trimmed.to_string()),
                    }
                }
                commands.push(dialect.exit_config.to_string());
                if save && dialect.save_command != dialect.exit_config {
                    commands.push(dialect.save_command.to_string());
                }

                // Execute config commands interactively.
                let cmd_refs: Vec<&str> = commands.iter().map(|s| s.as_str()).collect();
                let _output = session
                    .exec_interactive(&cmd_refs, dialect.prompt_pattern, timeout)
                    .await?;

                debug!(host = host_log.as_str(), device_type = device_type.as_str(), "config applied");
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
    fn test_dialect_cisco_ios() {
        let d = dialect("cisco_ios").unwrap();
        assert_eq!(d.enter_config, "configure terminal");
        assert_eq!(d.save_command, "write memory");
        assert_eq!(d.negate_prefix, "no ");
    }

    #[test]
    fn test_dialect_cisco_nxos() {
        let d = dialect("cisco_nxos").unwrap();
        assert_eq!(d.save_command, "copy running-config startup-config");
    }

    #[test]
    fn test_dialect_junos() {
        let d = dialect("juniper_junos").unwrap();
        assert_eq!(d.enter_config, "configure");
        assert_eq!(d.exit_config, "commit");
        assert_eq!(d.negate_prefix, "delete ");
    }

    #[test]
    fn test_dialect_arista_eos() {
        let d = dialect("arista_eos").unwrap();
        assert_eq!(d.enter_config, "configure terminal");
        assert_eq!(d.save_command, "write memory");
        assert_eq!(d.negate_prefix, "no ");
        assert_eq!(d.prompt_pattern, "#");
    }

    #[test]
    fn test_dialect_cisco_iosxr() {
        let d = dialect("cisco_iosxr").unwrap();
        assert_eq!(d.enter_config, "configure terminal");
        assert_eq!(d.exit_config, "commit");
        assert_eq!(d.save_command, "end");
        assert_eq!(d.negate_prefix, "no ");
        assert_eq!(d.prompt_pattern, "#");
    }

    #[test]
    fn test_dialect_unknown() {
        assert!(dialect("unknown_vendor").is_none());
    }

    #[test]
    fn test_network_config_missing_params() {
        let executor = NetworkConfigExecutor::new();
        let params = HashMap::new();
        let result = executor.execute(&params, false);
        assert!(result.error.is_some());
    }
}
