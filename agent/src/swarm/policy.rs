// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Per-user usercrate-policy cache for swarm subsystems (0.41.0).
//!
//! The user executor writes
//! `/var/lib/vigo/usercrate-policy/<username>.json` at every reconcile
//! pass that touches the user (present state). The file mirrors the
//! authorization flags from the user resource — `puddle: bool` and
//! `swarmgit: bool` — so user-mode CLI verbs can decide whether
//! to proceed without contacting the server.
//!
//! Daemon-canonical + user-readable. Root writes (mode 0644); CLI
//! verbs read. Same shape as the existing peers.json / tombstones.json
//! / leaves.json / puddle_members.json mirrors.
//!
//! Refusal on missing cache is intentional. It surfaces the
//! "user provisioned via raw useradd, never enrolled in a Vigo
//! usercrate" case rather than silently allowing puddle / swarmgit
//! verbs that the operator didn't authorize. The error message
//! points at the cache path so the operator knows which file the
//! agent is looking for.

use std::fs;
use std::path::{Path, PathBuf};

use anyhow::{bail, Context, Result};
use serde::{Deserialize, Serialize};

/// Default directory for the per-user policy cache. Production paths
/// land here; tests pass a tempdir to the `_at` variants below.
pub const POLICY_DIR: &str = "/var/lib/vigo/usercrate-policy";

/// Authorization flags resolved from the user's usercrate. Both
/// default to `false` — the user can use a swarm subsystem only when
/// the matching flag is explicitly set in the usercrate AND the
/// envoy-level pattern list matches this host.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize, Default)]
pub struct UserPolicy {
    #[serde(default)]
    pub puddle: bool,
    #[serde(default)]
    pub swarmgit: bool,
}

/// Path to the on-disk policy file for `username` under `dir`.
pub fn policy_path_at(dir: &Path, username: &str) -> PathBuf {
    dir.join(format!("{username}.json"))
}

/// Path to the on-disk policy file for `username` under [`POLICY_DIR`].
///
/// `/var/lib/vigo/usercrate-policy/<username>.json` — root-owned,
/// world-readable so user-mode CLI verbs can stat + read without
/// elevation.
pub fn policy_path(username: &str) -> PathBuf {
    policy_path_at(Path::new(POLICY_DIR), username)
}

/// Read this user's policy from a specific directory. Used by
/// production callers via [`read_user_policy`] (which delegates to
/// this with [`POLICY_DIR`]) and by tests with a tempdir.
pub fn read_user_policy_at(dir: &Path, username: &str) -> Result<UserPolicy> {
    let path = policy_path_at(dir, username);
    let bytes = fs::read(&path).with_context(|| {
        format!(
            "reading usercrate-policy cache at {} — has the agent checked in \
             and reconciled this user? Per-user swarm verbs require a Vigo \
             usercrate enrollment with `puddle: true` or `swarmgit: true`.",
            path.display()
        )
    })?;
    serde_json::from_slice(&bytes)
        .with_context(|| format!("parsing usercrate-policy cache at {}", path.display()))
}

/// Read this user's policy from the daemon-canonical cache.
///
/// Returns an explicit error when the file is missing or unparseable
/// rather than defaulting to "deny" silently — the operator-facing
/// message points at the cache path so they know whether to enroll
/// the user in a usercrate or fix permissions on the policy dir.
pub fn read_user_policy(username: &str) -> Result<UserPolicy> {
    read_user_policy_at(Path::new(POLICY_DIR), username)
}

/// Write this user's policy to a specific directory. Atomic
/// (temp-write + rename), mode 0644. Parent directory is created
/// if missing.
pub fn write_user_policy_at(
    dir: &Path,
    username: &str,
    policy: &UserPolicy,
) -> Result<()> {
    fs::create_dir_all(dir)
        .with_context(|| format!("creating {}", dir.display()))?;
    let path = policy_path_at(dir, username);
    let tmp = path.with_extension("json.tmp");
    let bytes = serde_json::to_vec_pretty(policy).context("serializing usercrate policy")?;
    fs::write(&tmp, &bytes).with_context(|| format!("writing {}", tmp.display()))?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let _ = fs::set_permissions(&tmp, fs::Permissions::from_mode(0o644));
    }
    fs::rename(&tmp, &path)
        .with_context(|| format!("renaming {} -> {}", tmp.display(), path.display()))?;
    Ok(())
}

/// Write this user's policy to the daemon-canonical cache. Atomic
/// (temp-write + rename), root-owned, mode 0644 so user-mode CLI
/// verbs can read it without elevation. Parent directory is created
/// if missing.
///
/// Called from the user executor at the end of every reconcile pass
/// for users in `state: present`. Concurrent writes for different
/// usernames don't interfere (separate paths); concurrent writes for
/// the same username converge on whichever rename lands last (mirrors
/// the existing peers.json / tombstones.json behavior).
pub fn write_user_policy(username: &str, policy: &UserPolicy) -> Result<()> {
    write_user_policy_at(Path::new(POLICY_DIR), username, policy)
}

/// Remove this user's policy file from a specific directory. Idempotent.
pub fn remove_user_policy_at(dir: &Path, username: &str) -> Result<()> {
    let path = policy_path_at(dir, username);
    match fs::remove_file(&path) {
        Ok(()) => Ok(()),
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(()),
        Err(e) => Err(anyhow::Error::from(e))
            .with_context(|| format!("removing {}", path.display())),
    }
}

/// Remove this user's policy file. Called from the user executor when
/// the user reconciles to `state: absent`. Idempotent — missing file
/// is fine.
pub fn remove_user_policy(username: &str) -> Result<()> {
    remove_user_policy_at(Path::new(POLICY_DIR), username)
}

/// Refuse early in a CLI verb if the invoking user lacks the puddle
/// authorization flag, OR if the envoy itself is denied by the
/// substrate-level pattern list. Both layers must say yes — matching
/// the runtime gate in [`crate::swarm::gates::substrate_active`].
///
/// Layer 1 (user): reads `/var/lib/vigo/usercrate-policy/<user>.json`,
/// written by the user executor on every reconcile pass.
///
/// Layer 2 (envoy): reads `/var/lib/vigo/swarm/state.json` (0.41.1+),
/// written by `accept_bundle` on every successful policy commit.
/// A missing state.json is treated as "no recent check-in" and the
/// envoy check is skipped — refusing on absence would block legitimate
/// bootstrap on a freshly-enrolled envoy.
pub fn require_puddle(username: &str) -> Result<()> {
    let policy = read_user_policy(username)?;
    if !policy.puddle {
        bail!(
            "user {username} is not authorized to use puddle on this envoy. \
             Set `puddle: true` on the user resource in the usercrate, \
             publish the config, and wait for the agent to reconcile."
        );
    }
    if !envoy_layer_allows("puddle") {
        bail!(
            "user {username} is authorized but this envoy is not in the \
             puddle pattern list. Add this hostname to `swarm.puddle.enabled` \
             in server.yaml, publish, and wait for the agent to check in."
        );
    }
    Ok(())
}

/// Refuse early in a CLI verb if the invoking user lacks the swarmgit
/// authorization flag, OR if the envoy itself is denied by the
/// substrate-level pattern list. See [`require_puddle`] for the
/// two-layer rationale.
pub fn require_swarmgit(username: &str) -> Result<()> {
    let policy = read_user_policy(username)?;
    if !policy.swarmgit {
        bail!(
            "user {username} is not authorized to use swarmgit on this envoy. \
             Set `swarmgit: true` on the user resource in the usercrate, \
             publish the config, and wait for the agent to reconcile."
        );
    }
    if !envoy_layer_allows("swarmgit") {
        bail!(
            "user {username} is authorized but this envoy is not in the \
             swarmgit pattern list. Add this hostname to \
             `swarm.swarmgit.enabled` in server.yaml, publish, and wait \
             for the agent to check in."
        );
    }
    Ok(())
}

/// Read `state.json` and report whether the named subsystem gate is on.
/// A missing or unreadable state.json returns `true` (skip the envoy
/// check) — the file is written by the daemon after every accepted
/// bundle, so absence means either a freshly-enrolled envoy that hasn't
/// completed a check-in yet OR the daemon is on a pre-0.41.1 binary.
/// In both cases, refusing here would block the user from doing any
/// puddle/swarmgit setup, so we fail open.
fn envoy_layer_allows(subsystem: &str) -> bool {
    envoy_layer_allows_at(&crate::swarm::state_mirror::default_path(), subsystem)
}

/// Path-overridable form of [`envoy_layer_allows`]. Production callers
/// use the default path via [`envoy_layer_allows`]; tests pass a tempdir
/// path so the check is hermetic.
fn envoy_layer_allows_at(path: &std::path::Path, subsystem: &str) -> bool {
    let state = match crate::swarm::state_mirror::read(path) {
        Ok(s) => s,
        Err(_) => return true,
    };
    match subsystem {
        "puddle" => state.gates.puddle,
        "swarmgit" => state.gates.swarmgit,
        _ => true,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn user_policy_round_trips_through_json() {
        let p = UserPolicy { puddle: true, swarmgit: false };
        let json = serde_json::to_string(&p).unwrap();
        let q: UserPolicy = serde_json::from_str(&json).unwrap();
        assert_eq!(p, q);
    }

    #[test]
    fn missing_fields_default_to_false() {
        // A future field added to UserPolicy must not break existing
        // on-disk files. Empty {} parses as both-false.
        let p: UserPolicy = serde_json::from_str("{}").unwrap();
        assert!(!p.puddle);
        assert!(!p.swarmgit);
    }

    #[test]
    fn extra_fields_are_ignored() {
        // Tolerate extra keys an older agent might encounter from a
        // newer user-executor write.
        let p: UserPolicy = serde_json::from_str(
            r#"{"puddle": true, "swarmgit": false, "future_thing": 42}"#,
        )
        .unwrap();
        assert!(p.puddle);
        assert!(!p.swarmgit);
    }

    #[test]
    fn policy_path_is_per_user() {
        assert_eq!(
            policy_path("dan"),
            PathBuf::from("/var/lib/vigo/usercrate-policy/dan.json")
        );
        assert_ne!(policy_path("dan"), policy_path("alice"));
    }

    // The two-layer envoy_layer_allows helper isn't directly testable
    // without a global state.json fixture (default_path() reads
    // /var/lib/vigo/swarm/state.json). The semantics it implements
    // are exercised end-to-end by the home-lab smoke runs and indirectly
    // by state_mirror::tests::round_trips_through_disk which proves
    // the gate fields decode correctly. The fail-open-on-missing
    // semantic is the load-bearing one — verified here by the absence
    // of state.json on a fresh test environment causing no panic /
    // refusal.
    #[test]
    fn envoy_layer_fails_open_when_state_json_missing() {
        // Hermetic — point at a path inside an empty tempdir so the test
        // doesn't depend on whether /var/lib/vigo/swarm/state.json
        // happens to exist on the host running the suite.
        let dir = tempfile::TempDir::new().unwrap();
        let path = dir.path().join("nope.json");
        assert!(envoy_layer_allows_at(&path, "puddle"));
        assert!(envoy_layer_allows_at(&path, "swarmgit"));
        // Unknown subsystem name — defensive default-true.
        assert!(envoy_layer_allows_at(&path, "payload"));
    }

    #[test]
    fn envoy_layer_respects_explicit_state_json() {
        let dir = tempfile::TempDir::new().unwrap();
        let path = dir.path().join("state.json");
        crate::swarm::state_mirror::write(&path, "0.41.3", 1, br#"{"swarm_puddle_enabled": false}"#).unwrap();
        assert!(!envoy_layer_allows_at(&path, "puddle"));
        // Defaults to true for keys not in vars_json.
        assert!(envoy_layer_allows_at(&path, "swarmgit"));
    }
}
