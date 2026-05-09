// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Swarmgit — ADR-015 unified-substrate git over swarm.
//!
//! Each project has a stable, content-addressed identity:
//! `project_id = sha256(founder_puddle_pubkey)`. Projects are accessed
//! over a `vigo://<project_id>/<handle>` URL via the multi-call
//! `git-remote-vigo` helper (ADR-015 step 1, shipped 0.34.9). The
//! `vigo` agent verifies a stack of capability tokens (step 2, shipped
//! 0.34.10) against the project's anchor pubkey, then proxies to
//! `git-receive-pack` / `git-upload-pack` against a root-owned bare
//! repo at `/var/lib/vigo/swarm/swarmgit/projects/<project_id>.git`
//! (mode `0700 root:root`).
//!
//! Project shape is described by two orthogonal axes:
//!
//! | `members:`           | `dr_fanout:`   | Use case                      |
//! |----------------------|----------------|-------------------------------|
//! | `{my-puddle}`         | `fleet-wide`   | Personal-DR (today's default) |
//! | `{my-puddle}`         | `members-only` | Single-user, single-puddle     |
//! | `{team1, team2, …}`  | `members-only` | Multi-member collaboration    |
//! | `{team1, team2, …}`  | `fleet-wide`   | Team repo + fleet-wide DR     |
//!
//! ### Module layout (post-ADR-015 step 3)
//!
//! - [`tokens`] — capability-token primitives (Ed25519, deterministic
//!   canonical encoding, content-addressed token IDs, chain
//!   verification). Shipped at 0.34.10.
//! - [`project_state`] — per-user state at
//!   `~/.vigo-swarmgit/<project_id>/` (project config + token chain)
//!   and bare-repo path helpers under
//!   `/var/lib/vigo/swarm/swarmgit/projects/`.
//! - [`cli`] — the `vigo swarm swarmgit project {init,invite,join,
//!   leave,list,revoke,reanchor,status}` verb tree.
//! - [`gate`] — runtime gate consulted before any swarm-side swarmgit
//!   work runs.
//! - [`git_ops`] — the only place that spawns `git`; uniform hardening
//!   policy on every invocation.
//! - [`peer_table`], [`peer_pubkey_cache`] — swarm peer infrastructure
//!   shared with the rest of the swarmgit substrate.
//!
//! [`receiver`], [`pending_claims`], and [`state`] kept as live
//! support for the discovery / swarm-server transport code in
//! `crate::swarm::{discovery, server}`: `ClaimsHandle` ingests
//! LAN claim announcements; `PendingClaims` + `format_claim_line` /
//! `parse_claim_line` shape the multicast wire format; `TraitsSnapshot`
//! is the schema the trait collector reads. The legacy bundle-based
//! receiver/publisher pipeline they originally served was removed in
//! 0.38.16 alongside ADR-015 step 4's complete rollout.

pub mod cli;
pub mod envelopes;
pub mod fan_out;
pub mod gate;
pub mod git_ops;
pub mod helper;
pub mod peer_pubkey_cache;
pub mod peer_table;
pub mod pending_claims;
pub mod pubkey_bootstrap;
pub mod project_peers;
pub mod project_state;
pub mod receiver;
pub mod rekey;
pub mod remote_helper;
pub mod state;
pub mod tokens;

pub use gate::Gate;

use tokio_util::sync::CancellationToken;
use tracing::info;

/// Maximum git bundle size swarmgit will accept on the swarm peer
/// port, in bytes.
///
/// 20 GiB. Bundles are git history, not working-tree contents, so
/// even a large personal repo with long history typically fits in a
/// few hundred megabytes. The cap exists to prevent unbounded growth
/// from a misconfigured source, not to be tight. Enforced by the
/// swarm peer-port HTTP server on inbound bundle PUTs.
pub const MAX_BUNDLE_SIZE: u64 = 20 * 1024 * 1024 * 1024;

/// Spawn the swarmgit subsystem.
///
/// Three concurrent tasks run under the cancellation token:
///
/// 1. **Helper daemon** ([`helper::run`]) — listens on
///    `/var/run/vigo/swarmgit.sock`, proxies `git-receive-pack` /
///    `git-upload-pack` for the user-side `git-remote-vigo` binary
///    after token-chain verification.
/// 2. **Sentinel reactor** ([`run_sentinel_reactor`]) — periodically
///    scans every user's `~/.vigo-swarmgit/<pid>/{leave,revoke}.pending`
///    files (which the CLI writes as already-signed envelopes), moves
///    them into the canonical envelopes/ store, and unlinks the
///    sentinels.
/// 3. **Reap reactor** ([`run_reap_reactor`]) — hourly GC of envelopes
///    older than 90 days.
///
pub fn spawn(
    tls: Option<crate::update::TlsCerts>,
    cancel: CancellationToken,
    gate: Gate,
) {
    info!(
        initial_gate = gate.enabled(),
        socket = %helper::socket_path().display(),
        tls_available = tls.is_some(),
        "swarmgit: spawning helper daemon + sentinel reactor (ADR-015 step 4)"
    );

    // Initialize the process-wide ProjectPeers index. Discovery's
    // listener populates it from SWARMGITPROJECT lines; the helper
    // daemon fan-out hook consults it for LAN auto-discovered targets.
    let _ = project_peers::init_global();

    // Helper daemon — runs forever until cancellation. TLS certs are
    // threaded through so the post-receive fan-out hook can build an
    // mTLS HTTP client to push bundles to peers.
    let helper_cancel = cancel.clone();
    let helper_tls = tls.clone();
    tokio::spawn(async move {
        if let Err(e) = helper::run(helper_cancel, helper_tls).await {
            tracing::error!(error = %e, "swarmgit helper daemon exited with error");
        }
    });

    // Sentinel reactor — drains *.pending files into canonical
    // envelopes/, gossips signed envelopes to known peers, and
    // periodically reconciles via /digest comparisons.
    tokio::spawn(run_sentinel_reactor(cancel.clone(), tls));

    // 90-day reap.
    tokio::spawn(run_reap_reactor(cancel));
}

/// How often the sentinel reactor sweeps every user's
/// `~/.vigo-swarmgit/`. 60 s is a UX-vs-CPU trade: a fresh `vigo
/// swarm swarmgit project leave` lands in the canonical store
/// within one tick, but root spends only a few read_dir() calls per
/// minute on idle envoys.
const SENTINEL_SCAN_INTERVAL: std::time::Duration = std::time::Duration::from_secs(60);

/// How often the reaper sweeps the canonical envelopes/ tree for
/// records older than [`envelopes::DEFAULT_RETENTION_SECS`]. Hourly
/// is generous — even a multi-month outage holding a surplus of
/// expired envelopes recovers within an hour of the agent coming
/// back up.
const REAP_INTERVAL: std::time::Duration = std::time::Duration::from_secs(60 * 60);

/// How often the reactor walks every locally-held project and
/// reconciles envelopes against each peer's `GET /digest` (pulling
/// `GET /all` on mismatch). 5 minutes covers offline peers without
/// flooding the network on a steady-state cluster — the cheap path
/// is the digest GET, which is just a sha256 + counts.
const RECONCILE_INTERVAL: std::time::Duration = std::time::Duration::from_secs(5 * 60);

async fn run_sentinel_reactor(
    cancel: CancellationToken,
    tls: Option<crate::update::TlsCerts>,
) {
    let mut tick = tokio::time::interval(SENTINEL_SCAN_INTERVAL);
    let mut reconcile_tick = tokio::time::interval(RECONCILE_INTERVAL);
    // Skip the immediate first ticks — gives the rest of the agent a
    // moment to come up before we start hitting /etc/passwd / network.
    tick.tick().await;
    reconcile_tick.tick().await;
    loop {
        tokio::select! {
            _ = cancel.cancelled() => return,
            _ = tick.tick() => {
                let base = crate::platform::paths::default_swarmgit_dir();
                if let Err(e) = scan_all_users(&base, tls.as_ref()) {
                    tracing::warn!(error = %e, "swarmgit sentinel reactor: scan error");
                }
            }
            _ = reconcile_tick.tick() => {
                let base = crate::platform::paths::default_swarmgit_dir();
                let tls_ref = match tls.as_ref() {
                    Some(t) => t.clone(),
                    None => continue,
                };
                tokio::task::spawn_blocking(move || {
                    if let Err(e) = reconcile_all_users(&base, &tls_ref) {
                        tracing::warn!(error = %e, "swarmgit reconcile reactor: error");
                    }
                });
            }
        }
    }
}

async fn run_reap_reactor(cancel: CancellationToken) {
    let mut tick = tokio::time::interval(REAP_INTERVAL);
    tick.tick().await;
    loop {
        tokio::select! {
            _ = cancel.cancelled() => return,
            _ = tick.tick() => {
                let base = crate::platform::paths::default_swarmgit_dir();
                let now = unix_seconds_now();
                match envelopes::reap_all(&base, now, envelopes::effective_retention_secs()) {
                    Ok(0) => {}
                    Ok(n) => tracing::info!(reaped = n, "swarmgit: reaped expired envelopes"),
                    Err(e) => tracing::warn!(error = %e, "swarmgit reap reactor: error"),
                }
            }
        }
    }
}

fn scan_all_users(
    swarmgit_base: &std::path::Path,
    tls: Option<&crate::update::TlsCerts>,
) -> anyhow::Result<()> {
    let body = std::fs::read_to_string("/etc/passwd")?;
    for line in body.lines() {
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        let fields: Vec<&str> = line.split(':').collect();
        if fields.len() < 7 {
            continue;
        }
        let home = std::path::PathBuf::from(fields[5]);
        let dot_dir = home.join(".vigo-swarmgit");
        if !dot_dir.exists() {
            continue;
        }
        scan_user_dir(swarmgit_base, &home, &dot_dir, tls);
    }
    Ok(())
}

/// Walk every user's `~/.vigo-swarmgit/<pid>/` and reconcile each
/// project's envelopes against known peers via the `/digest` ↔
/// `/all` flow. Best-effort; per-peer errors stay local.
fn reconcile_all_users(
    swarmgit_base: &std::path::Path,
    tls: &crate::update::TlsCerts,
) -> anyhow::Result<()> {
    let body = std::fs::read_to_string("/etc/passwd")?;
    for line in body.lines() {
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        let fields: Vec<&str> = line.split(':').collect();
        if fields.len() < 7 {
            continue;
        }
        let home = std::path::PathBuf::from(fields[5]);
        let dot_dir = home.join(".vigo-swarmgit");
        let entries = match std::fs::read_dir(&dot_dir) {
            Ok(it) => it,
            Err(_) => continue,
        };
        for entry in entries.flatten() {
            let p = entry.path();
            if !p.is_dir() {
                continue;
            }
            if let Some(name) = p.file_name().and_then(|n| n.to_str()) {
                if let Ok(pid) = project_state::parse_hex_id(name) {
                    fan_out::reconcile_envelopes(swarmgit_base, &home, &pid, tls);
                }
            }
        }
    }
    Ok(())
}

fn scan_user_dir(
    swarmgit_base: &std::path::Path,
    home: &std::path::Path,
    dot_dir: &std::path::Path,
    tls: Option<&crate::update::TlsCerts>,
) {
    let entries = match std::fs::read_dir(dot_dir) {
        Ok(it) => it,
        Err(_) => return,
    };
    for entry in entries.flatten() {
        let project_dir = entry.path();
        if !project_dir.is_dir() {
            continue;
        }
        let leave = project_dir.join("leave.pending");
        if leave.exists() {
            if let Err(e) = process_leave_sentinel(swarmgit_base, home, &leave, tls) {
                tracing::warn!(
                    path = %leave.display(),
                    error = %e,
                    "swarmgit: leave sentinel error"
                );
            }
        }
        let revoke = project_dir.join("revoke.pending");
        if revoke.exists() {
            if let Err(e) = process_revoke_sentinel(swarmgit_base, home, &revoke, tls) {
                tracing::warn!(
                    path = %revoke.display(),
                    error = %e,
                    "swarmgit: revoke sentinel error"
                );
            }
        }
    }
}

fn process_leave_sentinel(
    swarmgit_base: &std::path::Path,
    home: &std::path::Path,
    path: &std::path::Path,
    tls: Option<&crate::update::TlsCerts>,
) -> anyhow::Result<()> {
    use anyhow::Context;
    let bytes = std::fs::read(path)
        .with_context(|| format!("read {}", path.display()))?;
    let envelope: envelopes::LeaveEnvelope = serde_json::from_slice(&bytes)
        .with_context(|| format!("parse leave envelope at {}", path.display()))?;
    envelopes::save_leave(swarmgit_base, &envelope)
        .with_context(|| format!("persist leave envelope from {}", path.display()))?;
    let _ = std::fs::remove_file(path);
    tracing::info!(
        path = %path.display(),
        project_id = %envelope.project_id,
        "swarmgit: persisted LEAVE envelope from sentinel"
    );
    if let Some(tls) = tls {
        if let Ok(pid) = project_state::parse_hex_id(&envelope.project_id) {
            let (attempted, succeeded) =
                fan_out::gossip_leave_to_peers(home, &pid, &envelope, tls);
            if attempted > 0 {
                tracing::info!(
                    project_id = %envelope.project_id,
                    attempted,
                    succeeded,
                    "swarmgit: gossiped LEAVE envelope"
                );
            }
        }
    }
    Ok(())
}

fn process_revoke_sentinel(
    swarmgit_base: &std::path::Path,
    home: &std::path::Path,
    path: &std::path::Path,
    tls: Option<&crate::update::TlsCerts>,
) -> anyhow::Result<()> {
    use anyhow::Context;
    let bytes = std::fs::read(path)
        .with_context(|| format!("read {}", path.display()))?;
    let envelope: envelopes::RevokeEnvelope = serde_json::from_slice(&bytes)
        .with_context(|| format!("parse revoke envelope at {}", path.display()))?;
    envelopes::save_revoke(swarmgit_base, &envelope)
        .with_context(|| format!("persist revoke envelope from {}", path.display()))?;
    let _ = std::fs::remove_file(path);
    tracing::info!(
        path = %path.display(),
        project_id = %envelope.project_id,
        token_id = %envelope.token_id,
        "swarmgit: persisted REVOKE envelope from sentinel"
    );
    if let Some(tls) = tls {
        if let Ok(pid) = project_state::parse_hex_id(&envelope.project_id) {
            let (attempted, succeeded) =
                fan_out::gossip_revoke_to_peers(home, &pid, &envelope, tls);
            if attempted > 0 {
                tracing::info!(
                    project_id = %envelope.project_id,
                    attempted,
                    succeeded,
                    "swarmgit: gossiped REVOKE envelope"
                );
            }
        }
    }
    Ok(())
}

fn unix_seconds_now() -> i64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs() as i64)
        .unwrap_or(0)
}

#[cfg(test)]
mod tests {
    use super::*;
    use ed25519_dalek::SigningKey;
    use rand::rngs::OsRng;

    fn fresh_key() -> SigningKey {
        SigningKey::generate(&mut OsRng)
    }

    #[test]
    fn leave_sentinel_persists_and_unlinks() {
        let base = tempfile::tempdir().unwrap();
        let user_dir = tempfile::tempdir().unwrap();
        let project_id = [0xAAu8; tokens::ID_LEN];
        let project_dir = user_dir.path().join(super::project_state::hex_id(&project_id));
        std::fs::create_dir_all(&project_dir).unwrap();
        let sentinel = project_dir.join("leave.pending");

        let leaver = fresh_key();
        let env = envelopes::LeaveEnvelope::build(&leaver, &project_id, 1_700_000_000);
        std::fs::write(&sentinel, serde_json::to_vec(&env).unwrap()).unwrap();

        // Pass home=user_dir.path() — the sentinel's per-user dot-dir
        // root. tls=None so gossip is a no-op (we're testing only
        // the persist + unlink path).
        process_leave_sentinel(base.path(), user_dir.path(), &sentinel, None)
            .expect("process leave");
        assert!(!sentinel.exists(), "sentinel should be unlinked");
        let stored = envelopes::load_leaves(base.path(), &project_id).unwrap();
        assert_eq!(stored.len(), 1);
        assert_eq!(stored[0], env);
    }

    #[test]
    fn revoke_sentinel_persists_and_unlinks() {
        let base = tempfile::tempdir().unwrap();
        let user_dir = tempfile::tempdir().unwrap();
        let project_id = [0xBBu8; tokens::ID_LEN];
        let project_dir = user_dir.path().join(super::project_state::hex_id(&project_id));
        std::fs::create_dir_all(&project_dir).unwrap();
        let sentinel = project_dir.join("revoke.pending");

        let admin = fresh_key();
        let token_id = [0x42u8; tokens::ID_LEN];
        let env = envelopes::RevokeEnvelope::build(
            &admin,
            &project_id,
            &token_id,
            1_700_000_000,
            "compromised",
        );
        std::fs::write(&sentinel, serde_json::to_vec(&env).unwrap()).unwrap();

        process_revoke_sentinel(base.path(), user_dir.path(), &sentinel, None)
            .expect("process revoke");
        assert!(!sentinel.exists(), "sentinel should be unlinked");
        let stored = envelopes::load_revokes(base.path(), &project_id).unwrap();
        assert_eq!(stored.len(), 1);
        assert_eq!(stored[0], env);
        assert!(envelopes::is_token_revoked(base.path(), &project_id, &token_id));
    }

    #[test]
    fn malformed_sentinel_keeps_file_for_diagnosis() {
        let base = tempfile::tempdir().unwrap();
        let user_dir = tempfile::tempdir().unwrap();
        let project_id = [0xCCu8; tokens::ID_LEN];
        let project_dir = user_dir.path().join(super::project_state::hex_id(&project_id));
        std::fs::create_dir_all(&project_dir).unwrap();
        let sentinel = project_dir.join("leave.pending");
        std::fs::write(&sentinel, b"not valid json").unwrap();

        let result = process_leave_sentinel(base.path(), user_dir.path(), &sentinel, None);
        assert!(result.is_err(), "malformed sentinel should bubble error");
        assert!(
            sentinel.exists(),
            "malformed sentinel should not be silently unlinked — operator needs to see it"
        );
    }

    #[test]
    fn scan_user_dir_processes_both_kinds() {
        let base = tempfile::tempdir().unwrap();
        let user_dir = tempfile::tempdir().unwrap();
        let pid_a = [0x01u8; tokens::ID_LEN];
        let pid_b = [0x02u8; tokens::ID_LEN];
        let dir_a = user_dir.path().join(super::project_state::hex_id(&pid_a));
        let dir_b = user_dir.path().join(super::project_state::hex_id(&pid_b));
        std::fs::create_dir_all(&dir_a).unwrap();
        std::fs::create_dir_all(&dir_b).unwrap();

        let leaver = fresh_key();
        let admin = fresh_key();
        let leave_env = envelopes::LeaveEnvelope::build(&leaver, &pid_a, 100);
        let revoke_env = envelopes::RevokeEnvelope::build(
            &admin,
            &pid_b,
            &[0x99u8; tokens::ID_LEN],
            200,
            "rotation",
        );
        std::fs::write(
            dir_a.join("leave.pending"),
            serde_json::to_vec(&leave_env).unwrap(),
        )
        .unwrap();
        std::fs::write(
            dir_b.join("revoke.pending"),
            serde_json::to_vec(&revoke_env).unwrap(),
        )
        .unwrap();

        // home=user_dir.path() so gossip lookups (tls=None → no-op
        // path) load the right project.json fixture; we don't have
        // one, so gossip silently aborts at "no project.json".
        scan_user_dir(base.path(), user_dir.path(), user_dir.path(), None);

        assert!(!dir_a.join("leave.pending").exists());
        assert!(!dir_b.join("revoke.pending").exists());
        assert_eq!(envelopes::load_leaves(base.path(), &pid_a).unwrap().len(), 1);
        assert_eq!(envelopes::load_revokes(base.path(), &pid_b).unwrap().len(), 1);
    }
}

/// Check whether swarmgit should be active on this envoy right now.
///
/// Thin wrapper over [`crate::swarm::gates::substrate_active`] —
/// applies the global `swarm_enabled` AND `swarm_swarmgit_enabled`
/// rule from the cached `PolicyBundle.vars_json`. The server has
/// already resolved `swarm.swarmgit.enabled` pattern matching for
/// this envoy's hostname; the agent only sees the resolved bools.
/// See `crate::swarm::gates` for the fail-open semantics and
/// recognised value shapes.
pub fn swarmgit_enabled_for_envoy(state_store: Option<&crate::state::StateStore>) -> bool {
    crate::swarm::gates::substrate_active(
        crate::swarm::gates::Substrate::Swarmgit,
        state_store,
    )
}
