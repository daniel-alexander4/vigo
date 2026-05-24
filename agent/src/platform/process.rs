// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Cross-platform process management.

use std::time::Duration;

/// Kill a process and all its children.
///
/// On Unix: SIGTERM to process group, wait grace period, SIGKILL.
/// On Windows: taskkill /PID /T /F.
///
/// Synchronous on purpose. The work (signal + blocking sleep, or one
/// `taskkill` exec) is entirely syscall-level; making this async added
/// nothing except forcing every sync caller (`exec.rs`,
/// `executors/mod.rs` × 2) to build a throwaway `tokio::runtime` per
/// invocation. The one async caller (`push::kill_process_group`)
/// hops to `tokio::task::spawn_blocking` — the canonical bridge for
/// sync work from an async context — so a 5s SIGTERM-wait doesn't
/// stall a runtime worker.
#[cfg(unix)]
pub fn kill_process_tree(pid: u32, grace: Duration) {
    let pgid = nix::unistd::Pid::from_raw(pid as i32);
    if let Err(e) = nix::sys::signal::killpg(pgid, nix::sys::signal::Signal::SIGTERM) {
        tracing::warn!(pid = pid, error = %e, "SIGTERM to process group failed");
    }
    std::thread::sleep(grace);
    let _ = nix::sys::signal::killpg(pgid, nix::sys::signal::Signal::SIGKILL);
}

#[cfg(windows)]
pub fn kill_process_tree(pid: u32, _grace: Duration) {
    // taskkill /PID <pid> /T /F kills the process tree forcefully.
    let _ = std::process::Command::new("taskkill")
        .args(["/PID", &pid.to_string(), "/T", "/F"])
        .output();
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
/// `nice_level` is clamped to 0..=19.
///
/// On Linux, I/O priority is set via `ioprio_set`:
///   - `nice_level >= IDLE_THRESHOLD` → `IOPRIO_CLASS_IDLE` (class 3). The
///     process only gets disk I/O when no best-effort tasks are waiting.
///     This is what callers asking for nice 19 actually want — "invisible
///     to anything on the hot path" — because best-effort 7 still competes
///     with ordinary best-effort I/O, just with smaller time slices.
///   - `nice_level < IDLE_THRESHOLD` → `IOPRIO_CLASS_BE` with a proportional
///     level (`io_level = level * 7 / 19`). Kept for callers that want
///     "lower priority but still responsive" — a nice 10 shouldn't yield
///     the disk to idle, it should just weigh less.
#[cfg(unix)]
pub fn apply_nice(cmd: &mut std::process::Command, nice_level: i32) {
    use std::os::unix::process::CommandExt;
    let level = nice_level.clamp(0, 19);
    unsafe {
        cmd.pre_exec(move || {
            libc::nice(level);

            #[cfg(target_os = "linux")]
            {
                libc::syscall(libc::SYS_ioprio_set, 1, 0, ioprio_for_nice(level));
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
                libc::syscall(libc::SYS_ioprio_set, 1, 0, ioprio_for_nice(level));
            }

            Ok(())
        });
    }
}

/// Threshold at which `apply_nice*` switches from proportional best-effort
/// ionice to the idle class. Chosen so operator-set nice values up to 14
/// keep competing (as "lower priority" best-effort), while the check-in-
/// path default of 19 for background execs drops into idle.
#[cfg(target_os = "linux")]
const IDLE_THRESHOLD: i32 = 15;

/// Compute the `ioprio` word for a given nice level. Exposed as its own
/// helper so both the std and tokio `apply_nice*` paths share one rule
/// and the threshold behavior is directly testable without a live child.
///
/// The encoding follows `include/linux/ioprio.h`:
///   ioprio = (class << 13) | level
///   IOPRIO_CLASS_BE    = 2
///   IOPRIO_CLASS_IDLE  = 3   (level ignored)
#[cfg(target_os = "linux")]
pub(crate) fn ioprio_for_nice(nice_level: i32) -> i32 {
    let level = nice_level.clamp(0, 19);
    if level >= IDLE_THRESHOLD {
        // Idle class — level bits are ignored.
        3 << 13
    } else {
        // Best-effort with proportional level.
        let io_level = (level * 7 / 19).min(7);
        (2 << 13) | io_level
    }
}

#[cfg(windows)]
pub fn apply_nice_tokio(_cmd: &mut tokio::process::Command, _nice_level: i32) {}

/// Default memory limit for exec resources (512 MiB).
pub const DEFAULT_EXEC_MEMORY_LIMIT: u64 = 512 * 1024 * 1024;

/// Apply a virtual memory limit to a std::process::Command via RLIMIT_AS.
/// Set in pre_exec so the child inherits the limit. Non-fatal on error.
#[cfg(unix)]
pub fn apply_memory_limit(cmd: &mut std::process::Command, memory_bytes: u64) {
    use std::os::unix::process::CommandExt;
    unsafe {
        cmd.pre_exec(move || {
            let limit = libc::rlimit {
                rlim_cur: memory_bytes,
                rlim_max: memory_bytes,
            };
            libc::setrlimit(libc::RLIMIT_AS, &limit);
            Ok(())
        });
    }
}

#[cfg(not(unix))]
pub fn apply_memory_limit(_cmd: &mut std::process::Command, _memory_bytes: u64) {}

#[cfg(all(test, target_os = "linux"))]
mod tests {
    use super::*;

    /// Encodes `(class << 13) | level`. Pulling the decoding logic out of
    /// the assertions makes the intent obvious at the test site —
    /// otherwise raw `24576 == 3 << 13` reads as a magic number.
    fn decode_ioprio(ioprio: i32) -> (i32, i32) {
        let class = (ioprio >> 13) & 0x7;
        let level = ioprio & 0xFF;
        (class, level)
    }

    #[test]
    fn nice_19_maps_to_idle_class() {
        let (class, _) = decode_ioprio(ioprio_for_nice(19));
        assert_eq!(class, 3, "nice 19 should land in IOPRIO_CLASS_IDLE");
    }

    #[test]
    fn nice_15_is_the_idle_threshold() {
        let (class, _) = decode_ioprio(ioprio_for_nice(15));
        assert_eq!(class, 3, "nice 15 is the cutover point — must be idle");
    }

    #[test]
    fn nice_14_stays_best_effort() {
        // A hair below the threshold — must NOT switch to idle. The
        // boundary matters because "nice 10 means lower priority, not
        // starve me" is the caller intent this test protects.
        let (class, _) = decode_ioprio(ioprio_for_nice(14));
        assert_eq!(class, 2, "nice 14 must still be IOPRIO_CLASS_BE");
    }

    #[test]
    fn nice_zero_is_best_effort_zero() {
        let (class, level) = decode_ioprio(ioprio_for_nice(0));
        assert_eq!(class, 2);
        assert_eq!(level, 0);
    }

    #[test]
    fn best_effort_level_scales_proportionally_up_to_14() {
        // nice 10 → (10 * 7 / 19) = 3. Pins the proportional formula
        // so refactors that change division order don't drift.
        let (class, level) = decode_ioprio(ioprio_for_nice(10));
        assert_eq!(class, 2);
        assert_eq!(level, 3);
    }

    #[test]
    fn clamps_negative_and_oversized_inputs() {
        // Callers may pass out-of-range values; the function must
        // normalize rather than panic or produce garbage ioprio bits.
        assert_eq!(decode_ioprio(ioprio_for_nice(-5)).0, 2);
        assert_eq!(decode_ioprio(ioprio_for_nice(1_000)).0, 3);
    }
}
