use crate::exec;
use anyhow::Result;
use std::net::TcpStream;
use std::time::{Duration, Instant};

use super::{Capability, CapabilityContext, CapabilityResult, Evidence, VerificationStatus};

pub struct KillSwitch;

impl Capability for KillSwitch {
    fn name(&self) -> &'static str { "kill_switch" }
    fn controls(&self) -> &'static [&'static str] { &["RTS7-Art14-1"] }

    fn verify(&self, ctx: &CapabilityContext) -> Result<CapabilityResult> {
        let start = Instant::now();
        let started_at = chrono::Utc::now().to_rfc3339();

        let (status, summary, evidence) = validate_kill_switch(&ctx.config)?;

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

fn validate_kill_switch(
    config: &super::CapabilityConfig,
) -> Result<(VerificationStatus, String, Vec<Evidence>)> {
    let mut evidence = Vec::new();
    let mut issues = Vec::new();

    let binary_path = config.params.get("binary_path")
        .and_then(|v| v.as_str());

    if binary_path.is_none() {
        return Ok((
            VerificationStatus::Skipped,
            "No binary_path configured in params".to_string(),
            vec![Evidence {
                key: "config".to_string(),
                value: serde_json::json!("missing"),
                detail: "params.binary_path is required".to_string(),
            }],
        ));
    }

    let binary = binary_path.unwrap();
    let config_path = config.params.get("config_path").and_then(|v| v.as_str());
    let dry_run_flag = config.params.get("dry_run_flag")
        .and_then(|v| v.as_str())
        .unwrap_or("--dry-run");

    // --- Check 1: Binary exists and is executable ---
    let binary_exists = std::path::Path::new(binary).exists();
    evidence.push(Evidence {
        key: "binary_exists".to_string(),
        value: serde_json::json!(binary_exists),
        detail: format!("Kill switch binary {binary}: {}", if binary_exists { "found" } else { "NOT FOUND" }),
    });

    if !binary_exists {
        issues.push(format!("kill switch binary not found: {binary}"));
        return Ok((VerificationStatus::Fail, issues.join("; "), evidence));
    }

    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        if let Ok(meta) = std::fs::metadata(binary) {
            let executable = meta.permissions().mode() & 0o111 != 0;
            evidence.push(Evidence {
                key: "binary_executable".to_string(),
                value: serde_json::json!(executable),
                detail: format!("Binary executable: {executable}"),
            });
            if !executable {
                issues.push("kill switch binary is not executable".to_string());
            }
        }
    }

    // --- Check 2: Config file exists (if specified) ---
    if let Some(cfg_path) = config_path {
        let cfg_exists = std::path::Path::new(cfg_path).exists();
        evidence.push(Evidence {
            key: "config_exists".to_string(),
            value: serde_json::json!(cfg_exists),
            detail: format!("Config file {cfg_path}: {}", if cfg_exists { "found" } else { "NOT FOUND" }),
        });
        if !cfg_exists {
            issues.push(format!("kill switch config not found: {cfg_path}"));
        }
    }

    // --- Check 3: Dry-run execution ---
    let mut args: Vec<&str> = vec![dry_run_flag];
    if let Some(cfg_path) = config_path {
        // Common pattern: --config <path>.
        args.extend(["--config", cfg_path]);
    }

    // Bounded just under the capability's 300s budget.
    let dry_run_result = exec::run(binary, &args, std::time::Duration::from_secs(290));
    match dry_run_result {
        Ok(output) => {
            let exit_ok = output.status.success();
            let stdout = String::from_utf8_lossy(&output.stdout).trim().to_string();
            let stderr = String::from_utf8_lossy(&output.stderr).trim().to_string();

            evidence.push(Evidence {
                key: "dry_run_success".to_string(),
                value: serde_json::json!(exit_ok),
                detail: format!("Dry-run exit code: {}", output.status.code().unwrap_or(-1)),
            });

            if !stdout.is_empty() {
                let tail: String = stdout.lines().rev().take(5)
                    .collect::<Vec<_>>().into_iter().rev()
                    .collect::<Vec<_>>().join("\n");
                evidence.push(Evidence {
                    key: "dry_run_output".to_string(),
                    value: serde_json::json!(tail),
                    detail: "Dry-run output".to_string(),
                });
            }

            if !exit_ok {
                let err_tail: String = stderr.lines().take(3).collect::<Vec<_>>().join("\n");
                issues.push(format!("dry-run failed: {err_tail}"));
            }
        }
        Err(e) => {
            issues.push(format!("dry-run execution failed: {e}"));
            evidence.push(Evidence {
                key: "dry_run_error".to_string(),
                value: serde_json::json!(e.to_string()),
                detail: format!("Could not execute dry-run: {e}"),
            });
        }
    }

    // --- Check 4: Target reachability ---
    let targets = config.params.get("targets")
        .and_then(|v| v.as_array())
        .cloned()
        .unwrap_or_default();

    if !targets.is_empty() {
        let mut reachable = 0u32;
        let mut unreachable = 0u32;
        let mut target_results = Vec::new();

        for target_val in &targets {
            let addr = target_val.as_str()
                .or_else(|| target_val.get("address").and_then(|v| v.as_str()))
                .unwrap_or("");
            if addr.is_empty() { continue; }

            let is_reachable = TcpStream::connect_timeout(
                &addr.parse().unwrap_or_else(|_| std::net::SocketAddr::from(([0, 0, 0, 0], 0))),
                Duration::from_secs(5),
            ).is_ok();

            // Fallback: try parsing as host:port and resolving.
            let is_reachable = if !is_reachable {
                // Try with ToSocketAddrs which does DNS resolution.
                use std::net::ToSocketAddrs;
                addr.to_socket_addrs()
                    .ok()
                    .and_then(|mut addrs| addrs.next())
                    .map(|a| TcpStream::connect_timeout(&a, Duration::from_secs(5)).is_ok())
                    .unwrap_or(false)
            } else {
                is_reachable
            };

            target_results.push(serde_json::json!({
                "address": addr,
                "reachable": is_reachable,
            }));

            if is_reachable {
                reachable += 1;
            } else {
                unreachable += 1;
                issues.push(format!("target unreachable: {addr}"));
            }
        }

        evidence.push(Evidence {
            key: "target_reachability".to_string(),
            value: serde_json::json!(target_results),
            detail: format!("{reachable} reachable, {unreachable} unreachable out of {} targets", targets.len()),
        });
    }

    let status = if issues.is_empty() {
        VerificationStatus::Pass
    } else if issues.iter().any(|i| i.contains("not found") || i.contains("dry-run failed")) {
        VerificationStatus::Fail
    } else {
        VerificationStatus::Warn
    };

    let summary = if issues.is_empty() {
        "Kill switch binary present, dry-run passed, targets reachable".to_string()
    } else {
        issues.join("; ")
    };

    Ok((status, summary, evidence))
}
