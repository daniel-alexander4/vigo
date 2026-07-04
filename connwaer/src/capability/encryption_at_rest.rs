use crate::exec;
use anyhow::Result;
use std::time::Instant;

use super::{Capability, CapabilityContext, CapabilityResult, Evidence, VerificationStatus};

pub struct EncryptionAtRest;

impl Capability for EncryptionAtRest {
    fn name(&self) -> &'static str { "encryption_at_rest" }
    fn controls(&self) -> &'static [&'static str] { &["HIPAA-164.312(a)(2)(iv)", "PCI-DSS-3.5.1"] }

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

    // Get configured volumes to check, or discover via lsblk.
    let volumes: Vec<String> = config.params.get("volumes")
        .and_then(|v| v.as_array())
        .map(|arr| arr.iter().filter_map(|v| v.as_str().map(String::from)).collect())
        .unwrap_or_default();

    if volumes.is_empty() {
        // Auto-discover: use lsblk to find all block devices and check for crypto_LUKS.
        if let Some(lsblk_out) = exec::stdout_of("lsblk", &["--fs", "--json"]) {
            match serde_json::from_str::<serde_json::Value>(&lsblk_out) {
                Ok(json) => {
                    let mut encrypted = Vec::new();
                    let mut unencrypted = Vec::new();
                    collect_devices(&json, &mut encrypted, &mut unencrypted);

                    evidence.push(Evidence {
                        key: "encrypted_devices".to_string(),
                        value: serde_json::json!(encrypted),
                        detail: format!("{} encrypted partition(s) found", encrypted.len()),
                    });
                    evidence.push(Evidence {
                        key: "unencrypted_devices".to_string(),
                        value: serde_json::json!(unencrypted),
                        detail: format!("{} unencrypted partition(s) with data filesystems", unencrypted.len()),
                    });

                    if encrypted.is_empty() {
                        issues.push("no LUKS-encrypted partitions found".to_string());
                    }
                }
                Err(e) => {
                    evidence.push(Evidence {
                        key: "lsblk_parse_error".to_string(),
                        value: serde_json::json!(e.to_string()),
                        detail: format!("Failed to parse lsblk JSON: {e}"),
                    });
                }
            }
        } else {
            issues.push("lsblk not available".to_string());
        }
    } else {
        // Check each configured volume via cryptsetup status.
        for vol in &volumes {
            let vol_name = vol.rsplit('/').next().unwrap_or(vol);

            if let Some(status_out) = exec::stdout_of("cryptsetup", &["status", vol_name]) {
                let is_active = status_out.contains("is active");
                let cipher = status_out.lines()
                    .find(|l| l.trim().starts_with("cipher:"))
                    .map(|l| l.trim().trim_start_matches("cipher:").trim().to_string())
                    .unwrap_or_default();
                let keysize = status_out.lines()
                    .find(|l| l.trim().starts_with("keysize:"))
                    .map(|l| l.trim().trim_start_matches("keysize:").trim().to_string())
                    .unwrap_or_default();

                evidence.push(Evidence {
                    key: format!("volume_{}", vol_name),
                    value: serde_json::json!({
                        "device": vol,
                        "active": is_active,
                        "cipher": cipher,
                        "keysize": keysize,
                    }),
                    detail: format!("{}: {} (cipher: {}, keysize: {})",
                        vol, if is_active { "LUKS active" } else { "not active" },
                        cipher, keysize),
                });

                if !is_active {
                    issues.push(format!("{} is not an active LUKS volume", vol));
                }
            } else {
                // cryptsetup status failed -- try lsblk for this device.
                let fstype = exec::stdout_of("lsblk", &["--noheadings", "-o", "FSTYPE", vol])
                    .unwrap_or_default();
                let is_luks = fstype.trim() == "crypto_LUKS";

                evidence.push(Evidence {
                    key: format!("volume_{}", vol_name),
                    value: serde_json::json!({
                        "device": vol,
                        "active": false,
                        "fstype": fstype.trim(),
                        "is_luks": is_luks,
                    }),
                    detail: format!("{}: fstype={}, LUKS={}", vol, fstype.trim(), is_luks),
                });

                if !is_luks {
                    issues.push(format!("{} is not LUKS-encrypted (fstype: {})", vol, fstype.trim()));
                }
            }
        }
    }

    // Optional: custom TDE (Transparent Data Encryption) check command.
    if let Some(tde_cmd) = config.params.get("tde_check_command").and_then(|v| v.as_str()) {
        let parts: Vec<&str> = tde_cmd.split_whitespace().collect();
        if !parts.is_empty() {
            let tde_out = exec::stdout_of(parts[0], &parts[1..]);
            let tde_ok = tde_out.is_some();
            evidence.push(Evidence {
                key: "tde_check".to_string(),
                value: serde_json::json!({
                    "command": tde_cmd,
                    "success": tde_ok,
                    "output": tde_out.as_deref().unwrap_or(""),
                }),
                detail: format!("TDE check ({}): {}", tde_cmd,
                    if tde_ok { "passed" } else { "failed" }),
            });
            if !tde_ok {
                issues.push(format!("TDE check failed: {}", tde_cmd));
            }
        }
    }

    let status = if issues.is_empty() {
        VerificationStatus::Pass
    } else {
        VerificationStatus::Fail
    };

    let summary = if issues.is_empty() {
        "All configured volumes are encrypted".to_string()
    } else {
        issues.join("; ")
    };

    Ok((status, summary, evidence))
}

/// Recursively walk lsblk JSON to find encrypted and unencrypted devices.
#[cfg(target_os = "linux")]
fn collect_devices(json: &serde_json::Value, encrypted: &mut Vec<String>, unencrypted: &mut Vec<String>) {
    if let Some(devices) = json.get("blockdevices").and_then(|v| v.as_array()) {
        for dev in devices {
            walk_device(dev, encrypted, unencrypted);
        }
    }
}

#[cfg(target_os = "linux")]
fn walk_device(dev: &serde_json::Value, encrypted: &mut Vec<String>, unencrypted: &mut Vec<String>) {
    let name = dev.get("name").and_then(|v| v.as_str()).unwrap_or("");
    let fstype = dev.get("fstype").and_then(|v| v.as_str()).unwrap_or("");

    if fstype == "crypto_LUKS" {
        encrypted.push(name.to_string());
    } else if !fstype.is_empty() && fstype != "swap" && fstype != "vfat" {
        // Has a data filesystem but is not encrypted.
        unencrypted.push(format!("{} ({})", name, fstype));
    }

    if let Some(children) = dev.get("children").and_then(|v| v.as_array()) {
        for child in children {
            walk_device(child, encrypted, unencrypted);
        }
    }
}

#[cfg(target_os = "windows")]
fn verify_windows(
    config: &super::CapabilityConfig,
) -> Result<(VerificationStatus, String, Vec<Evidence>)> {
    let mut evidence = Vec::new();
    let mut issues = Vec::new();

    // Default drives to check: C:
    let drives: Vec<String> = config.params.get("volumes")
        .and_then(|v| v.as_array())
        .map(|arr| arr.iter().filter_map(|v| v.as_str().map(String::from)).collect())
        .unwrap_or_else(|| vec!["C:".to_string()]);

    for drive in &drives {
        if let Some(bde_out) = exec::stdout_of("manage-bde", &["-status", drive]) {
            let protection_on = bde_out.contains("Protection On");
            let encryption_pct = bde_out.lines()
                .find(|l| l.contains("Percentage Encrypted"))
                .and_then(|l| {
                    l.split(':').nth(1)
                        .and_then(|v| v.trim().trim_end_matches('%').trim().parse::<f64>().ok())
                })
                .unwrap_or(0.0);

            let encryption_method = bde_out.lines()
                .find(|l| l.contains("Encryption Method"))
                .map(|l| l.split(':').nth(1).unwrap_or("").trim().to_string())
                .unwrap_or_default();

            let key_protectors = bde_out.lines()
                .filter(|l| l.contains("Key Protector"))
                .count();

            evidence.push(Evidence {
                key: format!("bitlocker_{}", drive.replace(':', "")),
                value: serde_json::json!({
                    "drive": drive,
                    "protection_on": protection_on,
                    "encryption_pct": encryption_pct,
                    "encryption_method": encryption_method,
                    "key_protectors": key_protectors,
                }),
                detail: format!("{}: protection={}, encrypted={:.0}%, method={}",
                    drive, if protection_on { "on" } else { "off" },
                    encryption_pct, encryption_method),
            });

            if !protection_on {
                issues.push(format!("{} BitLocker protection is off", drive));
            }
            if encryption_pct < 100.0 {
                issues.push(format!("{} only {:.0}% encrypted", drive, encryption_pct));
            }

            // Check for recovery key escrow (TPM + recovery key).
            let has_recovery = bde_out.contains("Numerical Password") ||
                               bde_out.contains("External Key");
            evidence.push(Evidence {
                key: format!("recovery_key_{}", drive.replace(':', "")),
                value: serde_json::json!(has_recovery),
                detail: format!("{}: recovery key protector {}",
                    drive, if has_recovery { "present" } else { "not found" }),
            });
            if !has_recovery {
                issues.push(format!("{} no recovery key protector found", drive));
            }
        } else {
            issues.push(format!("{} manage-bde not available or access denied", drive));
            evidence.push(Evidence {
                key: format!("bitlocker_{}", drive.replace(':', "")),
                value: serde_json::json!({"drive": drive, "error": "manage-bde failed"}),
                detail: format!("{}: could not query BitLocker status", drive),
            });
        }
    }

    // Optional TDE check.
    if let Some(tde_cmd) = config.params.get("tde_check_command").and_then(|v| v.as_str()) {
        let parts: Vec<&str> = tde_cmd.split_whitespace().collect();
        if !parts.is_empty() {
            let tde_out = exec::stdout_of(parts[0], &parts[1..]);
            let tde_ok = tde_out.is_some();
            evidence.push(Evidence {
                key: "tde_check".to_string(),
                value: serde_json::json!({"command": tde_cmd, "success": tde_ok}),
                detail: format!("TDE check: {}", if tde_ok { "passed" } else { "failed" }),
            });
            if !tde_ok {
                issues.push(format!("TDE check failed: {}", tde_cmd));
            }
        }
    }

    let status = if issues.is_empty() {
        VerificationStatus::Pass
    } else {
        VerificationStatus::Fail
    };

    let summary = if issues.is_empty() {
        format!("BitLocker active on all {} drive(s)", drives.len())
    } else {
        issues.join("; ")
    };

    Ok((status, summary, evidence))
}
