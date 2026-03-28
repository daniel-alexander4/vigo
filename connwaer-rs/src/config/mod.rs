pub mod watch;

use anyhow::{Context, Result};
use serde::Deserialize;
use std::collections::HashMap;
use std::path::Path;

use crate::platform::paths;

/// Top-level Connwaer configuration, loaded from YAML.
#[derive(Debug, Deserialize)]
pub struct ConnwaerConfig {
    #[serde(default = "default_license_dir")]
    pub license_dir: String,

    #[serde(default = "default_results_dir")]
    pub results_dir: String,

    #[serde(default = "default_trends_db")]
    pub trends_db: String,

    #[serde(default = "default_log_level")]
    pub log_level: String,

    #[serde(default)]
    pub alerting: AlertingConfig,

    #[serde(default)]
    pub capabilities: HashMap<String, CapabilityEntry>,
}

/// Per-capability configuration.
#[derive(Debug, Clone, Deserialize)]
pub struct CapabilityEntry {
    #[serde(default = "default_true")]
    pub enabled: bool,

    /// Cron expression: "0 */6 * * *" or "@daily" or "@every 5m".
    pub schedule: String,

    /// Capabilities that must complete before this one runs.
    #[serde(default)]
    pub depends_on: Vec<String>,

    /// Capability-specific parameters.
    #[serde(default)]
    pub params: HashMap<String, serde_json::Value>,

    /// Pass/fail thresholds.
    #[serde(default)]
    pub thresholds: HashMap<String, serde_json::Value>,
}

/// Alerting configuration.
#[derive(Debug, Clone, Deserialize)]
pub struct AlertingConfig {
    #[serde(default)]
    pub webhooks: Vec<WebhookTarget>,

    #[serde(default)]
    pub commands: Vec<CommandTarget>,

    #[serde(default)]
    pub syslog: Option<SyslogTarget>,

    /// Seconds to suppress duplicate alerts for the same capability + status.
    #[serde(default = "default_dedup_window")]
    pub dedup_window_seconds: u64,

    /// Send an alert when a previously-failing capability passes.
    #[serde(default)]
    pub recovery_alerts: bool,
}

#[derive(Debug, Clone, Deserialize)]
pub struct WebhookTarget {
    pub url: String,
    #[serde(default)]
    pub headers: HashMap<String, String>,
    /// Minimum severity to trigger this webhook: "fail", "warn", or "error".
    #[serde(default = "default_severity_min")]
    pub severity_min: String,
}

#[derive(Debug, Clone, Deserialize)]
pub struct CommandTarget {
    pub command: String,
    #[serde(default)]
    pub args: Vec<String>,
    #[serde(default = "default_severity_min")]
    pub severity_min: String,
}

#[derive(Debug, Clone, Deserialize)]
pub struct SyslogTarget {
    #[serde(default = "default_syslog_facility")]
    pub facility: String,
    #[serde(default = "default_severity_min")]
    pub severity_min: String,
}

impl Default for AlertingConfig {
    fn default() -> Self {
        Self {
            webhooks: vec![],
            commands: vec![],
            syslog: None,
            dedup_window_seconds: default_dedup_window(),
            recovery_alerts: false,
        }
    }
}

fn default_license_dir() -> String { paths::DEFAULT_LICENSE_DIR.to_string() }
fn default_results_dir() -> String { paths::DEFAULT_RESULTS_DIR.to_string() }
fn default_trends_db() -> String { paths::DEFAULT_TRENDS_DB.to_string() }
fn default_log_level() -> String { "info".to_string() }
fn default_true() -> bool { true }
fn default_dedup_window() -> u64 { 3600 }
fn default_severity_min() -> String { "fail".to_string() }
fn default_syslog_facility() -> String { "daemon".to_string() }

impl ConnwaerConfig {
    /// Load configuration from a YAML file.
    pub fn load(path: &Path) -> Result<Self> {
        let content = std::fs::read_to_string(path)
            .with_context(|| format!("reading config: {}", path.display()))?;
        let config: Self = serde_yaml::from_str(&content)
            .with_context(|| format!("parsing config: {}", path.display()))?;
        config.validate()?;
        Ok(config)
    }

    /// Validate the configuration after loading.
    fn validate(&self) -> Result<()> {
        for (name, entry) in &self.capabilities {
            if entry.schedule.is_empty() {
                anyhow::bail!("capability '{}': schedule is required", name);
            }
            // Validate depends_on references exist
            for dep in &entry.depends_on {
                if !self.capabilities.contains_key(dep) {
                    anyhow::bail!(
                        "capability '{}': depends_on '{}' not found in capabilities",
                        name, dep
                    );
                }
            }
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::NamedTempFile;
    use std::io::Write;

    fn write_yaml(content: &str) -> NamedTempFile {
        let mut f = NamedTempFile::new().unwrap();
        f.write_all(content.as_bytes()).unwrap();
        f
    }

    #[test]
    fn load_minimal_config() {
        let f = write_yaml("log_level: debug\n");
        let cfg = ConnwaerConfig::load(f.path()).unwrap();
        assert_eq!(cfg.log_level, "debug");
        assert_eq!(cfg.results_dir, paths::DEFAULT_RESULTS_DIR);
        assert!(cfg.capabilities.is_empty());
    }

    #[test]
    fn load_with_capabilities() {
        let f = write_yaml(r#"
capabilities:
  time_sync:
    enabled: true
    schedule: "*/15 * * * *"
    thresholds:
      max_offset_ms: 100
  integrity_monitoring:
    enabled: true
    schedule: "@daily"
    depends_on:
      - time_sync
"#);
        let cfg = ConnwaerConfig::load(f.path()).unwrap();
        assert_eq!(cfg.capabilities.len(), 2);
        assert!(cfg.capabilities["time_sync"].enabled);
        assert_eq!(cfg.capabilities["integrity_monitoring"].depends_on, vec!["time_sync"]);
    }

    #[test]
    fn validate_rejects_empty_schedule() {
        let f = write_yaml(r#"
capabilities:
  time_sync:
    enabled: true
    schedule: ""
"#);
        let err = ConnwaerConfig::load(f.path());
        assert!(err.is_err());
        assert!(err.unwrap_err().to_string().contains("schedule is required"));
    }

    #[test]
    fn validate_rejects_missing_depends_on() {
        let f = write_yaml(r#"
capabilities:
  bcp_test:
    enabled: true
    schedule: "@daily"
    depends_on:
      - backup_restore
"#);
        let err = ConnwaerConfig::load(f.path());
        assert!(err.is_err());
        assert!(err.unwrap_err().to_string().contains("not found in capabilities"));
    }

    #[test]
    fn load_with_alerting() {
        let f = write_yaml(r#"
alerting:
  recovery_alerts: true
  dedup_window_seconds: 7200
  webhooks:
    - url: "https://hooks.slack.com/test"
      severity_min: fail
"#);
        let cfg = ConnwaerConfig::load(f.path()).unwrap();
        assert!(cfg.alerting.recovery_alerts);
        assert_eq!(cfg.alerting.dedup_window_seconds, 7200);
        assert_eq!(cfg.alerting.webhooks.len(), 1);
    }

    #[test]
    fn defaults_applied() {
        let f = write_yaml("");
        let cfg = ConnwaerConfig::load(f.path()).unwrap();
        assert_eq!(cfg.log_level, "info");
        assert_eq!(cfg.license_dir, paths::DEFAULT_LICENSE_DIR);
        assert_eq!(cfg.alerting.dedup_window_seconds, 3600);
        assert!(!cfg.alerting.recovery_alerts);
    }
}
