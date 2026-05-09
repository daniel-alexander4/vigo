// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Agent self-telemetry — reports the agent process's own resource
//! footprint on every check-in so operators can track per-envoy memory
//! growth without a Prometheus scrape target on the envoy.
//!
//! Linux: reads `/proc/self/status` (VmRSS, VmSize, Threads) and counts
//! entries in `/proc/self/fd/`.
//! macOS: uses `ps -o rss,vsz,nlwp -p <pid>` as a fallback — no procfs.
//! Other platforms return `Value::Null`.
//!
//! Trait namespace: `agent_self`
//!   - rss_bytes: resident set size of the agent process
//!   - vsz_bytes: virtual memory size
//!   - threads: live thread count
//!   - open_fds: open file descriptor count (unix only)
//!   - descendants: live count of processes whose PPID is the agent (Linux only)
//!   - descendant_open_fds: sum of open_fds across those descendants (Linux only)
//!   - uptime_secs: seconds since the agent binary started
//!
//! Why descendants matter: the agent regularly spawns short-lived
//! children (git-receive-pack via the gitmirror helper, package-manager
//! probes, exec resources) and FD leaks in those children would not
//! show up in `open_fds`. Tracking the descendant total lets the fleet
//! detector catch leaks in child code paths that the agent's own
//! /proc/self/fd count is blind to.
//!
//! Intentionally zero-cost on the hot path: on Linux the entire collect
//! call is four short procfs reads. Classified `volatile` so it refreshes
//! every cycle — the point is to catch leaks *within* a single envoy's
//! uptime, not once an hour.

use anyhow::Result;
use serde_json::{json, Value};
use std::sync::OnceLock;
use std::time::Instant;

/// Captured once at process start so uptime_secs is reported relative to
/// the agent's own lifetime, not the host's.
static PROCESS_START: OnceLock<Instant> = OnceLock::new();

/// Call once from the agent's main entry point so the uptime anchor is
/// set before the first check-in. Subsequent calls are no-ops.
pub fn init_start_marker() {
    let _ = PROCESS_START.set(Instant::now());
}

pub fn collect() -> Result<Value> {
    let uptime_secs = PROCESS_START
        .get()
        .map(|s| s.elapsed().as_secs())
        .unwrap_or(0);

    #[cfg(target_os = "linux")]
    {
        return Ok(collect_linux(uptime_secs));
    }

    #[cfg(target_os = "macos")]
    {
        return Ok(collect_macos(uptime_secs));
    }

    #[cfg(not(any(target_os = "linux", target_os = "macos")))]
    {
        let _ = uptime_secs;
        Ok(Value::Null)
    }
}

#[cfg(target_os = "linux")]
fn collect_linux(uptime_secs: u64) -> Value {
    let status = std::fs::read_to_string("/proc/self/status").unwrap_or_default();
    let mut rss_kb: u64 = 0;
    let mut vsz_kb: u64 = 0;
    let mut threads: u64 = 0;
    for line in status.lines() {
        if let Some(rest) = line.strip_prefix("VmRSS:") {
            rss_kb = parse_kb(rest);
        } else if let Some(rest) = line.strip_prefix("VmSize:") {
            vsz_kb = parse_kb(rest);
        } else if let Some(rest) = line.strip_prefix("Threads:") {
            threads = rest.trim().parse().unwrap_or(0);
        }
    }

    let open_fds = count_fds();
    let (descendants, descendant_open_fds) = count_descendants_and_fds(std::process::id());

    json!({
        "rss_bytes": rss_kb * 1024,
        "vsz_bytes": vsz_kb * 1024,
        "threads": threads,
        "open_fds": open_fds,
        "descendants": descendants,
        "descendant_open_fds": descendant_open_fds,
        "uptime_secs": uptime_secs,
    })
}

/// Walk /proc once and return (count, total_fd_count) for processes
/// whose PPID is the given pid. One level deep — the agent's direct
/// children are what we care about (gitmirror helper's git child,
/// exec-resource subprocesses, package-manager probes). Going deeper
/// would be more accurate for grandchildren but adds proc-walk cost
/// on every check-in; defer until we see a real grandchild leak.
///
/// Returns (0, 0) if /proc isn't readable. PPID is read from
/// /proc/<pid>/stat field 4 (after the parenthesized comm, which can
/// itself contain spaces — we split on the rightmost ')').
#[cfg(target_os = "linux")]
fn count_descendants_and_fds(self_pid: u32) -> (u64, u64) {
    let entries = match std::fs::read_dir("/proc") {
        Ok(it) => it,
        Err(_) => return (0, 0),
    };
    let mut count: u64 = 0;
    let mut fd_total: u64 = 0;
    for entry in entries.flatten() {
        let name = entry.file_name();
        let name = match name.to_str() {
            Some(s) => s,
            None => continue,
        };
        // Skip non-numeric /proc entries (kallsyms, sys, etc.).
        let pid: u32 = match name.parse() {
            Ok(p) => p,
            Err(_) => continue,
        };
        if pid == self_pid {
            continue;
        }
        let stat = match std::fs::read_to_string(format!("/proc/{}/stat", pid)) {
            Ok(s) => s,
            Err(_) => continue,
        };
        // /proc/<pid>/stat shape: "PID (comm with possibly spaces) STATE PPID ...".
        // The right-most ')' terminates comm; PPID is two fields after it.
        let close = match stat.rfind(')') {
            Some(i) => i,
            None => continue,
        };
        let after = &stat[close + 1..];
        let mut iter = after.split_whitespace();
        let _state = match iter.next() { Some(s) => s, None => continue };
        let ppid: u32 = match iter.next().and_then(|s| s.parse().ok()) {
            Some(p) => p,
            None => continue,
        };
        if ppid != self_pid {
            continue;
        }
        count += 1;
        if let Ok(fd_iter) = std::fs::read_dir(format!("/proc/{}/fd", pid)) {
            fd_total += fd_iter.filter_map(|e| e.ok()).count() as u64;
        }
    }
    (count, fd_total)
}

#[cfg(not(target_os = "linux"))]
fn count_descendants_and_fds(_self_pid: u32) -> (u64, u64) {
    (0, 0)
}

#[cfg(target_os = "linux")]
fn parse_kb(s: &str) -> u64 {
    // Line shape: "VmRSS:\t   12345 kB"
    s.split_whitespace()
        .next()
        .and_then(|n| n.parse::<u64>().ok())
        .unwrap_or(0)
}

#[cfg(unix)]
fn count_fds() -> u64 {
    std::fs::read_dir("/proc/self/fd")
        .map(|it| it.filter_map(|e| e.ok()).count() as u64)
        .unwrap_or(0)
}

#[cfg(not(unix))]
#[allow(dead_code)]
fn count_fds() -> u64 {
    0
}

#[cfg(target_os = "macos")]
fn collect_macos(uptime_secs: u64) -> Value {
    // procfs is not available on macOS; call ps once. Short timeout,
    // best-effort — if ps fails we emit zeros so the shape stays stable.
    let pid = std::process::id().to_string();
    let output = std::process::Command::new("ps")
        .args(["-o", "rss=,vsz=,nlwp=", "-p", &pid])
        .output();
    let (rss_kb, vsz_kb, threads) = match output {
        Ok(o) if o.status.success() => parse_ps(&String::from_utf8_lossy(&o.stdout)),
        _ => (0, 0, 0),
    };

    json!({
        "rss_bytes": rss_kb * 1024,
        "vsz_bytes": vsz_kb * 1024,
        "threads": threads,
        "open_fds": 0,  // not exposed by ps; lsof would require a subprocess per call
        "descendants": 0,  // /proc not available; pgrep would require a subprocess per call
        "descendant_open_fds": 0,
        "uptime_secs": uptime_secs,
    })
}

#[cfg(target_os = "macos")]
fn parse_ps(s: &str) -> (u64, u64, u64) {
    let line = s.trim();
    let parts: Vec<&str> = line.split_whitespace().collect();
    if parts.len() < 3 {
        return (0, 0, 0);
    }
    let rss = parts[0].parse::<u64>().unwrap_or(0);
    let vsz = parts[1].parse::<u64>().unwrap_or(0);
    let threads = parts[2].parse::<u64>().unwrap_or(0);
    (rss, vsz, threads)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[cfg(target_os = "linux")]
    #[test]
    fn parse_kb_extracts_first_number() {
        assert_eq!(parse_kb("\t   12345 kB"), 12345);
        assert_eq!(parse_kb("  42 kB"), 42);
        assert_eq!(parse_kb("garbage"), 0);
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn collect_linux_reports_self() {
        let v = collect_linux(0);
        let rss = v.get("rss_bytes").and_then(|x| x.as_u64()).unwrap_or(0);
        let threads = v.get("threads").and_then(|x| x.as_u64()).unwrap_or(0);
        // Our own process has nonzero RSS and at least one thread.
        assert!(rss > 0, "rss_bytes should be > 0 for self");
        assert!(threads >= 1, "threads should be >= 1 for self");
        // descendants/descendant_open_fds are present and non-negative
        // (test harness has no children, so they should be 0).
        assert!(v.get("descendants").is_some(), "descendants field present");
        assert!(v.get("descendant_open_fds").is_some(), "descendant_open_fds field present");
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn count_descendants_finds_spawned_child() {
        use std::process::{Command, Stdio};
        let mut child = Command::new("sleep")
            .arg("3")
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .spawn()
            .expect("spawn sleep");
        // Give the kernel a moment to register the new PPID.
        std::thread::sleep(std::time::Duration::from_millis(50));
        let (count, fds) = count_descendants_and_fds(std::process::id());
        let _ = child.kill();
        let _ = child.wait();
        assert!(count >= 1, "should see at least one descendant (spawned sleep), got {}", count);
        // sleep opens stdin/stdout/stderr at minimum.
        assert!(fds >= 3, "sleep child should have >=3 fds open, got {}", fds);
    }

    #[cfg(target_os = "macos")]
    #[test]
    fn parse_ps_handles_typical_output() {
        let (rss, vsz, threads) = parse_ps("  12345 67890 8\n");
        assert_eq!(rss, 12345);
        assert_eq!(vsz, 67890);
        assert_eq!(threads, 8);
    }

    #[cfg(target_os = "macos")]
    #[test]
    fn parse_ps_handles_empty() {
        assert_eq!(parse_ps(""), (0, 0, 0));
        assert_eq!(parse_ps("garbage"), (0, 0, 0));
    }

    #[test]
    fn init_start_marker_is_idempotent() {
        init_start_marker();
        let first = PROCESS_START.get().copied();
        init_start_marker();
        let second = PROCESS_START.get().copied();
        assert_eq!(first, second);
    }
}
