// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! network_banner executor — login/MOTD banner management on network devices over SSH.
//!
//! Params:
//! - host (required): auto-injected
//! - credential (required): auto-injected
//! - username (required): auto-injected
//! - port (optional): SSH port, default 22
//! - device_type (required): cisco_ios, cisco_nxos, juniper_junos
//! - banner_type (required): login or motd
//! - text (required): banner text to set
//! - state (optional): present (default) or absent

use crate::executors::{Action, Executor, ResourceResult};
use crate::executors::network_common::{self, require_param, SshParams, DEFAULT_TIMEOUT};
use std::collections::HashMap;
use std::time::Instant;
use tracing::debug;

pub struct NetworkBannerExecutor;

impl NetworkBannerExecutor {
    pub fn new() -> Self {
        Self
    }
}

/// Returns the prompt pattern used for interactive exec on each device type.
fn prompt_pattern(device_type: &str) -> Option<&'static str> {
    match device_type {
        "cisco_ios" | "cisco_nxos" | "arista_eos" | "cisco_iosxr" => Some("#"),
        "juniper_junos" => Some(">"),
        _ => None,
    }
}

/// Build the check command to inspect current banner state.
fn check_command(device_type: &str, banner_type: &str) -> String {
    match device_type {
        "cisco_ios" | "cisco_nxos" | "arista_eos" | "cisco_iosxr" => format!("show banner {}", banner_type),
        "juniper_junos" => "show configuration system login".to_string(),
        _ => String::new(),
    }
}

/// Determine whether the banner text is already present in the device output.
fn banner_present(device_type: &str, banner_type: &str, output: &str, text: &str) -> bool {
    match device_type {
        "cisco_ios" | "cisco_nxos" | "arista_eos" | "cisco_iosxr" => output.contains(text),
        "juniper_junos" => {
            // Junos uses "message" for login banners, "announcement" for motd.
            let keyword = match banner_type {
                "login" => "message",
                _ => "announcement",
            };
            output.contains(keyword) && output.contains(text)
        }
        _ => false,
    }
}

/// Build the config commands to set (present) or remove (absent) a banner.
fn build_commands(
    device_type: &str,
    banner_type: &str,
    text: &str,
    state: &str,
) -> Vec<String> {
    let mut commands = Vec::new();

    match device_type {
        "cisco_ios" | "cisco_nxos" | "arista_eos" => {
            commands.push("configure terminal".to_string());
            match state {
                "absent" => {
                    commands.push(format!("no banner {}", banner_type));
                }
                _ => {
                    commands.push(format!("banner {} ^{}^", banner_type, text));
                }
            }
            commands.push("end".to_string());
            commands.push("write memory".to_string());
        }
        "cisco_iosxr" => {
            commands.push("configure terminal".to_string());
            match state {
                "absent" => {
                    commands.push(format!("no banner {}", banner_type));
                }
                _ => {
                    commands.push(format!("banner {} ^{}^", banner_type, text));
                }
            }
            commands.push("commit".to_string());
            commands.push("end".to_string());
        }
        "juniper_junos" => {
            let junos_keyword = match banner_type {
                "login" => "message",
                _ => "announcement",
            };
            commands.push("configure".to_string());
            match state {
                "absent" => {
                    commands.push(format!("delete system login {}", junos_keyword));
                }
                _ => {
                    commands.push(format!(
                        "set system login {} \"{}\"",
                        junos_keyword, text
                    ));
                }
            }
            commands.push("commit".to_string());
            commands.push("exit".to_string());
        }
        _ => {}
    }

    commands
}

impl Executor for NetworkBannerExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let mut result = ResourceResult::new(
            "network_banner",
            params.get("name").map_or("", |s| s),
        );

        let ssh = match SshParams::from_params(params, result) {
            Ok(s) => s,
            Err(r) => return *r,
        };
        result = ResourceResult::new("network_banner", params.get("name").map_or("", |s| s));
        let device_type = require_param!(params, "device_type", result);
        let banner_type = require_param!(params, "banner_type", result);
        let text = require_param!(params, "text", result);
        let state = network_common::parse_state(params).to_string();
        let timeout = DEFAULT_TIMEOUT;

        let prompt = match prompt_pattern(&device_type) {
            Some(p) => p,
            None => return result.with_error(format!("unsupported device_type: {}", device_type)),
        };

        if banner_type != "login" && banner_type != "motd" {
            return result.with_error(format!("invalid banner_type: {} (must be login or motd)", banner_type));
        }

        let action_on_change = if state == "absent" {
            Action::Deleted
        } else {
            Action::Updated
        };

        let host_log = ssh.host.clone();
        network_common::run_ssh_executor_with_action(&mut result, start, &ssh, action_on_change, |session| {
            Box::pin(async move {
                // Check current state.
                let check_cmd = check_command(&device_type, &banner_type);
                let current = session.exec(&check_cmd, timeout).await?;
                let current_output = current.stdout;

                let is_present =
                    banner_present(&device_type, &banner_type, &current_output, &text);

                let needs_change = match state.as_str() {
                    "present" => !is_present,
                    "absent" => {
                        // For absent, any banner text in output means we need to remove it.
                        banner_present(&device_type, &banner_type, &current_output, &text)
                    }
                    _ => return Err(anyhow::anyhow!("invalid state: {}", state)),
                };

                if !needs_change {
                    return Ok(false);
                }

                if dry_run {
                    return Ok(true);
                }

                // Push config commands.
                let commands = build_commands(&device_type, &banner_type, &text, &state);
                let cmd_refs: Vec<&str> = commands.iter().map(|s| s.as_str()).collect();
                let _output = session
                    .exec_interactive(&cmd_refs, prompt, timeout)
                    .await?;

                debug!(
                    host = host_log.as_str(),
                    device_type = device_type.as_str(),
                    banner_type = banner_type.as_str(),
                    state = state.as_str(),
                    "banner applied"
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
    fn test_network_banner_missing_params() {
        let executor = NetworkBannerExecutor::new();
        let params = HashMap::new();
        let result = executor.execute(&params, false);
        assert!(result.error.is_some());
    }

    #[test]
    fn test_network_banner_missing_text() {
        let executor = NetworkBannerExecutor::new();
        let mut params = HashMap::new();
        params.insert("host".to_string(), "192.168.1.1".to_string());
        params.insert("username".to_string(), "admin".to_string());
        params.insert("credential".to_string(), "password123".to_string());
        params.insert("device_type".to_string(), "cisco_ios".to_string());
        params.insert("banner_type".to_string(), "login".to_string());
        let result = executor.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("text"));
    }

    #[test]
    fn test_prompt_pattern_cisco() {
        assert_eq!(prompt_pattern("cisco_ios"), Some("#"));
        assert_eq!(prompt_pattern("cisco_nxos"), Some("#"));
    }

    #[test]
    fn test_prompt_pattern_junos() {
        assert_eq!(prompt_pattern("juniper_junos"), Some(">"));
    }

    #[test]
    fn test_prompt_pattern_arista_eos() {
        assert_eq!(prompt_pattern("arista_eos"), Some("#"));
    }

    #[test]
    fn test_prompt_pattern_iosxr() {
        assert_eq!(prompt_pattern("cisco_iosxr"), Some("#"));
    }

    #[test]
    fn test_prompt_pattern_unknown() {
        assert!(prompt_pattern("unknown_vendor").is_none());
    }

    #[test]
    fn test_check_command_cisco() {
        assert_eq!(check_command("cisco_ios", "login"), "show banner login");
        assert_eq!(check_command("cisco_nxos", "motd"), "show banner motd");
    }

    #[test]
    fn test_check_command_junos() {
        assert_eq!(
            check_command("juniper_junos", "login"),
            "show configuration system login"
        );
    }

    #[test]
    fn test_banner_present_cisco() {
        assert!(banner_present("cisco_ios", "login", "Authorized access only", "Authorized access only"));
        assert!(!banner_present("cisco_ios", "login", "No banner set", "Authorized access only"));
    }

    #[test]
    fn test_banner_present_junos_login() {
        let output = "message \"Welcome to the system\";\n";
        assert!(banner_present("juniper_junos", "login", output, "Welcome to the system"));
        assert!(!banner_present("juniper_junos", "login", output, "Different text"));
    }

    #[test]
    fn test_banner_present_junos_motd() {
        let output = "announcement \"Maintenance window tonight\";\n";
        assert!(banner_present("juniper_junos", "motd", output, "Maintenance window tonight"));
        assert!(!banner_present("juniper_junos", "motd", "message \"login msg\";", "Maintenance window tonight"));
    }

    #[test]
    fn test_build_commands_cisco_present() {
        let cmds = build_commands("cisco_ios", "login", "Authorized only", "present");
        assert_eq!(cmds, vec![
            "configure terminal",
            "banner login ^Authorized only^",
            "end",
            "write memory",
        ]);
    }

    #[test]
    fn test_build_commands_cisco_absent() {
        let cmds = build_commands("cisco_nxos", "motd", "text", "absent");
        assert_eq!(cmds, vec![
            "configure terminal",
            "no banner motd",
            "end",
            "write memory",
        ]);
    }

    #[test]
    fn test_build_commands_junos_present_login() {
        let cmds = build_commands("juniper_junos", "login", "Welcome", "present");
        assert_eq!(cmds, vec![
            "configure",
            "set system login message \"Welcome\"",
            "commit",
            "exit",
        ]);
    }

    #[test]
    fn test_build_commands_junos_present_motd() {
        let cmds = build_commands("juniper_junos", "motd", "Notice", "present");
        assert_eq!(cmds, vec![
            "configure",
            "set system login announcement \"Notice\"",
            "commit",
            "exit",
        ]);
    }

    #[test]
    fn test_build_commands_junos_absent_login() {
        let cmds = build_commands("juniper_junos", "login", "text", "absent");
        assert_eq!(cmds, vec![
            "configure",
            "delete system login message",
            "commit",
            "exit",
        ]);
    }

    #[test]
    fn test_build_commands_junos_absent_motd() {
        let cmds = build_commands("juniper_junos", "motd", "text", "absent");
        assert_eq!(cmds, vec![
            "configure",
            "delete system login announcement",
            "commit",
            "exit",
        ]);
    }

    #[test]
    fn test_build_commands_arista_eos_present() {
        let cmds = build_commands("arista_eos", "login", "Authorized only", "present");
        assert_eq!(cmds, vec![
            "configure terminal",
            "banner login ^Authorized only^",
            "end",
            "write memory",
        ]);
    }

    #[test]
    fn test_build_commands_iosxr_present() {
        let cmds = build_commands("cisco_iosxr", "login", "Authorized only", "present");
        assert_eq!(cmds, vec![
            "configure terminal",
            "banner login ^Authorized only^",
            "commit",
            "end",
        ]);
    }

    #[test]
    fn test_build_commands_iosxr_absent() {
        let cmds = build_commands("cisco_iosxr", "motd", "text", "absent");
        assert_eq!(cmds, vec![
            "configure terminal",
            "no banner motd",
            "commit",
            "end",
        ]);
    }
}
