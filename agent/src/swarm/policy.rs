// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Per-user usercrate-policy cache for swarm subsystems (0.41.0).
//!
//! The user executor writes
//! `/var/lib/vigo/usercrate-policy/<username>.json` at every reconcile
//! pass that touches the user (present state). The file mirrors the
//! authorization flags from the user resource — `puddle: bool`,
//! `gitback: bool`, `curator: bool` (ADR-024), and `poolq: bool`
//! (ADR-029) — so user-mode CLI verbs can decide whether to proceed
//! without contacting the server.
//!
//! Daemon-canonical + user-readable. Root writes (mode 0644); CLI
//! verbs read. Same shape as the existing peers.json / tombstones.json
//! / leaves.json / puddle_members.json mirrors.
//!
//! Refusal on missing cache is intentional. It surfaces the
//! "user provisioned via raw useradd, never enrolled in a Vigo
//! usercrate" case rather than silently allowing puddle / gitback /
//! curator / poolq verbs that the operator didn't authorize. The error
//! message points at the cache path so the operator knows which file
//! the agent is looking for.

use std::fs;
use std::path::{Path, PathBuf};

use anyhow::{bail, Context, Result};
use serde::{Deserialize, Serialize};

/// Default directory for the per-user policy cache. Production paths
/// land here; tests pass a tempdir to the `_at` variants below.
pub const POLICY_DIR: &str = "/var/lib/vigo/usercrate-policy";

/// Authorization flags resolved from the user's usercrate. All
/// default to `false` — the user can use a swarm subsystem only when
/// the matching flag is explicitly set in the usercrate AND the
/// envoy-level pattern list matches this host. `curator` and `poolq`
/// are heavier grants (publish to the fleet's registry / post into the
/// fleet's ordered logs, fleet-readable by default — ADR-024,
/// ADR-029) and additionally require an unlocked puddle.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize, Default)]
pub struct UserPolicy {
    #[serde(default)]
    pub puddle: bool,
    #[serde(default)]
    pub gitback: bool,
    #[serde(default)]
    pub curator: bool,
    #[serde(default)]
    pub poolq: bool,
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
             usercrate enrollment with `puddle: true` or `gitback: true`.",
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
    refuse_root("puddle", username)?;
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

/// Refuse early in a CLI verb if the invoking user lacks the gitback
/// authorization flag, OR if the envoy itself is denied by the
/// substrate-level pattern list. See [`require_puddle`] for the
/// two-layer rationale.
pub fn require_gitback(username: &str) -> Result<()> {
    refuse_root("gitback", username)?;
    let policy = read_user_policy(username)?;
    if !policy.gitback {
        bail!(
            "user {username} is not authorized to use gitback on this envoy. \
             Set `gitback: true` on the user resource in the usercrate, \
             publish the config, and wait for the agent to reconcile."
        );
    }
    if !envoy_layer_allows("gitback") {
        bail!(
            "user {username} is authorized but this envoy is not in the \
             gitback pattern list. Add this hostname to \
             `swarm.gitback.enabled` in server.yaml, publish, and wait \
             for the agent to check in."
        );
    }
    Ok(())
}

/// Refuse early in a CLI verb if the invoking user lacks the curator
/// authorization flag, OR if the envoy itself is denied by the
/// `swarm.curator.enabled` pattern list. See [`require_puddle`] for the
/// two-layer rationale. Note: `curator: true` does not by itself let
/// the user publish — the helper daemon also requires an unlocked
/// puddle (the signing key) per ADR-024; this check is the cheap
/// up-front "is this user/envoy even allowed to try" gate.
pub fn require_curator(username: &str) -> Result<()> {
    refuse_root("curator", username)?;
    let policy = read_user_policy(username)?;
    if !policy.curator {
        bail!(
            "user {username} is not authorized to use curator on this envoy. \
             Set `curator: true` on the user resource in the usercrate, \
             publish the config, and wait for the agent to reconcile."
        );
    }
    if !envoy_layer_allows("curator") {
        bail!(
            "user {username} is authorized but this envoy is not in the \
             curator pattern list. Add this hostname to \
             `swarm.curator.enabled` in server.yaml, publish, and wait \
             for the agent to check in."
        );
    }
    Ok(())
}

/// Refuse early in a CLI verb if the invoking user lacks the poolq
/// authorization flag, OR if the envoy itself is denied by the
/// `swarm.poolq.enabled` pattern list. See [`require_puddle`] for the
/// two-layer rationale. Note: `poolq: true` does not by itself let
/// the user post — the helper daemon also requires an unlocked puddle
/// (the signing key) per ADR-029; this check is the cheap up-front
/// "is this user/envoy even allowed to try" gate.
pub fn require_poolq(username: &str) -> Result<()> {
    refuse_root("poolq", username)?;
    let policy = read_user_policy(username)?;
    if !policy.poolq {
        bail!(
            "user {username} is not authorized to use poolq on this envoy. \
             Set `poolq: true` on the user resource in the usercrate, \
             publish the config, and wait for the agent to reconcile."
        );
    }
    if !envoy_layer_allows("poolq") {
        bail!(
            "user {username} is authorized but this envoy is not in the \
             poolq pattern list. Add this hostname to \
             `swarm.poolq.enabled` in server.yaml, publish, and wait \
             for the agent to check in."
        );
    }
    Ok(())
}

/// Refuse a per-user swarm verb invocation when the resolved invoking user
/// is `root`. The CLI's user-resolver returns `"root"` only when uid == 0,
/// `SUDO_USER` is unset/empty/`root`, and `USER=root` — i.e. an interactive
/// root shell, never a real-user-via-sudo invocation (sudo's `SUDO_USER`
/// path is checked first). Per-user verbs sign with the user's puddle
/// identity (`~/.vigo-puddle/`), which root doesn't have, so we point at
/// the fix instead of letting the failure manifest as "missing root.json"
/// or "no puddle session helper" downstream.
fn refuse_root(feature: &str, username: &str) -> Result<()> {
    if username != "root" {
        return Ok(());
    }
    bail!(
        "this `{feature}` verb signs with a per-user puddle identity and \
         cannot run as root. Switch to the Unix user whose usercrate carries \
         `{feature}: true` — e.g. `su - <username>` or `sudo -iu <username>` \
         — and try again on this envoy."
    );
}

/// Read `state.json` and report whether the named subsystem gate is on.
/// A missing or unreadable state.json returns `true` (skip the envoy
/// check) — the file is written by the daemon after every accepted
/// bundle, so absence means either a freshly-enrolled envoy that hasn't
/// completed a check-in yet OR the daemon is on a pre-0.41.1 binary.
/// In both cases, refusing here would block the user from doing any
/// puddle/gitback setup, so we fail open.
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
        "gitback" => state.gates.gitback,
        "curator" => state.gates.curator,
        "poolq" => state.gates.poolq,
        _ => true,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn user_policy_round_trips_through_json() {
        let p = UserPolicy { puddle: true, gitback: false, curator: true, poolq: true };
        let json = serde_json::to_string(&p).unwrap();
        let q: UserPolicy = serde_json::from_str(&json).unwrap();
        assert_eq!(p, q);
    }

    #[test]
    fn missing_fields_default_to_false() {
        // A future field added to UserPolicy must not break existing
        // on-disk files. Empty {} parses as all-false. (This also
        // covers the pre-curator / pre-poolq on-disk file an upgraded
        // agent would read for one reconcile cycle.)
        let p: UserPolicy = serde_json::from_str("{}").unwrap();
        assert!(!p.puddle);
        assert!(!p.gitback);
        assert!(!p.curator);
        assert!(!p.poolq);
        // A pre-poolq file with three old keys present must parse
        // and leave poolq at its safe default (false / deny).
        let old: UserPolicy = serde_json::from_str(
            r#"{"puddle": true, "gitback": true, "curator": true}"#,
        ).unwrap();
        assert!(old.puddle && old.gitback && old.curator && !old.poolq);
    }

    #[test]
    fn extra_fields_are_ignored() {
        // Tolerate extra keys an older agent might encounter from a
        // newer user-executor write.
        let p: UserPolicy = serde_json::from_str(
            r#"{"puddle": true, "gitback": false, "curator": true, "poolq": true, "future_thing": 42}"#,
        )
        .unwrap();
        assert!(p.puddle);
        assert!(!p.gitback);
        assert!(p.curator);
        assert!(p.poolq);
    }

    #[test]
    fn policy_path_is_per_user() {
        assert_eq!(
            policy_path("dan"),
            PathBuf::from("/var/lib/vigo/usercrate-policy/dan.json")
        );
        assert_ne!(policy_path("dan"), policy_path("alice"));
    }

    #[test]
    fn refuse_root_bails_for_root_only() {
        // A non-root username falls through silently — the rest of
        // `require_*` then runs as normal.
        assert!(refuse_root("puddle", "dan").is_ok());
        assert!(refuse_root("gitback", "alice").is_ok());
        assert!(refuse_root("curator", "ann").is_ok());
        assert!(refuse_root("poolq", "bob").is_ok());

        // Root bails for each feature with a tailored message that
        // names the right flag and the fix.
        for feature in ["puddle", "gitback", "curator", "poolq"] {
            let err = refuse_root(feature, "root").unwrap_err();
            let msg = format!("{err:#}");
            assert!(msg.contains("cannot run as root"), "feature={feature} msg={msg}");
            assert!(
                msg.contains(&format!("`{feature}: true`")),
                "feature={feature} msg={msg}"
            );
            assert!(msg.contains("su - <username>"), "feature={feature} msg={msg}");
        }
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
        assert!(envoy_layer_allows_at(&path, "gitback"));
        assert!(envoy_layer_allows_at(&path, "curator"));
        assert!(envoy_layer_allows_at(&path, "poolq"));
        // Unknown subsystem name — defensive default-true.
        assert!(envoy_layer_allows_at(&path, "payload"));
    }

    #[test]
    fn envoy_layer_respects_explicit_state_json() {
        let dir = tempfile::TempDir::new().unwrap();
        let path = dir.path().join("state.json");
        crate::swarm::state_mirror::write(&path, "0.41.3", 1, br#"{"swarm_puddle_enabled": false, "swarm_curator_enabled": false}"#).unwrap();
        assert!(!envoy_layer_allows_at(&path, "puddle"));
        assert!(!envoy_layer_allows_at(&path, "curator"));
        // Defaults to true for keys not in vars_json.
        assert!(envoy_layer_allows_at(&path, "gitback"));
    }
}
