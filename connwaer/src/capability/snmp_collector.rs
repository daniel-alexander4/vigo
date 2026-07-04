use anyhow::Result;
use std::time::{Duration, Instant};

use super::{Capability, CapabilityContext, CapabilityResult, Evidence, VerificationStatus};

pub struct SnmpCollector;

impl Capability for SnmpCollector {
    fn name(&self) -> &'static str { "snmp_collector" }
    fn controls(&self) -> &'static [&'static str] { &[] }

    fn verify(&self, ctx: &CapabilityContext) -> Result<CapabilityResult> {
        let start = Instant::now();
        let started_at = chrono::Utc::now().to_rfc3339();

        let (status, summary, evidence) = poll_targets(&ctx.config)?;

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

fn poll_targets(
    config: &super::CapabilityConfig,
) -> Result<(VerificationStatus, String, Vec<Evidence>)> {
    let mut evidence = Vec::new();
    let mut issues = Vec::new();

    // Parse targets from config.
    let targets = config.params.get("targets")
        .and_then(|v| v.as_array())
        .cloned()
        .unwrap_or_default();

    if targets.is_empty() {
        return Ok((
            VerificationStatus::Skipped,
            "No SNMP targets configured".to_string(),
            vec![Evidence {
                key: "targets".to_string(),
                value: serde_json::json!(0),
                detail: "No targets configured in params.targets".to_string(),
            }],
        ));
    }

    evidence.push(Evidence {
        key: "target_count".to_string(),
        value: serde_json::json!(targets.len()),
        detail: format!("{} SNMP targets configured", targets.len()),
    });

    let mut polled = 0u32;
    let mut failed = 0u32;

    for target_val in &targets {
        let host = target_val.get("host").and_then(|v| v.as_str()).unwrap_or("127.0.0.1");
        let port = target_val.get("port").and_then(|v| v.as_u64()).unwrap_or(161) as u16;
        let community = target_val.get("community").and_then(|v| v.as_str()).unwrap_or("public");
        let oids = target_val.get("oids")
            .and_then(|v| v.as_array())
            .map(|arr| arr.iter().filter_map(|v| v.as_str()).collect::<Vec<_>>())
            .unwrap_or_else(|| vec!["1.3.6.1.2.1.1.1.0"]); // sysDescr

        let addr = format!("{host}:{port}");
        let label = target_val.get("label").and_then(|v| v.as_str()).unwrap_or(host);

        let mut target_evidence = serde_json::Map::new();
        target_evidence.insert("host".to_string(), serde_json::json!(addr));
        target_evidence.insert("label".to_string(), serde_json::json!(label));

        // Attempt SNMPv2c GET for each OID.
        let mut oid_results = Vec::new();
        let mut target_ok = true;

        for oid_str in &oids {
            match snmp_get_v2c(&addr, community, oid_str) {
                Ok(value) => {
                    oid_results.push(serde_json::json!({
                        "oid": oid_str,
                        "value": value,
                        "status": "ok",
                    }));
                }
                Err(e) => {
                    target_ok = false;
                    oid_results.push(serde_json::json!({
                        "oid": oid_str,
                        "error": e.to_string(),
                        "status": "error",
                    }));
                }
            }
        }

        target_evidence.insert("oids".to_string(), serde_json::json!(oid_results));
        target_evidence.insert("reachable".to_string(), serde_json::json!(target_ok));

        evidence.push(Evidence {
            key: format!("target_{label}"),
            value: serde_json::Value::Object(target_evidence),
            detail: format!("{label} ({addr}): {} OIDs polled", oids.len()),
        });

        polled += 1;
        if !target_ok {
            failed += 1;
            issues.push(format!("{label}: SNMP poll failed"));
        }
    }

    let status = if failed == 0 {
        VerificationStatus::Pass
    } else if failed < polled {
        VerificationStatus::Warn
    } else {
        VerificationStatus::Fail
    };

    let summary = if issues.is_empty() {
        format!("{polled} SNMP targets polled successfully")
    } else {
        format!("{}/{} targets failed: {}", failed, polled, issues.join(", "))
    };

    Ok((status, summary, evidence))
}

/// Send an SNMPv2c GET request and return the response value as a string.
/// Uses raw UDP — the `snmp` crate handles encoding/decoding.
fn snmp_get_v2c(addr: &str, community: &str, oid_str: &str) -> Result<String> {
    use snmp::SyncSession;

    let timeout = Duration::from_secs(5);
    let mut session = SyncSession::new(addr, community.as_bytes(), Some(timeout), 0)
        .map_err(|e| anyhow::anyhow!("SNMP session to {addr}: {e:?}"))?;

    let oid: Vec<u32> = oid_str.split('.')
        .filter_map(|s| s.parse().ok())
        .collect();

    let response = session.get(&oid)
        .map_err(|e| anyhow::anyhow!("SNMP GET {oid_str} from {addr}: {e:?}"))?;

    // Extract the first varbind value.
    let mut varbinds = response.varbinds;
    let result = varbinds.next()
        .map(|(_, val)| format!("{val:?}"))
        .unwrap_or_else(|| String::from("no response"));

    Ok(result)
}
