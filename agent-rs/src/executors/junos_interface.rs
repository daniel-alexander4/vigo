// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! junos_interface executor — manages interface configuration on Juniper Junos devices over SSH.
//!
//! Params:
//! - host (required): auto-injected
//! - credential (required): auto-injected
//! - username (required): auto-injected
//! - port (optional): SSH port, default 22
//! - interface (required): interface name, e.g. "ge-0/0/0"
//! - description (optional): interface description
//! - ip_address (optional): IP address with prefix, e.g. "10.0.1.1/24"
//! - unit (optional): logical unit number, default "0"
//! - family (optional): address family, default "inet"

use crate::executors::{Executor, ResourceResult};
use crate::executors::network_common::{self, require_param, SshParams, DEFAULT_TIMEOUT};
use std::collections::HashMap;
use std::time::Instant;
use tracing::debug;

pub struct JunosInterfaceExecutor;

impl JunosInterfaceExecutor {
    pub fn new() -> Self {
        Self
    }
}

impl Executor for JunosInterfaceExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let mut result = ResourceResult::new(
            "junos_interface",
            params.get("name").map_or("", |s| s),
        );

        let ssh = match SshParams::from_params(params, result) {
            Ok(s) => s,
            Err(r) => return r,
        };
        result = ResourceResult::new("junos_interface", params.get("name").map_or("", |s| s));
        let interface = require_param!(params, "interface", result);
        let unit = params.get("unit").map_or("0", |s| s.as_str()).to_string();
        let family = params
            .get("family")
            .map_or("inet", |s| s.as_str())
            .to_string();
        let description = params.get("description").cloned();
        let ip_address = params.get("ip_address").cloned();
        let timeout = DEFAULT_TIMEOUT;

        let host_log = ssh.host.clone();
        network_common::run_ssh_executor(&mut result, start, &ssh, |session| {
            Box::pin(async move {
                // Check current interface configuration.
                let check_cmd = format!("show configuration interfaces {}", interface);
                let current = session.exec(&check_cmd, timeout).await?;
                let current_config = current.stdout;

                // Compare desired fields against current config.
                let mut needs_change = false;

                if let Some(ref desc) = description {
                    if !current_config.contains(&format!("description \"{}\"", desc))
                        && !current_config.contains(&format!("description {}", desc))
                    {
                        needs_change = true;
                    }
                }

                if let Some(ref addr) = ip_address {
                    if !current_config.contains(addr) {
                        needs_change = true;
                    }
                }

                if !needs_change {
                    return Ok(false);
                }

                if dry_run {
                    return Ok(true);
                }

                // Build Junos set commands.
                let mut commands: Vec<String> = vec!["configure".to_string()];

                if let Some(ref desc) = description {
                    commands.push(format!(
                        "set interfaces {} unit {} description \"{}\"",
                        interface, unit, desc
                    ));
                }

                if let Some(ref addr) = ip_address {
                    commands.push(format!(
                        "set interfaces {} unit {} family {} address {}",
                        interface, unit, family, addr
                    ));
                }

                commands.push("commit".to_string());
                commands.push("exit".to_string());

                // Execute config commands interactively.
                let cmd_refs: Vec<&str> = commands.iter().map(|s| s.as_str()).collect();
                let _output = session
                    .exec_interactive(&cmd_refs, ">", timeout)
                    .await?;

                debug!(
                    host = host_log.as_str(),
                    interface = interface.as_str(),
                    "junos interface config applied"
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
    fn test_junos_interface_missing_params() {
        let executor = JunosInterfaceExecutor::new();
        let params = HashMap::new();
        let result = executor.execute(&params, false);
        assert!(result.error.is_some());
    }

    #[test]
    fn test_junos_interface_missing_interface() {
        let executor = JunosInterfaceExecutor::new();
        let mut params = HashMap::new();
        params.insert("host".to_string(), "10.0.0.1".to_string());
        params.insert("username".to_string(), "admin".to_string());
        params.insert("credential".to_string(), "secret".to_string());
        let result = executor.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("interface"));
    }
}
