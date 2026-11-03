// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! The curator package-repo facade (ADR-024 slice 10).
//!
//! Serves a private apt / dnf / apk package repository out of a curator
//! artifact: `GET /repo/<name>/<path>` resolves `<name>` to a verified catalog
//! entry of kind `apt-repo` / `dnf-repo` / `apk-repo`, materializes its blob —
//! a plain (uncompressed) `.tar` of a pre-built, GPG-signed repo tree — and
//! streams `<path>` from inside it. Read-only and localhost-only, both
//! inherited from the shared listener ([`super::listener`]); dispatched from
//! [`super::routes::route`].
//!
//! The consume side is the existing `repository` resource pointed at
//! `http://127.0.0.1:<swarm.curator.local_port>/repo/<name>` — no new
//! consume-side code; native `apt`/`dnf`/`apk` does dependency resolution.
//!
//! ### Addressing
//!
//! `<name>` is a curator artifact ref: `<puddle-name>/<artifact>`,
//! `<hex-artifact-id>/<artifact>`, or a bare `<artifact>` (implicit operator
//! namespace). A curator ref is at most two path segments, so the facade
//! splits `/repo/<rest>` by trying the two-segment ref first, then the bare
//! one, resolving each against the verified cached catalog — unambiguous, no
//! heuristic. apt/dnf/apk URLs carry no tag component, so the facade resolves
//! `latest` (falling back to the sole version when an artifact has exactly
//! one). A repo tarball is architecture-agnostic — one tree holds every
//! `binary-*` — so the facade ignores os/arch and serves the resolved
//! version's single platform row.

use http_body_util::Full;
use hyper::body::Bytes;
use hyper::{Response, StatusCode};

use super::oci_archive::read_tar_file;
use super::routes::ShimState;
use crate::swarm::curator::catalog::{CatalogEntry, KIND_APK_REPO, KIND_APT_REPO, KIND_DNF_REPO};
use crate::swarm::curator::catalog_cache;
use crate::swarm::curator::executor_resolver::resolve_repo_to_artifact_id;
use crate::swarm::curator::resolver::Resolution;
use crate::swarm::puddle::names_cache::FleetNameMap;

/// Handle a `/repo/<rest>` request. `rest` is the path with `/repo/` already
/// stripped — `<name-segments>/<inner-repo-path>`. Always returns a built
/// response (errors become plain-text 4xx/5xx — apt/dnf/apk clients read the
/// status, not a structured body).
pub async fn handle(rest: &str, state: &ShimState, head_only: bool) -> Response<Full<Bytes>> {
    let names = FleetNameMap::load().unwrap_or_default();
    let entries = catalog_cache::load_catalog_in(state.resolver.artifacts_dir());

    let Some((entry, aid_hex, inner_path)) = resolve_ref_and_path(rest, &names, &entries) else {
        return repo_error(
            StatusCode::NOT_FOUND,
            &format!(
                "no curator package-repo artifact resolves `/repo/{rest}` — \
                 check the artifact name and that it is published to the fleet"
            ),
        );
    };

    // (1) the artifact must be a package-repo kind.
    let kind = entry.body.kind.as_str();
    if !is_repo_kind(kind) {
        return repo_error(
            StatusCode::NOT_FOUND,
            &format!(
                "artifact is kind `{kind}`, not a package-repo kind \
                 (apt-repo / dnf-repo / apk-repo) — the package-repo facade cannot serve it"
            ),
        );
    }

    // (2) if the request path is recognizably a *different* repo kind's
    // metadata layout, the consumer is pointed at the wrong artifact. Catch it
    // with a clear error instead of a bare 404 on a path that simply isn't in
    // the tarball. Only the distinctive metadata entry points are classified;
    // a package-file or key-file path matches nothing and passes through.
    if let Some(shape) = metadata_kind(inner_path) {
        if shape != kind {
            return repo_error(
                StatusCode::NOT_FOUND,
                &format!(
                    "`{inner_path}` is `{shape}` repo-metadata layout, but this artifact \
                     is kind `{kind}` — point this consumer at a `{kind}` repository"
                ),
            );
        }
    }

    let version = match resolve_repo_version(&entry) {
        Ok(v) => v,
        Err(e) => return repo_error(StatusCode::NOT_FOUND, &e),
    };
    let platforms = &entry
        .body
        .versions
        .get(&version)
        .expect("resolve_repo_version only returns a key present in versions")
        .platforms;
    let Some(platform) = platforms.first() else {
        return repo_error(
            StatusCode::NOT_FOUND,
            &format!("version `{version}` of this repo artifact has no published blob"),
        );
    };

    let resolution = Resolution {
        artifact_id_hex: aid_hex,
        name: entry.body.name.clone(),
        version: version.clone(),
        tag: entry.body.tags.get("latest").map(|_| "latest".to_string()),
        os: platform.os.clone(),
        arch: platform.arch.clone(),
        blob_sha: platform.blob_sha.clone(),
        size: platform.size,
        entry: entry.clone(),
    };

    let tarball = match state.resolver.materialize(&resolution) {
        Ok(p) => p,
        Err(e) => {
            return repo_error(
                StatusCode::INTERNAL_SERVER_ERROR,
                &format!("materializing the repo blob: {e:#}"),
            )
        }
    };

    match read_tar_file(&tarball, inner_path) {
        Ok(Some(bytes)) => serve_bytes(bytes, head_only),
        Ok(None) => repo_error(
            StatusCode::NOT_FOUND,
            &format!("`{inner_path}` is not present in repo `{}`", entry.body.name),
        ),
        Err(e) => repo_error(
            StatusCode::INTERNAL_SERVER_ERROR,
            &format!("reading the repo tarball: {e:#}"),
        ),
    }
}

fn is_repo_kind(kind: &str) -> bool {
    kind == KIND_APT_REPO || kind == KIND_DNF_REPO || kind == KIND_APK_REPO
}

/// Classify a request path by the repo kind whose *metadata layout* it matches
/// — `dists/…` is apt's, `repodata/…` is dnf's, `…/APKINDEX.tar.gz` is apk's.
/// Returns `None` for anything else (package files, the signing key, …), which
/// the caller treats as "don't second-guess it".
fn metadata_kind(path: &str) -> Option<&'static str> {
    if path.starts_with("dists/") {
        Some(KIND_APT_REPO)
    } else if path.starts_with("repodata/") {
        Some(KIND_DNF_REPO)
    } else if path.ends_with("APKINDEX.tar.gz") {
        Some(KIND_APK_REPO)
    } else {
        None
    }
}

/// Resolve the version a tag-less repo URL maps to: the `latest` tag, or — when
/// there's no usable `latest` — the sole version. Multiple versions with no
/// `latest` is an operator error (apt/dnf/apk can't pick).
fn resolve_repo_version(entry: &CatalogEntry) -> Result<String, String> {
    if let Some(v) = catalog_cache::resolve_version(entry, "latest") {
        if entry.body.versions.contains_key(&v) {
            return Ok(v);
        }
        // `latest` points at a missing version (dangling tag) — fall through.
    }
    let mut keys = entry.body.versions.keys();
    match (keys.next(), keys.next()) {
        (Some(only), None) => Ok(only.clone()),
        (None, _) => Err("this repo artifact has no published versions".to_string()),
        (Some(_), Some(_)) => Err(
            "this repo artifact has multiple versions but no usable `latest` tag — \
             re-publish with `--tag latest`"
                .to_string(),
        ),
    }
}

/// Split `/repo/<rest>` into a resolved catalog entry + the inner repo path.
/// A curator ref is ≤2 segments; the two-segment ref is tried first so an
/// explicit `<namespace>/<artifact>` wins over a bare `<artifact>`.
fn resolve_ref_and_path<'a>(
    rest: &'a str,
    names: &FleetNameMap,
    entries: &[CatalogEntry],
) -> Option<(CatalogEntry, String, &'a str)> {
    let mut it = rest.splitn(3, '/');
    if let (Some(s0), Some(s1), Some(path)) = (it.next(), it.next(), it.next()) {
        if !path.is_empty() {
            if let Some((entry, aid)) = try_resolve(&format!("{s0}/{s1}"), names, entries) {
                return Some((entry, aid, path));
            }
        }
    }
    let mut it = rest.splitn(2, '/');
    if let (Some(s0), Some(path)) = (it.next(), it.next()) {
        if !path.is_empty() {
            if let Some((entry, aid)) = try_resolve(s0, names, entries) {
                return Some((entry, aid, path));
            }
        }
    }
    None
}

fn try_resolve(
    name: &str,
    names: &FleetNameMap,
    entries: &[CatalogEntry],
) -> Option<(CatalogEntry, String)> {
    let (aid, _) = resolve_repo_to_artifact_id(name, names).ok()?;
    let entry = catalog_cache::lookup_verified(entries, &aid)?.clone();
    Some((entry, aid))
}

fn serve_bytes(bytes: Vec<u8>, head_only: bool) -> Response<Full<Bytes>> {
    let len = bytes.len();
    let body = if head_only { Vec::new() } else { bytes };
    Response::builder()
        .status(StatusCode::OK)
        .header("Content-Type", "application/octet-stream")
        .header("Content-Length", len.to_string())
        .body(Full::new(Bytes::from(body)))
        .unwrap()
}

fn repo_error(status: StatusCode, msg: &str) -> Response<Full<Bytes>> {
    if status.is_server_error() {
        tracing::warn!(error = %msg, "curator package-repo facade: 5xx");
    }
    let body = format!("{msg}\n");
    Response::builder()
        .status(status)
        .header("Content-Type", "text/plain; charset=utf-8")
        .header("Content-Length", body.len().to_string())
        .body(Full::new(Bytes::from(body)))
        .unwrap()
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::swarm::curator::catalog::{
        ArtifactVersion, CatalogEntryBody, Platform, KIND_GENERIC, SCHEMA_VERSION,
    };
    use crate::swarm::curator::resolver::Resolver;
    use ed25519_dalek::SigningKey;
    use rand::rngs::OsRng;
    use std::collections::BTreeMap;
    use tempfile::TempDir;

    /// Build a plain (uncompressed) tar from `(path, bytes)` entries.
    fn plain_tar(entries: &[(&str, &[u8])]) -> Vec<u8> {
        let mut buf = Vec::new();
        {
            let mut ar = tar::Builder::new(&mut buf);
            for (name, body) in entries {
                let mut h = tar::Header::new_gnu();
                h.set_size(body.len() as u64);
                h.set_mode(0o644);
                h.set_cksum();
                ar.append_data(&mut h, name, std::io::Cursor::new(*body)).unwrap();
            }
            ar.finish().unwrap();
        }
        buf
    }

    struct Bed {
        _arts: TempDir,
        _blobs: TempDir,
        state: ShimState,
        /// Hex founder puddle pubkey — the registry-URL namespace segment.
        pubkey_hex: String,
    }

    /// Publish `blob` as a single-version curator artifact of `kind` and stand
    /// up a `ShimState` whose resolver materializes it from the local cache.
    fn bed_with_repo(name: &str, kind: &str, blob: &[u8]) -> Bed {
        use sha2::{Digest, Sha256};
        let arts = TempDir::new().unwrap();
        let blobs = TempDir::new().unwrap();
        let k = SigningKey::generate(&mut OsRng);
        let pubkey_hex = hex::encode(k.verifying_key().to_bytes());
        let sha = hex::encode(Sha256::digest(blob));

        let mut versions = BTreeMap::new();
        versions.insert(
            "1".to_string(),
            ArtifactVersion {
                platforms: vec![Platform {
                    // a repo tarball is arch-agnostic — `linux/all` is the
                    // documented publish placeholder; the facade ignores it.
                    os: "linux".into(),
                    arch: "all".into(),
                    blob_sha: sha.clone(),
                    size: blob.len() as u64,
                }],
            },
        );
        let mut tags = BTreeMap::new();
        tags.insert("latest".to_string(), "1".to_string());
        let entry = CatalogEntry::issue(
            &k,
            CatalogEntryBody {
                schema_version: SCHEMA_VERSION,
                name: name.into(),
                kind: kind.into(),
                founder_puddle_pubkey: pubkey_hex.clone(),
                signing_puddle_pubkey: pubkey_hex.clone(),
                versions,
                tags,
                recipients: vec!["*".into()],
                target: "*".into(),
                issued_at: 1,
                delegations: vec![],
            },
        )
        .unwrap();

        catalog_cache::write_catalog_in(arts.path(), &serde_json::to_vec(&vec![entry]).unwrap())
            .unwrap();
        let cache = crate::swarm::cache::BlobCache::new(blobs.path().to_path_buf()).unwrap();
        std::fs::write(cache.blob_path(&sha), blob).unwrap();

        let resolver = Resolver::with_dirs(
            "",
            None,
            blobs.path().to_path_buf(),
            arts.path().to_path_buf(),
        );
        Bed {
            _arts: arts,
            _blobs: blobs,
            state: ShimState::new(resolver),
            pubkey_hex,
        }
    }

    async fn body_of(r: Response<Full<Bytes>>) -> Vec<u8> {
        use http_body_util::BodyExt;
        r.into_body().collect().await.unwrap().to_bytes().to_vec()
    }

    #[tokio::test]
    async fn serves_a_metadata_file_from_an_apt_repo() {
        let release: &[u8] = b"Origin: acme\nSuite: stable\n";
        let blob = plain_tar(&[
            ("dists/stable/InRelease", release),
            ("pool/main/a/acme/acme_1_all.deb", b"DEB-BYTES"),
        ]);
        let bed = bed_with_repo("acme", KIND_APT_REPO, &blob);
        let resp = handle(
            &format!("{}/acme/dists/stable/InRelease", bed.pubkey_hex),
            &bed.state,
            false,
        )
        .await;
        assert_eq!(resp.status(), StatusCode::OK);
        assert_eq!(body_of(resp).await, release);
    }

    #[tokio::test]
    async fn serves_a_package_file_from_an_apt_repo() {
        let blob = plain_tar(&[
            ("dists/stable/InRelease", b"R"),
            ("pool/main/a/acme/acme_1_all.deb", b"DEB-BYTES"),
        ]);
        let bed = bed_with_repo("acme", KIND_APT_REPO, &blob);
        let resp = handle(
            &format!("{}/acme/pool/main/a/acme/acme_1_all.deb", bed.pubkey_hex),
            &bed.state,
            false,
        )
        .await;
        assert_eq!(resp.status(), StatusCode::OK);
        assert_eq!(body_of(resp).await, b"DEB-BYTES");
    }

    #[tokio::test]
    async fn head_request_sets_length_with_empty_body() {
        let release: &[u8] = b"Origin: acme\n";
        let blob = plain_tar(&[("dists/stable/InRelease", release)]);
        let bed = bed_with_repo("acme", KIND_APT_REPO, &blob);
        let resp = handle(
            &format!("{}/acme/dists/stable/InRelease", bed.pubkey_hex),
            &bed.state,
            true,
        )
        .await;
        assert_eq!(resp.status(), StatusCode::OK);
        assert_eq!(
            resp.headers().get("content-length").unwrap(),
            &release.len().to_string()
        );
        assert!(body_of(resp).await.is_empty());
    }

    #[tokio::test]
    async fn missing_path_is_404() {
        let blob = plain_tar(&[("dists/stable/InRelease", b"R")]);
        let bed = bed_with_repo("acme", KIND_APT_REPO, &blob);
        let resp = handle(
            &format!("{}/acme/dists/stable/Packages.gz", bed.pubkey_hex),
            &bed.state,
            false,
        )
        .await;
        assert_eq!(resp.status(), StatusCode::NOT_FOUND);
    }

    #[tokio::test]
    async fn unknown_artifact_is_404() {
        let blob = plain_tar(&[("dists/stable/InRelease", b"R")]);
        let bed = bed_with_repo("acme", KIND_APT_REPO, &blob);
        let resp = handle(
            &format!("{}/nosuch/dists/stable/InRelease", bed.pubkey_hex),
            &bed.state,
            false,
        )
        .await;
        assert_eq!(resp.status(), StatusCode::NOT_FOUND);
    }

    #[tokio::test]
    async fn non_repo_kind_is_rejected() {
        let blob = plain_tar(&[("dists/stable/InRelease", b"R")]);
        let bed = bed_with_repo("img", KIND_GENERIC, &blob);
        let resp = handle(
            &format!("{}/img/dists/stable/InRelease", bed.pubkey_hex),
            &bed.state,
            false,
        )
        .await;
        assert_eq!(resp.status(), StatusCode::NOT_FOUND);
        let body = String::from_utf8(body_of(resp).await).unwrap();
        assert!(
            body.contains("generic") && body.contains("package-repo"),
            "error should name the kind mismatch: {body}"
        );
    }

    #[tokio::test]
    async fn wrong_kind_metadata_path_is_rejected() {
        // a dnf-layout request path against an apt-repo artifact.
        let blob = plain_tar(&[("dists/stable/InRelease", b"R")]);
        let bed = bed_with_repo("acme", KIND_APT_REPO, &blob);
        let resp = handle(
            &format!("{}/acme/repodata/repomd.xml", bed.pubkey_hex),
            &bed.state,
            false,
        )
        .await;
        assert_eq!(resp.status(), StatusCode::NOT_FOUND);
        let body = String::from_utf8(body_of(resp).await).unwrap();
        assert!(
            body.contains("dnf-repo") && body.contains("apt-repo"),
            "error should name the layout mismatch: {body}"
        );
    }
}
