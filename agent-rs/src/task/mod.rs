// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0

//! Ad-hoc task executor for orchestration.
//!
//! Runs shell commands dispatched by the server with timeout enforcement,
//! output capture, and clean subprocess termination on stream disconnect.

use crate::proto::{agent_stream_message, AgentStreamMessage, TaskAck, TaskDispatch, TaskResult};
use std::time::Instant;
use tokio::io::AsyncReadExt;
use tokio::sync::mpsc;
use tokio_util::sync::CancellationToken;
use tracing::{info, warn};

/// Maximum stdout capture: 1 MB.
const MAX_STDOUT: usize = 1_048_576;

/// Maximum stderr capture: 256 KB.
const MAX_STDERR: usize = 262_144;

/// Default timeout when the server sends 0.
const DEFAULT_TIMEOUT_SECS: u64 = 60;

/// Grace period between SIGTERM and SIGKILL.
const KILL_GRACE_SECS: u64 = 5;

/// Send a TaskAck on the stream.
pub async fn send_ack(
    tx: &mpsc::Sender<AgentStreamMessage>,
    task_id: &str,
    accepted: bool,
    reject_reason: &str,
) {
    let msg = AgentStreamMessage {
        msg: Some(agent_stream_message::Msg::TaskAck(TaskAck {
            task_id: task_id.to_string(),
            accepted,
            reject_reason: reject_reason.to_string(),
        })),
    };
    if let Err(e) = tx.send(msg).await {
        warn!(task_id = task_id, error = %e, "failed to send task ack");
    }
}

/// Send a TaskResult on the stream.
pub async fn send_result(tx: &mpsc::Sender<AgentStreamMessage>, result: TaskResult) {
    let msg = AgentStreamMessage {
        msg: Some(agent_stream_message::Msg::TaskResult(result)),
    };
    if let Err(e) = tx.send(msg).await {
        warn!(error = %e, "failed to send task result");
    }
}

/// Execute a dispatched task and return the result.
///
/// The `cancel` token is tied to the stream lifecycle — when the stream
/// closes, any running subprocess receives SIGTERM then SIGKILL.
pub async fn execute(dispatch: &TaskDispatch, cancel: &CancellationToken) -> TaskResult {
    let start = Instant::now();
    let task_id = &dispatch.task_id;

    let timeout_secs = if dispatch.timeout_seconds <= 0 {
        DEFAULT_TIMEOUT_SECS
    } else {
        dispatch.timeout_seconds as u64
    };

    info!(
        task_id = task_id,
        command = &dispatch.command,
        timeout_secs = timeout_secs,
        "executing task"
    );

    // Build the command via platform shell.
    let (shell, shell_arg) = crate::platform::paths::shell_command();
    let mut cmd = tokio::process::Command::new(shell);
    cmd.arg(shell_arg).arg(&dispatch.command);

    // Inject environment variables from the dispatch.
    for (k, v) in &dispatch.env {
        cmd.env(k, v);
    }

    // Capture stdout and stderr.
    cmd.stdout(std::process::Stdio::piped());
    cmd.stderr(std::process::Stdio::piped());

    // Create a new process group so we can signal the entire group.
    crate::platform::process::configure_process_group(&mut cmd);

    let mut child = match cmd.spawn() {
        Ok(c) => c,
        Err(e) => {
            return TaskResult {
                task_id: task_id.clone(),
                exit_code: -1,
                stdout: Vec::new(),
                stderr: Vec::new(),
                status: "failed".to_string(),
                duration_ms: start.elapsed().as_millis() as i32,
                error_message: format!("spawn failed: {}", e),
            };
        }
    };

    let pid = match child.id() {
        Some(pid) => pid as i32,
        None => {
            // Process already exited before we could get the PID.
            return collect_child_output(&mut child, task_id, &start, "completed").await;
        }
    };

    // Take stdout/stderr handles so we can read them after wait().
    let stdout_pipe = child.stdout.take();
    let stderr_pipe = child.stderr.take();

    let timeout = tokio::time::sleep(std::time::Duration::from_secs(timeout_secs));
    tokio::pin!(timeout);

    // Wait for the child, respecting timeout and cancellation.
    let (wait_status, status_str) = tokio::select! {
        result = child.wait() => {
            match result {
                Ok(status) => (Some(status), "completed"),
                Err(e) => {
                    return TaskResult {
                        task_id: task_id.clone(),
                        exit_code: -1,
                        stdout: Vec::new(),
                        stderr: Vec::new(),
                        status: "failed".to_string(),
                        duration_ms: start.elapsed().as_millis() as i32,
                        error_message: format!("wait failed: {}", e),
                    };
                }
            }
        }
        _ = &mut timeout => {
            warn!(task_id = task_id, "task timed out after {}s", timeout_secs);
            kill_process_group(pid).await;
            (child.wait().await.ok(), "timeout")
        }
        _ = cancel.cancelled() => {
            warn!(task_id = task_id, "stream closed, killing task subprocess");
            kill_process_group(pid).await;
            (child.wait().await.ok(), "killed")
        }
    };

    // Read captured output from pipes.
    let stdout = read_pipe(stdout_pipe, MAX_STDOUT).await;
    let stderr = read_pipe(stderr_pipe, MAX_STDERR).await;

    let exit_code = wait_status.and_then(|s| s.code()).unwrap_or(-1);
    let actual_status = if exit_code != 0 && status_str == "completed" {
        "failed"
    } else {
        status_str
    };

    TaskResult {
        task_id: task_id.clone(),
        exit_code,
        stdout,
        stderr,
        status: actual_status.to_string(),
        duration_ms: start.elapsed().as_millis() as i32,
        error_message: String::new(),
    }
}

/// Wait for a child that may have already exited, collect output.
async fn collect_child_output(
    child: &mut tokio::process::Child,
    task_id: &str,
    start: &Instant,
    status: &str,
) -> TaskResult {
    let stdout_pipe = child.stdout.take();
    let stderr_pipe = child.stderr.take();
    let wait_status = child.wait().await.ok();
    let stdout = read_pipe(stdout_pipe, MAX_STDOUT).await;
    let stderr = read_pipe(stderr_pipe, MAX_STDERR).await;
    let exit_code = wait_status.and_then(|s| s.code()).unwrap_or(-1);
    let actual_status = if exit_code != 0 && status == "completed" {
        "failed"
    } else {
        status
    };

    TaskResult {
        task_id: task_id.to_string(),
        exit_code,
        stdout,
        stderr,
        status: actual_status.to_string(),
        duration_ms: start.elapsed().as_millis() as i32,
        error_message: String::new(),
    }
}

/// Read from an optional pipe into a Vec, truncating at max bytes.
///
/// Uses a trait-based approach to handle both ChildStdout and ChildStderr.
async fn read_pipe<R: tokio::io::AsyncRead + Unpin>(pipe: Option<R>, max: usize) -> Vec<u8> {
    let pipe = match pipe {
        Some(p) => p,
        None => return Vec::new(),
    };
    let mut buf = Vec::new();
    // Read up to max + 1 to detect truncation.
    let limit = (max + 1) as u64;
    match pipe.take(limit).read_to_end(&mut buf).await {
        Ok(_) => truncate_vec(buf, max),
        Err(_) => Vec::new(),
    }
}

/// Kill a process group (platform-specific).
async fn kill_process_group(pid: i32) {
    crate::platform::process::kill_process_tree(
        pid as u32,
        std::time::Duration::from_secs(KILL_GRACE_SECS),
    )
    .await;
}

/// Truncate a Vec<u8> in place, appending a marker if truncated.
fn truncate_vec(mut data: Vec<u8>, max: usize) -> Vec<u8> {
    if data.len() <= max {
        return data;
    }
    let marker = b"\n... [output truncated] ...";
    let trim_to = max.saturating_sub(marker.len());
    data.truncate(trim_to);
    data.extend_from_slice(marker);
    data
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::HashMap;

    #[test]
    fn truncate_vec_preserves_small() {
        let data = vec![b'a'; 50];
        let result = truncate_vec(data.clone(), 100);
        assert_eq!(result, data);
    }

    #[test]
    fn truncate_vec_exact() {
        let data = vec![b'h', b'e', b'l', b'l', b'o'];
        let result = truncate_vec(data.clone(), 5);
        assert_eq!(result, data);
    }

    #[test]
    fn truncate_vec_truncates_large() {
        let data = vec![b'x'; 2048];
        let result = truncate_vec(data, 100);
        assert!(result.len() <= 100);
        assert!(result.ends_with(b"... [output truncated] ..."));
    }

    #[tokio::test]
    async fn execute_echo() {
        let cancel = CancellationToken::new();
        let dispatch = TaskDispatch {
            task_id: "test-1".to_string(),
            command: "echo hello".to_string(),
            timeout_seconds: 10,
            env: HashMap::new(),
        };

        let result = execute(&dispatch, &cancel).await;
        assert_eq!(result.task_id, "test-1");
        assert_eq!(result.exit_code, 0);
        assert_eq!(result.status, "completed");
        assert_eq!(String::from_utf8_lossy(&result.stdout).trim(), "hello");
        assert!(result.duration_ms >= 0);
    }

    #[tokio::test]
    async fn execute_with_env() {
        let cancel = CancellationToken::new();
        let mut env = HashMap::new();
        env.insert("MY_VAR".to_string(), "test_value".to_string());

        let dispatch = TaskDispatch {
            task_id: "test-env".to_string(),
            command: "echo $MY_VAR".to_string(),
            timeout_seconds: 10,
            env,
        };

        let result = execute(&dispatch, &cancel).await;
        assert_eq!(result.exit_code, 0);
        assert_eq!(
            String::from_utf8_lossy(&result.stdout).trim(),
            "test_value"
        );
    }

    #[tokio::test]
    async fn execute_nonzero_exit() {
        let cancel = CancellationToken::new();
        let dispatch = TaskDispatch {
            task_id: "test-fail".to_string(),
            command: "exit 42".to_string(),
            timeout_seconds: 10,
            env: HashMap::new(),
        };

        let result = execute(&dispatch, &cancel).await;
        assert_eq!(result.exit_code, 42);
        assert_eq!(result.status, "failed");
    }

    #[tokio::test]
    async fn execute_stderr_capture() {
        let cancel = CancellationToken::new();
        let dispatch = TaskDispatch {
            task_id: "test-stderr".to_string(),
            command: "echo error_output >&2".to_string(),
            timeout_seconds: 10,
            env: HashMap::new(),
        };

        let result = execute(&dispatch, &cancel).await;
        assert_eq!(result.exit_code, 0);
        assert_eq!(
            String::from_utf8_lossy(&result.stderr).trim(),
            "error_output"
        );
    }

    #[tokio::test]
    async fn execute_timeout() {
        let cancel = CancellationToken::new();
        let dispatch = TaskDispatch {
            task_id: "test-timeout".to_string(),
            command: "sleep 60".to_string(),
            timeout_seconds: 1,
            env: HashMap::new(),
        };

        let result = execute(&dispatch, &cancel).await;
        assert_eq!(result.status, "timeout");
        assert!(result.duration_ms >= 1000);
    }

    #[tokio::test]
    async fn execute_cancel_kills_subprocess() {
        let cancel = CancellationToken::new();
        let dispatch = TaskDispatch {
            task_id: "test-cancel".to_string(),
            command: "sleep 60".to_string(),
            timeout_seconds: 30,
            env: HashMap::new(),
        };

        let cancel_clone = cancel.clone();
        tokio::spawn(async move {
            tokio::time::sleep(std::time::Duration::from_millis(200)).await;
            cancel_clone.cancel();
        });

        let result = execute(&dispatch, &cancel).await;
        assert_eq!(result.status, "killed");
    }

    #[tokio::test]
    async fn execute_default_timeout() {
        // Verify that timeout_seconds=0 uses default (60s), not instant timeout.
        let cancel = CancellationToken::new();
        let dispatch = TaskDispatch {
            task_id: "test-default-timeout".to_string(),
            command: "echo fast".to_string(),
            timeout_seconds: 0,
            env: HashMap::new(),
        };

        let result = execute(&dispatch, &cancel).await;
        assert_eq!(result.exit_code, 0);
        assert_eq!(result.status, "completed");
    }
}
