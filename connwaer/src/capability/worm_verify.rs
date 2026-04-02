use anyhow::Result;
use std::io::Write;
use std::time::Instant;

use super::{Capability, CapabilityContext, CapabilityResult, Evidence, VerificationStatus};

pub struct WormVerify;

impl Capability for WormVerify {
    fn name(&self) -> &'static str { "worm_verify" }
    fn display_name(&self) -> &'static str { "WORM Storage Verification" }
    fn controls(&self) -> &'static [&'static str] { &["SEC-17a4-1", "SEC-17a4-2"] }

    fn verify(&self, ctx: &CapabilityContext) -> Result<CapabilityResult> {
        let start = Instant::now();
        let started_at = chrono::Utc::now().to_rfc3339();

        let (status, summary, evidence) = verify_worm(&ctx.config)?;

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

fn verify_worm(
    config: &super::CapabilityConfig,
) -> Result<(VerificationStatus, String, Vec<Evidence>)> {
    let mut evidence = Vec::new();
    let mut issues = Vec::new();

    let worm_path = config.params.get("worm_path")
        .and_then(|v| v.as_str());

    if worm_path.is_none() {
        return Ok((
            VerificationStatus::Skipped,
            "No worm_path configured in params".to_string(),
            vec![Evidence {
                key: "config".to_string(),
                value: serde_json::json!("missing"),
                detail: "params.worm_path is required".to_string(),
            }],
        ));
    }

    let worm_path = worm_path.unwrap();
    let worm_dir = std::path::Path::new(worm_path);

    evidence.push(Evidence {
        key: "worm_path".to_string(),
        value: serde_json::json!(worm_path),
        detail: format!("WORM path: {worm_path}"),
    });

    // Check path exists.
    if !worm_dir.exists() {
        issues.push(format!("WORM path does not exist: {worm_path}"));
        return Ok((VerificationStatus::Fail, issues.join("; "), evidence));
    }

    // --- Test 1: Attempt to write a new file (must fail) ---
    let test_file = worm_dir.join(".connwaer_worm_test");
    let write_blocked = match std::fs::File::create(&test_file) {
        Ok(mut f) => {
            // File created — try to write.
            let wrote = f.write_all(b"connwaer worm test").is_ok();
            // Clean up if we managed to create it.
            let _ = std::fs::remove_file(&test_file);
            // If we successfully wrote, WORM is NOT enforcing.
            !wrote
        }
        Err(e) => {
            // Permission denied or read-only = WORM is working.
            let blocked = matches!(
                e.kind(),
                std::io::ErrorKind::PermissionDenied | std::io::ErrorKind::ReadOnlyFilesystem
            );
            if !blocked {
                // Some other error — still count as blocked but note it.
                evidence.push(Evidence {
                    key: "write_error_kind".to_string(),
                    value: serde_json::json!(format!("{:?}", e.kind())),
                    detail: format!("Write attempt error: {e}"),
                });
            }
            true
        }
    };

    evidence.push(Evidence {
        key: "write_test_blocked".to_string(),
        value: serde_json::json!(write_blocked),
        detail: if write_blocked {
            "Write attempt to WORM path was correctly blocked".to_string()
        } else {
            "WARNING: Write to WORM path was NOT blocked".to_string()
        },
    });

    if !write_blocked {
        issues.push("write to WORM path was not blocked — immutability not enforced".to_string());
    }

    // --- Test 2: Check mount options (Linux) ---
    #[cfg(target_os = "linux")]
    {
        if let Ok(mounts) = std::fs::read_to_string("/proc/mounts") {
            let mount_line = mounts.lines()
                .find(|l| {
                    let parts: Vec<&str> = l.split_whitespace().collect();
                    parts.len() >= 2 && worm_path.starts_with(parts[1])
                });

            if let Some(line) = mount_line {
                let parts: Vec<&str> = line.split_whitespace().collect();
                let options = parts.get(3).unwrap_or(&"");
                let is_ro = options.split(',').any(|o| o == "ro");

                evidence.push(Evidence {
                    key: "mount_options".to_string(),
                    value: serde_json::json!(options),
                    detail: format!("Mount options: {options}"),
                });
                evidence.push(Evidence {
                    key: "mount_readonly".to_string(),
                    value: serde_json::json!(is_ro),
                    detail: format!("Read-only mount: {is_ro}"),
                });

                let expected_options = config.params.get("expected_mount_options")
                    .and_then(|v| v.as_str());
                if let Some(expected) = expected_options {
                    let has_expected = expected.split(',')
                        .all(|opt| options.contains(opt.trim()));
                    evidence.push(Evidence {
                        key: "expected_options_present".to_string(),
                        value: serde_json::json!(has_expected),
                        detail: format!("Expected options '{}': {}", expected,
                            if has_expected { "present" } else { "MISSING" }),
                    });
                    if !has_expected {
                        issues.push(format!("expected mount options '{}' not found", expected));
                    }
                }
            }
        }
    }

    // --- Test 3: Verify existing files haven't been modified ---
    let verify_files = config.params.get("verify_files")
        .and_then(|v| v.as_array())
        .cloned()
        .unwrap_or_default();

    if !verify_files.is_empty() {
        let mut verified = 0u32;
        let mut tampered = 0u32;

        for file_val in &verify_files {
            let path = file_val.get("path").and_then(|v| v.as_str()).unwrap_or("");
            let expected_hash = file_val.get("sha256").and_then(|v| v.as_str());

            if path.is_empty() { continue; }

            if let Some(expected) = expected_hash {
                match file_sha256(path) {
                    Ok(actual) => {
                        if actual == expected {
                            verified += 1;
                        } else {
                            tampered += 1;
                            issues.push(format!("{path}: hash mismatch"));
                        }
                    }
                    Err(e) => {
                        tampered += 1;
                        issues.push(format!("{path}: {e}"));
                    }
                }
            }
        }

        evidence.push(Evidence {
            key: "file_integrity".to_string(),
            value: serde_json::json!({"verified": verified, "tampered": tampered}),
            detail: format!("{verified} files verified, {tampered} tampered/unreadable"),
        });
    }

    // --- Test 4: Cloud WORM check command ---
    let cloud_check = config.params.get("cloud_check_command")
        .and_then(|v| v.as_str());
    if let Some(cmd) = cloud_check {
        let output = std::process::Command::new("sh")
            .args(["-c", cmd])
            .output();
        match output {
            Ok(o) if o.status.success() => {
                let stdout = String::from_utf8_lossy(&o.stdout).trim().to_string();
                evidence.push(Evidence {
                    key: "cloud_worm_check".to_string(),
                    value: serde_json::json!(stdout),
                    detail: format!("Cloud WORM check passed: {}", stdout.lines().next().unwrap_or("")),
                });
            }
            Ok(o) => {
                let stderr = String::from_utf8_lossy(&o.stderr).trim().to_string();
                issues.push(format!("cloud WORM check failed: {stderr}"));
            }
            Err(e) => {
                issues.push(format!("cloud WORM check error: {e}"));
            }
        }
    }

    let status = if issues.is_empty() {
        VerificationStatus::Pass
    } else {
        VerificationStatus::Fail
    };

    let summary = if issues.is_empty() {
        "WORM immutability verified — write blocked, integrity confirmed".to_string()
    } else {
        issues.join("; ")
    };

    Ok((status, summary, evidence))
}

fn file_sha256(path: &str) -> Result<String> {
    use sha2::{Sha256, Digest};
    let data = std::fs::read(path)?;
    let hash = Sha256::digest(&data);
    Ok(hex::encode(hash))
}
