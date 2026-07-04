use crate::exec;
use anyhow::Result;
use std::time::Instant;

use super::{Capability, CapabilityContext, CapabilityResult, Evidence, VerificationStatus};

pub struct TimeSync;

impl Capability for TimeSync {
    fn name(&self) -> &'static str { "time_sync" }
    fn controls(&self) -> &'static [&'static str] { &["PCI-DSS-10.6.1", "NIST-AU-8"] }

    fn supported(&self) -> bool {
        cfg!(target_os = "linux") || cfg!(target_os = "windows")
    }

    fn verify(&self, ctx: &CapabilityContext) -> Result<CapabilityResult> {
        let start = Instant::now();
        let started_at = chrono::Utc::now().to_rfc3339();

        #[cfg(target_os = "linux")]
        let (status, summary, evidence) = verify_linux(&ctx.config)?;

        #[cfg(target_os = "windows")]
        let (status, summary, evidence) = verify_windows(&ctx.config)?;

        #[cfg(not(any(target_os = "linux", target_os = "windows")))]
        let (status, summary, evidence) = (
            VerificationStatus::Skipped,
            "Not supported on this platform".to_string(),
            vec![],
        );


        let elapsed = start.elapsed();
        Ok(CapabilityResult {
            capability: self.name().to_string(),
            status,
            summary,
            evidence,
            controls: self.controls().iter().map(|s| s.to_string()).collect(),
            started_at,
            completed_at: chrono::Utc::now().to_rfc3339(),
            duration_ms: elapsed.as_millis() as u64,
            platform: ctx.platform.clone(),
            hostname: ctx.hostname.clone(),
            version: ctx.version.clone(),
            error: None,
        })
    }
}

#[cfg(target_os = "linux")]
fn verify_linux(
    config: &super::CapabilityConfig,
) -> Result<(VerificationStatus, String, Vec<Evidence>)> {
    let mut evidence = Vec::new();
    let mut issues = Vec::new();
    let mut measured_offset: Option<f64> = None;

    let max_offset_ms = config.thresholds.get("max_offset_ms")
        .and_then(|v| v.as_f64())
        .unwrap_or(100.0);

    // --- Check which NTP service is running ---
    let chronyd_active = exec::stdout_of("systemctl", &["is-active", "chronyd"])
        .map(|s| s == "active")
        .unwrap_or(false);
    let ntpd_active = exec::stdout_of("systemctl", &["is-active", "ntpd"])
        .map(|s| s == "active")
        .unwrap_or(false);
    let ntp_active = exec::stdout_of("systemctl", &["is-active", "ntp"])
        .map(|s| s == "active")
        .unwrap_or(false);
    let timesyncd_active = exec::stdout_of("systemctl", &["is-active", "systemd-timesyncd"])
        .map(|s| s == "active")
        .unwrap_or(false);

    let service_name = if chronyd_active {
        "chronyd"
    } else if ntpd_active || ntp_active {
        "ntpd"
    } else if timesyncd_active {
        "systemd-timesyncd"
    } else {
        "none"
    };

    evidence.push(Evidence {
        key: "ntp_service".to_string(),
        value: serde_json::json!({
            "chronyd": chronyd_active,
            "ntpd": ntpd_active || ntp_active,
            "systemd-timesyncd": timesyncd_active,
            "active_service": service_name,
        }),
        detail: format!("NTP service: {}", service_name),
    });

    if service_name == "none" {
        issues.push("no NTP service is running (chronyd, ntpd, or systemd-timesyncd)".to_string());
    }

    // --- Query offset based on the active service ---
    if chronyd_active {
        if let Some(tracking_out) = exec::stdout_of("chronyc", &["tracking"]) {
            // Parse "System time" and "Last offset" lines.
            let mut sync_source = String::new();
            let mut system_time_offset: Option<f64> = None;
            let mut last_offset: Option<f64> = None;

            for line in tracking_out.lines() {
                let line = line.trim();
                if line.starts_with("Reference ID") {
                    sync_source = line.split(':').nth(1)
                        .map(|s| s.trim().to_string())
                        .unwrap_or_default();
                } else if line.starts_with("System time") {
                    // "System time     : 0.000012345 seconds fast of NTP time"
                    system_time_offset = line.split(':').nth(1)
                        .and_then(|s| {
                            let parts: Vec<&str> = s.split_whitespace().collect();
                            parts.first().and_then(|v| v.parse::<f64>().ok())
                        });
                } else if line.starts_with("Last offset") {
                    // "Last offset     : +0.000001234 seconds"
                    last_offset = line.split(':').nth(1)
                        .and_then(|s| {
                            let val = s.split_whitespace().next().unwrap_or("");
                            val.trim_start_matches('+').parse::<f64>().ok()
                        });
                }
            }

            let offset_sec = last_offset.or(system_time_offset).unwrap_or(0.0);
            let offset_ms = offset_sec.abs() * 1000.0;
            measured_offset = Some(offset_ms);

            evidence.push(Evidence {
                key: "chrony_tracking".to_string(),
                value: serde_json::json!({
                    "sync_source": sync_source,
                    "offset_ms": offset_ms,
                    "offset_sec": offset_sec,
                }),
                detail: format!("chrony: source={}, offset={:.3}ms", sync_source, offset_ms),
            });

            if offset_ms > max_offset_ms {
                issues.push(format!("NTP offset {:.1}ms exceeds {:.0}ms threshold", offset_ms, max_offset_ms));
            }
        }
    } else if ntpd_active || ntp_active {
        if let Some(ntpq_out) = exec::stdout_of("ntpq", &["-p"]) {
            // Parse ntpq output: look for the active peer (marked with *).
            let mut sync_source = String::new();
            let mut offset_ms_val: Option<f64> = None;

            for line in ntpq_out.lines() {
                let line = line.trim();
                if line.starts_with('*') {
                    let parts: Vec<&str> = line.split_whitespace().collect();
                    if !parts.is_empty() {
                        sync_source = parts[0].trim_start_matches('*').to_string();
                    }
                    // Offset is typically the 9th column (index 8).
                    if parts.len() >= 9 {
                        offset_ms_val = parts[8].parse::<f64>().ok();
                    }
                }
            }

            if let Some(offset) = offset_ms_val {
                let abs_offset = offset.abs();
                measured_offset = Some(abs_offset);
                evidence.push(Evidence {
                    key: "ntpq_status".to_string(),
                    value: serde_json::json!({
                        "sync_source": sync_source,
                        "offset_ms": abs_offset,
                    }),
                    detail: format!("ntpd: source={}, offset={:.3}ms", sync_source, abs_offset),
                });
                if abs_offset > max_offset_ms {
                    issues.push(format!("NTP offset {:.1}ms exceeds {:.0}ms threshold", abs_offset, max_offset_ms));
                }
            }
        }
    } else if timesyncd_active {
        if let Some(timectl_out) = exec::stdout_of("timedatectl", &["show-timesync"]) {
            // Parse NTPMessage and server info.
            let mut server = String::new();

            for line in timectl_out.lines() {
                let line = line.trim();
                if line.starts_with("ServerName=") {
                    server = line.trim_start_matches("ServerName=").to_string();
                }
            }

            evidence.push(Evidence {
                key: "timesyncd_status".to_string(),
                value: serde_json::json!({
                    "server": server,
                }),
                detail: format!("systemd-timesyncd: server={}", server),
            });
        }

        // Try timedatectl status for sync status.
        if let Some(status_out) = exec::stdout_of("timedatectl", &["status"]) {
            let synced = status_out.contains("synchronized: yes") ||
                         status_out.contains("NTP synchronized: yes");
            evidence.push(Evidence {
                key: "ntp_synchronized".to_string(),
                value: serde_json::json!(synced),
                detail: format!("NTP synchronized: {}", synced),
            });
            if !synced {
                issues.push("system clock is not NTP synchronized".to_string());
            }
        }
    }

    // --- Optional: manual NTP server check ---
    if let Some(ntp_server) = config.params.get("ntp_server").and_then(|v| v.as_str()) {
        // Use chronyd -Q or ntpdate -q for a one-shot query.
        if let Some(ntpdate_out) = exec::stdout_of("ntpdate", &["-q", ntp_server]) {
            // Parse "offset X.XXXX sec"
            if let Some(offset_str) = ntpdate_out.lines().last() {
                if let Some(pos) = offset_str.find("offset") {
                    let after = &offset_str[pos + 7..];
                    if let Some(val) = after.split_whitespace().next().and_then(|v| v.parse::<f64>().ok()) {
                        let offset_ms = val.abs() * 1000.0;
                        evidence.push(Evidence {
                            key: "manual_ntp_check".to_string(),
                            value: serde_json::json!({
                                "server": ntp_server,
                                "offset_ms": offset_ms,
                            }),
                            detail: format!("Manual NTP check against {}: offset={:.3}ms",
                                ntp_server, offset_ms),
                        });
                    }
                }
            }
        }
    }

    let status = if issues.is_empty() {
        VerificationStatus::Pass
    } else if issues.iter().any(|i| i.contains("no NTP service")) {
        VerificationStatus::Fail
    } else {
        VerificationStatus::Warn
    };

    let summary = if issues.is_empty() {
        let offset_str = measured_offset
            .map(|o| format!(", offset={:.1}ms", o))
            .unwrap_or_default();
        format!("{} active{}", service_name, offset_str)
    } else {
        issues.join("; ")
    };

    Ok((status, summary, evidence))
}

#[cfg(target_os = "windows")]
fn verify_windows(
    config: &super::CapabilityConfig,
) -> Result<(VerificationStatus, String, Vec<Evidence>)> {
    let mut evidence = Vec::new();
    let mut issues = Vec::new();
    let mut measured_offset: Option<f64> = None;

    let max_offset_ms = config.thresholds.get("max_offset_ms")
        .and_then(|v| v.as_f64())
        .unwrap_or(100.0);

    // Check w32time service status.
    let svc_status = exec::stdout_of("sc.exe", &["query", "w32time"]);
    let is_running = svc_status.as_ref().map(|s| s.contains("RUNNING")).unwrap_or(false);

    evidence.push(Evidence {
        key: "w32time_service".to_string(),
        value: serde_json::json!(is_running),
        detail: format!("w32time service: {}", if is_running { "running" } else { "not running" }),
    });

    if !is_running {
        issues.push("w32time service is not running".to_string());
    }

    // Query time sync status via w32tm.
    if let Some(w32tm_out) = exec::stdout_of("w32tm", &["/query", "/status"]) {
        let mut source = String::new();
        let mut stratum: u32 = 0;

        for line in w32tm_out.lines() {
            let line = line.trim();
            if line.starts_with("Source:") {
                source = line.trim_start_matches("Source:").trim().to_string();
            } else if line.starts_with("Stratum:") {
                stratum = line.trim_start_matches("Stratum:")
                    .trim()
                    .split_whitespace().next()
                    .and_then(|v| v.parse().ok())
                    .unwrap_or(0);
            }
        }

        evidence.push(Evidence {
            key: "w32tm_status".to_string(),
            value: serde_json::json!({
                "source": source,
                "stratum": stratum,
            }),
            detail: format!("w32tm: source={}, stratum={}", source, stratum),
        });

        if source.contains("Free-Running") || source == "Local CMOS Clock" {
            issues.push("time source is local only (not syncing to NTP)".to_string());
        }
    }

    // Query offset via w32tm /stripchart (one-shot).
    if let Some(ntp_server) = config.params.get("ntp_server").and_then(|v| v.as_str()) {
        if let Some(strip_out) = exec::stdout_of(
            "w32tm",
            &["/stripchart", &format!("/computer:{}", ntp_server), "/samples:1", "/dataonly"],
        ) {
            // Parse the offset from the last line.
            if let Some(last_line) = strip_out.lines().last() {
                // Format: "HH:MM:SS, +00.0012345s"
                if let Some(offset_str) = last_line.split(',').nth(1) {
                    let cleaned = offset_str.trim().trim_end_matches('s');
                    if let Ok(offset_sec) = cleaned.parse::<f64>() {
                        let offset_ms = offset_sec.abs() * 1000.0;
                        measured_offset = Some(offset_ms);
                        evidence.push(Evidence {
                            key: "ntp_offset".to_string(),
                            value: serde_json::json!({
                                "offset_ms": offset_ms,
                                "server": ntp_server,
                            }),
                            detail: format!("NTP offset: {:.3}ms (from {})", offset_ms, ntp_server),
                        });
                        if offset_ms > max_offset_ms {
                            issues.push(format!("NTP offset {:.1}ms exceeds {:.0}ms threshold",
                                offset_ms, max_offset_ms));
                        }
                    }
                }
            }
        }
    }

    let status = if issues.is_empty() {
        VerificationStatus::Pass
    } else if issues.iter().any(|i| i.contains("not running")) {
        VerificationStatus::Fail
    } else {
        VerificationStatus::Warn
    };

    let summary = if issues.is_empty() {
        let offset_str = measured_offset
            .map(|o| format!(", offset={:.1}ms", o))
            .unwrap_or_default();
        format!("w32time active{}", offset_str)
    } else {
        issues.join("; ")
    };

    Ok((status, summary, evidence))
}
