// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Resource executors for the Hobnob agent.
//!
//! Every executor implements the [`Executor`] trait. Executors are idempotent:
//! they check current state before acting and only modify the system when drift
//! is detected.

// Cross-platform executors.
pub mod archive;
pub mod atomic;
pub mod blockinfile;
pub mod custom;
pub mod directory;
pub mod docker_container;
pub mod docker_image;
pub mod exec;
pub mod field_edit;
pub mod file;
pub mod file_line;
pub mod gem;
pub mod git;
pub mod host;
pub mod ini;
pub mod json_file;
pub mod cisco_acl;
pub mod cisco_interface;
pub mod cisco_vlan;
pub mod junos_interface;
pub mod network_banner;
pub mod network_config;
pub mod network_ntp;
pub mod network_snmp;
pub mod network_syslog;
pub mod network_user;
pub mod npm;
pub mod pip;
pub mod pkgmgr;
pub mod region;
pub mod replace;
pub mod process;
pub mod reboot;
pub mod repository;
pub mod ssh_exec;
pub mod sshkey;
pub mod symlink;

// Linux-only executors.
#[cfg(target_os = "linux")]
pub mod alternatives;
#[cfg(any(target_os = "linux", target_os = "macos", target_os = "freebsd", target_os = "openbsd", target_os = "netbsd", target_os = "illumos"))]
pub mod cron;
#[cfg(target_os = "linux")]
pub mod firewall;
#[cfg(any(target_os = "linux", target_os = "openbsd", target_os = "netbsd", target_os = "illumos"))]
pub mod group;
#[cfg(target_os = "linux")]
pub mod hostname;
#[cfg(target_os = "linux")]
pub mod kernel_module;
#[cfg(target_os = "linux")]
pub mod logrotate;
#[cfg(target_os = "linux")]
pub mod mount;
#[cfg(target_os = "linux")]
pub mod selinux;
#[cfg(target_os = "linux")]
pub mod service;
#[cfg(target_os = "linux")]
pub mod swap;
#[cfg(target_os = "linux")]
pub mod sysctl;
#[cfg(target_os = "linux")]
pub mod timezone;
#[cfg(target_os = "linux")]
pub mod tmpfile;
#[cfg(any(target_os = "linux", target_os = "openbsd", target_os = "netbsd", target_os = "illumos"))]
pub mod user;

// macOS-native executors.
#[cfg(target_os = "macos")]
pub mod group_macos;
#[cfg(target_os = "macos")]
pub mod hostname_macos;
#[cfg(target_os = "macos")]
pub mod service_macos;
#[cfg(target_os = "macos")]
pub mod timezone_macos;
#[cfg(target_os = "macos")]
pub mod user_macos;

// FreeBSD-native executors.
#[cfg(target_os = "freebsd")]
pub mod group_freebsd;
#[cfg(target_os = "freebsd")]
pub mod kernel_module_freebsd;
#[cfg(target_os = "freebsd")]
pub mod service_freebsd;
#[cfg(target_os = "freebsd")]
pub mod user_freebsd;

// OpenBSD-native executors.
#[cfg(target_os = "openbsd")]
pub mod service_openbsd;

// illumos-native executors.
#[cfg(target_os = "illumos")]
pub mod service_illumos;
#[cfg(target_os = "illumos")]
pub mod timezone_illumos;

// Shared BSD/macOS executors.
#[cfg(any(target_os = "macos", target_os = "freebsd", target_os = "openbsd", target_os = "netbsd"))]
pub mod firewall_pf;
#[cfg(any(target_os = "macos", target_os = "freebsd", target_os = "openbsd", target_os = "netbsd", target_os = "illumos"))]
pub mod hostname_bsd;
#[cfg(any(target_os = "macos", target_os = "freebsd", target_os = "openbsd", target_os = "netbsd"))]
pub mod mount_bsd;
#[cfg(any(target_os = "macos", target_os = "freebsd", target_os = "openbsd", target_os = "netbsd"))]
pub mod swap_bsd;
#[cfg(any(target_os = "macos", target_os = "freebsd", target_os = "openbsd", target_os = "netbsd"))]
pub mod sysctl_bsd;
#[cfg(any(target_os = "macos", target_os = "freebsd", target_os = "openbsd", target_os = "netbsd"))]
pub mod timezone_bsd;

// Windows-native executors.
#[cfg(windows)]
pub mod cron_windows;
#[cfg(windows)]
pub mod firewall_windows;
#[cfg(windows)]
pub mod hostname_windows;
#[cfg(windows)]
pub mod package_windows;
#[cfg(windows)]
pub mod service_windows;
#[cfg(windows)]
pub mod timezone_windows;
#[cfg(windows)]
pub mod user_windows;

#[cfg(test)]
pub mod test_support;

pub mod network_common;

use std::collections::HashMap;
use std::fmt;
use std::process::Output;

/// What action an executor performed on a resource.
#[derive(Debug, Clone, PartialEq, Eq, Default)]
pub enum Action {
    #[default]
    None,
    Created,
    Updated,
    Deleted,
    Skipped,
}

impl fmt::Display for Action {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Action::None => write!(f, "already in desired state"),
            Action::Created => write!(f, "created"),
            Action::Updated => write!(f, "updated"),
            Action::Deleted => write!(f, "deleted"),
            Action::Skipped => write!(f, "skipped"),
        }
    }
}

impl Action {
    /// Parse from the string representation used in proto/Go.
    pub fn from_str_lossy(s: &str) -> Self {
        match s {
            "created" => Action::Created,
            "updated" => Action::Updated,
            "deleted" => Action::Deleted,
            "skipped" => Action::Skipped,
            "already in desired state" => Action::None,
            _ => Action::None,
        }
    }
}

/// The outcome of executing a single resource.
#[derive(Debug, Clone, Default)]
pub struct ResourceResult {
    pub module_name: String,
    pub resource_type: String,
    pub resource_name: String,
    pub action: Action,
    pub changed: bool,
    pub error: Option<String>,
    pub duration_ms: i32,
    /// File executor: content of the file before overwrite (for server-side snapshots).
    pub previous_content: Option<Vec<u8>>,
    /// File executor: SHA-256 hex of the new content written.
    pub content_hash: Option<String>,
    /// File executor: absolute path of the managed file.
    pub file_path: Option<String>,
}

impl ResourceResult {
    /// Create a new result pre-filled with resource metadata.
    pub fn new(resource_type: &str, resource_name: &str) -> Self {
        Self {
            module_name: String::new(),
            resource_type: resource_type.to_string(),
            resource_name: resource_name.to_string(),
            action: Action::None,
            changed: false,
            error: None,
            duration_ms: 0,
            previous_content: None,
            content_hash: None,
            file_path: None,
        }
    }

    /// Set error and return self for chaining.
    pub fn with_error(mut self, msg: String) -> Self {
        self.error = Some(msg);
        self
    }
}

/// Trait that all resource executors must implement.
pub trait Executor: Send + Sync {
    fn execute(
        &self,
        params: &HashMap<String, String>,
        dry_run: bool,
    ) -> ResourceResult;
}

/// Runs a command and returns (stdout, success).
/// This is the default command runner used by executors.
pub fn run_command(name: &str, args: &[&str]) -> std::io::Result<Output> {
    std::process::Command::new(name).args(args).output()
}

/// Runs a command with stdin piped in.
pub fn run_command_with_stdin(
    name: &str,
    args: &[&str],
    stdin_data: &[u8],
) -> std::io::Result<Output> {
    use std::io::Write;
    use std::process::{Command, Stdio};

    let mut child = Command::new(name)
        .args(args)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()?;

    if let Some(ref mut stdin) = child.stdin {
        stdin.write_all(stdin_data)?;
    }

    child.wait_with_output()
}

/// Trait for injectable command runners (enables testing).
pub trait CommandRunner: Send + Sync {
    fn run(&self, name: &str, args: &[&str]) -> std::io::Result<Output>;
    fn run_with_stdin(
        &self,
        stdin_data: &[u8],
        name: &str,
        args: &[&str],
    ) -> std::io::Result<Output>;
}

/// Default command runner that shells out via std::process.
pub struct DefaultRunner;

impl CommandRunner for DefaultRunner {
    fn run(&self, name: &str, args: &[&str]) -> std::io::Result<Output> {
        run_command(name, args)
    }

    fn run_with_stdin(
        &self,
        stdin_data: &[u8],
        name: &str,
        args: &[&str],
    ) -> std::io::Result<Output> {
        run_command_with_stdin(name, args, stdin_data)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn action_display() {
        assert_eq!(Action::None.to_string(), "already in desired state");
        assert_eq!(Action::Created.to_string(), "created");
        assert_eq!(Action::Updated.to_string(), "updated");
        assert_eq!(Action::Deleted.to_string(), "deleted");
        assert_eq!(Action::Skipped.to_string(), "skipped");
    }

    #[test]
    fn action_from_str_lossy() {
        assert_eq!(Action::from_str_lossy("created"), Action::Created);
        assert_eq!(Action::from_str_lossy("unknown"), Action::None);
    }

    #[test]
    fn resource_result_new() {
        let r = ResourceResult::new("file", "/etc/app.conf");
        assert_eq!(r.resource_type, "file");
        assert_eq!(r.resource_name, "/etc/app.conf");
        assert_eq!(r.action, Action::None);
        assert!(!r.changed);
        assert!(r.error.is_none());
    }

    #[test]
    fn resource_result_with_error() {
        let r = ResourceResult::new("file", "/etc/app.conf")
            .with_error("something broke".into());
        assert_eq!(r.error.as_deref(), Some("something broke"));
    }
}
