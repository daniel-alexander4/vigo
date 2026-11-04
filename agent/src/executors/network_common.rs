// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Shared helpers for SSH-based network device executors.

use crate::executors::{Action, ResourceResult};
use crate::transport::ssh::SshSession;
use std::collections::HashMap;
use std::time::{Duration, Instant};

/// Extract a required parameter or return an error result.
macro_rules! require_param {
    ($params:expr, $key:expr, $result:expr) => {
        match $params.get($key) {
            Some(v) => v.clone(),
            None => return $result.with_error(format!("missing required param: {}", $key)),
        }
    };
}

pub(crate) use require_param;

/// Common SSH connection parameters extracted from executor params.
pub struct SshParams {
    pub host: String,
    pub port: u16,
    pub username: String,
    pub credential: String,
}

impl SshParams {
    /// Extract SSH connection params from the executor param map.
    /// Returns `Err(Box<ResourceResult>)` if required params are missing —
    /// boxed because `ResourceResult` is large and would bloat every
    /// caller's `Result` otherwise.
    pub fn from_params(
        params: &HashMap<String, String>,
        result: ResourceResult,
    ) -> Result<Self, Box<ResourceResult>> {
        let host = match params.get("host") {
            Some(h) => h.clone(),
            None => return Err(Box::new(result.with_error("missing required param: host".into()))),
        };
        let username = match params.get("username") {
            Some(u) => u.clone(),
            None => {
                return Err(Box::new(
                    result.with_error("missing required param: username".into()),
                ))
            }
        };
        let credential = match params.get("credential") {
            Some(c) => c.clone(),
            None => {
                return Err(Box::new(
                    result.with_error("missing required param: credential".into()),
                ))
            }
        };
        let port: u16 = params
            .get("port")
            .and_then(|p| p.parse().ok())
            .unwrap_or(22);

        Ok(Self {
            host,
            port,
            username,
            credential,
        })
    }

    /// Open an SSH session with these params.
    pub async fn connect(&self) -> anyhow::Result<SshSession> {
        SshSession::connect(&self.host, self.port, &self.username, &self.credential).await
    }
}

/// Run an async SSH operation from a sync executor context.
/// Handles the tokio bridge (block_in_place) and maps the result to a ResourceResult.
///
/// The closure receives a connected SshSession and should return:
/// - Ok(false) for no change needed
/// - Ok(true) for change applied (or would apply in dry_run)
/// - Err for failures
pub fn run_ssh_executor<F>(
    result: &mut ResourceResult,
    start: Instant,
    ssh: &SshParams,
    op: F,
) where
    F: FnOnce(SshSession) -> std::pin::Pin<Box<dyn std::future::Future<Output = anyhow::Result<bool>> + Send>>,
{
    let rt = match tokio::runtime::Handle::try_current() {
        Ok(h) => h,
        Err(_) => {
            result.error = Some("no tokio runtime available".into());
            return;
        }
    };

    let exec_result = tokio::task::block_in_place(|| {
        rt.block_on(async {
            let session = ssh.connect().await?;
            op(session).await
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
}

/// Like run_ssh_executor but allows setting the action for true results.
pub fn run_ssh_executor_with_action<F>(
    result: &mut ResourceResult,
    start: Instant,
    ssh: &SshParams,
    action_on_change: Action,
    op: F,
) where
    F: FnOnce(SshSession) -> std::pin::Pin<Box<dyn std::future::Future<Output = anyhow::Result<bool>> + Send>>,
{
    let rt = match tokio::runtime::Handle::try_current() {
        Ok(h) => h,
        Err(_) => {
            result.error = Some("no tokio runtime available".into());
            return;
        }
    };

    let exec_result = tokio::task::block_in_place(|| {
        rt.block_on(async {
            let session = ssh.connect().await?;
            op(session).await
        })
    });

    result.duration_ms = start.elapsed().as_millis() as i32;

    match exec_result {
        Ok(false) => {
            result.action = Action::None;
            result.changed = false;
        }
        Ok(true) => {
            result.action = action_on_change;
            result.changed = true;
        }
        Err(e) => {
            result.error = Some(e.to_string());
        }
    }
}

/// Default SSH command timeout.
pub const DEFAULT_TIMEOUT: Duration = Duration::from_secs(30);

/// Parse port from params, default 22.
pub fn parse_port(params: &HashMap<String, String>) -> u16 {
    params.get("port").and_then(|p| p.parse().ok()).unwrap_or(22)
}

/// Parse state from params, default "present".
pub fn parse_state(params: &HashMap<String, String>) -> &str {
    params.get("state").map_or("present", |s| s.as_str())
}
