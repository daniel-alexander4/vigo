// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Reports vigorepo source-side publishing state.
//!
//! The vigorepo reactor (`agent/src/vigorepo/reactor.rs`) writes a
//! [`TraitsSnapshot`] at the end of every pass. This collector reads
//! that snapshot on each check-in cycle and ships its contents as the
//! `vigorepo` trait so operators can see:
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
//! Trait namespace: `vigorepo`.

use anyhow::Result;
use serde_json::{json, Value};

use crate::platform::paths::default_vigorepo_dir;
use crate::vigorepo::state::TraitsSnapshot;

pub fn collect() -> Result<Value> {
    let snapshot = match TraitsSnapshot::load(&default_vigorepo_dir())? {
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
        "stale_mirrors": snapshot.stale_mirrors,
        "mirrors": snapshot.mirrors,
    }))
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::vigorepo::state::{
        MirrorTraitEntry, PublishErrorEntry, SourceErrorEntry, SourceTraitEntry, TraitsSnapshot,
        TRAITS_SNAPSHOT_VERSION,
    };
    use tempfile::tempdir;

    /// Test helper: write a snapshot to the given base dir and invoke
    /// the collector with the `VIGO_VIGOREPO_DIR` env override. Tests
    /// using this must be annotated `#[serial]` — no, the agent's
    /// test suite does not use `serial_test`. Instead we make each
    /// test self-contained and only inspect the returned `Value`,
    /// not the env var itself.
    fn with_snapshot_dir<F: FnOnce() -> R, R>(dir: &std::path::Path, f: F) -> R {
        // SAFETY: setting env vars is process-wide and races with
        // parallel tests. We unset immediately after the closure,
        // and the collector reads the var only at the top of
        // `collect()`. Parallel runs of other vigorepo tests don't
        // read this var, so the race window is self-contained.
        std::env::set_var("VIGO_VIGOREPO_DIR", dir);
        let result = f();
        std::env::remove_var("VIGO_VIGOREPO_DIR");
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
                label: "vigorepo:host:user:proj".to_string(),
                paused: false,
                max_mirrors: 5,
                tags: vec!["personal".to_string(), "notes".to_string()],
                description: "test".to_string(),
                include_envoys: vec![],
                exclude_envoys: vec![],
                when: String::new(),
                extra_mirrors: vec![],
                git_sha: Some("deadbeef".to_string()),
                blob_sha: Some("cafef00d".to_string()),
                selected_mirrors: vec![],
            }],
            active_sources: 1,
            paused_sources: 0,
            source_errors: vec![SourceErrorEntry {
                path: "/home/dan/vigorepo/bad".to_string(),
                reason: "invalid sidecar".to_string(),
            }],
            publish_errors: vec![PublishErrorEntry {
                label: "vigorepo:h:u:broken".to_string(),
                reason: "bundle exceeds cap".to_string(),
            }],
            stale_mirrors: vec!["vigorepo:host:user:stale".to_string()],
            mirrors: vec![MirrorTraitEntry {
                label: "vigorepo:host:user:proj".to_string(),
                seeder: "host".to_string(),
                user: "user".to_string(),
                project: "proj".to_string(),
                git_sha: "deadbeef".to_string(),
                blob_sha: "cafef00d".to_string(),
                last_refreshed: 1_744_560_000,
                stale: false,
                next_gc_at: 1_744_600_000,
            }],
        };
        snapshot.save(dir.path()).unwrap();

        let v = with_snapshot_dir(dir.path(), collect).unwrap();
        assert_eq!(v["active_sources"], 1);
        assert_eq!(v["paused_sources"], 0);
        assert_eq!(v["updated_at"], 1_744_560_000u64);
        assert_eq!(v["sources"][0]["label"], "vigorepo:host:user:proj");
        assert_eq!(v["sources"][0]["git_sha"], "deadbeef");
        // Sidecar fields flow through to the trait payload so the
        // server-side ClaimTable builder can read them directly.
        assert_eq!(v["sources"][0]["max_mirrors"], 5);
        assert_eq!(v["sources"][0]["tags"][0], "personal");
        assert_eq!(v["sources"][0]["tags"][1], "notes");
        assert_eq!(v["sources"][0]["description"], "test");
        assert_eq!(v["source_errors"][0]["path"], "/home/dan/vigorepo/bad");
        assert_eq!(v["publish_errors"][0]["label"], "vigorepo:h:u:broken");
        // Receiver-side mirrors must flow through — regression guard
        // for the missing-field bug where `mirrors` was populated in
        // the on-disk snapshot but dropped from the wire payload.
        assert_eq!(v["mirrors"][0]["label"], "vigorepo:host:user:proj");
        assert_eq!(v["mirrors"][0]["seeder"], "host");
        assert_eq!(v["mirrors"][0]["git_sha"], "deadbeef");
        assert_eq!(v["mirrors"][0]["last_refreshed"], 1_744_560_000u64);
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
