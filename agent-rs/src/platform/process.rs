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

/// Apply nice (CPU) and ionice (I/O) priority to a std::process::Command.
/// `nice_level` is clamped to 0..=19. On Linux, also sets I/O priority to
/// best-effort with a proportional level (nice 19 → ionice 7, nice 0 → ionice 0).
#[cfg(unix)]
pub fn apply_nice(cmd: &mut std::process::Command, nice_level: i32) {
    use std::os::unix::process::CommandExt;
    let level = nice_level.clamp(0, 19);
    unsafe {
        cmd.pre_exec(move || {
            libc::nice(level);

            #[cfg(target_os = "linux")]
            {
                // Map nice 0..19 → ionice level 0..7 (best-effort class).
                let io_level = (level * 7 / 19).min(7);
                let ioprio = (2 << 13) | io_level;
                libc::syscall(libc::SYS_ioprio_set, 1, 0, ioprio);
            }

            Ok(())
        });
    }
}

#[cfg(windows)]
pub fn apply_nice(_cmd: &mut std::process::Command, _nice_level: i32) {
    // Windows priority classes are set differently; not yet implemented.
}

/// Apply nice (CPU) and ionice (I/O) priority to a tokio::process::Command.
/// Same semantics as `apply_nice` for std commands.
#[cfg(unix)]
pub fn apply_nice_tokio(cmd: &mut tokio::process::Command, nice_level: i32) {
    let level = nice_level.clamp(0, 19);
    unsafe {
        cmd.pre_exec(move || {
            libc::nice(level);

            #[cfg(target_os = "linux")]
            {
                let io_level = (level * 7 / 19).min(7);
                let ioprio = (2 << 13) | io_level;
                libc::syscall(libc::SYS_ioprio_set, 1, 0, ioprio);
            }

            Ok(())
        });
    }
}

#[cfg(windows)]
pub fn apply_nice_tokio(_cmd: &mut tokio::process::Command, _nice_level: i32) {}
