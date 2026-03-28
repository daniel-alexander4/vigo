use anyhow::Result;
use std::time::Instant;

use super::{Capability, CapabilityContext, CapabilityResult, Evidence, VerificationStatus};

pub struct HsmLifecycle;

impl Capability for HsmLifecycle {
    fn name(&self) -> &'static str { "hsm_lifecycle" }
    fn display_name(&self) -> &'static str { "HSM Key Lifecycle" }
    fn controls(&self) -> &'static [&'static str] { &["CCSS-9.1"] }

    fn verify(&self, ctx: &CapabilityContext) -> Result<CapabilityResult> {
        let start = Instant::now();
        let started_at = chrono::Utc::now().to_rfc3339();

        let (status, summary, evidence) = verify_hsm(&ctx.config)?;

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

fn verify_hsm(
    config: &super::CapabilityConfig,
) -> Result<(VerificationStatus, String, Vec<Evidence>)> {
    let mut evidence = Vec::new();

    let pkcs11_library = config.params.get("pkcs11_library")
        .and_then(|v| v.as_str());

    if pkcs11_library.is_none() {
        return Ok((
            VerificationStatus::Skipped,
            "No pkcs11_library configured — no HSM present".to_string(),
            vec![Evidence {
                key: "config".to_string(),
                value: serde_json::json!("missing"),
                detail: "params.pkcs11_library is required (path to PKCS#11 .so/.dll)".to_string(),
            }],
        ));
    }

    let lib_path = pkcs11_library.unwrap();
    let _slot_id = config.params.get("slot_id")
        .and_then(|v| v.as_u64())
        .unwrap_or(0);
    let expiry_warning_days = config.thresholds.get("expiry_warning_days")
        .and_then(|v| v.as_u64())
        .unwrap_or(30);

    // Check library exists.
    if !std::path::Path::new(lib_path).exists() {
        return Ok((
            VerificationStatus::Skipped,
            format!("PKCS#11 library not found: {lib_path}"),
            vec![Evidence {
                key: "pkcs11_library".to_string(),
                value: serde_json::json!(lib_path),
                detail: format!("Library not found at {lib_path} — HSM may not be installed"),
            }],
        ));
    }

    evidence.push(Evidence {
        key: "pkcs11_library".to_string(),
        value: serde_json::json!(lib_path),
        detail: format!("PKCS#11 library: {lib_path}"),
    });

    // Attempt to load the PKCS#11 library and enumerate tokens/keys.
    // The cryptoki crate does runtime dlopen, so this is safe if the library exists.
    let mut issues = Vec::new();

    match load_and_audit_hsm(lib_path, _slot_id, expiry_warning_days) {
        Ok(hsm_evidence) => {
            for ev in &hsm_evidence {
                evidence.push(ev.clone());
            }
            // Check for issues in evidence.
            for ev in &hsm_evidence {
                if ev.key == "expired_keys" {
                    if let Some(count) = ev.value.as_u64() {
                        if count > 0 {
                            issues.push(format!("{count} expired keys found"));
                        }
                    }
                }
                if ev.key == "extractable_keys" {
                    if let Some(count) = ev.value.as_u64() {
                        if count > 0 {
                            issues.push(format!("{count} keys with extractable=true"));
                        }
                    }
                }
                if ev.key == "expiring_soon" {
                    if let Some(count) = ev.value.as_u64() {
                        if count > 0 {
                            issues.push(format!("{count} keys expiring within {expiry_warning_days} days"));
                        }
                    }
                }
            }
        }
        Err(e) => {
            evidence.push(Evidence {
                key: "pkcs11_error".to_string(),
                value: serde_json::json!(e.to_string()),
                detail: format!("PKCS#11 error: {e}"),
            });
            issues.push(format!("could not access HSM: {e}"));
        }
    }

    let status = if issues.is_empty() {
        VerificationStatus::Pass
    } else if issues.iter().any(|i| i.contains("expired")) {
        VerificationStatus::Fail
    } else {
        VerificationStatus::Warn
    };

    let summary = if issues.is_empty() {
        "HSM keys audited — no expired or extractable keys found".to_string()
    } else {
        issues.join("; ")
    };

    Ok((status, summary, evidence))
}

/// Load the PKCS#11 library and audit key attributes.
/// Uses the cryptoki crate for runtime PKCS#11 binding.
fn load_and_audit_hsm(
    lib_path: &str,
    slot_id: u64,
    expiry_warning_days: u64,
) -> Result<Vec<Evidence>> {
    use cryptoki::context::{CInitializeArgs, Pkcs11};
    use cryptoki::object::{Attribute, AttributeType};

    let mut evidence = Vec::new();

    let pkcs11 = Pkcs11::new(lib_path)
        .map_err(|e| anyhow::anyhow!("loading PKCS#11 library: {e:?}"))?;

    pkcs11.initialize(CInitializeArgs::OsThreads)
        .map_err(|e| anyhow::anyhow!("PKCS#11 initialize: {e:?}"))?;

    // List slots.
    let slots = pkcs11.get_slots_with_token()
        .map_err(|e| anyhow::anyhow!("listing slots: {e:?}"))?;

    evidence.push(Evidence {
        key: "slots_with_token".to_string(),
        value: serde_json::json!(slots.len()),
        detail: format!("{} slots with tokens found", slots.len()),
    });

    if slots.is_empty() {
        evidence.push(Evidence {
            key: "warning".to_string(),
            value: serde_json::json!("no_tokens"),
            detail: "No token slots found — HSM may not be initialized".to_string(),
        });
        return Ok(evidence);
    }

    // Use the configured slot (or first available).
    let slot = if (slot_id as usize) < slots.len() {
        slots[slot_id as usize]
    } else {
        slots[0]
    };

    // Get token info.
    if let Ok(token_info) = pkcs11.get_token_info(slot) {
        evidence.push(Evidence {
            key: "token_label".to_string(),
            value: serde_json::json!(token_info.label().trim()),
            detail: format!("Token: {}", token_info.label().trim()),
        });
    }

    // Open read-only session (no PIN needed for key enumeration on most HSMs).
    let session = pkcs11.open_ro_session(slot)
        .map_err(|e| anyhow::anyhow!("opening session: {e:?}"))?;

    // Enumerate objects (keys, certificates, etc.).
    let objects = session.find_objects(&[])
        .map_err(|e| anyhow::anyhow!("finding objects: {e:?}"))?;

    evidence.push(Evidence {
        key: "total_objects".to_string(),
        value: serde_json::json!(objects.len()),
        detail: format!("{} objects found in token", objects.len()),
    });

    // Inspect key attributes: CKA_END_DATE, CKA_EXTRACTABLE.
    let now = chrono::Utc::now().naive_utc().date();
    let warning_date = now + chrono::Duration::days(expiry_warning_days as i64);

    let mut expired_count: u64 = 0;
    let mut extractable_count: u64 = 0;
    let mut expiring_soon_count: u64 = 0;
    let mut inspected: u64 = 0;

    for obj in &objects {
        let attrs = match session.get_attributes(
            *obj,
            &[AttributeType::EndDate, AttributeType::Extractable],
        ) {
            Ok(a) => a,
            Err(_) => continue, // skip objects that don't support these attributes
        };
        inspected += 1;

        for attr in &attrs {
            match attr {
                Attribute::EndDate(date) => {
                    if !date.is_empty() {
                        let y = std::str::from_utf8(&date.year).unwrap_or("0000");
                        let m = std::str::from_utf8(&date.month).unwrap_or("00");
                        let d = std::str::from_utf8(&date.day).unwrap_or("00");
                        if let Ok(end) = chrono::NaiveDate::parse_from_str(
                            &format!("{y}-{m}-{d}"), "%Y-%m-%d",
                        ) {
                            if end < now {
                                expired_count += 1;
                            } else if end < warning_date {
                                expiring_soon_count += 1;
                            }
                        }
                    }
                }
                Attribute::Extractable(true) => {
                    extractable_count += 1;
                }
                _ => {}
            }
        }
    }

    evidence.push(Evidence {
        key: "inspected_objects".to_string(),
        value: serde_json::json!(inspected),
        detail: format!("{inspected} objects inspected for key attributes"),
    });

    evidence.push(Evidence {
        key: "expired_keys".to_string(),
        value: serde_json::json!(expired_count),
        detail: format!("{expired_count} keys with CKA_END_DATE in the past"),
    });

    evidence.push(Evidence {
        key: "extractable_keys".to_string(),
        value: serde_json::json!(extractable_count),
        detail: format!("{extractable_count} keys with CKA_EXTRACTABLE=true"),
    });

    evidence.push(Evidence {
        key: "expiring_soon".to_string(),
        value: serde_json::json!(expiring_soon_count),
        detail: format!("{expiring_soon_count} keys expiring within {expiry_warning_days} days"),
    });

    Ok(evidence)
}
