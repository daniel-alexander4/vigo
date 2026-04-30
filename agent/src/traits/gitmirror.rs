// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Reports gitmirror state for the `gitmirror` trait namespace.
//!
//! **Transitional during the ADR-015 cutover.** Pre-ADR-015 the
//! gitmirror reactor wrote a [`TraitsSnapshot`] at the end of every
//! pass and this collector shipped its contents on each check-in.
//! Step 3 of the ADR-015 arc removed the publish-and-claim reactor;
//! the new helper-daemon-driven snapshot writer lands in step 4.
//! Until then, the legacy snapshot file is never written, so this
//! collector returns `Value::Null` in production — same fail-soft
//! behaviour as `swarm_seeds::collect` on an empty state.
//!
//! The fixture-driven tests below still exercise the legacy schema so
//! consumers that still consume `gitmirror.sources[]` /
//! `gitmirror.mirrors[]` keep passing through the cutover. Step 4
//! re-emits the trait against the unified-substrate
//! `~/.vigo-gitmirror/<project_id>/` state surface.
//!
//! Trait namespace: `gitmirror`.

use anyhow::Result;
use serde_json::{json, Value};

use crate::platform::paths::default_gitmirror_dir;
use crate::swarm::gitmirror::state::TraitsSnapshot;

pub fn collect() -> Result<Value> {
    let snapshot = match TraitsSnapshot::load(&default_gitmirror_dir())? {
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
    use crate::swarm::gitmirror::state::{
        MirrorTraitEntry, PublishErrorEntry, SourceErrorEntry, SourceTraitEntry, TraitsSnapshot,
        TRAITS_SNAPSHOT_VERSION,
    };
    use tempfile::tempdir;

    /// Test helper: write a snapshot to the given base dir and invoke
    /// the collector with the `VIGO_GITMIRROR_DIR` env override. Tests
    /// using this must be annotated `#[serial]` — no, the agent's
    /// test suite does not use `serial_test`. Instead we make each
    /// test self-contained and only inspect the returned `Value`,
    /// not the env var itself.
    fn with_snapshot_dir<F: FnOnce() -> R, R>(dir: &std::path::Path, f: F) -> R {
        // SAFETY: setting env vars is process-wide and races with
        // parallel tests. We unset immediately after the closure,
        // and the collector reads the var only at the top of
        // `collect()`. Parallel runs of other gitmirror tests don't
        // read this var, so the race window is self-contained.
        std::env::set_var("VIGO_GITMIRROR_DIR", dir);
        let result = f();
        std::env::remove_var("VIGO_GITMIRROR_DIR");
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
                label: "gitmirror:host:user:proj".to_string(),
                paused: false,
                max_mirrors: 5,
                tags: vec!["personal".to_string(), "notes".to_string()],
                description: "test".to_string(),
                include_envoys: vec![],
                exclude_envoys: vec![],
                when: String::new(),
                cross_subnet_mirrors: vec![],
                git_sha: Some("deadbeef".to_string()),
                blob_sha: Some("cafef00d".to_string()),
                last_publish_full: Some(true),
                last_bundle_size: Some(8192),
                selected_mirrors: vec![],
            }],
            active_sources: 1,
            paused_sources: 0,
            source_errors: vec![SourceErrorEntry {
                path: "/home/dan/gitmirror/bad".to_string(),
                reason: "invalid sidecar".to_string(),
            }],
            publish_errors: vec![PublishErrorEntry {
                label: "gitmirror:h:u:broken".to_string(),
                reason: "bundle exceeds cap".to_string(),
            }],
            stale_mirrors: vec!["gitmirror:host:user:stale".to_string()],
            mirrors: vec![MirrorTraitEntry {
                label: "gitmirror:host:user:proj".to_string(),
                seeder: "host".to_string(),
                user: "user".to_string(),
                project: "proj".to_string(),
                git_sha: "deadbeef".to_string(),
                blob_sha: "cafef00d".to_string(),
                last_refreshed: 1_744_560_000,
                stale: false,
                next_gc_at: 1_744_600_000,
                size_bytes: 4_096,
            }],
        };
        snapshot.save(dir.path()).unwrap();

        let v = with_snapshot_dir(dir.path(), collect).unwrap();
        assert_eq!(v["active_sources"], 1);
        assert_eq!(v["paused_sources"], 0);
        assert_eq!(v["updated_at"], 1_744_560_000u64);
        assert_eq!(v["sources"][0]["label"], "gitmirror:host:user:proj");
        assert_eq!(v["sources"][0]["git_sha"], "deadbeef");
        // Sidecar fields flow through to the trait payload so the
        // server-side ClaimTable builder can read them directly.
        assert_eq!(v["sources"][0]["max_mirrors"], 5);
        assert_eq!(v["sources"][0]["tags"][0], "personal");
        assert_eq!(v["sources"][0]["tags"][1], "notes");
        assert_eq!(v["sources"][0]["description"], "test");
        assert_eq!(v["source_errors"][0]["path"], "/home/dan/gitmirror/bad");
        assert_eq!(v["publish_errors"][0]["label"], "gitmirror:h:u:broken");
        // Receiver-side mirrors must flow through — regression guard
        // for the missing-field bug where `mirrors` was populated in
        // the on-disk snapshot but dropped from the wire payload.
        assert_eq!(v["mirrors"][0]["label"], "gitmirror:host:user:proj");
        assert_eq!(v["mirrors"][0]["seeder"], "host");
        assert_eq!(v["mirrors"][0]["git_sha"], "deadbeef");
        assert_eq!(v["mirrors"][0]["last_refreshed"], 1_744_560_000u64);
        assert_eq!(v["mirrors"][0]["size_bytes"], 4_096u64);
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
