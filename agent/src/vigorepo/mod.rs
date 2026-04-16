// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Vigorepo — git-commit-triggered repo sync over swarm.
//!
//! Vigorepo auto-syncs a small number of a user's personal git repos
//! from one "source" envoy to a bounded handful of other envoys where
//! the same user has an account. The trigger is `git commit`, not
//! `git push`. Receivers are read-only mirrors: they hold a bare repo
//! under `/var/lib/vigo/vigorepo/<seeder>/<user>/<project>.git` and a
//! checked-out working tree at `~<user>/vigorepo/<project>/` with no
//! `.git/` directory inside it.
//!
//! Vigorepo is a personal-DR tool, not a fleet distribution mechanism.
//! Fleet-wide content distribution is what modules and `file:` resources
//! are for. Vigorepo carries history so a user can `git clone` off any
//! mirror to recover their work after losing their primary machine.
//!
//! Configuration lives entirely in a mandatory `.vigorepo.yaml` committed
//! to the root of each synced repo. See [`sidecar`].
//!
//! Hard cap enforcement for `max_mirrors` is delegated to the server's
//! vigorepo ClaimTable, which selects a deterministic subset of candidate
//! envoys and delivers the selection to agents via a new
//! `vigorepo_claims` field on the check-in response. See the plan at
//! `docs/concepts/vigorepo-claim-table.md` (forthcoming).
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
//! - [`sidecar`] — strict parser + validator for `.vigorepo.yaml`.
//! - [`path_guard`] — canonicalization + containment for source and
//!   receiver-derived paths.
//! - [`git_ops`] — the only place that spawns `git`; applies a fixed
//!   hardening policy on every invocation.
//! - [`state`] — on-disk state (`state.json` per-label seeding state,
//!   bundle tmp dir, orphan cleanup).
//! - [`source`] — discovery scanner: walks every user's
//!   `~/vigorepo/*` and produces a [`source::Source`] per validated
//!   repo.
//! - [`publisher`] — bundle creation + dual-seed (meta + content)
//!   swarm publishing for every non-paused [`source::Source`].
//! - [`reactor`] — tokio task that wakes every
//!   [`reactor::TICK_INTERVAL`] to re-scan sources and re-run the
//!   publisher. Polling was chosen over inotify ref-watching; see the
//!   `reactor` module docs for the rationale.
//!
//! `receiver.rs` will be added in a later commit once the server's
//! ClaimTable + check-in proto field land.

pub mod cli;
pub mod git_ops;
pub mod path_guard;
pub mod publisher;
pub mod reactor;
pub mod receiver;
pub mod sidecar;
pub mod source;
pub mod state;

/// Separator character between segments of a vigorepo swarm label.
///
/// Labels are `vigorepo:<host>:<user>:<project>`. The separator is `:`
/// because the label is also the seed *filename* in
/// `default_seeds_dir()`, and `/` is the only filename-illegal
/// character on Linux. `:` is not a legal hostname character per
/// [`sidecar::is_valid_hostname`], not a legal Linux username
/// character (it's the `/etc/passwd` field separator), and is
/// rejected from project names by
/// [`path_guard::validate_project_name`].
pub const LABEL_SEP: char = ':';

/// Label namespace for vigorepo content (bundle) seeds.
///
/// Vigorepo no longer publishes a paired meta seed: the validated
/// sidecar fields are routed through the `vigorepo` trait
/// ([`state::SourceTraitEntry`]) instead of a separate swarm blob.
/// This was locked in at commit `c692d86e` and the dead meta-seed
/// publishing was removed in the receiver-precursor commit. Server
/// consumers read sidecar data from the trait; receivers download
/// only the content seed.
pub const CONTENT_LABEL_PREFIX: &str = "vigorepo";

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

/// Minimum non-source mirrors vigorepo will target.
///
/// DR safety requires at least 3 redundant copies. This is a
/// **target**, not a hard gate: if the candidate pool has fewer than
/// `MIN_MIRRORS` envoys with the target user, vigorepo materializes on
/// whatever is available and the server emits a
/// `vigorepo.under_replicated` trait. The floor is immutable and
/// cannot be overridden by configuration, CLI flag, env var, or
/// sidecar value.
pub const MIN_MIRRORS: usize = 3;

/// Maximum non-source mirrors vigorepo will enforce.
///
/// Vigorepo is a personal-DR tool, not a fleet distribution mechanism.
/// Normal per-repo mirror count is 3-5; power users may legitimately
/// want up to 10. Beyond 10 is almost certainly a misconfiguration.
/// This ceiling is immutable and cannot be overridden.
pub const MAX_MIRRORS: usize = 10;

/// Spawn the vigorepo subsystem.
///
/// Synchronous startup work (runs before returning):
///
/// - Clean up orphan bundle tmp files from previous runs.
/// - Run the git version check. On failure, log a warning and skip
///   spawning the reactor — vigorepo refuses to seed until resolved.
///
/// If the version check passes, a single [`reactor`] task is
/// spawned. The reactor owns `cancel` and wakes every
/// [`reactor::TICK_INTERVAL`] to re-run discovery + publish + the
/// receiver tick.
///
/// `claims` and `manifest` enable the receiver side. Both `Some`
/// in production wiring (see `agent/src/main.rs`); both `None` in
/// publish-only deployments (e.g. swarm subsystem disabled) or
/// test fixtures.
pub fn spawn(
    _tls: Option<crate::update::TlsCerts>,
    cancel: CancellationToken,
    claims: Option<receiver::ClaimsHandle>,
    manifest: Option<std::sync::Arc<crate::swarm::manifest::Manifest>>,
    state_store: Option<std::sync::Arc<crate::state::StateStore>>,
) {
    // Opt-in gate: vigorepo only runs on envoys whose matchfile sets
    // `vars: { vigorepo: true }`. The flag is delivered to the agent
    // via `PolicyBundle.vars_json` on every check-in and cached in
    // the LMDB state store for offline convergence. This function
    // reads the cached copy at subsystem startup; live flips take
    // effect on the next agent restart.
    //
    // Fresh-enrollment edge case: the cache is empty until the first
    // check-in lands, so a just-enrolled envoy stays dormant through
    // its first agent process. On the next restart (after the first
    // check-in wrote the cache), the subsystem spawns if enabled.
    // One extra restart during enrollment; never an auto-participate
    // surprise.
    if !matchfile_enables_vigorepo(state_store.as_deref()) {
        info!(
            "vigorepo: disabled on this envoy (matchfile var `vigorepo` is not true); \
             set `vars: {{ vigorepo: true }}` in a matchfile or common.vgo to enable"
        );
        return;
    }

    info!(
        min_mirrors = MIN_MIRRORS,
        max_mirrors = MAX_MIRRORS,
        "vigorepo: subsystem starting"
    );

    let state_dir = crate::platform::paths::default_vigorepo_dir();
    state::cleanup_stale_bundles(&state_dir);

    // Reset per-label publisher state on every startup. This
    // guarantees the first publish pass after restart generates a
    // full bundle rather than a delta whose base commit could be
    // unreachable to receivers (e.g. the previous full was replaced
    // by a delta mid-session and never downloaded). Cost: one full
    // bundle per source per restart; bounds the window of "stale
    // baseline" risk to the lifetime of a single agent process.
    let mut state = state::State::load(&state_dir);
    if !state.labels.is_empty() {
        info!(
            labels = state.labels.len(),
            "vigorepo: resetting per-label publisher state at startup — next publish will be a full bundle"
        );
        state.reset_all_labels();
        if let Err(e) = state.save(&state_dir) {
            warn!(
                error = %e,
                "vigorepo: failed to persist reset state.json; reactor will still run but next publish may be a stale delta"
            );
        }
    }

    match git_ops::check_version() {
        Ok((major, minor)) => {
            info!(major, minor, "vigorepo: git version check passed");
        }
        Err(e) => {
            warn!(
                error = %e,
                "vigorepo: git version check failed; reactor will not start until resolved"
            );
            return;
        }
    }

    reactor::spawn(cancel, claims, manifest);
}

/// Check the cached `PolicyBundle.vars_json` for `vigorepo: true`.
///
/// Returns `false` (disabled) in every ambiguous case:
/// - No state store available (startup failure).
/// - State store read fails.
/// - No cached policy bundle yet (fresh enrollment).
/// - Empty `vars_json`.
/// - `vars_json` doesn't parse as a JSON object.
/// - `vigorepo` key missing, or present with a non-truthy value.
///
/// Accepted truthy shapes for `vigorepo`:
/// - JSON boolean `true`.
/// - JSON string `"true"`, `"yes"`, or `"1"` (case-insensitive).
/// - JSON number `1` (or any non-zero integer).
///
/// Everything else — including absence — means "disabled". The flag
/// is **opt-in**: operators must set `vars: { vigorepo: true }` in a
/// matchfile or `common.vgo` for the subsystem to start.
fn matchfile_enables_vigorepo(state_store: Option<&crate::state::StateStore>) -> bool {
    let Some(store) = state_store else {
        warn!("vigorepo: no state store available at startup; subsystem disabled");
        return false;
    };

    let rtxn = match store.read_txn() {
        Ok(r) => r,
        Err(e) => {
            warn!(error = %e, "vigorepo: could not open state store read txn; subsystem disabled");
            return false;
        }
    };

    let bundle_bytes = match store.policy.load(&rtxn) {
        Ok(Some((_, b))) => b,
        Ok(None) => {
            info!("vigorepo: no cached policy bundle yet (fresh enrollment); subsystem disabled until next restart after first check-in");
            return false;
        }
        Err(e) => {
            warn!(error = %e, "vigorepo: could not read cached policy bundle; subsystem disabled");
            return false;
        }
    };

    vars_json_has_vigorepo_true(&bundle_bytes.vars_json)
}

/// Parse a `vars_json` byte slice and return `true` iff the `vigorepo`
/// key is set to a truthy value. Broken out of
/// [`matchfile_enables_vigorepo`] so tests can exercise the parse
/// logic without stubbing an entire `StateStore`.
fn vars_json_has_vigorepo_true(vars_json: &[u8]) -> bool {
    if vars_json.is_empty() {
        return false;
    }

    let val: serde_json::Value = match serde_json::from_slice(vars_json) {
        Ok(v) => v,
        Err(e) => {
            warn!(error = %e, "vigorepo: vars_json parse failed; subsystem disabled");
            return false;
        }
    };

    let Some(v) = val.get("vigorepo") else {
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
    use super::vars_json_has_vigorepo_true;

    #[test]
    fn empty_vars_json_is_disabled() {
        assert!(!vars_json_has_vigorepo_true(b""));
    }

    #[test]
    fn missing_vigorepo_key_is_disabled() {
        assert!(!vars_json_has_vigorepo_true(br#"{"other": true}"#));
    }

    #[test]
    fn vigorepo_true_bool_enables() {
        assert!(vars_json_has_vigorepo_true(br#"{"vigorepo": true}"#));
    }

    #[test]
    fn vigorepo_false_bool_is_disabled() {
        assert!(!vars_json_has_vigorepo_true(br#"{"vigorepo": false}"#));
    }

    #[test]
    fn vigorepo_string_true_enables() {
        assert!(vars_json_has_vigorepo_true(br#"{"vigorepo": "true"}"#));
        assert!(vars_json_has_vigorepo_true(br#"{"vigorepo": "True"}"#));
        assert!(vars_json_has_vigorepo_true(br#"{"vigorepo": "YES"}"#));
        assert!(vars_json_has_vigorepo_true(br#"{"vigorepo": "1"}"#));
    }

    #[test]
    fn vigorepo_string_false_is_disabled() {
        assert!(!vars_json_has_vigorepo_true(br#"{"vigorepo": "false"}"#));
        assert!(!vars_json_has_vigorepo_true(br#"{"vigorepo": "no"}"#));
        assert!(!vars_json_has_vigorepo_true(br#"{"vigorepo": "0"}"#));
        assert!(!vars_json_has_vigorepo_true(br#"{"vigorepo": ""}"#));
    }

    #[test]
    fn vigorepo_number_nonzero_enables() {
        assert!(vars_json_has_vigorepo_true(br#"{"vigorepo": 1}"#));
        assert!(vars_json_has_vigorepo_true(br#"{"vigorepo": 42}"#));
    }

    #[test]
    fn vigorepo_number_zero_is_disabled() {
        assert!(!vars_json_has_vigorepo_true(br#"{"vigorepo": 0}"#));
    }

    #[test]
    fn vigorepo_null_is_disabled() {
        assert!(!vars_json_has_vigorepo_true(br#"{"vigorepo": null}"#));
    }

    #[test]
    fn malformed_json_is_disabled() {
        assert!(!vars_json_has_vigorepo_true(b"{not valid json"));
    }

    #[test]
    fn non_object_root_is_disabled() {
        assert!(!vars_json_has_vigorepo_true(br#"["vigorepo"]"#));
        assert!(!vars_json_has_vigorepo_true(br#""vigorepo""#));
    }

    #[test]
    fn vigorepo_coexists_with_other_vars() {
        let vars = br#"{"app_name": "web", "vigorepo": true, "timeout": 30}"#;
        assert!(vars_json_has_vigorepo_true(vars));
    }
}
