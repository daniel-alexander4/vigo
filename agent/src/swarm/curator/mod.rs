// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! curator — the fifth swarm content subsystem: a P2P artifact registry.
//!
//! filecast's content-addressed blob transport + the [`crate::swarm::anchor`]
//! layer's puddle-keyed identity/access machinery (capability tokens,
//! ADR-019 delegation envelopes, the SO_PEERCRED helper-daemon helper,
//! shared with gitback) + a thin catalog layer (ADR-024). An *artifact*
//! is keyed `artifact_id = sha256(founder_puddle_pubkey ‖ name)` (same shape as
//! a gitback `project_id`, so Alice's `foo` ≠ Bob's `foo`); the human-facing
//! reference is `<founder-puddle-name>/<name>` (the puddle name resolves via the
//! ADR-022 fleet name map). Each artifact has one signed [`catalog::CatalogEntry`]
//! — versions × platforms + a mutable tag map — re-signed last-write-wins on every
//! mutation; the signature is the "writable only by puddle members" enforcement.
//!
//! ### Module layout
//!
//! - [`catalog`] — the signed-entry data model + canonical bytes + anchor-rooted
//!   verification (slice 1).
//! - [`state`] — the per-user on-disk layout (`~/.vigo-curator/<artifact_id>/`)
//!   shared by the helper daemon, the reactor below, and the trait collector
//!   ([`crate::traits::curator`]).
//! - [`helper`] — the root helper daemon at `/var/run/vigo/curator.sock`: the
//!   privileged half of the publish path (validate the signed entry, stage the
//!   blob into the swarm cache, record a `Manifest` entry with the artifact's
//!   `target`, drop the entry as a `*.pending` sentinel).
//! - [`run_sentinel_reactor`] — the root reactor that re-verifies each
//!   `entry.json.pending` and promotes it (last-write-wins on `issued_at`) to the
//!   canonical `entry.json` the `swarm_curator` trait collector reads.
//! - [`catalog_cache`] — the agent-side state under `/var/lib/vigo/artifacts/`:
//!   `catalog.json` (the fleet catalog the server attaches to every check-in,
//!   re-cached on each accepted bundle, so resolution survives a server outage)
//!   and `cache.json` (the materialization index).
//! - [`resolver`] — resolve a `<artifact_id>` ref → blob (server-mediated, with
//!   a cached-catalog fallback) → fetch the bytes (local DR replica or the
//!   server's `/blob` relay) → verify the sha → materialize under
//!   `artifacts/<artifact_id>/<name>/<version>/<os>-<arch>` + update `cache.json`.
//!
//! Slices 1-2 ship the catalog + the publish path + config plumbing; slice 3 the
//! server REST surface (`resolve`/`blob`/`fleet`/`block`) + the catalog feed into
//! the manifest store + the agent-side catalog cache + resolver/materializer; the
//! `vigocli`/`vigo` CLI surfaces are slice 4 (and the resolver's first callers),
//! the `/curator` UI + docs slice 5 (see ADR-024). Slice 2 deliberately does
//! **not** gossip catalog entries peer-to-peer (the propagation path is
//! `entry.json` → `swarm_curator` trait → check-in → `curatormesh` aggregator →
//! the agent catalog cache, sourced from the server like the ADR-022 name map —
//! see `pending.md`).
//!
//! `catalog.rs` imports [`crate::swarm::anchor::envelopes::DelegationEnvelope`]
//! (the ADR-019 rekey-bridge envelope) and `helper.rs` imports
//! [`crate::swarm::anchor::peer_uid::peer_uid`] (SO_PEERCRED) directly from the
//! anchor layer. The extraction landed at 0.51.7 (see [`crate::swarm::anchor`]).

pub mod catalog;
pub mod catalog_cache;
pub mod cli;
pub mod executor_resolver;
pub mod helper;
pub mod registry;
pub mod repobuild;
pub mod resolver;
pub mod state;

use std::path::Path;

use tokio_util::sync::CancellationToken;
use tracing::{debug, info, warn};

use crate::swarm::anchor::envelopes::DELEGATION_MAX_LEN;
use crate::swarm::manifest::Manifest;

/// Per-blob (one `{name, version, os, arch}` artifact) size cap when the
/// operator hasn't published `swarm.curator.max_bytes` yet, or left it at
/// 0/unset — 2 GiB, matching the post-0.49.3 gitback bundle default
/// ([`crate::swarm::gitback::DEFAULT_MAX_BUNDLE_SIZE`]). Read at the publish
/// gate via [`crate::swarm::runtime_limits::curator_max_bytes`]; raise the
/// server-side knob for large container images.
pub const DEFAULT_MAX_BYTES: u64 = 2 * 1024 * 1024 * 1024;

/// How often the sentinel reactor sweeps every user's `~/.vigo-curator/` for
/// `entry.json.pending` files. 60 s — a freshly-published artifact's entry lands
/// in `entry.json` (and so in the next `swarm_curator` trait report) within one
/// tick, while idle envoys spend only a few `read_dir` calls per minute.
const SENTINEL_SCAN_INTERVAL: std::time::Duration = std::time::Duration::from_secs(60);

/// Spawn the curator subsystem — three concurrent tasks under the cancellation token:
///
/// - **Helper daemon** ([`helper::run`]) — listens on `/var/run/vigo/curator.sock`; validates a re-signed [`catalog::CatalogEntry`] + the blob bytes pushed by a `curator: true` user, stages the blob into the swarm cache, records a `Manifest` entry with the artifact's `target`, and drops the entry as `~<user>/.vigo-curator/<artifact_id>/entry.json.pending`.
/// - **Sentinel reactor** ([`run_sentinel_reactor`]) — periodically re-verifies each `entry.json.pending` and promotes it (last-write-wins on `issued_at`) to `entry.json`, the single canonical copy the `swarm_curator` trait collector reads.
/// - **Registry shim supervisor** ([`registry::listener::run_supervisor`], ADR-024 slice 7) — watches the server-published `swarm.curator.local_port` cap and binds `127.0.0.1:<port>` to the Docker Registry v2 surface in [`registry::routes`]. Disabled (no listener) when the server-side port is 0 / unset; re-binds when the operator flips the value at runtime.
///
/// `envoy_id` is this envoy's enrollment id (used as the manifest seeder id);
/// `manifest` is the shared swarm manifest CRDT.
pub fn spawn(envoy_id: String, cancel: CancellationToken, manifest: std::sync::Arc<Manifest>) {
    info!(
        socket = %helper::socket_path().display(),
        envoy_id = %envoy_id,
        "curator: spawning helper daemon + sentinel reactor + registry supervisor (ADR-024 slices 2 + 7)"
    );
    let helper_cancel = cancel.clone();
    tokio::spawn(async move {
        if let Err(e) = helper::run(envoy_id, helper_cancel, manifest).await {
            warn!(error = %e, "curator helper daemon exited with error");
        }
    });
    let reactor_cancel = cancel.clone();
    tokio::spawn(run_sentinel_reactor(reactor_cancel));
    tokio::spawn(registry::listener::run_supervisor(cancel));
}

async fn run_sentinel_reactor(cancel: CancellationToken) {
    let mut tick = tokio::time::interval(SENTINEL_SCAN_INTERVAL);
    tick.tick().await; // skip the immediate first tick — let the rest of the agent come up
    loop {
        tokio::select! {
            _ = cancel.cancelled() => return,
            _ = tick.tick() => {
                let passwd = crate::traits::gitback::passwd_path();
                match std::fs::read_to_string(&passwd) {
                    Ok(body) => scan_all_users(&body),
                    Err(e) => debug!(error = %e, path = %passwd.display(), "curator reactor: cannot read passwd"),
                }
            }
        }
    }
}

/// Walk every user in `passwd_body`, and for each `~/.vigo-curator/<aid>/entry.json.pending`
/// re-verify it and either promote it to `entry.json` (LWW on `issued_at`) or
/// drop it (corrupt / non-self-verifying / misfiled / stale).
fn scan_all_users(passwd_body: &str) {
    for line in passwd_body.lines() {
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        let fields: Vec<&str> = line.split(':').collect();
        if fields.len() < 7 {
            continue;
        }
        let curator_dir = state::user_curator_dir(Path::new(fields[5]));
        let Ok(rd) = std::fs::read_dir(&curator_dir) else { continue };
        for ent in rd.flatten() {
            let p = ent.path();
            if !p.is_dir() {
                continue;
            }
            let Some(dir_name) = p.file_name().and_then(|n| n.to_str()) else { continue };
            let pending = p.join(state::ENTRY_PENDING_FILE);
            if !pending.exists() {
                continue;
            }
            if let Err(e) = promote_pending_entry(&pending, &p.join(state::ENTRY_FILE), dir_name) {
                warn!(path = %pending.display(), error = %e, "curator reactor: dropping bad pending entry");
                let _ = std::fs::remove_file(&pending);
            }
        }
    }
}

/// Validate `pending` and atomically promote it to `entry_path` unless an
/// existing `entry_path` is newer (LWW on `issued_at`). Returns `Err` (the
/// caller then unlinks `pending`) for a corrupt, non-self-verifying, or misfiled
/// pending entry. A *stale* pending entry (older than the existing one) is not an
/// error — it's just consumed without overwriting.
fn promote_pending_entry(pending: &Path, entry_path: &Path, dir_name: &str) -> anyhow::Result<()> {
    use anyhow::{bail, Context};
    let raw = std::fs::read(pending).with_context(|| format!("reading {}", pending.display()))?;
    let entry: catalog::CatalogEntry =
        serde_json::from_slice(&raw).context("parsing pending catalog entry")?;
    entry.verify(DELEGATION_MAX_LEN).context("pending catalog entry failed verification")?;
    catalog::validate_artifact_name(&entry.body.name).context("pending catalog entry artifact name")?;
    let aid = entry.artifact_id_hex().context("deriving artifact_id")?;
    if aid != dir_name {
        bail!("pending entry's artifact_id {aid} ≠ its directory name {dir_name} (misfiled)");
    }
    // LWW: never replace a newer canonical entry with an older pending one.
    if let Ok(existing_raw) = std::fs::read(entry_path) {
        if let Ok(existing) = serde_json::from_slice::<catalog::CatalogEntry>(&existing_raw) {
            if existing.body.issued_at > entry.body.issued_at {
                let _ = std::fs::remove_file(pending);
                debug!(artifact_id = %aid, "curator reactor: discarding stale pending entry");
                return Ok(());
            }
        }
    }
    std::fs::rename(pending, entry_path)
        .with_context(|| format!("renaming {} -> {}", pending.display(), entry_path.display()))?;
    info!(artifact_id = %aid, name = %entry.body.name, "curator reactor: promoted pending catalog entry");
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::swarm::curator::catalog::{
        ArtifactVersion, CatalogEntry, CatalogEntryBody, Platform, KIND_GENERIC,
    };
    use ed25519_dalek::SigningKey;
    use rand::rngs::OsRng;
    use std::collections::BTreeMap;
    use tempfile::TempDir;

    fn entry_for(k: &SigningKey, name: &str, issued_at: i64) -> CatalogEntry {
        let mut versions = BTreeMap::new();
        versions.insert(
            "1.0.0".to_string(),
            ArtifactVersion {
                platforms: vec![Platform {
                    os: "linux".into(),
                    arch: "amd64".into(),
                    blob_sha: "a".repeat(64),
                    size: 1,
                }],
            },
        );
        let mut tags = BTreeMap::new();
        tags.insert("latest".to_string(), "1.0.0".to_string());
        CatalogEntry::issue(
            k,
            CatalogEntryBody {
                schema_version: 1,
                name: name.into(),
                kind: KIND_GENERIC.into(),
                founder_puddle_pubkey: hex::encode(k.verifying_key().to_bytes()),
                signing_puddle_pubkey: hex::encode(k.verifying_key().to_bytes()),
                versions,
                tags,
                recipients: vec!["*".into()],
                target: "*".into(),
                issued_at,
                delegations: vec![],
            },
        )
        .unwrap()
    }

    fn write_pending(home: &Path, aid: &str, entry: &CatalogEntry) {
        let dir = state::artifact_dir(home, aid);
        std::fs::create_dir_all(&dir).unwrap();
        std::fs::write(dir.join(state::ENTRY_PENDING_FILE), serde_json::to_vec(entry).unwrap()).unwrap();
    }

    fn passwd_line(user: &str, uid: u32, home: &Path) -> String {
        format!("{user}:x:{uid}:{uid}::{}:/bin/sh\n", home.display())
    }

    #[test]
    fn promotes_a_valid_pending_entry() {
        let home = TempDir::new().unwrap();
        let k = SigningKey::generate(&mut OsRng);
        let entry = entry_for(&k, "vigo-agent", 100);
        let aid = entry.artifact_id_hex().unwrap();
        write_pending(home.path(), &aid, &entry);

        scan_all_users(&passwd_line("dan", 1000, home.path()));

        let entry_path = state::entry_path(home.path(), &aid);
        assert!(entry_path.exists(), "valid pending must be promoted to entry.json");
        assert!(!state::entry_pending_path(home.path(), &aid).exists(), "the pending file is consumed");
        let promoted: CatalogEntry = serde_json::from_slice(&std::fs::read(&entry_path).unwrap()).unwrap();
        assert_eq!(promoted, entry);
    }

    #[test]
    fn drops_corrupt_misfiled_and_tampered_pending_entries() {
        let home = TempDir::new().unwrap();
        let k = SigningKey::generate(&mut OsRng);

        // 1. Not JSON.
        let d1 = state::artifact_dir(home.path(), &"a".repeat(64));
        std::fs::create_dir_all(&d1).unwrap();
        std::fs::write(d1.join(state::ENTRY_PENDING_FILE), b"not json").unwrap();
        // 2. Valid entry filed under the wrong artifact_id directory.
        let good = entry_for(&k, "x", 1);
        write_pending(home.path(), &"b".repeat(64), &good);
        // 3. Tampered: mutated name ⇒ artifact_id recomputes to the dir we file it
        //    under (so the dir-name check passes) but the signature no longer verifies.
        let mut tampered = entry_for(&k, "y", 1);
        tampered.body.name = "yy".into();
        write_pending(home.path(), &tampered.artifact_id_hex().unwrap(), &tampered);

        scan_all_users(&passwd_line("dan", 1000, home.path()));

        // None promoted; all pending files removed.
        for aid in [&"a".repeat(64), &good.artifact_id_hex().unwrap(), &"b".repeat(64), &tampered.artifact_id_hex().unwrap()] {
            assert!(!state::entry_path(home.path(), aid).exists(), "no entry.json for {aid}");
            assert!(!state::entry_pending_path(home.path(), aid).exists(), "pending removed for {aid}");
        }
    }

    #[test]
    fn lww_keeps_the_newer_entry() {
        let home = TempDir::new().unwrap();
        let k = SigningKey::generate(&mut OsRng);
        let older = entry_for(&k, "shared", 100);
        let newer = entry_for(&k, "shared", 200);
        let aid = older.artifact_id_hex().unwrap();

        // A newer entry is already canonical; a stale pending arrives and must be discarded.
        let dir = state::artifact_dir(home.path(), &aid);
        std::fs::create_dir_all(&dir).unwrap();
        std::fs::write(state::entry_path(home.path(), &aid), serde_json::to_vec(&newer).unwrap()).unwrap();
        std::fs::write(state::entry_pending_path(home.path(), &aid), serde_json::to_vec(&older).unwrap()).unwrap();
        scan_all_users(&passwd_line("dan", 1000, home.path()));
        let kept: CatalogEntry = serde_json::from_slice(&std::fs::read(state::entry_path(home.path(), &aid)).unwrap()).unwrap();
        assert_eq!(kept.body.issued_at, 200, "stale pending must not clobber the newer entry");
        assert!(!state::entry_pending_path(home.path(), &aid).exists());

        // The reverse: an older entry is canonical, a newer pending arrives and wins.
        std::fs::write(state::entry_path(home.path(), &aid), serde_json::to_vec(&older).unwrap()).unwrap();
        std::fs::write(state::entry_pending_path(home.path(), &aid), serde_json::to_vec(&newer).unwrap()).unwrap();
        scan_all_users(&passwd_line("dan", 1000, home.path()));
        let kept: CatalogEntry = serde_json::from_slice(&std::fs::read(state::entry_path(home.path(), &aid)).unwrap()).unwrap();
        assert_eq!(kept.body.issued_at, 200);
    }
}
