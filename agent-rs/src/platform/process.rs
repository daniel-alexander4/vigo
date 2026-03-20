// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Cross-platform process management.

use std::time::Duration;

/// Kill a process and all its children.
///
/// On Unix: SIGTERM to process group, wait grace period, SIGKILL.
/// On Windows: taskkill /PID /T /F.
#[cfg(unix)]
pub async fn kill_process_tree(pid: u32, grace: Duration) {
    let pgid = nix::unistd::Pid::from_raw(pid as i32);
    if let Err(e) = nix::sys::signal::killpg(pgid, nix::sys::signal::Signal::SIGTERM) {
        tracing::warn!(pid = pid, error = %e, "SIGTERM to process group failed");
    }
    tokio::time::sleep(grace).await;
    let _ = nix::sys::signal::killpg(pgid, nix::sys::signal::Signal::SIGKILL);
}

#[cfg(windows)]
pub async fn kill_process_tree(pid: u32, _grace: Duration) {
    // taskkill /PID <pid> /T /F kills the process tree forcefully.
    let _ = tokio::process::Command::new("taskkill")
        .args(["/PID", &pid.to_string(), "/T", "/F"])
        .output()
        .await;
}

/// Set up a new process group before exec (Unix only).
/// On Windows, process groups work differently and this is a no-op.
#[cfg(unix)]
pub fn configure_process_group(cmd: &mut tokio::process::Command) {
    unsafe {
        cmd.pre_exec(|| {
            libc::setpgid(0, 0);
            Ok(())
        });
    }
}

#[cfg(windows)]
pub fn configure_process_group(_cmd: &mut tokio::process::Command) {
    // Windows processes are created in a new process group by default via
    // CREATE_NEW_PROCESS_GROUP, but we don't need it for our use case.
}
