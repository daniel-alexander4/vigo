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
//! - [`state`] — on-disk state (`state.json` per-label seeding state,
//!   bundle tmp dir, orphan cleanup).
//! - [`source`] — discovery scanner: walks every user's
//!   `~/vigogit/*` and produces a [`source::Source`] per validated
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

pub mod git_ops;
pub mod path_guard;
pub mod publisher;
pub mod reactor;
pub mod sidecar;
pub mod source;
pub mod state;

/// Separator character between segments of a vigogit swarm label.
///
/// Labels are `vigogit:<host>:<user>:<project>` (and
/// `vigogit-meta:<host>:<user>:<project>`). The separator is `:`
/// because the label is also the seed *filename* in
/// `default_seeds_dir()`, and `/` is the only filename-illegal
/// character on Linux. `:` is not a legal hostname character per
/// [`sidecar::is_valid_hostname`], not a legal Linux username
/// character (it's the `/etc/passwd` field separator), and is
/// rejected from project names by
/// [`path_guard::validate_project_name`].
pub const LABEL_SEP: char = ':';

/// Label namespace for the bundle (content) seed.
pub const CONTENT_LABEL_PREFIX: &str = "vigogit";

/// Label namespace for the meta (sidecar bytes) seed.
pub const META_LABEL_PREFIX: &str = "vigogit-meta";

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

/// Build the meta-seed label for a `(hostname, user, project)` triple.
pub fn meta_label(hostname: &str, user: &str, project: &str) -> String {
    format!(
        "{}{sep}{}{sep}{}{sep}{}",
        META_LABEL_PREFIX,
        hostname.to_lowercase(),
        user,
        project,
        sep = LABEL_SEP
    )
}

/// Derive the meta label that pairs with a content label by swapping
/// the prefix. Returns `None` if the input is not a content label.
pub fn meta_label_from_content(content_label: &str) -> Option<String> {
    let suffix = content_label.strip_prefix(CONTENT_LABEL_PREFIX)?;
    Some(format!("{}{}", META_LABEL_PREFIX, suffix))
}

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
/// Synchronous startup work (runs before returning):
///
/// - Clean up orphan bundle tmp files from previous runs.
/// - Run the git version check. On failure, log a warning and skip
///   spawning the reactor — vigogit refuses to seed until resolved.
///
/// If the version check passes, a single [`reactor`] task is
/// spawned. The reactor owns `cancel` and wakes every
/// [`reactor::TICK_INTERVAL`] to re-run discovery + publish.
/// Publisher short-circuits on `state.git_sha == HEAD`, so a
/// steady-state tick costs one `git rev-parse` per source.
pub fn spawn(_tls: Option<crate::update::TlsCerts>, cancel: CancellationToken) {
    info!(
        min_mirrors = MIN_MIRRORS,
        max_mirrors = MAX_MIRRORS,
        "vigogit: subsystem starting"
    );

    let state_dir = crate::platform::paths::default_vigogit_dir();
    state::cleanup_stale_bundles(&state_dir);

    match git_ops::check_version() {
        Ok((major, minor)) => {
            info!(major, minor, "vigogit: git version check passed");
        }
        Err(e) => {
            warn!(
                error = %e,
                "vigogit: git version check failed; reactor will not start until resolved"
            );
            return;
        }
    }

    reactor::spawn(cancel);
}
