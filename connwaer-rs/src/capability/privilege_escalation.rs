use anyhow::Result;
use std::process::Command;
use std::time::Instant;

use super::{Capability, CapabilityContext, CapabilityResult, Evidence, VerificationStatus};

pub struct PrivilegeEscalation;

impl Capability for PrivilegeEscalation {
    fn name(&self) -> &'static str { "privilege_escalation" }
    fn display_name(&self) -> &'static str { "Privilege Escalation Audit" }
    fn controls(&self) -> &'static [&'static str] { &["NIST-AC-6", "CIS-6.1"] }

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
        // SUID scan can take a while on large filesystems.
        std::time::Duration::from_secs(600)
    }
}

#[cfg(target_os = "linux")]
fn verify_linux(
    config: &super::CapabilityConfig,
) -> Result<(VerificationStatus, String, Vec<Evidence>)> {
    let mut evidence = Vec::new();
    let mut issues = Vec::new();

    // Get the SUID allowlist from config.
    let suid_allowlist: Vec<String> = config.params.get("suid_allowlist")
        .and_then(|v| v.as_array())
        .map(|arr| arr.iter().filter_map(|v| v.as_str().map(String::from)).collect())
        .unwrap_or_else(default_suid_allowlist);

    // --- Find SUID binaries ---
    // Use timeout to prevent hanging on very large filesystems.
    let suid_output = run_command(
        "timeout",
        &["120", "find", "/", "-perm", "-4000", "-type", "f"],
    );

    let suid_binaries: Vec<String> = suid_output
        .map(|o| o.lines()
            .filter(|l| !l.is_empty())
            .map(String::from)
            .collect())
        .unwrap_or_default();

    let unexpected: Vec<&String> = suid_binaries.iter()
        .filter(|b| !suid_allowlist.iter().any(|a| b.as_str() == a.as_str()))
        .collect();

    evidence.push(Evidence {
        key: "suid_binaries".to_string(),
        value: serde_json::json!({
            "total": suid_binaries.len(),
            "allowed": suid_binaries.len() - unexpected.len(),
            "unexpected": unexpected.len(),
        }),
        detail: format!("{} SUID binaries found, {} in allowlist, {} unexpected",
            suid_binaries.len(), suid_binaries.len() - unexpected.len(), unexpected.len()),
    });

    if !unexpected.is_empty() {
        let unexpected_list: Vec<&str> = unexpected.iter().map(|s| s.as_str()).collect();
        evidence.push(Evidence {
            key: "suid_unexpected".to_string(),
            value: serde_json::json!(unexpected_list),
            detail: format!("Unexpected SUID binaries: {}", unexpected_list.join(", ")),
        });
        issues.push(format!("{} SUID binaries not in allowlist", unexpected.len()));
    }

    // --- Check for unexpected file capabilities ---
    let getcap_output = run_command(
        "timeout",
        &["60", "getcap", "-r", "/"],
    );

    if let Some(cap_out) = getcap_output {
        let capabilities: Vec<String> = cap_out.lines()
            .filter(|l| !l.is_empty())
            .map(String::from)
            .collect();

        // Filter out standard capabilities that are expected.
        let standard_caps = [
            "/usr/bin/ping",
            "/usr/bin/traceroute",
            "/usr/bin/mtr-packet",
            "/usr/sbin/clockdiff",
        ];
        let unexpected_caps: Vec<&String> = capabilities.iter()
            .filter(|c| !standard_caps.iter().any(|s| c.starts_with(s)))
            .collect();

        evidence.push(Evidence {
            key: "file_capabilities".to_string(),
            value: serde_json::json!({
                "total": capabilities.len(),
                "unexpected": unexpected_caps.len(),
                "entries": capabilities,
            }),
            detail: format!("{} file capabilities found, {} unexpected",
                capabilities.len(), unexpected_caps.len()),
        });

        if !unexpected_caps.is_empty() {
            let cap_list: Vec<&str> = unexpected_caps.iter().map(|s| s.as_str()).collect();
            evidence.push(Evidence {
                key: "capabilities_unexpected".to_string(),
                value: serde_json::json!(cap_list),
                detail: format!("Unexpected file capabilities: {}", cap_list.join(", ")),
            });
            issues.push(format!("{} unexpected file capabilities", unexpected_caps.len()));
        }
    }

    // --- Check for world-writable files in system directories ---
    let writable_output = run_command(
        "timeout",
        &["30", "find", "/usr", "/etc", "-type", "f", "-perm", "-o+w"],
    );

    if let Some(writable_out) = writable_output {
        let writable_files: Vec<String> = writable_out.lines()
            .filter(|l| !l.is_empty())
            .map(String::from)
            .collect();

        if !writable_files.is_empty() {
            evidence.push(Evidence {
                key: "world_writable_system_files".to_string(),
                value: serde_json::json!({
                    "count": writable_files.len(),
                    "files": &writable_files[..writable_files.len().min(20)],
                }),
                detail: format!("{} world-writable files in /usr or /etc", writable_files.len()),
            });
            issues.push(format!("{} world-writable files in system directories", writable_files.len()));
        }
    }

    let status = if issues.is_empty() {
        VerificationStatus::Pass
    } else if issues.iter().any(|i| i.contains("SUID")) {
        VerificationStatus::Warn
    } else {
        VerificationStatus::Warn
    };

    let summary = if issues.is_empty() {
        format!("{} SUID binaries, all in allowlist", suid_binaries.len())
    } else {
        issues.join("; ")
    };

    Ok((status, summary, evidence))
}

/// Default set of expected SUID binaries on a typical Linux system.
#[cfg(target_os = "linux")]
fn default_suid_allowlist() -> Vec<String> {
    vec![
        "/usr/bin/sudo",
        "/usr/bin/su",
        "/usr/bin/passwd",
        "/usr/bin/chsh",
        "/usr/bin/chfn",
        "/usr/bin/newgrp",
        "/usr/bin/gpasswd",
        "/usr/bin/mount",
        "/usr/bin/umount",
        "/usr/bin/fusermount",
        "/usr/bin/fusermount3",
        "/usr/bin/pkexec",
        "/usr/bin/crontab",
        "/usr/bin/at",
        "/usr/bin/ssh-agent",
        "/usr/bin/wall",
        "/usr/bin/expiry",
        "/usr/bin/chage",
        "/usr/sbin/pam_timestamp_check",
        "/usr/sbin/unix_chkpwd",
        "/usr/lib/polkit-1/polkit-agent-helper-1",
        "/usr/lib/dbus-1.0/dbus-daemon-launch-helper",
        "/usr/libexec/polkit-agent-helper-1",
        "/usr/libexec/dbus-daemon-launch-helper",
    ].into_iter().map(String::from).collect()
}

#[cfg(target_os = "windows")]
fn verify_windows(
    _config: &super::CapabilityConfig,
) -> Result<(VerificationStatus, String, Vec<Evidence>)> {
    let mut evidence = Vec::new();
    let mut issues = Vec::new();

    // --- Check for unquoted service paths ---
    if let Some(svc_out) = run_command(
        "powershell.exe",
        &["-NoProfile", "-Command",
          "Get-WmiObject win32_service | Where-Object { $_.PathName -notmatch '^\".+\"' -and $_.PathName -match '.+ .+' } | Select-Object Name, PathName | Format-List"],
    ) {
        let unquoted: Vec<String> = svc_out.split("Name")
            .filter(|s| !s.trim().is_empty() && s.contains("PathName"))
            .map(|s| s.trim().to_string())
            .collect();

        evidence.push(Evidence {
            key: "unquoted_service_paths".to_string(),
            value: serde_json::json!({
                "count": unquoted.len(),
                "services": &unquoted[..unquoted.len().min(10)],
            }),
            detail: format!("{} services with unquoted paths", unquoted.len()),
        });

        if !unquoted.is_empty() {
            issues.push(format!("{} services have unquoted paths (privilege escalation risk)", unquoted.len()));
        }
    }

    // --- Check users in local Administrators group ---
    if let Some(admin_out) = run_command("net", &["localgroup", "Administrators"]) {
        // Parse members between the dashed lines.
        let members: Vec<String> = admin_out.lines()
            .skip_while(|l| !l.starts_with("---"))
            .skip(1)
            .take_while(|l| !l.starts_with("The command"))
            .filter(|l| !l.trim().is_empty())
            .map(|l| l.trim().to_string())
            .collect();

        evidence.push(Evidence {
            key: "local_admins".to_string(),
            value: serde_json::json!({
                "count": members.len(),
                "members": members,
            }),
            detail: format!("{} members in local Administrators group: {}",
                members.len(), members.join(", ")),
        });

        // Flag if there are more than 3 admin accounts (configurable could be added).
        if members.len() > 3 {
            issues.push(format!("{} users in Administrators group (review recommended)", members.len()));
        }
    }

    // --- Check for AlwaysInstallElevated registry key ---
    if let Some(reg_out) = run_command(
        "reg",
        &["query", "HKLM\\SOFTWARE\\Policies\\Microsoft\\Windows\\Installer", "/v", "AlwaysInstallElevated"],
    ) {
        if reg_out.contains("0x1") {
            evidence.push(Evidence {
                key: "always_install_elevated".to_string(),
                value: serde_json::json!(true),
                detail: "AlwaysInstallElevated is enabled (privilege escalation risk)".to_string(),
            });
            issues.push("AlwaysInstallElevated is enabled".to_string());
        }
    }

    let status = if issues.is_empty() {
        VerificationStatus::Pass
    } else {
        VerificationStatus::Warn
    };

    let summary = if issues.is_empty() {
        "No privilege escalation vectors found".to_string()
    } else {
        issues.join("; ")
    };

    Ok((status, summary, evidence))
}

fn run_command(cmd: &str, args: &[&str]) -> Option<String> {
    Command::new(cmd)
        .args(args)
        .stderr(std::process::Stdio::null())
        .output()
        .ok()
        .map(|o| String::from_utf8_lossy(&o.stdout).trim().to_string())
}
