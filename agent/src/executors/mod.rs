// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Resource executors for the Vigo agent.
//!
//! Every executor implements the [`Executor`] trait. Executors are idempotent:
//! they check current state before acting and only modify the system when drift
//! is detected.

// Cross-platform executors.
pub mod download;
pub mod source_package;
pub mod nonrepo_package;
pub mod atomic;
pub mod backup;
pub mod blockinfile;
pub mod db_backup;
pub mod custom;
pub mod directory;
pub mod container;
pub mod vm;
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
pub mod network_acl;
pub mod network_banner;
pub mod network_config;
pub mod network_dns;
pub mod network_interface;
pub mod network_ntp;
pub mod network_snmp;
pub mod network_static_route;
pub mod network_syslog;
pub mod network_user;
pub mod network_vlan;
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
pub mod systemd_dropin;
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
pub mod certificate_windows;
#[cfg(windows)]
pub mod domain_membership;
#[cfg(windows)]
pub mod package_windows;
#[cfg(windows)]
pub mod powershell;
#[cfg(windows)]
pub mod reboot_windows;
#[cfg(windows)]
pub mod registry_windows;
#[cfg(windows)]
pub mod service_windows;
#[cfg(windows)]
pub mod timezone_windows;
#[cfg(windows)]
pub mod user_windows;
#[cfg(windows)]
pub mod local_security_policy;
#[cfg(windows)]
pub mod windows_env;
#[cfg(windows)]
pub mod service_recovery_windows;
#[cfg(windows)]
pub mod windows_defender;
#[cfg(windows)]
pub mod windows_update;

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
    /// Resource could not be applied due to an infrastructure issue (broken repo,
    /// expired GPG key, disk full, etc.). Distinct from a normal failure — the
    /// module config may be correct but external conditions prevent execution.
    /// Server treats blocked resources as "diverged" convergence, not "failed".
    Blocked,
}

impl fmt::Display for Action {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Action::None => write!(f, "settled"),
            Action::Created => write!(f, "created"),
            Action::Updated => write!(f, "updated"),
            Action::Deleted => write!(f, "deleted"),
            Action::Skipped => write!(f, "skipped"),
            Action::Blocked => write!(f, "blocked"),
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
            "blocked" => Action::Blocked,
            "settled" => Action::None,
            _ => Action::None,
        }
    }
}

/// The outcome of executing a single resource.
///
/// **Contract:** at runner emission, `error.is_some()` implies
/// `action ∈ {None, Skipped, Blocked} && changed == false`. Executors may
/// set `action`/`changed` optimistically before a fallible operation —
/// `Runner` calls `normalize_for_error` to scrub contradictory state
/// (`Created`/`Updated`/`Deleted` + error) at the result boundary, so
/// per-executor manual rollback is unnecessary. `Blocked` survives the
/// normalizer because it carries a legitimate diagnostic ("diverged but
/// not failed"); see `Action::Blocked`.
#[derive(Debug, Clone, Default)]
pub struct ResourceResult {
    pub configcrate_name: String,
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
    /// Brief reason when action is Skipped (e.g. "when: false", "onlyif guard", "dep failed").
    pub skip_reason: Option<String>,
}

impl ResourceResult {
    /// Create a new result pre-filled with resource metadata.
    pub fn new(resource_type: &str, resource_name: &str) -> Self {
        Self {
            configcrate_name: String::new(),
            resource_type: resource_type.to_string(),
            resource_name: resource_name.to_string(),
            action: Action::None,
            changed: false,
            error: None,
            duration_ms: 0,
            previous_content: None,
            content_hash: None,
            file_path: None,
            skip_reason: None,
        }
    }

    /// Set error and return self for chaining.
    pub fn with_error(mut self, msg: String) -> Self {
        self.error = Some(msg);
        self
    }

    /// Enforce the `ResourceResult` contract at result emission: if an error
    /// is set and the action claims a state change (Created/Updated/Deleted),
    /// scrub `action` to `None` and `changed` to `false`. Preserves
    /// `Blocked` (diverged-not-failed with diagnostic), `Skipped`, and the
    /// already-correct `None`. Preserves `duration_ms` — time spent on the
    /// failed operation is useful operator data.
    pub fn normalize_for_error(&mut self) {
        if self.error.is_none() {
            return;
        }
        if matches!(self.action, Action::Created | Action::Updated | Action::Deleted) {
            self.action = Action::None;
            self.changed = false;
        }
    }
}

/// Trait that all resource executors must implement.
pub trait Executor: Send + Sync {
    fn execute(
        &self,
        params: &HashMap<String, String>,
        dry_run: bool,
    ) -> ResourceResult;

    /// Read the resource's current live value, for resource-reversal snapshots.
    ///
    /// Only value-setter executors (hostname, timezone) implement this — it
    /// exposes the same current-state read they already do in `execute`'s
    /// check-before-act, so the reversal layer can snapshot the operator's
    /// prior value without a second source of truth. Returns `None` when the
    /// value can't be read or the executor isn't snapshot-reversible (the
    /// default), which the reversal layer treats as "no snapshot available".
    fn current_value(&self, _params: &HashMap<String, String>) -> Option<String> {
        None
    }
}

/// Runs a command and returns (stdout, success).
/// This is the default command runner used by executors.
pub fn run_command(name: &str, args: &[&str]) -> std::io::Result<Output> {
    std::process::Command::new(name).args(args).output()
}

/// Runs a command with a nice level applied.
pub fn run_command_niced(name: &str, args: &[&str], nice_level: i32) -> std::io::Result<Output> {
    let mut cmd = std::process::Command::new(name);
    cmd.args(args);
    if nice_level > 0 {
        crate::platform::process::apply_nice(&mut cmd, nice_level);
    }
    // Make this child the first OOM-kill target so host workloads win under
    // memory pressure (do NOT cap RLIMIT_AS — it crashes Go/JVM CLIs).
    crate::platform::process::apply_oom_score_adj(&mut cmd);
    cmd.output()
}

/// Writes `data` to a child's stdin on a detached thread, tolerating a broken
/// pipe. A child that exits before consuming all of stdin closes the pipe —
/// its exit status, not the write, is what matters, so `BrokenPipe` is not an
/// error here. Draining stdin on its own thread also lets `wait_with_output`
/// drain stdout/stderr concurrently, so a child that emits a large amount of
/// output before reading all of stdin can't deadlock against our write. The
/// stdin handle is moved in and dropped (EOF) when the write completes.
fn spawn_stdin_writer(stdin: Option<std::process::ChildStdin>, data: &[u8]) {
    let Some(mut stdin) = stdin else { return };
    let data = data.to_vec();
    std::thread::spawn(move || {
        use std::io::Write;
        if let Err(e) = stdin.write_all(&data) {
            if e.kind() != std::io::ErrorKind::BrokenPipe {
                tracing::debug!(error = %e, "writing stdin to child failed");
            }
        }
        // stdin dropped here → EOF for the child.
    });
}

/// Runs a command with stdin piped in.
pub fn run_command_with_stdin(
    name: &str,
    args: &[&str],
    stdin_data: &[u8],
) -> std::io::Result<Output> {
    run_command_with_stdin_niced(name, args, stdin_data, 0)
}

/// Runs a command with stdin piped in and a nice level applied.
pub fn run_command_with_stdin_niced(
    name: &str,
    args: &[&str],
    stdin_data: &[u8],
    nice_level: i32,
) -> std::io::Result<Output> {
    use std::process::{Command, Stdio};

    let mut cmd = Command::new(name);
    cmd.args(args)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());
    if nice_level > 0 {
        crate::platform::process::apply_nice(&mut cmd, nice_level);
    }
    // Make this child the first OOM-kill target so host workloads win under
    // memory pressure (do NOT cap RLIMIT_AS — it crashes Go/JVM CLIs).
    crate::platform::process::apply_oom_score_adj(&mut cmd);
    let mut child = cmd.spawn()?;

    spawn_stdin_writer(child.stdin.take(), stdin_data);
    child.wait_with_output()
}

/// Runs a command with nice, memory cap, and a per-process timeout.
/// On timeout the child process group is SIGTERM'd (5-second grace) then
/// SIGKILL'd via [`crate::platform::process::kill_process_tree`] so
/// nothing leaks behind. Returns a `TimedOut` io::Error in that case.
///
/// Used by the exec executor's foreground path when `timeout:` is set.
/// See `run_command_niced` for the no-timeout variant.
pub fn run_command_niced_timeout(
    name: &str,
    args: &[&str],
    nice_level: i32,
    timeout: std::time::Duration,
) -> std::io::Result<Output> {
    use std::process::{Command, Stdio};
    use std::sync::mpsc;
    use std::thread;

    let mut cmd = Command::new(name);
    cmd.args(args).stdout(Stdio::piped()).stderr(Stdio::piped());
    if nice_level > 0 {
        crate::platform::process::apply_nice(&mut cmd, nice_level);
    }
    // Running in its own process group so kill_process_tree can terminate
    // any shell-spawned children (sh -c 'scan | tee ...'), not just the
    // direct child.
    #[cfg(unix)]
    {
        use std::os::unix::process::CommandExt;
        unsafe {
            cmd.pre_exec(|| {
                libc::setpgid(0, 0);
                Ok(())
            });
        }
    }
    crate::platform::process::apply_oom_score_adj(&mut cmd);

    let child = cmd.spawn()?;
    let pid = child.id();
    let (tx, rx) = mpsc::channel();
    thread::spawn(move || {
        let result = child.wait_with_output();
        let _ = tx.send(result);
    });

    match rx.recv_timeout(timeout) {
        Ok(result) => result,
        Err(_) => {
            // Fire-and-forget kill — we don't wait for the reaper thread
            // above, it will eventually drain the zombie. Logging handled
            // by the caller via the TimedOut error.
            let grace = std::time::Duration::from_secs(5);
            crate::platform::process::kill_process_tree(pid, grace);
            Err(std::io::Error::new(
                std::io::ErrorKind::TimedOut,
                format!(
                    "{} timed out after {:.0}s",
                    name,
                    timeout.as_secs_f64()
                ),
            ))
        }
    }
}

/// Runs a command with stdin piped in, nice, memory cap, and a per-process
/// timeout. On timeout the child process group is killed; returns
/// `TimedOut`. Used by the exec executor's foreground-with-stdin path.
pub fn run_command_with_stdin_niced_timeout(
    name: &str,
    args: &[&str],
    stdin_data: &[u8],
    nice_level: i32,
    timeout: std::time::Duration,
) -> std::io::Result<Output> {
    use std::process::{Command, Stdio};
    use std::sync::mpsc;
    use std::thread;

    let mut cmd = Command::new(name);
    cmd.args(args)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());
    if nice_level > 0 {
        crate::platform::process::apply_nice(&mut cmd, nice_level);
    }
    #[cfg(unix)]
    {
        use std::os::unix::process::CommandExt;
        unsafe {
            cmd.pre_exec(|| {
                libc::setpgid(0, 0);
                Ok(())
            });
        }
    }
    crate::platform::process::apply_oom_score_adj(&mut cmd);

    let mut child = cmd.spawn()?;
    let pid = child.id();

    spawn_stdin_writer(child.stdin.take(), stdin_data);

    let (tx, rx) = mpsc::channel();
    thread::spawn(move || {
        let result = child.wait_with_output();
        let _ = tx.send(result);
    });

    match rx.recv_timeout(timeout) {
        Ok(result) => result,
        Err(_) => {
            let grace = std::time::Duration::from_secs(5);
            crate::platform::process::kill_process_tree(pid, grace);
            Err(std::io::Error::new(
                std::io::ErrorKind::TimedOut,
                format!(
                    "{} timed out after {:.0}s",
                    name,
                    timeout.as_secs_f64()
                ),
            ))
        }
    }
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

    /// Run a command with a nice level. Default delegates to `run`.
    fn run_niced(&self, name: &str, args: &[&str], nice_level: i32) -> std::io::Result<Output> {
        let _ = nice_level;
        self.run(name, args)
    }

    /// Run a command with stdin and a nice level. Default delegates to `run_with_stdin`.
    fn run_with_stdin_niced(
        &self,
        stdin_data: &[u8],
        name: &str,
        args: &[&str],
        nice_level: i32,
    ) -> std::io::Result<Output> {
        let _ = nice_level;
        self.run_with_stdin(stdin_data, name, args)
    }

    /// Run a command with a nice level and a per-process timeout. On
    /// expiry the child process group is killed (SIGTERM → grace →
    /// SIGKILL) and the call returns `io::ErrorKind::TimedOut`. Mocks
    /// safely ignore the timeout — it's a correctness feature on the
    /// real runner, not a test axis.
    fn run_niced_timeout(
        &self,
        name: &str,
        args: &[&str],
        nice_level: i32,
        timeout: std::time::Duration,
    ) -> std::io::Result<Output> {
        let _ = timeout;
        self.run_niced(name, args, nice_level)
    }

    /// Run a command with stdin, a nice level, and a per-process timeout.
    fn run_with_stdin_niced_timeout(
        &self,
        stdin_data: &[u8],
        name: &str,
        args: &[&str],
        nice_level: i32,
        timeout: std::time::Duration,
    ) -> std::io::Result<Output> {
        let _ = timeout;
        self.run_with_stdin_niced(stdin_data, name, args, nice_level)
    }
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

    fn run_niced(&self, name: &str, args: &[&str], nice_level: i32) -> std::io::Result<Output> {
        run_command_niced(name, args, nice_level)
    }

    fn run_with_stdin_niced(
        &self,
        stdin_data: &[u8],
        name: &str,
        args: &[&str],
        nice_level: i32,
    ) -> std::io::Result<Output> {
        run_command_with_stdin_niced(name, args, stdin_data, nice_level)
    }

    fn run_niced_timeout(
        &self,
        name: &str,
        args: &[&str],
        nice_level: i32,
        timeout: std::time::Duration,
    ) -> std::io::Result<Output> {
        run_command_niced_timeout(name, args, nice_level, timeout)
    }

    fn run_with_stdin_niced_timeout(
        &self,
        stdin_data: &[u8],
        name: &str,
        args: &[&str],
        nice_level: i32,
        timeout: std::time::Duration,
    ) -> std::io::Result<Output> {
        run_command_with_stdin_niced_timeout(name, args, stdin_data, nice_level, timeout)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    // The stdin writers must tolerate a child that exits without consuming all
    // of stdin: the unread payload closes as a broken pipe on the writer
    // thread, which is swallowed so the caller sees the child's real exit
    // status rather than an io error. (Regression for the `script_exit_nonzero`
    // flake — a script that exits before reading its JSON stdin.)
    #[cfg(unix)]
    #[test]
    fn run_with_stdin_tolerates_child_ignoring_stdin() {
        let out = run_command_with_stdin("true", &[], &vec![b'x'; 200_000]).unwrap();
        assert!(out.status.success());
    }

    // The stdin write and the stdout drain must run concurrently: `cat` echoes
    // a >64KiB payload, which deadlocks on a full pipe unless something drains
    // stdout while we still feed stdin. `wait_with_output` drains stdout/stderr
    // while `spawn_stdin_writer` feeds stdin on its own thread.
    #[cfg(unix)]
    #[test]
    fn run_with_stdin_drains_output_larger_than_pipe_buffer() {
        let payload = vec![b'a'; 512 * 1024];
        let out = run_command_with_stdin("cat", &[], &payload).unwrap();
        assert!(out.status.success());
        assert_eq!(out.stdout.len(), payload.len());
    }

    // Same concurrent-drain guarantee on the kill-capable timeout path (the one
    // the stream_edit pipeline now uses).
    #[cfg(unix)]
    #[test]
    fn run_with_stdin_timeout_drains_output_larger_than_pipe_buffer() {
        let payload = vec![b'b'; 512 * 1024];
        let out = run_command_with_stdin_niced_timeout(
            "cat",
            &[],
            &payload,
            0,
            std::time::Duration::from_secs(10),
        )
        .unwrap();
        assert!(out.status.success());
        assert_eq!(out.stdout.len(), payload.len());
    }

    #[test]
    fn action_display() {
        assert_eq!(Action::None.to_string(), "settled");
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

    #[test]
    fn normalize_for_error_scrubs_created() {
        let mut r = ResourceResult::new("pip", "requests");
        r.action = Action::Created;
        r.changed = true;
        r.duration_ms = 42;
        r.error = Some("pip install failed".into());
        r.normalize_for_error();
        assert_eq!(r.action, Action::None);
        assert!(!r.changed);
        assert_eq!(r.duration_ms, 42, "duration must be preserved");
        assert_eq!(r.error.as_deref(), Some("pip install failed"));
    }

    #[test]
    fn normalize_for_error_scrubs_updated() {
        let mut r = ResourceResult::new("pip", "requests");
        r.action = Action::Updated;
        r.changed = true;
        r.error = Some("upgrade failed".into());
        r.normalize_for_error();
        assert_eq!(r.action, Action::None);
        assert!(!r.changed);
    }

    #[test]
    fn normalize_for_error_scrubs_deleted() {
        let mut r = ResourceResult::new("pip", "requests");
        r.action = Action::Deleted;
        r.changed = true;
        r.error = Some("uninstall failed".into());
        r.normalize_for_error();
        assert_eq!(r.action, Action::None);
        assert!(!r.changed);
    }

    #[test]
    fn normalize_for_error_preserves_blocked() {
        // Blocked + error is legitimate: "diverged but not failed" with
        // diagnostic. Server treats Blocked as a separate convergence axis
        // (see Action::Blocked docs).
        let mut r = ResourceResult::new("package", "nginx");
        r.action = Action::Blocked;
        r.changed = false;
        r.error = Some("apt repo unreachable".into());
        r.normalize_for_error();
        assert_eq!(r.action, Action::Blocked);
        assert!(!r.changed);
        assert_eq!(r.error.as_deref(), Some("apt repo unreachable"));
    }

    #[test]
    fn normalize_for_error_preserves_skipped() {
        let mut r = ResourceResult::new("exec", "task");
        r.action = Action::Skipped;
        r.error = Some("dep failed".into());
        r.normalize_for_error();
        assert_eq!(r.action, Action::Skipped);
    }

    #[test]
    fn normalize_for_error_noop_on_success() {
        let mut r = ResourceResult::new("pip", "requests");
        r.action = Action::Created;
        r.changed = true;
        r.normalize_for_error();
        assert_eq!(r.action, Action::Created, "no error → no scrub");
        assert!(r.changed);
    }

    #[test]
    fn normalize_for_error_noop_on_none_with_error() {
        let mut r = ResourceResult::new("file", "/x");
        r.action = Action::None;
        r.error = Some("validation failed".into());
        r.normalize_for_error();
        assert_eq!(r.action, Action::None);
        assert!(!r.changed);
    }
}
