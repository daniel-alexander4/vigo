pub mod command;
#[cfg(unix)]
pub mod syslog;
pub mod webhook;

use std::collections::HashMap;
use std::time::Instant;

use crate::capability::{CapabilityResult, VerificationStatus};
use crate::config::AlertingConfig;

/// Dispatches alerts based on capability results.
pub struct AlertDispatcher {
    config: AlertingConfig,
    /// Tracks last alert time per capability+status for dedup.
    last_alert: HashMap<String, Instant>,
    /// Tracks previous status per capability for recovery alerts.
    previous_status: HashMap<String, VerificationStatus>,
}

impl AlertDispatcher {
    pub fn new(config: AlertingConfig) -> Self {
        Self {
            config,
            last_alert: HashMap::new(),
            previous_status: HashMap::new(),
        }
    }

    /// Evaluate a capability result and fire alerts if warranted.
    pub fn evaluate(&mut self, result: &CapabilityResult) {
        let is_failure = matches!(
            result.status,
            VerificationStatus::Fail | VerificationStatus::Error
        );
        let is_warning = matches!(result.status, VerificationStatus::Warn);

        // Check for recovery: was failing, now passing.
        let is_recovery = if self.config.recovery_alerts {
            match self.previous_status.get(&result.capability) {
                Some(prev) => {
                    matches!(prev, VerificationStatus::Fail | VerificationStatus::Error)
                        && matches!(result.status, VerificationStatus::Pass)
                }
                None => false,
            }
        } else {
            false
        };

        self.previous_status.insert(result.capability.clone(), result.status.clone());

        let should_alert = is_failure || is_warning || is_recovery;
        if !should_alert {
            return;
        }

        // Dedup check.
        let dedup_key = format!("{}:{}", result.capability, result.status);
        if let Some(last) = self.last_alert.get(&dedup_key) {
            if last.elapsed().as_secs() < self.config.dedup_window_seconds {
                tracing::debug!("alert suppressed (dedup): {dedup_key}");
                return;
            }
        }
        self.last_alert.insert(dedup_key, Instant::now());

        let severity = if is_recovery {
            "recovery"
        } else if is_failure {
            "fail"
        } else {
            "warn"
        };

        // Fire webhooks.
        for target in &self.config.webhooks {
            if severity_meets_min(severity, &target.severity_min) {
                if let Err(e) = webhook::send(target, result, severity) {
                    tracing::error!("webhook alert to {} failed: {e:#}", target.url);
                }
            }
        }

        // Fire commands.
        for target in &self.config.commands {
            if severity_meets_min(severity, &target.severity_min) {
                if let Err(e) = command::run(target, result) {
                    tracing::error!("command alert '{}' failed: {e:#}", target.command);
                }
            }
        }

        // Fire syslog.
        #[cfg(unix)]
        if let Some(target) = &self.config.syslog {
            if severity_meets_min(severity, &target.severity_min) {
                if let Err(e) = syslog::send(target, result, severity) {
                    tracing::error!("syslog alert failed: {e:#}");
                }
            }
        }
    }
}

/// Check if the given severity meets the minimum threshold.
fn severity_meets_min(severity: &str, min: &str) -> bool {
    let level = |s: &str| match s {
        "recovery" => 0,
        "warn" => 1,
        "fail" | "error" => 2,
        _ => 0,
    };
    level(severity) >= level(min)
}
