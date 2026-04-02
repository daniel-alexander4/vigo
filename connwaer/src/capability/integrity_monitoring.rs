use anyhow::Result;
use std::process::Command;
use std::time::Instant;

use super::{Capability, CapabilityContext, CapabilityResult, Evidence, VerificationStatus};

pub struct IntegrityMonitoring;

impl Capability for IntegrityMonitoring {
    fn name(&self) -> &'static str { "integrity_monitoring" }
    fn display_name(&self) -> &'static str { "File Integrity Monitoring" }
    fn controls(&self) -> &'static [&'static str] { &["PCI-DSS-11.5", "NIST-SI-7"] }

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

    fn timeout(&self) -> std::time::Duration {
        // AIDE check can take a long time on large systems.
        std::time::Duration::from_secs(600)
    }
}

#[cfg(target_os = "linux")]
fn verify_linux(
    config: &super::CapabilityConfig,
) -> Result<(VerificationStatus, String, Vec<Evidence>)> {
    let mut evidence = Vec::new();
    let mut issues = Vec::new();
    let mut tool_found = false;

    let aide_binary = config.params.get("aide_binary")
        .and_then(|v| v.as_str())
        .unwrap_or("aide");

    let expected_changes_max = config.thresholds.get("expected_changes_max")
        .and_then(|v| v.as_u64())
        .unwrap_or(0);

    // --- Check AIDE ---
    let aide_version = run_command(aide_binary, &["--version"]);
    let aide_installed = aide_version.is_some();

    evidence.push(Evidence {
        key: "aide_installed".to_string(),
        value: serde_json::json!(aide_installed),
        detail: if aide_installed {
            format!("AIDE installed: {}", aide_version.as_deref().unwrap_or("unknown version"))
        } else {
            format!("AIDE not found at '{}'", aide_binary)
        },
    });

    if aide_installed {
        tool_found = true;

        // Check if AIDE database exists.
        let db_paths = [
            "/var/lib/aide/aide.db",
            "/var/lib/aide/aide.db.gz",
            "/etc/aide/aide.db",
            "/var/lib/aide.db",
        ];
        let db_exists = db_paths.iter().any(|p| std::path::Path::new(p).exists());
        let db_path = db_paths.iter().find(|p| std::path::Path::new(p).exists())
            .unwrap_or(&"/var/lib/aide/aide.db");

        evidence.push(Evidence {
            key: "aide_db_exists".to_string(),
            value: serde_json::json!({
                "exists": db_exists,
                "path": db_path,
            }),
            detail: if db_exists {
                format!("AIDE database found at {}", db_path)
            } else {
                "AIDE database not found (run 'aide --init' first)".to_string()
            },
        });

        if !db_exists {
            issues.push("AIDE database not initialized".to_string());
        } else {
            // Optionally run AIDE check. This can be slow, so only do it if the DB exists.
            let run_check = config.params.get("run_aide_check")
                .and_then(|v| v.as_bool())
                .unwrap_or(true);

            if run_check {
                // aide --check exits 0 if no changes, non-zero if changes detected.
                let check_output = Command::new(aide_binary)
                    .args(["--check"])
                    .output()
                    .ok();

                if let Some(output) = check_output {
                    let stdout = String::from_utf8_lossy(&output.stdout);
                    let exit_code = output.status.code().unwrap_or(-1);

                    // Parse AIDE output for change counts.
                    let mut changed: u64 = 0;
                    let mut added: u64 = 0;
                    let mut removed: u64 = 0;

                    for line in stdout.lines() {
                        let line = line.trim();
                        // AIDE summary format: "Total number of entries: ...\n  Added entries: ...\n  Removed entries: ...\n  Changed entries: ..."
                        if line.starts_with("Added entries") || line.starts_with("Added files") {
                            added = parse_count_from_line(line);
                        } else if line.starts_with("Removed entries") || line.starts_with("Removed files") {
                            removed = parse_count_from_line(line);
                        } else if line.starts_with("Changed entries") || line.starts_with("Changed files") {
                            changed = parse_count_from_line(line);
                        }
                    }

                    let total_changes = changed + added + removed;

                    evidence.push(Evidence {
                        key: "aide_check".to_string(),
                        value: serde_json::json!({
                            "exit_code": exit_code,
                            "changed": changed,
                            "added": added,
                            "removed": removed,
                            "total_changes": total_changes,
                        }),
                        detail: format!("AIDE check: {} changed, {} added, {} removed (exit code {})",
                            changed, added, removed, exit_code),
                    });

                    if total_changes > expected_changes_max {
                        issues.push(format!("{} file changes detected (threshold: {})",
                            total_changes, expected_changes_max));
                    }
                }
            }
        }
    }

    // --- Check for OSSEC/Wazuh ---
    let ossec_paths = [
        "/var/ossec/bin/ossec-control",
        "/var/ossec/bin/wazuh-control",
    ];
    let mut ossec_name = "";
    let mut ossec_running = false;

    for (path, name) in ossec_paths.iter().zip(["OSSEC", "Wazuh"]) {
        if std::path::Path::new(path).exists() {
            tool_found = true;
            ossec_name = name;

            if let Some(status_out) = run_command(path, &["status"]) {
                ossec_running = status_out.contains("is running");
                evidence.push(Evidence {
                    key: format!("{}_status", name.to_lowercase()),
                    value: serde_json::json!({
                        "installed": true,
                        "running": ossec_running,
                    }),
                    detail: format!("{}: {}", name, if ossec_running { "running" } else { "not running" }),
                });
                if !ossec_running {
                    issues.push(format!("{} is installed but not running", name));
                }
            }
            break;
        }
    }

    // Also check via systemctl for wazuh-agent.
    if !tool_found {
        let wazuh_active = run_command("systemctl", &["is-active", "wazuh-agent"])
            .map(|s| s == "active")
            .unwrap_or(false);
        if wazuh_active {
            tool_found = true;
            ossec_name = "Wazuh";
            ossec_running = true;
            evidence.push(Evidence {
                key: "wazuh_status".to_string(),
                value: serde_json::json!({"installed": true, "running": true}),
                detail: "Wazuh agent: running (via systemctl)".to_string(),
            });
        }
    }

    if !tool_found {
        issues.push("no file integrity monitoring tool found (AIDE, OSSEC, or Wazuh)".to_string());
    }

    let status = if issues.is_empty() {
        VerificationStatus::Pass
    } else if issues.iter().any(|i| i.contains("no file integrity")) {
        VerificationStatus::Fail
    } else {
        VerificationStatus::Warn
    };

    let summary = if issues.is_empty() {
        let mut parts = Vec::new();
        if aide_installed { parts.push("AIDE".to_string()); }
        if !ossec_name.is_empty() && ossec_running { parts.push(ossec_name.to_string()); }
        format!("File integrity monitoring active: {}", parts.join(", "))
    } else {
        issues.join("; ")
    };

    Ok((status, summary, evidence))
}

/// Parse a count value from an AIDE summary line like "Changed entries:    5".
#[cfg(target_os = "linux")]
fn parse_count_from_line(line: &str) -> u64 {
    line.split(':')
        .nth(1)
        .and_then(|v| v.trim().parse::<u64>().ok())
        .unwrap_or(0)
}

#[cfg(target_os = "windows")]
fn verify_windows(
    config: &super::CapabilityConfig,
) -> Result<(VerificationStatus, String, Vec<Evidence>)> {
    let mut evidence = Vec::new();
    let mut issues = Vec::new();
    let mut tool_found = false;

    // --- Check for OSSEC/Wazuh service ---
    let ossec_services = ["OssecSvc", "WazuhSvc"];
    for svc in &ossec_services {
        if let Some(svc_out) = run_command("sc.exe", &["query", svc]) {
            let is_running = svc_out.contains("RUNNING");
            let name = if *svc == "OssecSvc" { "OSSEC" } else { "Wazuh" };
            tool_found = true;

            evidence.push(Evidence {
                key: format!("{}_service", name.to_lowercase()),
                value: serde_json::json!({
                    "service": svc,
                    "running": is_running,
                }),
                detail: format!("{}: {}", name, if is_running { "running" } else { "not running" }),
            });

            if !is_running {
                issues.push(format!("{} service is not running", name));
            }
        }
    }

    // --- Check for Tripwire ---
    if !tool_found {
        let tripwire_check = config.params.get("tripwire_binary")
            .and_then(|v| v.as_str())
            .unwrap_or("tripwire.exe");

        if let Some(tw_out) = run_command(tripwire_check, &["--version"]) {
            tool_found = true;
            evidence.push(Evidence {
                key: "tripwire_installed".to_string(),
                value: serde_json::json!({"installed": true, "version": tw_out}),
                detail: format!("Tripwire installed: {}", tw_out),
            });
        }
    }

    // --- Check Windows built-in: System File Checker last result ---
    if let Some(sfc_log) = run_command(
        "powershell.exe",
        &["-NoProfile", "-Command",
          "Select-String -Path $env:SystemRoot\\Logs\\CBS\\CBS.log -Pattern 'Verify complete' -Context 0,0 | Select-Object -Last 1 | ForEach-Object { $_.Line }"],
    ) {
        if !sfc_log.is_empty() {
            let integrity_ok = sfc_log.contains("no integrity violations");
            evidence.push(Evidence {
                key: "sfc_status".to_string(),
                value: serde_json::json!({
                    "last_result": sfc_log,
                    "integrity_ok": integrity_ok,
                }),
                detail: format!("SFC last result: {}", sfc_log),
            });
        }
    }

    if !tool_found {
        issues.push("no file integrity monitoring tool found (OSSEC, Wazuh, or Tripwire)".to_string());
    }

    let status = if issues.is_empty() {
        VerificationStatus::Pass
    } else if issues.iter().any(|i| i.contains("no file integrity")) {
        VerificationStatus::Fail
    } else {
        VerificationStatus::Warn
    };

    let summary = if issues.is_empty() {
        "File integrity monitoring active".to_string()
    } else {
        issues.join("; ")
    };

    Ok((status, summary, evidence))
}

fn run_command(cmd: &str, args: &[&str]) -> Option<String> {
    Command::new(cmd)
        .args(args)
        .output()
        .ok()
        .map(|o| String::from_utf8_lossy(&o.stdout).trim().to_string())
}
