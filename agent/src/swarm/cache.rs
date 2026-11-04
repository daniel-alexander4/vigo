// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Content-addressed blob cache with disk space guardrails.
//!
//! Complete blobs: `<dir>/<sha256>`
//! In-progress:   `<dir>/<sha256>.partial/chunk_0000`, `chunk_0001`, ...

use anyhow::{bail, Context, Result};
use serde::Deserialize;
use sha2::{Digest, Sha256};
use std::collections::{HashMap, HashSet};
use std::fs;
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Arc;
use tracing::info;

/// Chunk source data read from {sha256}.sources files.
#[derive(Deserialize)]
pub struct ChunkSourceData {
    pub counts: HashMap<String, u32>,
    #[serde(default)]
    pub chunks: Vec<ChunkDetail>,
    #[serde(default)]
    pub total_ms: u64,
}

#[derive(Deserialize)]
pub struct ChunkDetail {
    pub chunk: u32,
    pub source: String,
    pub ms: u64,
}

use super::meta::CHUNK_SIZE;

/// Default disk reserve in MiB — downloads are skipped if available space
/// after the download would drop below this threshold.
const DEFAULT_RESERVE_MB: u64 = 512;

/// Default cache size limit in MiB.
const DEFAULT_CACHE_MAX_MB: u64 = 2048;

/// Blob cache backed by the filesystem.
pub struct BlobCache {
    dir: PathBuf,
    reserve_mb: u64,
    max_mb: u64,
    /// Monotonic counter bumped on every mutation (write_chunk, assemble,
    /// evict, delete_partial). Advertised as `X-Peer-Server-Version` and
    /// used in the ETag on `/meta` responses so probing peers can cheaply
    /// detect that our inventory hasn't changed.
    version: Arc<AtomicU64>,
    /// Free-space source behind [`Self::check_disk_space`]. Always
    /// [`DiskProbe::HOST`] in production; tests swap it so they don't
    /// depend on how full the build host's disk is.
    disk_probe: DiskProbe,
}

/// The two disk measurements [`BlobCache::check_disk_space`] gates on.
#[derive(Clone, Copy)]
pub(crate) struct DiskProbe {
    /// Bytes available to unprivileged writers on the filesystem holding
    /// the given path.
    available: fn(&Path) -> Result<u64>,
    /// Whether the swarm disk is at or below the pressure threshold.
    pressured: fn() -> bool,
}

impl DiskProbe {
    /// The real host measurements: statvfs / GetDiskFreeSpaceExW on the
    /// cache dir, and the swarm-purpose pressure check.
    const HOST: DiskProbe = DiskProbe {
        available: available_disk_space,
        pressured: || {
            crate::swarm::peer_health::is_self_pressured(
                &crate::swarm::peer_health::Purpose::Swarm,
            )
        },
    };

    /// A disk with 1 TiB free and no pressure.
    #[cfg(test)]
    pub(crate) const ROOMY: DiskProbe = DiskProbe {
        available: |_| Ok(1 << 40),
        pressured: || false,
    };

    /// A disk with 1 TiB free that is nonetheless past the pressure
    /// threshold (a very large, nearly full volume).
    #[cfg(test)]
    pub(crate) const PRESSURED: DiskProbe = DiskProbe {
        available: |_| Ok(1 << 40),
        pressured: || true,
    };
}

impl BlobCache {
    /// Create a blob cache at `dir`, creating the directory if needed.
    pub fn new(dir: PathBuf) -> Result<Self> {
        fs::create_dir_all(&dir).context("creating blob cache directory")?;
        Ok(Self {
            dir,
            reserve_mb: DEFAULT_RESERVE_MB,
            max_mb: DEFAULT_CACHE_MAX_MB,
            version: Arc::new(AtomicU64::new(1)),
            disk_probe: DiskProbe::HOST,
        })
    }

    /// Replace the free-space source (tests only).
    #[cfg(test)]
    pub(crate) fn with_disk_probe(mut self, probe: DiskProbe) -> Self {
        self.disk_probe = probe;
        self
    }

    /// Current monotonic version of the local cache. Incremented on any
    /// write that could affect what /meta reports.
    pub fn version(&self) -> u64 {
        self.version.load(Ordering::Relaxed)
    }

    fn bump_version(&self) {
        self.version.fetch_add(1, Ordering::Relaxed);
    }

    /// Set the disk reserve (minimum free space after download).
    pub fn set_reserve_mb(&mut self, mb: u64) {
        self.reserve_mb = mb;
    }

    /// Set the maximum cache size.
    pub fn set_max_mb(&mut self, mb: u64) {
        self.max_mb = mb;
    }

    /// Check if the complete blob exists in the cache.
    pub fn has_blob(&self, sha256: &str) -> bool {
        self.blob_path(sha256).is_file()
    }

    /// Check if a specific chunk exists for an in-progress blob.
    pub fn has_chunk(&self, sha256: &str, index: u32) -> bool {
        self.chunk_path(sha256, index).is_file()
    }

    /// Return the set of chunk indices available for a blob (partial or complete).
    /// Callers must pass the real `total_chunks` from the manifest — passing a
    /// sentinel like `u32::MAX` would materialize a multi-gigabyte HashSet.
    pub fn chunk_bitmap(&self, sha256: &str, total_chunks: u32) -> HashSet<u32> {
        if self.has_blob(sha256) {
            return (0..total_chunks).collect();
        }
        self.partial_chunks(sha256)
    }

    /// Enumerate chunk indices present in the `.partial/` directory for an
    /// in-progress download. Returns empty if no partial dir exists. Unlike
    /// `chunk_bitmap`, this never short-circuits on a complete blob, so it is
    /// safe to call without knowing the total chunk count — callers that only
    /// want partial chunks should use this directly.
    pub fn partial_chunks(&self, sha256: &str) -> HashSet<u32> {
        let partial = self.partial_dir(sha256);
        if !partial.is_dir() {
            return HashSet::new();
        }
        let mut chunks = HashSet::new();
        if let Ok(entries) = fs::read_dir(&partial) {
            for entry in entries.flatten() {
                if let Some(name) = entry.file_name().to_str() {
                    if let Some(idx) = parse_chunk_filename(name) {
                        chunks.insert(idx);
                    }
                }
            }
        }
        chunks
    }

    /// Write a single chunk to the partial directory.
    pub fn write_chunk(&self, sha256: &str, index: u32, data: &[u8]) -> Result<()> {
        let partial = self.partial_dir(sha256);
        fs::create_dir_all(&partial).context("creating partial directory")?;
        let path = self.chunk_path(sha256, index);
        fs::write(&path, data).with_context(|| format!("writing chunk {}", index))?;
        self.bump_version();
        Ok(())
    }

    /// Read a chunk from cache (from partial dir or by reading a range of the complete blob).
    pub fn read_chunk(&self, sha256: &str, index: u32) -> Result<Vec<u8>> {
        // Try partial dir first.
        let chunk_path = self.chunk_path(sha256, index);
        if chunk_path.is_file() {
            return fs::read(&chunk_path).context("reading chunk file");
        }
        // Try the complete blob.
        let blob_path = self.blob_path(sha256);
        if blob_path.is_file() {
            use std::io::{Read, Seek, SeekFrom};
            let mut f = fs::File::open(&blob_path).context("opening complete blob")?;
            let offset = index as u64 * CHUNK_SIZE;
            f.seek(SeekFrom::Start(offset))?;
            let mut buf = vec![0u8; CHUNK_SIZE as usize];
            let n = f.read(&mut buf)?;
            buf.truncate(n);
            return Ok(buf);
        }
        bail!("chunk {} not found for blob {}", index, &sha256[..12]);
    }

    /// Assemble all chunks into the final blob, verify SHA256, and clean up.
    pub fn assemble(&self, sha256: &str, expected_size: u64, total_chunks: u32) -> Result<PathBuf> {
        let partial = self.partial_dir(sha256);
        let dest = self.blob_path(sha256);

        // If already assembled, just verify.
        if dest.is_file() {
            return Ok(dest);
        }

        let tmp = self.dir.join(format!(".assembling-{}", &sha256[..16]));

        // Drop guard — removes the temp file on ANY early return
        // (ENOSPC mid-write, missing chunk file, hash/size mismatch,
        // panic). Without this, an interrupted assembly leaves a
        // half-written `.assembling-*` file on disk that nothing else
        // cleans up. Forgotten only after the successful atomic rename.
        struct TempGuard<'a>(&'a std::path::Path);
        impl Drop for TempGuard<'_> {
            fn drop(&mut self) {
                let _ = fs::remove_file(self.0);
            }
        }
        let guard = TempGuard(&tmp);

        {
            use std::io::Write;
            let mut out = fs::File::create(&tmp).context("creating assembly temp file")?;
            for i in 0..total_chunks {
                let chunk_path = self.chunk_path(sha256, i);
                let data = fs::read(&chunk_path)
                    .with_context(|| format!("reading chunk {} during assembly", i))?;
                out.write_all(&data)
                    .with_context(|| format!("writing chunk {} during assembly", i))?;
            }
            out.flush().context("flushing assembly temp file")?;
        }

        // Verify SHA256.
        let computed = hash_file(&tmp)?;
        if computed != sha256 {
            bail!(
                "SHA256 mismatch after assembly: expected {}, got {}",
                &sha256[..16],
                &computed[..16]
            );
        }

        // Verify size.
        let actual_size = fs::metadata(&tmp)?.len();
        if actual_size != expected_size {
            bail!(
                "size mismatch after assembly: expected {}, got {}",
                expected_size,
                actual_size
            );
        }

        // Atomic rename into the final blob path. Forget the guard so
        // its Drop doesn't try to remove the now-renamed file.
        fs::rename(&tmp, &dest).context("renaming assembled blob")?;
        std::mem::forget(guard);

        // Clean up partial directory.
        if partial.is_dir() {
            fs::remove_dir_all(&partial).ok();
        }

        self.bump_version();
        info!(sha256 = &sha256[..12], size = expected_size, "blob assembled and verified");
        Ok(dest)
    }

    /// List all complete blobs in the cache.
    pub fn list_blobs(&self) -> Vec<String> {
        let (blobs, _) = self.list_blobs_and_partials();
        blobs
    }

    /// List in-progress partial blobs (sha256 hashes with a .partial directory).
    pub fn list_partials(&self) -> Vec<String> {
        let (_, partials) = self.list_blobs_and_partials();
        partials
    }

    /// Walk `.blobs/` once and partition entries into (complete_blobs,
    /// partial_blobs). Saves one full directory read per call compared to
    /// calling `list_blobs` and `list_partials` back to back — the
    /// announcer does exactly that on every cycle.
    pub fn list_blobs_and_partials(&self) -> (Vec<String>, Vec<String>) {
        let mut blobs = Vec::new();
        let mut partials = Vec::new();
        let entries = match fs::read_dir(&self.dir) {
            Ok(e) => e,
            Err(_) => return (blobs, partials),
        };
        for entry in entries.flatten() {
            let name = entry.file_name();
            let s = name.to_string_lossy();
            if let Some(sha) = s.strip_suffix(".partial") {
                if sha.len() == 64
                    && sha.chars().all(|c| c.is_ascii_hexdigit() && !c.is_uppercase())
                {
                    partials.push(sha.to_string());
                }
                continue;
            }
            if s.len() == 64
                && s.chars().all(|c| c.is_ascii_hexdigit() && !c.is_uppercase())
                && entry.path().is_file()
            {
                blobs.push(s.to_string());
            }
        }
        (blobs, partials)
    }

    /// Create a symlink in the sibling `files/` directory so admins can
    /// find blobs by their original filename.
    pub fn link_by_name(&self, sha256: &str, label: &str) {
        if label.is_empty() {
            return;
        }
        // Sanitize: strip path separators to prevent directory traversal.
        let safe_label = label.replace(['/', '\\'], "_");
        if safe_label.is_empty() {
            return;
        }

        let by_name_dir = crate::platform::paths::files_dir_for(&self.dir);
        if fs::create_dir_all(&by_name_dir).is_err() {
            return;
        }

        let link_path = by_name_dir.join(&safe_label);
        // Remove stale symlink if it exists (may point to a different blob).
        if link_path.exists() || link_path.symlink_metadata().is_ok() {
            fs::remove_file(&link_path).ok();
        }

        let target = self.blob_path(sha256);
        #[cfg(unix)]
        {
            std::os::unix::fs::symlink(&target, &link_path).ok();
        }
        #[cfg(windows)]
        {
            std::os::windows::fs::symlink_file(&target, &link_path).ok();
        }
    }

    /// Remove a blob (complete and partial) from the cache.
    pub fn evict(&self, sha256: &str) {
        fs::remove_file(self.blob_path(sha256)).ok();
        fs::remove_dir_all(self.partial_dir(sha256)).ok();
        fs::remove_file(self.sources_path(sha256)).ok();
        self.bump_version();
    }

    /// Delete only the partial directory for a blob (preserving the complete blob if it exists).
    /// Used after assembly failure to force re-download of all chunks.
    pub fn delete_partial(&self, sha256: &str) {
        fs::remove_dir_all(self.partial_dir(sha256)).ok();
        fs::remove_file(self.sources_path(sha256)).ok();
        self.bump_version();
    }

    /// Write chunk source data after a successful download.
    /// File: .blobs/{sha256}.sources (JSON with summary counts, per-chunk detail, and total time)
    pub fn write_chunk_sources(
        &self,
        sha256: &str,
        counts: &std::collections::HashMap<String, u32>,
        chunks: &[(u32, String, u64)], // (index, source, duration_ms)
        total_ms: u64,
    ) {
        if counts.is_empty() {
            return;
        }
        let detail: Vec<serde_json::Value> = chunks
            .iter()
            .map(|(idx, src, ms)| serde_json::json!({"chunk": idx, "source": src, "ms": ms}))
            .collect();
        let data = serde_json::json!({
            "counts": counts,
            "chunks": detail,
            "total_ms": total_ms,
        });
        if let Ok(json) = serde_json::to_string(&data) {
            let final_path = self.sources_path(sha256);
            let tmp_path = self.dir.join(format!("{}.sources.tmp", sha256));
            if fs::write(&tmp_path, json).is_ok() {
                fs::rename(&tmp_path, &final_path).ok();
            }
        }
    }

    /// Read chunk source data for a cached blob.
    pub fn read_chunk_sources(&self, sha256: &str) -> Option<ChunkSourceData> {
        let data = fs::read_to_string(self.sources_path(sha256)).ok()?;
        serde_json::from_str(&data).ok()
    }

    fn sources_path(&self, sha256: &str) -> std::path::PathBuf {
        self.dir.join(format!("{}.sources", sha256))
    }

    /// Check if there is enough disk space to download a blob of `size` bytes.
    /// Returns Ok(()) if sufficient, Err with a descriptive message if not.
    ///
    /// Two gates, both must pass:
    ///
    /// 1. Absolute reserve — `available_bytes >= size + reserve_mb`.
    ///    Originally the only check. Protects against accepting a
    ///    blob that would fully consume the disk.
    /// 2. Pressure threshold — `free_pct > min_free_pct` (default 10%,
    ///    [`crate::swarm::peer_health::DEFAULT_MIN_FREE_PCT`]). Swarm
    ///    uses a pull model, so the "sender disables sending" guardrail
    ///    from the push subsystems lands here as "receiver refuses to
    ///    start a download when the target disk is already pressured."
    pub fn check_disk_space(&self, size: u64) -> Result<()> {
        let available = (self.disk_probe.available)(&self.dir)?;
        let needed = size + self.reserve_mb * 1024 * 1024;
        if available < needed {
            let avail_mb = available / (1024 * 1024);
            let need_mb = needed / (1024 * 1024);
            bail!(
                "insufficient_disk:available_mb={},needed_mb={}",
                avail_mb,
                need_mb
            );
        }
        if (self.disk_probe.pressured)() {
            bail!(
                "disk_pressure:free_pct_at_or_below={}",
                crate::swarm::peer_health::min_free_pct()
            );
        }
        Ok(())
    }

    /// Path to a complete blob.
    pub fn blob_path(&self, sha256: &str) -> PathBuf {
        self.dir.join(sha256)
    }

    /// Path to the partial download directory for a blob.
    fn partial_dir(&self, sha256: &str) -> PathBuf {
        self.dir.join(format!("{}.partial", sha256))
    }

    /// Path to a specific chunk file.
    fn chunk_path(&self, sha256: &str, index: u32) -> PathBuf {
        self.partial_dir(sha256).join(format!("chunk_{:010}", index))
    }

    /// Cache directory path.
    pub fn dir(&self) -> &Path {
        &self.dir
    }
}

/// Parse a chunk filename like "chunk_0042" into the index 42.
fn parse_chunk_filename(name: &str) -> Option<u32> {
    name.strip_prefix("chunk_")?.parse().ok()
}

/// Compute the SHA256 hash of a file.
fn hash_file(path: &Path) -> Result<String> {
    use std::io::Read;
    let mut f = fs::File::open(path)?;
    let mut hasher = Sha256::new();
    let mut buf = [0u8; 8192];
    loop {
        let n = f.read(&mut buf)?;
        if n == 0 {
            break;
        }
        hasher.update(&buf[..n]);
    }
    Ok(hex::encode(hasher.finalize()))
}

/// Get available disk space at the given path.
#[cfg(unix)]
fn available_disk_space(path: &Path) -> Result<u64> {
    use std::ffi::CString;
    use std::mem::MaybeUninit;

    let c_path = CString::new(path.to_string_lossy().as_bytes())
        .context("invalid path for statvfs")?;
    let mut stat = MaybeUninit::<libc::statvfs>::uninit();
    let ret = unsafe { libc::statvfs(c_path.as_ptr(), stat.as_mut_ptr()) };
    if ret != 0 {
        bail!("statvfs failed: {}", std::io::Error::last_os_error());
    }
    let stat = unsafe { stat.assume_init() };
    Ok(stat.f_bavail * stat.f_frsize)
}

#[cfg(windows)]
fn available_disk_space(path: &Path) -> Result<u64> {
    use std::os::windows::ffi::OsStrExt;

    let wide: Vec<u16> = path
        .as_os_str()
        .encode_wide()
        .chain(std::iter::once(0))
        .collect();
    let mut free: u64 = 0;
    let ret = unsafe {
        GetDiskFreeSpaceExW(wide.as_ptr(), &mut free as *mut u64, std::ptr::null_mut(), std::ptr::null_mut())
    };
    if ret == 0 {
        bail!("GetDiskFreeSpaceExW failed");
    }
    Ok(free)
}

#[cfg(windows)]
extern "system" {
    fn GetDiskFreeSpaceExW(
        directory: *const u16,
        free_bytes_available: *mut u64,
        total_bytes: *mut u64,
        total_free_bytes: *mut u64,
    ) -> i32;
}

#[cfg(test)]
mod tests {
    use super::*;
    

    #[test]
    fn test_write_and_read_chunk() {
        let dir = tempfile::tempdir().unwrap();
        let cache = BlobCache::new(dir.path().to_path_buf()).unwrap();
        let sha = "a".repeat(64);

        cache.write_chunk(&sha, 0, b"chunk zero").unwrap();
        cache.write_chunk(&sha, 1, b"chunk one").unwrap();

        assert!(cache.has_chunk(&sha, 0));
        assert!(cache.has_chunk(&sha, 1));
        assert!(!cache.has_chunk(&sha, 2));

        let data = cache.read_chunk(&sha, 0).unwrap();
        assert_eq!(data, b"chunk zero");
    }

    #[test]
    fn test_chunk_bitmap() {
        let dir = tempfile::tempdir().unwrap();
        let cache = BlobCache::new(dir.path().to_path_buf()).unwrap();
        let sha = "b".repeat(64);

        assert!(cache.chunk_bitmap(&sha, 5).is_empty());

        cache.write_chunk(&sha, 1, b"x").unwrap();
        cache.write_chunk(&sha, 3, b"y").unwrap();

        let bitmap = cache.chunk_bitmap(&sha, 5);
        assert_eq!(bitmap.len(), 2);
        assert!(bitmap.contains(&1));
        assert!(bitmap.contains(&3));
    }

    #[test]
    fn test_partial_chunks_ignores_complete_blob() {
        // Regression: an orphan `.partial/` left behind for a completed blob
        // must not route through the all-chunks short-circuit. `partial_chunks`
        // is used by callers (announcer, traits) that don't know — and don't
        // need — the total chunk count, and it must never materialize
        // (0..total) for a complete blob.
        let dir = tempfile::tempdir().unwrap();
        let cache = BlobCache::new(dir.path().to_path_buf()).unwrap();
        let sha = "c".repeat(64);

        // Simulate a complete blob and a leftover partial dir for the same sha.
        fs::write(cache.blob_path(&sha), b"complete blob bytes").unwrap();
        cache.write_chunk(&sha, 7, b"orphan chunk").unwrap();
        assert!(cache.has_blob(&sha));

        let partial = cache.partial_chunks(&sha);
        assert_eq!(partial.len(), 1);
        assert!(partial.contains(&7));
    }

    #[test]
    fn test_assemble_and_verify() {
        let dir = tempfile::tempdir().unwrap();
        let cache = BlobCache::new(dir.path().to_path_buf()).unwrap();

        let data = b"hello swarm blob!";
        let sha = hex::encode(sha2::Sha256::digest(data));

        // Write as single chunk.
        cache.write_chunk(&sha, 0, data).unwrap();

        let path = cache.assemble(&sha, data.len() as u64, 1).unwrap();
        assert!(path.is_file());
        assert_eq!(fs::read(&path).unwrap(), data);
        assert!(cache.has_blob(&sha));

        // Partial dir should be cleaned up.
        assert!(!cache.partial_dir(&sha).exists());
    }

    #[test]
    fn test_assemble_bad_hash() {
        let dir = tempfile::tempdir().unwrap();
        let cache = BlobCache::new(dir.path().to_path_buf()).unwrap();
        let sha = "c".repeat(64); // wrong hash

        cache.write_chunk(&sha, 0, b"data").unwrap();
        assert!(cache.assemble(&sha, 4, 1).is_err());
    }

    #[test]
    fn test_list_and_evict() {
        let dir = tempfile::tempdir().unwrap();
        let cache = BlobCache::new(dir.path().to_path_buf()).unwrap();

        let data = b"test";
        let sha = hex::encode(sha2::Sha256::digest(data));
        cache.write_chunk(&sha, 0, data).unwrap();
        cache.assemble(&sha, 4, 1).unwrap();

        let blobs = cache.list_blobs();
        assert_eq!(blobs.len(), 1);
        assert_eq!(blobs[0], sha);

        cache.evict(&sha);
        assert!(!cache.has_blob(&sha));
        assert!(cache.list_blobs().is_empty());
    }

    #[test]
    fn test_disk_space_check() {
        let dir = tempfile::tempdir().unwrap();
        let cache = BlobCache::new(dir.path().to_path_buf())
            .unwrap()
            .with_disk_probe(DiskProbe::ROOMY);

        // A reasonable size fits on a roomy, unpressured disk.
        assert!(cache.check_disk_space(1024).is_ok());

        // A size past what's available fails the absolute-reserve gate.
        let err = cache.check_disk_space(u64::MAX / 2).unwrap_err().to_string();
        assert!(err.starts_with("insufficient_disk:"), "{err}");

        // Space for the blob is not enough once the disk is pressured.
        let cache = cache.with_disk_probe(DiskProbe::PRESSURED);
        let err = cache.check_disk_space(1024).unwrap_err().to_string();
        assert!(err.starts_with("disk_pressure:"), "{err}");
    }

    #[test]
    fn test_disk_space_check_counts_reserve() {
        // 1 MiB short of size + reserve must fail; exactly enough passes.
        let dir = tempfile::tempdir().unwrap();
        let mut cache = BlobCache::new(dir.path().to_path_buf()).unwrap().with_disk_probe(DiskProbe {
            available: |_| Ok(600 * 1024 * 1024),
            pressured: || false,
        });
        cache.set_reserve_mb(512);
        assert!(cache.check_disk_space(88 * 1024 * 1024).is_ok());
        assert!(cache.check_disk_space(89 * 1024 * 1024).is_err());
    }

    #[test]
    fn test_parse_chunk_filename() {
        assert_eq!(parse_chunk_filename("chunk_0000"), Some(0));
        assert_eq!(parse_chunk_filename("chunk_0042"), Some(42));
        assert_eq!(parse_chunk_filename("chunk_9999"), Some(9999));
        assert_eq!(parse_chunk_filename("notachunk"), None);
        assert_eq!(parse_chunk_filename("chunk_"), None);
    }
}
