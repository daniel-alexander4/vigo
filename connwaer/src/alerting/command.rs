use anyhow::{Context, Result};
use std::time::Duration;

use crate::capability::CapabilityResult;
use crate::config::CommandTarget;
use crate::exec;

/// Hard cap on how long an alert command may run before it's killed. A hung
/// alert command must never block the dispatcher (which fires alerts inline on
/// the scheduler path).
const COMMAND_TIMEOUT: Duration = Duration::from_secs(30);

/// Run a local command with the result JSON piped to stdin.
pub fn run(target: &CommandTarget, result: &CapabilityResult) -> Result<()> {
    let json = serde_json::to_string(result)
        .context("serializing result for command alert")?;

    let args: Vec<&str> = target.args.iter().map(String::as_str).collect();
    let output = exec::run_with_stdin(
        &target.command,
        &args,
        Some(json.as_bytes()),
        COMMAND_TIMEOUT,
    )
    .with_context(|| format!("alert command '{}'", target.command))?;

    if !output.status.success() {
        anyhow::bail!(
            "alert command '{}' exited with {}",
            target.command,
            output.status
        );
    }
    tracing::debug!("alert command '{}' completed", target.command);
    Ok(())
}
