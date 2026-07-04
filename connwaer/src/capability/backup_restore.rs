use crate::exec;
use anyhow::{bail, Context, Result};
use std::time::{Duration, Instant};

use super::{Capability, CapabilityContext, CapabilityResult, Evidence, VerificationStatus};

pub struct BackupRestore;

impl Capability for BackupRestore {
    fn name(&self) -> &'static str { "backup_restore" }
    fn controls(&self) -> &'static [&'static str] { &["FINRA-BCP-1", "FINRA-BCP-2"] }

    fn timeout(&self) -> Duration {
        Duration::from_secs(3600) // restore tests can take up to 1 hour
    }

    fn verify(&self, ctx: &CapabilityContext) -> Result<CapabilityResult> {
        let start = Instant::now();
        let started_at = chrono::Utc::now().to_rfc3339();

        let (status, summary, evidence) = run_restore_test(ctx)?;

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

fn run_restore_test(
    ctx: &CapabilityContext,
) -> Result<(VerificationStatus, String, Vec<Evidence>)> {
    let mut evidence = Vec::new();
    let mut issues = Vec::new();

    // Required params: restore_command.
    let restore_command = ctx.config.params.get("restore_command")
        .and_then(|v| v.as_str());
    let verify_command = ctx.config.params.get("verify_command")
        .and_then(|v| v.as_str());
    let rto_target_secs = ctx.config.thresholds.get("rto_target_seconds")
        .and_then(|v| v.as_u64())
        .unwrap_or(3600);

    if restore_command.is_none() {
        return Ok((
            VerificationStatus::Skipped,
            "No restore_command configured in params".to_string(),
            vec![Evidence {
                key: "config".to_string(),
                value: serde_json::json!("missing"),
                detail: "params.restore_command is required".to_string(),
            }],
        ));
    }

    let restore_cmd = restore_command.unwrap();

    evidence.push(Evidence {
        key: "restore_command".to_string(),
        value: serde_json::json!(restore_cmd),
        detail: format!("Restore command: {restore_cmd}"),
    });
    evidence.push(Evidence {
        key: "rto_target_seconds".to_string(),
        value: serde_json::json!(rto_target_secs),
        detail: format!("RTO target: {rto_target_secs}s"),
    });

    // --- Execute restore command ---
    let restore_start = Instant::now();
    let restore_result = run_shell_command(restore_cmd);
    let restore_elapsed = restore_start.elapsed();
    let rto_seconds = restore_elapsed.as_secs();

    evidence.push(Evidence {
        key: "rto_seconds".to_string(),
        value: serde_json::json!(rto_seconds),
        detail: format!("Actual RTO: {}s ({:.1} minutes)", rto_seconds, rto_seconds as f64 / 60.0),
    });

    match &restore_result {
        Ok(output) => {
            evidence.push(Evidence {
                key: "restore_exit_code".to_string(),
                value: serde_json::json!(0),
                detail: "Restore command completed successfully".to_string(),
            });
            // Capture last few lines of output as evidence.
            let tail: String = output.lines().rev().take(10).collect::<Vec<_>>().into_iter().rev().collect::<Vec<_>>().join("\n");
            if !tail.is_empty() {
                evidence.push(Evidence {
                    key: "restore_output".to_string(),
                    value: serde_json::json!(tail),
                    detail: "Last 10 lines of restore output".to_string(),
                });
            }
        }
        Err(e) => {
            issues.push(format!("restore command failed: {e}"));
            evidence.push(Evidence {
                key: "restore_error".to_string(),
                value: serde_json::json!(e.to_string()),
                detail: format!("Restore failed: {e}"),
            });
        }
    }

    // Check RTO against target.
    let rto_met = rto_seconds <= rto_target_secs;
    evidence.push(Evidence {
        key: "rto_met".to_string(),
        value: serde_json::json!(rto_met),
        detail: if rto_met {
            format!("RTO met: {}s <= {}s target", rto_seconds, rto_target_secs)
        } else {
            format!("RTO EXCEEDED: {}s > {}s target", rto_seconds, rto_target_secs)
        },
    });
    if !rto_met {
        issues.push(format!("RTO {}s exceeds {}s target", rto_seconds, rto_target_secs));
    }

    // --- Execute verify command (checksum, integrity check) ---
    if let Some(verify_cmd) = verify_command {
        evidence.push(Evidence {
            key: "verify_command".to_string(),
            value: serde_json::json!(verify_cmd),
            detail: format!("Verify command: {verify_cmd}"),
        });

        let verify_start = Instant::now();
        let verify_result = run_shell_command(verify_cmd);
        let verify_elapsed = verify_start.elapsed();

        evidence.push(Evidence {
            key: "verify_duration_seconds".to_string(),
            value: serde_json::json!(verify_elapsed.as_secs()),
            detail: format!("Verification took {}s", verify_elapsed.as_secs()),
        });

        match verify_result {
            Ok(output) => {
                evidence.push(Evidence {
                    key: "checksum_verified".to_string(),
                    value: serde_json::json!(true),
                    detail: "Verification command passed".to_string(),
                });
                let tail: String = output.lines().rev().take(5).collect::<Vec<_>>().into_iter().rev().collect::<Vec<_>>().join("\n");
                if !tail.is_empty() {
                    evidence.push(Evidence {
                        key: "verify_output".to_string(),
                        value: serde_json::json!(tail),
                        detail: "Verification output".to_string(),
                    });
                }
            }
            Err(e) => {
                issues.push(format!("verification failed: {e}"));
                evidence.push(Evidence {
                    key: "checksum_verified".to_string(),
                    value: serde_json::json!(false),
                    detail: format!("Verification failed: {e}"),
                });
            }
        }
    }

    // Record to trends DB for RTO trending.
    let metrics = serde_json::json!({
        "rto_seconds": rto_seconds,
        "rto_target_seconds": rto_target_secs,
        "restore_success": restore_result.is_ok(),
    });
    let _ = ctx.trends_db.record_run("backup_restore",
        if issues.is_empty() { "pass" } else { "fail" }, &metrics);

    // Compute RTO trend if enough data.
    let trend = ctx.trends_db.compute_trend("backup_restore", "rto_seconds", None);
    if let Ok(t) = trend {
        if t.data_points >= 3 {
            evidence.push(Evidence {
                key: "rto_trend_slope".to_string(),
                value: serde_json::json!(format!("{:.2}", t.slope_per_day)),
                detail: format!("RTO trend: {:.2}s/run over {} data points ({}=stable, +ve=degrading)",
                    t.slope_per_day, t.data_points, if t.slope_per_day.abs() < 1.0 { "stable" } else { "changing" }),
            });
        }
    }

    let status = if issues.is_empty() {
        VerificationStatus::Pass
    } else if restore_result.is_err() {
        VerificationStatus::Fail
    } else {
        VerificationStatus::Warn
    };

    let summary = if issues.is_empty() {
        format!("Restore completed in {}s (target: {}s), verified", rto_seconds, rto_target_secs)
    } else {
        issues.join("; ")
    };

    Ok((status, summary, evidence))
}

/// Run a shell command and return stdout on success, or error message on failure.
fn run_shell_command(cmd: &str) -> Result<String> {
    // Budget sits just under the capability's 1h timeout so the process-group
    // kill (and a real error result) land before the scheduler's backstop.
    let output = exec::run_shell(cmd, std::time::Duration::from_secs(3590))
        .with_context(|| format!("executing: {cmd}"))?;

    if !output.status.success() {
        let stderr = String::from_utf8_lossy(&output.stderr);
        let code = output.status.code().unwrap_or(-1);
        bail!("exit code {code}: {}", stderr.trim());
    }

    Ok(String::from_utf8_lossy(&output.stdout).to_string())
}
