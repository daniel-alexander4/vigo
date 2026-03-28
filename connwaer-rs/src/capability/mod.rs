pub mod app_allowlisting;
pub mod backup_restore;
pub mod bcp_test;
pub mod capacity_monitor;
pub mod encryption_at_rest;
pub mod hsm_lifecycle;
pub mod integrity_monitoring;
pub mod kill_switch;
pub mod log_review;
pub mod network_segmentation;
pub mod password_policy;
pub mod privilege_escalation;
pub mod rng_validation;
pub mod sandboxing;
pub mod session_timeout;
pub mod snmp_collector;
pub mod time_sync;
pub mod worm_verify;

use anyhow::Result;
use serde::{Deserialize, Serialize};
use std::collections::HashMap;
use std::sync::Arc;
use std::time::Duration;

/// Status of a capability verification run.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum VerificationStatus {
    Pass,
    Fail,
    Warn,
    Error,
    Skipped,
}

impl std::fmt::Display for VerificationStatus {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::Pass => write!(f, "pass"),
            Self::Fail => write!(f, "fail"),
            Self::Warn => write!(f, "warn"),
            Self::Error => write!(f, "error"),
            Self::Skipped => write!(f, "skipped"),
        }
    }
}

/// A single piece of structured evidence produced by a capability.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Evidence {
    pub key: String,
    pub value: serde_json::Value,
    pub detail: String,
}

/// The result of a single capability verification run. Written as JSON to the results directory.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CapabilityResult {
    pub capability: String,
    pub status: VerificationStatus,
    pub summary: String,
    pub evidence: Vec<Evidence>,
    pub controls: Vec<String>,
    pub started_at: String,
    pub completed_at: String,
    pub duration_ms: u64,
    pub platform: String,
    pub hostname: String,
    pub version: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
}

/// Context passed to every capability during verification.
pub struct CapabilityContext {
    pub config: CapabilityConfig,
    pub results_dir: std::path::PathBuf,
    pub trends_db: Arc<crate::trends::TrendStore>,
    pub hostname: String,
    pub platform: String,
    pub version: String,
}

/// Per-capability configuration from the YAML config file.
#[derive(Debug, Clone, Deserialize, Default)]
pub struct CapabilityConfig {
    #[serde(default = "default_true")]
    pub enabled: bool,
    #[serde(default)]
    pub params: HashMap<String, serde_json::Value>,
    #[serde(default)]
    pub thresholds: HashMap<String, serde_json::Value>,
}

fn default_true() -> bool {
    true
}

/// The interface every capability implements.
pub trait Capability: Send + Sync {
    /// Machine name — matches JSON filename (e.g., "app_allowlisting", "rng_validation").
    fn name(&self) -> &'static str;

    /// Human-readable display name.
    fn display_name(&self) -> &'static str;

    /// Compliance control IDs this capability addresses.
    fn controls(&self) -> &'static [&'static str];

    /// Execute the verification and return a result.
    fn verify(&self, ctx: &CapabilityContext) -> Result<CapabilityResult>;

    /// Maximum execution time before the capability is killed.
    fn timeout(&self) -> Duration {
        Duration::from_secs(300)
    }

    /// Whether this capability is supported on the current platform.
    fn supported(&self) -> bool {
        true
    }
}

/// Enum-based dispatch for all 18 capabilities. Follows the agent-rs CollectorKind pattern.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum CapabilityKind {
    AppAllowlisting,
    Sandboxing,
    LogReview,
    CapacityMonitor,
    BackupRestore,
    BcpTest,
    WormVerify,
    KillSwitch,
    RngValidation,
    HsmLifecycle,
    SnmpCollector,
    EncryptionAtRest,
    PasswordPolicy,
    NetworkSegmentation,
    PrivilegeEscalation,
    TimeSync,
    IntegrityMonitoring,
    SessionTimeout,
}

impl CapabilityKind {
    pub fn name(&self) -> &'static str {
        match self {
            Self::AppAllowlisting => "app_allowlisting",
            Self::Sandboxing => "sandboxing",
            Self::LogReview => "log_review",
            Self::CapacityMonitor => "capacity_monitor",
            Self::BackupRestore => "backup_restore",
            Self::BcpTest => "bcp_test",
            Self::WormVerify => "worm_verify",
            Self::KillSwitch => "kill_switch",
            Self::RngValidation => "rng_validation",
            Self::HsmLifecycle => "hsm_lifecycle",
            Self::SnmpCollector => "snmp_collector",
            Self::EncryptionAtRest => "encryption_at_rest",
            Self::PasswordPolicy => "password_policy",
            Self::NetworkSegmentation => "network_segmentation",
            Self::PrivilegeEscalation => "privilege_escalation",
            Self::TimeSync => "time_sync",
            Self::IntegrityMonitoring => "integrity_monitoring",
            Self::SessionTimeout => "session_timeout",
        }
    }

    pub fn from_name(name: &str) -> Option<Self> {
        match name {
            "app_allowlisting" => Some(Self::AppAllowlisting),
            "sandboxing" => Some(Self::Sandboxing),
            "log_review" => Some(Self::LogReview),
            "capacity_monitor" => Some(Self::CapacityMonitor),
            "backup_restore" => Some(Self::BackupRestore),
            "bcp_test" => Some(Self::BcpTest),
            "worm_verify" => Some(Self::WormVerify),
            "kill_switch" => Some(Self::KillSwitch),
            "rng_validation" => Some(Self::RngValidation),
            "hsm_lifecycle" => Some(Self::HsmLifecycle),
            "snmp_collector" => Some(Self::SnmpCollector),
            "encryption_at_rest" => Some(Self::EncryptionAtRest),
            "password_policy" => Some(Self::PasswordPolicy),
            "network_segmentation" => Some(Self::NetworkSegmentation),
            "privilege_escalation" => Some(Self::PrivilegeEscalation),
            "time_sync" => Some(Self::TimeSync),
            "integrity_monitoring" => Some(Self::IntegrityMonitoring),
            "session_timeout" => Some(Self::SessionTimeout),
            _ => None,
        }
    }

    pub fn all() -> &'static [CapabilityKind] {
        &[
            Self::AppAllowlisting,
            Self::Sandboxing,
            Self::LogReview,
            Self::CapacityMonitor,
            Self::BackupRestore,
            Self::BcpTest,
            Self::WormVerify,
            Self::KillSwitch,
            Self::RngValidation,
            Self::HsmLifecycle,
            Self::SnmpCollector,
            Self::EncryptionAtRest,
            Self::PasswordPolicy,
            Self::NetworkSegmentation,
            Self::PrivilegeEscalation,
            Self::TimeSync,
            Self::IntegrityMonitoring,
            Self::SessionTimeout,
        ]
    }

    /// Instantiate the capability implementation.
    pub fn instantiate(&self) -> Box<dyn Capability> {
        match self {
            Self::AppAllowlisting => Box::new(app_allowlisting::AppAllowlisting),
            Self::Sandboxing => Box::new(sandboxing::Sandboxing),
            Self::LogReview => Box::new(log_review::LogReview),
            Self::CapacityMonitor => Box::new(capacity_monitor::CapacityMonitor),
            Self::BackupRestore => Box::new(backup_restore::BackupRestore),
            Self::BcpTest => Box::new(bcp_test::BcpTest),
            Self::WormVerify => Box::new(worm_verify::WormVerify),
            Self::KillSwitch => Box::new(kill_switch::KillSwitch),
            Self::RngValidation => Box::new(rng_validation::RngValidation),
            Self::HsmLifecycle => Box::new(hsm_lifecycle::HsmLifecycle),
            Self::SnmpCollector => Box::new(snmp_collector::SnmpCollector),
            Self::EncryptionAtRest => Box::new(encryption_at_rest::EncryptionAtRest),
            Self::PasswordPolicy => Box::new(password_policy::PasswordPolicy),
            Self::NetworkSegmentation => Box::new(network_segmentation::NetworkSegmentation),
            Self::PrivilegeEscalation => Box::new(privilege_escalation::PrivilegeEscalation),
            Self::TimeSync => Box::new(time_sync::TimeSync),
            Self::IntegrityMonitoring => Box::new(integrity_monitoring::IntegrityMonitoring),
            Self::SessionTimeout => Box::new(session_timeout::SessionTimeout),
        }
    }
}

impl std::fmt::Display for CapabilityKind {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "{}", self.name())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::HashSet;

    #[test]
    fn all_names_unique() {
        let mut names = HashSet::new();
        for kind in CapabilityKind::all() {
            assert!(names.insert(kind.name()), "duplicate name: {}", kind.name());
        }
    }

    #[test]
    fn from_name_roundtrip() {
        for kind in CapabilityKind::all() {
            let parsed = CapabilityKind::from_name(kind.name());
            assert_eq!(parsed, Some(*kind), "roundtrip failed for {}", kind.name());
        }
    }

    #[test]
    fn from_name_unknown_returns_none() {
        assert_eq!(CapabilityKind::from_name("nonexistent"), None);
    }

    #[test]
    fn all_count() {
        assert_eq!(CapabilityKind::all().len(), 18);
    }
}
