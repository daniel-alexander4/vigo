// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Reports vigogit source-side publishing state.
//!
//! The vigogit reactor (`agent/src/vigogit/reactor.rs`) writes a
//! [`TraitsSnapshot`] at the end of every pass. This collector reads
//! that snapshot on each check-in cycle and ships its contents as the
//! `vigogit` trait so operators can see:
//!
//! - How many sources are actively publishing (and how many are paused)
//! - Per-source label, current `git_sha`, and `blob_sha`
//! - Discovery errors (invalid sidecars, missing `.git/`, etc.)
//! - Publish errors (bundle creation failures, oversize, etc.)
//!
//! If the reactor has never ticked (cold start before the first
//! interval fires, or git version check failed so the reactor never
//! started), the snapshot file does not exist and this collector
//! returns `Value::Null`, matching the pattern used by
//! `swarm_seeds::collect` for an empty state.
//!
//! Trait namespace: `vigogit`.

use anyhow::Result;
use serde_json::{json, Value};

use crate::platform::paths::default_vigogit_dir;
use crate::vigogit::state::TraitsSnapshot;

pub fn collect() -> Result<Value> {
    let snapshot = match TraitsSnapshot::load(&default_vigogit_dir())? {
        Some(s) => s,
        None => return Ok(Value::Null),
    };

    // Build the trait payload from the snapshot. Every field is
    // passed through as-is; the reactor is the only writer so the
    // schema here matches `TraitsSnapshot` exactly.
    Ok(json!({
        "updated_at": snapshot.updated_at,
        "active_sources": snapshot.active_sources,
        "paused_sources": snapshot.paused_sources,
        "sources": snapshot.sources,
        "source_errors": snapshot.source_errors,
        "publish_errors": snapshot.publish_errors,
    }))
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::vigogit::state::{
        PublishErrorEntry, SourceErrorEntry, SourceTraitEntry, TraitsSnapshot,
        TRAITS_SNAPSHOT_VERSION,
    };
    use tempfile::tempdir;

    /// Test helper: write a snapshot to the given base dir and invoke
    /// the collector with the `VIGO_VIGOGIT_DIR` env override. Tests
    /// using this must be annotated `#[serial]` — no, the agent's
    /// test suite does not use `serial_test`. Instead we make each
    /// test self-contained and only inspect the returned `Value`,
    /// not the env var itself.
    fn with_snapshot_dir<F: FnOnce() -> R, R>(dir: &std::path::Path, f: F) -> R {
        // SAFETY: setting env vars is process-wide and races with
        // parallel tests. We unset immediately after the closure,
        // and the collector reads the var only at the top of
        // `collect()`. Parallel runs of other vigogit tests don't
        // read this var, so the race window is self-contained.
        std::env::set_var("VIGO_VIGOGIT_DIR", dir);
        let result = f();
        std::env::remove_var("VIGO_VIGOGIT_DIR");
        result
    }

    #[test]
    fn collect_returns_null_when_no_snapshot() {
        let dir = tempdir().unwrap();
        let v = with_snapshot_dir(dir.path(), collect).unwrap();
        assert!(v.is_null());
    }

    #[test]
    fn collect_returns_snapshot_contents() {
        let dir = tempdir().unwrap();

        let snapshot = TraitsSnapshot {
            version: TRAITS_SNAPSHOT_VERSION,
            updated_at: 1_744_560_000,
            sources: vec![SourceTraitEntry {
                label: "vigogit:host:user:proj".to_string(),
                paused: false,
                git_sha: Some("deadbeef".to_string()),
                blob_sha: Some("cafef00d".to_string()),
            }],
            active_sources: 1,
            paused_sources: 0,
            source_errors: vec![SourceErrorEntry {
                path: "/home/dan/vigogit/bad".to_string(),
                reason: "invalid sidecar".to_string(),
            }],
            publish_errors: vec![PublishErrorEntry {
                label: "vigogit:h:u:broken".to_string(),
                reason: "bundle exceeds cap".to_string(),
            }],
        };
        snapshot.save(dir.path()).unwrap();

        let v = with_snapshot_dir(dir.path(), collect).unwrap();
        assert_eq!(v["active_sources"], 1);
        assert_eq!(v["paused_sources"], 0);
        assert_eq!(v["updated_at"], 1_744_560_000u64);
        assert_eq!(v["sources"][0]["label"], "vigogit:host:user:proj");
        assert_eq!(v["sources"][0]["git_sha"], "deadbeef");
        assert_eq!(v["source_errors"][0]["path"], "/home/dan/vigogit/bad");
        assert_eq!(v["publish_errors"][0]["label"], "vigogit:h:u:broken");
    }

    #[test]
    fn collect_handles_corrupt_snapshot_as_null() {
        let dir = tempdir().unwrap();
        // Write garbage where the snapshot file would go.
        std::fs::write(dir.path().join("traits.json"), b"not json").unwrap();
        let v = with_snapshot_dir(dir.path(), collect).unwrap();
        assert!(v.is_null());
    }
}
