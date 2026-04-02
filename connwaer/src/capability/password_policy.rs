use anyhow::Result;
use std::process::Command;
use std::time::Instant;

use super::{Capability, CapabilityContext, CapabilityResult, Evidence, VerificationStatus};

pub struct PasswordPolicy;

impl Capability for PasswordPolicy {
    fn name(&self) -> &'static str { "password_policy" }
    fn display_name(&self) -> &'static str { "Password Policy Testing" }
    fn controls(&self) -> &'static [&'static str] { &["NIST-800-63B", "PCI-DSS-8.3"] }

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

    let required_min_length = config.thresholds.get("min_length")
        .and_then(|v| v.as_u64())
        .unwrap_or(12) as i64;

    let require_complexity = config.thresholds.get("require_complexity")
        .and_then(|v| v.as_bool())
        .unwrap_or(true);

    // --- Parse /etc/security/pwquality.conf ---
    let mut pwq_minlen: Option<i64> = None;
    let mut pwq_dcredit: Option<i64> = None;
    let mut pwq_ucredit: Option<i64> = None;
    let mut pwq_lcredit: Option<i64> = None;
    let mut pwq_ocredit: Option<i64> = None;
    let mut pwq_minclass: Option<i64> = None;

    if let Ok(content) = std::fs::read_to_string("/etc/security/pwquality.conf") {
        for line in content.lines() {
            let line = line.trim();
            if line.starts_with('#') || line.is_empty() {
                continue;
            }
            if let Some((key, val)) = line.split_once('=') {
                let key = key.trim();
                let val = val.trim().parse::<i64>().ok();
                match key {
                    "minlen" => pwq_minlen = val,
                    "dcredit" => pwq_dcredit = val,
                    "ucredit" => pwq_ucredit = val,
                    "lcredit" => pwq_lcredit = val,
                    "ocredit" => pwq_ocredit = val,
                    "minclass" => pwq_minclass = val,
                    _ => {}
                }
            }
        }

        evidence.push(Evidence {
            key: "pwquality_conf".to_string(),
            value: serde_json::json!({
                "minlen": pwq_minlen,
                "dcredit": pwq_dcredit,
                "ucredit": pwq_ucredit,
                "lcredit": pwq_lcredit,
                "ocredit": pwq_ocredit,
                "minclass": pwq_minclass,
            }),
            detail: format!("pwquality.conf: minlen={}, dcredit={}, ucredit={}, lcredit={}, ocredit={}, minclass={}",
                pwq_minlen.map(|v| v.to_string()).unwrap_or_else(|| "unset".into()),
                pwq_dcredit.map(|v| v.to_string()).unwrap_or_else(|| "unset".into()),
                pwq_ucredit.map(|v| v.to_string()).unwrap_or_else(|| "unset".into()),
                pwq_lcredit.map(|v| v.to_string()).unwrap_or_else(|| "unset".into()),
                pwq_ocredit.map(|v| v.to_string()).unwrap_or_else(|| "unset".into()),
                pwq_minclass.map(|v| v.to_string()).unwrap_or_else(|| "unset".into()),
            ),
        });

        // Check minlen against threshold.
        if let Some(minlen) = pwq_minlen {
            if minlen < required_min_length {
                issues.push(format!("minlen {} is below required {}", minlen, required_min_length));
            }
        } else {
            issues.push("minlen not set in pwquality.conf".to_string());
        }

        // Check complexity: dcredit/ucredit/lcredit should be negative (require N chars of type)
        // or minclass >= 3.
        if require_complexity {
            let has_class_requirement = pwq_minclass.map(|v| v >= 3).unwrap_or(false);
            let has_credit_requirements =
                pwq_dcredit.map(|v| v < 0).unwrap_or(false) &&
                pwq_ucredit.map(|v| v < 0).unwrap_or(false) &&
                pwq_lcredit.map(|v| v < 0).unwrap_or(false);

            if !has_class_requirement && !has_credit_requirements {
                issues.push("complexity requirements not enforced (dcredit/ucredit/lcredit not set or minclass < 3)".to_string());
            }
        }
    } else {
        evidence.push(Evidence {
            key: "pwquality_conf".to_string(),
            value: serde_json::json!(null),
            detail: "/etc/security/pwquality.conf not found".to_string(),
        });
        issues.push("pwquality.conf not found".to_string());
    }

    // --- Check PAM configuration for pam_pwquality or pam_cracklib ---
    let pam_paths = [
        "/etc/pam.d/common-password",
        "/etc/pam.d/system-auth",
        "/etc/pam.d/password-auth",
    ];
    let mut pam_module_found = false;
    let mut pam_module_name = String::new();

    for path in &pam_paths {
        if let Ok(content) = std::fs::read_to_string(path) {
            for line in content.lines() {
                let line = line.trim();
                if line.starts_with('#') {
                    continue;
                }
                if line.contains("pam_pwquality.so") {
                    pam_module_found = true;
                    pam_module_name = "pam_pwquality".to_string();
                } else if line.contains("pam_cracklib.so") {
                    pam_module_found = true;
                    pam_module_name = "pam_cracklib".to_string();
                }
            }
        }
    }

    evidence.push(Evidence {
        key: "pam_module".to_string(),
        value: serde_json::json!({
            "found": pam_module_found,
            "module": pam_module_name,
        }),
        detail: if pam_module_found {
            format!("PAM password quality module: {}", pam_module_name)
        } else {
            "No PAM password quality module found (pam_pwquality/pam_cracklib)".to_string()
        },
    });

    if !pam_module_found {
        issues.push("no PAM password quality module configured".to_string());
    }

    // --- Optional: test weak password rejection via pwscore ---
    if let Some(pwscore_out) = run_command_with_stdin("pwscore", &[], "weak") {
        // pwscore returns a score 0-100, or an error for weak passwords.
        let score: i64 = pwscore_out.trim().parse().unwrap_or(-1);
        let rejected = score < 0; // pwscore exits non-zero for weak passwords, so we won't get output.
        evidence.push(Evidence {
            key: "pwscore_weak_test".to_string(),
            value: serde_json::json!({"password": "weak", "score": score, "rejected": rejected}),
            detail: format!("pwscore test with 'weak': score={} ({})", score,
                if rejected { "rejected" } else { "accepted -- policy too weak" }),
        });
        if !rejected {
            issues.push("pwscore accepted trivially weak password".to_string());
        }
    } else {
        // pwscore returning None means it exited non-zero (rejected the password) or isn't installed.
        // Check if pwscore exists first.
        if run_command("which", &["pwscore"]).is_some() {
            // pwscore exists and rejected the password -- good.
            evidence.push(Evidence {
                key: "pwscore_weak_test".to_string(),
                value: serde_json::json!({"password": "weak", "rejected": true}),
                detail: "pwscore correctly rejected weak password".to_string(),
            });
        }
    }

    let status = if issues.is_empty() {
        VerificationStatus::Pass
    } else {
        VerificationStatus::Fail
    };

    let summary = if issues.is_empty() {
        format!("Password policy enforced: minlen={}, complexity={}",
            pwq_minlen.unwrap_or(0), if require_complexity { "required" } else { "not required" })
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

    let required_min_length = config.thresholds.get("min_length")
        .and_then(|v| v.as_u64())
        .unwrap_or(12);

    let require_complexity = config.thresholds.get("require_complexity")
        .and_then(|v| v.as_bool())
        .unwrap_or(true);

    // Query password policy via `net accounts`.
    if let Some(net_out) = run_command("net", &["accounts"]) {
        let mut min_length: u64 = 0;
        let mut lockout_threshold: u64 = 0;
        let mut max_age: u64 = 0;
        let mut min_age: u64 = 0;

        for line in net_out.lines() {
            let line = line.trim();
            if line.starts_with("Minimum password length") {
                min_length = line.split_whitespace().last()
                    .and_then(|v| v.parse().ok())
                    .unwrap_or(0);
            } else if line.starts_with("Lockout threshold") {
                lockout_threshold = line.split_whitespace().last()
                    .and_then(|v| v.parse().ok())
                    .unwrap_or(0);
            } else if line.starts_with("Maximum password age") {
                max_age = line.split_whitespace().last()
                    .and_then(|v| v.parse().ok())
                    .unwrap_or(0);
            } else if line.starts_with("Minimum password age") {
                min_age = line.split_whitespace().last()
                    .and_then(|v| v.parse().ok())
                    .unwrap_or(0);
            }
        }

        evidence.push(Evidence {
            key: "net_accounts".to_string(),
            value: serde_json::json!({
                "min_length": min_length,
                "lockout_threshold": lockout_threshold,
                "max_age_days": max_age,
                "min_age_days": min_age,
            }),
            detail: format!("net accounts: minlen={}, lockout={}, max_age={}, min_age={}",
                min_length, lockout_threshold, max_age, min_age),
        });

        if min_length < required_min_length {
            issues.push(format!("minimum password length {} is below required {}", min_length, required_min_length));
        }

        if lockout_threshold == 0 {
            issues.push("account lockout threshold is 0 (disabled)".to_string());
        }
    } else {
        issues.push("net accounts command failed".to_string());
    }

    // Check complexity via secedit export or PowerShell.
    if require_complexity {
        if let Some(complexity_out) = run_command(
            "powershell.exe",
            &["-NoProfile", "-Command",
              "(Get-ItemProperty 'HKLM:\\SYSTEM\\CurrentControlSet\\Control\\Lsa' -Name 'NoLMHash' -ErrorAction SilentlyContinue).NoLMHash"],
        ) {
            // Also check complexity requirement via net accounts or secedit.
            let _ = complexity_out; // NoLMHash is a separate check.
        }

        // Use secedit to export and check password complexity.
        let secedit_check = run_command(
            "powershell.exe",
            &["-NoProfile", "-Command",
              "secedit /export /cfg $env:TEMP\\connwaer_secpol.cfg /quiet; Get-Content $env:TEMP\\connwaer_secpol.cfg | Select-String 'PasswordComplexity'"],
        );

        if let Some(secedit_out) = secedit_check {
            let complexity_enabled = secedit_out.contains("= 1");
            evidence.push(Evidence {
                key: "password_complexity".to_string(),
                value: serde_json::json!(complexity_enabled),
                detail: format!("Password complexity: {}", if complexity_enabled { "enabled" } else { "disabled" }),
            });
            if !complexity_enabled {
                issues.push("password complexity requirement is disabled".to_string());
            }
        }
    }

    let status = if issues.is_empty() {
        VerificationStatus::Pass
    } else {
        VerificationStatus::Fail
    };

    let summary = if issues.is_empty() {
        "Password policy meets requirements".to_string()
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

/// Run a command with data piped to stdin.
#[cfg(target_os = "linux")]
fn run_command_with_stdin(cmd: &str, args: &[&str], stdin_data: &str) -> Option<String> {
    use std::io::Write;
    let mut child = Command::new(cmd)
        .args(args)
        .stdin(std::process::Stdio::piped())
        .stdout(std::process::Stdio::piped())
        .stderr(std::process::Stdio::piped())
        .spawn()
        .ok()?;

    if let Some(ref mut stdin) = child.stdin {
        let _ = stdin.write_all(stdin_data.as_bytes());
    }

    let output = child.wait_with_output().ok()?;
    if output.status.success() {
        Some(String::from_utf8_lossy(&output.stdout).trim().to_string())
    } else {
        None
    }
}
