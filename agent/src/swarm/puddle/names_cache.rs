// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Local cache of the fleet friendly-name map (ADR-022).
//!
//! The server aggregates every puddle's name claim (gossiped via the
//! `gitback` trait) into a `name → claims` map and serves it at
//! `GET /api/v1/swarm/puddle/names`. The agent's `names_bootstrap`
//! loop fetches that periodically and on startup, writing the result
//! here as world-readable JSON. Three consumers read it, all *off*
//! the check-in hot path:
//!
//! - `vigo swarm puddle name set <name>` — refuse-check ("is this
//!   name already claimed by a different puddle?").
//! - the `git-remote-gitback` resolver — `<name>` → puddle pubkey →
//!   `project_id = sha256(pubkey ‖ handle)` (via the helper daemon).
//! - `vigo swarm puddle name show` — surfaces the fleet view.
//!
//! Entries are the **full signed claims** (`NameClaim`), so the
//! resolver can verify the signature and the `supersedes` chain
//! itself rather than trusting the server's aggregation. The server
//! publishes; it never arbitrates. A name with one claim is
//! unambiguous; a name with two unrelated claims is a *collision* and
//! the resolver errors (pointing at the hex form); two claims where
//! the newer one's `supersedes` chain links back to the older are a
//! `re-found`, and the resolver follows it.

use std::collections::BTreeMap;
use std::path::{Path, PathBuf};

use anyhow::{Context, Result};
use serde::{Deserialize, Serialize};

use super::name::NameClaim;

/// `/var/lib/vigo/swarm/puddle/` — the daemon-owned per-envoy puddle
/// state dir (currently just the names cache). Env-overridable for
/// tests via `VIGO_PUDDLE_DIR`.
pub fn puddle_state_dir() -> PathBuf {
    if let Some(p) = std::env::var_os("VIGO_PUDDLE_DIR") {
        return PathBuf::from(p);
    }
    #[cfg(target_os = "macos")]
    {
        PathBuf::from("/Library/Vigo/swarm/puddle")
    }
    #[cfg(all(unix, not(target_os = "macos")))]
    {
        PathBuf::from("/var/lib/vigo/swarm/puddle")
    }
    #[cfg(windows)]
    {
        crate::platform::paths::default_data_dir()
            .join("swarm")
            .join("puddle")
    }
}

/// Path of the fleet name-map cache file.
pub fn names_cache_path() -> PathBuf {
    puddle_state_dir().join("names.json")
}

/// On-disk shape — also the wire shape of `GET /api/v1/swarm/puddle/names`.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct FleetNameMap {
    /// Unix seconds the server generated this snapshot. 0 if unknown.
    #[serde(default)]
    pub updated_at: i64,
    /// `name` → the signed claims for it. Length 1 = unambiguous;
    /// length >1 = either a collision or a `re-found` supersession,
    /// disambiguated by walking each claim's `supersedes` chain.
    #[serde(default)]
    pub names: BTreeMap<String, Vec<NameClaim>>,
}

impl FleetNameMap {
    /// Load the cache file. Missing file → empty map (the resolver
    /// then can't resolve any friendly name, which surfaces as a
    /// helpful error pointing the user at the hex form). A
    /// present-but-corrupt file is an error — better to fail loudly
    /// than silently resolve nothing.
    pub fn load() -> Result<FleetNameMap> {
        Self::load_from(&names_cache_path())
    }

    pub fn load_from(path: &Path) -> Result<FleetNameMap> {
        match std::fs::read(path) {
            Ok(bytes) => {
                let m: FleetNameMap = serde_json::from_slice(&bytes)
                    .with_context(|| format!("parsing {}", path.display()))?;
                Ok(m)
            }
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(FleetNameMap::default()),
            Err(e) => Err(e).with_context(|| format!("reading {}", path.display())),
        }
    }

    /// Verified claims for `name`, with the puddle pubkey decoded.
    /// Drops any claim whose signature doesn't verify (a malformed or
    /// tampered entry in the published map shouldn't poison the rest).
    pub fn verified_claims(&self, name: &str) -> Vec<(NameClaim, [u8; 32])> {
        let Some(raw) = self.names.get(name) else {
            return Vec::new();
        };
        let mut out = Vec::with_capacity(raw.len());
        for claim in raw {
            if claim.verify_signature().is_err() {
                continue;
            }
            match claim.puddle_pubkey_bytes() {
                Ok(pk) => out.push((claim.clone(), pk)),
                Err(_) => continue,
            }
        }
        out
    }

    /// If `name` is currently claimed by a puddle whose pubkey is NOT
    /// `my_pubkey_hex`, return that pubkey (hex). Used by `name set`
    /// to refuse the *accidental* collision (Alice and Bob both
    /// reaching for `mainbox`). Does not — cannot — beat the
    /// propagation window or a deliberate squatter; see ADR-022.
    ///
    /// A claim whose `supersedes` chain links back to *my* pubkey is
    /// not a conflict (it's a `re-found` of my own name and a re-set
    /// would just collide with my live anchor — caller handles that).
    /// We keep the check simple: any verified claim under a different
    /// pubkey blocks, unless it equals mine.
    pub fn claimed_by_other(&self, name: &str, my_pubkey_hex: &str) -> Option<String> {
        let mine = my_pubkey_hex.to_ascii_lowercase();
        for (claim, _) in self.verified_claims(name) {
            let theirs = claim.body.puddle_pubkey.to_ascii_lowercase();
            if theirs != mine {
                return Some(theirs);
            }
        }
        None
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::swarm::puddle::name::{NameClaim, NameClaimBody};
    use ed25519_dalek::SigningKey;
    use rand::rngs::OsRng;

    fn claim_for(name: &str) -> (NameClaim, SigningKey) {
        let key = SigningKey::generate(&mut OsRng);
        let body = NameClaimBody {
            puddle_pubkey: hex::encode(key.verifying_key().to_bytes()),
            name: name.to_string(),
            claimed_at: 1_700_000_000,
            supersedes: vec![],
        };
        (NameClaim::issue(&key, body).unwrap(), key)
    }

    #[test]
    fn load_missing_is_empty() {
        let m = FleetNameMap::load_from(Path::new("/no/such/names.json")).unwrap();
        assert!(m.names.is_empty());
    }

    #[test]
    fn verified_claims_drops_bad_signature() {
        let (mut bad, _) = claim_for("dan");
        bad.signature = hex::encode([0u8; 64]);
        let (good, _) = claim_for("dan");
        let mut m = FleetNameMap::default();
        m.names.insert("dan".to_string(), vec![bad, good.clone()]);
        let v = m.verified_claims("dan");
        assert_eq!(v.len(), 1);
        assert_eq!(v[0].0.body.puddle_pubkey, good.body.puddle_pubkey);
    }

    #[test]
    fn claimed_by_other_detects_conflict() {
        let (mine, my_key) = claim_for("dan");
        let (theirs, _) = claim_for("dan");
        let mut m = FleetNameMap::default();
        m.names
            .insert("dan".to_string(), vec![mine.clone(), theirs.clone()]);
        let my_hex = hex::encode(my_key.verifying_key().to_bytes());
        // Their claim blocks me.
        assert_eq!(
            m.claimed_by_other("dan", &my_hex).as_deref(),
            Some(theirs.body.puddle_pubkey.as_str())
        );
        // A name only I hold is free.
        let mut m2 = FleetNameMap::default();
        m2.names.insert("dan".to_string(), vec![mine]);
        assert!(m2.claimed_by_other("dan", &my_hex).is_none());
        // An unclaimed name is free.
        assert!(m2.claimed_by_other("nobody", &my_hex).is_none());
    }
}
