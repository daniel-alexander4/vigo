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
            if should_deliver(severity, &target.severity_min) {
                if let Err(e) = webhook::send(target, result, severity) {
                    tracing::error!("webhook alert failed: {e:#}");
                }
            }
        }

        // Fire commands.
        for target in &self.config.commands {
            if should_deliver(severity, &target.severity_min) {
                if let Err(e) = command::run(target, result) {
                    tracing::error!("command alert '{}' failed: {e:#}", target.command);
                }
            }
        }

        // Fire syslog.
        #[cfg(unix)]
        if let Some(target) = &self.config.syslog {
            if should_deliver(severity, &target.severity_min) {
                if let Err(e) = syslog::send(target, result, severity) {
                    tracing::error!("syslog alert failed: {e:#}");
                }
            }
        }
    }
}

/// Decide whether an alert of `severity` reaches a target with the given
/// `severity_min`. Recovery is an event class, not a severity: once the
/// global `recovery_alerts` gate admitted it, per-target severity floors
/// don't apply — a floor of "fail" is about failure noise, not about muting
/// the all-clear.
fn should_deliver(severity: &str, min: &str) -> bool {
    severity == "recovery" || severity_meets_min(severity, min)
}

/// Check if the given severity meets the minimum threshold.
fn severity_meets_min(severity: &str, min: &str) -> bool {
    let level = |s: &str| match s {
        "warn" => 1,
        "fail" | "error" => 2,
        _ => 0,
    };
    level(severity) >= level(min)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn severity_floor() {
        assert!(severity_meets_min("fail", "fail"));
        assert!(severity_meets_min("error", "fail"));
        assert!(severity_meets_min("fail", "warn"));
        assert!(severity_meets_min("warn", "warn"));
        assert!(!severity_meets_min("warn", "fail"));
    }

    #[test]
    fn recovery_bypasses_the_floor() {
        // The bug this pins: at the default severity_min ("fail"), recovery
        // alerts were silently dropped — recovery_alerts: true reached no
        // default target.
        assert!(should_deliver("recovery", "fail"));
        assert!(should_deliver("recovery", "warn"));
        assert!(!should_deliver("warn", "fail"));
        assert!(should_deliver("fail", "fail"));
    }
}
