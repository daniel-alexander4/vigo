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
//!   - uptime_secs: seconds since the agent binary started
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

    json!({
        "rss_bytes": rss_kb * 1024,
        "vsz_bytes": vsz_kb * 1024,
        "threads": threads,
        "open_fds": open_fds,
        "uptime_secs": uptime_secs,
    })
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
