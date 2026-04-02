use anyhow::Result;
use std::net::{TcpStream, ToSocketAddrs};
use std::time::{Duration, Instant};

use super::{Capability, CapabilityContext, CapabilityResult, Evidence, VerificationStatus};

pub struct NetworkSegmentation;

impl Capability for NetworkSegmentation {
    fn name(&self) -> &'static str { "network_segmentation" }
    fn display_name(&self) -> &'static str { "Network Segmentation" }
    fn controls(&self) -> &'static [&'static str] { &["PCI-DSS-1.3", "NIST-AC-4"] }

    fn verify(&self, ctx: &CapabilityContext) -> Result<CapabilityResult> {
        let start = Instant::now();
        let started_at = chrono::Utc::now().to_rfc3339();

        let (status, summary, evidence) = verify_segmentation(&ctx.config)?;

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

fn verify_segmentation(
    config: &super::CapabilityConfig,
) -> Result<(VerificationStatus, String, Vec<Evidence>)> {
    let mut evidence = Vec::new();
    let mut issues = Vec::new();

    let blocked_targets: Vec<String> = config.params.get("blocked_targets")
        .and_then(|v| v.as_array())
        .map(|arr| arr.iter().filter_map(|v| v.as_str().map(String::from)).collect())
        .unwrap_or_default();

    let allowed_targets: Vec<String> = config.params.get("allowed_targets")
        .and_then(|v| v.as_array())
        .map(|arr| arr.iter().filter_map(|v| v.as_str().map(String::from)).collect())
        .unwrap_or_default();

    if blocked_targets.is_empty() && allowed_targets.is_empty() {
        return Ok((
            VerificationStatus::Skipped,
            "No targets configured (set params.blocked_targets and/or params.allowed_targets)".to_string(),
            vec![],
        ));
    }

    let timeout = Duration::from_secs(5);

    // Test blocked targets -- these MUST be unreachable.
    for target in &blocked_targets {
        let reachable = test_tcp_connect(target, timeout);

        evidence.push(Evidence {
            key: format!("blocked_{}", target.replace(':', "_")),
            value: serde_json::json!({
                "target": target,
                "expected": "unreachable",
                "actual": if reachable { "reachable" } else { "unreachable" },
                "pass": !reachable,
            }),
            detail: format!("blocked target {}: {}",
                target, if reachable { "REACHABLE (fail)" } else { "unreachable (pass)" }),
        });

        if reachable {
            issues.push(format!("{} should be blocked but is reachable", target));
        }
    }

    // Test allowed targets -- these MUST be reachable.
    for target in &allowed_targets {
        let reachable = test_tcp_connect(target, timeout);

        evidence.push(Evidence {
            key: format!("allowed_{}", target.replace(':', "_")),
            value: serde_json::json!({
                "target": target,
                "expected": "reachable",
                "actual": if reachable { "reachable" } else { "unreachable" },
                "pass": reachable,
            }),
            detail: format!("allowed target {}: {}",
                target, if reachable { "reachable (pass)" } else { "UNREACHABLE (fail)" }),
        });

        if !reachable {
            issues.push(format!("{} should be allowed but is unreachable", target));
        }
    }

    let total = blocked_targets.len() + allowed_targets.len();
    let passed = total - issues.len();

    evidence.push(Evidence {
        key: "summary".to_string(),
        value: serde_json::json!({
            "total_targets": total,
            "passed": passed,
            "failed": issues.len(),
        }),
        detail: format!("{}/{} segmentation tests passed", passed, total),
    });

    let status = if issues.is_empty() {
        VerificationStatus::Pass
    } else {
        VerificationStatus::Fail
    };

    let summary = if issues.is_empty() {
        format!("All {} segmentation tests passed", total)
    } else {
        issues.join("; ")
    };

    Ok((status, summary, evidence))
}

/// Attempt a TCP connection to host:port with the given timeout.
/// Returns true if the connection succeeds (target is reachable).
fn test_tcp_connect(target: &str, timeout: Duration) -> bool {
    // Resolve the address first.
    let addrs: Vec<_> = match target.to_socket_addrs() {
        Ok(addrs) => addrs.collect(),
        Err(_) => return false,
    };

    for addr in addrs {
        if TcpStream::connect_timeout(&addr, timeout).is_ok() {
            return true;
        }
    }
    false
}
