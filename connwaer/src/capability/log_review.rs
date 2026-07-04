use crate::exec;
use anyhow::Result;
use std::time::Instant;

use super::{Capability, CapabilityContext, CapabilityResult, Evidence, VerificationStatus};

pub struct LogReview;

impl Capability for LogReview {
    fn name(&self) -> &'static str { "log_review" }
    fn controls(&self) -> &'static [&'static str] { &["PCI-DSS-10.4.1"] }

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

/// Default rules for log analysis when none configured.
struct ReviewRule {
    name: &'static str,
    severity: &'static str,
    #[cfg(target_os = "linux")]
    ausearch_args: &'static [&'static str],
    threshold: u64,
}

#[cfg(target_os = "linux")]
const DEFAULT_RULES: &[ReviewRule] = &[
    ReviewRule {
        name: "failed_logins",
        severity: "warn",
        ausearch_args: &["-m", "USER_LOGIN", "--success", "no", "-ts", "today"],
        threshold: 10,
    },
    ReviewRule {
        name: "privilege_escalation",
        severity: "fail",
        ausearch_args: &["-m", "USER_AUTH,USER_ROLE_CHANGE", "-ts", "today"],
        threshold: 50,
    },
    ReviewRule {
        name: "file_access_violations",
        severity: "warn",
        ausearch_args: &["-m", "AVC", "-ts", "today"],
        threshold: 20,
    },
    ReviewRule {
        name: "account_changes",
        severity: "warn",
        ausearch_args: &["-m", "ADD_USER,DEL_USER,ADD_GROUP,DEL_GROUP,USER_MGMT,GRP_MGMT", "-ts", "today"],
        threshold: 5,
    },
    ReviewRule {
        name: "config_changes",
        severity: "warn",
        ausearch_args: &["-m", "MAC_CONFIG_CHANGE,DAEMON_CONFIG", "-ts", "today"],
        threshold: 10,
    },
];

#[cfg(target_os = "linux")]
fn verify_linux(
    config: &super::CapabilityConfig,
) -> Result<(VerificationStatus, String, Vec<Evidence>)> {
    let mut evidence = Vec::new();
    let mut issues = Vec::new();

    // Check if auditd is running.
    let auditd_active = exec::stdout_of("systemctl", &["is-active", "auditd"])
        .map(|s| s == "active")
        .unwrap_or(false);

    evidence.push(Evidence {
        key: "auditd_active".to_string(),
        value: serde_json::json!(auditd_active),
        detail: format!("auditd service: {}", if auditd_active { "active" } else { "not active" }),
    });

    if !auditd_active {
        issues.push("auditd is not running".to_string());
        let status = VerificationStatus::Fail;
        let summary = issues.join("; ");
        return Ok((status, summary, evidence));
    }

    // Count audit rules.
    let rule_count = exec::stdout_of("auditctl", &["-l"])
        .map(|o| o.lines().filter(|l| !l.trim().is_empty() && !l.starts_with("No rules")).count())
        .unwrap_or(0);

    evidence.push(Evidence {
        key: "audit_rule_count".to_string(),
        value: serde_json::json!(rule_count),
        detail: format!("{rule_count} audit rules loaded"),
    });

    if rule_count == 0 {
        issues.push("no audit rules loaded".to_string());
    }

    // Get custom threshold from config or use defaults.
    let anomaly_threshold = config.thresholds.get("anomaly_threshold")
        .and_then(|v| v.as_u64())
        .unwrap_or(0);

    // Run each default rule through ausearch and count matches.
    let mut total_findings = 0u64;
    let mut rule_results = Vec::new();

    for rule in DEFAULT_RULES {
        let count = exec::stdout_of("ausearch", rule.ausearch_args)
            .map(|o| o.lines().filter(|l| l.starts_with("type=")).count() as u64)
            .unwrap_or(0);

        let threshold = if anomaly_threshold > 0 { anomaly_threshold } else { rule.threshold };
        let exceeded = count > threshold;

        rule_results.push(serde_json::json!({
            "rule": rule.name,
            "count": count,
            "threshold": threshold,
            "exceeded": exceeded,
            "severity": rule.severity,
        }));

        if exceeded {
            issues.push(format!("{}: {} events (threshold: {})", rule.name, count, threshold));
        }

        total_findings += count;
    }

    evidence.push(Evidence {
        key: "rule_results".to_string(),
        value: serde_json::json!(rule_results),
        detail: format!("{} total audit events analyzed across {} rules", total_findings, DEFAULT_RULES.len()),
    });

    evidence.push(Evidence {
        key: "total_findings".to_string(),
        value: serde_json::json!(total_findings),
        detail: format!("{total_findings} total events found today"),
    });

    // Check log rotation / disk usage for audit logs.
    if let Ok(meta) = std::fs::metadata("/var/log/audit/audit.log") {
        let size_mb = meta.len() as f64 / (1024.0 * 1024.0);
        evidence.push(Evidence {
            key: "audit_log_size_mb".to_string(),
            value: serde_json::json!(format!("{:.1}", size_mb)),
            detail: format!("audit.log size: {:.1} MB", size_mb),
        });
        if size_mb > 500.0 {
            issues.push(format!("audit.log is large ({:.0} MB) — check rotation", size_mb));
        }
    }

    let status = if issues.is_empty() {
        VerificationStatus::Pass
    } else if issues.iter().any(|i| i.contains("not running") || i.contains("no audit rules")) {
        VerificationStatus::Fail
    } else {
        VerificationStatus::Warn
    };

    let summary = if issues.is_empty() {
        format!("auditd active, {} rules, {} events — no anomalies", rule_count, total_findings)
    } else {
        issues.join("; ")
    };

    Ok((status, summary, evidence))
}

#[cfg(target_os = "windows")]
fn verify_windows(
    config: &super::CapabilityConfig,
) -> Result<(VerificationStatus, String, Vec<Evidence>)> {
    let mut evidence = Vec::new();
    let mut issues = Vec::new();

    // Check Windows Event Log service.
    let eventlog_running = exec::stdout_of("sc.exe", &["query", "EventLog"])
        .map(|s| s.contains("RUNNING"))
        .unwrap_or(false);

    evidence.push(Evidence {
        key: "eventlog_active".to_string(),
        value: serde_json::json!(eventlog_running),
        detail: format!("Windows Event Log: {}", if eventlog_running { "running" } else { "not running" }),
    });

    if !eventlog_running {
        issues.push("Windows Event Log service is not running".to_string());
        return Ok((VerificationStatus::Fail, issues.join("; "), evidence));
    }

    // Check for failed logon events (Event ID 4625) in last 24 hours.
    let failed_logons = exec::stdout_of(
        "powershell.exe",
        &["-NoProfile", "-Command",
          "(Get-WinEvent -FilterHashtable @{LogName='Security'; Id=4625; StartTime=(Get-Date).AddDays(-1)} -ErrorAction SilentlyContinue | Measure-Object).Count"],
    ).and_then(|s| s.trim().parse::<u64>().ok()).unwrap_or(0);

    let threshold = config.thresholds.get("failed_logon_threshold")
        .and_then(|v| v.as_u64())
        .unwrap_or(10);

    evidence.push(Evidence {
        key: "failed_logons_24h".to_string(),
        value: serde_json::json!(failed_logons),
        detail: format!("{failed_logons} failed logon attempts in last 24h (threshold: {threshold})"),
    });

    if failed_logons > threshold {
        issues.push(format!("{failed_logons} failed logons exceed threshold of {threshold}"));
    }

    // Check for privilege escalation (Event ID 4672 — special privileges).
    let priv_events = exec::stdout_of(
        "powershell.exe",
        &["-NoProfile", "-Command",
          "(Get-WinEvent -FilterHashtable @{LogName='Security'; Id=4672; StartTime=(Get-Date).AddDays(-1)} -ErrorAction SilentlyContinue | Measure-Object).Count"],
    ).and_then(|s| s.trim().parse::<u64>().ok()).unwrap_or(0);

    evidence.push(Evidence {
        key: "privilege_events_24h".to_string(),
        value: serde_json::json!(priv_events),
        detail: format!("{priv_events} special privilege logon events in last 24h"),
    });

    // Check for account management events (4720=created, 4726=deleted).
    let account_events = exec::stdout_of(
        "powershell.exe",
        &["-NoProfile", "-Command",
          "(Get-WinEvent -FilterHashtable @{LogName='Security'; Id=4720,4722,4723,4724,4725,4726; StartTime=(Get-Date).AddDays(-1)} -ErrorAction SilentlyContinue | Measure-Object).Count"],
    ).and_then(|s| s.trim().parse::<u64>().ok()).unwrap_or(0);

    evidence.push(Evidence {
        key: "account_changes_24h".to_string(),
        value: serde_json::json!(account_events),
        detail: format!("{account_events} account management events in last 24h"),
    });

    let acct_threshold = config.thresholds.get("account_change_threshold")
        .and_then(|v| v.as_u64())
        .unwrap_or(5);
    if account_events > acct_threshold {
        issues.push(format!("{account_events} account changes exceed threshold of {acct_threshold}"));
    }

    let status = if issues.is_empty() {
        VerificationStatus::Pass
    } else {
        VerificationStatus::Warn
    };

    let summary = if issues.is_empty() {
        format!("Event Log active, no anomalies — {} failed logons, {} account changes",
            failed_logons, account_events)
    } else {
        issues.join("; ")
    };

    Ok((status, summary, evidence))
}
