use anyhow::{Context, Result};
use std::time::{Duration, Instant};

use super::{Capability, CapabilityContext, CapabilityResult, Evidence, VerificationStatus};
use crate::rng;

pub struct RngValidation;

impl Capability for RngValidation {
    fn name(&self) -> &'static str { "rng_validation" }
    fn display_name(&self) -> &'static str { "RNG Statistical Validation" }
    fn controls(&self) -> &'static [&'static str] { &["NGC-TS-4.2", "MGA-RGR-6.1"] }

    fn timeout(&self) -> Duration {
        Duration::from_secs(600) // 10 minutes — NIST tests are computationally expensive
    }

    fn verify(&self, ctx: &CapabilityContext) -> Result<CapabilityResult> {
        let start = Instant::now();
        let started_at = chrono::Utc::now().to_rfc3339();

        let (status, summary, evidence) = run_rng_validation(&ctx.config)?;

        let elapsed = start.elapsed();

        // Record to trends for drift detection.
        let passed_count = evidence.iter()
            .find(|e| e.key == "tests_passed")
            .and_then(|e| e.value.as_u64())
            .unwrap_or(0);
        let total_count = evidence.iter()
            .find(|e| e.key == "tests_total")
            .and_then(|e| e.value.as_u64())
            .unwrap_or(0);
        let metrics = serde_json::json!({
            "tests_passed": passed_count,
            "tests_total": total_count,
            "duration_ms": elapsed.as_millis() as u64,
        });
        let _ = ctx.trends_db.record_run("rng_validation", &status.to_string(), &metrics);

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

fn run_rng_validation(
    config: &super::CapabilityConfig,
) -> Result<(VerificationStatus, String, Vec<Evidence>)> {
    let mut evidence = Vec::new();

    let source = config.params.get("source")
        .and_then(|v| v.as_str())
        .unwrap_or("/dev/urandom");
    let sample_bytes = config.params.get("sample_size_bytes")
        .and_then(|v| v.as_u64())
        .unwrap_or(125_000) as usize; // 125KB = 1M bits
    let alpha = config.thresholds.get("alpha")
        .and_then(|v| v.as_f64())
        .unwrap_or(0.01);

    evidence.push(Evidence {
        key: "source".to_string(),
        value: serde_json::json!(source),
        detail: format!("RNG source: {source}"),
    });
    evidence.push(Evidence {
        key: "sample_bits".to_string(),
        value: serde_json::json!(sample_bytes * 8),
        detail: format!("Sample size: {} bits ({} bytes)", sample_bytes * 8, sample_bytes),
    });
    evidence.push(Evidence {
        key: "alpha".to_string(),
        value: serde_json::json!(alpha),
        detail: format!("Significance level: {alpha}"),
    });

    // Read random data from source. Only whole-file `read` a regular file —
    // a character device like /dev/urandom never EOFs, so an unbounded read
    // would grow until RLIMIT_AS kills the process. For anything that isn't a
    // regular file, read exactly `sample_bytes` and stop.
    let is_regular_file = std::fs::metadata(source)
        .map(|m| m.is_file())
        .with_context(|| format!("stat RNG source: {source}"))?;

    let data = if is_regular_file {
        std::fs::read(source)
            .with_context(|| format!("reading RNG source: {source}"))?
    } else {
        use std::io::Read;
        let mut f = std::fs::File::open(source)
            .with_context(|| format!("opening RNG source: {source}"))?;
        let mut buf = vec![0u8; sample_bytes];
        f.read_exact(&mut buf)
            .with_context(|| format!("reading {sample_bytes} bytes from RNG source: {source}"))?;
        buf
    };

    let data = if data.len() > sample_bytes { &data[..sample_bytes] } else { &data };
    let bits = rng::bytes_to_bits(data);

    evidence.push(Evidence {
        key: "actual_bits".to_string(),
        value: serde_json::json!(bits.len()),
        detail: format!("Actual bits read: {}", bits.len()),
    });

    // Run NIST SP 800-22 test suite.
    let results = rng::run_suite(&bits, alpha)
        .context("running NIST SP 800-22 test suite")?;

    let total = results.len();
    let passed = results.iter().filter(|r| r.passed).count();
    let failed = results.iter().filter(|r| !r.passed).count();

    evidence.push(Evidence {
        key: "tests_total".to_string(),
        value: serde_json::json!(total),
        detail: format!("{total} NIST tests executed"),
    });
    evidence.push(Evidence {
        key: "tests_passed".to_string(),
        value: serde_json::json!(passed),
        detail: format!("{passed}/{total} tests passed"),
    });

    // Per-test results.
    let test_details: Vec<serde_json::Value> = results.iter().map(|r| {
        serde_json::json!({
            "name": r.name,
            "p_value": format!("{:.6}", r.p_value),
            "passed": r.passed,
        })
    }).collect();

    evidence.push(Evidence {
        key: "test_results".to_string(),
        value: serde_json::json!(test_details),
        detail: format!("{passed} passed, {failed} failed out of {total} tests"),
    });

    // List failed tests explicitly.
    let failed_names: Vec<&str> = results.iter()
        .filter(|r| !r.passed)
        .map(|r| r.name.as_str())
        .collect();
    if !failed_names.is_empty() {
        evidence.push(Evidence {
            key: "failed_tests".to_string(),
            value: serde_json::json!(failed_names),
            detail: format!("Failed: {}", failed_names.join(", ")),
        });
    }

    // Status: pass if all tests pass, warn if >80% pass, fail otherwise.
    let pass_rate = passed as f64 / total as f64;
    let status = if failed == 0 {
        VerificationStatus::Pass
    } else if pass_rate >= 0.8 {
        VerificationStatus::Warn
    } else {
        VerificationStatus::Fail
    };

    let summary = if failed == 0 {
        format!("All {} NIST SP 800-22 tests passed", total)
    } else {
        format!("{passed}/{total} NIST tests passed, {failed} failed: {}",
            failed_names.join(", "))
    };

    Ok((status, summary, evidence))
}
