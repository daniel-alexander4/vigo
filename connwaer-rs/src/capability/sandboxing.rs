use anyhow::Result;
use std::process::Command;
use std::time::Instant;

use super::{Capability, CapabilityContext, CapabilityResult, Evidence, VerificationStatus};

pub struct Sandboxing;

impl Capability for Sandboxing {
    fn name(&self) -> &'static str { "sandboxing" }
    fn display_name(&self) -> &'static str { "Application Sandboxing" }
    fn controls(&self) -> &'static [&'static str] { &["CE-MW-5"] }

    fn supported(&self) -> bool {
        cfg!(target_os = "linux") || cfg!(target_os = "windows")
    }

    fn verify(&self, ctx: &CapabilityContext) -> Result<CapabilityResult> {
        let start = Instant::now();
        let started_at = chrono::Utc::now().to_rfc3339();

        #[cfg(target_os = "linux")]
        let (status, summary, evidence) = verify_linux()?;

        #[cfg(target_os = "windows")]
        let (status, summary, evidence) = verify_windows()?;

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
fn verify_linux() -> Result<(VerificationStatus, String, Vec<Evidence>)> {
    let mut evidence = Vec::new();
    let mut issues = Vec::new();
    let mut framework_found = false;

    // Check SELinux.
    if std::path::Path::new("/etc/selinux/config").exists() || which_exists("getenforce") {
        framework_found = true;
        verify_selinux(&mut evidence, &mut issues)?;
    }

    // Check AppArmor.
    if std::path::Path::new("/sys/module/apparmor").exists() || which_exists("aa-status") {
        framework_found = true;
        verify_apparmor(&mut evidence, &mut issues)?;
    }

    // Check seccomp availability.
    let seccomp = std::path::Path::new("/proc/sys/kernel/seccomp").exists()
        || std::fs::read_to_string("/proc/self/status")
            .ok()
            .map(|s| s.contains("Seccomp:"))
            .unwrap_or(false);
    evidence.push(Evidence {
        key: "seccomp_available".to_string(),
        value: serde_json::json!(seccomp),
        detail: format!("seccomp: {}", if seccomp { "available" } else { "not available" }),
    });

    if !framework_found {
        issues.push("no MAC framework detected (neither SELinux nor AppArmor)".to_string());
        evidence.push(Evidence {
            key: "framework".to_string(),
            value: serde_json::json!("none"),
            detail: "Neither SELinux nor AppArmor detected".to_string(),
        });
    }

    let status = if issues.is_empty() {
        VerificationStatus::Pass
    } else if framework_found {
        VerificationStatus::Warn
    } else {
        VerificationStatus::Fail
    };

    let summary = if issues.is_empty() {
        "MAC framework active and enforcing".to_string()
    } else {
        issues.join("; ")
    };

    Ok((status, summary, evidence))
}

#[cfg(target_os = "linux")]
fn verify_selinux(evidence: &mut Vec<Evidence>, issues: &mut Vec<String>) -> Result<()> {
    evidence.push(Evidence {
        key: "selinux_present".to_string(),
        value: serde_json::json!(true),
        detail: "SELinux detected".to_string(),
    });

    if let Some(mode) = run_command("getenforce", &[]) {
        let enforcing = mode.trim().eq_ignore_ascii_case("enforcing");
        evidence.push(Evidence {
            key: "selinux_mode".to_string(),
            value: serde_json::json!(mode.trim()),
            detail: format!("SELinux mode: {}", mode.trim()),
        });
        if !enforcing {
            issues.push(format!("SELinux is not enforcing (mode: {})", mode.trim()));
        }
    }

    if let Some(sestatus) = run_command("sestatus", &[]) {
        let policy_line = sestatus.lines()
            .find(|l| l.contains("Loaded policy name"))
            .map(|l| l.trim().to_string());
        if let Some(policy) = policy_line {
            evidence.push(Evidence {
                key: "selinux_policy".to_string(),
                value: serde_json::json!(policy),
                detail: policy,
            });
        }
    }

    Ok(())
}

#[cfg(target_os = "linux")]
fn verify_apparmor(evidence: &mut Vec<Evidence>, issues: &mut Vec<String>) -> Result<()> {
    evidence.push(Evidence {
        key: "apparmor_present".to_string(),
        value: serde_json::json!(true),
        detail: "AppArmor detected".to_string(),
    });

    if let Some(status_output) = run_command("aa-status", &["--json"]) {
        if let Ok(parsed) = serde_json::from_str::<serde_json::Value>(&status_output) {
            let enforce = parsed.get("profiles")
                .and_then(|p| p.get("enforce"))
                .and_then(|e| e.as_u64())
                .unwrap_or(0);
            let complain = parsed.get("profiles")
                .and_then(|p| p.get("complain"))
                .and_then(|e| e.as_u64())
                .unwrap_or(0);
            let unconfined = parsed.get("processes")
                .and_then(|p| p.get("unconfined"))
                .and_then(|e| e.as_u64())
                .unwrap_or(0);

            evidence.push(Evidence {
                key: "apparmor_enforce_profiles".to_string(),
                value: serde_json::json!(enforce),
                detail: format!("{enforce} profiles in enforce mode"),
            });
            evidence.push(Evidence {
                key: "apparmor_complain_profiles".to_string(),
                value: serde_json::json!(complain),
                detail: format!("{complain} profiles in complain mode"),
            });
            evidence.push(Evidence {
                key: "apparmor_unconfined".to_string(),
                value: serde_json::json!(unconfined),
                detail: format!("{unconfined} unconfined processes"),
            });

            if enforce == 0 {
                issues.push("AppArmor has no profiles in enforce mode".to_string());
            }
        }
    } else {
        // Fallback: read kernel module status.
        if let Ok(enabled) = std::fs::read_to_string("/sys/module/apparmor/parameters/enabled") {
            let is_enabled = enabled.trim() == "Y";
            evidence.push(Evidence {
                key: "apparmor_kernel_enabled".to_string(),
                value: serde_json::json!(is_enabled),
                detail: format!("AppArmor kernel module: {}", if is_enabled { "enabled" } else { "disabled" }),
            });
            if !is_enabled {
                issues.push("AppArmor kernel module is not enabled".to_string());
            }
        }
    }

    Ok(())
}

#[cfg(target_os = "windows")]
fn verify_windows() -> Result<(VerificationStatus, String, Vec<Evidence>)> {
    let mut evidence = Vec::new();
    let mut issues = Vec::new();

    // Check Credential Guard via DeviceGuard WMI.
    if let Some(dg_output) = run_command(
        "powershell.exe",
        &["-NoProfile", "-Command",
          "Get-CimInstance -ClassName Win32_DeviceGuard -Namespace root\\Microsoft\\Windows\\DeviceGuard | ConvertTo-Json -Compress"],
    ) {
        if let Ok(dg) = serde_json::from_str::<serde_json::Value>(&dg_output) {
            // SecurityServicesRunning: 1=Credential Guard, 2=HVCI
            let services = dg.get("SecurityServicesRunning")
                .and_then(|v| v.as_array())
                .cloned()
                .unwrap_or_default();
            let cg_running = services.iter().any(|v| v.as_u64() == Some(1));
            let hvci_running = services.iter().any(|v| v.as_u64() == Some(2));

            evidence.push(Evidence {
                key: "credential_guard".to_string(),
                value: serde_json::json!(cg_running),
                detail: format!("Credential Guard: {}", if cg_running { "running" } else { "not running" }),
            });
            evidence.push(Evidence {
                key: "hvci".to_string(),
                value: serde_json::json!(hvci_running),
                detail: format!("HVCI: {}", if hvci_running { "running" } else { "not running" }),
            });

            if !cg_running {
                issues.push("Credential Guard is not running".to_string());
            }

            // Code integrity enforcement.
            let ci_status = dg.get("CodeIntegrityPolicyEnforcementStatus")
                .and_then(|v| v.as_u64())
                .unwrap_or(0);
            let ci_enforced = ci_status == 2;
            evidence.push(Evidence {
                key: "wdac_enforced".to_string(),
                value: serde_json::json!(ci_enforced),
                detail: format!("WDAC enforcement status: {ci_status} (2=enforced)"),
            });
            if !ci_enforced {
                issues.push("WDAC code integrity is not enforced".to_string());
            }
        }
    }

    // Check Defender Application Guard.
    if let Some(dag_output) = run_command(
        "powershell.exe",
        &["-NoProfile", "-Command",
          "(Get-WindowsOptionalFeature -Online -FeatureName Windows-Defender-ApplicationGuard).State"],
    ) {
        let enabled = dag_output.trim().eq_ignore_ascii_case("enabled");
        evidence.push(Evidence {
            key: "defender_application_guard".to_string(),
            value: serde_json::json!(enabled),
            detail: format!("Defender Application Guard: {}", if enabled { "enabled" } else { "disabled" }),
        });
    }

    let status = if issues.is_empty() {
        VerificationStatus::Pass
    } else {
        VerificationStatus::Fail
    };
    let summary = if issues.is_empty() {
        "WDAC, Credential Guard, and HVCI active".to_string()
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
        .filter(|o| o.status.success())
        .map(|o| String::from_utf8_lossy(&o.stdout).trim().to_string())
}

#[cfg(target_os = "linux")]
fn which_exists(cmd: &str) -> bool {
    Command::new("which")
        .arg(cmd)
        .output()
        .map(|o| o.status.success())
        .unwrap_or(false)
}
