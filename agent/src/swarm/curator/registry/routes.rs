// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Docker Registry HTTP v2 subset served over curator artifacts (ADR-024 slice 7).
//!
//! Endpoints we serve:
//!
//! - `GET /v2/`                                  — `200 OK`, version probe
//! - `GET /v2/<repo>/manifests/<reference>`      — image manifest by tag or digest
//! - `GET /v2/<repo>/blobs/<digest>`             — content-addressed blob (config / layer)
//! - `GET /v2/<repo>/tags/list`                  — `{"name":..., "tags":[...]}`
//! - HEAD on any of the GET endpoints — same headers, empty body
//!
//! Everything else → `405 Method Not Allowed` with a Docker v2 error body.
//!
//! The shim is a pure consumer of [`super::super::resolver::Resolver`]: parse the
//! URL, classify the request, hand off to the resolver to materialize the OCI
//! archive (idempotent on a cache hit), open the archive via
//! [`super::oci_archive::OciArchive`], serve the manifest/blob bytes from inside
//! it. Zero parallel fetch logic — the resolver owns blob acquisition, sha
//! verification, and the materialized-path layout.
//!
//! ### Platform selection
//!
//! Curator artifacts are single-arch per `{name, version, os, arch}` row in the
//! catalog — there is no multi-arch index inside one OCI archive. The shim
//! resolves the **host** arch via [`super::super::executor_resolver::current_platform`]
//! (`std::env::consts` → catalog vocab) and looks up the matching platform row.
//! A `(host_os, host_arch)` miss returns `404 manifest unknown` with a body
//! listing the platforms that **do** exist for the requested tag, so the
//! `docker pull` log on a wrong-arch host is actionable instead of silent.

use std::sync::Arc;

use anyhow::Context;
use http_body_util::Full;
use hyper::body::{Bytes, Incoming};
use hyper::{Method, Request, Response, StatusCode};
use serde::Serialize;

use super::oci_archive::{ArchiveFormat, OciArchive};
use crate::swarm::curator::catalog::Platform;
use crate::swarm::curator::catalog_cache;
use crate::swarm::curator::executor_resolver::{current_platform, resolve_repo_to_artifact_id};
use crate::swarm::curator::resolver::{Resolution, Resolver};
use crate::swarm::puddle::names_cache::FleetNameMap;

/// Header set on every response the shim emits. The Docker client treats this
/// as the "this is really a v2 registry" sentinel and refuses the connection
/// without it.
pub const API_VERSION_HEADER: &str = "Docker-Distribution-API-Version";
pub const API_VERSION_VALUE: &str = "registry/2.0";

/// Shared state for the registry shim — a single [`Resolver`] is enough.
/// Fleet name map is loaded fresh per request: it's small (a `BTreeMap` of
/// at most ~hundreds of name claims), changes rarely, and the
/// `RESOLVE-NAME` socket cost is `git-remote-gitback`-grade negligible.
pub struct ShimState {
    pub resolver: Resolver,
}

impl ShimState {
    pub fn new(resolver: Resolver) -> Self {
        Self { resolver }
    }
}

/// Entry point — every request lands here. Returns a fully-built response;
/// never errors at the `Result` layer (errors become 4xx/5xx responses).
pub async fn handle(
    req: Request<Incoming>,
    state: Arc<ShimState>,
) -> Result<Response<Full<Bytes>>, std::convert::Infallible> {
    let method = req.method().clone();
    let path = req.uri().path().to_string();
    let resp = route(&method, &path, &state).await;
    Ok(with_common_headers(resp))
}

async fn route(method: &Method, path: &str, state: &ShimState) -> Response<Full<Bytes>> {
    let head_only = method == Method::HEAD;
    if method != Method::GET && method != Method::HEAD {
        return method_not_allowed();
    }

    match parse_v2_path(path) {
        Some(V2Route::Root) => v2_root(head_only),
        Some(V2Route::Manifest { repo, reference }) => {
            handle_manifest(repo, reference, state, head_only).await
        }
        Some(V2Route::Blob { repo, digest }) => handle_blob(repo, digest, state, head_only).await,
        Some(V2Route::Tags { repo }) => handle_tags(repo, state, head_only).await,
        None => not_found_error("NAME_UNKNOWN", "path does not match a Docker Registry v2 route"),
    }
}

/// Parsed shape of a `/v2/...` URL. The repo name can contain `/`, so the
/// parser anchors on the verb suffix (`/manifests/<ref>`, `/blobs/<digest>`,
/// `/tags/list`), then takes everything before it as the repo.
#[derive(Debug, PartialEq, Eq)]
pub(crate) enum V2Route<'a> {
    Root,
    Manifest { repo: &'a str, reference: &'a str },
    Blob { repo: &'a str, digest: &'a str },
    Tags { repo: &'a str },
}

pub(crate) fn parse_v2_path(path: &str) -> Option<V2Route<'_>> {
    if path == "/v2" || path == "/v2/" {
        return Some(V2Route::Root);
    }
    let after = path.strip_prefix("/v2/")?;
    if let Some(repo) = after.strip_suffix("/tags/list") {
        if repo.is_empty() {
            return None;
        }
        return Some(V2Route::Tags { repo });
    }
    // /manifests/<ref> and /blobs/<digest>: split at the LAST occurrence of
    // the verb so a repo containing the word `manifests` or `blobs` as a
    // namespace component still parses unambiguously.
    if let Some(pos) = after.rfind("/manifests/") {
        let repo = &after[..pos];
        let reference = &after[pos + "/manifests/".len()..];
        if repo.is_empty() || reference.is_empty() || reference.contains('/') {
            return None;
        }
        return Some(V2Route::Manifest { repo, reference });
    }
    if let Some(pos) = after.rfind("/blobs/") {
        let repo = &after[..pos];
        let digest = &after[pos + "/blobs/".len()..];
        if repo.is_empty() || digest.is_empty() || digest.contains('/') {
            return None;
        }
        return Some(V2Route::Blob { repo, digest });
    }
    None
}

// ---------- handlers ----------

fn v2_root(head_only: bool) -> Response<Full<Bytes>> {
    let body = if head_only { Vec::new() } else { b"{}".to_vec() };
    Response::builder()
        .status(StatusCode::OK)
        .header("Content-Type", "application/json")
        .header("Content-Length", body.len().to_string())
        .body(Full::new(Bytes::from(body)))
        .unwrap()
}

async fn handle_manifest(
    repo: &str,
    reference: &str,
    state: &ShimState,
    head_only: bool,
) -> Response<Full<Bytes>> {
    let names = FleetNameMap::load().unwrap_or_default();
    let (aid_hex, _name) = match resolve_repo_to_artifact_id(repo, &names) {
        Ok(v) => v,
        Err(e) => return name_unknown(repo, &format!("{e:#}")),
    };
    let (os, arch) = current_platform();

    let resolution = match state.resolver.resolve(&aid_hex, reference, &os, &arch) {
        Ok(r) => r,
        Err(e) => return classify_resolve_error(repo, reference, &os, &arch, &aid_hex, e, state),
    };
    let archive_path = match state.resolver.materialize(&resolution) {
        Ok(p) => p,
        Err(e) => return server_error(&format!("materialize: {e:#}")),
    };

    let archive = OciArchive::open(&archive_path);
    match archive.format() {
        Ok(ArchiveFormat::Oci) => {}
        Ok(ArchiveFormat::DockerSave) => {
            return server_error(
                "this artifact's blob is a legacy `docker save` archive — \
                 curator only serves OCI image-layout archives via the registry shim",
            );
        }
        Ok(ArchiveFormat::Unknown) => {
            return server_error(
                "this artifact's blob is not an OCI image-layout archive — \
                 the registry shim cannot serve it",
            );
        }
        Err(e) => return server_error(&format!("reading archive {}: {e:#}", archive_path.display())),
    }

    let index = match archive.read_index() {
        Ok(i) => i,
        Err(e) => return server_error(&format!("parsing index.json: {e:#}")),
    };

    // Prefer a manifest whose ref-name annotation matches `reference`; fall back
    // to a digest match; finally fall back to the first manifest in the index
    // (the common single-manifest archive case).
    let descriptor = index
        .find_by_ref_name(reference)
        .or_else(|| {
            if reference.starts_with("sha256:") || reference.len() == 64 {
                index.find_by_digest(reference)
            } else {
                None
            }
        })
        .or_else(|| index.manifests.first());
    let Some(descriptor) = descriptor else {
        return manifest_unknown(reference, "the OCI archive has no manifest descriptors");
    };

    let manifest_bytes = match archive.read_blob(&descriptor.digest) {
        Ok(b) => b,
        Err(e) => {
            return server_error(&format!(
                "manifest blob {} missing from archive: {e:#}",
                descriptor.digest
            ))
        }
    };

    let body = if head_only { Vec::new() } else { manifest_bytes.clone() };
    Response::builder()
        .status(StatusCode::OK)
        .header("Content-Type", &descriptor.media_type)
        .header("Docker-Content-Digest", &descriptor.digest)
        .header("Content-Length", manifest_bytes.len().to_string())
        .body(Full::new(Bytes::from(body)))
        .unwrap()
}

async fn handle_blob(
    repo: &str,
    digest: &str,
    state: &ShimState,
    head_only: bool,
) -> Response<Full<Bytes>> {
    let names = FleetNameMap::load().unwrap_or_default();
    let (aid_hex, _name) = match resolve_repo_to_artifact_id(repo, &names) {
        Ok(v) => v,
        Err(e) => return name_unknown(repo, &format!("{e:#}")),
    };
    let entries = catalog_cache::load_catalog_in(state.resolver.artifacts_dir());
    let Some(entry) = catalog_cache::lookup_verified(&entries, &aid_hex) else {
        return name_unknown(repo, "no verifiable cached catalog entry");
    };
    let (os, arch) = current_platform();

    // Scan every (version, host-platform) pair until one archive yields the
    // requested blob. Typical artifacts have 1–3 versions; per-blob cost is
    // bounded and the resolver short-circuits on already-materialized paths.
    for version in entry.body.versions.keys() {
        let Some(_p) = catalog_cache::resolve_platform(entry, version, &os, &arch) else {
            continue;
        };
        let resolution = match state.resolver.resolve(&aid_hex, version, &os, &arch) {
            Ok(r) => r,
            Err(_) => continue,
        };
        let path = match state.resolver.materialize(&resolution) {
            Ok(p) => p,
            Err(_) => continue,
        };
        let archive = OciArchive::open(&path);
        match archive.read_blob(digest) {
            Ok(bytes) => {
                let normalized = if digest.starts_with("sha256:") {
                    digest.to_string()
                } else {
                    format!("sha256:{digest}")
                };
                let body = if head_only { Vec::new() } else { bytes.clone() };
                return Response::builder()
                    .status(StatusCode::OK)
                    .header("Content-Type", "application/octet-stream")
                    .header("Docker-Content-Digest", normalized)
                    .header("Content-Length", bytes.len().to_string())
                    .body(Full::new(Bytes::from(body)))
                    .unwrap();
            }
            Err(_) => continue,
        }
    }

    not_found_error(
        "BLOB_UNKNOWN",
        &format!("blob {digest} not found in any {os}/{arch} build of {repo}"),
    )
}

async fn handle_tags(repo: &str, state: &ShimState, head_only: bool) -> Response<Full<Bytes>> {
    let names = FleetNameMap::load().unwrap_or_default();
    let (aid_hex, _name) = match resolve_repo_to_artifact_id(repo, &names) {
        Ok(v) => v,
        Err(e) => return name_unknown(repo, &format!("{e:#}")),
    };
    let entries = catalog_cache::load_catalog_in(state.resolver.artifacts_dir());
    let Some(entry) = catalog_cache::lookup_verified(&entries, &aid_hex) else {
        return name_unknown(repo, "no verifiable cached catalog entry");
    };
    let _ = state; // tags don't need the resolver — purely catalog-driven

    #[derive(Serialize)]
    struct TagsBody<'a> {
        name: &'a str,
        tags: Vec<&'a str>,
    }
    let body = TagsBody {
        name: repo,
        tags: entry.body.tags.keys().map(String::as_str).collect(),
    };
    let bytes = serde_json::to_vec(&body)
        .context("serializing tags/list")
        .unwrap_or_else(|_| b"{}".to_vec());
    let out = if head_only { Vec::new() } else { bytes.clone() };
    Response::builder()
        .status(StatusCode::OK)
        .header("Content-Type", "application/json")
        .header("Content-Length", bytes.len().to_string())
        .body(Full::new(Bytes::from(out)))
        .unwrap()
}

// ---------- error helpers ----------

/// Docker Registry v2 error response body. Per the v2 spec a non-2xx response
/// carries `{"errors":[{"code":"<CODE>","message":"<text>"...}]}`.
#[derive(Serialize)]
struct RegistryError<'a> {
    errors: Vec<RegistryErrorEntry<'a>>,
}
#[derive(Serialize)]
struct RegistryErrorEntry<'a> {
    code: &'a str,
    message: &'a str,
    #[serde(skip_serializing_if = "Option::is_none")]
    detail: Option<serde_json::Value>,
}

fn registry_error(status: StatusCode, code: &str, message: &str) -> Response<Full<Bytes>> {
    let body = RegistryError {
        errors: vec![RegistryErrorEntry { code, message, detail: None }],
    };
    let bytes = serde_json::to_vec(&body).unwrap_or_else(|_| b"{}".to_vec());
    Response::builder()
        .status(status)
        .header("Content-Type", "application/json")
        .header("Content-Length", bytes.len().to_string())
        .body(Full::new(Bytes::from(bytes)))
        .unwrap()
}

fn method_not_allowed() -> Response<Full<Bytes>> {
    let mut r = registry_error(
        StatusCode::METHOD_NOT_ALLOWED,
        "UNSUPPORTED",
        "method not allowed — curator's registry shim is read-only",
    );
    r.headers_mut().insert(
        hyper::header::ALLOW,
        hyper::header::HeaderValue::from_static("GET, HEAD"),
    );
    r
}

fn not_found_error(code: &str, message: &str) -> Response<Full<Bytes>> {
    registry_error(StatusCode::NOT_FOUND, code, message)
}

fn server_error(message: &str) -> Response<Full<Bytes>> {
    tracing::warn!(error = %message, "curator registry shim: 500");
    registry_error(StatusCode::INTERNAL_SERVER_ERROR, "UNKNOWN", message)
}

fn name_unknown(repo: &str, detail: &str) -> Response<Full<Bytes>> {
    let msg = format!("repository `{repo}` is not in the cached curator catalog: {detail}");
    not_found_error("NAME_UNKNOWN", &msg)
}

fn manifest_unknown(reference: &str, detail: &str) -> Response<Full<Bytes>> {
    let msg = format!("manifest `{reference}` not found: {detail}");
    not_found_error("MANIFEST_UNKNOWN", &msg)
}

/// Classify the resolver's `anyhow::Error` into the right Docker Registry v2
/// error. The resolver's message strings are stable enough to pattern-match;
/// when in doubt we fall back to 500 to surface a real bug rather than hide it
/// behind a 404.
fn classify_resolve_error(
    repo: &str,
    reference: &str,
    os: &str,
    arch: &str,
    aid_hex: &str,
    err: anyhow::Error,
    state: &ShimState,
) -> Response<Full<Bytes>> {
    let msg = format!("{err:#}");
    if msg.contains("no such tag or version") || msg.contains("version") && msg.contains("is not in the entry") {
        return manifest_unknown(reference, &msg);
    }
    // Resolver returns two slightly different messages depending on which
    // path failed: the server path says "platform <os>/<arch> is not in
    // version …", the cache path says "has no build for <os>/<arch> at
    // version …". Either way the meaning is "wrong platform for this
    // version", and the right Docker response is 404 + available list.
    if (msg.contains("platform") && msg.contains("not in version"))
        || msg.contains("has no build for")
    {
        // Look up the catalog entry to enumerate the platforms that DO exist
        // for this version — invaluable for a wrong-arch `docker pull`.
        let entries = catalog_cache::load_catalog_in(state.resolver.artifacts_dir());
        if let Some(entry) = catalog_cache::lookup_verified(&entries, aid_hex) {
            let version = catalog_cache::resolve_version(entry, reference);
            let available: Vec<String> = match version.as_deref() {
                Some(v) => entry
                    .body
                    .versions
                    .get(v)
                    .map(|av| av.platforms.iter().map(platform_str).collect())
                    .unwrap_or_default(),
                None => entry
                    .body
                    .versions
                    .values()
                    .flat_map(|av| av.platforms.iter().map(platform_str))
                    .collect(),
            };
            return manifest_unknown(
                reference,
                &format!(
                    "no `{os}/{arch}` build for `{repo}:{reference}` — available: [{}]",
                    available.join(", ")
                ),
            );
        }
        return manifest_unknown(reference, &msg);
    }
    if msg.contains("no verifiable cached catalog entry") {
        return name_unknown(repo, &msg);
    }
    server_error(&format!("resolve: {msg}"))
}

fn platform_str(p: &Platform) -> String {
    format!("{}/{}", p.os, p.arch)
}

fn with_common_headers(mut r: Response<Full<Bytes>>) -> Response<Full<Bytes>> {
    r.headers_mut().insert(
        API_VERSION_HEADER,
        hyper::header::HeaderValue::from_static(API_VERSION_VALUE),
    );
    r
}

// Documented-but-unused helper marker — the resolver borrows `Resolution`
// internally; we exported the type only to keep the registry → resolver glue
// readable. Touching it here silences `unused_imports` without `#[allow]`.
#[allow(dead_code)]
fn _resolution_phantom(_r: &Resolution) {}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::swarm::curator::catalog::{ArtifactVersion, CatalogEntry, CatalogEntryBody, Platform};
    use ed25519_dalek::SigningKey;
    use rand::rngs::OsRng;
    use std::collections::BTreeMap;
    use tempfile::TempDir;

    // ---- parse_v2_path ----

    #[test]
    fn parse_root() {
        assert_eq!(parse_v2_path("/v2"), Some(V2Route::Root));
        assert_eq!(parse_v2_path("/v2/"), Some(V2Route::Root));
    }

    #[test]
    fn parse_manifest_simple() {
        assert_eq!(
            parse_v2_path("/v2/foo/manifests/latest"),
            Some(V2Route::Manifest { repo: "foo", reference: "latest" })
        );
    }

    #[test]
    fn parse_manifest_nested_repo() {
        assert_eq!(
            parse_v2_path("/v2/alexander4/vigo-agent/manifests/v3"),
            Some(V2Route::Manifest { repo: "alexander4/vigo-agent", reference: "v3" })
        );
    }

    #[test]
    fn parse_manifest_with_digest_ref() {
        assert_eq!(
            parse_v2_path("/v2/foo/manifests/sha256:abc"),
            Some(V2Route::Manifest { repo: "foo", reference: "sha256:abc" })
        );
    }

    #[test]
    fn parse_blob() {
        assert_eq!(
            parse_v2_path("/v2/alexander4/foo/blobs/sha256:deadbeef"),
            Some(V2Route::Blob { repo: "alexander4/foo", digest: "sha256:deadbeef" })
        );
    }

    #[test]
    fn parse_tags() {
        assert_eq!(
            parse_v2_path("/v2/alexander4/foo/tags/list"),
            Some(V2Route::Tags { repo: "alexander4/foo" })
        );
    }

    #[test]
    fn parse_rejects_missing_reference() {
        assert_eq!(parse_v2_path("/v2/foo/manifests/"), None);
        assert_eq!(parse_v2_path("/v2/foo/blobs/"), None);
        assert_eq!(parse_v2_path("/v2//tags/list"), None);
    }

    #[test]
    fn parse_rejects_reference_with_slash() {
        assert_eq!(parse_v2_path("/v2/foo/manifests/oh/no"), None);
        assert_eq!(parse_v2_path("/v2/foo/blobs/a/b"), None);
    }

    #[test]
    fn parse_rejects_unknown_verb() {
        assert_eq!(parse_v2_path("/v2/foo/whatever/x"), None);
        assert_eq!(parse_v2_path("/v2/foo"), None);
        assert_eq!(parse_v2_path("/v3/foo/manifests/latest"), None);
    }

    #[test]
    fn parse_anchors_on_last_verb_so_namespace_called_manifests_still_works() {
        // Wildly unusual but defensible: `/v2/manifests-team/foo/manifests/latest`
        // — the rfind-from-end matches the trailing verb, not the namespace token.
        assert_eq!(
            parse_v2_path("/v2/manifests-team/foo/manifests/latest"),
            Some(V2Route::Manifest { repo: "manifests-team/foo", reference: "latest" })
        );
    }

    // ---- integration with resolver ----

    /// Build an OCI archive tarball with one manifest descriptor + the named
    /// blobs inside. Returns (tarball_bytes, manifest_digest, manifest_body).
    fn build_oci_archive_bytes(ref_name: &str) -> (Vec<u8>, String, Vec<u8>) {
        use sha2::{Digest, Sha256};
        let manifest_body = format!(
            r#"{{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:{cfg}","size":2}},"layers":[]}}"#,
            cfg = "0".repeat(64)
        )
        .into_bytes();
        let manifest_digest_hex = hex::encode(Sha256::digest(&manifest_body));
        let manifest_digest = format!("sha256:{manifest_digest_hex}");

        let index = serde_json::json!({
            "schemaVersion": 2,
            "mediaType": "application/vnd.oci.image.index.v1+json",
            "manifests": [{
                "mediaType": "application/vnd.oci.image.manifest.v1+json",
                "digest": manifest_digest,
                "size": manifest_body.len(),
                "annotations": {"org.opencontainers.image.ref.name": ref_name}
            }]
        });
        let index_bytes = serde_json::to_vec(&index).unwrap();

        let cfg_body = b"{}".to_vec();

        let mut tar_buf: Vec<u8> = Vec::new();
        {
            let mut ar = tar::Builder::new(&mut tar_buf);
            for (name, body) in [
                ("oci-layout", br#"{"imageLayoutVersion":"1.0.0"}"#.to_vec()),
                ("index.json", index_bytes),
                (
                    Box::leak(format!("blobs/sha256/{manifest_digest_hex}").into_boxed_str()),
                    manifest_body.clone(),
                ),
                (
                    Box::leak(format!("blobs/sha256/{}", "0".repeat(64)).into_boxed_str()),
                    cfg_body,
                ),
            ] {
                let mut h = tar::Header::new_gnu();
                h.set_size(body.len() as u64);
                h.set_mode(0o644);
                h.set_cksum();
                ar.append_data(&mut h, name, std::io::Cursor::new(body)).unwrap();
            }
            ar.finish().unwrap();
        }
        (tar_buf, manifest_digest, manifest_body)
    }

    fn build_entry(k: &SigningKey, name: &str, blob: &[u8], os: &str, arch: &str) -> (CatalogEntry, String) {
        use sha2::{Digest, Sha256};
        let sha = hex::encode(Sha256::digest(blob));
        let mut versions = BTreeMap::new();
        versions.insert(
            "1.0.0".to_string(),
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
        tags.insert("latest".to_string(), "1.0.0".to_string());
        let e = CatalogEntry::issue(
            k,
            CatalogEntryBody {
                schema_version: 1,
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

    struct Bed {
        _arts: TempDir,
        _blobs: TempDir,
        state: Arc<ShimState>,
        /// Hex of the founder puddle pubkey — used as the registry-URL
        /// namespace (the 64-hex path component the namespace resolver
        /// treats as a literal puddle pubkey).
        pubkey_hex: String,
        manifest_digest: String,
        manifest_body: Vec<u8>,
    }

    fn bed_with_artifact(name: &str, os: &str, arch: &str) -> Bed {
        let arts = TempDir::new().unwrap();
        let blobs = TempDir::new().unwrap();
        let k = SigningKey::generate(&mut OsRng);
        let pubkey_hex = hex::encode(k.verifying_key().to_bytes());
        let (blob, manifest_digest, manifest_body) = build_oci_archive_bytes("latest");
        let (entry, sha) = build_entry(&k, name, &blob, os, arch);

        catalog_cache::write_catalog_in(
            arts.path(),
            &serde_json::to_vec(&vec![entry.clone()]).unwrap(),
        )
        .unwrap();
        let cache = crate::swarm::cache::BlobCache::new(blobs.path().to_path_buf()).unwrap();
        std::fs::write(cache.blob_path(&sha), &blob).unwrap();

        let resolver = Resolver::with_dirs(
            "",
            None,
            blobs.path().to_path_buf(),
            arts.path().to_path_buf(),
        );
        let state = Arc::new(ShimState::new(resolver));
        Bed {
            _arts: arts,
            _blobs: blobs,
            state,
            pubkey_hex,
            manifest_digest,
            manifest_body,
        }
    }

    fn ok_status(r: &Response<Full<Bytes>>) -> bool {
        r.status().is_success()
    }

    async fn body_of(r: Response<Full<Bytes>>) -> Vec<u8> {
        use http_body_util::BodyExt;
        r.into_body().collect().await.unwrap().to_bytes().to_vec()
    }

    #[tokio::test]
    async fn v2_root_works() {
        // Note: doesn't need an artifact, but plumbing parallels.
        let resolver = Resolver::with_dirs("", None, TempDir::new().unwrap().keep(), TempDir::new().unwrap().keep());
        let state = Arc::new(ShimState::new(resolver));
        let resp = route(&Method::GET, "/v2/", &state).await;
        assert!(ok_status(&resp));
    }

    #[tokio::test]
    async fn manifest_get_returns_inner_manifest_bytes() {
        let (os, arch) = current_platform();
        let bed = bed_with_artifact("vigo-agent", &os, &arch);

        // Look up the artifact via its 64-hex aid as namespace (avoids
        // depending on the fleet name map on disk).
        let path = format!("/v2/{pk}/vigo-agent/manifests/latest", pk = bed.pubkey_hex);
        let resp = route(&Method::GET, &path, &bed.state).await;
        assert!(ok_status(&resp), "got status {}", resp.status());
        let dcd = resp.headers().get("Docker-Content-Digest").cloned();
        let body = body_of(resp).await;
        assert_eq!(body, bed.manifest_body);
        assert_eq!(dcd.map(|v| v.to_str().unwrap().to_string()), Some(bed.manifest_digest.clone()));
    }

    #[tokio::test]
    async fn blob_get_returns_blob_bytes() {
        let (os, arch) = current_platform();
        let bed = bed_with_artifact("vigo-agent", &os, &arch);

        let path = format!("/v2/{pk}/vigo-agent/blobs/{d}", pk = bed.pubkey_hex, d = bed.manifest_digest);
        let resp = route(&Method::GET, &path, &bed.state).await;
        assert!(ok_status(&resp), "got status {}", resp.status());
        let body = body_of(resp).await;
        assert_eq!(body, bed.manifest_body);
    }

    #[tokio::test]
    async fn tags_list_returns_known_tags() {
        let (os, arch) = current_platform();
        let bed = bed_with_artifact("vigo-agent", &os, &arch);

        let path = format!("/v2/{pk}/vigo-agent/tags/list", pk = bed.pubkey_hex);
        let resp = route(&Method::GET, &path, &bed.state).await;
        assert!(ok_status(&resp));
        let body = body_of(resp).await;
        let j: serde_json::Value = serde_json::from_slice(&body).unwrap();
        let tags = j["tags"].as_array().expect("tags is an array");
        assert!(tags.iter().any(|t| t == "latest"), "got: {tags:?}");
    }

    #[tokio::test]
    async fn manifest_for_wrong_arch_returns_404_with_available_platforms() {
        // Publish only linux/amd64; resolve on whatever the test host is —
        // if host arch is linux/amd64 too, force a mismatch by publishing s390x.
        let bed = bed_with_artifact("vigo-agent", "linux", "s390x");

        let path = format!("/v2/{pk}/vigo-agent/manifests/latest", pk = bed.pubkey_hex);
        let resp = route(&Method::GET, &path, &bed.state).await;
        assert_eq!(resp.status(), StatusCode::NOT_FOUND);
        let body = body_of(resp).await;
        let s = String::from_utf8_lossy(&body);
        assert!(s.contains("linux/s390x") || s.contains("available"), "got: {s}");
    }

    #[tokio::test]
    async fn unknown_repo_returns_404_name_unknown() {
        let resolver = Resolver::with_dirs("", None, TempDir::new().unwrap().keep(), TempDir::new().unwrap().keep());
        let state = Arc::new(ShimState::new(resolver));
        let resp = route(&Method::GET, "/v2/totally/missing/manifests/latest", &state).await;
        assert_eq!(resp.status(), StatusCode::NOT_FOUND);
        let body = body_of(resp).await;
        let s = String::from_utf8_lossy(&body);
        assert!(s.contains("NAME_UNKNOWN") || s.contains("no puddle has claimed"), "got: {s}");
    }

    #[tokio::test]
    async fn post_method_is_405() {
        let resolver = Resolver::with_dirs("", None, TempDir::new().unwrap().keep(), TempDir::new().unwrap().keep());
        let state = Arc::new(ShimState::new(resolver));
        let resp = route(&Method::POST, "/v2/", &state).await;
        assert_eq!(resp.status(), StatusCode::METHOD_NOT_ALLOWED);
        assert_eq!(
            resp.headers().get(hyper::header::ALLOW).map(|v| v.to_str().unwrap()),
            Some("GET, HEAD")
        );
    }

    #[tokio::test]
    async fn head_request_returns_empty_body_with_headers() {
        let (os, arch) = current_platform();
        let bed = bed_with_artifact("vigo-agent", &os, &arch);

        let path = format!("/v2/{pk}/vigo-agent/manifests/latest", pk = bed.pubkey_hex);
        let resp = route(&Method::HEAD, &path, &bed.state).await;
        assert!(ok_status(&resp));
        let dcd = resp.headers().get("Docker-Content-Digest").cloned();
        let body = body_of(resp).await;
        assert!(body.is_empty(), "HEAD must have empty body");
        assert_eq!(dcd.map(|v| v.to_str().unwrap().to_string()), Some(bed.manifest_digest.clone()));
    }
}
