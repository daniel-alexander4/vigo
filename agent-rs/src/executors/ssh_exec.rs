// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0

//! ssh_exec executor — runs commands on remote devices over SSH.
//!
//! Params:
//! - host (required): target IP/hostname
//! - port (optional): SSH port, default 22
//! - username (required): SSH user
//! - credential (required): password or SSH key
//! - command (required): command to execute
//! - unless (optional): skip if this command succeeds (exit 0)
//! - onlyif (optional): only run if this command succeeds (exit 0)
//! - timeout (optional): per-command timeout in seconds (default 30)

use crate::executors::{Action, Executor, ResourceResult};
use crate::executors::network_common::SshParams;
use std::collections::HashMap;
use std::time::{Duration, Instant};
use tracing::debug;

pub struct SshExecExecutor;

impl SshExecExecutor {
    pub fn new() -> Self {
        Self
    }
}

impl Executor for SshExecExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let mut result = ResourceResult::new("ssh_exec", params.get("name").map_or("", |s| s));

        let ssh = match SshParams::from_params(params, result) {
            Ok(s) => s,
            Err(r) => return r,
        };
        result = ResourceResult::new("ssh_exec", params.get("name").map_or("", |s| s));
        let command = match params.get("command") {
            Some(c) => c.clone(),
            None => return result.with_error("missing required param: command".into()),
        };
        let timeout_secs: u64 = params
            .get("timeout")
            .and_then(|t| t.parse().ok())
            .unwrap_or(30);
        let timeout = Duration::from_secs(timeout_secs);

        // Clone guard params for the closure.
        let unless_cmd = params.get("unless").cloned();
        let onlyif_cmd = params.get("onlyif").cloned();

        // Bridge async SSH operations into the sync Executor trait.
        let rt = match tokio::runtime::Handle::try_current() {
            Ok(h) => h,
            Err(_) => {
                result.error = Some("no tokio runtime available".into());
                return result;
            }
        };

        let exec_result = tokio::task::block_in_place(|| {
            rt.block_on(async {
                let session = ssh.connect().await?;

                // Check "unless" guard — skip if it succeeds.
                if let Some(ref unless_cmd) = unless_cmd {
                    let guard = session.exec(unless_cmd, timeout).await?;
                    if guard.exit_code == 0 {
                        debug!(cmd = unless_cmd.as_str(), "unless guard succeeded, skipping");
                        return Ok(None); // skip
                    }
                }

                // Check "onlyif" guard — skip if it fails.
                if let Some(ref onlyif_cmd) = onlyif_cmd {
                    let guard = session.exec(onlyif_cmd, timeout).await?;
                    if guard.exit_code != 0 {
                        debug!(cmd = onlyif_cmd.as_str(), "onlyif guard failed, skipping");
                        return Ok(None); // skip
                    }
                }

                if dry_run {
                    return Ok(Some(("would execute".to_string(), true)));
                }

                let output = session.exec(&command, timeout).await?;
                if output.exit_code != 0 {
                    anyhow::bail!(
                        "command exited with code {}: stderr={}",
                        output.exit_code,
                        output.stderr.trim()
                    );
                }

                Ok(Some((output.stdout, true)))
            })
        });

        result.duration_ms = start.elapsed().as_millis() as i32;

        match exec_result {
            Ok(None) => {
                // Guard condition met — no change needed.
                result.action = Action::None;
                result.changed = false;
            }
            Ok(Some(_)) => {
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
    fn test_ssh_exec_missing_params() {
        let executor = SshExecExecutor::new();
        let params = HashMap::new();
        let result = executor.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("host"));
    }

    #[test]
    fn test_ssh_exec_missing_command() {
        let executor = SshExecExecutor::new();
        let mut params = HashMap::new();
        params.insert("host".to_string(), "10.0.0.1".to_string());
        params.insert("username".to_string(), "admin".to_string());
        params.insert("credential".to_string(), "secret".to_string());
        let result = executor.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("command"));
    }
}
