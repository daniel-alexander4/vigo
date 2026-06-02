// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Reports per-user puddle identity + health for the `puddle` trait
//! namespace (ADR-014).
//!
//! The puddle is the foundational per-user swarm identity that gitback,
//! curator, poolq, lockbox, and longdrawer all build on. This collector
//! is the **sole reporter** of puddle identity and state: the current
//! pubkey, the friendly-name claim (ADR-022), and the local health
//! signals (initialized, session unlocked, a pairing/rekey window open,
//! how many keypairs have been retired). Sibling subsystems only ever
//! *reference* puddle pubkeys — they never re-report them. The
//! server-side `puddlemesh` aggregator groups these per-(envoy, user)
//! entries by pubkey into the fleet puddle topology + owns the friendly
//! name map served at `GET /api/v1/swarm/puddle/names`.
//!
//! The collector walks every user in `/etc/passwd` and, for each one
//! whose `~/.vigo-puddle/identity.pub` is present, emits one entry.
//! Trait collection runs as root, so per-user "is the session unlocked?"
//! is read from the home-rooted `session.unlocked` sentinel (written by
//! the session helper, removed on lock) rather than the uid-bound helper
//! socket — the sentinel is the cross-subsystem signal documented in
//! `puddle::config`, and reading it gives a genuinely per-user answer.
//!
//! Returns `Value::Null` when no user on this envoy has a puddle —
//! matches `gitback::collect` / `lockbox::collect`.
//!
//! ### Per-entry payload
//!
//! ```jsonc
//! {
//!   "user":             "dan",
//!   "puddle_pubkey":    "<64-hex>",
//!   "initialized":      true,
//!   "session_unlocked": true,
//!   "pair_pending":     false,
//!   "rekey_pending":    false,
//!   "retired_count":    0,
//!   "name_claim": {            // omitted when the user has claimed no name
//!     "body": { "puddle_pubkey": "<64-hex>", "name": "dan",
//!               "claimed_at": <unix-seconds>, "supersedes": [ ... ] },
//!     "signature": "<128-hex>"
//!   }
//! }
//! ```
//!
//! Top-level shape:
//!
//! ```jsonc
//! {
//!   "updated_at":    <unix-seconds>,
//!   "puddle_count":  1,
//!   "puddles":      [ ... ]
//! }
//! ```

use std::path::{Path, PathBuf};

use anyhow::Result;
use serde::Serialize;
use serde_json::{json, Value};

use crate::swarm::puddle::name::{parse_pubkey_hex, NameClaim};
use crate::swarm::puddle::{config, is_initialized, local_puddle_pubkey};

/// One Unix user's puddle identity + health as observed on this envoy.
/// `name_claim` is the full signed [`NameClaim`] (ADR-022), published
/// verbatim so the server-side aggregator and resolvers verify the
/// signature + `supersedes` chain end-to-end without trusting the
/// server. A locally-corrupt or non-self-verifying claim is dropped
/// here rather than published.
#[derive(Debug, Serialize)]
pub struct PuddleEntry {
    pub user: String,
    pub puddle_pubkey: String,
    pub initialized: bool,
    pub session_unlocked: bool,
    pub pair_pending: bool,
    pub rekey_pending: bool,
    pub retired_count: usize,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub name_claim: Option<NameClaim>,
}

pub fn collect() -> Result<Value> {
    let passwd_body = match std::fs::read_to_string(passwd_path()) {
        Ok(s) => s,
        Err(_) => return Ok(Value::Null),
    };

    let puddles = collect_puddles(&passwd_body);
    if puddles.is_empty() {
        return Ok(Value::Null);
    }

    Ok(json!({
        "updated_at": unix_seconds_now(),
        "puddle_count": puddles.len(),
        "puddles": puddles,
    }))
}

/// Walk passwd and emit one entry per user who has initialized a puddle
/// on this envoy. Pulled out for testability; the env-var override on
/// the passwd path lets tests redirect onto a tempfile. Sorted by user
/// for deterministic trait output.
pub(crate) fn collect_puddles(passwd_body: &str) -> Vec<PuddleEntry> {
    let mut out = Vec::new();
    for (user, home) in walk_passwd(passwd_body) {
        // Identity present iff `identity.pub` is readable. Normalize to
        // raw 64-hex (strips any `ed25519:` prefix); a corrupt pubkey
        // drops the whole entry rather than publishing junk.
        let pubkey_raw = match local_puddle_pubkey(&home) {
            Ok(Some(s)) => s,
            _ => continue,
        };
        let puddle_pubkey = match parse_pubkey_hex(&pubkey_raw) {
            Ok(b) => hex::encode(b),
            Err(_) => continue,
        };

        // Name claim — verbatim, self-verifying only.
        let name_claim = match NameClaim::load(&home) {
            Ok(Some(claim)) if claim.verify_signature().is_ok() => Some(claim),
            _ => None,
        };

        out.push(PuddleEntry {
            user,
            puddle_pubkey,
            initialized: is_initialized(&home),
            session_unlocked: config::session_unlocked_path(&home).exists(),
            pair_pending: config::pair_pending_path(&home).exists(),
            rekey_pending: config::rekey_pending_path(&home).exists(),
            retired_count: retired_count(&home),
            name_claim,
        });
    }
    out.sort_by(|a, b| a.user.cmp(&b.user));
    out
}

/// Count retired puddle keypairs from `~/.vigo-puddle/retired_puddles.json`
/// (ADR-018). The file is a JSON array; a missing or malformed file
/// counts as zero — an informational signal must never fail an envoy's
/// check-in.
fn retired_count(home: &Path) -> usize {
    let path = config::retired_puddles_path(home);
    match std::fs::read_to_string(&path) {
        Ok(s) => serde_json::from_str::<Vec<Value>>(&s)
            .map(|v| v.len())
            .unwrap_or(0),
        Err(_) => 0,
    }
}

/// Production passwd path, env-overridable for tests.
fn passwd_path() -> PathBuf {
    if let Some(p) = std::env::var_os("VIGO_PUDDLE_PASSWD_FILE") {
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
        out.push((fields[0].to_string(), PathBuf::from(fields[5])));
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
    use crate::swarm::puddle::name::{NameClaim, NameClaimBody};
    use ed25519_dalek::SigningKey;
    use rand::rngs::OsRng;
    use tempfile::tempdir;

    fn seed_identity(home: &Path, pubkey_hex: &str) {
        let dot = config::dot_dir(home);
        std::fs::create_dir_all(&dot).unwrap();
        std::fs::write(config::pubkey_path(home), format!("ed25519:{}\n", pubkey_hex)).unwrap();
        std::fs::write(config::wrapped_path(home), b"wrapped").unwrap();
        std::fs::write(config::salt_path(home), b"salt").unwrap();
    }

    #[test]
    fn collect_returns_empty_when_no_puddles() {
        let dir = tempdir().unwrap();
        let body = format!("nobody:x:99:99::{}:/bin/sh\n", dir.path().display());
        assert!(collect_puddles(&body).is_empty());
    }

    #[test]
    fn collect_reports_initialized_puddle_with_name_claim() {
        let base = tempdir().unwrap();
        let dan_home = base.path().join("home/dan");

        let key = SigningKey::generate(&mut OsRng);
        let pubkey_hex = hex::encode(key.verifying_key().to_bytes());
        seed_identity(&dan_home, &pubkey_hex);

        // A valid name claim + an unlocked-session sentinel.
        let claim = NameClaim::issue(
            &key,
            NameClaimBody {
                puddle_pubkey: pubkey_hex.clone(),
                name: "dan".to_string(),
                claimed_at: 1_700_000_000,
                supersedes: vec![],
            },
        )
        .unwrap();
        std::fs::write(
            config::name_claim_path(&dan_home),
            serde_json::to_vec_pretty(&claim).unwrap(),
        )
        .unwrap();
        std::fs::write(config::session_unlocked_path(&dan_home), b"123\n").unwrap();

        let body = format!("dan:x:1000:1000::{}:/bin/sh\n", dan_home.display());
        let puddles = collect_puddles(&body);
        assert_eq!(puddles.len(), 1);
        let p = &puddles[0];
        assert_eq!(p.user, "dan");
        assert_eq!(p.puddle_pubkey, pubkey_hex);
        assert!(p.initialized);
        assert!(p.session_unlocked);
        assert!(!p.pair_pending);
        assert_eq!(p.retired_count, 0);
        assert_eq!(p.name_claim.as_ref().unwrap().body.name, "dan");
    }

    #[test]
    fn collect_drops_non_self_verifying_name_claim_but_keeps_puddle() {
        let base = tempdir().unwrap();
        let home = base.path().join("home/eve");

        let key = SigningKey::generate(&mut OsRng);
        let pubkey_hex = hex::encode(key.verifying_key().to_bytes());
        seed_identity(&home, &pubkey_hex);

        let mut claim = NameClaim::issue(
            &key,
            NameClaimBody {
                puddle_pubkey: pubkey_hex.clone(),
                name: "eve".to_string(),
                claimed_at: 1,
                supersedes: vec![],
            },
        )
        .unwrap();
        claim.signature = hex::encode([0u8; 64]); // tamper
        std::fs::write(
            config::name_claim_path(&home),
            serde_json::to_vec_pretty(&claim).unwrap(),
        )
        .unwrap();

        let body = format!("eve:x:1002:1002::{}:/bin/sh\n", home.display());
        let puddles = collect_puddles(&body);
        assert_eq!(puddles.len(), 1);
        assert_eq!(puddles[0].puddle_pubkey, pubkey_hex);
        assert!(puddles[0].name_claim.is_none());
    }

    #[test]
    fn collect_skips_users_without_identity() {
        let base = tempdir().unwrap();
        let home = base.path().join("home/noident");
        std::fs::create_dir_all(&home).unwrap();
        let body = format!("noident:x:1003:1003::{}:/bin/sh\n", home.display());
        assert!(collect_puddles(&body).is_empty());
    }
}
