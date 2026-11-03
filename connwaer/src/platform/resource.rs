// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Process-level resource guardrails: nice, ionice, memory limits.
//! Keeps connwaer invisible to the host.

use crate::config::ResourceLimits;
use tracing::{info, warn};

/// Apply all resource limits at process startup.
pub fn apply(limits: &ResourceLimits) {
    apply_nice(limits.cpu_nice);
    apply_ionice(&limits.io_class);
    apply_memory_limit(limits.memory_max_mb);
}

/// Set process nice value (Unix only).
fn apply_nice(nice_val: i32) {
    #[cfg(unix)]
    {
        // setpriority(PRIO_PROCESS, 0, nice) — 0 = current process.
        let ret = unsafe { libc::setpriority(libc::PRIO_PROCESS, 0, nice_val) };
        if ret == 0 {
            info!(nice = nice_val, "set process nice value");
        } else {
            warn!(nice = nice_val, "failed to set nice value (may need root for values < 0)");
        }
    }
    #[cfg(not(unix))]
    {
        let _ = nice_val;
    }
}

/// Set I/O scheduling class (Linux only via ioprio_set syscall).
fn apply_ionice(io_class: &str) {
    #[cfg(target_os = "linux")]
    {
        // ioprio_set(IOPRIO_WHO_PROCESS, 0, class << 13 | priority)
        // Classes: 0=none, 1=realtime, 2=best-effort, 3=idle
        let class: u32 = match io_class {
            "idle" => 3,
            "best-effort" => 2,
            "none" => 0,
            _ => {
                warn!(io_class, "unknown io_class, skipping ionice");
                return;
            }
        };
        // For idle class, priority bits are ignored. For best-effort, use 7 (lowest).
        let priority: u32 = if class == 2 { 7 } else { 0 };
        let ioprio = (class << 13) | priority;

        // ioprio_set syscall: NR 251 on x86_64, 314 on aarch64
        let ret = unsafe {
            libc::syscall(
                libc::SYS_ioprio_set,
                1i32, // IOPRIO_WHO_PROCESS
                0i32, // pid 0 = self
                ioprio,
            )
        };
        if ret == 0 {
            info!(io_class, "set I/O scheduling class");
        } else {
            warn!(io_class, "failed to set I/O scheduling class");
        }
    }
    #[cfg(not(target_os = "linux"))]
    {
        let _ = io_class;
    }
}

/// Set process virtual memory limit via RLIMIT_AS (Unix only). Default: 512 MiB.
fn apply_memory_limit(max_mb: u64) {
    if max_mb == 0 {
        return;
    }

    #[cfg(unix)]
    {
        let bytes = max_mb * 1024 * 1024;
        let rlim = libc::rlimit {
            rlim_cur: bytes,
            rlim_max: bytes,
        };
        let ret = unsafe { libc::setrlimit(libc::RLIMIT_AS, &rlim) };
        if ret == 0 {
            info!(max_mb, "set process memory limit (RLIMIT_AS)");
        } else {
            warn!(max_mb, "failed to set memory limit");
        }
    }
    #[cfg(not(unix))]
    {
        let _ = max_mb;
    }
}
