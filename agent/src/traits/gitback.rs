// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Reports gitback state for the `gitback` trait namespace.
//!
//! Re-emitted against the unified-substrate state surface (ADR-015
//! step 4). The collector walks every user's `~/.vigo-gitback/<pid>/`
//! and joins per-user state with the per-envoy bare repo + envelopes
//! store. Multiple users on the same envoy that hold the same project
//! collapse to one entry; user names land in `users_with_state` so
//! the server-side mesh can attribute things back.
//!
//! Trait namespace: `gitback`. Schema is keyed on `project_id`
//! (`sha256(founder_puddle_pubkey)`); legacy publisher/receiver
//! distinction is gone.
//!
//! ### Per-project payload
//!
//! ```jsonc
//! {
//!   "project_id":           "<64-hex>",
//!   "handle":               "vigo-web",
//!   "founder_puddle_pubkey": "<64-hex>",
//!   "members":              ["<64-hex>", ...],
//!   "dr_scope":             "swarm-wide" | "members-only",
//!   "users_with_state":     ["dan", "alice"],
//!   "has_bare_repo":        true,
//!   "bare_repo_size_bytes": 4096,
//!   "head_refs":            { "refs/heads/main": "<40-hex>" },
//!   "envelopes": {
//!       "revokes":     0,
//!       "leaves":      0,
//!       "digest_hex":  "<32-hex>"
//!   },
//!   "leaf_role":            "admin" | "member",
//!   "token_chain_depth":    1,
//!   "token_expires_at":     null  // or unix-seconds
//! }
//! ```
//!
//! ### Friendly-name claims (ADR-022)
//!
//! The collector also walks each Unix user's `~/.vigo-puddle/name-claim.json`
//! and emits the **full signed claim** verbatim, so the server-side
//! aggregator (`gitbackmesh`) can join them into a fleet
//! `name → claims` map and resolvers can verify the signature +
//! `supersedes` chain end-to-end without trusting the server. A
//! locally-corrupt or non-self-verifying claim is dropped here rather
//! than published.
//!
//! ```jsonc
//! "name_claims": [
//!   { "user": "dan", "claim": { "body": { "puddle_pubkey": "<64-hex>",
//!                                          "name": "dan",
//!                                          "claimed_at": <unix-seconds>,
//!                                          "supersedes": [ ... ] },
//!                               "signature": "<128-hex>" } }
//! ]
//! ```
//!
//! Top-level shape:
//!
//! ```jsonc
//! {
//!   "updated_at":       <unix-seconds>,
//!   "project_count":    3,
//!   "projects":        [ ... ],
//!   "name_claim_count": 1,            // omitted when 0
//!   "name_claims":     [ ... ]        // omitted when empty
//! }
//! ```

use std::collections::{BTreeMap, BTreeSet};
use std::path::{Path, PathBuf};

use anyhow::Result;
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};

use crate::platform::paths::default_gitback_dir;
use crate::swarm::gitback::envelopes;
use crate::swarm::gitback::fan_out;
use crate::swarm::gitback::project_state::{
    parse_hex_id, project_repo_path, ProjectConfig, TokenChain, PROJECT_STATE_VERSION,
};
#[allow(unused_imports)]
use crate::swarm::anchor::tokens::{Role, ID_LEN, NEVER_EXPIRES};

/// One project's view for the trait. Sorted by `project_id` in the
/// emitted array for deterministic output. `Deserialize` so the
/// `vigo swarm gitback status` CLI can parse the helper daemon's
/// `STATUS` reply (the daemon runs `collect_projects` as root, since
/// the bare-repo facts live under a `0700` `projects/` dir the
/// unprivileged CLI can't stat into).
#[derive(Debug, Serialize, Deserialize)]
pub struct ProjectEntry {
    pub project_id: String,
    pub handle: String,
    pub founder_puddle_pubkey: String,
    pub members: Vec<String>,
    pub dr_scope: String,
    pub users_with_state: Vec<String>,
    pub has_bare_repo: bool,
    pub bare_repo_size_bytes: u64,
    pub head_refs: BTreeMap<String, String>,
    pub envelopes: EnvelopeSummary,
    pub leaf_role: Option<String>,
    pub token_chain_depth: usize,
    pub token_expires_at: Option<i64>,
}

#[derive(Debug, Serialize, Deserialize)]
pub struct EnvelopeSummary {
    pub revokes: usize,
    pub leaves: usize,
    pub digest_hex: String,
}

pub fn collect() -> Result<Value> {
    let gitback_base = default_gitback_dir();
    let passwd_body = match std::fs::read_to_string(passwd_path()) {
        Ok(s) => s,
        Err(_) => return Ok(Value::Null),
    };

    let projects = collect_projects(&passwd_body, &gitback_base);
    let name_claims = collect_name_claims(&passwd_body);
    if projects.is_empty() && name_claims.is_empty() {
        return Ok(Value::Null);
    }

    let mut out = json!({
        "updated_at": unix_seconds_now(),
        "project_count": projects.len(),
        "projects": projects,
    });
    if !name_claims.is_empty() {
        out["name_claim_count"] = json!(name_claims.len());
        out["name_claims"] = json!(name_claims);
    }
    Ok(out)
}

/// One Unix user's friendly-name claim (ADR-022), as it appears in
/// the `gitback` trait. The `claim` is the full signed
/// [`crate::swarm::puddle::name::NameClaim`] — published verbatim so
/// resolvers verify it themselves.
#[derive(Debug, Serialize)]
pub struct NameClaimEntry {
    pub user: String,
    pub claim: crate::swarm::puddle::name::NameClaim,
}

/// Walk passwd, read each user's `~/.vigo-puddle/name-claim.json`, and
/// return the self-verifying ones. Sorted by claimed name then user
/// for deterministic trait output.
pub(crate) fn collect_name_claims(passwd_body: &str) -> Vec<NameClaimEntry> {
    use crate::swarm::puddle::name::NameClaim;
    let mut out = Vec::new();
    for (user, home) in walk_passwd(passwd_body) {
        if let Ok(Some(claim)) = NameClaim::load(&home) {
            if claim.verify_signature().is_ok() {
                out.push(NameClaimEntry { user, claim });
            }
        }
    }
    out.sort_by(|a, b| {
        a.claim
            .body
            .name
            .cmp(&b.claim.body.name)
            .then_with(|| a.user.cmp(&b.user))
    });
    out
}

/// Walk the passwd file and for each user with a `~/.vigo-gitback/`
/// dir, collapse their per-project state into the cross-user view.
/// Pulled out for testability; the env-var override on the gitback
/// base lets tests redirect onto a tempdir.
pub(crate) fn collect_projects(passwd_body: &str, gitback_base: &Path) -> Vec<ProjectEntry> {
    let user_homes = walk_passwd(passwd_body);

    // For each project_id, gather the set of (user, home) pairs that
    // hold local state.
    let mut by_project: BTreeMap<String, ProjectAccumulator> = BTreeMap::new();
    for (user, home) in &user_homes {
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
            let name = match p.file_name().and_then(|n| n.to_str()) {
                Some(n) => n.to_string(),
                None => continue,
            };
            let pid = match parse_hex_id(&name) {
                Ok(p) => p,
                Err(_) => continue,
            };
            // Verify schema version + load.
            let cfg = match ProjectConfig::load(home, &pid) {
                Ok(Some(c)) => c,
                _ => continue,
            };
            if cfg.version != PROJECT_STATE_VERSION {
                continue;
            }
            let chain = TokenChain::load(home, &pid).ok().flatten();
            let acc = by_project
                .entry(name.clone())
                .or_insert_with(|| ProjectAccumulator::from_config(cfg.clone()));
            acc.users.insert(user.clone());
            // Capture chain depth + leaf role from the first user we see;
            // multiple users on the same envoy holding the same project
            // typically have identical chains (each holds their own under
            // the same puddle identity), but if they differ we keep the
            // first as the displayed snapshot.
            if acc.token_chain_depth == 0 {
                if let Some(c) = chain.as_ref() {
                    acc.token_chain_depth = c.chain.len();
                    if let Some(leaf) = c.chain.last() {
                        acc.leaf_role = Some(format!("{:?}", leaf.body.role));
                        acc.token_expires_at = match leaf.body.expires_at {
                            None => None,
                            Some(NEVER_EXPIRES) => None,
                            Some(t) => Some(t),
                        };
                        let _ = leaf.body.role; // shut up dead-code lint
                        let _ = Role::Member;
                    }
                }
            }
        }
    }

    let mut out = Vec::with_capacity(by_project.len());
    for (pid_hex, mut acc) in by_project {
        let pid = match parse_hex_id(&pid_hex) {
            Ok(p) => p,
            Err(_) => continue,
        };
        // Bare repo + head refs + envelopes are per-envoy (not per-user).
        let repo = project_repo_path(gitback_base, &pid);
        let has_bare_repo = repo.exists();
        let bare_repo_size_bytes = if has_bare_repo {
            dir_size_bytes(&repo).unwrap_or(0)
        } else {
            0
        };
        let head_refs = if has_bare_repo {
            fan_out::snapshot_refs(&repo).unwrap_or_default()
        } else {
            BTreeMap::new()
        };
        let revokes = envelopes::load_revokes(gitback_base, &pid)
            .map(|v| v.len())
            .unwrap_or(0);
        let leaves = envelopes::load_leaves(gitback_base, &pid)
            .map(|v| v.len())
            .unwrap_or(0);
        let digest_hex = envelopes::project_digest(gitback_base, &pid)
            .map(|d| hex::encode(d))
            .unwrap_or_default();

        let users_with_state: Vec<String> = acc.users.into_iter().collect();
        out.push(ProjectEntry {
            project_id: pid_hex,
            handle: std::mem::take(&mut acc.handle),
            founder_puddle_pubkey: std::mem::take(&mut acc.founder_puddle_pubkey),
            members: std::mem::take(&mut acc.members),
            dr_scope: std::mem::take(&mut acc.dr_scope),
            users_with_state,
            has_bare_repo,
            bare_repo_size_bytes,
            head_refs,
            envelopes: EnvelopeSummary {
                revokes,
                leaves,
                digest_hex,
            },
            leaf_role: acc.leaf_role,
            token_chain_depth: acc.token_chain_depth,
            token_expires_at: acc.token_expires_at,
        });
    }
    out
}

struct ProjectAccumulator {
    handle: String,
    founder_puddle_pubkey: String,
    members: Vec<String>,
    dr_scope: String,
    users: BTreeSet<String>,
    leaf_role: Option<String>,
    token_chain_depth: usize,
    token_expires_at: Option<i64>,
}

impl ProjectAccumulator {
    fn from_config(cfg: ProjectConfig) -> Self {
        Self {
            handle: cfg.handle,
            founder_puddle_pubkey: cfg.founder_puddle_pubkey,
            members: cfg.members,
            dr_scope: cfg.dr_scope.as_str().to_string(),
            users: BTreeSet::new(),
            leaf_role: None,
            token_chain_depth: 0,
            token_expires_at: None,
        }
    }
}

/// Production passwd path, env-overridable for tests.
pub(crate) fn passwd_path() -> PathBuf {
    if let Some(p) = std::env::var_os("VIGO_GITBACK_PASSWD_FILE") {
        return PathBuf::from(p);
    }
    PathBuf::from("/etc/passwd")
}

fn walk_passwd(body: &str) -> Vec<(String, PathBuf)> {
    let mut out = Vec::new();
    for line in body.lines() {
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        let fields: Vec<&str> = line.split(':').collect();
        if fields.len() < 7 {
            continue;
        }
        let name = fields[0].to_string();
        let home = PathBuf::from(fields[5]);
        out.push((name, home));
    }
    out
}

/// Sum of every regular file under `dir`. Errors collapse to 0 — the
/// trait shouldn't fail an entire envoy's check-in over a bare repo
/// permissions blip; the operator notices missing data instead.
fn dir_size_bytes(dir: &Path) -> Result<u64> {
    let mut total: u64 = 0;
    for entry in walkdir(dir) {
        if let Ok(meta) = std::fs::metadata(&entry) {
            if meta.is_file() {
                total = total.saturating_add(meta.len());
            }
        }
    }
    Ok(total)
}

fn walkdir(dir: &Path) -> Vec<PathBuf> {
    let mut stack = vec![dir.to_path_buf()];
    let mut out = Vec::new();
    while let Some(p) = stack.pop() {
        let entries = match std::fs::read_dir(&p) {
            Ok(it) => it,
            Err(_) => continue,
        };
        for entry in entries.flatten() {
            let path = entry.path();
            if path.is_dir() {
                stack.push(path);
            } else {
                out.push(path);
            }
        }
    }
    out
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
    use crate::swarm::gitback::project_state::DrScope;
    use ed25519_dalek::SigningKey;
    use rand::rngs::OsRng;
    use tempfile::tempdir;

    fn fresh_key() -> SigningKey {
        SigningKey::generate(&mut OsRng)
    }

    fn pub_bytes(k: &SigningKey) -> [u8; ID_LEN] {
        k.verifying_key().to_bytes()
    }

    /// Build a ProjectConfig for a freshly-generated founder, write
    /// it under `<home>/.vigo-gitback/<pid>/project.json`. Returns
    /// the project_id.
    fn seed_project(home: &Path, handle: &str) -> [u8; ID_LEN] {
        use crate::swarm::anchor::tokens::project_id_for;
        let founder = fresh_key();
        let pid = project_id_for(&pub_bytes(&founder), "proj");
        let cfg = ProjectConfig {
            version: PROJECT_STATE_VERSION,
            project_id: hex::encode(pid),
            handle: handle.to_string(),
            founder_puddle_pubkey: hex::encode(pub_bytes(&founder)),
            members: vec![hex::encode(pub_bytes(&founder))],
            dr_scope: DrScope::SwarmWide,
            cross_subnet_members: vec![],
            updated_at: 1_700_000_000,
            tombstoned_at: None,
        };
        cfg.save(home).unwrap();
        pid
    }

    #[test]
    fn collect_returns_null_when_no_passwd() {
        // Point passwd at a missing file.
        let body_path = tempdir().unwrap();
        let missing = body_path.path().join("nonexistent");
        std::env::set_var("VIGO_GITBACK_PASSWD_FILE", &missing);
        let v = collect().unwrap();
        std::env::remove_var("VIGO_GITBACK_PASSWD_FILE");
        assert!(v.is_null());
    }

    #[test]
    fn collect_returns_null_when_no_users_have_state() {
        let dir = tempdir().unwrap();
        let body = format!("nobody:x:99:99::{}:/bin/sh\n", dir.path().display());
        let projects = collect_projects(&body, dir.path());
        assert!(projects.is_empty());
    }

    #[test]
    fn collect_collapses_multi_user_holders() {
        let base = tempdir().unwrap();
        let dan_home = base.path().join("home/dan");
        let alice_home = base.path().join("home/alice");
        std::fs::create_dir_all(&dan_home).unwrap();
        std::fs::create_dir_all(&alice_home).unwrap();

        // Same project_id from a single founder, persisted under both
        // users' dot-dirs (e.g., they were both invited as members).
        use crate::swarm::anchor::tokens::project_id_for;
        let founder = fresh_key();
        let pid = project_id_for(&pub_bytes(&founder), "proj");
        let cfg = ProjectConfig {
            version: PROJECT_STATE_VERSION,
            project_id: hex::encode(pid),
            handle: "team-notes".to_string(),
            founder_puddle_pubkey: hex::encode(pub_bytes(&founder)),
            members: vec![hex::encode(pub_bytes(&founder))],
            dr_scope: DrScope::MembersOnly,
            cross_subnet_members: vec![],
            updated_at: 1_700_000_000,
            tombstoned_at: None,
        };
        cfg.save(&dan_home).unwrap();
        cfg.save(&alice_home).unwrap();

        let body = format!(
            "dan:x:1000:1000::{}:/bin/sh\nalice:x:1001:1001::{}:/bin/sh\n",
            dan_home.display(),
            alice_home.display()
        );
        let projects = collect_projects(&body, base.path());
        assert_eq!(projects.len(), 1);
        let p = &projects[0];
        assert_eq!(p.handle, "team-notes");
        assert_eq!(p.users_with_state, vec!["alice", "dan"]);
        assert_eq!(p.dr_scope, "members-only");
        assert!(!p.has_bare_repo); // no bare repo seeded in this test
        assert_eq!(p.envelopes.revokes, 0);
    }

    #[test]
    fn collect_picks_up_envelope_counts_and_digest() {
        let base = tempdir().unwrap();
        let dan_home = base.path().join("home/dan");
        std::fs::create_dir_all(&dan_home).unwrap();
        let pid = seed_project(&dan_home, "vigo-web");

        // Drop in a revoke envelope.
        let admin = fresh_key();
        let env = envelopes::RevokeEnvelope::build(&admin, &pid, &[0x01u8; ID_LEN], 100, "");
        envelopes::save_revoke(base.path(), &env).unwrap();

        let body = format!("dan:x:1000:1000::{}:/bin/sh\n", dan_home.display());
        let projects = collect_projects(&body, base.path());
        assert_eq!(projects.len(), 1);
        assert_eq!(projects[0].envelopes.revokes, 1);
        assert_eq!(projects[0].envelopes.leaves, 0);
        assert!(!projects[0].envelopes.digest_hex.is_empty());
    }

    #[test]
    fn collect_skips_pid_dirs_with_invalid_names() {
        let base = tempdir().unwrap();
        let dan_home = base.path().join("home/dan");
        std::fs::create_dir_all(&dan_home).unwrap();
        // Valid pid + a junk dirname should appear once.
        seed_project(&dan_home, "good");
        std::fs::create_dir_all(dan_home.join(".vigo-gitback").join("not-a-pid")).unwrap();

        let body = format!("dan:x:1000:1000::{}:/bin/sh\n", dan_home.display());
        let projects = collect_projects(&body, base.path());
        assert_eq!(projects.len(), 1);
    }

    #[test]
    fn collect_reports_bare_repo_when_present_and_round_trips_json() {
        let base = tempdir().unwrap();
        let dan_home = base.path().join("home/dan");
        std::fs::create_dir_all(&dan_home).unwrap();
        let pid = seed_project(&dan_home, "vigo");
        // Materialize a bare repo at the canonical path, as a first
        // push / DR catch-up would. A non-empty directory is enough to
        // exercise `has_bare_repo` + `bare_repo_size_bytes`; we don't
        // need a real git repo here.
        let repo = project_repo_path(base.path(), &pid);
        std::fs::create_dir_all(&repo).unwrap();
        std::fs::write(repo.join("HEAD"), b"ref: refs/heads/main\n").unwrap();

        let body = format!("dan:x:1000:1000::{}:/bin/sh\n", dan_home.display());
        let projects = collect_projects(&body, base.path());
        assert_eq!(projects.len(), 1);
        assert!(projects[0].has_bare_repo, "bare repo dir exists");
        assert!(projects[0].bare_repo_size_bytes > 0);

        // The helper-daemon `STATUS` reply is exactly this JSON.
        let json = serde_json::to_string(&projects).unwrap();
        let back: Vec<ProjectEntry> = serde_json::from_str(&json).unwrap();
        assert_eq!(back.len(), 1);
        assert!(back[0].has_bare_repo);
        assert_eq!(back[0].project_id, projects[0].project_id);
        assert_eq!(back[0].handle, "vigo");
    }

    #[test]
    fn dir_size_bytes_sums_files_recursively() {
        let dir = tempdir().unwrap();
        std::fs::write(dir.path().join("a"), b"hello").unwrap(); // 5
        std::fs::create_dir_all(dir.path().join("sub")).unwrap();
        std::fs::write(dir.path().join("sub/b"), b"world!").unwrap(); // 6
        assert_eq!(dir_size_bytes(dir.path()).unwrap(), 11);
    }

    #[test]
    fn collect_name_claims_reads_per_user_claims() {
        use crate::swarm::puddle::name::{NameClaim, NameClaimBody};
        let base = tempdir().unwrap();
        let dan_home = base.path().join("home/dan");
        let alice_home = base.path().join("home/alice");
        std::fs::create_dir_all(dan_home.join(".vigo-puddle")).unwrap();
        std::fs::create_dir_all(alice_home.join(".vigo-puddle")).unwrap();

        let dan_key = SigningKey::generate(&mut OsRng);
        let dan_claim = NameClaim::issue(
            &dan_key,
            NameClaimBody {
                puddle_pubkey: hex::encode(pub_bytes(&dan_key)),
                name: "dan".to_string(),
                claimed_at: 1_700_000_000,
                supersedes: vec![],
            },
        )
        .unwrap();
        std::fs::write(
            dan_home.join(".vigo-puddle/name-claim.json"),
            serde_json::to_vec_pretty(&dan_claim).unwrap(),
        )
        .unwrap();

        // Alice's file is corrupt — must be skipped, not crash.
        std::fs::write(alice_home.join(".vigo-puddle/name-claim.json"), b"{not json").unwrap();

        let body = format!(
            "dan:x:1000:1000::{}:/bin/sh\nalice:x:1001:1001::{}:/bin/sh\n",
            dan_home.display(),
            alice_home.display()
        );
        let claims = collect_name_claims(&body);
        assert_eq!(claims.len(), 1);
        assert_eq!(claims[0].user, "dan");
        assert_eq!(claims[0].claim.body.name, "dan");
    }

    #[test]
    fn collect_name_claims_drops_non_self_verifying() {
        use crate::swarm::puddle::name::{NameClaim, NameClaimBody};
        let base = tempdir().unwrap();
        let home = base.path().join("home/eve");
        std::fs::create_dir_all(home.join(".vigo-puddle")).unwrap();
        let key = SigningKey::generate(&mut OsRng);
        let mut claim = NameClaim::issue(
            &key,
            NameClaimBody {
                puddle_pubkey: hex::encode(pub_bytes(&key)),
                name: "eve".to_string(),
                claimed_at: 1,
                supersedes: vec![],
            },
        )
        .unwrap();
        // Tamper the signature so it no longer verifies.
        claim.signature = hex::encode([0u8; 64]);
        std::fs::write(
            home.join(".vigo-puddle/name-claim.json"),
            serde_json::to_vec_pretty(&claim).unwrap(),
        )
        .unwrap();
        let body = format!("eve:x:1002:1002::{}:/bin/sh\n", home.display());
        assert!(collect_name_claims(&body).is_empty());
    }
}
