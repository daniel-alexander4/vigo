use anyhow::{Context, Result};
use std::io::Write;
use std::process::{Command, Stdio};
use std::time::{Duration, Instant};

use crate::capability::CapabilityResult;
use crate::config::CommandTarget;

/// Hard cap on how long an alert command may run before it's killed. A hung
/// alert command must never block the dispatcher (which fires alerts inline on
/// the scheduler path).
const COMMAND_TIMEOUT: Duration = Duration::from_secs(30);

/// Run a local command with the result JSON piped to stdin.
pub fn run(target: &CommandTarget, result: &CapabilityResult) -> Result<()> {
    let json = serde_json::to_string(result)
        .context("serializing result for command alert")?;

    let mut cmd = Command::new(&target.command);
    for arg in &target.args {
        cmd.arg(arg);
    }
    cmd.stdin(Stdio::piped());
    cmd.stdout(Stdio::null());
    cmd.stderr(Stdio::null());

    let mut child = cmd.spawn()
        .with_context(|| format!("spawning alert command: {}", target.command))?;

    // Write the result JSON, then drop stdin so the child sees EOF.
    if let Some(mut stdin) = child.stdin.take() {
        let _ = stdin.write_all(json.as_bytes());
    }

    // Bounded wait: poll for completion until the deadline, then kill.
    let deadline = Instant::now() + COMMAND_TIMEOUT;
    loop {
        match child.try_wait() {
            Ok(Some(status)) => {
                if !status.success() {
                    anyhow::bail!(
                        "alert command '{}' exited with {}",
                        target.command,
                        status
                    );
                }
                tracing::debug!("alert command '{}' completed", target.command);
                return Ok(());
            }
            Ok(None) => {
                if Instant::now() >= deadline {
                    let _ = child.kill();
                    let _ = child.wait();
                    anyhow::bail!(
                        "alert command '{}' timed out after {}s",
                        target.command,
                        COMMAND_TIMEOUT.as_secs()
                    );
                }
                std::thread::sleep(Duration::from_millis(50));
            }
            Err(e) => {
                return Err(e).with_context(|| {
                    format!("waiting for alert command: {}", target.command)
                });
            }
        }
    }
}
