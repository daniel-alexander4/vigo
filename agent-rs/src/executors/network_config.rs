// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

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

use crate::executors::{Action, Executor, ResourceResult};
use crate::transport::ssh::SshSession;
use std::collections::HashMap;
use std::time::{Duration, Instant};
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
        let config_lines = match params.get("config_lines") {
            Some(cl) => cl.clone(),
            None => return result.with_error("missing required param: config_lines".into()),
        };

        let port: u16 = params
            .get("port")
            .and_then(|p| p.parse().ok())
            .unwrap_or(22);
        let state = params.get("state").map_or("present", |s| s.as_str());
        let check_command = params
            .get("check_command")
            .map_or("show running-config", |s| s.as_str())
            .to_string();
        let save = params.get("save").map_or(true, |s| s != "false");
        let timeout = Duration::from_secs(30);

        let dialect = match dialect(&device_type) {
            Some(d) => d,
            None => return result.with_error(format!("unsupported device_type: {}", device_type)),
        };

        let lines: Vec<&str> = config_lines.lines().collect();

        let rt = match tokio::runtime::Handle::try_current() {
            Ok(h) => h,
            Err(_) => return result.with_error("no tokio runtime available".into()),
        };

        let exec_result = tokio::task::block_in_place(|| {
            rt.block_on(async {
                let session = SshSession::connect(&host, port, &username, &credential, None).await?;

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

                let needs_change = match state {
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
                    match state {
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

                debug!(host = host.as_str(), device_type = device_type.as_str(), "config applied");
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
    fn test_dialect_unknown() {
        assert!(dialect("arista_eos").is_none());
    }

    #[test]
    fn test_network_config_missing_params() {
        let executor = NetworkConfigExecutor::new();
        let params = HashMap::new();
        let result = executor.execute(&params, false);
        assert!(result.error.is_some());
    }
}
