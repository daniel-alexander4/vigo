// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Pluggable trait collectors that gather system information from managed envoys.
//! Traits are used in Go templates as .Traits.os.family, .Traits.hardware.cpu_count, etc.

pub mod cached;
pub mod network_device;

// Cross-platform collectors.
mod cloud;
mod connwaer;
mod display;
mod dmi;
mod docker;
pub(crate) mod swarm_cache;
mod swarm_seeds;
mod time_collector;
mod vigocrypt;
mod vigorepo;
mod vigosync;

// Shared sysctl utility for macOS/BSD collectors.
#[cfg(any(target_os = "macos", target_os = "freebsd", target_os = "openbsd", target_os = "netbsd", target_os = "illumos"))]
pub(crate) mod sysctl_util;

// Unix-specific collectors.
#[cfg(unix)]
mod cert_lifecycle;
#[cfg(unix)]
mod ports;
pub(crate) mod security_scan;

// Linux-specific collectors.
#[cfg(target_os = "linux")]
mod blockdevices;
#[cfg(target_os = "linux")]
mod filesystem;
#[cfg(target_os = "linux")]
mod gpu;
#[cfg(target_os = "linux")]
mod hardware;
#[cfg(target_os = "linux")]
mod cve_running_correlation;
#[cfg(target_os = "linux")]
mod kernel_module_attestation;
#[cfg(target_os = "linux")]
mod listeners;
#[cfg(target_os = "linux")]
mod package_source_audit;
#[cfg(target_os = "linux")]
mod secureboot;
#[cfg(target_os = "linux")]
mod tpm;
#[cfg(target_os = "linux")]
mod identity;
#[cfg(target_os = "linux")]
mod kernel;
#[cfg(target_os = "linux")]
mod loadavg;
#[cfg(target_os = "linux")]
mod locale;
#[cfg(target_os = "linux")]
mod memory;
#[cfg(target_os = "linux")]
mod mountpoints;
#[cfg(target_os = "linux")]
mod network;
#[cfg(target_os = "linux")]
mod os;
#[cfg(target_os = "linux")]
mod packages;
#[cfg(target_os = "linux")]
mod processors;
#[cfg(target_os = "linux")]
mod selinux;
#[cfg(target_os = "linux")]
mod swap;
#[cfg(target_os = "linux")]
mod systemd;
#[cfg(target_os = "linux")]
mod timezone;
#[cfg(target_os = "linux")]
mod uptime;
#[cfg(target_os = "linux")]
mod users;
#[cfg(target_os = "linux")]
mod virt;

// macOS-specific collectors.
#[cfg(target_os = "macos")]
mod hardware_macos;
#[cfg(target_os = "macos")]
mod memory_macos;
#[cfg(target_os = "macos")]
mod network_macos;
#[cfg(target_os = "macos")]
mod os_macos;
#[cfg(target_os = "macos")]
mod swap_macos;

// FreeBSD-specific collectors.
#[cfg(target_os = "freebsd")]
mod os_freebsd;

// OpenBSD-specific collectors.
#[cfg(target_os = "openbsd")]
mod os_openbsd;

// NetBSD-specific collectors.
#[cfg(target_os = "netbsd")]
mod os_netbsd;

// illumos-specific collectors.
#[cfg(target_os = "illumos")]
mod os_illumos;
#[cfg(target_os = "illumos")]
mod hardware_illumos;
#[cfg(target_os = "illumos")]
mod memory_illumos;
#[cfg(target_os = "illumos")]
mod network_illumos;
#[cfg(target_os = "illumos")]
mod uptime_illumos;
#[cfg(target_os = "illumos")]
mod kernel_illumos;
#[cfg(target_os = "illumos")]
mod swap_illumos;
#[cfg(target_os = "illumos")]
mod mountpoints_illumos;
#[cfg(target_os = "illumos")]
mod filesystem_illumos;

// Shared BSD/macOS collectors.
#[cfg(any(target_os = "macos", target_os = "freebsd", target_os = "openbsd", target_os = "netbsd"))]
mod blockdevices_bsd;
#[cfg(any(target_os = "freebsd", target_os = "openbsd", target_os = "netbsd"))]
mod hardware_bsd;
#[cfg(any(target_os = "macos", target_os = "freebsd", target_os = "openbsd", target_os = "netbsd", target_os = "illumos"))]
mod identity_bsd;
#[cfg(any(target_os = "macos", target_os = "freebsd", target_os = "openbsd", target_os = "netbsd"))]
mod kernel_bsd;
#[cfg(any(target_os = "macos", target_os = "freebsd", target_os = "openbsd", target_os = "netbsd", target_os = "illumos"))]
mod loadavg_bsd;
#[cfg(any(target_os = "macos", target_os = "freebsd", target_os = "openbsd", target_os = "netbsd", target_os = "illumos"))]
mod locale_bsd;
#[cfg(any(target_os = "freebsd", target_os = "openbsd", target_os = "netbsd"))]
mod memory_bsd;
#[cfg(any(target_os = "macos", target_os = "freebsd", target_os = "openbsd", target_os = "netbsd"))]
pub(crate) mod mountpoints_bsd;
#[cfg(any(target_os = "freebsd", target_os = "openbsd", target_os = "netbsd"))]
mod network_bsd;
#[cfg(any(target_os = "macos", target_os = "freebsd", target_os = "openbsd", target_os = "netbsd"))]
mod filesystem_bsd;
#[cfg(any(target_os = "macos", target_os = "freebsd", target_os = "openbsd", target_os = "netbsd", target_os = "illumos"))]
mod packages_bsd;
#[cfg(any(target_os = "macos", target_os = "freebsd", target_os = "openbsd", target_os = "netbsd", target_os = "illumos"))]
mod processors_bsd;
#[cfg(any(target_os = "freebsd", target_os = "openbsd", target_os = "netbsd"))]
mod swap_freebsd;
#[cfg(any(target_os = "macos", target_os = "freebsd", target_os = "openbsd", target_os = "netbsd"))]
mod timezone_bsd;
#[cfg(any(target_os = "macos", target_os = "freebsd", target_os = "openbsd", target_os = "netbsd"))]
mod uptime_bsd;
#[cfg(any(target_os = "macos", target_os = "freebsd", target_os = "openbsd", target_os = "netbsd", target_os = "illumos"))]
mod users_bsd;
#[cfg(any(target_os = "macos", target_os = "freebsd", target_os = "openbsd", target_os = "netbsd", target_os = "illumos"))]
mod virt_bsd;
#[cfg(any(target_os = "macos", target_os = "freebsd", target_os = "openbsd", target_os = "netbsd", target_os = "illumos"))]
mod dmi_bsd;
#[cfg(any(target_os = "macos", target_os = "freebsd", target_os = "openbsd", target_os = "netbsd", target_os = "illumos"))]
mod gpu_bsd;

// Windows-specific collectors.
#[cfg(windows)]
mod filesystem_windows;
#[cfg(windows)]
mod hardware_windows;
#[cfg(windows)]
mod identity_windows;
#[cfg(windows)]
mod memory_windows;
#[cfg(windows)]
mod mountpoints_windows;
#[cfg(windows)]
mod network_windows;
#[cfg(windows)]
mod os_windows;
#[cfg(windows)]
mod packages_windows;
#[cfg(windows)]
mod processors_windows;
#[cfg(windows)]
mod swap_windows;
#[cfg(windows)]
mod timezone_windows;
#[cfg(windows)]
mod uptime_windows;
#[cfg(windows)]
mod users_windows;

use anyhow::Result;
use serde_json::Value;
use std::time::Duration;
use tracing::warn;

/// Maximum time a single trait collector may run.
pub const COLLECTOR_TIMEOUT: Duration = Duration::from_secs(5);

/// Enum dispatch for the 33 built-in trait collectors.
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
    CertLifecycle,
    Ports,
    SecurityScan,
    Tpm,
    SecureBoot,
    Connwaer,
    /// Display server detection (X11, Wayland, both, none).
    Display,
    /// Periodic: counts packages with available updates. Expensive — runs
    /// on a longer interval (e.g., hourly) and caches the result.
    PackageUpdates,
    /// Reports cached swarm blobs for distribution tracking.
    SwarmCache,
    /// Reports seed files available for envoy-originated distribution.
    SwarmSeeds,
    /// Reports vigorepo source-side publishing state (active sources,
    /// discovery errors, publish errors). Source-side data is written
    /// by `vigorepo::reactor` as a snapshot file; this collector reads
    /// the snapshot on each check-in cycle.
    Vigorepo,
    /// Enumerates apt/yum/dnf package sources configured on the host.
    /// Emits per-source rows with URL (credentials stripped), suite,
    /// components, and enabled state. Linux-only. Server-side compares
    /// against `compliance.approved_sources` allowlist.
    PackageSourceAudit,
    /// Reads `/proc/modules` + `/sys/module/<name>/taint` to report
    /// loaded kernel modules and their signature status. Linux-only.
    /// Pure sysfs reads; no subprocess spawns.
    KernelModuleAttestation,
    /// Correlates `/proc/net/tcp*` LISTEN-state sockets with the owning
    /// process via `/proc/<pid>/fd/*` symlinks. Emits per-listener rows
    /// with port, bind address, pid, command, and binary path.
    /// Linux-only. Passive — no handshakes, no network traffic.
    Listeners,
    /// Joins scanner CVE output (trivy / debsecan) with running binary
    /// discovery via `/proc/*/exe` and batched dpkg/rpm ownership
    /// lookup. Emits per-CVE-per-binary rows so operators can filter
    /// for CVEs affecting code that's actually executing, not just
    /// installed. Linux-only, periodic (hourly).
    CveRunningCorrelation,
    /// Per-user vigosync mesh state — file list, tombstones, digests.
    /// Consumed by `server/vigosyncmesh` to render the `/mesh` page.
    Vigosync,
    /// Per-user vigocrypt mesh state — file list, tombstones,
    /// recipients, session-unlocked signal. Consumed by
    /// `server/vigocryptmesh`.
    Vigocrypt,
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
            Self::CertLifecycle => "cert_lifecycle",
            Self::Ports => "ports",
            Self::SecurityScan => "security_scan",
            Self::Tpm => "tpm",
            Self::SecureBoot => "secureboot",
            Self::Connwaer => "connwaer",
            Self::Display => "display",
            Self::PackageUpdates => "package_updates",
            Self::SwarmCache => "swarm",
            Self::SwarmSeeds => "swarm_seeds",
            Self::Vigorepo => "vigorepo",
            Self::PackageSourceAudit => "package_source_audit",
            Self::KernelModuleAttestation => "kernel_module_attestation",
            Self::Listeners => "listeners",
            Self::CveRunningCorrelation => "cve_running_correlation",
            Self::Vigosync => "vigosync",
            Self::Vigocrypt => "vigocrypt",
        }
    }

    /// Per-collector timeout. Most collectors use the default 5s. Collectors
    /// that hash large files on first run get a longer window.
    pub fn timeout(&self) -> Duration {
        match self {
            // Seed collector hashes files that can be tens of GiB. First hash
            // is slow (cached afterward), so allow up to 10 minutes.
            Self::SwarmSeeds => Duration::from_secs(600),
            // Security scan runs external tools that can be slow.
            Self::SecurityScan => Duration::from_secs(60),
            // CVE correlation runs a /proc walk plus a batched dpkg/rpm
            // lookup. Per-call ceiling is 15s (see PKG_LOOKUP_TIMEOUT);
            // the overall collector gets 30s so the walk + lookup +
            // parsing fit comfortably on big hosts.
            Self::CveRunningCorrelation => Duration::from_secs(30),
            _ => COLLECTOR_TIMEOUT,
        }
    }

    /// Collect traits for this collector.
    pub fn collect(&self) -> Result<Value> {
        match self {
            Self::Time => time_collector::collect(),
            Self::Cloud => cloud::collect(),
            Self::Connwaer => connwaer::collect(),
            Self::Display => display::collect(),
            Self::Docker => docker::collect(),
            Self::SwarmCache => swarm_cache::collect(),
            Self::SwarmSeeds => swarm_seeds::collect(),
            Self::Vigorepo => vigorepo::collect(),
            Self::Vigosync => vigosync::collect(),
            Self::Vigocrypt => vigocrypt::collect(),

            // Unix implementations.
            #[cfg(unix)]
            Self::CertLifecycle => cert_lifecycle::collect(),
            #[cfg(unix)]
            Self::Ports => ports::collect(),
            Self::SecurityScan => security_scan::collect(),
            Self::PackageUpdates => packages::collect_updates(),

            // Linux implementations.
            #[cfg(target_os = "linux")]
            Self::Os => os::collect(),
            #[cfg(target_os = "linux")]
            Self::Hardware => hardware::collect(),
            #[cfg(target_os = "linux")]
            Self::Network => network::collect(),
            #[cfg(target_os = "linux")]
            Self::Packages => packages::collect(),
            #[cfg(target_os = "linux")]
            Self::Identity => identity::collect(),
            #[cfg(target_os = "linux")]
            Self::Virtual => virt::collect(),
            #[cfg(target_os = "linux")]
            Self::Uptime => uptime::collect(),
            #[cfg(target_os = "linux")]
            Self::LoadAvg => loadavg::collect(),
            #[cfg(target_os = "linux")]
            Self::Mountpoints => mountpoints::collect(),
            #[cfg(target_os = "linux")]
            Self::Processors => processors::collect(),
            #[cfg(target_os = "linux")]
            Self::Filesystem => filesystem::collect(),
            #[cfg(target_os = "linux")]
            Self::Kernel => kernel::collect(),
            #[cfg(target_os = "linux")]
            Self::Users => users::collect(),
            #[cfg(target_os = "linux")]
            Self::Memory => memory::collect(),
            #[cfg(target_os = "linux")]
            Self::BlockDevices => blockdevices::collect(),
            #[cfg(target_os = "linux")]
            Self::Dmi => dmi::collect(),
            #[cfg(target_os = "linux")]
            Self::Systemd => systemd::collect(),
            #[cfg(target_os = "linux")]
            Self::SELinux => selinux::collect(),
            #[cfg(target_os = "linux")]
            Self::Timezone => timezone::collect(),
            #[cfg(target_os = "linux")]
            Self::Locale => locale::collect(),
            #[cfg(target_os = "linux")]
            Self::Swap => swap::collect(),
            #[cfg(target_os = "linux")]
            Self::Gpu => gpu::collect(),
            #[cfg(target_os = "linux")]
            Self::Tpm => tpm::collect(),
            #[cfg(target_os = "linux")]
            Self::SecureBoot => secureboot::collect(),
            #[cfg(target_os = "linux")]
            Self::PackageSourceAudit => package_source_audit::collect(),
            #[cfg(target_os = "linux")]
            Self::KernelModuleAttestation => kernel_module_attestation::collect(),
            #[cfg(target_os = "linux")]
            Self::Listeners => listeners::collect(),
            #[cfg(target_os = "linux")]
            Self::CveRunningCorrelation => cve_running_correlation::collect(),

            // macOS implementations.
            #[cfg(target_os = "macos")]
            Self::Os => os_macos::collect(),
            #[cfg(target_os = "macos")]
            Self::Hardware => hardware_macos::collect(),
            #[cfg(target_os = "macos")]
            Self::Network => network_macos::collect(),
            #[cfg(target_os = "macos")]
            Self::Memory => memory_macos::collect(),
            #[cfg(target_os = "macos")]
            Self::Swap => swap_macos::collect(),

            // FreeBSD implementations.
            #[cfg(target_os = "freebsd")]
            Self::Os => os_freebsd::collect(),
            #[cfg(target_os = "freebsd")]
            Self::Hardware => hardware_bsd::collect(),
            #[cfg(target_os = "freebsd")]
            Self::Network => network_bsd::collect(),
            #[cfg(target_os = "freebsd")]
            Self::Memory => memory_bsd::collect(),
            #[cfg(target_os = "freebsd")]
            Self::Swap => swap_freebsd::collect(),

            // OpenBSD implementations.
            #[cfg(target_os = "openbsd")]
            Self::Os => os_openbsd::collect(),
            #[cfg(target_os = "openbsd")]
            Self::Hardware => hardware_bsd::collect(),
            #[cfg(target_os = "openbsd")]
            Self::Network => network_bsd::collect(),
            #[cfg(target_os = "openbsd")]
            Self::Memory => memory_bsd::collect(),
            #[cfg(target_os = "openbsd")]
            Self::Swap => swap_freebsd::collect(),

            // NetBSD implementations.
            #[cfg(target_os = "netbsd")]
            Self::Os => os_netbsd::collect(),
            #[cfg(target_os = "netbsd")]
            Self::Hardware => hardware_bsd::collect(),
            #[cfg(target_os = "netbsd")]
            Self::Network => network_bsd::collect(),
            #[cfg(target_os = "netbsd")]
            Self::Memory => memory_bsd::collect(),
            #[cfg(target_os = "netbsd")]
            Self::Swap => swap_freebsd::collect(),

            // illumos implementations.
            #[cfg(target_os = "illumos")]
            Self::Os => os_illumos::collect(),
            #[cfg(target_os = "illumos")]
            Self::Hardware => hardware_illumos::collect(),
            #[cfg(target_os = "illumos")]
            Self::Network => network_illumos::collect(),
            #[cfg(target_os = "illumos")]
            Self::Memory => memory_illumos::collect(),
            #[cfg(target_os = "illumos")]
            Self::Uptime => uptime_illumos::collect(),
            #[cfg(target_os = "illumos")]
            Self::Kernel => kernel_illumos::collect(),
            #[cfg(target_os = "illumos")]
            Self::Swap => swap_illumos::collect(),
            #[cfg(target_os = "illumos")]
            Self::Mountpoints => mountpoints_illumos::collect(),
            #[cfg(target_os = "illumos")]
            Self::Filesystem => filesystem_illumos::collect(),

            // Shared BSD/macOS collectors.
            #[cfg(any(target_os = "macos", target_os = "freebsd", target_os = "openbsd", target_os = "netbsd", target_os = "illumos"))]
            Self::Packages => packages_bsd::collect(),
            #[cfg(any(target_os = "macos", target_os = "freebsd", target_os = "openbsd", target_os = "netbsd", target_os = "illumos"))]
            Self::Identity => identity_bsd::collect(),
            #[cfg(any(target_os = "macos", target_os = "freebsd", target_os = "openbsd", target_os = "netbsd", target_os = "illumos"))]
            Self::Virtual => virt_bsd::collect(),
            #[cfg(any(target_os = "macos", target_os = "freebsd", target_os = "openbsd", target_os = "netbsd"))]
            Self::Uptime => uptime_bsd::collect(),
            #[cfg(any(target_os = "macos", target_os = "freebsd", target_os = "openbsd", target_os = "netbsd", target_os = "illumos"))]
            Self::LoadAvg => loadavg_bsd::collect(),
            #[cfg(any(target_os = "macos", target_os = "freebsd", target_os = "openbsd", target_os = "netbsd"))]
            Self::Mountpoints => mountpoints_bsd::collect(),
            #[cfg(any(target_os = "macos", target_os = "freebsd", target_os = "openbsd", target_os = "netbsd", target_os = "illumos"))]
            Self::Processors => processors_bsd::collect(),
            #[cfg(any(target_os = "macos", target_os = "freebsd", target_os = "openbsd", target_os = "netbsd"))]
            Self::Filesystem => filesystem_bsd::collect(),
            #[cfg(any(target_os = "macos", target_os = "freebsd", target_os = "openbsd", target_os = "netbsd"))]
            Self::Kernel => kernel_bsd::collect(),
            #[cfg(any(target_os = "macos", target_os = "freebsd", target_os = "openbsd", target_os = "netbsd", target_os = "illumos"))]
            Self::Users => users_bsd::collect(),
            #[cfg(any(target_os = "macos", target_os = "freebsd", target_os = "openbsd", target_os = "netbsd"))]
            Self::BlockDevices => blockdevices_bsd::collect(),
            #[cfg(any(target_os = "macos", target_os = "freebsd", target_os = "openbsd", target_os = "netbsd", target_os = "illumos"))]
            Self::Dmi => dmi_bsd::collect(),
            #[cfg(any(target_os = "macos", target_os = "freebsd", target_os = "openbsd", target_os = "netbsd"))]
            Self::Timezone => timezone_bsd::collect(),
            #[cfg(any(target_os = "macos", target_os = "freebsd", target_os = "openbsd", target_os = "netbsd", target_os = "illumos"))]
            Self::Locale => locale_bsd::collect(),
            #[cfg(any(target_os = "macos", target_os = "freebsd", target_os = "openbsd", target_os = "netbsd", target_os = "illumos"))]
            Self::Gpu => gpu_bsd::collect(),

            // illumos: return null for unsupported concepts.
            #[cfg(target_os = "illumos")]
            Self::BlockDevices | Self::Timezone | Self::Systemd | Self::SELinux |
            Self::PackageSourceAudit | Self::KernelModuleAttestation | Self::Listeners |
            Self::CveRunningCorrelation => Ok(Value::Null),

            // Linux-only concepts — return null on BSD/macOS.
            #[cfg(any(target_os = "macos", target_os = "freebsd", target_os = "openbsd", target_os = "netbsd"))]
            Self::Systemd | Self::SELinux |
            Self::PackageSourceAudit | Self::KernelModuleAttestation | Self::Listeners |
            Self::CveRunningCorrelation => Ok(Value::Null),

            // Windows implementations.
            #[cfg(windows)]
            Self::Os => os_windows::collect(),
            #[cfg(windows)]
            Self::Hardware => hardware_windows::collect(),
            #[cfg(windows)]
            Self::Network => network_windows::collect(),
            #[cfg(windows)]
            Self::Packages => packages_windows::collect(),
            #[cfg(windows)]
            Self::Identity => identity_windows::collect(),
            #[cfg(windows)]
            Self::Uptime => uptime_windows::collect(),
            #[cfg(windows)]
            Self::Mountpoints => mountpoints_windows::collect(),
            #[cfg(windows)]
            Self::Processors => processors_windows::collect(),
            #[cfg(windows)]
            Self::Filesystem => filesystem_windows::collect(),
            #[cfg(windows)]
            Self::Users => users_windows::collect(),
            #[cfg(windows)]
            Self::Memory => memory_windows::collect(),
            #[cfg(windows)]
            Self::Swap => swap_windows::collect(),
            #[cfg(windows)]
            Self::Timezone => timezone_windows::collect(),
            #[cfg(windows)]
            Self::Dmi => dmi::collect(),

            // Collectors that skip on unsupported platforms.
            #[cfg(windows)]
            Self::Virtual | Self::LoadAvg | Self::Kernel |
            Self::BlockDevices | Self::Systemd | Self::SELinux |
            Self::Locale | Self::Gpu | Self::CertLifecycle | Self::Ports |
            Self::PackageSourceAudit | Self::KernelModuleAttestation |
            Self::Listeners | Self::CveRunningCorrelation => Ok(Value::Null),
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
            "cert_lifecycle" => Some(Self::CertLifecycle),
            "ports" => Some(Self::Ports),
            "security_scan" => Some(Self::SecurityScan),
            "tpm" => Some(Self::Tpm),
            "secureboot" => Some(Self::SecureBoot),
            "connwaer" => Some(Self::Connwaer),
            "display" => Some(Self::Display),
            "package_updates" => Some(Self::PackageUpdates),
            "swarm" => Some(Self::SwarmCache),
            "swarm_seeds" => Some(Self::SwarmSeeds),
            "vigorepo" => Some(Self::Vigorepo),
            "package_source_audit" => Some(Self::PackageSourceAudit),
            "kernel_module_attestation" => Some(Self::KernelModuleAttestation),
            "listeners" => Some(Self::Listeners),
            "cve_running_correlation" => Some(Self::CveRunningCorrelation),
            "vigosync" => Some(Self::Vigosync),
            "vigocrypt" => Some(Self::Vigocrypt),
            _ => None,
        }
    }
}

/// The standard set of default trait collectors.
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
        CollectorKind::CertLifecycle,
        CollectorKind::Ports,
        CollectorKind::SecurityScan,
        CollectorKind::Tpm,
        CollectorKind::SecureBoot,
        CollectorKind::Connwaer,
        CollectorKind::Display,
        CollectorKind::PackageUpdates,
        CollectorKind::SwarmCache,
        CollectorKind::SwarmSeeds,
        CollectorKind::Vigorepo,
        CollectorKind::PackageSourceAudit,
        CollectorKind::KernelModuleAttestation,
        CollectorKind::Listeners,
        CollectorKind::CveRunningCorrelation,
        CollectorKind::Vigosync,
        CollectorKind::Vigocrypt,
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
        CollectorKind::Ports,
        CollectorKind::SwarmCache,
        CollectorKind::SwarmSeeds,
        CollectorKind::Vigorepo,
        CollectorKind::Vigosync,
        CollectorKind::Vigocrypt,
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
        CollectorKind::Kernel,
        CollectorKind::BlockDevices,
        CollectorKind::Docker,
        CollectorKind::Systemd,
        CollectorKind::SELinux,
        CollectorKind::Timezone,
        CollectorKind::Locale,
        CollectorKind::Gpu,
        CollectorKind::CertLifecycle,
        CollectorKind::SecurityScan,
        CollectorKind::Tpm,
        CollectorKind::SecureBoot,
        CollectorKind::Connwaer,
        CollectorKind::Display,
        CollectorKind::PackageSourceAudit,
        CollectorKind::KernelModuleAttestation,
        CollectorKind::Listeners,
    ]
}

/// Collectors that are expensive (network I/O, slow subprocesses) and should
/// run on a much longer interval — e.g., hourly. Results are cached between runs.
pub fn periodic_collectors() -> Vec<CollectorKind> {
    vec![
        CollectorKind::PackageUpdates,
        CollectorKind::Cloud,
        CollectorKind::CveRunningCorrelation,
    ]
}

/// Collect traits from all provided collectors in parallel.
/// Failed collectors are logged and skipped (non-fatal).
pub async fn collect_all(collectors: &[CollectorKind]) -> Value {
    let mut handles = Vec::with_capacity(collectors.len());

    for &kind in collectors {
        handles.push(tokio::spawn(async move {
            let result = tokio::time::timeout(
                kind.timeout(),
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
        assert_eq!(default_collectors().len(), 40);
    }

    #[test]
    fn test_volatile_plus_stable_plus_periodic_covers_all() {
        let mut all: Vec<CollectorKind> = volatile_collectors();
        all.extend(stable_collectors());
        all.extend(periodic_collectors());
        let defaults = default_collectors();
        for d in &defaults {
            assert!(all.contains(d), "collector {:?} missing from volatile+stable+periodic", d);
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
