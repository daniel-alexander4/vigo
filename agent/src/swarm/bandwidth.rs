// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Adaptive bandwidth monitor for swarm peer serving.
//!
//! Samples interface utilization from sysfs (Linux) or returns a conservative
//! default on other platforms. Zero subprocesses — reads procfs/sysfs only.
//!
//! Throttle policy:
//!   Interface < 10% utilized  → swarm uses up to max_bandwidth_percent (default 50%)
//!   Interface 10-50% utilized → swarm uses up to 10%
//!   Interface > 50% utilized  → swarm uses up to 2%
//!   Interface > 90% utilized  → swarm pauses completely

use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use tracing::debug;

/// Whether swarm serving should be paused.
#[derive(Clone)]
pub struct BandwidthMonitor {
    paused: Arc<AtomicBool>,
    /// Effective bandwidth limit as a percentage of link speed (0-100).
    /// When 0, serving is paused.
    limit_pct: Arc<std::sync::atomic::AtomicU8>,
}

impl BandwidthMonitor {
    pub fn new() -> Self {
        Self {
            paused: Arc::new(AtomicBool::new(false)),
            limit_pct: Arc::new(std::sync::atomic::AtomicU8::new(50)),
        }
    }

    /// Whether serving should be paused entirely.
    pub fn is_paused(&self) -> bool {
        self.paused.load(Ordering::Relaxed)
    }

    /// Current bandwidth limit as a percentage (0 = paused).
    pub fn limit_percent(&self) -> u8 {
        self.limit_pct.load(Ordering::Relaxed)
    }

    /// Spawn a background task that samples interface utilization every second.
    /// Runs until the cancellation token fires.
    pub fn spawn_sampler(
        &self,
        cancel: tokio_util::sync::CancellationToken,
        max_bandwidth_pct: u8,
    ) {
        let paused = self.paused.clone();
        let limit_pct = self.limit_pct.clone();

        tokio::spawn(async move {
            let mut prev_sample: Option<InterfaceSample> = None;

            loop {
                tokio::select! {
                    _ = cancel.cancelled() => return,
                    _ = tokio::time::sleep(std::time::Duration::from_secs(1)) => {}
                }

                let sample = match sample_primary_interface() {
                    Some(s) => s,
                    None => {
                        // Can't read interface stats — use conservative default.
                        limit_pct.store(max_bandwidth_pct.min(10), Ordering::Relaxed);
                        paused.store(false, Ordering::Relaxed);
                        prev_sample = None;
                        continue;
                    }
                };

                if let Some(ref prev) = prev_sample {
                    if sample.link_speed_bytes > 0 {
                        let tx_delta = sample.tx_bytes.saturating_sub(prev.tx_bytes);
                        let rx_delta = sample.rx_bytes.saturating_sub(prev.rx_bytes);
                        let total_delta = tx_delta + rx_delta;
                        // utilization as percentage of full-duplex link speed
                        let util_pct = (total_delta * 100 / sample.link_speed_bytes).min(100) as u8;

                        let effective = if util_pct > 90 {
                            paused.store(true, Ordering::Relaxed);
                            0
                        } else if util_pct > 50 {
                            paused.store(false, Ordering::Relaxed);
                            2
                        } else if util_pct > 10 {
                            paused.store(false, Ordering::Relaxed);
                            10
                        } else {
                            paused.store(false, Ordering::Relaxed);
                            max_bandwidth_pct
                        };
                        limit_pct.store(effective, Ordering::Relaxed);
                        debug!(util_pct, effective, "bandwidth sample");
                    }
                }

                prev_sample = Some(sample);
            }
        });
    }
}

struct InterfaceSample {
    tx_bytes: u64,
    rx_bytes: u64,
    /// Link speed in bytes/sec (not bits).
    link_speed_bytes: u64,
}

/// Read stats for the primary (default-route) interface from sysfs.
#[cfg(target_os = "linux")]
fn sample_primary_interface() -> Option<InterfaceSample> {
    // Find the default-route interface from /proc/net/route.
    let iface = default_interface_linux()?;

    let tx = read_sysfs_u64(&format!("/sys/class/net/{}/statistics/tx_bytes", iface))?;
    let rx = read_sysfs_u64(&format!("/sys/class/net/{}/statistics/rx_bytes", iface))?;
    // speed is in Mbit/s, convert to bytes/s
    let speed_mbps = read_sysfs_u64(&format!("/sys/class/net/{}/speed", iface)).unwrap_or(1000);
    let speed_bytes = speed_mbps * 1_000_000 / 8;

    Some(InterfaceSample {
        tx_bytes: tx,
        rx_bytes: rx,
        link_speed_bytes: speed_bytes,
    })
}

#[cfg(target_os = "linux")]
fn default_interface_linux() -> Option<String> {
    let content = std::fs::read_to_string("/proc/net/route").ok()?;
    for line in content.lines().skip(1) {
        let fields: Vec<&str> = line.split_whitespace().collect();
        if fields.len() >= 2 && fields[1] == "00000000" {
            // Destination 0.0.0.0 = default route.
            return Some(fields[0].to_string());
        }
    }
    None
}

#[cfg(target_os = "linux")]
fn read_sysfs_u64(path: &str) -> Option<u64> {
    std::fs::read_to_string(path)
        .ok()?
        .trim()
        .parse()
        .ok()
}

/// Non-Linux: no sysfs available, return None to trigger conservative default.
#[cfg(not(target_os = "linux"))]
fn sample_primary_interface() -> Option<InterfaceSample> {
    None
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn monitor_defaults() {
        let mon = BandwidthMonitor::new();
        assert!(!mon.is_paused());
        assert_eq!(mon.limit_percent(), 50);
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn default_interface_found() {
        // On any Linux with network, there should be a default route.
        let iface = default_interface_linux();
        // May be None in containers, so just ensure no panic.
        if let Some(ref name) = iface {
            assert!(!name.is_empty());
        }
    }
}
