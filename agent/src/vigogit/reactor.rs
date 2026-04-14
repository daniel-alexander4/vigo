// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Vigogit source-side reactor loop.
//!
//! The reactor is a single tokio task that wakes every
//! [`TICK_INTERVAL`] seconds and runs one "pass":
//!
//! 1. Re-scan every user's `~/vigogit/*` via
//!    [`super::source::scan_local`].
//! 2. Load `state.json`.
//! 3. Call [`super::publisher::publish_source`] on every discovered
//!    [`Source`] — which is a no-op `git rev-parse HEAD` check when
//!    `state.git_sha == HEAD`, so steady-state ticks are cheap.
//!
//! ### Why polling instead of inotify
//!
//! The original plan called for an inotify ref-watcher. Polling won
//! on correctness: `git commit` may write to `refs/heads/<branch>`
//! directly *or* pack refs into `packed-refs`, and HEAD / reflog /
//! tempfile+rename all need coverage. On a personal-DR subsystem
//! with a handful of repos per host, a 5-second poll over
//! `git rev-parse HEAD` (~5 ms per call) is strictly simpler and
//! correct by construction. The locked-in 5-second debounce falls
//! out automatically: whatever HEAD is at tick time is what gets
//! published, and multiple commits inside the window collapse into
//! one publish.
//!
//! ### Edge-triggered logging
//!
//! Steady-state ticks are silent — a pass where every source is
//! `UpToDate` logs nothing. Discovery errors and publish failures
//! are tracked across ticks in [`PassState`] and only logged on the
//! edge (first tick where an error appears; follow-up tick where it
//! clears). This keeps a persistent failure from filling the log
//! every 5 seconds.

use std::collections::HashMap;
#[cfg(test)]
use std::collections::HashSet;
use std::path::PathBuf;
use std::time::Duration;

use tokio::time::MissedTickBehavior;
use tokio_util::sync::CancellationToken;
use tracing::{debug, info, warn};

use super::publisher::{self, Outcome, PublishContext, PublishError};
use super::source;
use super::state::{
    unix_seconds_now, PublishErrorEntry, SourceErrorEntry, SourceTraitEntry, State, TraitsSnapshot,
};

/// How often the reactor runs a full pass. 5 seconds matches the
/// locked-in debounce window: multiple commits within this window
/// collapse into a single publish.
pub const TICK_INTERVAL: Duration = Duration::from_secs(5);

/// Spawn the reactor as a tokio task. Returns immediately; the task
/// lives until `cancel` fires.
pub fn spawn(cancel: CancellationToken) {
    tokio::spawn(run(cancel));
}

async fn run(cancel: CancellationToken) {
    let hostname = std::fs::read_to_string("/etc/hostname")
        .unwrap_or_else(|_| "unknown".to_string())
        .trim()
        .to_string();
    info!(
        hostname = %hostname,
        tick_secs = TICK_INTERVAL.as_secs(),
        "vigogit: reactor started"
    );

    let mut pass_state = PassState::new(hostname);
    let mut tick = tokio::time::interval(TICK_INTERVAL);
    tick.set_missed_tick_behavior(MissedTickBehavior::Delay);

    loop {
        tokio::select! {
            biased;

            _ = cancel.cancelled() => {
                info!("vigogit: reactor shutting down");
                return;
            }

            _ = tick.tick() => {
                let state = std::mem::take(&mut pass_state);
                // Publisher does filesystem I/O and spawns git
                // subprocesses — run it on a blocking worker so we
                // don't stall the async runtime.
                let joined = tokio::task::spawn_blocking(move || {
                    let mut s = state;
                    let summary = s.run_pass();
                    (s, summary)
                })
                .await;
                match joined {
                    Ok((s, summary)) => {
                        pass_state = s;
                        summary.log();
                    }
                    Err(e) => {
                        warn!(error = %e, "vigogit: reactor pass panicked");
                        pass_state = PassState::new_lost();
                    }
                }
            }
        }
    }
}

/// Persistent state carried across reactor ticks so we can
/// edge-trigger log messages instead of spamming every tick.
#[derive(Default)]
pub struct PassState {
    hostname: String,
    /// Paths whose most recent discovery attempt produced an error.
    /// Map value is the reason string, so we can detect reason
    /// changes (same path, different error) as a new edge.
    prev_discovery_errors: HashMap<PathBuf, String>,
    /// Labels whose most recent publish attempt failed. Map value is
    /// the error string for the same reason-change detection.
    prev_publish_errors: HashMap<String, String>,
}

impl PassState {
    pub fn new(hostname: String) -> Self {
        Self {
            hostname,
            prev_discovery_errors: HashMap::new(),
            prev_publish_errors: HashMap::new(),
        }
    }

    /// Construct a fresh state after a tick worker panic. Loses edge
    /// history so the next tick may re-log persistent errors once —
    /// acceptable, and panics are already a warning.
    fn new_lost() -> Self {
        Self::default()
    }

    /// Run one pass: scan, publish, update the edge-trigger maps,
    /// return a summary for the reactor loop to log.
    pub fn run_pass(&mut self) -> PassSummary {
        if self.hostname.is_empty() {
            // Lost-state recovery path — re-read hostname. This only
            // happens after a tick worker panic.
            self.hostname = std::fs::read_to_string("/etc/hostname")
                .unwrap_or_else(|_| "unknown".to_string())
                .trim()
                .to_string();
        }

        let scan = source::scan_local(&self.hostname);

        self.log_discovery_error_edges(&scan.errors);

        let ctx = PublishContext::from_defaults();

        let mut summary = PassSummary {
            sources: scan.sources.len(),
            ..PassSummary::default()
        };

        if scan.sources.is_empty() {
            // Still write a snapshot so the trait collector can
            // report an honest "no sources discovered" state with a
            // fresh timestamp, instead of showing stale data from a
            // previous run.
            write_traits_snapshot(
                &ctx.state_dir,
                &scan.errors,
                &HashMap::new(),
                &[],
                &State::default(),
                &summary,
            );
            return summary;
        }

        let mut state = State::load(&ctx.state_dir);

        let mut current_publish_errors: HashMap<String, String> = HashMap::new();

        for src in &scan.sources {
            match publisher::publish_source(src, &mut state, &ctx) {
                Ok(Outcome::Paused { .. }) => summary.paused += 1,
                Ok(Outcome::UpToDate { .. }) => summary.up_to_date += 1,
                Ok(Outcome::Published { full, .. }) => {
                    summary.published += 1;
                    if full {
                        summary.full_bundles += 1;
                    }
                }
                Err(e) => {
                    summary.failed += 1;
                    let label = publish_error_label(&e);
                    current_publish_errors.insert(label.to_string(), e.to_string());
                }
            }
        }

        self.log_publish_error_edges(&current_publish_errors);

        write_traits_snapshot(
            &ctx.state_dir,
            &scan.errors,
            &current_publish_errors,
            &scan.sources,
            &state,
            &summary,
        );

        self.prev_publish_errors = current_publish_errors;

        summary
    }

    fn log_discovery_error_edges(&mut self, errors: &[source::SourceError]) {
        let current: HashMap<PathBuf, String> = errors
            .iter()
            .map(|e| (e.path.clone(), e.reason.clone()))
            .collect();

        // New or changed errors.
        for (path, reason) in &current {
            match self.prev_discovery_errors.get(path) {
                Some(prev_reason) if prev_reason == reason => {}
                _ => warn!(
                    path = %path.display(),
                    reason = %reason,
                    "vigogit: source discovery error"
                ),
            }
        }

        // Errors that cleared.
        for path in self.prev_discovery_errors.keys() {
            if !current.contains_key(path) {
                info!(
                    path = %path.display(),
                    "vigogit: source discovery error cleared"
                );
            }
        }

        self.prev_discovery_errors = current;
    }

    fn log_publish_error_edges(&self, current: &HashMap<String, String>) {
        for (label, reason) in current {
            match self.prev_publish_errors.get(label) {
                Some(prev) if prev == reason => {}
                _ => warn!(
                    label = %label,
                    error = %reason,
                    "vigogit: source publish failed"
                ),
            }
        }

        for label in self.prev_publish_errors.keys() {
            if !current.contains_key(label) {
                info!(label = %label, "vigogit: source publish recovered");
            }
        }
    }
}

/// Build a [`TraitsSnapshot`] reflecting the current pass and save
/// it atomically. Failure to save is logged but never propagated —
/// a write error must not stall the reactor or the publisher, and
/// the next successful pass will overwrite a stale snapshot anyway.
fn write_traits_snapshot(
    state_dir: &std::path::Path,
    discovery_errors: &[source::SourceError],
    publish_errors: &HashMap<String, String>,
    sources: &[source::Source],
    state: &State,
    summary: &PassSummary,
) {
    let source_entries: Vec<SourceTraitEntry> = sources
        .iter()
        .map(|src| {
            let entry = state.labels.get(&src.label);
            SourceTraitEntry {
                label: src.label.clone(),
                paused: src.sidecar.paused,
                git_sha: entry.map(|s| s.git_sha.clone()),
                blob_sha: entry.map(|s| s.blob_sha.clone()),
            }
        })
        .collect();

    let source_error_entries: Vec<SourceErrorEntry> = discovery_errors
        .iter()
        .map(|e| SourceErrorEntry {
            path: e.path.display().to_string(),
            reason: e.reason.clone(),
        })
        .collect();

    let publish_error_entries: Vec<PublishErrorEntry> = publish_errors
        .iter()
        .map(|(label, reason)| PublishErrorEntry {
            label: label.clone(),
            reason: reason.clone(),
        })
        .collect();

    let snapshot = TraitsSnapshot {
        version: super::state::TRAITS_SNAPSHOT_VERSION,
        updated_at: unix_seconds_now(),
        active_sources: summary.sources.saturating_sub(summary.paused),
        paused_sources: summary.paused,
        sources: source_entries,
        source_errors: source_error_entries,
        publish_errors: publish_error_entries,
    };

    if let Err(e) = snapshot.save(state_dir) {
        warn!(
            error = %e,
            dir = %state_dir.display(),
            "vigogit: failed to save traits snapshot; trait collector will report stale data"
        );
    }
}

/// Extract a label from a [`PublishError`] for edge-trigger tracking.
/// Every variant currently carries a label; this function exists so
/// that if new variants are added later, the match is exhaustive and
/// the compiler will force the caller to decide how to key them.
fn publish_error_label(e: &PublishError) -> &str {
    match e {
        PublishError::PathReCheck { label, .. }
        | PublishError::HeadParse { label, .. }
        | PublishError::BundleCreate { label, .. }
        | PublishError::OversizedBundle { label, .. }
        | PublishError::Io { label, .. } => label,
    }
}

/// One-tick summary passed from the blocking worker back to the
/// async reactor for logging.
#[derive(Debug, Default, Clone, PartialEq, Eq)]
pub struct PassSummary {
    pub sources: usize,
    pub published: usize,
    pub full_bundles: usize,
    pub up_to_date: usize,
    pub paused: usize,
    pub failed: usize,
}

impl PassSummary {
    /// Edge-triggered summary logging. A steady-state "every source
    /// up to date" pass logs nothing; only ticks that actually
    /// published or failed produce a line.
    pub fn log(&self) {
        if self.published == 0 && self.failed == 0 {
            // Quiet tick — debug-level in case someone turns up
            // tracing for diagnostics.
            debug!(
                sources = self.sources,
                up_to_date = self.up_to_date,
                paused = self.paused,
                "vigogit: reactor pass (quiet)"
            );
            return;
        }
        info!(
            sources = self.sources,
            published = self.published,
            full_bundles = self.full_bundles,
            up_to_date = self.up_to_date,
            paused = self.paused,
            failed = self.failed,
            "vigogit: reactor pass"
        );
    }

    /// Total non-quiet entries — handy for tests.
    pub fn total_visible_activity(&self) -> usize {
        self.published + self.failed
    }
}

// --- internal helpers used by tests below ---------------------------

/// For tests: compute the set of keys added between two maps.
#[cfg(test)]
fn added<K: Eq + std::hash::Hash + Clone, V>(
    prev: &HashMap<K, V>,
    cur: &HashMap<K, V>,
) -> HashSet<K> {
    cur.keys()
        .filter(|k| !prev.contains_key(*k))
        .cloned()
        .collect()
}

/// For tests: compute the set of keys removed between two maps.
#[cfg(test)]
fn removed<K: Eq + std::hash::Hash + Clone, V>(
    prev: &HashMap<K, V>,
    cur: &HashMap<K, V>,
) -> HashSet<K> {
    prev.keys()
        .filter(|k| !cur.contains_key(*k))
        .cloned()
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn pass_summary_quiet_when_no_activity() {
        let s = PassSummary {
            sources: 3,
            up_to_date: 3,
            ..Default::default()
        };
        assert_eq!(s.total_visible_activity(), 0);
    }

    #[test]
    fn pass_summary_visible_when_published() {
        let s = PassSummary {
            sources: 2,
            published: 1,
            up_to_date: 1,
            ..Default::default()
        };
        assert_eq!(s.total_visible_activity(), 1);
    }

    #[test]
    fn pass_summary_visible_when_failed() {
        let s = PassSummary {
            sources: 1,
            failed: 1,
            ..Default::default()
        };
        assert_eq!(s.total_visible_activity(), 1);
    }

    #[test]
    fn publish_error_label_extracts_from_every_variant() {
        let e1 = PublishError::PathReCheck {
            label: "l1".to_string(),
            reason: "r".to_string(),
        };
        let e2 = PublishError::HeadParse {
            label: "l2".to_string(),
            reason: "r".to_string(),
        };
        let e3 = PublishError::BundleCreate {
            label: "l3".to_string(),
            reason: "r".to_string(),
        };
        let e4 = PublishError::OversizedBundle {
            label: "l4".to_string(),
            size: 1,
            limit: 1,
        };
        let e5 = PublishError::Io {
            label: "l5".to_string(),
            source: std::io::Error::other("x"),
        };
        assert_eq!(publish_error_label(&e1), "l1");
        assert_eq!(publish_error_label(&e2), "l2");
        assert_eq!(publish_error_label(&e3), "l3");
        assert_eq!(publish_error_label(&e4), "l4");
        assert_eq!(publish_error_label(&e5), "l5");
    }

    #[test]
    fn added_and_removed_helpers_compute_symmetric_difference() {
        let mut prev: HashMap<String, ()> = HashMap::new();
        prev.insert("a".to_string(), ());
        prev.insert("b".to_string(), ());

        let mut cur: HashMap<String, ()> = HashMap::new();
        cur.insert("b".to_string(), ());
        cur.insert("c".to_string(), ());

        let added_set = added(&prev, &cur);
        let removed_set = removed(&prev, &cur);

        assert_eq!(added_set, HashSet::from(["c".to_string()]));
        assert_eq!(removed_set, HashSet::from(["a".to_string()]));
    }

    #[test]
    fn pass_state_discovery_error_edges_log_new_and_cleared() {
        // Smoke-test: construct a PassState, feed it a discovery
        // error list, then an empty list, and verify the internal
        // map updates correctly. (We can't capture tracing output
        // here without a subscriber, but we can at least prove the
        // bookkeeping is right.)
        let mut s = PassState::new("danlap".to_string());

        let err1 = source::SourceError {
            path: PathBuf::from("/home/dan/vigogit/a"),
            reason: "boom".to_string(),
        };
        s.log_discovery_error_edges(&[err1]);
        assert_eq!(s.prev_discovery_errors.len(), 1);
        assert_eq!(
            s.prev_discovery_errors
                .get(&PathBuf::from("/home/dan/vigogit/a"))
                .unwrap(),
            "boom"
        );

        s.log_discovery_error_edges(&[]);
        assert!(s.prev_discovery_errors.is_empty());
    }

    #[test]
    fn pass_state_discovery_error_reason_change_counts_as_edge() {
        let mut s = PassState::new("danlap".to_string());

        let err1 = source::SourceError {
            path: PathBuf::from("/home/dan/vigogit/a"),
            reason: "boom".to_string(),
        };
        s.log_discovery_error_edges(&[err1]);

        let err2 = source::SourceError {
            path: PathBuf::from("/home/dan/vigogit/a"),
            reason: "different".to_string(),
        };
        s.log_discovery_error_edges(&[err2]);
        // The new reason is now the stored one.
        assert_eq!(
            s.prev_discovery_errors
                .get(&PathBuf::from("/home/dan/vigogit/a"))
                .unwrap(),
            "different"
        );
    }

    #[test]
    fn pass_state_publish_error_edges_track_current() {
        let s = PassState::new("danlap".to_string());
        let mut current: HashMap<String, String> = HashMap::new();
        current.insert("vigogit:h:u:p".to_string(), "err-a".to_string());
        // Just exercises the function — no panic, no output capture.
        s.log_publish_error_edges(&current);
    }
}
