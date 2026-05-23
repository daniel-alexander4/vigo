// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Reports curator-published artifacts for the `curator` trait namespace (ADR-024).
//!
//! Walks every Unix user's `~/.vigo-curator/<artifact_id>/entry.json` and emits
//! the full signed [`crate::swarm::curator::catalog::CatalogEntry`] verbatim. The
//! server's `curatormesh` aggregator joins these into the fleet catalog (keyed by
//! artifact_id, last-write-wins on `issued_at`); the agent resolver and
//! `vigocli swarm curator inspect` verify the signature + ADR-019 delegation
//! chain end-to-end without trusting the server — the same "server publishes,
//! never arbitrates" stance gitback's name claims take. A locally-corrupt,
//! misfiled (dir name ≠ artifact_id), or non-self-verifying entry is dropped
//! here rather than published.
//!
//! Top-level shape (`Value::Null` ⇒ no curator state on this envoy, so no trait):
//!
//! ```jsonc
//! { "updated_at": <unix-seconds>,
//!   "artifact_count": 2,
//!   "artifacts": [ { ...full CatalogEntry... }, ... ] }
//! ```
//!
//! Slice 1 establishes the on-disk layout (`~/.vigo-curator/<artifact_id>/entry.json`)
//! and this collector contract; the helper-daemon publish path that *writes* that
//! layout lands in slice 2 (ADR-024).

use std::collections::BTreeMap;
use std::path::Path;

use anyhow::Result;
use serde_json::{json, Value};

use super::gitback::passwd_path;
use crate::swarm::curator::catalog::CatalogEntry;
use crate::swarm::anchor::envelopes::DELEGATION_MAX_LEN;

pub fn collect() -> Result<Value> {
    let passwd_body = match std::fs::read_to_string(passwd_path()) {
        Ok(s) => s,
        Err(_) => return Ok(Value::Null),
    };
    let artifacts = collect_artifacts(&passwd_body);
    if artifacts.is_empty() {
        return Ok(Value::Null);
    }
    Ok(json!({
        "updated_at": unix_seconds_now(),
        "artifact_count": artifacts.len(),
        "artifacts": artifacts,
    }))
}

/// Walk passwd, read each user's `~/.vigo-curator/<artifact_id>/entry.json`,
/// keep the self-verifying ones whose directory name matches their artifact_id,
/// and collapse duplicates (the same artifact_id held by two users on this envoy
/// → keep the higher `issued_at`). Sorted by artifact_id for deterministic trait
/// output. Pulled out for testability.
pub(crate) fn collect_artifacts(passwd_body: &str) -> Vec<CatalogEntry> {
    let mut by_id: BTreeMap<String, CatalogEntry> = BTreeMap::new();
    for line in passwd_body.lines() {
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        let fields: Vec<&str> = line.split(':').collect();
        if fields.len() < 7 {
            continue;
        }
        let dot = crate::swarm::curator::state::user_curator_dir(Path::new(fields[5]));
        let dir_it = match std::fs::read_dir(&dot) {
            Ok(it) => it,
            Err(_) => continue,
        };
        for ent in dir_it.flatten() {
            let p = ent.path();
            if !p.is_dir() {
                continue;
            }
            let raw = match std::fs::read_to_string(p.join(crate::swarm::curator::state::ENTRY_FILE)) {
                Ok(s) => s,
                Err(_) => continue,
            };
            let entry: CatalogEntry = match serde_json::from_str(&raw) {
                Ok(e) => e,
                Err(_) => continue,
            };
            let aid = match entry.artifact_id_hex() {
                Ok(a) => a,
                Err(_) => continue,
            };
            // Directory name must equal the entry's own artifact_id, and the
            // entry must self-verify, or it never enters the fleet catalog
            // through us.
            if p.file_name().and_then(|n| n.to_str()) != Some(aid.as_str()) {
                continue;
            }
            if entry.verify(DELEGATION_MAX_LEN).is_err() {
                continue;
            }
            match by_id.get(&aid) {
                Some(existing) if existing.body.issued_at >= entry.body.issued_at => {}
                _ => {
                    by_id.insert(aid, entry);
                }
            }
        }
    }
    by_id.into_values().collect()
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
    use crate::swarm::curator::catalog::{ArtifactVersion, CatalogEntryBody, Platform, KIND_GENERIC};
    use ed25519_dalek::SigningKey;
    use rand::rngs::OsRng;
    use tempfile::tempdir;

    fn pub_hex(k: &SigningKey) -> String {
        hex::encode(k.verifying_key().to_bytes())
    }

    fn entry_signed_by(k: &SigningKey, name: &str, issued_at: i64) -> CatalogEntry {
        let mut versions = BTreeMap::new();
        versions.insert(
            "1.0.0".to_string(),
            ArtifactVersion {
                platforms: vec![Platform {
                    os: "linux".into(),
                    arch: "amd64".into(),
                    blob_sha: "a".repeat(64),
                    size: 1,
                }],
            },
        );
        let mut tags = BTreeMap::new();
        tags.insert("latest".to_string(), "1.0.0".to_string());
        let body = CatalogEntryBody {
            schema_version: 1,
            name: name.into(),
            kind: KIND_GENERIC.into(),
            founder_puddle_pubkey: pub_hex(k),
            signing_puddle_pubkey: pub_hex(k),
            versions,
            tags,
            recipients: vec!["*".into()],
            target: "*".into(),
            issued_at,
            delegations: vec![],
        };
        CatalogEntry::issue(k, body).unwrap()
    }

    fn write_entry_under(home: &Path, dir_name: &str, entry: &CatalogEntry) {
        let dir = home.join(".vigo-curator").join(dir_name);
        std::fs::create_dir_all(&dir).unwrap();
        std::fs::write(dir.join("entry.json"), serde_json::to_string(entry).unwrap()).unwrap();
    }

    fn passwd_line(user: &str, uid: u32, home: &Path) -> String {
        format!("{user}:x:{uid}:{uid}::{}:/bin/sh\n", home.display())
    }

    #[test]
    fn empty_when_no_state() {
        let tmp = tempdir().unwrap();
        assert!(collect_artifacts(&passwd_line("dan", 1000, tmp.path())).is_empty());
        // Also fine when the dir doesn't exist or passwd is junk.
        assert!(collect_artifacts("garbage\n#comment\n").is_empty());
    }

    #[test]
    fn picks_up_a_valid_entry() {
        let tmp = tempdir().unwrap();
        let k = SigningKey::generate(&mut OsRng);
        let entry = entry_signed_by(&k, "vigo-agent", 100);
        write_entry_under(tmp.path(), &entry.artifact_id_hex().unwrap(), &entry);
        let got = collect_artifacts(&passwd_line("dan", 1000, tmp.path()));
        assert_eq!(got.len(), 1);
        assert_eq!(got[0].body.name, "vigo-agent");
        got[0].verify(DELEGATION_MAX_LEN).unwrap();
    }

    #[test]
    fn drops_corrupt_misfiled_and_tampered_entries() {
        let tmp = tempdir().unwrap();
        // 1. Not JSON.
        let d1 = tmp.path().join(".vigo-curator").join("a".repeat(64));
        std::fs::create_dir_all(&d1).unwrap();
        std::fs::write(d1.join("entry.json"), b"not json").unwrap();
        // 2. Valid entry, filed under the wrong artifact_id directory.
        let k = SigningKey::generate(&mut OsRng);
        let good = entry_signed_by(&k, "x", 1);
        write_entry_under(tmp.path(), &"b".repeat(64), &good);
        // 3. Tampered (mutated name ⇒ artifact_id recomputes to the dir name
        //    we file it under, so the dir-name check passes, but the signature
        //    no longer verifies).
        let mut tampered = entry_signed_by(&k, "y", 1);
        tampered.body.name = "yy".into();
        write_entry_under(tmp.path(), &tampered.artifact_id_hex().unwrap(), &tampered);

        assert!(collect_artifacts(&passwd_line("dan", 1000, tmp.path())).is_empty());
    }

    #[test]
    fn dedups_by_artifact_id_keeping_higher_issued_at() {
        // Two Unix users sharing one puddle key → same artifact_id → newest wins.
        let tmp1 = tempdir().unwrap();
        let tmp2 = tempdir().unwrap();
        let k = SigningKey::generate(&mut OsRng);
        let older = entry_signed_by(&k, "shared", 100);
        let newer = entry_signed_by(&k, "shared", 200);
        write_entry_under(tmp1.path(), &older.artifact_id_hex().unwrap(), &older);
        write_entry_under(tmp2.path(), &newer.artifact_id_hex().unwrap(), &newer);
        let passwd = format!(
            "{}{}",
            passwd_line("dan", 1000, tmp1.path()),
            passwd_line("ann", 1001, tmp2.path()),
        );
        let got = collect_artifacts(&passwd);
        assert_eq!(got.len(), 1);
        assert_eq!(got[0].body.issued_at, 200);
    }
}
