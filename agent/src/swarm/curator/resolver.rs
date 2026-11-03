// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Resolve a curator artifact reference to a blob, fetch the bytes, verify, and
//! materialize them under `/var/lib/vigo/artifacts/` (ADR-024).
//!
//! Resolution is server-mediated (the server owns the aggregated catalog and the
//! moderation backstop); if the server is unreachable, a swarm-enabled envoy
//! falls back to its check-in-cached catalog ([`super::catalog_cache`]) — it
//! still verifies the signed entry end-to-end, so a stale or absent cache just
//! fails the resolve, never trusts a bad entry. The bytes come from the local
//! swarm blob cache when this envoy holds the `target` replica, otherwise from
//! the server's `/api/v1/swarm/curator/blob/<sha>` relay; either way the sha is
//! verified before the blob is hardlinked (copied on a cross-filesystem fall
//! back) into `artifacts/<artifact_id>/<name>/<version>/<os>-<arch>` (0644) and
//! the `cache.json` index is updated.
//!
//! The `<artifact_id>` (hex) is always the path/cache-key namespace here — a
//! self-certifying form that needs no name-map lookup. The friendly-name form
//! (`<puddle-name>/<name>`) is a presentation concern for the `vigo swarm
//! curator pull/resolve` CLI (slice 4), which has the ADR-022 name map; it
//! resolves the name to the hex artifact_id and calls in here. There is no
//! caller of [`Resolver`] in slice 3 yet — `pull` (slice 4) and the `artifact:`
//! executor param (slice 6) are its first ones.

use std::io::Read;
use std::path::{Path, PathBuf};
use std::time::Duration;

use anyhow::{anyhow, bail, Context, Result};
use serde::Deserialize;
use sha2::{Digest, Sha256};

use crate::swarm::curator::catalog::CatalogEntry;
use crate::swarm::curator::catalog_cache;
use crate::swarm::anchor::envelopes::DELEGATION_MAX_LEN;
use crate::update::TlsCerts;

/// Short timeout for the small JSON `/resolve` call.
const RESOLVE_TIMEOUT: Duration = Duration::from_secs(20);
/// Read-buffer size for streaming a blob.
const STREAM_CHUNK: usize = 64 * 1024;

/// What a successful resolve yields: the verified signed entry plus the concrete
/// `{version, os, arch}` build's blob coordinates. `tag` is set only when the
/// reference was a tag the entry carries (so `cache.json` records the `:<tag>`
/// alias as well as the `@<version>` one).
#[derive(Debug, Clone)]
pub struct Resolution {
    pub artifact_id_hex: String,
    pub name: String,
    pub version: String,
    pub tag: Option<String>,
    pub os: String,
    pub arch: String,
    pub blob_sha: String,
    pub size: u64,
    pub entry: CatalogEntry,
}

/// Resolves + materializes curator artifacts. Construct with [`Resolver::new`]
/// for the production paths; tests use [`Resolver::with_dirs`] to point the blob
/// cache and the artifacts root at tempdirs.
pub struct Resolver {
    api_addr: String,
    tls: Option<TlsCerts>,
    blobs_dir: PathBuf,
    artifacts_dir: PathBuf,
}

impl Resolver {
    /// Production constructor: uses the default swarm blob cache and the default
    /// `/var/lib/vigo/artifacts/` root. `tls` is `None` pre-enrollment (the
    /// server-mediated paths then can't run; a swarm-enabled envoy can still
    /// resolve from its local catalog cache and materialize a locally-held blob).
    pub fn new(api_addr: impl Into<String>, tls: Option<TlsCerts>) -> Self {
        Self {
            api_addr: api_addr.into(),
            tls,
            blobs_dir: crate::platform::paths::default_blobs_dir(),
            artifacts_dir: catalog_cache::artifacts_dir(),
        }
    }

    /// Test constructor: explicit blob cache + artifacts root.
    pub fn with_dirs(api_addr: impl Into<String>, tls: Option<TlsCerts>, blobs_dir: PathBuf, artifacts_dir: PathBuf) -> Self {
        Self { api_addr: api_addr.into(), tls, blobs_dir, artifacts_dir }
    }

    /// Read-only accessor: the directory this resolver materializes into and
    /// the `catalog.json` / `cache.json` it reads. The registry shim
    /// ([`crate::swarm::curator::registry`]) consults it so its
    /// `catalog_cache::load_catalog_in` calls hit the same on-disk catalog the
    /// resolver does — production and tests stay in lockstep without the shim
    /// re-deriving the path.
    pub fn artifacts_dir(&self) -> &Path {
        &self.artifacts_dir
    }

    /// Resolve `<artifact_id_hex>` + `<tag-or-version>` + `<os>/<arch>` to a
    /// [`Resolution`]. Asks the server first; on a network error, falls back to
    /// the check-in-cached catalog. The returned entry has been verified
    /// (signature + ADR-019 delegation chain) and `artifact_id` confirmed.
    pub fn resolve(&self, artifact_id_hex: &str, tag_or_version: &str, os: &str, arch: &str) -> Result<Resolution> {
        let aid = artifact_id_hex.to_ascii_lowercase();
        if !is_hex64(&aid) {
            bail!("artifact_id must be 64 hex characters");
        }
        if tag_or_version.is_empty() {
            bail!("a tag or version is required");
        }
        if os.is_empty() || arch.is_empty() {
            bail!("os and arch are required");
        }
        match self.resolve_via_server(&aid, tag_or_version, os, arch) {
            Ok(r) => Ok(r),
            Err(server_err) => {
                tracing::debug!(error = %server_err, "curator resolve: server path failed, trying the cached catalog");
                self.resolve_via_cache(&aid, tag_or_version, os, arch)
                    .with_context(|| format!("server resolve failed ({server_err:#}) and the cached catalog has no answer"))
            }
        }
    }

    /// Fetch the blob, verify the sha, materialize it, update `cache.json`, and
    /// return the materialized path. Idempotent: if the destination already
    /// exists, returns it without re-fetching.
    pub fn materialize(&self, r: &Resolution) -> Result<PathBuf> {
        let dest = self
            .artifacts_dir
            .join(&r.artifact_id_hex)
            .join(&r.name)
            .join(&r.version)
            .join(format!("{}-{}", r.os, r.arch));
        if dest.exists() {
            return Ok(dest);
        }
        let cache = crate::swarm::cache::BlobCache::new(self.blobs_dir.clone()).context("opening swarm blob cache")?;
        let blob_path = if cache.has_blob(&r.blob_sha) {
            cache.blob_path(&r.blob_sha)
        } else {
            self.fetch_blob_to_cache(&cache, r)?
        };
        if let Some(parent) = dest.parent() {
            std::fs::create_dir_all(parent).with_context(|| format!("creating {}", parent.display()))?;
        }
        materialize_file(&blob_path, &dest)?;
        self.update_cache_map(r, &dest).context("updating cache.json")?;
        tracing::info!(artifact_id = %r.artifact_id_hex, name = %r.name, version = %r.version, platform = %format!("{}/{}", r.os, r.arch), path = %dest.display(), "curator: materialized artifact");
        Ok(dest)
    }

    /// `resolve` + `materialize`.
    pub fn pull(&self, artifact_id_hex: &str, tag_or_version: &str, os: &str, arch: &str) -> Result<PathBuf> {
        let r = self.resolve(artifact_id_hex, tag_or_version, os, arch)?;
        self.materialize(&r)
    }

    // ---------- internals ----------

    fn resolve_via_server(&self, aid: &str, tag_or_version: &str, os: &str, arch: &str) -> Result<Resolution> {
        let tls = self.tls.as_ref().ok_or_else(|| anyhow!("no mTLS material — cannot reach the server"))?;
        if self.api_addr.is_empty() {
            bail!("no server address configured");
        }
        let url = format!(
            "{}/api/v1/swarm/curator/resolve?artifact_id={}&tag={}&os={}&arch={}",
            self.api_addr.trim_end_matches('/'),
            aid,
            urlencode(tag_or_version),
            urlencode(os),
            urlencode(arch),
        );
        let agent = build_agent(tls, Some(RESOLVE_TIMEOUT))?;
        let resp = agent.get(&url).call().map_err(|e| anyhow!("GET {url}: {e}"))?;
        if resp.status() != 200 {
            bail!("GET {url}: status {}", resp.status());
        }
        let body = resp.into_body().read_to_vec().context("reading resolve response")?;
        #[derive(Deserialize)]
        struct Wire {
            artifact_id: String,
            version: String,
            blob_sha: String,
            size: u64,
            signed_entry: CatalogEntry,
        }
        let w: Wire = serde_json::from_slice(&body).context("parsing resolve response")?;
        self.finish_resolution(aid, &w.artifact_id, w.signed_entry, &w.version, &w.blob_sha, w.size, tag_or_version, os, arch)
    }

    fn resolve_via_cache(&self, aid: &str, tag_or_version: &str, os: &str, arch: &str) -> Result<Resolution> {
        let entries = catalog_cache::load_catalog_in(&self.artifacts_dir);
        let entry = catalog_cache::lookup_verified(&entries, aid)
            .ok_or_else(|| anyhow!("no verifiable cached catalog entry for {aid}"))?
            .clone();
        let version = catalog_cache::resolve_version(&entry, tag_or_version)
            .ok_or_else(|| anyhow!("no such tag or version {tag_or_version:?} for {aid}"))?;
        let platform = catalog_cache::resolve_platform(&entry, &version, os, arch)
            .ok_or_else(|| anyhow!("artifact {aid} has no build for {os}/{arch} at version {version}"))?;
        let (sha, size) = (platform.blob_sha.clone(), platform.size);
        self.finish_resolution(aid, aid, entry, &version, &sha, size, tag_or_version, os, arch)
    }

    #[allow(clippy::too_many_arguments)]
    fn finish_resolution(
        &self,
        want_aid: &str,
        got_aid: &str,
        entry: CatalogEntry,
        version: &str,
        blob_sha: &str,
        size: u64,
        tag_or_version: &str,
        os: &str,
        arch: &str,
    ) -> Result<Resolution> {
        entry.verify(DELEGATION_MAX_LEN).context("resolved catalog entry failed verification")?;
        let derived = entry.artifact_id_hex().context("deriving artifact_id from the resolved entry")?;
        if derived != want_aid || (!got_aid.is_empty() && got_aid.to_ascii_lowercase() != want_aid) {
            bail!("resolved entry's artifact_id ({derived}) doesn't match the requested one ({want_aid})");
        }
        // Confirm the build the server (or cache) named is actually in the entry.
        let av = entry.body.versions.get(version).ok_or_else(|| anyhow!("version {version:?} is not in the entry"))?;
        let p = av.platforms.iter().find(|p| p.os == os && p.arch == arch).ok_or_else(|| anyhow!("platform {os}/{arch} is not in version {version:?} of the entry"))?;
        if !p.blob_sha.eq_ignore_ascii_case(blob_sha) || p.size != size {
            bail!("the resolved blob coordinates don't match the entry's record for {os}/{arch} @ {version}");
        }
        let tag = if entry.body.tags.contains_key(tag_or_version) { Some(tag_or_version.to_string()) } else { None };
        Ok(Resolution {
            artifact_id_hex: want_aid.to_string(),
            name: entry.body.name.clone(),
            version: version.to_string(),
            tag,
            os: os.to_string(),
            arch: arch.to_string(),
            blob_sha: blob_sha.to_ascii_lowercase(),
            size,
            entry,
        })
    }

    /// Stream the blob from the server's relay into the swarm blob cache (so this
    /// envoy becomes a P2P source for it too), verifying the sha. Returns the
    /// cached blob's path.
    fn fetch_blob_to_cache(&self, cache: &crate::swarm::cache::BlobCache, r: &Resolution) -> Result<PathBuf> {
        let tls = self.tls.as_ref().ok_or_else(|| anyhow!("no mTLS material — cannot fetch blob {} from the server", &r.blob_sha[..12]))?;
        if self.api_addr.is_empty() {
            bail!("no server address configured to fetch blob {}", &r.blob_sha[..12]);
        }
        let url = format!("{}/api/v1/swarm/curator/blob/{}", self.api_addr.trim_end_matches('/'), r.blob_sha);
        let agent = build_agent(tls, None)?; // no global timeout — a 2 GiB image over a slow link is legitimate
        let resp = agent.get(&url).call().map_err(|e| anyhow!("GET {url}: {e}"))?;
        if resp.status() != 200 {
            bail!("GET {url}: status {}", resp.status());
        }
        let stage = self.blobs_dir.join(format!(".curator-pull.{}.tmp", std::process::id()));
        let got = stream_to_file(&mut resp.into_body().into_reader(), &stage, r.size)
            .inspect_err(|_| {
                let _ = std::fs::remove_file(&stage);
            })?;
        if !got.eq_ignore_ascii_case(&r.blob_sha) {
            let _ = std::fs::remove_file(&stage);
            bail!("fetched blob hashes to {got}, expected {}", r.blob_sha);
        }
        let dest = cache.blob_path(&r.blob_sha);
        if cache.has_blob(&r.blob_sha) {
            let _ = std::fs::remove_file(&stage);
        } else {
            if let Some(parent) = dest.parent() {
                std::fs::create_dir_all(parent).ok();
            }
            std::fs::rename(&stage, &dest).with_context(|| format!("moving fetched blob into {}", dest.display()))?;
        }
        Ok(dest)
    }

    fn update_cache_map(&self, r: &Resolution, dest: &Path) -> Result<()> {
        let mut map = catalog_cache::load_cache_map_in(&self.artifacts_dir);
        let dest_s = dest.to_string_lossy().to_string();
        map.entries.insert(format!("{}/{}@{}", r.artifact_id_hex, r.name, r.version), dest_s.clone());
        if let Some(tag) = &r.tag {
            map.entries.insert(format!("{}/{}:{}", r.artifact_id_hex, r.name, tag), dest_s);
        }
        catalog_cache::write_cache_map_in(&self.artifacts_dir, &map)
    }
}

// ---------- free helpers ----------

/// Hardlink `src` → `dest`, falling back to a copy across filesystems / when the
/// link target already exists; then set `dest` to mode 0644.
fn materialize_file(src: &Path, dest: &Path) -> Result<()> {
    match std::fs::hard_link(src, dest) {
        Ok(()) => {}
        Err(_) => {
            std::fs::copy(src, dest).with_context(|| format!("copying {} -> {}", src.display(), dest.display()))?;
        }
    }
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let _ = std::fs::set_permissions(dest, std::fs::Permissions::from_mode(0o644));
    }
    Ok(())
}

/// Stream exactly `expected` bytes from `rdr` into a fresh file at `path`,
/// returning the lowercase sha256 hex of what was written. Errors if the stream
/// ends early or runs long.
fn stream_to_file(rdr: &mut impl Read, path: &Path, expected: u64) -> Result<String> {
    let mut f = std::fs::File::create(path).with_context(|| format!("creating {}", path.display()))?;
    let mut hasher = Sha256::new();
    let mut buf = vec![0u8; STREAM_CHUNK];
    let mut total: u64 = 0;
    loop {
        let n = rdr.read(&mut buf).context("reading blob bytes")?;
        if n == 0 {
            break;
        }
        total += n as u64;
        if total > expected {
            bail!("blob stream is longer than the declared {expected} bytes");
        }
        hasher.update(&buf[..n]);
        use std::io::Write;
        f.write_all(&buf[..n]).context("writing staged blob")?;
    }
    if total != expected {
        bail!("blob stream ended after {total} of {expected} bytes");
    }
    Ok(hex::encode(hasher.finalize()))
}

fn build_agent(tls: &TlsCerts, timeout: Option<Duration>) -> Result<ureq::Agent> {
    use ureq::tls::{parse_pem, PemItem};
    let ca_pem = std::fs::read(&tls.ca_cert).context("read CA cert")?;
    let cert_pem = std::fs::read(&tls.client_cert).context("read client cert")?;
    let key_pem = std::fs::read(&tls.client_key).context("read client key")?;
    let mut ca_certs = Vec::new();
    for item in parse_pem(&ca_pem) {
        if let Ok(PemItem::Certificate(c)) = item {
            ca_certs.push(c);
        }
    }
    let mut client_certs = Vec::new();
    for item in parse_pem(&cert_pem) {
        if let Ok(PemItem::Certificate(c)) = item {
            client_certs.push(c);
        }
    }
    let mut client_key = None;
    for item in parse_pem(&key_pem) {
        if let Ok(PemItem::PrivateKey(k)) = item {
            client_key = Some(k);
            break;
        }
    }
    let key = client_key.context("no private key found in client key PEM")?;
    let tls_cfg = ureq::tls::TlsConfig::builder()
        .root_certs(ureq::tls::RootCerts::new_with_certs(&ca_certs))
        .client_cert(Some(ureq::tls::ClientCert::new_with_certs(&client_certs, key)))
        .build();
    Ok(ureq::Agent::new_with_config(
        ureq::config::Config::builder().tls_config(tls_cfg).timeout_global(timeout).build(),
    ))
}

fn urlencode(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    for b in s.bytes() {
        match b {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'_' | b'.' | b'~' => out.push(b as char),
            _ => out.push_str(&format!("%{b:02X}")),
        }
    }
    out
}

fn is_hex64(s: &str) -> bool {
    s.len() == 64 && s.bytes().all(|b| b.is_ascii_hexdigit())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::swarm::cache::BlobCache;
    use crate::swarm::curator::catalog::{ArtifactVersion, CatalogEntryBody, Platform, KIND_GENERIC};
    use ed25519_dalek::SigningKey;
    use rand::rngs::OsRng;
    use std::collections::BTreeMap;
    use tempfile::TempDir;

    /// Build an entry over real blob bytes, returning (entry, blob, sha).
    fn entry_for(k: &SigningKey, name: &str, blob: &[u8]) -> (CatalogEntry, String) {
        let sha = hex::encode(Sha256::digest(blob));
        let mut versions = BTreeMap::new();
        versions.insert(
            "1.0.0".to_string(),
            ArtifactVersion { platforms: vec![Platform { os: "linux".into(), arch: "amd64".into(), blob_sha: sha.clone(), size: blob.len() as u64 }] },
        );
        let mut tags = BTreeMap::new();
        tags.insert("latest".to_string(), "1.0.0".to_string());
        let e = CatalogEntry::issue(
            k,
            CatalogEntryBody {
                schema_version: 1,
                kind: KIND_GENERIC.into(),
                name: name.into(),
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
        .unwrap();
        (e, sha)
    }

    #[test]
    fn resolve_falls_back_to_the_cached_catalog() {
        let arts = TempDir::new().unwrap();
        let blobs = TempDir::new().unwrap();
        let k = SigningKey::generate(&mut OsRng);
        let blob = b"the artifact bytes".to_vec();
        let (entry, sha) = entry_for(&k, "vigo-agent", &blob);
        let aid = entry.artifact_id_hex().unwrap();
        catalog_cache::write_catalog_in(arts.path(), &serde_json::to_vec(&vec![entry.clone()]).unwrap()).unwrap();

        // Empty api_addr ⇒ the server path errors immediately ⇒ falls back to the cache.
        let r = Resolver::with_dirs("", None, blobs.path().to_path_buf(), arts.path().to_path_buf());
        let res = r.resolve(&aid, "latest", "linux", "amd64").unwrap();
        assert_eq!(res.version, "1.0.0");
        assert_eq!(res.blob_sha, sha);
        assert_eq!(res.name, "vigo-agent");
        assert_eq!(res.tag.as_deref(), Some("latest"));
        // Unknown tag / platform ⇒ error.
        assert!(r.resolve(&aid, "nope", "linux", "amd64").is_err());
        assert!(r.resolve(&aid, "latest", "darwin", "arm64").is_err());
        // Unknown artifact ⇒ error.
        assert!(r.resolve(&"f".repeat(64), "latest", "linux", "amd64").is_err());
    }

    #[test]
    fn materialize_from_a_locally_cached_blob() {
        let arts = TempDir::new().unwrap();
        let blobs = TempDir::new().unwrap();
        let k = SigningKey::generate(&mut OsRng);
        let blob = b"some bytes for the artifact".to_vec();
        let (entry, sha) = entry_for(&k, "thing", &blob);
        let aid = entry.artifact_id_hex().unwrap();
        catalog_cache::write_catalog_in(arts.path(), &serde_json::to_vec(&vec![entry]).unwrap()).unwrap();
        // Pre-seed the blob in the cache (as if the swarm Manager had downloaded it).
        let cache = BlobCache::new(blobs.path().to_path_buf()).unwrap();
        std::fs::write(cache.blob_path(&sha), &blob).unwrap();
        assert!(cache.has_blob(&sha));

        let r = Resolver::with_dirs("", None, blobs.path().to_path_buf(), arts.path().to_path_buf());
        let res = r.resolve(&aid, "latest", "linux", "amd64").unwrap();
        let dest = r.materialize(&res).unwrap();

        assert!(dest.exists(), "materialized file must exist at {}", dest.display());
        assert_eq!(std::fs::read(&dest).unwrap(), blob);
        assert!(dest.ends_with(format!("{aid}/thing/1.0.0/linux-amd64")), "dest = {}", dest.display());
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            assert_eq!(std::fs::metadata(&dest).unwrap().permissions().mode() & 0o777, 0o644);
        }
        // cache.json carries both the @version and :tag aliases.
        let m = catalog_cache::load_cache_map_in(arts.path());
        assert_eq!(m.entries.get(&format!("{aid}/thing@1.0.0")).map(String::as_str), Some(dest.to_string_lossy().as_ref()));
        assert_eq!(m.entries.get(&format!("{aid}/thing:latest")).map(String::as_str), Some(dest.to_string_lossy().as_ref()));
        // Idempotent.
        assert_eq!(r.materialize(&res).unwrap(), dest);
    }

    #[test]
    fn materialize_without_the_blob_and_no_server_errors_cleanly() {
        let arts = TempDir::new().unwrap();
        let blobs = TempDir::new().unwrap();
        let k = SigningKey::generate(&mut OsRng);
        let blob = b"unavailable".to_vec();
        let (entry, _) = entry_for(&k, "x", &blob);
        let aid = entry.artifact_id_hex().unwrap();
        catalog_cache::write_catalog_in(arts.path(), &serde_json::to_vec(&vec![entry]).unwrap()).unwrap();
        let r = Resolver::with_dirs("", None, blobs.path().to_path_buf(), arts.path().to_path_buf());
        let res = r.resolve(&aid, "latest", "linux", "amd64").unwrap();
        let err = r.materialize(&res).unwrap_err();
        assert!(format!("{err:#}").contains("no server address") || format!("{err:#}").contains("no mTLS"), "got: {err:#}");
    }
}
