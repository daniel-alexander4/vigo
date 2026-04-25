// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Cairn — git-commit-triggered repo sync over swarm.
//!
//! Cairn auto-syncs a small number of a user's personal git repos
//! from one "source" envoy to a bounded handful of other envoys where
//! the same user has an account. The trigger is `git commit`, not
//! `git push`. Receivers are read-only mirrors: they hold a bare repo
//! under `/var/lib/vigo/cairn/mirrors/<seeder>/<user>/<project>.git`.
//! Users recover into a working clone on demand via
//! `vigo cairn recover <project>`.
//!
//! Cairn is a personal-DR tool, not a fleet distribution mechanism.
//! Fleet-wide content distribution is what modules and `file:` resources
//! are for. Cairn carries history so a user can `git clone` off any
//! mirror to recover their work after losing their primary machine.
//!
//! Configuration lives entirely in a mandatory `.cairn.yaml` committed
//! to the root of each synced repo. See [`sidecar`].
//!
//! Hard cap enforcement for `max_mirrors` runs agent-side: the LAN
//! selector ([`lan_selector`]) picks a deterministic subset of
//! discovered peers via sha256 scoring, and the receiver enforces
//! per-receiver and per-source caps. See `docs/concepts/cairn.md`.
//!
//! ### Hardcoded constants
//!
//! [`MIN_MIRRORS`] and [`MAX_MIRRORS`] bound the valid range of the
//! per-repo `max_mirrors` sidecar field. These are immutable and
//! cannot be overridden by configuration, CLI flag, env var, or
//! sidecar value.
//!
//! ### Module layout
//!
//! - [`sidecar`] — strict parser + validator for `.cairn.yaml`.
//! - [`path_guard`] — canonicalization + containment for source and
//!   receiver-derived paths.
//! - [`git_ops`] — the only place that spawns `git`; applies a fixed
//!   hardening policy on every invocation.
//! - [`state`] — on-disk state (`state.json` per-label seeding state,
//!   bundle tmp dir, orphan cleanup).
//! - [`source`] — discovery scanner: walks every user's
//!   `~/cairn/*` and produces a [`source::Source`] per validated
//!   repo.
//! - [`publisher`] — bundle creation + content-seed swarm publishing
//!   for every non-paused [`source::Source`].
//! - [`reactor`] — tokio task that wakes every
//!   [`reactor::TICK_INTERVAL`] to re-scan sources and re-run the
//!   publisher. Polling was chosen over inotify ref-watching; see the
//!   `reactor` module docs for the rationale.
//!
//! - [`receiver`] — materializes claimed bundles into root-owned bare
//!   repos, handles staleness detection and orphan cleanup.

pub mod cli;
pub mod cross_subnet_mirrors;
pub mod gate;
pub mod git_ops;
pub mod lan_selector;
pub mod path_guard;
pub mod peer_pubkey_cache;
pub mod peer_table;
pub mod pending_claims;
pub mod publisher;
pub mod reactor;
pub mod receiver;
pub mod sidecar;
pub mod source;
pub mod source_cli;
pub mod state;

pub use gate::Gate;

/// Separator character between segments of a cairn swarm label.
///
/// Labels are `cairn:<host>:<user>:<project>`. The separator is `:`
/// because the label is also the seed *filename* in
/// `default_seeds_dir()`, and `/` is the only filename-illegal
/// character on Linux. `:` is not a legal hostname character per
/// [`sidecar::is_valid_hostname`], not a legal Linux username
/// character (it's the `/etc/passwd` field separator), and is
/// rejected from project names by
/// [`path_guard::validate_project_name`].
pub const LABEL_SEP: char = ':';

/// Label namespace for cairn content (bundle) seeds.
///
/// Cairn no longer publishes a paired meta seed: the validated
/// sidecar fields are routed through the `cairn` trait
/// ([`state::SourceTraitEntry`]) instead of a separate swarm blob.
/// This was locked in at commit `c692d86e` and the dead meta-seed
/// publishing was removed in the receiver-precursor commit. Server
/// consumers read sidecar data from the trait; receivers download
/// only the content seed.
pub const CONTENT_LABEL_PREFIX: &str = "cairn";

/// Build the content-seed label for a `(hostname, user, project)`
/// triple. The hostname is lowercased to match the case-insensitive
/// owner check in [`source::scan`].
pub fn content_label(hostname: &str, user: &str, project: &str) -> String {
    format!(
        "{}{sep}{}{sep}{}{sep}{}",
        CONTENT_LABEL_PREFIX,
        hostname.to_lowercase(),
        user,
        project,
        sep = LABEL_SEP
    )
}

use tokio_util::sync::CancellationToken;
use tracing::{info, warn};

/// Minimum non-source mirrors cairn will target.
///
/// DR safety requires at least 3 redundant copies. This is a
/// **target**, not a hard gate: if the candidate pool has fewer than
/// `MIN_MIRRORS` envoys with the target user, cairn materializes on
/// whatever is available and the server emits a
/// `cairn.under_replicated` trait. The floor is immutable and
/// cannot be overridden by configuration, CLI flag, env var, or
/// sidecar value.
pub const MIN_MIRRORS: usize = 3;

/// Maximum non-source mirrors cairn will enforce.
///
/// Cairn is a personal-DR tool, not a fleet distribution mechanism.
/// Normal per-repo mirror count is 3-5; power users may legitimately
/// want up to 10. Beyond 10 is almost certainly a misconfiguration.
/// This ceiling is immutable and cannot be overridden.
pub const MAX_MIRRORS: usize = 10;

/// Maximum git bundle size cairn will publish or accept, in bytes.
///
/// 20 GiB. Bundles are git history, not working-tree contents, so
/// even a large personal repo with long history typically fits in a
/// few hundred megabytes. The cap is deliberately very high so it
/// never bounds a legitimate personal-DR use case — it exists to
/// prevent unbounded growth from a misconfigured source, not to be
/// tight. Enforced on the publish side (refuse to publish oversize
/// bundles) and the receive side (refuse to accept them).
pub const MAX_BUNDLE_SIZE: u64 = 20 * 1024 * 1024 * 1024;

/// Maximum total distinct mirrors a single receiver will hold across
/// all sources.
///
/// 50. The implicit storage ceiling is `MAX_BUNDLE_SIZE ×
/// MAX_MIRRORS_PER_RECEIVER = 1 TiB`, but in practice a homelab
/// receiver will hold 1-5 mirrors; the cap exists to catch runaway
/// behavior from a compromised or misconfigured fleet. Immutable.
pub const MAX_MIRRORS_PER_RECEIVER: usize = 50;

/// Maximum mirrors one source envoy can register on a single receiver.
///
/// 20. Stops a single compromised source envoy from filling the
/// whole `MAX_MIRRORS_PER_RECEIVER` budget by itself. Matches the
/// sidecar-level `MAX_MIRRORS = 10` ceiling with headroom to grow.
/// Immutable.
pub const MAX_PER_SOURCE_MIRRORS: usize = 20;

/// How long a materialized mirror may go without a successful refresh
/// before it's reported as stale, in seconds. 30 days.
///
/// Staleness is advisory only: the mirror is NOT auto-evicted — the
/// whole point of cairn is that the backup is still reachable
/// when the source envoy is dead, and auto-pruning would destroy the
/// backup at the moment it's most valuable. The receiver reactor
/// emits a rate-limited `warn!` log line and surfaces stale labels
/// through the `cairn.stale_mirrors` trait; manual cleanup is
/// available via `vigo cairn prune`.
pub const STALE_THRESHOLD_SECS: u64 = 30 * 24 * 60 * 60;

/// Spawn the cairn subsystem.
///
/// The reactor task is spawned unconditionally and consults the
/// shared [`Gate`] on every tick — live flips of the matchfile var
/// `cairn: true` take effect without restarting the agent. The
/// reactor runs no publish / receive work while the gate is off and
/// runs the per-label state reset on every OFF→ON transition so a
/// re-enabled envoy emits a full bundle on its first publish instead
/// of an unreachable delta.
///
/// `claims` and `manifest` enable the receiver side. Both `Some`
/// in production wiring (see `agent/src/main.rs`); both `None` in
/// publish-only deployments (e.g. swarm subsystem disabled) or
/// test fixtures.
pub fn spawn(
    tls: Option<crate::update::TlsCerts>,
    cancel: CancellationToken,
    claims: Option<receiver::ClaimsHandle>,
    manifest: Option<std::sync::Arc<crate::swarm::manifest::Manifest>>,
    _state_store: Option<std::sync::Arc<crate::state::StateStore>>,
    peer_table: Option<std::sync::Arc<peer_table::PeerTable>>,
    pending_claims: Option<std::sync::Arc<pending_claims::PendingClaims>>,
    gate: Gate,
) {
    info!(
        min_mirrors = MIN_MIRRORS,
        max_mirrors = MAX_MIRRORS,
        initial_gate = gate.enabled(),
        "cairn: reactor spawning (runtime gate)"
    );
    reactor::spawn(cancel, claims, manifest, peer_table, pending_claims, tls, gate);
}

/// Activation work that runs on OFF→ON transitions (including the
/// first tick when the reactor boots with the gate already on).
///
/// Returns `Ok(())` when the subsystem is clear to seed this tick.
/// Returns `Err` when the git version check fails — the reactor
/// treats the subsystem as dormant for that tick and retries on
/// subsequent ticks without needing an external reset.
pub(crate) fn activate() -> Result<(), String> {
    let state_dir = crate::platform::paths::default_cairn_dir();
    state::cleanup_stale_bundles(&state_dir);

    // Reset per-label publisher state on every activation. Guarantees
    // the first publish after turning on emits a full bundle rather
    // than a delta whose base commit could be unreachable to
    // receivers. Cost: one full bundle per source per activation;
    // activations are rare in normal operation.
    let mut state = state::State::load(&state_dir);
    if !state.labels.is_empty() {
        info!(
            labels = state.labels.len(),
            "cairn: resetting per-label publisher state — next publish will be a full bundle"
        );
        state.reset_all_labels();
        if let Err(e) = state.save(&state_dir) {
            warn!(
                error = %e,
                "cairn: failed to persist reset state.json; next publish may be a stale delta"
            );
        }
    }

    match git_ops::check_version() {
        Ok((major, minor)) => {
            info!(major, minor, "cairn: git version check passed");
            Ok(())
        }
        Err(e) => {
            let msg = format!("git version check failed: {e}");
            warn!(
                error = %e,
                "cairn: git version check failed; subsystem stays dormant until resolved"
            );
            Err(msg)
        }
    }
}

/// Check the cached `PolicyBundle.vars_json` for `cairn: true`.
///
/// Returns `false` (disabled) in every ambiguous case:
/// - No state store available (startup failure).
/// - State store read fails.
/// - No cached policy bundle yet (fresh enrollment).
/// - Empty `vars_json`.
/// - `vars_json` doesn't parse as a JSON object.
/// - `cairn` key missing, or present with a non-truthy value.
///
/// Accepted truthy shapes for `cairn`:
/// - JSON boolean `true`.
/// - JSON string `"true"`, `"yes"`, or `"1"` (case-insensitive).
/// - JSON number `1` (or any non-zero integer).
///
/// Everything else — including absence — means "disabled". The flag
/// is **opt-in**: operators must set `vars: { cairn: true }` in a
/// matchfile or `common.vgo` for the subsystem to start.
///
/// Public: `agent/src/main.rs` calls this before wiring the swarm
/// discovery subsystem so it can gate the LAN peer announcement /
/// peer-table construction on the same matchfile var that gates the
/// subsystem itself. Keeping the two checks in sync via a shared
/// helper avoids the bug where a dormant envoy still announces
/// itself as a cairn peer.
pub fn matchfile_enables_cairn(state_store: Option<&crate::state::StateStore>) -> bool {
    let Some(store) = state_store else {
        warn!("cairn: no state store available at startup; subsystem disabled");
        return false;
    };

    let rtxn = match store.read_txn() {
        Ok(r) => r,
        Err(e) => {
            warn!(error = %e, "cairn: could not open state store read txn; subsystem disabled");
            return false;
        }
    };

    let bundle_bytes = match store.policy.load(&rtxn) {
        Ok(Some((_, b))) => b,
        Ok(None) => {
            info!("cairn: no cached policy bundle yet (fresh enrollment); subsystem disabled until next restart after first check-in");
            return false;
        }
        Err(e) => {
            warn!(error = %e, "cairn: could not read cached policy bundle; subsystem disabled");
            return false;
        }
    };

    vars_json_has_cairn_true(&bundle_bytes.vars_json)
}

/// Parse a `vars_json` byte slice and return `true` iff the `cairn`
/// key is set to a truthy value. Broken out of
/// [`matchfile_enables_cairn`] so tests can exercise the parse
/// logic without stubbing an entire `StateStore`.
fn vars_json_has_cairn_true(vars_json: &[u8]) -> bool {
    if vars_json.is_empty() {
        return false;
    }

    let val: serde_json::Value = match serde_json::from_slice(vars_json) {
        Ok(v) => v,
        Err(e) => {
            warn!(error = %e, "cairn: vars_json parse failed; subsystem disabled");
            return false;
        }
    };

    let Some(v) = val.get("cairn") else {
        return false;
    };

    match v {
        serde_json::Value::Bool(b) => *b,
        serde_json::Value::String(s) => matches!(
            s.to_ascii_lowercase().as_str(),
            "true" | "yes" | "1"
        ),
        serde_json::Value::Number(n) => n.as_i64().map_or(false, |i| i != 0),
        _ => false,
    }
}

#[cfg(test)]
mod gate_tests {
    use super::vars_json_has_cairn_true;

    #[test]
    fn empty_vars_json_is_disabled() {
        assert!(!vars_json_has_cairn_true(b""));
    }

    #[test]
    fn missing_cairn_key_is_disabled() {
        assert!(!vars_json_has_cairn_true(br#"{"other": true}"#));
    }

    #[test]
    fn cairn_true_bool_enables() {
        assert!(vars_json_has_cairn_true(br#"{"cairn": true}"#));
    }

    #[test]
    fn cairn_false_bool_is_disabled() {
        assert!(!vars_json_has_cairn_true(br#"{"cairn": false}"#));
    }

    #[test]
    fn cairn_string_true_enables() {
        assert!(vars_json_has_cairn_true(br#"{"cairn": "true"}"#));
        assert!(vars_json_has_cairn_true(br#"{"cairn": "True"}"#));
        assert!(vars_json_has_cairn_true(br#"{"cairn": "YES"}"#));
        assert!(vars_json_has_cairn_true(br#"{"cairn": "1"}"#));
    }

    #[test]
    fn cairn_string_false_is_disabled() {
        assert!(!vars_json_has_cairn_true(br#"{"cairn": "false"}"#));
        assert!(!vars_json_has_cairn_true(br#"{"cairn": "no"}"#));
        assert!(!vars_json_has_cairn_true(br#"{"cairn": "0"}"#));
        assert!(!vars_json_has_cairn_true(br#"{"cairn": ""}"#));
    }

    #[test]
    fn cairn_number_nonzero_enables() {
        assert!(vars_json_has_cairn_true(br#"{"cairn": 1}"#));
        assert!(vars_json_has_cairn_true(br#"{"cairn": 42}"#));
    }

    #[test]
    fn cairn_number_zero_is_disabled() {
        assert!(!vars_json_has_cairn_true(br#"{"cairn": 0}"#));
    }

    #[test]
    fn cairn_null_is_disabled() {
        assert!(!vars_json_has_cairn_true(br#"{"cairn": null}"#));
    }

    #[test]
    fn malformed_json_is_disabled() {
        assert!(!vars_json_has_cairn_true(b"{not valid json"));
    }

    #[test]
    fn non_object_root_is_disabled() {
        assert!(!vars_json_has_cairn_true(br#"["cairn"]"#));
        assert!(!vars_json_has_cairn_true(br#""cairn""#));
    }

    #[test]
    fn cairn_coexists_with_other_vars() {
        let vars = br#"{"app_name": "web", "cairn": true, "timeout": 30}"#;
        assert!(vars_json_has_cairn_true(vars));
    }
}
