use crate::exec;
use anyhow::{Context, Result};
use std::time::{Duration, Instant};

use super::{Capability, CapabilityContext, CapabilityResult, Evidence, VerificationStatus};

pub struct BcpTest;

impl Capability for BcpTest {
    fn name(&self) -> &'static str { "bcp_test" }
    fn controls(&self) -> &'static [&'static str] { &["FINRA-BCP-3", "MGA-RGR-5.2", "UKGC-RTS-5B"] }

    fn timeout(&self) -> Duration {
        Duration::from_secs(7200) // BCP tests can take up to 2 hours
    }

    fn verify(&self, ctx: &CapabilityContext) -> Result<CapabilityResult> {
        let start = Instant::now();
        let started_at = chrono::Utc::now().to_rfc3339();

        let (status, summary, evidence) = run_bcp_test(ctx)?;

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

fn run_bcp_test(
    ctx: &CapabilityContext,
) -> Result<(VerificationStatus, String, Vec<Evidence>)> {
    let mut evidence = Vec::new();

    // Parse steps from config.
    let steps = ctx.config.params.get("steps")
        .and_then(|v| v.as_array())
        .cloned()
        .unwrap_or_default();

    if steps.is_empty() {
        return Ok((
            VerificationStatus::Skipped,
            "No BCP test steps configured in params.steps".to_string(),
            vec![Evidence {
                key: "config".to_string(),
                value: serde_json::json!("missing"),
                detail: "params.steps is required: list of {name, command, expected_exit_code?} objects".to_string(),
            }],
        ));
    }

    evidence.push(Evidence {
        key: "step_count".to_string(),
        value: serde_json::json!(steps.len()),
        detail: format!("{} BCP test steps configured", steps.len()),
    });

    let rpo_target_secs = ctx.config.thresholds.get("rpo_target_seconds")
        .and_then(|v| v.as_u64());
    let total_timeout_secs = ctx.config.thresholds.get("total_timeout_seconds")
        .and_then(|v| v.as_u64())
        .unwrap_or(7200);

    let total_start = Instant::now();
    let mut step_results = Vec::new();
    let mut passed = 0u32;
    let mut failed = 0u32;
    let mut abort = false;

    for (i, step_val) in steps.iter().enumerate() {
        let step_name = step_val.get("name")
            .and_then(|v| v.as_str())
            .unwrap_or(&format!("step_{}", i + 1))
            .to_string();
        let command = step_val.get("command")
            .and_then(|v| v.as_str())
            .unwrap_or("");
        let expected_exit = step_val.get("expected_exit_code")
            .and_then(|v| v.as_i64())
            .unwrap_or(0);
        let continue_on_fail = step_val.get("continue_on_failure")
            .and_then(|v| v.as_bool())
            .unwrap_or(false);

        if command.is_empty() {
            step_results.push(serde_json::json!({
                "name": step_name,
                "status": "skipped",
                "reason": "no command specified",
            }));
            continue;
        }

        // Check total timeout.
        if total_start.elapsed().as_secs() > total_timeout_secs {
            step_results.push(serde_json::json!({
                "name": step_name,
                "status": "aborted",
                "reason": format!("total timeout exceeded ({}s)", total_timeout_secs),
            }));
            abort = true;
            break;
        }

        let step_start = Instant::now();
        let result = run_shell_command(command);
        let step_duration = step_start.elapsed();

        match result {
            Ok((exit_code, stdout, stderr)) => {
                let step_passed = exit_code == expected_exit as i32;
                if step_passed {
                    passed += 1;
                } else {
                    failed += 1;
                }

                let stdout_tail: String = stdout.lines().rev().take(5)
                    .collect::<Vec<_>>().into_iter().rev()
                    .collect::<Vec<_>>().join("\n");

                step_results.push(serde_json::json!({
                    "name": step_name,
                    "status": if step_passed { "pass" } else { "fail" },
                    "exit_code": exit_code,
                    "expected_exit_code": expected_exit,
                    "duration_seconds": step_duration.as_secs(),
                    "output": stdout_tail,
                    "stderr": stderr.lines().take(3).collect::<Vec<_>>().join("\n"),
                }));

                if !step_passed && !continue_on_fail {
                    abort = true;
                    break;
                }
            }
            Err(e) => {
                failed += 1;
                step_results.push(serde_json::json!({
                    "name": step_name,
                    "status": "error",
                    "error": e.to_string(),
                    "duration_seconds": step_duration.as_secs(),
                }));

                if !continue_on_fail {
                    abort = true;
                    break;
                }
            }
        }
    }

    let total_duration = total_start.elapsed();

    evidence.push(Evidence {
        key: "step_results".to_string(),
        value: serde_json::json!(step_results),
        detail: format!("{} steps: {} passed, {} failed", steps.len(), passed, failed),
    });

    evidence.push(Evidence {
        key: "total_duration_seconds".to_string(),
        value: serde_json::json!(total_duration.as_secs()),
        detail: format!("Total BCP test duration: {}s ({:.1} minutes)",
            total_duration.as_secs(), total_duration.as_secs() as f64 / 60.0),
    });

    evidence.push(Evidence {
        key: "steps_passed".to_string(),
        value: serde_json::json!(passed),
        detail: format!("{passed} steps passed"),
    });

    evidence.push(Evidence {
        key: "steps_failed".to_string(),
        value: serde_json::json!(failed),
        detail: format!("{failed} steps failed"),
    });

    if abort {
        evidence.push(Evidence {
            key: "aborted".to_string(),
            value: serde_json::json!(true),
            detail: "BCP test was aborted due to step failure or timeout".to_string(),
        });
    }

    // Check RPO target if configured.
    if let Some(rpo_target) = rpo_target_secs {
        let rpo_met = total_duration.as_secs() <= rpo_target;
        evidence.push(Evidence {
            key: "rpo_met".to_string(),
            value: serde_json::json!(rpo_met),
            detail: format!("RPO: {}s {} {}s target",
                total_duration.as_secs(),
                if rpo_met { "<=" } else { ">" },
                rpo_target),
        });
    }

    // Record to trends.
    let metrics = serde_json::json!({
        "total_seconds": total_duration.as_secs(),
        "steps_passed": passed,
        "steps_failed": failed,
    });
    let _ = ctx.trends_db.record_run("bcp_test",
        if failed == 0 && !abort { "pass" } else { "fail" }, &metrics);

    let status = if failed == 0 && !abort {
        VerificationStatus::Pass
    } else {
        VerificationStatus::Fail
    };

    let summary = if failed == 0 && !abort {
        format!("BCP test passed: {}/{} steps in {}s",
            passed, steps.len(), total_duration.as_secs())
    } else {
        format!("BCP test failed: {}/{} steps passed, {} failed{}",
            passed, steps.len(), failed,
            if abort { " (aborted)" } else { "" })
    };

    Ok((status, summary, evidence))
}

/// Run a shell command, return (exit_code, stdout, stderr).
fn run_shell_command(cmd: &str) -> Result<(i32, String, String)> {
    // Budget sits just under the capability's 2h timeout so the process-group
    // kill (and a real error result) land before the scheduler's backstop.
    let output = exec::run_shell(cmd, std::time::Duration::from_secs(7190))
        .with_context(|| format!("executing: {cmd}"))?;

    let exit_code = output.status.code().unwrap_or(-1);
    let stdout = String::from_utf8_lossy(&output.stdout).to_string();
    let stderr = String::from_utf8_lossy(&output.stderr).to_string();

    Ok((exit_code, stdout, stderr))
}
