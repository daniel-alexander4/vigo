use anyhow::{Context, Result};
use std::io::Write;
use std::process::{Command, Stdio};

use crate::capability::CapabilityResult;
use crate::config::CommandTarget;

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
    cmd.stderr(Stdio::piped());

    let mut child = cmd.spawn()
        .with_context(|| format!("spawning alert command: {}", target.command))?;

    if let Some(mut stdin) = child.stdin.take() {
        let _ = stdin.write_all(json.as_bytes());
    }

    let output = child.wait_with_output()
        .with_context(|| format!("waiting for alert command: {}", target.command))?;

    if !output.status.success() {
        let stderr = String::from_utf8_lossy(&output.stderr);
        anyhow::bail!(
            "alert command '{}' exited with {}: {}",
            target.command,
            output.status,
            stderr.trim()
        );
    }

    tracing::debug!("alert command '{}' completed", target.command);
    Ok(())
}
