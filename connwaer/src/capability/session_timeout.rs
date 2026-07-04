#[cfg(target_os = "windows")]
use crate::exec;
use anyhow::Result;
#[cfg(target_os = "windows")]
use std::process::Command;
use std::time::Instant;

use super::{Capability, CapabilityContext, CapabilityResult, Evidence, VerificationStatus};

pub struct SessionTimeout;

impl Capability for SessionTimeout {
    fn name(&self) -> &'static str { "session_timeout" }
    fn controls(&self) -> &'static [&'static str] { &["HIPAA-164.312(a)(2)(iii)", "PCI-DSS-8.2.8"] }

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

    let max_idle_seconds = config.thresholds.get("max_idle_seconds")
        .and_then(|v| v.as_u64())
        .unwrap_or(900);

    // --- Parse sshd_config for ClientAliveInterval and ClientAliveCountMax ---
    let sshd_config_paths = [
        "/etc/ssh/sshd_config",
        "/etc/ssh/sshd_config.d",
    ];

    let mut client_alive_interval: Option<u64> = None;
    let mut client_alive_count_max: Option<u64> = None;

    // Parse main sshd_config.
    if let Ok(content) = std::fs::read_to_string("/etc/ssh/sshd_config") {
        for line in content.lines() {
            let line = line.trim();
            if line.starts_with('#') || line.is_empty() {
                continue;
            }
            let parts: Vec<&str> = line.split_whitespace().collect();
            if parts.len() >= 2 {
                match parts[0].to_lowercase().as_str() {
                    "clientaliveinterval" => {
                        client_alive_interval = parts[1].parse().ok();
                    }
                    "clientalivecountmax" => {
                        client_alive_count_max = parts[1].parse().ok();
                    }
                    _ => {}
                }
            }
        }
    }

    // Also check sshd_config.d drop-in files (later values override).
    if let Ok(entries) = std::fs::read_dir("/etc/ssh/sshd_config.d") {
        let mut files: Vec<_> = entries.filter_map(|e| e.ok()).collect();
        files.sort_by_key(|e| e.file_name());
        for entry in files {
            if entry.path().extension().map(|e| e == "conf").unwrap_or(false) {
                if let Ok(content) = std::fs::read_to_string(entry.path()) {
                    for line in content.lines() {
                        let line = line.trim();
                        if line.starts_with('#') || line.is_empty() {
                            continue;
                        }
                        let parts: Vec<&str> = line.split_whitespace().collect();
                        if parts.len() >= 2 {
                            match parts[0].to_lowercase().as_str() {
                                "clientaliveinterval" => {
                                    client_alive_interval = parts[1].parse().ok();
                                }
                                "clientalivecountmax" => {
                                    client_alive_count_max = parts[1].parse().ok();
                                }
                                _ => {}
                            }
                        }
                    }
                }
            }
        }
    }

    // Compute effective SSH timeout.
    // Default ClientAliveInterval is 0 (disabled), default ClientAliveCountMax is 3.
    let interval = client_alive_interval.unwrap_or(0);
    let count_max = client_alive_count_max.unwrap_or(3);
    let effective_ssh_timeout = if interval > 0 {
        interval * count_max
    } else {
        0 // 0 means no timeout (disabled)
    };

    evidence.push(Evidence {
        key: "ssh_timeout".to_string(),
        value: serde_json::json!({
            "client_alive_interval": interval,
            "client_alive_count_max": count_max,
            "effective_timeout_seconds": effective_ssh_timeout,
            "config_paths": sshd_config_paths,
        }),
        detail: format!("SSH: ClientAliveInterval={}, ClientAliveCountMax={}, effective timeout={}s",
            interval, count_max, effective_ssh_timeout),
    });

    if effective_ssh_timeout == 0 {
        issues.push("SSH idle timeout is disabled (ClientAliveInterval=0)".to_string());
    } else if effective_ssh_timeout > max_idle_seconds {
        issues.push(format!("SSH effective timeout {}s exceeds {}s threshold",
            effective_ssh_timeout, max_idle_seconds));
    }

    // --- Check TMOUT in /etc/profile and /etc/profile.d/ ---
    let mut tmout_value: Option<u64> = None;
    let mut tmout_source = String::new();

    if let Ok(content) = std::fs::read_to_string("/etc/profile") {
        if let Some(val) = parse_tmout(&content) {
            tmout_value = Some(val);
            tmout_source = "/etc/profile".to_string();
        }
    }

    // Check /etc/profile.d/*.sh files.
    if let Ok(entries) = std::fs::read_dir("/etc/profile.d") {
        for entry in entries.filter_map(|e| e.ok()) {
            if entry.path().extension().map(|e| e == "sh").unwrap_or(false) {
                if let Ok(content) = std::fs::read_to_string(entry.path()) {
                    if let Some(val) = parse_tmout(&content) {
                        tmout_value = Some(val);
                        tmout_source = entry.path().to_string_lossy().to_string();
                    }
                }
            }
        }
    }

    evidence.push(Evidence {
        key: "shell_tmout".to_string(),
        value: serde_json::json!({
            "tmout_seconds": tmout_value,
            "source": tmout_source,
        }),
        detail: if let Some(val) = tmout_value {
            format!("TMOUT={}s (from {})", val, tmout_source)
        } else {
            "TMOUT not set in /etc/profile or /etc/profile.d/".to_string()
        },
    });

    if let Some(tmout) = tmout_value {
        if tmout > max_idle_seconds {
            issues.push(format!("TMOUT {}s exceeds {}s threshold", tmout, max_idle_seconds));
        }
    } else {
        issues.push("TMOUT not configured (shell sessions have no idle timeout)".to_string());
    }

    // --- Overall effective idle timeout ---
    let effective_timeout = match (effective_ssh_timeout, tmout_value) {
        (0, None) => 0,
        (0, Some(t)) => t,
        (s, None) => s,
        (s, Some(t)) => s.min(t),
    };

    evidence.push(Evidence {
        key: "effective_idle_timeout".to_string(),
        value: serde_json::json!({
            "seconds": effective_timeout,
            "threshold": max_idle_seconds,
            "pass": effective_timeout > 0 && effective_timeout <= max_idle_seconds,
        }),
        detail: format!("Effective idle timeout: {}s (threshold: {}s)",
            effective_timeout, max_idle_seconds),
    });

    let status = if issues.is_empty() {
        VerificationStatus::Pass
    } else if effective_timeout == 0 {
        VerificationStatus::Fail
    } else {
        VerificationStatus::Warn
    };

    let summary = if issues.is_empty() {
        format!("Session timeout enforced: SSH={}s, TMOUT={}s (max {}s)",
            effective_ssh_timeout,
            tmout_value.unwrap_or(0),
            max_idle_seconds)
    } else {
        issues.join("; ")
    };

    Ok((status, summary, evidence))
}

/// Parse TMOUT=NNN from shell profile content. Handles `export TMOUT=NNN`,
/// `TMOUT=NNN`, and `readonly TMOUT=NNN`.
#[cfg(target_os = "linux")]
fn parse_tmout(content: &str) -> Option<u64> {
    let mut value = None;
    for line in content.lines() {
        let line = line.trim();
        if line.starts_with('#') {
            continue;
        }
        // Match patterns: TMOUT=N, export TMOUT=N, readonly TMOUT=N
        let stripped = line
            .trim_start_matches("export ")
            .trim_start_matches("readonly ")
            .trim();
        if let Some(rest) = stripped.strip_prefix("TMOUT=") {
            // Remove quotes and trailing comments.
            let val_str = rest.split('#').next().unwrap_or("")
                .trim()
                .trim_matches('"')
                .trim_matches('\'');
            if let Ok(v) = val_str.parse::<u64>() {
                value = Some(v);
            }
        }
    }
    value
}

#[cfg(target_os = "windows")]
fn verify_windows(
    config: &super::CapabilityConfig,
) -> Result<(VerificationStatus, String, Vec<Evidence>)> {
    let mut evidence = Vec::new();
    let mut issues = Vec::new();

    let max_idle_seconds = config.thresholds.get("max_idle_seconds")
        .and_then(|v| v.as_u64())
        .unwrap_or(900);

    // --- Check screen lock timeout via registry ---
    let inactivity_timeout = exec::stdout_of(
        "reg",
        &["query",
          "HKLM\\SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\Policies\\System",
          "/v", "InactivityTimeoutSecs"],
    );

    let mut screen_lock_seconds: Option<u64> = None;

    if let Some(reg_out) = inactivity_timeout {
        // Parse registry output: "    InactivityTimeoutSecs    REG_DWORD    0x384"
        for line in reg_out.lines() {
            if line.contains("InactivityTimeoutSecs") {
                let parts: Vec<&str> = line.split_whitespace().collect();
                if let Some(hex_val) = parts.last() {
                    if let Some(stripped) = hex_val.strip_prefix("0x") {
                        screen_lock_seconds = u64::from_str_radix(stripped, 16).ok();
                    } else {
                        screen_lock_seconds = hex_val.parse().ok();
                    }
                }
            }
        }
    }

    evidence.push(Evidence {
        key: "screen_lock_timeout".to_string(),
        value: serde_json::json!({
            "seconds": screen_lock_seconds,
            "configured": screen_lock_seconds.is_some(),
        }),
        detail: if let Some(secs) = screen_lock_seconds {
            format!("Screen lock timeout: {}s", secs)
        } else {
            "Screen lock timeout not configured via Group Policy".to_string()
        },
    });

    if let Some(secs) = screen_lock_seconds {
        if secs > max_idle_seconds {
            issues.push(format!("screen lock timeout {}s exceeds {}s threshold", secs, max_idle_seconds));
        }
    } else {
        issues.push("screen lock inactivity timeout not configured".to_string());
    }

    // --- Check screensaver lock ---
    let screensaver_active = exec::stdout_of(
        "reg",
        &["query",
          "HKCU\\Control Panel\\Desktop",
          "/v", "ScreenSaverIsSecure"],
    );

    if let Some(ss_out) = screensaver_active {
        let is_secure = ss_out.contains("0x1") || ss_out.contains("    1");
        evidence.push(Evidence {
            key: "screensaver_lock".to_string(),
            value: serde_json::json!(is_secure),
            detail: format!("Screensaver lock on resume: {}", if is_secure { "enabled" } else { "disabled" }),
        });
    }

    // --- Check RDP idle disconnect timeout ---
    let rdp_idle = exec::stdout_of(
        "reg",
        &["query",
          "HKLM\\SOFTWARE\\Policies\\Microsoft\\Windows NT\\Terminal Services",
          "/v", "MaxIdleTime"],
    );

    let mut rdp_idle_ms: Option<u64> = None;
    if let Some(rdp_out) = rdp_idle {
        for line in rdp_out.lines() {
            if line.contains("MaxIdleTime") {
                let parts: Vec<&str> = line.split_whitespace().collect();
                if let Some(hex_val) = parts.last() {
                    if let Some(stripped) = hex_val.strip_prefix("0x") {
                        rdp_idle_ms = u64::from_str_radix(stripped, 16).ok();
                    } else {
                        rdp_idle_ms = hex_val.parse().ok();
                    }
                }
            }
        }
    }

    let rdp_idle_seconds = rdp_idle_ms.map(|ms| ms / 1000);

    evidence.push(Evidence {
        key: "rdp_idle_timeout".to_string(),
        value: serde_json::json!({
            "milliseconds": rdp_idle_ms,
            "seconds": rdp_idle_seconds,
            "configured": rdp_idle_ms.is_some(),
        }),
        detail: if let Some(secs) = rdp_idle_seconds {
            format!("RDP idle disconnect: {}s", secs)
        } else {
            "RDP idle disconnect not configured via Group Policy".to_string()
        },
    });

    if let Some(secs) = rdp_idle_seconds {
        if secs > max_idle_seconds {
            issues.push(format!("RDP idle timeout {}s exceeds {}s threshold", secs, max_idle_seconds));
        }
    }

    // --- Determine overall effective timeout ---
    let effective = [screen_lock_seconds, rdp_idle_seconds]
        .iter()
        .filter_map(|v| *v)
        .min();

    evidence.push(Evidence {
        key: "effective_idle_timeout".to_string(),
        value: serde_json::json!({
            "seconds": effective,
            "threshold": max_idle_seconds,
            "pass": effective.map(|e| e <= max_idle_seconds).unwrap_or(false),
        }),
        detail: format!("Effective idle timeout: {} (threshold: {}s)",
            effective.map(|e| format!("{}s", e)).unwrap_or_else(|| "not set".to_string()),
            max_idle_seconds),
    });

    let status = if issues.is_empty() {
        VerificationStatus::Pass
    } else if screen_lock_seconds.is_none() && rdp_idle_seconds.is_none() {
        VerificationStatus::Fail
    } else {
        VerificationStatus::Warn
    };

    let summary = if issues.is_empty() {
        format!("Session timeouts configured within {}s threshold", max_idle_seconds)
    } else {
        issues.join("; ")
    };

    Ok((status, summary, evidence))
}
