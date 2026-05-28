// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Gitback — ADR-015 unified-substrate git over swarm.
//!
//! Each project has a stable, content-addressed identity:
//! `project_id = sha256(founder_puddle_pubkey)`. Projects are accessed
//! over a `gitback://<project_id>/<handle>` URL via the multi-call
//! `git-remote-gitback` helper (ADR-015 step 1, shipped 0.34.9). The
//! `vigo` agent verifies a stack of capability tokens (step 2, shipped
//! 0.34.10) against the project's anchor pubkey, then proxies to
//! `git-receive-pack` / `git-upload-pack` against a root-owned bare
//! repo at `/var/lib/vigo/swarm/gitback/projects/<project_id>.git`
//! (mode `0700 root:root`).
//!
//! Project shape is described by two orthogonal axes:
//!
//! | `members:`           | `dr_scope:`    | Use case                      |
//! |----------------------|----------------|-------------------------------|
//! | `{my-puddle}`         | `swarm-wide`   | Personal-DR (today's default) |
//! | `{my-puddle}`         | `members-only` | Single-user, single-puddle     |
//! | `{team1, team2, …}`  | `members-only` | Multi-member collaboration    |
//! | `{team1, team2, …}`  | `swarm-wide`   | Team repo + swarm-wide DR     |
//!
//! ### Module layout (post-ADR-015 step 3)
//!
//! The puddle-rooted ownership/access primitive (capability tokens,
//! ADR-019 delegation envelopes, the SO_PEERCRED helper) is **shared**
//! with curator (ADR-024) and the ADR-022 friendly-name claims, and
//! lives in [`crate::swarm::anchor`] — gitback imports it like any
//! other consumer. The submodules below are the git-specific layer.
//!
//! - [`project_state`] — per-user state at
//!   `~/.vigo-gitback/<project_id>/` (project config + token chain)
//!   and bare-repo path helpers under
//!   `/var/lib/vigo/swarm/gitback/projects/`.
//! - [`cli`] — the `vigo swarm gitback project {init,invite,join,
//!   leave,list,revoke,reanchor,status}` verb tree.
//! - [`gate`] — runtime gate consulted before any swarm-side gitback
//!   work runs.
//! - [`git_ops`] — the only place that spawns `git`; uniform hardening
//!   policy on every invocation.
//! - [`peer_table`], [`peer_pubkey_cache`] — swarm peer infrastructure
//!   shared with the rest of the gitback substrate.
//!
//! [`receiver`], [`pending_claims`], and [`state`] kept as live
//! support for the discovery / swarm-server transport code in
//! `crate::swarm::{discovery, server}`: `ClaimsHandle` ingests
//! LAN claim announcements; `PendingClaims` + `format_claim_line` /
//! `parse_claim_line` shape the multicast wire format; `state` holds
//! the shared `unix_seconds_now` helper. The legacy bundle-based
//! receiver/publisher pipeline they originally served was removed in
//! 0.38.16 alongside ADR-015 step 4's complete rollout — including the
//! `traits.json` snapshot the trait collector used to read; the
//! collector now walks live per-user state directly.

pub mod cli;
pub mod dr_scope;
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

pub use gate::Gate;

use tokio_util::sync::CancellationToken;
use tracing::info;

/// Default maximum git bundle size gitback accepts on any transfer
/// path, in bytes — overridable per host via
/// `swarm.gitback.max_bundle_bytes` in server.yaml (resolved through
/// [`crate::swarm::runtime_limits::gitback_max_bundle_bytes`]; `0` /
/// unset falls back to this default).
///
/// 2 GiB. Bundles are git history, not working-tree contents, so a
/// large personal repo with deep history still typically fits in a
/// few hundred megabytes; the cap bounds a runaway source rather than
/// being tight. Enforced identically on the publisher fan-out, the
/// swarm peer-port push-receive handlers, and the DR catch-up GET.
pub const DEFAULT_MAX_BUNDLE_SIZE: u64 = 2 * 1024 * 1024 * 1024;

/// Spawn the gitback subsystem.
///
/// Four concurrent tasks run under the cancellation token:
///
/// 1. **Helper daemon** ([`helper::run`]) — listens on
///    `/var/run/vigo/gitback.sock`, proxies `git-receive-pack` /
///    `git-upload-pack` for the user-side `git-remote-gitback` binary
///    after token-chain verification.
/// 2. **Sentinel reactor** ([`run_sentinel_reactor`]) — periodically
///    scans every user's `~/.vigo-gitback/<pid>/{leave,revoke}.pending`
///    files (which the CLI writes as already-signed envelopes), moves
///    them into the canonical envelopes/ store, and unlinks the
///    sentinels; also runs the periodic envelope `/digest` reconcile.
/// 3. **Catch-up reactor** ([`run_catchup_reactor`]) — pulls a bare
///    repo for any locally-held project this envoy has no (or an
///    empty) bare repo for, from a peer that announces holding it.
/// 4. **Reap reactor** ([`run_reap_reactor`]) — hourly GC of envelopes
///    older than 90 days.
///
pub fn spawn(
    tls: Option<crate::update::TlsCerts>,
    cancel: CancellationToken,
    gate: Gate,
    peer_pubkey_cache: std::sync::Arc<peer_pubkey_cache::PeerPubkeyCache>,
    peer_table: std::sync::Arc<peer_table::PeerTable>,
    claims: receiver::ClaimsHandle,
) {
    info!(
        initial_gate = gate.enabled(),
        socket = %helper::socket_path().display(),
        tls_available = tls.is_some(),
        "gitback: spawning helper daemon + sentinel reactor (ADR-015 step 4)"
    );

    // Initialize the process-wide ProjectPeers index. Discovery's
    // listener populates it from GITBACKPROJECT lines; the helper
    // daemon fan-out hook consults it for LAN auto-discovered targets.
    let _ = project_peers::init_global();

    // Helper daemon — runs forever until cancellation. TLS certs are
    // threaded through so the post-receive fan-out hook can build an
    // mTLS HTTP client to push bundles to peers.
    let helper_cancel = cancel.clone();
    let helper_tls = tls.clone();
    tokio::spawn(async move {
        if let Err(e) = helper::run(helper_cancel, helper_tls).await {
            tracing::error!(error = %e, "gitback helper daemon exited with error");
        }
    });

    // Sentinel reactor — drains *.pending files into canonical
    // envelopes/, gossips signed envelopes to known peers, and
    // periodically reconciles via /digest comparisons.
    tokio::spawn(run_sentinel_reactor(cancel.clone(), tls.clone()));

    // DR catch-up reactor — pulls a bare repo for any project this
    // envoy holds local state for but has no (or an empty) bare repo
    // for, from a peer that announces holding it. Closes the
    // swarm-wide-DR bootstrap gap (post-receive fan-out only fires on
    // a ref delta, so a freshly-joined member otherwise waits for the
    // founder's next push).
    tokio::spawn(run_catchup_reactor(cancel.clone(), tls));

    // 90-day reap.
    tokio::spawn(run_reap_reactor(cancel.clone()));

    // Prune reactor — bounds the four per-peer in-memory caches that
    // otherwise grow with every unique announcer hostname seen. Each
    // cache defines its own TTL constant; this reactor just drives
    // the eviction on a periodic tick.
    tokio::spawn(run_prune_reactor(
        cancel,
        peer_pubkey_cache,
        peer_table,
        claims,
    ));
}

/// How often the sentinel reactor sweeps every user's
/// `~/.vigo-gitback/`. 60 s is a UX-vs-CPU trade: a fresh `vigo
/// swarm gitback project leave` lands in the canonical store
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

/// How often the catch-up reactor looks for locally-held projects
/// missing their bare repo and pulls them from a peer. Slightly
/// longer than the ~30 s multicast announce cadence so a freshly-
/// joined holder has had a chance to advertise first; the per-tick
/// cost is an in-memory peer-index read — the GET only fires when a
/// real gap exists, and stops as soon as the bare repo has refs.
const CATCHUP_INTERVAL: std::time::Duration = std::time::Duration::from_secs(45);

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
                let base = crate::platform::paths::default_gitback_dir();
                if let Err(e) = scan_all_users(&base, tls.as_ref()) {
                    tracing::warn!(error = %e, "gitback sentinel reactor: scan error");
                }
            }
            _ = reconcile_tick.tick() => {
                let base = crate::platform::paths::default_gitback_dir();
                let tls_ref = match tls.as_ref() {
                    Some(t) => t.clone(),
                    None => continue,
                };
                tokio::task::spawn_blocking(move || {
                    if let Err(e) = reconcile_all_users(&base, &tls_ref) {
                        tracing::warn!(error = %e, "gitback reconcile reactor: error");
                    }
                });
            }
        }
    }
}

async fn run_catchup_reactor(
    cancel: CancellationToken,
    tls: Option<crate::update::TlsCerts>,
) {
    // No mTLS material → we can't talk to peers; nothing to do.
    let tls = match tls {
        Some(t) => t,
        None => return,
    };
    let mut tick = tokio::time::interval(CATCHUP_INTERVAL);
    tick.tick().await; // skip the immediate first tick
    loop {
        tokio::select! {
            _ = cancel.cancelled() => return,
            _ = tick.tick() => {
                let base = crate::platform::paths::default_gitback_dir();
                let tls = tls.clone();
                tokio::task::spawn_blocking(move || catchup_all_users(&base, &tls));
            }
        }
    }
}

/// Walk every user's `~/.vigo-gitback/<pid>/`; for the first project
/// the user holds local state for but where this envoy has no (or an
/// empty) bare repo, pull a `git bundle --all` from a peer that
/// announces holding it, then stop — at most one repo backfilled per
/// tick (a `vigo` repo's history is hundreds of MB; we don't want to
/// pull four of them back to back). Peers are tried in turn until one
/// serves it; the whole thing is a no-op once every locally-held
/// project has a populated bare repo.
fn catchup_all_users(gitback_base: &std::path::Path, tls: &crate::update::TlsCerts) {
    use std::collections::HashSet;
    let now = unix_seconds_now();
    // ADR-023: refresh the world-readable announced-projects cache
    // every tick so `vigo swarm puddle unlock` can auto-adopt the
    // projects this puddle founded. Best-effort; off the hot path.
    project_peers::write_announced_cache(now);
    let body = match std::fs::read_to_string("/etc/passwd") {
        Ok(b) => b,
        Err(_) => return,
    };
    let peers = match project_peers::global() {
        Some(p) => p,
        None => return,
    };
    // Multiple users can hold the same project; handle each project
    // once per tick.
    let mut handled: HashSet<String> = HashSet::new();
    for line in body.lines() {
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        let fields: Vec<&str> = line.split(':').collect();
        if fields.len() < 7 {
            continue;
        }
        let home = std::path::PathBuf::from(fields[5]);
        let entries = match std::fs::read_dir(home.join(".vigo-gitback")) {
            Ok(it) => it,
            Err(_) => continue,
        };
        for entry in entries.flatten() {
            let p = entry.path();
            if !p.is_dir() {
                continue;
            }
            let name = match p.file_name().and_then(|n| n.to_str()) {
                Some(n) => n.to_string(),
                None => continue,
            };
            if handled.contains(&name) {
                continue;
            }
            let pid = match project_state::parse_hex_id(&name) {
                Ok(p) => p,
                Err(_) => continue,
            };
            handled.insert(name.clone());

            // Already hold a populated bare repo? Nothing to do.
            let repo = project_state::project_repo_path(gitback_base, &pid);
            if repo.exists()
                && fan_out::snapshot_refs(&repo)
                    .map(|m| !m.is_empty())
                    .unwrap_or(false)
            {
                continue;
            }
            // Need this user's authority chain (+ founder delegations)
            // to ask a peer for the bundle.
            let chain = match project_state::TokenChain::load(&home, &pid) {
                Ok(Some(c)) => c,
                _ => continue,
            };
            let delegations = project_state::DelegationChain::load(&home, &pid)
                .ok()
                .flatten()
                .map(|c| c.delegations)
                .unwrap_or_default();

            let holders: Vec<String> = peers
                .peers_for(&name, now)
                .into_iter()
                .map(|e| match e.addr {
                    Some(std::net::IpAddr::V4(v4)) => v4.to_string(),
                    Some(std::net::IpAddr::V6(v6)) => format!("[{}]", v6),
                    None => e.host,
                })
                .collect();
            if holders.is_empty() {
                continue; // no known holder yet — retry next tick
            }
            let mut last_err: Option<String> = None;
            for holder in &holders {
                match fan_out::fetch_bundle_from_peer(
                    gitback_base,
                    &pid,
                    &chain,
                    &delegations,
                    holder,
                    tls,
                ) {
                    Ok(changed) => {
                        tracing::info!(
                            project_id = %name,
                            from = %holder,
                            refs_changed = changed,
                            "gitback catch-up: pulled bare repo from peer"
                        );
                        return; // one repo per tick — gentle on the host
                    }
                    Err(e) => {
                        tracing::debug!(
                            project_id = %name,
                            from = %holder,
                            error = %e,
                            "gitback catch-up: peer pull failed, trying next"
                        );
                        last_err = Some(format!("{:#}", e));
                    }
                }
            }
            // Every announced holder failed this tick. Warn (not debug)
            // with the last error so the journal names the problem —
            // catch-up retries every CATCHUP_INTERVAL, so a persistent
            // failure (bundle too large, peer 404, transport) would
            // otherwise loop silently at the default log level.
            tracing::warn!(
                project_id = %name,
                holders = holders.len(),
                error = %last_err.as_deref().unwrap_or("(none)"),
                "gitback catch-up: no announced holder served the bundle this tick; will retry"
            );
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
                let base = crate::platform::paths::default_gitback_dir();
                let now = unix_seconds_now();
                match envelopes::reap_all(&base, now, envelopes::effective_retention_secs()) {
                    Ok(0) => {}
                    Ok(n) => tracing::info!(reaped = n, "gitback: reaped expired envelopes"),
                    Err(e) => tracing::warn!(error = %e, "gitback reap reactor: error"),
                }
            }
        }
    }
}

/// How often the prune reactor evicts stale entries from the four
/// per-peer in-memory caches (`PeerPubkeyCache`, `PeerTable`,
/// `ProjectPeers`, `ClaimsHandle`). 60 s matches the lockbox
/// `PRUNE_INTERVAL` so operators learn one cadence across all
/// agent-side periodic batching.
const PRUNE_INTERVAL: std::time::Duration = std::time::Duration::from_secs(60);

/// Periodic eviction of stale per-peer in-memory state. Each of the
/// four caches defines its own TTL — this reactor just drives them
/// on a shared tick. Bounded memory under a peer-flood (e.g. burst
/// loadtests, post-outage thundering herd) depends entirely on this
/// running: the pre-fix design defined every prune function but
/// wired none of them into a periodic task, so a 25K-envoy burst
/// could leave a 3+ GB heap behind. See `tshoot/log.md` (the
/// "vigo agent on danlap holds 3.5 GB RSS" investigation).
async fn run_prune_reactor(
    cancel: CancellationToken,
    peer_pubkey_cache: std::sync::Arc<peer_pubkey_cache::PeerPubkeyCache>,
    peer_table: std::sync::Arc<peer_table::PeerTable>,
    claims: receiver::ClaimsHandle,
) {
    let mut tick = tokio::time::interval(PRUNE_INTERVAL);
    tick.tick().await;
    loop {
        tokio::select! {
            _ = cancel.cancelled() => return,
            _ = tick.tick() => {
                let now_i64 = unix_seconds_now();
                let now_u64 = now_i64 as u64;

                let pubkey_cache_evicted =
                    peer_pubkey_cache.gc(now_i64, peer_pubkey_cache::PUBKEY_CACHE_TTL_SECS);
                let peer_table_evicted = peer_table.prune_expired_at(now_u64);
                let project_peers_evicted = match project_peers::global() {
                    Some(p) => p.prune_stale(now_i64),
                    None => 0,
                };
                let claims_evicted = claims.prune_lan_expired(now_u64);

                if pubkey_cache_evicted
                    | peer_table_evicted
                    | project_peers_evicted
                    | claims_evicted
                    != 0
                {
                    tracing::info!(
                        peer_pubkeys = pubkey_cache_evicted,
                        peer_table = peer_table_evicted,
                        project_peers = project_peers_evicted,
                        claims = claims_evicted,
                        "gitback prune: evicted stale per-peer entries",
                    );
                }
            }
        }
    }
}

fn scan_all_users(
    gitback_base: &std::path::Path,
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
        let dot_dir = home.join(".vigo-gitback");
        if !dot_dir.exists() {
            continue;
        }
        scan_user_dir(gitback_base, &home, &dot_dir, tls);
    }
    Ok(())
}

/// Walk every user's `~/.vigo-gitback/<pid>/` and reconcile each
/// project's envelopes against known peers via the `/digest` ↔
/// `/all` flow. Best-effort; per-peer errors stay local.
fn reconcile_all_users(
    gitback_base: &std::path::Path,
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
        let dot_dir = home.join(".vigo-gitback");
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
                    fan_out::reconcile_envelopes(gitback_base, &home, &pid, tls);
                }
            }
        }
    }
    Ok(())
}

fn scan_user_dir(
    gitback_base: &std::path::Path,
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
            if let Err(e) = process_leave_sentinel(gitback_base, home, &leave, tls) {
                tracing::warn!(
                    path = %leave.display(),
                    error = %e,
                    "gitback: leave sentinel error"
                );
            }
        }
        let revoke = project_dir.join("revoke.pending");
        if revoke.exists() {
            if let Err(e) = process_revoke_sentinel(gitback_base, home, &revoke, tls) {
                tracing::warn!(
                    path = %revoke.display(),
                    error = %e,
                    "gitback: revoke sentinel error"
                );
            }
        }
    }
}

fn process_leave_sentinel(
    gitback_base: &std::path::Path,
    home: &std::path::Path,
    path: &std::path::Path,
    tls: Option<&crate::update::TlsCerts>,
) -> anyhow::Result<()> {
    use anyhow::Context;
    let bytes = std::fs::read(path)
        .with_context(|| format!("read {}", path.display()))?;
    let envelope: envelopes::LeaveEnvelope = serde_json::from_slice(&bytes)
        .with_context(|| format!("parse leave envelope at {}", path.display()))?;
    envelopes::save_leave(gitback_base, &envelope)
        .with_context(|| format!("persist leave envelope from {}", path.display()))?;
    let _ = std::fs::remove_file(path);
    tracing::info!(
        path = %path.display(),
        project_id = %envelope.project_id,
        "gitback: persisted LEAVE envelope from sentinel"
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
                    "gitback: gossiped LEAVE envelope"
                );
            }
        }
    }
    Ok(())
}

fn process_revoke_sentinel(
    gitback_base: &std::path::Path,
    home: &std::path::Path,
    path: &std::path::Path,
    tls: Option<&crate::update::TlsCerts>,
) -> anyhow::Result<()> {
    use anyhow::Context;
    let bytes = std::fs::read(path)
        .with_context(|| format!("read {}", path.display()))?;
    let envelope: envelopes::RevokeEnvelope = serde_json::from_slice(&bytes)
        .with_context(|| format!("parse revoke envelope at {}", path.display()))?;
    envelopes::save_revoke(gitback_base, &envelope)
        .with_context(|| format!("persist revoke envelope from {}", path.display()))?;
    let _ = std::fs::remove_file(path);
    tracing::info!(
        path = %path.display(),
        project_id = %envelope.project_id,
        token_id = %envelope.token_id,
        "gitback: persisted REVOKE envelope from sentinel"
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
                    "gitback: gossiped REVOKE envelope"
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

/// Check whether gitback should be active on this envoy right now.
///
/// Thin wrapper over [`crate::swarm::gates::substrate_active`] —
/// applies the global `swarm_enabled` AND `swarm_gitback_enabled`
/// rule from the cached `PolicyBundle.vars_json`. The server has
/// already resolved `swarm.gitback.enabled` pattern matching for
/// this envoy's hostname; the agent only sees the resolved bools.
/// See `crate::swarm::gates` for the fail-open semantics and
/// recognised value shapes.
pub fn gitback_enabled_for_envoy(state_store: Option<&crate::state::StateStore>) -> bool {
    crate::swarm::gates::substrate_active(
        crate::swarm::gates::Substrate::Gitback,
        state_store,
    )
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::swarm::anchor::tokens;
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
