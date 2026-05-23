// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Curator accelerator for agent self-update (ADR-024 slice 8).
//!
//! The agent's self-update path (see [`super::apply`]) already gets its
//! authoritative answer to "what version is current, what's the SHA, what's
//! the signature" from `GET /api/v1/agent/latest`. The only step that's
//! bandwidth-expensive is fetching the actual binary bytes. This module makes
//! that fetch *opportunistically* go through curator's P2P artifact substrate
//! when the operator has published the agent as a curator artifact — and
//! cleanly falls back to the REST `download_url` whenever curator can't or
//! won't serve.
//!
//! ### Constraints (from ADR-024)
//!
//! - **Registry never replaces `/api/v1/agent/latest`.** The REST endpoint
//!   stays the authoritative answer for "what version, what SHA, what
//!   signature." This module just changes the bytes-on-the-wire step.
//! - **Authority lives in the binary signature, not in curator's catalog
//!   signature.** The agent's ed25519 binary-signing key is a separate piece
//!   of operations material from any puddle key. After this module hands
//!   back bytes, the existing apply() flow runs `sha256(body) == expected_sha`
//!   and verifies the binary signature exactly as it does on the REST path.
//! - **Update must not break when curator is empty / broken / disabled.**
//!   Every error inside this module returns `None`; the apply() flow then
//!   runs the REST path unchanged. There is no "curator is required" mode.
//!
//! ### Operator convention
//!
//! Publish the agent binary as a curator artifact named `vigo-agent` under
//! your puddle, one platform per `(version, os, arch)` row, tag `<version>`
//! mapping to the same version:
//!
//! ```text
//! vigo swarm curator push vigo-agent:0.51.8 ./dist/agent/linux/amd64/vigo --platform linux/amd64
//! vigo swarm curator push vigo-agent:0.51.8 ./dist/agent/linux/arm64/vigo --platform linux/arm64
//! # ... one push per platform you ship to
//! vigo swarm curator tag vigo-agent:latest 0.51.8   # optional convenience tag
//! ```
//!
//! When the fleet's check-in reports a new server version, every envoy that
//! has the matching platform's blob in its swarm cache pulls from there
//! (~free) instead of from the server's `/dist/agent/<os>/<arch>/vigo` (~10s
//! of MB × fleet size against one server). Envoys missing the cache copy fall
//! back to REST. The published blob's SHA must match the server's
//! `/api/v1/agent/latest` SHA — if not, the accelerator emits one `warn!` and
//! falls back to REST.
//!
//! ### Implicit namespace
//!
//! Like slice 6's bare `<name>:<tag>` artifact form, the accelerator uses the
//! fleet's unique puddle-name claim as the implicit namespace. Zero or 2+
//! claimed names → the accelerator silently falls back. Operators who want
//! to nail down the namespace explicitly can use the explicit
//! `<puddle-name>/vigo-agent:<version>` push form — the resolution side
//! (this module) still uses bare-form, matching the operator-puddle
//! convention.

use std::path::PathBuf;

use tracing::{debug, info, warn};

use crate::swarm::curator::executor_resolver::{
    build_resolver_from_agent_config, resolve_repo_to_artifact_id,
};
use crate::swarm::curator::resolver::Resolver;
use crate::swarm::puddle::names_cache::FleetNameMap;

/// Production entry point — used inside `apply()` to try curator before REST.
///
/// Builds a fresh resolver and name map from the agent's config, then
/// delegates to [`fetch_via_curator_for`] which carries the test seam
/// (explicit resolver and name map). Returns `None` on every failure path;
/// the caller takes that as "use the REST `download_url`."
pub fn fetch_via_curator(
    version: &str,
    expected_sha: &str,
    os: &str,
    arch: &str,
) -> Option<Vec<u8>> {
    let resolver = match build_resolver_from_agent_config() {
        Ok(r) => r,
        Err(e) => {
            debug!(error = %e, "curator accelerator: no resolver available; falling back to REST");
            return None;
        }
    };
    let names = FleetNameMap::load().unwrap_or_default();
    fetch_via_curator_for(&resolver, &names, version, expected_sha, os, arch)
}

/// Test seam — takes an explicit resolver + fleet name map so unit tests don't
/// need the agent's on-disk config or a real /var/lib/vigo/swarm/puddle/names.json.
/// The production [`fetch_via_curator`] is a thin wrapper.
pub fn fetch_via_curator_for(
    resolver: &Resolver,
    names: &FleetNameMap,
    version: &str,
    expected_sha: &str,
    os: &str,
    arch: &str,
) -> Option<Vec<u8>> {
    let (aid_hex, _name) = match resolve_repo_to_artifact_id("vigo-agent", names) {
        Ok(v) => v,
        Err(e) => {
            debug!(error = %e, "curator accelerator: cannot resolve `vigo-agent` artifact ref; falling back to REST");
            return None;
        }
    };
    let resolution = match resolver.resolve(&aid_hex, version, os, arch) {
        Ok(r) => r,
        Err(e) => {
            debug!(
                error = %e,
                version,
                os,
                arch,
                "curator accelerator: artifact unresolvable for this version/platform; falling back to REST"
            );
            return None;
        }
    };
    if !resolution.blob_sha.eq_ignore_ascii_case(expected_sha) {
        warn!(
            artifact_blob_sha = %resolution.blob_sha,
            rest_sha = expected_sha,
            version = %resolution.version,
            "curator accelerator: published artifact's blob_sha does not match the REST update's sha — falling back to REST (operator likely needs to repush vigo-agent for this version)"
        );
        return None;
    }
    let path: PathBuf = match resolver.materialize(&resolution) {
        Ok(p) => p,
        Err(e) => {
            debug!(
                error = %e,
                "curator accelerator: materialization failed; falling back to REST"
            );
            return None;
        }
    };
    match std::fs::read(&path) {
        Ok(bytes) => {
            info!(
                version,
                path = %path.display(),
                size = bytes.len(),
                "curator accelerator: serving agent self-update from curator-published artifact"
            );
            Some(bytes)
        }
        Err(e) => {
            debug!(
                error = %e,
                path = %path.display(),
                "curator accelerator: cannot read materialized binary; falling back to REST"
            );
            None
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::swarm::cache::BlobCache;
    use crate::swarm::curator::catalog::{
        ArtifactVersion, CatalogEntry, CatalogEntryBody, Platform, KIND_GENERIC,
    };
    use crate::swarm::curator::catalog_cache;
    use crate::swarm::puddle::name::{NameClaim, NameClaimBody};
    use anyhow::Result;
    use ed25519_dalek::SigningKey;
    use rand::rngs::OsRng;
    use sha2::{Digest, Sha256};
    use std::collections::BTreeMap;
    use tempfile::TempDir;

    fn build_resolver(arts: &TempDir, blobs: &TempDir) -> Resolver {
        Resolver::with_dirs(
            "",
            None,
            blobs.path().to_path_buf(),
            arts.path().to_path_buf(),
        )
    }

    /// Build a catalog entry over real blob bytes + seed the swarm cache so
    /// `Resolver::materialize` succeeds without a server-relay hop.
    fn seed_artifact(
        arts: &TempDir,
        blobs: &TempDir,
        founder: &SigningKey,
        version: &str,
        os: &str,
        arch: &str,
        blob: &[u8],
    ) -> Result<(String, String)> {
        let sha = hex::encode(Sha256::digest(blob));
        let mut versions = BTreeMap::new();
        versions.insert(
            version.to_string(),
            ArtifactVersion {
                platforms: vec![Platform {
                    os: os.into(),
                    arch: arch.into(),
                    blob_sha: sha.clone(),
                    size: blob.len() as u64,
                }],
            },
        );
        let mut tags = BTreeMap::new();
        tags.insert("latest".to_string(), version.to_string());
        let entry = CatalogEntry::issue(
            founder,
            CatalogEntryBody {
                schema_version: 1,
                kind: KIND_GENERIC.into(),
                name: "vigo-agent".into(),
                founder_puddle_pubkey: hex::encode(founder.verifying_key().to_bytes()),
                signing_puddle_pubkey: hex::encode(founder.verifying_key().to_bytes()),
                versions,
                tags,
                recipients: vec!["*".into()],
                target: "*".into(),
                issued_at: 1,
                delegations: vec![],
            },
        )?;
        let aid = entry.artifact_id_hex()?;
        catalog_cache::write_catalog_in(
            arts.path(),
            &serde_json::to_vec(&vec![entry])?,
        )?;
        let cache = BlobCache::new(blobs.path().to_path_buf())?;
        std::fs::write(cache.blob_path(&sha), blob)?;
        Ok((aid, sha))
    }

    /// Build a fleet name map with one claim, so `vigo-agent:<tag>` (bare /
    /// implicit namespace) resolves to the founder's puddle.
    fn name_map_with(name: &str, founder: &SigningKey) -> Result<FleetNameMap> {
        let mut m = FleetNameMap::default();
        let body = NameClaimBody {
            puddle_pubkey: hex::encode(founder.verifying_key().to_bytes()),
            name: name.to_string(),
            claimed_at: 1,
            supersedes: vec![],
        };
        let claim = NameClaim::issue(founder, body)?;
        m.names.insert(name.to_string(), vec![claim]);
        Ok(m)
    }

    #[test]
    fn returns_bytes_when_sha_matches() {
        let arts = TempDir::new().unwrap();
        let blobs = TempDir::new().unwrap();
        let k = SigningKey::generate(&mut OsRng);
        let blob = b"PRETEND THIS IS A 30 MB VIGO BINARY".to_vec();
        let (_aid, sha) = seed_artifact(&arts, &blobs, &k, "0.51.8", "linux", "amd64", &blob).unwrap();
        let names = name_map_with("alexander4", &k).unwrap();
        let resolver = build_resolver(&arts, &blobs);

        let got = fetch_via_curator_for(&resolver, &names, "0.51.8", &sha, "linux", "amd64");
        assert_eq!(got, Some(blob));
    }

    #[test]
    fn returns_none_when_sha_differs_from_rest_sha() {
        let arts = TempDir::new().unwrap();
        let blobs = TempDir::new().unwrap();
        let k = SigningKey::generate(&mut OsRng);
        let blob = b"the curator-published bytes".to_vec();
        seed_artifact(&arts, &blobs, &k, "0.51.8", "linux", "amd64", &blob).unwrap();
        let names = name_map_with("alexander4", &k).unwrap();
        let resolver = build_resolver(&arts, &blobs);

        // REST says the sha is something else — operator pushed the wrong blob.
        let mismatched = "f".repeat(64);
        let got = fetch_via_curator_for(&resolver, &names, "0.51.8", &mismatched, "linux", "amd64");
        assert_eq!(got, None);
    }

    #[test]
    fn returns_none_when_no_artifact_for_version() {
        let arts = TempDir::new().unwrap();
        let blobs = TempDir::new().unwrap();
        let k = SigningKey::generate(&mut OsRng);
        let blob = b"only 0.51.7 is published".to_vec();
        let (_aid, sha) = seed_artifact(&arts, &blobs, &k, "0.51.7", "linux", "amd64", &blob).unwrap();
        let names = name_map_with("alexander4", &k).unwrap();
        let resolver = build_resolver(&arts, &blobs);

        // The server wants the agent to go to 0.51.8 but only 0.51.7 is in curator.
        let got = fetch_via_curator_for(&resolver, &names, "0.51.8", &sha, "linux", "amd64");
        assert_eq!(got, None);
    }

    #[test]
    fn returns_none_when_no_artifact_for_platform() {
        let arts = TempDir::new().unwrap();
        let blobs = TempDir::new().unwrap();
        let k = SigningKey::generate(&mut OsRng);
        let blob = b"only linux/amd64 is published".to_vec();
        let (_aid, sha) = seed_artifact(&arts, &blobs, &k, "0.51.8", "linux", "amd64", &blob).unwrap();
        let names = name_map_with("alexander4", &k).unwrap();
        let resolver = build_resolver(&arts, &blobs);

        // Host is darwin/arm64 — no matching platform.
        let got = fetch_via_curator_for(&resolver, &names, "0.51.8", &sha, "darwin", "arm64");
        assert_eq!(got, None);
    }

    #[test]
    fn returns_none_when_no_puddle_name_claimed() {
        let arts = TempDir::new().unwrap();
        let blobs = TempDir::new().unwrap();
        let k = SigningKey::generate(&mut OsRng);
        let blob = b"the bytes".to_vec();
        let (_aid, sha) = seed_artifact(&arts, &blobs, &k, "0.51.8", "linux", "amd64", &blob).unwrap();
        // Empty name map — implicit-namespace resolution errors.
        let names = FleetNameMap::default();
        let resolver = build_resolver(&arts, &blobs);

        let got = fetch_via_curator_for(&resolver, &names, "0.51.8", &sha, "linux", "amd64");
        assert_eq!(got, None);
    }

    #[test]
    fn returns_none_when_two_puddle_names_claimed() {
        let arts = TempDir::new().unwrap();
        let blobs = TempDir::new().unwrap();
        let k = SigningKey::generate(&mut OsRng);
        let blob = b"the bytes".to_vec();
        let (_aid, sha) = seed_artifact(&arts, &blobs, &k, "0.51.8", "linux", "amd64", &blob).unwrap();
        let mut names = name_map_with("alexander4", &k).unwrap();
        // A second claimed name with a different puddle — implicit-namespace
        // resolution errors (ambiguous).
        let k2 = SigningKey::generate(&mut OsRng);
        let body = NameClaimBody {
            puddle_pubkey: hex::encode(k2.verifying_key().to_bytes()),
            name: "operator-b".to_string(),
            claimed_at: 1,
            supersedes: vec![],
        };
        let claim = NameClaim::issue(&k2, body).unwrap();
        names.names.insert("operator-b".to_string(), vec![claim]);
        let resolver = build_resolver(&arts, &blobs);

        let got = fetch_via_curator_for(&resolver, &names, "0.51.8", &sha, "linux", "amd64");
        assert_eq!(got, None);
    }
}
