// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Agent-side curator state under `/var/lib/vigo/artifacts/` (ADR-024):
//!
//! - `catalog.json` — a cached copy of the fleet artifact catalog (the JSON
//!   `[]CatalogEntry` the server attaches to every `CheckInResponse`), refreshed
//!   on every accepted check-in. Lets a swarm-enabled envoy resolve `<name>:<tag>`
//!   locally through a server outage (it holds the DR replica for its
//!   `target`-matched artifacts and the catalog entry to point at it). Same
//!   "server-pushed, agent-cached, agent-verified" shape as the ADR-022 fleet
//!   name map.
//! - `cache.json` — the materialization index: `<ns>/<name>:<tag>` and
//!   `<ns>/<name>@<version>` → the path under `artifacts/<ns>/<name>/<version>/<os>-<arch>`
//!   where a pulled blob landed. Written by the resolver/materializer; read by
//!   `vigo swarm curator pull/resolve` and (post-v1) the `artifact:` executor param.
//!
//! All write paths are atomic temp+rename, mode 0644 (root-managed cache — the
//! trait collector and the resolver run as root; the materialized blob files are
//! 0644 too, per the ADR's "materialization-gating, not byte-confidentiality"
//! stance). The `_in` / `_to` variants take an explicit directory so tests stay
//! hermetic without touching a process-wide env var.

use std::collections::BTreeMap;
use std::path::{Path, PathBuf};

use anyhow::{Context, Result};
use serde::{Deserialize, Serialize};

use crate::swarm::curator::catalog::CatalogEntry;

/// Root of the agent's curator state. Override with `VIGO_CURATOR_ARTIFACTS_DIR`
/// (tests; the `_in`/`_to` variants below are the preferred test seam).
pub const ARTIFACTS_DIR: &str = "/var/lib/vigo/artifacts";

/// Resolve the artifacts dir, honouring the env override.
pub fn artifacts_dir() -> PathBuf {
    std::env::var_os("VIGO_CURATOR_ARTIFACTS_DIR")
        .map(PathBuf::from)
        .unwrap_or_else(|| PathBuf::from(ARTIFACTS_DIR))
}

/// Path to the cached fleet catalog under `dir`.
pub fn catalog_path_in(dir: &Path) -> PathBuf {
    dir.join("catalog.json")
}

/// Path to the materialization index under `dir`.
pub fn cache_map_path_in(dir: &Path) -> PathBuf {
    dir.join("cache.json")
}

// ---------- the cached fleet catalog ----------

/// Validate that `bytes` is a JSON array of [`CatalogEntry`], then persist it
/// verbatim to `<dir>/catalog.json`. An empty / whitespace-only / non-array
/// payload is treated as "no catalog this cycle" — the existing cache is left
/// untouched (so a transient server hiccup that drops the field doesn't wipe a
/// usable cache). Best-effort: callers (the check-in loop) log a warning on
/// error and carry on.
pub fn write_catalog_in(dir: &Path, bytes: &[u8]) -> Result<()> {
    let trimmed = bytes.iter().position(|b| !b.is_ascii_whitespace());
    if trimmed.is_none() {
        return Ok(()); // empty payload — keep whatever we have
    }
    // Parse-validate so a corrupt push never lands as our cache.
    let _: Vec<CatalogEntry> = serde_json::from_slice(bytes).context("parsing pushed curator catalog")?;
    std::fs::create_dir_all(dir).with_context(|| format!("creating {}", dir.display()))?;
    let path = catalog_path_in(dir);
    let tmp = path.with_extension("json.tmp");
    std::fs::write(&tmp, bytes).with_context(|| format!("writing {}", tmp.display()))?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let _ = std::fs::set_permissions(&tmp, std::fs::Permissions::from_mode(0o644));
    }
    std::fs::rename(&tmp, &path).with_context(|| format!("renaming {} -> {}", tmp.display(), path.display()))?;
    Ok(())
}

/// Persist the pushed catalog to the default artifacts dir.
pub fn write_catalog(bytes: &[u8]) -> Result<()> {
    write_catalog_in(&artifacts_dir(), bytes)
}

/// Read the cached fleet catalog from `<dir>/catalog.json`. Missing / unreadable
/// / unparseable ⇒ an empty `Vec` (resolution then has to go to the server).
pub fn load_catalog_in(dir: &Path) -> Vec<CatalogEntry> {
    let bytes = match std::fs::read(catalog_path_in(dir)) {
        Ok(b) => b,
        Err(_) => return Vec::new(),
    };
    serde_json::from_slice(&bytes).unwrap_or_default()
}

/// Read the cached fleet catalog from the default artifacts dir.
pub fn load_catalog() -> Vec<CatalogEntry> {
    load_catalog_in(&artifacts_dir())
}

/// Find the catalog entry whose `artifact_id` (hex) matches `artifact_id_hex`,
/// dropping any that don't self-verify (signature + ADR-019 delegation chain) —
/// the agent never trusts a cached entry it can't verify, same as the trait
/// collector and the server-side aggregator's consumers.
pub fn lookup_verified<'a>(entries: &'a [CatalogEntry], artifact_id_hex: &str) -> Option<&'a CatalogEntry> {
    use crate::swarm::anchor::envelopes::DELEGATION_MAX_LEN;
    let want = artifact_id_hex.to_ascii_lowercase();
    entries.iter().find(|e| {
        e.artifact_id_hex().map(|a| a == want).unwrap_or(false) && e.verify(DELEGATION_MAX_LEN).is_ok()
    })
}

/// Resolve a tag or version string to a version present in `entry`. A tag wins
/// over a literal version; a literal version is accepted if it exists. Mirrors
/// `curatormesh.ResolveVersion` on the server.
pub fn resolve_version(entry: &CatalogEntry, tag_or_version: &str) -> Option<String> {
    if let Some(v) = entry.body.tags.get(tag_or_version) {
        if entry.body.versions.contains_key(v) {
            return Some(v.clone());
        }
    }
    if entry.body.versions.contains_key(tag_or_version) {
        return Some(tag_or_version.to_string());
    }
    None
}

/// The `{os, arch}` platform record for a version of `entry`, or `None`.
pub fn resolve_platform<'a>(
    entry: &'a CatalogEntry,
    version: &str,
    os: &str,
    arch: &str,
) -> Option<&'a crate::swarm::curator::catalog::Platform> {
    entry
        .body
        .versions
        .get(version)?
        .platforms
        .iter()
        .find(|p| p.os == os && p.arch == arch)
}

// ---------- the materialization index (cache.json) ----------

/// The on-disk shape of `cache.json`: a flat map from a human reference
/// (`<ns>/<name>:<tag>` or `<ns>/<name>@<version>`) to the absolute path of the
/// materialized blob.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct CacheMap {
    #[serde(default)]
    pub entries: BTreeMap<String, String>,
}

/// Read `cache.json` from `<dir>`. Missing / corrupt ⇒ an empty map.
pub fn load_cache_map_in(dir: &Path) -> CacheMap {
    match std::fs::read(cache_map_path_in(dir)) {
        Ok(b) => serde_json::from_slice(&b).unwrap_or_default(),
        Err(_) => CacheMap::default(),
    }
}

/// Read `cache.json` from the default artifacts dir.
pub fn load_cache_map() -> CacheMap {
    load_cache_map_in(&artifacts_dir())
}

/// Atomically write `map` to `<dir>/cache.json` (temp+rename, 0644).
pub fn write_cache_map_in(dir: &Path, map: &CacheMap) -> Result<()> {
    std::fs::create_dir_all(dir).with_context(|| format!("creating {}", dir.display()))?;
    let path = cache_map_path_in(dir);
    let tmp = path.with_extension("json.tmp");
    let bytes = serde_json::to_vec_pretty(map).context("serializing cache.json")?;
    std::fs::write(&tmp, &bytes).with_context(|| format!("writing {}", tmp.display()))?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let _ = std::fs::set_permissions(&tmp, std::fs::Permissions::from_mode(0o644));
    }
    std::fs::rename(&tmp, &path).with_context(|| format!("renaming {} -> {}", tmp.display(), path.display()))?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::swarm::curator::catalog::{ArtifactVersion, CatalogEntryBody, Platform, KIND_GENERIC};
    use ed25519_dalek::SigningKey;
    use rand::rngs::OsRng;
    use tempfile::TempDir;

    fn entry(k: &SigningKey, name: &str) -> CatalogEntry {
        let mut versions = BTreeMap::new();
        versions.insert(
            "1.0.0".to_string(),
            ArtifactVersion { platforms: vec![Platform { os: "linux".into(), arch: "amd64".into(), blob_sha: "a".repeat(64), size: 7 }] },
        );
        versions.insert(
            "2.0.0".to_string(),
            ArtifactVersion { platforms: vec![Platform { os: "linux".into(), arch: "arm64".into(), blob_sha: "b".repeat(64), size: 9 }] },
        );
        let mut tags = BTreeMap::new();
        tags.insert("latest".to_string(), "2.0.0".to_string());
        tags.insert("dangling".to_string(), "9.9.9".to_string()); // points at a missing version
        CatalogEntry::issue(
            k,
            CatalogEntryBody {
                schema_version: 1,
                name: name.into(),
                kind: KIND_GENERIC.into(),
                founder_puddle_pubkey: hex::encode(k.verifying_key().to_bytes()),
                signing_puddle_pubkey: hex::encode(k.verifying_key().to_bytes()),
                versions,
                tags,
                recipients: vec!["*".into()],
                target: "*".into(),
                issued_at: 1,
                delegations: vec![],
            },
        )
        .unwrap()
    }

    #[test]
    fn catalog_write_validates_and_round_trips() {
        let dir = TempDir::new().unwrap();
        let k = SigningKey::generate(&mut OsRng);
        let entries = vec![entry(&k, "vigo-agent")];
        let bytes = serde_json::to_vec(&entries).unwrap();
        write_catalog_in(dir.path(), &bytes).unwrap();
        let got = load_catalog_in(dir.path());
        assert_eq!(got, entries);
        // Empty payload ⇒ no-op (cache untouched).
        write_catalog_in(dir.path(), b"   \n").unwrap();
        assert_eq!(load_catalog_in(dir.path()), entries);
        // Garbage ⇒ error, cache untouched.
        assert!(write_catalog_in(dir.path(), b"not json").is_err());
        assert_eq!(load_catalog_in(dir.path()), entries);
    }

    #[test]
    fn lookup_drops_unverifiable_entries() {
        let dir = TempDir::new().unwrap();
        let k = SigningKey::generate(&mut OsRng);
        let good = entry(&k, "good");
        let mut bad = entry(&k, "bad");
        bad.body.name = "bad-tampered".into(); // breaks the signature; artifact_id recomputes to a new id
        let aid_bad = bad.artifact_id_hex().unwrap();
        let aid_good = good.artifact_id_hex().unwrap();
        let entries = vec![good.clone(), bad];
        write_catalog_in(dir.path(), &serde_json::to_vec(&entries).unwrap()).unwrap();
        let cached = load_catalog_in(dir.path());
        assert!(lookup_verified(&cached, &aid_good).is_some());
        assert!(lookup_verified(&cached, &aid_bad).is_none(), "a tampered entry must not resolve");
        assert!(lookup_verified(&cached, &"f".repeat(64)).is_none());
    }

    #[test]
    fn version_and_platform_resolution() {
        let k = SigningKey::generate(&mut OsRng);
        let e = entry(&k, "x");
        assert_eq!(resolve_version(&e, "latest").as_deref(), Some("2.0.0"));
        assert_eq!(resolve_version(&e, "1.0.0").as_deref(), Some("1.0.0"));
        assert_eq!(resolve_version(&e, "dangling"), None); // tag → missing version
        assert_eq!(resolve_version(&e, "nope"), None);
        assert!(resolve_platform(&e, "1.0.0", "linux", "amd64").is_some());
        assert!(resolve_platform(&e, "1.0.0", "linux", "arm64").is_none());
        assert!(resolve_platform(&e, "2.0.0", "linux", "arm64").is_some());
        assert!(resolve_platform(&e, "9.9.9", "linux", "amd64").is_none());
    }

    #[test]
    fn cache_map_round_trips() {
        let dir = TempDir::new().unwrap();
        let mut m = CacheMap::default();
        m.entries.insert("ops/vigo-agent:latest".into(), "/var/lib/vigo/artifacts/ops/vigo-agent/2.0.0/linux-amd64".into());
        m.entries.insert("ops/vigo-agent@2.0.0".into(), "/var/lib/vigo/artifacts/ops/vigo-agent/2.0.0/linux-amd64".into());
        write_cache_map_in(dir.path(), &m).unwrap();
        let got = load_cache_map_in(dir.path());
        assert_eq!(got.entries, m.entries);
        // Missing file ⇒ empty.
        let empty_dir = TempDir::new().unwrap();
        assert!(load_cache_map_in(empty_dir.path()).entries.is_empty());
    }
}
