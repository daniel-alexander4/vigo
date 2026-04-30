// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Gitmirror — ADR-015 unified-substrate git over swarm.
//!
//! Each project has a stable, content-addressed identity:
//! `project_id = sha256(founder_fleet_pubkey)`. Projects are accessed
//! over a `vigo://<project_id>/<handle>` URL via the multi-call
//! `git-remote-vigo` helper (ADR-015 step 1, shipped 0.34.9). The
//! `vigo` agent verifies a stack of capability tokens (step 2, shipped
//! 0.34.10) against the project's anchor pubkey, then proxies to
//! `git-receive-pack` / `git-upload-pack` against a root-owned bare
//! repo at `/var/lib/vigo/swarm/gitmirror/projects/<project_id>.git`
//! (mode `0700 root:root`).
//!
//! Project shape is described by two orthogonal axes:
//!
//! | `members:`           | `dr_fanout:`   | Use case                      |
//! |----------------------|----------------|-------------------------------|
//! | `{my-fleet}`         | `fleet-wide`   | Personal-DR (today's default) |
//! | `{my-fleet}`         | `members-only` | Single-user, single-fleet     |
//! | `{team1, team2, …}`  | `members-only` | Multi-member collaboration    |
//! | `{team1, team2, …}`  | `fleet-wide`   | Team repo + fleet-wide DR     |
//!
//! ### Module layout (post-ADR-015 step 3)
//!
//! - [`tokens`] — capability-token primitives (Ed25519, deterministic
//!   canonical encoding, content-addressed token IDs, chain
//!   verification). Shipped at 0.34.10.
//! - [`project_state`] — per-user state at
//!   `~/.vigo-gitmirror/<project_id>/` (project config + token chain)
//!   and bare-repo path helpers under
//!   `/var/lib/vigo/swarm/gitmirror/projects/`.
//! - [`cli`] — the `vigo swarm gitmirror project {init,invite,join,
//!   leave,list,revoke,reanchor,status}` verb tree.
//! - [`gate`] — runtime gate consulted before any swarm-side gitmirror
//!   work runs.
//! - [`git_ops`] — the only place that spawns `git`; uniform hardening
//!   policy on every invocation.
//! - [`peer_table`], [`peer_pubkey_cache`] — swarm peer infrastructure
//!   shared with the rest of the gitmirror substrate.
//!
//! [`receiver`], [`pending_claims`], and [`state`] survive as
//! transitional dependencies of the discovery / swarm-server transport
//! code in `crate::swarm::{discovery, server}`. They no longer drive
//! any reactor work — `spawn()` is a no-op pending the helper-daemon
//! rewrite (ADR-015 step 4).

pub mod cli;
pub mod gate;
pub mod git_ops;
pub mod peer_pubkey_cache;
pub mod peer_table;
pub mod pending_claims;
pub mod project_state;
pub mod receiver;
pub mod state;
pub mod tokens;

pub use gate::Gate;

use tokio_util::sync::CancellationToken;
use tracing::info;

/// Maximum git bundle size gitmirror will accept on the swarm peer
/// port, in bytes.
///
/// 20 GiB. Bundles are git history, not working-tree contents, so
/// even a large personal repo with long history typically fits in a
/// few hundred megabytes. The cap exists to prevent unbounded growth
/// from a misconfigured source, not to be tight. Enforced by the
/// swarm peer-port HTTP server on inbound bundle PUTs.
pub const MAX_BUNDLE_SIZE: u64 = 20 * 1024 * 1024 * 1024;

/// Maximum total distinct mirrors a single receiver will hold across
/// every source on the legacy claim-based receiver path. Retained as
/// a transitional constant while [`receiver`] still serves the swarm
/// peer-port handler; the new helper-daemon-driven path lands in
/// ADR-015 step 4.
pub const MAX_MIRRORS_PER_RECEIVER: usize = 50;

/// Maximum mirrors one source envoy can register on a single receiver
/// (transitional, see [`MAX_MIRRORS_PER_RECEIVER`]).
pub const MAX_PER_SOURCE_MIRRORS: usize = 20;

/// Staleness threshold for a materialized mirror, in seconds (30 days).
/// Transitional — used by the legacy receiver-side state surface.
pub const STALE_THRESHOLD_SECS: u64 = 30 * 24 * 60 * 60;

/// Separator character between segments of a legacy gitmirror swarm
/// label. Pre-ADR-015 labels were `gitmirror:<host>:<user>:<project>`;
/// retained while the swarm peer-port handler still parses them.
pub const LABEL_SEP: char = ':';

/// Label namespace for legacy gitmirror content (bundle) seeds.
pub const CONTENT_LABEL_PREFIX: &str = "gitmirror";

/// Spawn the gitmirror subsystem.
///
/// **Step 3 (post-ADR-015 storage + CLI cutover)**: this is currently
/// a no-op. The legacy publish-and-claim reactor that scanned every
/// user's `~/gitmirror/` is gone; the new push-driven helper-daemon
/// reactor lands in step 4 of the ADR-015 arc. The function and its
/// arguments stay in place so `agent/src/main.rs` keeps a stable call
/// site through the cutover.
///
/// `_claims` and `_manifest` enable the receiver side of the swarm
/// peer-port transport. Both `Some` in production wiring (see
/// `agent/src/main.rs`); both `None` in publish-only deployments
/// (e.g. swarm subsystem disabled) or test fixtures.
#[allow(clippy::too_many_arguments)]
pub fn spawn(
    _tls: Option<crate::update::TlsCerts>,
    _cancel: CancellationToken,
    _claims: Option<receiver::ClaimsHandle>,
    _manifest: Option<std::sync::Arc<crate::swarm::manifest::Manifest>>,
    _state_store: Option<std::sync::Arc<crate::state::StateStore>>,
    _peer_table: Option<std::sync::Arc<peer_table::PeerTable>>,
    _pending_claims: Option<std::sync::Arc<pending_claims::PendingClaims>>,
    gate: Gate,
) {
    info!(
        initial_gate = gate.enabled(),
        "gitmirror: reactor wiring deferred to ADR-015 step 4 (helper daemon)"
    );
}

/// Check whether gitmirror should be active on this envoy right now.
///
/// Thin wrapper over [`crate::swarm::gates::substrate_active`] —
/// applies the global `swarm_enabled` AND `swarm_gitmirror_enabled`
/// AND NOT `gitmirror_blacklisted` rule from the cached
/// `PolicyBundle.vars_json`. See `crate::swarm::gates` for the
/// fail-open semantics and recognised value shapes.
pub fn gitmirror_enabled_for_envoy(state_store: Option<&crate::state::StateStore>) -> bool {
    crate::swarm::gates::substrate_active(
        crate::swarm::gates::Substrate::Gitmirror,
        state_store,
    )
}
