// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Pluggable trait collectors that gather system information from managed thralls.
//! Traits are used in Go templates as .Traits.os.family, .Traits.hardware.cpu_count, etc.

pub mod cached;
mod blockdevices;
mod cloud;
mod dmi;
mod docker;
mod filesystem;
mod gpu;
mod hardware;
mod identity;
mod kernel;
mod loadavg;
mod locale;
mod memory;
mod mountpoints;
mod network;
mod os;
mod packages;
mod processors;
mod selinux;
mod swap;
mod systemd;
mod time_collector;
mod timezone;
mod uptime;
mod users;
mod virt;

use anyhow::Result;
use serde_json::Value;
use std::time::Duration;
use tracing::warn;

/// Maximum time a single trait collector may run.
pub const COLLECTOR_TIMEOUT: Duration = Duration::from_secs(5);

/// Enum dispatch for the 25 built-in trait collectors.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum CollectorKind {
    Os,
    Hardware,
    Network,
    Packages,
    Time,
    Identity,
    Virtual,
    Uptime,
    LoadAvg,
    Mountpoints,
    Processors,
    Dmi,
    Cloud,
    Filesystem,
    Kernel,
    Users,
    Memory,
    BlockDevices,
    Docker,
    Systemd,
    SELinux,
    Timezone,
    Locale,
    Swap,
    Gpu,
}

impl CollectorKind {
    /// Top-level key under which this collector's traits are stored.
    pub fn name(&self) -> &'static str {
        match self {
            Self::Os => "os",
            Self::Hardware => "hardware",
            Self::Network => "network",
            Self::Packages => "packages",
            Self::Time => "time",
            Self::Identity => "identity",
            Self::Virtual => "virtual",
            Self::Uptime => "uptime",
            Self::LoadAvg => "loadavg",
            Self::Mountpoints => "mountpoints",
            Self::Processors => "processors",
            Self::Dmi => "dmi",
            Self::Cloud => "cloud",
            Self::Filesystem => "filesystem",
            Self::Kernel => "kernel",
            Self::Users => "users",
            Self::Memory => "memory",
            Self::BlockDevices => "blockdevices",
            Self::Docker => "docker",
            Self::Systemd => "systemd",
            Self::SELinux => "selinux",
            Self::Timezone => "timezone",
            Self::Locale => "locale",
            Self::Swap => "swap",
            Self::Gpu => "gpu",
        }
    }

    /// Collect traits for this collector.
    pub fn collect(&self) -> Result<Value> {
        match self {
            Self::Os => os::collect(),
            Self::Hardware => hardware::collect(),
            Self::Network => network::collect(),
            Self::Packages => packages::collect(),
            Self::Time => time_collector::collect(),
            Self::Identity => identity::collect(),
            Self::Virtual => virt::collect(),
            Self::Uptime => uptime::collect(),
            Self::LoadAvg => loadavg::collect(),
            Self::Mountpoints => mountpoints::collect(),
            Self::Processors => processors::collect(),
            Self::Dmi => dmi::collect(),
            Self::Cloud => cloud::collect(),
            Self::Filesystem => filesystem::collect(),
            Self::Kernel => kernel::collect(),
            Self::Users => users::collect(),
            Self::Memory => memory::collect(),
            Self::BlockDevices => blockdevices::collect(),
            Self::Docker => docker::collect(),
            Self::Systemd => systemd::collect(),
            Self::SELinux => selinux::collect(),
            Self::Timezone => timezone::collect(),
            Self::Locale => locale::collect(),
            Self::Swap => swap::collect(),
            Self::Gpu => gpu::collect(),
        }
    }

    /// Look up a collector by name.
    pub fn from_name(name: &str) -> Option<Self> {
        match name {
            "os" => Some(Self::Os),
            "hardware" => Some(Self::Hardware),
            "network" => Some(Self::Network),
            "packages" => Some(Self::Packages),
            "time" => Some(Self::Time),
            "identity" => Some(Self::Identity),
            "virtual" => Some(Self::Virtual),
            "uptime" => Some(Self::Uptime),
            "loadavg" => Some(Self::LoadAvg),
            "mountpoints" => Some(Self::Mountpoints),
            "processors" => Some(Self::Processors),
            "dmi" => Some(Self::Dmi),
            "cloud" => Some(Self::Cloud),
            "filesystem" => Some(Self::Filesystem),
            "kernel" => Some(Self::Kernel),
            "users" => Some(Self::Users),
            "memory" => Some(Self::Memory),
            "blockdevices" => Some(Self::BlockDevices),
            "docker" => Some(Self::Docker),
            "systemd" => Some(Self::Systemd),
            "selinux" => Some(Self::SELinux),
            "timezone" => Some(Self::Timezone),
            "locale" => Some(Self::Locale),
            "swap" => Some(Self::Swap),
            "gpu" => Some(Self::Gpu),
            _ => None,
        }
    }
}

/// The standard set of all 25 trait collectors, in the same order as Go.
pub fn default_collectors() -> Vec<CollectorKind> {
    vec![
        CollectorKind::Os,
        CollectorKind::Hardware,
        CollectorKind::Network,
        CollectorKind::Packages,
        CollectorKind::Time,
        CollectorKind::Identity,
        CollectorKind::Virtual,
        CollectorKind::Uptime,
        CollectorKind::LoadAvg,
        CollectorKind::Mountpoints,
        CollectorKind::Processors,
        CollectorKind::Dmi,
        CollectorKind::Cloud,
        CollectorKind::Filesystem,
        CollectorKind::Kernel,
        CollectorKind::Users,
        CollectorKind::Memory,
        CollectorKind::BlockDevices,
        CollectorKind::Docker,
        CollectorKind::Systemd,
        CollectorKind::SELinux,
        CollectorKind::Timezone,
        CollectorKind::Locale,
        CollectorKind::Swap,
        CollectorKind::Gpu,
    ]
}

/// Collectors whose data changes frequently and must run every cycle.
pub fn volatile_collectors() -> Vec<CollectorKind> {
    vec![
        CollectorKind::Time,
        CollectorKind::Uptime,
        CollectorKind::LoadAvg,
        CollectorKind::Mountpoints,
        CollectorKind::Filesystem,
        CollectorKind::Users,
        CollectorKind::Memory,
        CollectorKind::Swap,
    ]
}

/// Collectors whose data rarely changes and can be cached across cycles.
pub fn stable_collectors() -> Vec<CollectorKind> {
    vec![
        CollectorKind::Os,
        CollectorKind::Hardware,
        CollectorKind::Network,
        CollectorKind::Packages,
        CollectorKind::Identity,
        CollectorKind::Virtual,
        CollectorKind::Processors,
        CollectorKind::Dmi,
        CollectorKind::Cloud,
        CollectorKind::Kernel,
        CollectorKind::BlockDevices,
        CollectorKind::Docker,
        CollectorKind::Systemd,
        CollectorKind::SELinux,
        CollectorKind::Timezone,
        CollectorKind::Locale,
        CollectorKind::Gpu,
    ]
}

/// Collect traits from all provided collectors in parallel.
/// Failed collectors are logged and skipped (non-fatal).
pub async fn collect_all(collectors: &[CollectorKind]) -> Value {
    let mut handles = Vec::with_capacity(collectors.len());

    for &kind in collectors {
        handles.push(tokio::spawn(async move {
            let result = tokio::time::timeout(
                COLLECTOR_TIMEOUT,
                tokio::task::spawn_blocking(move || kind.collect()),
            )
            .await;

            match result {
                Ok(Ok(Ok(val))) => Some((kind.name(), val)),
                Ok(Ok(Err(e))) => {
                    warn!(collector = kind.name(), error = %e, "trait collector failed");
                    None
                }
                Ok(Err(e)) => {
                    warn!(collector = kind.name(), error = %e, "trait collector panicked");
                    None
                }
                Err(_) => {
                    warn!(collector = kind.name(), "trait collector timed out");
                    None
                }
            }
        }));
    }

    let mut map = serde_json::Map::new();
    for handle in handles {
        if let Ok(Some((name, val))) = handle.await {
            map.insert(name.to_string(), val);
        }
    }
    Value::Object(map)
}

/// Traverse a dot-separated path through a JSON value.
pub fn traverse_dot_path(data: &Value, path: &str) -> Option<Value> {
    if path.is_empty() {
        return None;
    }
    let pointer = format!("/{}", path.replace('.', "/"));
    data.pointer(&pointer).cloned()
}

/// Format a JSON value for CLI output. Scalars print as-is, objects/arrays as pretty JSON.
pub fn format_value(v: &Value) -> String {
    match v {
        Value::String(s) => s.clone(),
        Value::Number(n) => n.to_string(),
        Value::Bool(b) => b.to_string(),
        Value::Null => "null".to_string(),
        _ => serde_json::to_string_pretty(v).unwrap_or_else(|_| v.to_string()),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_default_collectors_count() {
        assert_eq!(default_collectors().len(), 25);
    }

    #[test]
    fn test_volatile_plus_stable_covers_all() {
        let mut all: Vec<CollectorKind> = volatile_collectors();
        all.extend(stable_collectors());
        let defaults = default_collectors();
        for d in &defaults {
            assert!(all.contains(d), "collector {:?} missing from volatile+stable", d);
        }
        assert_eq!(all.len(), defaults.len());
    }

    #[test]
    fn test_collector_names_unique() {
        let collectors = default_collectors();
        let mut names: Vec<&str> = collectors.iter().map(|c| c.name()).collect();
        let len = names.len();
        names.sort();
        names.dedup();
        assert_eq!(names.len(), len, "duplicate collector names");
    }

    #[test]
    fn test_from_name_roundtrip() {
        for c in default_collectors() {
            assert_eq!(CollectorKind::from_name(c.name()), Some(c));
        }
        assert_eq!(CollectorKind::from_name("nonexistent"), None);
    }

    #[test]
    fn test_traverse_dot_path() {
        let data = serde_json::json!({
            "os": {
                "family": "debian",
                "distro": "ubuntu",
            },
            "network": {
                "hostname": "web01",
            }
        });

        assert_eq!(
            traverse_dot_path(&data, "os.family"),
            Some(serde_json::json!("debian"))
        );
        assert_eq!(
            traverse_dot_path(&data, "os"),
            Some(serde_json::json!({"family": "debian", "distro": "ubuntu"}))
        );
        assert_eq!(traverse_dot_path(&data, "os.missing"), None);
        assert_eq!(traverse_dot_path(&data, ""), None);
        assert_eq!(traverse_dot_path(&data, "nonexistent"), None);
    }

    #[test]
    fn test_format_value() {
        assert_eq!(format_value(&serde_json::json!("hello")), "hello");
        assert_eq!(format_value(&serde_json::json!(42)), "42");
        assert_eq!(format_value(&serde_json::json!(true)), "true");
        assert_eq!(format_value(&serde_json::json!(null)), "null");
    }

    #[tokio::test]
    async fn test_collect_all_aggregates() {
        // Just verify collect_all returns a Value::Object (individual collectors
        // may fail on CI but should not panic).
        let collectors = vec![CollectorKind::Os, CollectorKind::Time];
        let result = collect_all(&collectors).await;
        assert!(result.is_object());
    }
}
