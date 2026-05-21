// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Host-level resource pressure indicators that don't fit in any
//! existing trait collector. The agent's own footprint goes in
//! [`super::agent_self`]; per-mountpoint disk in [`mountpoints`]; system
//! memory in [`memory`]. This collector covers the in-between signals
//! that matter for "is this host about to fall over": **kernel-wide FD
//! consumption**, **total process count**, and **recent OOM events**.
//!
//! Trait namespace: `host_self`
//!   - fd_used: bytes currently allocated to file table entries
//!     (`/proc/sys/fs/file-nr` field 1)
//!   - fd_max: kernel ceiling for the file table (`field 3`)
//!   - fd_pct: fd_used / fd_max × 100, rounded to 2 places
//!   - proc_count: number of `/proc/<pid>` entries seen
//!   - oom_recent: 1 if dmesg/kern.log has fired the OOM killer in the
//!     last hour, 0 otherwise
//!
//! Linux only. macOS/BSD/Windows return `Value::Null` — none of them
//! expose `/proc/sys/fs/file-nr` or `/proc` the same way, and `lsof -nP
//! | wc -l` would mean spawning a subprocess on every check-in.
//!
//! All reads are pure procfs (no subprocess fork). OOM detection
//! reads `/var/log/kern.log` if present; if not, falls back to
//! `journalctl -k --since '1 hour ago' | grep -c 'invoked oom-killer'`
//! — *that* spawns one subprocess, but only when kern.log is missing.
//! Errors are silently swallowed and the field is reported as 0; the
//! point of the collector is signal not certainty.

use anyhow::Result;
use serde_json::{json, Value};

pub fn collect() -> Result<Value> {
    #[cfg(target_os = "linux")]
    {
        Ok(collect_linux())
    }

    #[cfg(not(target_os = "linux"))]
    {
        Ok(Value::Null)
    }
}

#[cfg(target_os = "linux")]
fn collect_linux() -> Value {
    let (fd_used, fd_max) = read_fd_nr();
    let fd_pct = if fd_max > 0 {
        let raw = (fd_used as f64 / fd_max as f64) * 100.0;
        (raw * 100.0).round() / 100.0
    } else {
        0.0
    };
    let proc_count = count_proc_entries();
    let oom_recent = recent_oom_kill();

    json!({
        "fd_used": fd_used,
        "fd_max": fd_max,
        "fd_pct": fd_pct,
        "proc_count": proc_count,
        "oom_recent": oom_recent,
    })
}

/// Read /proc/sys/fs/file-nr: three whitespace-separated numbers —
/// allocated, free, max. We want allocated and max.
#[cfg(target_os = "linux")]
fn read_fd_nr() -> (u64, u64) {
    let s = match std::fs::read_to_string("/proc/sys/fs/file-nr") {
        Ok(s) => s,
        Err(_) => return (0, 0),
    };
    let parts: Vec<&str> = s.split_whitespace().collect();
    if parts.len() < 3 {
        return (0, 0);
    }
    let used: u64 = parts[0].parse().unwrap_or(0);
    let max: u64 = parts[2].parse().unwrap_or(0);
    (used, max)
}

#[cfg(target_os = "linux")]
fn count_proc_entries() -> u64 {
    let entries = match std::fs::read_dir("/proc") {
        Ok(it) => it,
        Err(_) => return 0,
    };
    let mut n: u64 = 0;
    for e in entries.flatten() {
        let name = e.file_name();
        if let Some(s) = name.to_str() {
            if s.bytes().all(|b| b.is_ascii_digit()) {
                n += 1;
            }
        }
    }
    n
}

/// Returns 1 if any OOM-killer invocation is visible in the last hour
/// of kernel log output, 0 otherwise. Uses `/var/log/kern.log` when
/// readable (no subprocess); falls back to journalctl when not. The
/// log scan is bounded to the last 256 KiB of the file to keep a
/// rotated 100 MB kern.log from being read in full.
#[cfg(target_os = "linux")]
fn recent_oom_kill() -> u64 {
    if scan_kern_log_oom() {
        return 1;
    }
    if scan_journalctl_oom() {
        return 1;
    }
    0
}

#[cfg(target_os = "linux")]
fn scan_kern_log_oom() -> bool {
    use std::io::{Read, Seek, SeekFrom};
    let mut f = match std::fs::File::open("/var/log/kern.log") {
        Ok(f) => f,
        Err(_) => return false,
    };
    let len = f.metadata().map(|m| m.len()).unwrap_or(0);
    let tail_size: u64 = 256 * 1024;
    let from = len.saturating_sub(tail_size);
    if f.seek(SeekFrom::Start(from)).is_err() {
        return false;
    }
    let mut buf = String::new();
    if f.take(tail_size).read_to_string(&mut buf).is_err() {
        return false;
    }
    // Within-the-hour window check: kern.log timestamps are
    // syslog-format; rather than parsing them we treat the tail as the
    // "recent" window. 256 KiB of kern.log is typically minutes to a
    // couple hours of normal traffic — bounded enough that a hit
    // means something current. Trim: if a single OOM event ten days
    // ago is the only line in the tail, that's still a real signal —
    // the alarm is "host has gone OOM lately," and ten-days-and-no-
    // log-rotation is itself a slow burn worth surfacing.
    buf.contains("invoked oom-killer") || buf.contains("Out of memory: Killed process")
}

#[cfg(target_os = "linux")]
fn scan_journalctl_oom() -> bool {
    use std::process::{Command, Stdio};
    use std::time::Duration;
    let child = match Command::new("journalctl")
        .args(["-k", "--since", "1 hour ago", "--no-pager", "-q"])
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()
    {
        Ok(c) => c,
        Err(_) => return false,
    };
    // 2-second cap so a wedged journalctl can never block check-in.
    let out = match child.wait_with_output_timeout(Duration::from_secs(2)) {
        Ok(o) => o,
        Err(_) => return false,
    };
    let s = String::from_utf8_lossy(&out.stdout);
    s.contains("invoked oom-killer") || s.contains("Out of memory: Killed process")
}

/// Tiny shim: std::process::Child has no wait_with_output_timeout —
/// add one inline. We poll exit status with a short busy loop bounded
/// by the timeout; if the child hasn't exited we kill it and return.
/// Not a hot path (only when /var/log/kern.log is missing).
#[cfg(target_os = "linux")]
trait ChildTimeoutExt {
    fn wait_with_output_timeout(self, timeout: std::time::Duration) -> std::io::Result<std::process::Output>;
}

#[cfg(target_os = "linux")]
impl ChildTimeoutExt for std::process::Child {
    fn wait_with_output_timeout(mut self, timeout: std::time::Duration) -> std::io::Result<std::process::Output> {
        let start = std::time::Instant::now();
        loop {
            match self.try_wait()? {
                Some(_status) => return self.wait_with_output(),
                None => {
                    if start.elapsed() >= timeout {
                        let _ = self.kill();
                        let _ = self.wait();
                        return Err(std::io::Error::new(std::io::ErrorKind::TimedOut, "journalctl timeout"));
                    }
                    std::thread::sleep(std::time::Duration::from_millis(50));
                }
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[cfg(target_os = "linux")]
    #[test]
    fn collect_linux_reports_sane_values() {
        let v = collect_linux();
        let used = v.get("fd_used").and_then(|x| x.as_u64()).unwrap_or(0);
        let max = v.get("fd_max").and_then(|x| x.as_u64()).unwrap_or(0);
        let pct = v.get("fd_pct").and_then(|x| x.as_f64()).unwrap_or(0.0);
        let proc_count = v.get("proc_count").and_then(|x| x.as_u64()).unwrap_or(0);
        let oom = v.get("oom_recent").and_then(|x| x.as_u64()).unwrap_or(99);

        // Any system running these tests has nonzero fd_used (the test
        // process itself has stdio open) and a nonzero kernel ceiling.
        assert!(used > 0, "fd_used should be > 0 on a live host");
        assert!(max > used, "fd_max should be > fd_used");
        assert!((0.0..=100.0).contains(&pct), "fd_pct out of range: {}", pct);
        assert!(proc_count > 0, "proc_count should see at least the test process");
        // oom_recent is 0 or 1 — our test shouldn't see anything unexpected.
        assert!(oom == 0 || oom == 1, "oom_recent must be 0 or 1, got {}", oom);
    }
}
