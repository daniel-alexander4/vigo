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

    // Tracks whether any test produced POSITIVE proof of immutability.
    // Pass requires at least one — a mere failure to write (permission
    // denied, read-only) proves nothing about WORM and must never be
    // reported as verified (honest-compliance: these controls escalate
    // FINRA SEC 17a-4 findings to "satisfied").
    let mut positive_proof = false;
    // Set when the probe was created THIS run and is still mutable —
    // possibly inside a WORM auto-commit window (e.g. SnapLock), so it
    // downgrades the run to Warn instead of Fail; the pre-existing probe
    // gives the real verdict from the next run onward.
    let mut commit_window_note = false;
    // Set when the probe couldn't be created at all (no positive proof
    // possible from Test 1).
    let mut probe_unavailable: Option<String> = None;

    // --- Test 1: probe-file immutability ---
    //
    // WORM means write-once-read-many: creating a NEW object is allowed;
    // mutating or deleting an EXISTING one is not. So the probe is a file
    // of our own: create it if missing (on genuine WORM it becomes a
    // permanent, tiny resident of the store — by design), then attempt to
    // overwrite, append to, and delete it. All three blocked on a probe
    // is positive proof; any succeeding on a pre-existing probe is a hard
    // failure. The delete attempt runs LAST and only if overwrite/append
    // were blocked, so a mutable store keeps its probe for next run's
    // pre-existing (Fail) verdict rather than resetting to fresh.
    let probe = worm_dir.join(".connwaer_worm_test");
    let pre_existing = probe.exists();
    let mut probe_present = pre_existing;
    if !pre_existing {
        match std::fs::File::create(&probe) {
            Ok(mut f) => {
                let _ = f.write_all(b"connwaer WORM probe - this file must remain immutable\n");
                probe_present = true;
            }
            Err(e) => {
                probe_unavailable = Some(format!("{:?}: {e}", e.kind()));
            }
        }
    }

    if probe_present {
        let overwrote = std::fs::OpenOptions::new()
            .write(true)
            .truncate(true)
            .open(&probe)
            .and_then(|mut f| f.write_all(b"connwaer WORM probe - this file must remain immutable\n"))
            .is_ok();
        let appended = std::fs::OpenOptions::new()
            .append(true)
            .open(&probe)
            .and_then(|mut f| f.write_all(b"#"))
            .is_ok();
        // Only attempt delete if the store blocked the writes — see above.
        let deleted = if !overwrote && !appended {
            std::fs::remove_file(&probe).is_ok()
        } else {
            false
        };
        let mutable = overwrote || appended || deleted;

        evidence.push(Evidence {
            key: "probe_mutation".to_string(),
            value: serde_json::json!({
                "pre_existing": pre_existing,
                "overwrite_blocked": !overwrote,
                "append_blocked": !appended,
                "delete_blocked": !deleted,
            }),
            detail: if mutable {
                format!(
                    "probe file {} was mutable (overwrite blocked: {}, append blocked: {}, delete blocked: {})",
                    probe.display(), !overwrote, !appended, !deleted
                )
            } else {
                format!("probe file {} resisted overwrite, append, and delete", probe.display())
            },
        });

        if !mutable {
            positive_proof = true;
        } else if pre_existing {
            issues.push("existing probe file in the WORM path was modified/deleted — immutability not enforced".to_string());
        } else {
            // Freshly created and still mutable: possibly within a WORM
            // auto-commit window. Not proof either way — re-verify next run.
            commit_window_note = true;
        }
    } else {
        evidence.push(Evidence {
            key: "probe_unavailable".to_string(),
            value: serde_json::json!(probe_unavailable),
            detail: "could not create a probe file, so immutability could not be exercised — this proves lack of write access, not WORM".to_string(),
        });
    }

    // --- Test 2: Check mount options (Linux) ---
    #[cfg(target_os = "linux")]
    {
        if let Ok(mounts) = std::fs::read_to_string("/proc/mounts") {
            // Longest matching mount point wins, and matches only on path
            // component boundaries (`/d` must not claim `/data`).
            let mount_line = mounts.lines()
                .filter(|l| {
                    let parts: Vec<&str> = l.split_whitespace().collect();
                    if parts.len() < 2 {
                        return false;
                    }
                    let mp = parts[1].trim_end_matches('/');
                    worm_path.trim_end_matches('/') == mp
                        || (worm_path.starts_with(mp) && worm_path[mp.len()..].starts_with('/'))
                })
                .max_by_key(|l| l.split_whitespace().nth(1).map_or(0, |mp| mp.len()));

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
                        .all(|opt| options.split(',').any(|o| o == opt.trim()));
                    evidence.push(Evidence {
                        key: "expected_options_present".to_string(),
                        value: serde_json::json!(has_expected),
                        detail: format!("Expected options '{}': {}", expected,
                            if has_expected { "present" } else { "MISSING" }),
                    });
                    if has_expected {
                        // Operator explicitly declared the mount contract and
                        // the kernel confirms it — counts as positive proof.
                        positive_proof = true;
                    } else {
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
                positive_proof = true;
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

    // Pass requires POSITIVE proof (probe mutation blocked, cloud check
    // passed, or a declared-and-present mount contract) and zero issues.
    // No issues but no proof either → Warn: the check ran but could not
    // demonstrate immutability, and it must not attest what it can't show.
    let (status, summary) = if !issues.is_empty() {
        (VerificationStatus::Fail, issues.join("; "))
    } else if positive_proof {
        (
            VerificationStatus::Pass,
            "WORM immutability verified — probe mutation blocked / declared proof present".to_string(),
        )
    } else if commit_window_note {
        (
            VerificationStatus::Warn,
            "probe file created this run is still mutable — possibly within a WORM auto-commit window; re-verifies on the next run".to_string(),
        )
    } else {
        (
            VerificationStatus::Warn,
            "no positive immutability proof — the probe could not be created (no write access to the WORM path); grant connwaer write access to the ingest path, or configure cloud_check_command / expected_mount_options".to_string(),
        )
    };

    Ok((status, summary, evidence))
}

fn file_sha256(path: &str) -> Result<String> {
    use sha2::{Sha256, Digest};
    let data = std::fs::read(path)?;
    let hash = Sha256::digest(&data);
    Ok(hex::encode(hash))
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::HashMap;

    fn config(params: &[(&str, serde_json::Value)]) -> super::super::CapabilityConfig {
        super::super::CapabilityConfig {
            enabled: true,
            params: params.iter().map(|(k, v)| (k.to_string(), v.clone())).collect(),
            thresholds: HashMap::new(),
        }
    }

    fn worm_config(dir: &std::path::Path) -> super::super::CapabilityConfig {
        config(&[("worm_path", serde_json::json!(dir.to_str().unwrap()))])
    }

    #[test]
    fn no_worm_path_is_skipped() {
        let (status, _, _) = verify_worm(&config(&[])).unwrap();
        assert!(matches!(status, VerificationStatus::Skipped));
    }

    #[test]
    fn missing_path_is_fail() {
        let (status, summary, _) =
            verify_worm(&config(&[("worm_path", serde_json::json!("/nonexistent/worm/dir"))])).unwrap();
        assert!(matches!(status, VerificationStatus::Fail));
        assert!(summary.contains("does not exist"));
    }

    /// The false-attestation regression: a directory the process simply can't
    /// write to (ordinary POSIX perms) used to report Pass — "WORM verified".
    /// It proves nothing about WORM and must be Warn.
    #[test]
    fn unwritable_dir_is_warn_not_pass() {
        if nix::unistd::geteuid().is_root() {
            return; // root bypasses mode bits; the scenario can't be built
        }
        let dir = tempfile::TempDir::new().unwrap();
        let mut perms = std::fs::metadata(dir.path()).unwrap().permissions();
        std::os::unix::fs::PermissionsExt::set_mode(&mut perms, 0o555);
        std::fs::set_permissions(dir.path(), perms.clone()).unwrap();

        let (status, summary, _) = verify_worm(&worm_config(dir.path())).unwrap();

        std::os::unix::fs::PermissionsExt::set_mode(&mut perms, 0o755);
        std::fs::set_permissions(dir.path(), perms).unwrap();

        assert!(
            matches!(status, VerificationStatus::Warn),
            "no-write-access must be Warn (no proof), got {status}: {summary}"
        );
        assert!(summary.contains("no positive immutability proof"));
    }

    /// A plain writable directory: run 1 creates the probe and finds it
    /// mutable — Warn (possible WORM auto-commit window), probe left behind.
    /// Run 2 sees the pre-existing probe still mutable — hard Fail.
    #[test]
    fn writable_dir_warns_then_fails() {
        let dir = tempfile::TempDir::new().unwrap();
        let cfg = worm_config(dir.path());

        let (status1, summary1, _) = verify_worm(&cfg).unwrap();
        assert!(
            matches!(status1, VerificationStatus::Warn),
            "fresh mutable probe must be Warn, got {status1}: {summary1}"
        );
        assert!(summary1.contains("auto-commit window"));
        assert!(dir.path().join(".connwaer_worm_test").exists(), "probe must persist for the next run");

        let (status2, summary2, _) = verify_worm(&cfg).unwrap();
        assert!(
            matches!(status2, VerificationStatus::Fail),
            "pre-existing mutable probe must be Fail, got {status2}: {summary2}"
        );
        assert!(summary2.contains("immutability not enforced"));
    }

    #[test]
    fn cloud_check_success_is_positive_proof() {
        let dir = tempfile::TempDir::new().unwrap();
        let cfg = config(&[
            ("worm_path", serde_json::json!(dir.path().to_str().unwrap())),
            ("cloud_check_command", serde_json::json!("true")),
        ]);
        let (status, _, _) = verify_worm(&cfg).unwrap();
        assert!(matches!(status, VerificationStatus::Pass));
    }

    #[test]
    fn cloud_check_failure_is_fail() {
        let dir = tempfile::TempDir::new().unwrap();
        let cfg = config(&[
            ("worm_path", serde_json::json!(dir.path().to_str().unwrap())),
            ("cloud_check_command", serde_json::json!("false")),
        ]);
        let (status, summary, _) = verify_worm(&cfg).unwrap();
        assert!(matches!(status, VerificationStatus::Fail));
        assert!(summary.contains("cloud WORM check failed"));
    }

    #[test]
    fn tampered_verify_file_is_fail() {
        let dir = tempfile::TempDir::new().unwrap();
        let archived = dir.path().join("record.dat");
        std::fs::write(&archived, b"archived record").unwrap();
        let cfg = config(&[
            ("worm_path", serde_json::json!(dir.path().to_str().unwrap())),
            (
                "verify_files",
                serde_json::json!([{
                    "path": archived.to_str().unwrap(),
                    "sha256": "0000000000000000000000000000000000000000000000000000000000000000",
                }]),
            ),
        ]);
        let (status, summary, _) = verify_worm(&cfg).unwrap();
        assert!(matches!(status, VerificationStatus::Fail));
        assert!(summary.contains("hash mismatch"));
    }
}
