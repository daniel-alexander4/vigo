// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! cisco_vlan executor — manages VLANs on Cisco IOS/NX-OS switches over SSH.
//!
//! Params:
//! - host (required): switch hostname or IP
//! - username (required): SSH username
//! - credential (required): SSH password or key
//! - port (optional): SSH port, default 22
//! - vlan_id (required): VLAN number (1-4094)
//! - name (required): VLAN name
//! - state (optional): present (default) or absent

use crate::executors::{Action, Executor, ResourceResult};
use crate::transport::ssh::SshSession;
use std::collections::HashMap;
use std::time::{Duration, Instant};
use tracing::debug;

pub struct CiscoVlanExecutor;

impl CiscoVlanExecutor {
    pub fn new() -> Self {
        Self
    }
}

/// Parse `show vlan brief` output to find a VLAN by ID.
/// Returns Some(name) if the VLAN exists, None otherwise.
///
/// Expected format (Cisco IOS/NX-OS):
/// ```text
/// VLAN Name                             Status    Ports
/// ---- -------------------------------- --------- ---
/// 1    default                          active    Gi0/1, Gi0/2
/// 100  engineering                      active    Gi0/3
/// ```
fn parse_vlan_brief(output: &str, vlan_id: &str) -> Option<String> {
    for line in output.lines() {
        let trimmed = line.trim();
        if trimmed.is_empty() {
            continue;
        }
        let fields: Vec<&str> = trimmed.split_whitespace().collect();
        if fields.len() >= 2 && fields[0] == vlan_id {
            return Some(fields[1].to_string());
        }
    }
    None
}

impl Executor for CiscoVlanExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let mut result = ResourceResult::new(
            "cisco_vlan",
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
        let vlan_id = match params.get("vlan_id") {
            Some(v) => v.clone(),
            None => return result.with_error("missing required param: vlan_id".into()),
        };
        let vlan_name = match params.get("name") {
            Some(n) => n.clone(),
            None => return result.with_error("missing required param: name".into()),
        };

        let port: u16 = params
            .get("port")
            .and_then(|p| p.parse().ok())
            .unwrap_or(22);
        let state = params.get("state").map_or("present", |s| s.as_str());
        let timeout = Duration::from_secs(30);

        if state != "present" && state != "absent" {
            return result.with_error(format!("invalid state: {} (must be present or absent)", state));
        }

        let rt = match tokio::runtime::Handle::try_current() {
            Ok(h) => h,
            Err(_) => return result.with_error("no tokio runtime available".into()),
        };

        let exec_result = tokio::task::block_in_place(|| {
            rt.block_on(async {
                let session = SshSession::connect(&host, port, &username, &credential, None).await?;

                // Check current VLAN state.
                let output = session.exec("show vlan brief", timeout).await?;
                let existing_name = parse_vlan_brief(&output.stdout, &vlan_id);

                match state {
                    "present" => {
                        if let Some(ref current_name) = existing_name {
                            if current_name == &vlan_name {
                                // VLAN exists with correct name — no change needed.
                                return Ok::<bool, anyhow::Error>(false);
                            }
                        }
                        // VLAN missing or name mismatch — need to create/update.
                        if dry_run {
                            return Ok(true);
                        }

                        let commands = [
                            "configure terminal",
                            &format!("vlan {}", vlan_id),
                            &format!("name {}", vlan_name),
                            "end",
                            "write memory",
                        ];
                        let cmd_refs: Vec<&str> = commands.iter().map(|s| s.as_ref()).collect();
                        let _output = session.exec_interactive(&cmd_refs, "#", timeout).await?;

                        debug!(
                            host = host.as_str(),
                            vlan_id = vlan_id.as_str(),
                            vlan_name = vlan_name.as_str(),
                            "vlan configured"
                        );
                        Ok(true)
                    }
                    "absent" => {
                        if existing_name.is_none() {
                            // VLAN doesn't exist — no change needed.
                            return Ok(false);
                        }
                        if dry_run {
                            return Ok(true);
                        }

                        let commands = [
                            "configure terminal",
                            &format!("no vlan {}", vlan_id),
                            "end",
                            "write memory",
                        ];
                        let cmd_refs: Vec<&str> = commands.iter().map(|s| s.as_ref()).collect();
                        let _output = session.exec_interactive(&cmd_refs, "#", timeout).await?;

                        debug!(
                            host = host.as_str(),
                            vlan_id = vlan_id.as_str(),
                            "vlan removed"
                        );
                        Ok(true)
                    }
                    _ => unreachable!(),
                }
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
    fn test_parse_vlan_brief_found() {
        let output = "\
VLAN Name                             Status    Ports
---- -------------------------------- --------- ---
1    default                          active    Gi0/1, Gi0/2
100  engineering                      active    Gi0/3
200  marketing                        active    Gi0/4";
        assert_eq!(parse_vlan_brief(output, "100"), Some("engineering".to_string()));
        assert_eq!(parse_vlan_brief(output, "200"), Some("marketing".to_string()));
    }

    #[test]
    fn test_parse_vlan_brief_not_found() {
        let output = "\
VLAN Name                             Status    Ports
---- -------------------------------- --------- ---
1    default                          active    Gi0/1";
        assert_eq!(parse_vlan_brief(output, "100"), None);
    }

    #[test]
    fn test_cisco_vlan_missing_params() {
        let executor = CiscoVlanExecutor::new();
        let params = HashMap::new();
        let result = executor.execute(&params, false);
        assert!(result.error.is_some());
    }

    #[test]
    fn test_cisco_vlan_missing_vlan_id() {
        let executor = CiscoVlanExecutor::new();
        let mut params = HashMap::new();
        params.insert("host".to_string(), "10.0.0.1".to_string());
        params.insert("username".to_string(), "admin".to_string());
        params.insert("credential".to_string(), "secret".to_string());
        let result = executor.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("vlan_id"));
    }
}
