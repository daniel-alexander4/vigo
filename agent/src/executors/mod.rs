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

/// Nice level for every package/repository/source subprocess. A positive
/// level also engages ionice (`apply_nice` maps it to a best-effort IO class)
/// — invisible-agent doctrine on the most I/O-expensive agent operations.
/// Shared by the pkgmgr backends and the package/repository/source executors.
pub(crate) const PKG_NICE: i32 = 10;

/// Budget for pure queries (dpkg -s, rpm -q, `which`, repo listing). Local
/// package-DB reads; seconds at worst.
pub(crate) const PKG_QUERY_TIMEOUT: std::time::Duration = std::time::Duration::from_secs(60);

/// Budget for mutations (install/remove/upgrade/configure/hold/extract) and
/// network index refreshes (apt-get update, pacman -Sy, zypper refresh, key
/// imports). Deliberately generous — a legitimate upgrade-all on a stale host
/// runs tens of minutes, and killing it mid-transaction is worse than a late
/// cleanup. This is the orphan-cleanup backstop layered UNDER the runner's
/// 300s scope-level detection (which reports the overrun but cannot kill),
/// not the responsiveness bound.
pub(crate) const PKG_MUTATE_TIMEOUT: std::time::Duration = std::time::Duration::from_secs(3600);

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
    let mut cmd = std::process::Command::new(name);
    cmd.args(args);
    run_built_command_niced_timeout(cmd, name, nice_level, timeout)
}

/// Core of [`run_command_niced_timeout`] for callers that need to prepare
/// the `Command` themselves (env vars, cwd): applies nice/ionice (when
/// `nice_level > 0`), own process group, oom-adj, pipes output, and
/// enforces the timeout with a process-tree kill. `display_name` labels
/// the `TimedOut` error.
pub fn run_built_command_niced_timeout(
    mut cmd: std::process::Command,
    display_name: &str,
    nice_level: i32,
    timeout: std::time::Duration,
) -> std::io::Result<Output> {
    use std::process::Stdio;
    use std::sync::mpsc;
    use std::thread;

    // stdin MUST be /dev/null, never inherited. The `pre_exec` below puts this
    // child in its own process group; a process in a *background* process group
    // that reads the controlling terminal is sent SIGTTIN, whose default action
    // is to stop the process — state `T`, forever, holding whatever lock it had
    // taken. `spawn()` inherits stdin by default (unlike `output()`, which nulls
    // it), so without this line every foreground exec launched from a terminal
    // is one stdin-reading maintainer script away from an unkillable-looking
    // hang with no output. Debian maintainer scripts do read stdin directly, so
    // DEBIAN_FRONTEND=noninteractive does not cover this. The background exec
    // path already nulls stdin (see `executors/exec.rs`); this is the same rule
    // for the foreground path.
    cmd.stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());
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

    wait_with_heartbeat(rx, pid, display_name, timeout)
}

/// How often a still-running child is sampled while we wait for it.
///
/// Nothing is emitted before the first tick, so ordinary sub-15s commands stay
/// completely silent — the heartbeat only ever talks about commands that are
/// already slow enough for an operator to wonder about.
#[cfg(not(test))]
const HEARTBEAT_INTERVAL: std::time::Duration = std::time::Duration::from_secs(15);

/// Tests need the tick to fire promptly — the mechanism under test is "what
/// happens on a tick", not the production cadence. Shortening it here keeps the
/// stopped-child test at well under a second instead of 15s+.
#[cfg(test)]
const HEARTBEAT_INTERVAL: std::time::Duration = std::time::Duration::from_millis(100);

/// Consecutive idle heartbeats (no CPU burned, no bytes moved) before a child
/// is called stalled. Four ticks ≈ 60s, long enough that a slow mirror or a big
/// fsync doesn't get reported as a hang.
const STALL_TICKS: u32 = 4;

/// Waits for a child's output, reporting progress while it runs and killing it
/// early if it can provably never finish.
///
/// This exists because "slow" and "hung" are indistinguishable from the outside:
/// the child's stdout is a pipe we don't drain until it exits, so a long
/// `apt-get upgrade` and a wedged process both present as a dead terminal. Each
/// tick samples procfs (see [`crate::platform::procprogress`]) and reports what
/// the tree is actually doing — phase, state, bytes moved — so the operator can
/// tell the two apart without attaching strace.
///
/// Two conditions are called out:
///
/// * **Stopped** (`T`, nothing runnable) — cannot resume without a SIGCONT, so
///   it is killed immediately rather than sitting on the convergence cycle until
///   the timeout expires. With a generous `timeout:` that would otherwise wedge
///   a run for the better part of an hour.
/// * **Stalled** (no CPU, no I/O across [`STALL_TICKS`]) — warned about but not
///   killed: a process blocked on a slow network read looks identical, and the
///   timeout is already the backstop for that.
///
/// The command string is never logged here — only the pid and the phase read
/// out of procfs — so a `sensitive: true` command cannot leak through the
/// heartbeat.
fn wait_with_heartbeat(
    rx: std::sync::mpsc::Receiver<std::io::Result<Output>>,
    pid: u32,
    display_name: &str,
    timeout: std::time::Duration,
) -> std::io::Result<Output> {
    use crate::platform::procprogress;
    use std::io::IsTerminal;
    use std::sync::mpsc::RecvTimeoutError;
    use std::time::Instant;

    // Grace before SIGKILL. Short under test so the kill path doesn't dominate
    // the suite's runtime; 5s in production so a killed child can unwind.
    #[cfg(not(test))]
    let grace = std::time::Duration::from_secs(5);
    #[cfg(test)]
    let grace = std::time::Duration::from_millis(100);

    let started = Instant::now();
    let deadline = started + timeout;
    // One-shot `vigo run` has a terminal and an operator watching it, so
    // progress belongs at info. The daemon has neither — there, routine
    // progress is debug and only the anomalies (below) reach the journal.
    let interactive = std::io::stderr().is_terminal();

    let mut prev: Option<procprogress::ProcSample> = None;
    let mut idle_ticks: u32 = 0;

    loop {
        let now = Instant::now();
        if now >= deadline {
            crate::platform::process::kill_process_tree(pid, grace);
            return Err(std::io::Error::new(
                std::io::ErrorKind::TimedOut,
                format!(
                    "{} timed out after {:.0}s",
                    display_name,
                    timeout.as_secs_f64()
                ),
            ));
        }
        // Never overshoot the deadline: the last slice is whatever is left.
        let slice = HEARTBEAT_INTERVAL.min(deadline - now);

        match rx.recv_timeout(slice) {
            Ok(result) => return result,
            // The reaper thread panicked without sending. Nothing will ever
            // arrive, so don't spin until the deadline.
            Err(RecvTimeoutError::Disconnected) => {
                crate::platform::process::kill_process_tree(pid, grace);
                return Err(std::io::Error::other(format!(
                    "{} wait thread died before reporting an exit status",
                    display_name
                )));
            }
            Err(RecvTimeoutError::Timeout) => {
                let elapsed = started.elapsed().as_secs();
                let sample = procprogress::sample_tree(pid);
                let phase = sample
                    .as_ref()
                    .and_then(|s| s.phase.as_deref())
                    .unwrap_or(display_name);

                // Stopped and nothing runnable: this never recovers on its own.
                if sample.as_ref().is_some_and(|s| s.is_wedged()) {
                    tracing::warn!(
                        pid = pid,
                        command = display_name,
                        phase = phase,
                        elapsed_secs = elapsed,
                        "child process is STOPPED and cannot make progress \
                         (SIGTTIN/SIGTTOU from touching the terminal, or an \
                         external SIGSTOP) — killing it"
                    );
                    crate::platform::process::kill_process_tree(pid, grace);
                    return Err(std::io::Error::other(format!(
                        "{} was stopped after {}s without exiting \
                         (SIGTTIN/SIGTTOU or SIGSTOP); killed",
                        display_name, elapsed
                    )));
                }

                let (state, procs, io_delta) = match (&sample, &prev) {
                    (Some(now_s), Some(before)) => {
                        (now_s.state, now_s.procs, now_s.io_delta(before))
                    }
                    (Some(now_s), None) => (now_s.state, now_s.procs, 0),
                    // No procfs (non-Linux, or the tree just exited): still
                    // report that we're waiting, just without the detail.
                    (None, _) => ('?', 0, 0),
                };

                let idle = match (&sample, &prev) {
                    (Some(now_s), Some(before)) => now_s.idle_since(before),
                    _ => false,
                };
                idle_ticks = if idle { idle_ticks + 1 } else { 0 };

                if idle_ticks >= STALL_TICKS {
                    tracing::warn!(
                        pid = pid,
                        command = display_name,
                        phase = phase,
                        state = %state,
                        elapsed_secs = elapsed,
                        idle_secs = idle_ticks as u64 * HEARTBEAT_INTERVAL.as_secs(),
                        "child has burned no CPU and moved no bytes — may be hung; \
                         letting it run until its timeout"
                    );
                    // Re-arm rather than warning on every subsequent tick.
                    idle_ticks = 0;
                } else if interactive {
                    tracing::info!(
                        pid = pid,
                        command = display_name,
                        phase = phase,
                        state = %state,
                        procs = procs,
                        elapsed_secs = elapsed,
                        io_delta_bytes = io_delta,
                        "still running"
                    );
                } else {
                    tracing::debug!(
                        pid = pid,
                        command = display_name,
                        phase = phase,
                        state = %state,
                        procs = procs,
                        elapsed_secs = elapsed,
                        io_delta_bytes = io_delta,
                        "still running"
                    );
                }

                if sample.is_some() {
                    prev = sample;
                }
            }
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

    // Same heartbeat as the no-stdin path: this one also runs in its own
    // process group, so it is subject to the same stop-forever failure mode if
    // anything in the tree opens /dev/tty directly (piped stdin closes the
    // common route, not that one).
    wait_with_heartbeat(rx, pid, name, timeout)
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

    // A foreground exec must get /dev/null on stdin, never the caller's
    // terminal. `spawn()` inherits stdin by default and the child runs in its
    // own process group, so an inherited terminal means the first child that
    // reads stdin is sent SIGTTIN and stops forever — state `T`, no output, no
    // exit, holding whatever lock it took. `wc -c` reporting 0 bytes is the
    // observable proof that stdin is an immediately-EOF /dev/null.
    #[cfg(unix)]
    #[test]
    fn foreground_exec_gets_null_stdin_not_the_terminal() {
        let out = run_command_niced_timeout(
            "sh",
            &["-c", "wc -c"],
            0,
            std::time::Duration::from_secs(30),
        )
        .expect("command runs");
        assert!(out.status.success());
        let n: u64 = String::from_utf8_lossy(&out.stdout)
            .trim()
            .parse()
            .expect("wc -c prints a count");
        assert_eq!(n, 0, "stdin should be empty /dev/null, got {n} bytes");
    }

    // A child that stops itself can never finish on its own. The heartbeat must
    // notice and kill it rather than letting it sit on the convergence cycle
    // until the timeout — which, with the generous `timeout:` a package upgrade
    // wants, is the difference between a 15s reported failure and an hour of
    // apparent hang. The timeout here is deliberately far longer than the test
    // should take: if the heartbeat misses it, the assertion on elapsed time is
    // what fails, not a hang.
    #[cfg(unix)]
    #[test]
    fn stopped_child_is_detected_and_killed_before_its_timeout() {
        let timeout = std::time::Duration::from_secs(60);
        let started = std::time::Instant::now();
        let err = run_command_niced_timeout("sh", &["-c", "kill -STOP $$; sleep 60"], 0, timeout)
            .expect_err("a stopped child must be reported as an error");
        let elapsed = started.elapsed();

        assert!(
            err.to_string().contains("stopped"),
            "error should name the stop as the cause, got: {err}"
        );
        assert!(
            elapsed < std::time::Duration::from_secs(30),
            "should be killed on a heartbeat tick, not at the {}s timeout (took {:?})",
            timeout.as_secs(),
            elapsed
        );
    }

    // The counterpart to the test above: a slow-but-working child must be left
    // alone. This is the false positive that would matter most in production —
    // killing a legitimate long `apt-get upgrade` mid-transaction is far worse
    // than the hang we set out to fix. The command sleeps through many heartbeat
    // ticks while writing, and must still exit on its own terms.
    #[cfg(unix)]
    #[test]
    fn slow_but_working_child_is_not_killed() {
        let out = run_command_niced_timeout(
            "sh",
            &["-c", "for i in 1 2 3 4 5; do echo tick; sleep 0.2; done"],
            0,
            std::time::Duration::from_secs(60),
        )
        .expect("a slow child must be allowed to finish");
        assert!(out.status.success());
        assert_eq!(String::from_utf8_lossy(&out.stdout).lines().count(), 5);
    }

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
