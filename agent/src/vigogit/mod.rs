// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Vigogit — git-commit-triggered repo sync over swarm.
//!
//! Vigogit auto-syncs a small number of a user's personal git repos
//! from one "source" envoy to a bounded handful of other envoys where
//! the same user has an account. The trigger is `git commit`, not
//! `git push`. Receivers are read-only mirrors: they hold a bare repo
//! under `/var/lib/vigo/vigogit/<seeder>/<user>/<project>.git` and a
//! checked-out working tree at `~<user>/vigogit/<project>/` with no
//! `.git/` directory inside it.
//!
//! Vigogit is a personal-DR tool, not a fleet distribution mechanism.
//! Fleet-wide content distribution is what modules and `file:` resources
//! are for. Vigogit carries history so a user can `git clone` off any
//! mirror to recover their work after losing their primary machine.
//!
//! Configuration lives entirely in a mandatory `.vigogit.yaml` committed
//! to the root of each synced repo. See [`sidecar`].
//!
//! Hard cap enforcement for `max_mirrors` is delegated to the server's
//! vigogit ClaimTable, which selects a deterministic subset of candidate
//! envoys and delivers the selection to agents via a new
//! `vigogit_claims` field on the check-in response. See the plan at
//! `docs/concepts/vigogit-claim-table.md` (forthcoming).
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
//! - [`sidecar`] — strict parser + validator for `.vigogit.yaml`.
//! - [`path_guard`] — canonicalization + containment for source and
//!   receiver-derived paths.
//! - [`git_ops`] — the only place that spawns `git`; applies a fixed
//!   hardening policy on every invocation.
//! - [`state`] — on-disk state (last_seeded_sha, bundle tmp dir,
//!   orphan cleanup).
//!
//! `source.rs` and `receiver.rs` will be added in later commits.

pub mod git_ops;
pub mod path_guard;
pub mod sidecar;
pub mod source;
pub mod state;

use tokio_util::sync::CancellationToken;
use tracing::{info, warn};

/// Minimum non-source mirrors vigogit will target.
///
/// DR safety requires at least 3 redundant copies. This is a
/// **target**, not a hard gate: if the candidate pool has fewer than
/// `MIN_MIRRORS` envoys with the target user, vigogit materializes on
/// whatever is available and the server emits a
/// `vigogit.under_replicated` trait. The floor is immutable and
/// cannot be overridden by configuration, CLI flag, env var, or
/// sidecar value.
pub const MIN_MIRRORS: usize = 3;

/// Maximum non-source mirrors vigogit will enforce.
///
/// Vigogit is a personal-DR tool, not a fleet distribution mechanism.
/// Normal per-repo mirror count is 3-5; power users may legitimately
/// want up to 10. Beyond 10 is almost certainly a misconfiguration.
/// This ceiling is immutable and cannot be overridden.
pub const MAX_MIRRORS: usize = 10;

/// Spawn the vigogit subsystem.
///
/// During scaffolding this is a no-op except for one-time startup
/// tasks:
/// - Clean up orphan bundle tmp files from previous runs.
/// - Run the git version check; log a warning if it fails.
///
/// The source-side scanner loop and receiver-side claim consumer
/// loop are populated in later commits.
pub fn spawn(_tls: Option<crate::update::TlsCerts>, cancel: CancellationToken) {
    info!(
        min_mirrors = MIN_MIRRORS,
        max_mirrors = MAX_MIRRORS,
        "vigogit: subsystem registered (scaffolding — source/receiver loops not yet implemented)"
    );

    // Consume the cancel token field for the type system; the future
    // loop implementations will own it.
    let _ = cancel;

    let state_dir = crate::platform::paths::default_vigogit_dir();
    state::cleanup_stale_bundles(&state_dir);

    match git_ops::check_version() {
        Ok((major, minor)) => {
            info!(major, minor, "vigogit: git version check passed");
        }
        Err(e) => {
            warn!(
                error = %e,
                "vigogit: git version check failed; source and receiver loops will refuse to run until resolved"
            );
        }
    }

    // One-shot startup scan for source repos. Discovery-only: logs what
    // was found, surfaces errors, but does not yet publish bundles.
    // The periodic scan loop and ref watchers are added in a later commit.
    let hostname = std::fs::read_to_string("/etc/hostname")
        .unwrap_or_else(|_| "unknown".to_string())
        .trim()
        .to_string();
    let scan = source::scan_local(&hostname);
    info!(
        sources = scan.sources.len(),
        errors = scan.errors.len(),
        skipped = scan.skip_count,
        "vigogit: initial source discovery complete"
    );
    for err in &scan.errors {
        warn!(
            path = %err.path.display(),
            reason = %err.reason,
            "vigogit: source discovery error"
        );
    }
    for src in &scan.sources {
        info!(
            label = %src.label,
            paused = src.sidecar.paused,
            max_mirrors = src.sidecar.max_mirrors,
            "vigogit: discovered source"
        );
    }
}
