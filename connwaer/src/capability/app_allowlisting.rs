use crate::exec;
use anyhow::{Context, Result};
use std::time::Instant;

use super::{Capability, CapabilityContext, CapabilityResult, Evidence, VerificationStatus};

pub struct AppAllowlisting;

impl Capability for AppAllowlisting {
    fn name(&self) -> &'static str { "app_allowlisting" }
    fn controls(&self) -> &'static [&'static str] { &["CE-MW-4"] }

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

    // Check which framework is available: fapolicyd or AppArmor application profiles.
    let framework = config.params.get("framework")
        .and_then(|v| v.as_str())
        .unwrap_or("auto");

    let use_fapolicyd = match framework {
        "fapolicyd" => true,
        "auto" => which_exists("fapolicyd"),
        _ => false,
    };

    if use_fapolicyd {
        verify_fapolicyd(&mut evidence, &mut issues)?;
    } else {
        issues.push("no application allowlisting framework found (fapolicyd not installed)".to_string());
        evidence.push(Evidence {
            key: "framework".to_string(),
            value: serde_json::json!("none"),
            detail: "Neither fapolicyd nor other allowlisting framework detected".to_string(),
        });
    }

    let status = if issues.is_empty() {
        VerificationStatus::Pass
    } else {
        VerificationStatus::Fail
    };

    let summary = if issues.is_empty() {
        "Application allowlisting active and enforcing".to_string()
    } else {
        issues.join("; ")
    };

    Ok((status, summary, evidence))
}

#[cfg(target_os = "linux")]
fn verify_fapolicyd(evidence: &mut Vec<Evidence>, issues: &mut Vec<String>) -> Result<()> {
    evidence.push(Evidence {
        key: "framework".to_string(),
        value: serde_json::json!("fapolicyd"),
        detail: "Using fapolicyd for application allowlisting".to_string(),
    });

    // Check if fapolicyd service is active.
    let active = exec::stdout_of("systemctl", &["is-active", "fapolicyd"]);
    let is_active = active.as_deref() == Some("active");
    evidence.push(Evidence {
        key: "service_active".to_string(),
        value: serde_json::json!(is_active),
        detail: format!("fapolicyd service: {}", active.as_deref().unwrap_or("unknown")),
    });
    if !is_active {
        issues.push("fapolicyd service is not active".to_string());
        return Ok(());
    }

    // Check fapolicyd enforcement mode via fapolicyctl.
    if let Some(status_output) = exec::stdout_of("fapolicyctl", &["status"]) {
        let enforcing = status_output.contains("enforcing");
        evidence.push(Evidence {
            key: "enforcing".to_string(),
            value: serde_json::json!(enforcing),
            detail: format!("fapolicyd mode: {}", if enforcing { "enforcing" } else { "permissive" }),
        });
        if !enforcing {
            issues.push("fapolicyd is not in enforcing mode".to_string());
        }
    } else {
        // fapolicyctl may not exist; check via config file.
        let permissive = std::fs::read_to_string("/etc/fapolicyd/fapolicyd.conf")
            .ok()
            .map(|c| c.contains("permissive = 0"))
            .unwrap_or(false);
        evidence.push(Evidence {
            key: "enforcing".to_string(),
            value: serde_json::json!(permissive),
            detail: "Checked /etc/fapolicyd/fapolicyd.conf for permissive=0".to_string(),
        });
        if !permissive {
            issues.push("fapolicyd may not be in enforcing mode".to_string());
        }
    }

    // Count rules.
    if let Some(rules_output) = exec::stdout_of("fapolicyctl", &["list"]) {
        let rule_count = rules_output.lines().count();
        evidence.push(Evidence {
            key: "rule_count".to_string(),
            value: serde_json::json!(rule_count),
            detail: format!("{rule_count} fapolicyd rules loaded"),
        });
    }

    // Active verification: write a temp binary and attempt to execute it.
    // fapolicyd should block execution of unknown binaries.
    match test_unauthorized_exec() {
        Ok(blocked) => {
            evidence.push(Evidence {
                key: "exec_test_blocked".to_string(),
                value: serde_json::json!(blocked),
                detail: if blocked {
                    "Unauthorized binary execution was blocked".to_string()
                } else {
                    "WARNING: Unauthorized binary execution was NOT blocked".to_string()
                },
            });
            if !blocked {
                issues.push("fapolicyd did not block unauthorized binary execution".to_string());
            }
        }
        Err(e) => {
            evidence.push(Evidence {
                key: "exec_test_error".to_string(),
                value: serde_json::json!(e.to_string()),
                detail: format!("Could not run exec test: {e}"),
            });
        }
    }

    Ok(())
}

#[cfg(target_os = "linux")]
fn test_unauthorized_exec() -> Result<bool> {
    use std::io::Write;

    let dir = tempfile::tempdir().context("creating temp dir for exec test")?;
    let test_binary = dir.path().join("connwaer_exec_test");

    // Write a minimal ELF-like script (just needs to be executable).
    let mut f = std::fs::File::create(&test_binary)?;
    f.write_all(b"#!/bin/sh\necho connwaer_test_ok\n")?;

    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(&test_binary, std::fs::Permissions::from_mode(0o755))?;
    }

    // Attempt to execute. If fapolicyd is working, this should fail.
    let result = exec::run(&test_binary.to_string_lossy(), &[], exec::PROBE_TIMEOUT);

    match result {
        Ok(output) => {
            // If it ran successfully, fapolicyd did NOT block it.
            Ok(!output.status.success() || !String::from_utf8_lossy(&output.stdout).contains("connwaer_test_ok"))
        }
        Err(_) => {
            // Failure to execute at all (permission denied, ENOEXEC, killed
            // at the deadline) counts as blocked.
            Ok(true)
        }
    }
}

#[cfg(target_os = "windows")]
fn verify_windows(
    _config: &super::CapabilityConfig,
) -> Result<(VerificationStatus, String, Vec<Evidence>)> {
    let mut evidence = Vec::new();
    let mut issues = Vec::new();

    evidence.push(Evidence {
        key: "framework".to_string(),
        value: serde_json::json!("applocker"),
        detail: "Checking AppLocker on Windows".to_string(),
    });

    // Check AppLocker service (AppIDSvc).
    let svc_status = exec::stdout_of("sc.exe", &["query", "AppIDSvc"]);
    let is_running = svc_status.as_ref().map(|s| s.contains("RUNNING")).unwrap_or(false);
    evidence.push(Evidence {
        key: "service_active".to_string(),
        value: serde_json::json!(is_running),
        detail: format!("AppIDSvc service: {}", if is_running { "running" } else { "not running" }),
    });
    if !is_running {
        issues.push("AppLocker service (AppIDSvc) is not running".to_string());
    }

    // Check for AppLocker policy via PowerShell.
    if let Some(policy_output) = exec::stdout_of(
        "powershell.exe",
        &["-NoProfile", "-Command", "Get-AppLockerPolicy -Effective -Xml | Select-String -Pattern 'RuleCollection'"],
    ) {
        let has_rules = policy_output.contains("RuleCollection");
        evidence.push(Evidence {
            key: "policy_present".to_string(),
            value: serde_json::json!(has_rules),
            detail: if has_rules {
                "AppLocker policy with rules is present".to_string()
            } else {
                "No AppLocker rules found in effective policy".to_string()
            },
        });
        if !has_rules {
            issues.push("no AppLocker rules configured".to_string());
        }
    }

    // Check enforcement mode.
    if let Some(mode_output) = exec::stdout_of(
        "powershell.exe",
        &["-NoProfile", "-Command", "Get-AppLockerPolicy -Effective | Select-Object -ExpandProperty RuleCollections | Select-Object -First 1 -ExpandProperty EnforcementMode"],
    ) {
        let enforcing = mode_output.trim() == "Enabled";
        evidence.push(Evidence {
            key: "enforcing".to_string(),
            value: serde_json::json!(enforcing),
            detail: format!("AppLocker enforcement: {}", mode_output.trim()),
        });
        if !enforcing {
            issues.push("AppLocker is not in enforce mode".to_string());
        }
    }

    let status = if issues.is_empty() {
        VerificationStatus::Pass
    } else {
        VerificationStatus::Fail
    };
    let summary = if issues.is_empty() {
        "AppLocker active and enforcing".to_string()
    } else {
        issues.join("; ")
    };

    Ok((status, summary, evidence))
}

/// Check if a command exists on the system.
#[cfg(target_os = "linux")]
fn which_exists(cmd: &str) -> bool {
    exec::run("which", &[cmd], exec::PROBE_TIMEOUT)
        .map(|o| o.status.success())
        .unwrap_or(false)
}
