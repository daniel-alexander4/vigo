// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Cheap liveness sampling for a running child process tree.
//!
//! Answers one question for the foreground-exec heartbeat: is this child
//! actually doing work, or is it wedged? A long `apt-get upgrade` on slow
//! storage and a child stopped by SIGTTIN look identical from the outside —
//! both produce no output and never exit — but they are opposite situations,
//! and the difference is visible in procfs.
//!
//! Everything here is a procfs read: no subprocesses, no allocation beyond a
//! few short strings per sample, per the agent's "prefer sysfs/procfs over
//! subprocesses" rule. Safe to call on a timer while a resource is in flight.
//!
//! Non-Linux platforms have no procfs, so [`sample_tree`] returns `None` and
//! the heartbeat degrades to elapsed-time-only reporting.

/// What a child process tree looked like at one instant.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ProcSample {
    /// Representative state for the tree: `R` or `D` when any member is
    /// running or in uninterruptible disk wait (the tree is working), else `T`
    /// when any member is stopped, else the root's own state.
    ///
    /// `D` is the normal state for a package unpack on slow storage — it means
    /// "working, blocked on the disk", not "hung".
    pub state: char,
    /// Whether any member of the tree is stopped (state `T`).
    pub any_stopped: bool,
    /// `utime + stime + cutime + cstime` summed over the tree, in clock ticks.
    ///
    /// The child times (`cutime`/`cstime`) are included deliberately: they
    /// accumulate only for *reaped* children, so they capture work done by
    /// short-lived grandchildren — dpkg firing off maintainer scripts — that
    /// a snapshot of live processes would miss between two samples.
    pub cpu_ticks: u64,
    /// `rchar + wchar` summed over the tree; 0 when `/proc/<pid>/io` is
    /// unreadable. Counts pipe and file traffic both, which is what makes it a
    /// good "bytes are moving" signal for a download or an unpack.
    pub io_bytes: u64,
    /// `comm` of the deepest descendant — a free "what phase is this in" label
    /// (`http` while apt downloads, `dpkg` while it unpacks). Also the reason
    /// the heartbeat never has to log the command string itself, which keeps
    /// `sensitive: true` commands out of the logs.
    pub phase: Option<String>,
    /// Number of processes sampled in the tree.
    pub procs: usize,
}

impl ProcSample {
    /// True when nothing moved since `prev`: no CPU burned, no bytes read or
    /// written. On its own this is suspicious rather than conclusive — a
    /// process legitimately blocked on a slow network read looks the same — so
    /// callers should require several consecutive idle samples.
    pub fn idle_since(&self, prev: &ProcSample) -> bool {
        self.cpu_ticks == prev.cpu_ticks && self.io_bytes == prev.io_bytes
    }

    /// True when the tree is stopped with nothing else running, which means it
    /// will never finish on its own.
    ///
    /// The cause is almost always SIGTTIN or SIGTTOU — a child in a background
    /// process group touching the controlling terminal — or an external
    /// SIGSTOP. Unlike a stall, this needs no confirmation window: a stopped
    /// tree with no runnable member cannot resume without a SIGCONT.
    pub fn is_wedged(&self) -> bool {
        self.any_stopped && self.state == 'T'
    }

    /// Bytes moved since `prev`, saturating (procfs counters can reset if a
    /// pid is recycled).
    pub fn io_delta(&self, prev: &ProcSample) -> u64 {
        self.io_bytes.saturating_sub(prev.io_bytes)
    }
}

/// Parse the `state` and summed CPU ticks out of a `/proc/<pid>/stat` line.
///
/// Split on the LAST `)` rather than tokenizing from the start: field 2 is the
/// executable name in parentheses and may itself contain spaces and
/// parentheses, which would shift every subsequent field.
fn parse_stat(s: &str) -> Option<(char, u64)> {
    let rest = &s[s.rfind(')')? + 1..];
    let f: Vec<&str> = rest.split_ascii_whitespace().collect();
    // Tokens after the comm start at field 3, so token index = field - 3:
    // state = field 3 (index 0), utime = 14 (11), stime = 15 (12),
    // cutime = 16 (13), cstime = 17 (14).
    let state = f.first()?.chars().next()?;
    let ticks = [11usize, 12, 13, 14]
        .iter()
        .filter_map(|&i| f.get(i))
        .filter_map(|t| t.parse::<u64>().ok())
        .fold(0u64, |acc, v| acc.saturating_add(v));
    Some((state, ticks))
}

/// Sum the `rchar` and `wchar` counters out of a `/proc/<pid>/io` body.
fn parse_io(s: &str) -> u64 {
    let mut total = 0u64;
    for line in s.lines() {
        let val = line
            .strip_prefix("rchar:")
            .or_else(|| line.strip_prefix("wchar:"));
        if let Some(v) = val {
            total = total.saturating_add(v.trim().parse::<u64>().unwrap_or(0));
        }
    }
    total
}

#[cfg(target_os = "linux")]
mod imp {
    use super::{parse_io, parse_stat, ProcSample};
    use std::collections::VecDeque;

    /// Upper bound on processes walked per sample. A convergence child tree is
    /// a handful of processes; this only exists so a fork bomb can't turn the
    /// heartbeat into the expensive thing on the box.
    const MAX_PROCS: usize = 256;

    /// Breadth-first walk of `root` and its descendants, returning
    /// `(pid, depth)` pairs. Level order means the last entry is the deepest,
    /// which is what names the phase.
    fn collect_tree(root: u32, max: usize) -> Vec<(u32, usize)> {
        let mut out = Vec::with_capacity(8);
        let mut queue = VecDeque::new();
        queue.push_back((root, 0usize));
        while let Some((pid, depth)) = queue.pop_front() {
            if out.len() >= max {
                break;
            }
            out.push((pid, depth));
            // Children are listed per-thread, so walk every task dir. Missing
            // or unreadable (no CONFIG_PROC_CHILDREN) just means we sample the
            // root alone — degraded, still useful.
            let Ok(entries) = std::fs::read_dir(format!("/proc/{}/task", pid)) else {
                continue;
            };
            for e in entries.flatten() {
                let Ok(s) = std::fs::read_to_string(e.path().join("children")) else {
                    continue;
                };
                for tok in s.split_ascii_whitespace() {
                    if let Ok(child) = tok.parse::<u32>() {
                        queue.push_back((child, depth + 1));
                    }
                }
            }
        }
        out
    }

    fn read_comm(pid: u32) -> Option<String> {
        std::fs::read_to_string(format!("/proc/{}/comm", pid))
            .ok()
            .map(|s| s.trim().to_string())
            .filter(|s| !s.is_empty())
    }

    pub fn sample_tree(pid: u32) -> Option<ProcSample> {
        let tree = collect_tree(pid, MAX_PROCS);

        let mut cpu_ticks = 0u64;
        let mut io_bytes = 0u64;
        let mut any_stopped = false;
        let mut active_state = None;
        let mut root_state = None;
        let mut deepest: Option<(usize, String)> = None;
        let mut procs = 0usize;

        for &(p, depth) in &tree {
            // A process that exited between the walk and the read is normal,
            // not an error — skip it and keep sampling the rest.
            let Some((state, ticks)) = std::fs::read_to_string(format!("/proc/{}/stat", p))
                .ok()
                .as_deref()
                .and_then(parse_stat)
            else {
                continue;
            };
            procs += 1;
            cpu_ticks = cpu_ticks.saturating_add(ticks);
            if let Ok(io) = std::fs::read_to_string(format!("/proc/{}/io", p)) {
                io_bytes = io_bytes.saturating_add(parse_io(&io));
            }

            if depth == 0 {
                root_state = Some(state);
            }
            match state {
                'T' => any_stopped = true,
                'R' | 'D' => active_state = Some(state),
                _ => {}
            }
            if depth > 0 && deepest.as_ref().is_none_or(|(d, _)| depth > *d) {
                if let Some(c) = read_comm(p) {
                    deepest = Some((depth, c));
                }
            }
        }

        if procs == 0 {
            return None;
        }

        // Working beats stopped: a tree with one stopped member but something
        // else still running is making progress, not wedged.
        let state = active_state
            .or(if any_stopped { Some('T') } else { None })
            .or(root_state)
            .unwrap_or('?');

        Some(ProcSample {
            state,
            any_stopped,
            cpu_ticks,
            io_bytes,
            phase: deepest.map(|(_, c)| c),
            procs,
        })
    }
}

#[cfg(not(target_os = "linux"))]
mod imp {
    use super::ProcSample;
    pub fn sample_tree(_pid: u32) -> Option<ProcSample> {
        None
    }
}

/// Sample a child process tree rooted at `pid`.
///
/// Returns `None` when the tree can't be sampled — every process already
/// exited, or the platform has no procfs.
pub fn sample_tree(pid: u32) -> Option<ProcSample> {
    imp::sample_tree(pid)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn sample(cpu: u64, io: u64, state: char, stopped: bool) -> ProcSample {
        ProcSample {
            state,
            any_stopped: stopped,
            cpu_ticks: cpu,
            io_bytes: io,
            phase: None,
            procs: 1,
        }
    }

    #[test]
    fn parse_stat_reads_state_and_cpu() {
        // pid comm state ppid pgrp session tty tpgid flags minflt cminflt
        // majflt cmajflt utime stime cutime cstime ...
        let line = "6801 (apt-get) T 6797 6797 6671 34819 6672 4194304 \
                    100 200 0 0 11 22 33 44 20 0 1 0 123 0 0";
        let (state, ticks) = parse_stat(line).expect("parses");
        assert_eq!(state, 'T');
        assert_eq!(ticks, 11 + 22 + 33 + 44);
    }

    #[test]
    fn parse_stat_survives_comm_with_spaces_and_parens() {
        // This is the whole reason we split on the last ')' — a naive
        // tokenizer reads the state from inside the comm and every CPU field
        // lands one or more slots off.
        let line = "999 (my (weird) prog) D 1 999 999 0 -1 0 \
                    0 0 0 0 5 6 7 8 20 0 1 0 9 0 0";
        let (state, ticks) = parse_stat(line).expect("parses");
        assert_eq!(state, 'D');
        assert_eq!(ticks, 5 + 6 + 7 + 8);
    }

    #[test]
    fn parse_stat_rejects_garbage() {
        assert!(parse_stat("no parens here").is_none());
    }

    #[test]
    fn parse_io_sums_rchar_and_wchar_only() {
        let body = "rchar: 366616699\n\
                    wchar: 1000\n\
                    syscr: 42\n\
                    read_bytes: 999999\n\
                    write_bytes: 888888\n";
        // read_bytes/write_bytes are deliberately excluded — rchar/wchar catch
        // pipe traffic, which is how a download shows up.
        assert_eq!(parse_io(body), 366616699 + 1000);
    }

    #[test]
    fn parse_io_tolerates_empty_and_malformed() {
        assert_eq!(parse_io(""), 0);
        assert_eq!(parse_io("rchar: banana\n"), 0);
    }

    #[test]
    fn idle_since_detects_no_movement() {
        let a = sample(100, 500, 'S', false);
        let b = sample(100, 500, 'S', false);
        assert!(b.idle_since(&a));
    }

    #[test]
    fn io_movement_alone_counts_as_progress() {
        // The apt download case: CPU flat, bytes climbing. Must not be called
        // idle, or a slow large download gets warned about as a hang.
        let a = sample(100, 500, 'S', false);
        let b = sample(100, 90_000, 'S', false);
        assert!(!b.idle_since(&a));
        assert_eq!(b.io_delta(&a), 89_500);
    }

    #[test]
    fn cpu_movement_alone_counts_as_progress() {
        let a = sample(100, 500, 'R', false);
        let b = sample(140, 500, 'R', false);
        assert!(!b.idle_since(&a));
    }

    #[test]
    fn stopped_tree_is_wedged() {
        assert!(sample(1, 1, 'T', true).is_wedged());
    }

    #[test]
    fn disk_wait_is_not_wedged() {
        // The false positive that matters most: unpacking chromium on an SD
        // card sits in 'D' for minutes and must never be called a hang.
        assert!(!sample(1, 1, 'D', false).is_wedged());
    }

    #[test]
    fn stopped_member_with_a_runnable_sibling_is_not_wedged() {
        assert!(!sample(1, 1, 'D', true).is_wedged());
    }

    #[test]
    fn io_delta_saturates_on_counter_reset() {
        let a = sample(0, 900, 'S', false);
        let b = sample(0, 100, 'S', false);
        assert_eq!(b.io_delta(&a), 0);
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn samples_our_own_process() {
        let s = sample_tree(std::process::id()).expect("own pid is sampleable");
        assert!(s.procs >= 1);
        // We're running, so we must not look wedged.
        assert!(!s.is_wedged());
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn dead_pid_samples_to_none() {
        // pid 0 is never a real userspace process.
        assert!(sample_tree(0).is_none());
    }
}
