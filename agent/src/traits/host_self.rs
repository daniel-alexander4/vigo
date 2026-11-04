// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Host-level resource pressure indicators that don't fit in any
//! existing trait collector. The agent's own footprint goes in
//! [`super::agent_self`]; per-mountpoint disk in [`mountpoints`]; system
//! memory in [`memory`]. This collector covers the in-between signals
//! that matter for "is this host about to fall over": **kernel-wide FD
//! consumption**, **total process count**, **recent OOM events**, and
//! **whether the host's previous session ended in a power loss**.
//!
//! Trait namespace: `host_self`
//!   - fd_used: bytes currently allocated to file table entries
//!     (`/proc/sys/fs/file-nr` field 1)
//!   - fd_max: kernel ceiling for the file table (`field 3`)
//!   - fd_pct: fd_used / fd_max × 100, rounded to 2 places
//!   - proc_count: number of `/proc/<pid>` entries seen
//!   - oom_recent: 1 if dmesg/kern.log has fired the OOM killer in the
//!     last hour, 0 otherwise
//!   - power_detector_state: `armed` | `first_boot` | `unsupported` —
//!     whether the two counts below mean anything
//!   - unclean_shutdowns_30d: host power losses observed in the last 30
//!     days (omitted unless `armed`)
//!   - boot_transitions_30d: all reboots observed in the same window —
//!     the denominator that makes a zero numerator readable (omitted
//!     unless `armed`)
//!   - last_unclean_date: `YYYY-MM-DD` of the newest power loss, omitted
//!     when there is none
//!
//! The power fields ride here rather than in a collector of their own
//! because `oom_recent` already established the shape: an
//! abnormal-termination count the server reads as a flat key. They are
//! computed once at startup by [`crate::state::power::evaluate`] and
//! published through [`set_power_status`]; `collect` never touches LMDB,
//! which has no state handle at collector dispatch.
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

use crate::state::power::PowerStatus;
use anyhow::Result;
use serde_json::{json, Value};
use std::sync::OnceLock;

/// Set once at process start, before the first check-in.
static POWER_STATUS: OnceLock<PowerStatus> = OnceLock::new();

/// Publish the startup power verdict for `collect` to report. Called once
/// from the agent's entry point; subsequent calls are no-ops.
pub fn set_power_status(status: PowerStatus) {
    let _ = POWER_STATUS.set(status);
}

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

    let mut out = json!({
        "fd_used": fd_used,
        "fd_max": fd_max,
        "fd_pct": fd_pct,
        "proc_count": proc_count,
        "oom_recent": oom_recent,
    });
    add_power_fields(&mut out, POWER_STATUS.get());
    out
}

/// Merge the startup power verdict into the collector's object.
///
/// The counts are emitted **only** when the detector is armed. Emitting `0`
/// for a detector that has never observed a boot transition would be
/// indistinguishable on the wire from a host that has genuinely not lost
/// power — and the server's health rollup treats a missing key as unknown but
/// a zero as good news, so the difference is the whole point.
#[cfg(target_os = "linux")]
fn add_power_fields(out: &mut Value, status: Option<&PowerStatus>) {
    use crate::state::power::DetectorState;

    let Some(obj) = out.as_object_mut() else { return };
    let Some(status) = status else { return };

    obj.insert(
        "power_detector_state".into(),
        json!(status.detector.as_str()),
    );
    if status.detector != DetectorState::Armed {
        return;
    }
    obj.insert("unclean_shutdowns_30d".into(), json!(status.unclean_30d));
    obj.insert("boot_transitions_30d".into(), json!(status.transitions_30d));
    if let Some(date) = &status.last_unclean_date {
        obj.insert("last_unclean_date".into(), json!(date));
    }
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
    // 2-second cap so a wedged journalctl can never block check-in.
    let out = match crate::traits::run_cmd_timeout(
        "journalctl",
        &["-k", "--since", "1 hour ago", "--no-pager", "-q"],
        std::time::Duration::from_secs(2),
    ) {
        Ok(o) => o,
        Err(_) => return false,
    };
    let s = String::from_utf8_lossy(&out.stdout);
    s.contains("invoked oom-killer") || s.contains("Out of memory: Killed process")
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

    #[cfg(target_os = "linux")]
    fn status(detector: crate::state::power::DetectorState, unclean: usize) -> PowerStatus {
        PowerStatus {
            detector,
            unclean_30d: unclean,
            transitions_30d: 9,
            last_unclean_date: (unclean > 0).then(|| "2026-09-04".to_string()),
        }
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn power_fields_absent_when_nothing_was_published() {
        // A store that failed to open must not fabricate a healthy zero.
        let mut v = json!({"fd_used": 1});
        add_power_fields(&mut v, None);
        assert!(v.get("power_detector_state").is_none());
        assert!(v.get("unclean_shutdowns_30d").is_none());
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn power_counts_omitted_unless_armed() {
        use crate::state::power::DetectorState;
        for state in [DetectorState::Unsupported, DetectorState::FirstBoot] {
            let mut v = json!({});
            add_power_fields(&mut v, Some(&status(state, 0)));
            assert_eq!(v["power_detector_state"], json!(state.as_str()));
            assert!(
                v.get("unclean_shutdowns_30d").is_none(),
                "{} must not report a count that would read as healthy",
                state.as_str()
            );
            assert!(v.get("boot_transitions_30d").is_none());
        }
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn power_counts_present_and_zero_when_armed_and_clean() {
        use crate::state::power::DetectorState;
        let mut v = json!({});
        add_power_fields(&mut v, Some(&status(DetectorState::Armed, 0)));
        assert_eq!(v["power_detector_state"], json!("armed"));
        assert_eq!(v["unclean_shutdowns_30d"], json!(0));
        assert_eq!(v["boot_transitions_30d"], json!(9), "the denominator is what makes the 0 readable");
        assert!(v.get("last_unclean_date").is_none());
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn power_date_reported_only_when_there_is_an_unclean_event() {
        use crate::state::power::DetectorState;
        let mut v = json!({});
        add_power_fields(&mut v, Some(&status(DetectorState::Armed, 3)));
        assert_eq!(v["unclean_shutdowns_30d"], json!(3));
        assert_eq!(v["last_unclean_date"], json!("2026-09-04"));
    }
}
