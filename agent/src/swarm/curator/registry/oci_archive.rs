// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! OCI image-layout tarball reader.
//!
//! A curator artifact blob *is* an OCI image-layout (image-layout 1.0.0) tarball
//! — a tar archive whose root contains:
//!
//! - `oci-layout`   — `{"imageLayoutVersion":"1.0.0"}`
//! - `index.json`   — the OCI image index (manifest descriptors + ref names)
//! - `blobs/sha256/<digest>` — every manifest, config, and layer blob, keyed by
//!   its sha256.
//!
//! Spec: <https://github.com/opencontainers/image-spec/blob/main/image-layout.md>.
//!
//! The registry shim parses one of these per `docker pull` against
//! `127.0.0.1:<port>` and serves it through the Docker Registry v2 endpoints
//! ([`super::routes`]). Tarballs aren't randomly seekable, so each request
//! linearly scans the archive once — fine for the ~3-4 reads a typical pull
//! does (manifest + config + layers).
//!
//! ### Why reject `docker save`
//!
//! Plain `docker save` writes a *legacy* layout (top-level `manifest.json`,
//! per-layer `*.tar`, no `oci-layout`/`blobs/sha256/`). Translating it to v2
//! responses on the fly would mean parsing both formats and handling all the
//! one-way conversions (config-on-disk → distribution manifest digest). One
//! format means one code path. Publishers use `docker save --format=oci-archive`
//! (BuildKit / Docker 25+) or `skopeo copy docker-daemon:... oci-archive:...`;
//! both produce valid OCI archives.

use std::fs::File;
use std::io::{Read, Seek};
use std::path::{Path, PathBuf};

use anyhow::{anyhow, bail, Context, Result};
use serde::{Deserialize, Serialize};

/// Marker file at the root of every OCI image-layout archive.
pub const OCI_LAYOUT_FILE: &str = "oci-layout";
/// The image index at the root of every OCI image-layout archive.
pub const INDEX_FILE: &str = "index.json";
/// Top-level file present in legacy `docker save` archives; absent from OCI.
pub const DOCKER_SAVE_MANIFEST: &str = "manifest.json";

/// Upper bound on the up-front allocation for a single tar entry. The entries
/// the shim reads (index.json, manifests, configs) are kilobytes; this only
/// caps the pre-allocation against a crafted header declaring a huge size on a
/// tiny entry — the read still grows to the real content if it's larger.
const MAX_PREALLOC_BYTES: u64 = 8 * 1024 * 1024;

/// Distinguishes the two tarball flavors `docker save` can produce. We only
/// support [`ArchiveFormat::Oci`]; [`ArchiveFormat::DockerSave`] is rejected at
/// the publish gate and again at registry-request time.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ArchiveFormat {
    /// OCI image-layout 1.0.0 (has `oci-layout` + `index.json`).
    Oci,
    /// Legacy `docker save` (has top-level `manifest.json`, no `oci-layout`).
    DockerSave,
    /// Neither — the archive doesn't look like a container image.
    Unknown,
}

/// Reader for an OCI image-layout tarball. Stateless: each call re-opens and
/// re-scans the file. Cheap because the archive lives on local disk; the
/// alternative (indexing the offsets up-front) buys little for the 3-4 reads
/// docker does per pull.
#[derive(Debug, Clone)]
pub struct OciArchive {
    path: PathBuf,
}

impl OciArchive {
    /// Wrap an on-disk tarball path. Does **not** validate the format; call
    /// [`Self::format`] or [`Self::read_index`] to do that.
    pub fn open(path: impl Into<PathBuf>) -> Self {
        Self { path: path.into() }
    }

    pub fn path(&self) -> &Path {
        &self.path
    }

    /// Sniff the archive and decide which of the two layouts it carries. Reads
    /// only top-level entries — short-circuits as soon as it can decide.
    pub fn format(&self) -> Result<ArchiveFormat> {
        let f = File::open(&self.path)
            .with_context(|| format!("opening {}", self.path.display()))?;
        format_of(f)
    }

    /// Return the parsed `index.json`. Errors if the archive is not OCI image-layout.
    pub fn read_index(&self) -> Result<ImageIndex> {
        let bytes = self.read_entry(INDEX_FILE)?;
        let idx: ImageIndex = serde_json::from_slice(&bytes)
            .context("parsing index.json")?;
        Ok(idx)
    }

    /// Read a blob by digest. Accepts both `sha256:<hex>` and bare `<hex>`. The
    /// returned bytes are exactly what the registry endpoint hands to the
    /// docker client (no re-compression, no re-wrapping).
    pub fn read_blob(&self, digest: &str) -> Result<Vec<u8>> {
        let hex = digest.strip_prefix("sha256:").unwrap_or(digest);
        if hex.len() != 64 || !hex.chars().all(|c| c.is_ascii_hexdigit()) {
            bail!("malformed digest {digest:?} — expected sha256:<64-hex>");
        }
        let want = format!("blobs/sha256/{hex}");
        self.read_entry(&want)
    }

    fn read_entry(&self, name: &str) -> Result<Vec<u8>> {
        read_tar_file(&self.path, name)?
            .ok_or_else(|| anyhow!("entry {name} not found in {}", self.path.display()))
    }
}

/// Read a file by its exact path from a plain (uncompressed) tar archive on
/// disk. `Ok(None)` when the path isn't in the archive. The generic kernel
/// under [`OciArchive`]'s blob reads and the curator package-repo facade
/// ([`super::repo`]) — the OCI shim addresses entries by `blobs/sha256/<hex>`,
/// the repo facade by arbitrary repo-tree paths (`dists/stable/InRelease`, …),
/// but both are "find one named entry and hand back its bytes".
pub(crate) fn read_tar_file(tarball: &Path, inner_path: &str) -> Result<Option<Vec<u8>>> {
    let f = File::open(tarball)
        .with_context(|| format!("opening {}", tarball.display()))?;
    read_named_entry(f, inner_path)
        .with_context(|| format!("looking up {inner_path} in {}", tarball.display()))
}

fn format_of<R: Read>(reader: R) -> Result<ArchiveFormat> {
    let mut ar = tar::Archive::new(reader);
    let mut saw_oci_layout = false;
    let mut saw_index = false;
    let mut saw_docker_manifest = false;
    for ent in ar.entries().context("reading tar header")? {
        let ent = ent.context("reading tar entry")?;
        let path = ent.path().context("reading tar entry path")?;
        let Some(p) = path.to_str() else { continue };
        // Match exact top-level filenames only — don't be fooled by
        // a nested `subdir/oci-layout`.
        match p {
            OCI_LAYOUT_FILE => saw_oci_layout = true,
            INDEX_FILE => saw_index = true,
            DOCKER_SAVE_MANIFEST => saw_docker_manifest = true,
            _ => {}
        }
        if saw_oci_layout && saw_index {
            return Ok(ArchiveFormat::Oci);
        }
    }
    if saw_oci_layout && saw_index {
        Ok(ArchiveFormat::Oci)
    } else if saw_docker_manifest {
        Ok(ArchiveFormat::DockerSave)
    } else {
        Ok(ArchiveFormat::Unknown)
    }
}

fn read_named_entry<R: Read + Seek>(reader: R, name: &str) -> Result<Option<Vec<u8>>> {
    let mut ar = tar::Archive::new(reader);
    for ent in ar.entries().context("reading tar header")? {
        let mut ent = ent.context("reading tar entry")?;
        let path = ent.path().context("reading tar entry path")?;
        let same = path.to_str().map(|p| p == name).unwrap_or(false);
        if same {
            // Cap the pre-allocation by the attacker-declared tar size: a
            // crafted header can claim a huge size on a tiny entry and OOM
            // the agent on this single allocation. read_to_end still reads
            // the real content (growing as needed), so honest entries larger
            // than the cap are unaffected — only the up-front reserve is bounded.
            let cap = (ent.size()).min(MAX_PREALLOC_BYTES) as usize;
            let mut buf = Vec::with_capacity(cap);
            ent.read_to_end(&mut buf)?;
            return Ok(Some(buf));
        }
    }
    Ok(None)
}

// ---------- index.json schema ----------
//
// We model only the subset the shim consumes. The OCI spec allows much more
// (annotations, platform fields on manifest descriptors, urls, data). The
// registry shim only needs digest, media_type, size, and the
// `org.opencontainers.image.ref.name` annotation that tools use to embed a
// tag in the archive.

/// OCI image index (root of `index.json`).
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ImageIndex {
    #[serde(rename = "schemaVersion")]
    pub schema_version: u32,
    #[serde(rename = "mediaType", default)]
    pub media_type: String,
    #[serde(default)]
    pub manifests: Vec<Descriptor>,
}

/// One entry in [`ImageIndex::manifests`].
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Descriptor {
    #[serde(rename = "mediaType")]
    pub media_type: String,
    pub digest: String,
    pub size: u64,
    #[serde(default)]
    pub platform: Option<DescriptorPlatform>,
    #[serde(default)]
    pub annotations: std::collections::BTreeMap<String, String>,
}

/// Optional per-manifest platform (only present on multi-arch indices, which
/// curator deliberately doesn't accept — see ADR-024).
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DescriptorPlatform {
    pub architecture: String,
    pub os: String,
    #[serde(rename = "os.version", default)]
    pub os_version: Option<String>,
    #[serde(rename = "os.features", default)]
    pub os_features: Option<Vec<String>>,
    #[serde(default)]
    pub variant: Option<String>,
}

impl ImageIndex {
    /// Find the manifest descriptor with the given
    /// `org.opencontainers.image.ref.name` annotation (the embedded tag).
    /// Returns `None` if no descriptor carries that ref.
    pub fn find_by_ref_name(&self, name: &str) -> Option<&Descriptor> {
        self.manifests.iter().find(|d| {
            d.annotations
                .get("org.opencontainers.image.ref.name")
                .map(|v| v == name)
                .unwrap_or(false)
        })
    }

    /// Find the manifest descriptor with the given digest. Accepts both
    /// `sha256:<hex>` and bare `<hex>`.
    pub fn find_by_digest(&self, digest: &str) -> Option<&Descriptor> {
        let want = if digest.starts_with("sha256:") {
            digest.to_string()
        } else {
            format!("sha256:{digest}")
        };
        self.manifests.iter().find(|d| d.digest == want)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::Cursor;

    /// Build a minimal valid OCI image-layout tarball in-memory and return the
    /// bytes. Caller can write it to a temp file and exercise the reader.
    fn build_oci_tarball(entries: &[(&str, &[u8])]) -> Vec<u8> {
        let mut buf: Vec<u8> = Vec::new();
        {
            let mut ar = tar::Builder::new(&mut buf);
            for (name, body) in entries {
                let mut h = tar::Header::new_gnu();
                h.set_size(body.len() as u64);
                h.set_mode(0o644);
                h.set_cksum();
                ar.append_data(&mut h, name, Cursor::new(body)).unwrap();
            }
            ar.finish().unwrap();
        }
        buf
    }

    #[test]
    fn detects_oci_format() {
        let blob = build_oci_tarball(&[
            ("oci-layout", b"{\"imageLayoutVersion\":\"1.0.0\"}"),
            ("index.json", b"{\"schemaVersion\":2,\"manifests\":[]}"),
        ]);
        let fmt = format_of(Cursor::new(&blob)).unwrap();
        assert_eq!(fmt, ArchiveFormat::Oci);
    }

    #[test]
    fn detects_docker_save_format() {
        let blob = build_oci_tarball(&[
            ("manifest.json", b"[]"),
        ]);
        let fmt = format_of(Cursor::new(&blob)).unwrap();
        assert_eq!(fmt, ArchiveFormat::DockerSave);
    }

    #[test]
    fn unknown_format_when_neither_marker_present() {
        let blob = build_oci_tarball(&[
            ("hello.txt", b"hi"),
        ]);
        let fmt = format_of(Cursor::new(&blob)).unwrap();
        assert_eq!(fmt, ArchiveFormat::Unknown);
    }

    #[test]
    fn nested_oci_layout_does_not_confuse_detector() {
        let blob = build_oci_tarball(&[
            ("subdir/oci-layout", b"{}"),
            ("subdir/index.json", b"{}"),
            ("manifest.json", b"[]"),
        ]);
        let fmt = format_of(Cursor::new(&blob)).unwrap();
        assert_eq!(fmt, ArchiveFormat::DockerSave, "nested files must not satisfy the OCI check");
    }

    #[test]
    fn reads_index_and_blob() {
        let index_bytes = br#"{
            "schemaVersion": 2,
            "mediaType": "application/vnd.oci.image.index.v1+json",
            "manifests": [{
                "mediaType": "application/vnd.oci.image.manifest.v1+json",
                "digest": "sha256:0000000000000000000000000000000000000000000000000000000000000001",
                "size": 7,
                "annotations": {"org.opencontainers.image.ref.name": "latest"}
            }]
        }"#;
        let manifest_blob: &[u8] = b"manibod";
        let blob = build_oci_tarball(&[
            ("oci-layout", br#"{"imageLayoutVersion":"1.0.0"}"#),
            ("index.json", index_bytes),
            ("blobs/sha256/0000000000000000000000000000000000000000000000000000000000000001", manifest_blob),
        ]);

        let f = tempfile::NamedTempFile::new().unwrap();
        std::fs::write(f.path(), &blob).unwrap();
        let ar = OciArchive::open(f.path());

        assert_eq!(ar.format().unwrap(), ArchiveFormat::Oci);

        let idx = ar.read_index().unwrap();
        assert_eq!(idx.manifests.len(), 1);
        let d = idx.find_by_ref_name("latest").expect("ref name lookup must work");
        assert_eq!(d.digest, "sha256:0000000000000000000000000000000000000000000000000000000000000001");
        assert_eq!(d.size, 7);

        assert!(idx.find_by_digest("sha256:0000000000000000000000000000000000000000000000000000000000000001").is_some());
        assert!(idx.find_by_digest("0000000000000000000000000000000000000000000000000000000000000001").is_some(), "bare hex accepted");
        assert!(idx.find_by_digest("sha256:beef").is_none());

        let got = ar.read_blob("sha256:0000000000000000000000000000000000000000000000000000000000000001").unwrap();
        assert_eq!(got, manifest_blob);

        let bare = ar.read_blob("0000000000000000000000000000000000000000000000000000000000000001").unwrap();
        assert_eq!(bare, manifest_blob);
    }

    #[test]
    fn rejects_malformed_digest() {
        let blob = build_oci_tarball(&[
            ("oci-layout", b"{}"),
            ("index.json", b"{}"),
        ]);
        let f = tempfile::NamedTempFile::new().unwrap();
        std::fs::write(f.path(), &blob).unwrap();
        let ar = OciArchive::open(f.path());

        assert!(ar.read_blob("sha256:short").is_err());
        assert!(ar.read_blob("sha256:zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz").is_err());
    }

    #[test]
    fn missing_blob_yields_error() {
        let blob = build_oci_tarball(&[
            ("oci-layout", b"{}"),
            ("index.json", br#"{"schemaVersion":2,"manifests":[]}"#),
        ]);
        let f = tempfile::NamedTempFile::new().unwrap();
        std::fs::write(f.path(), &blob).unwrap();
        let ar = OciArchive::open(f.path());

        assert!(ar.read_blob("sha256:0000000000000000000000000000000000000000000000000000000000000099").is_err());
    }
}
