// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

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
}

impl BlobCache {
    /// Create a blob cache at `dir`, creating the directory if needed.
    pub fn new(dir: PathBuf) -> Result<Self> {
        fs::create_dir_all(&dir).context("creating blob cache directory")?;
        Ok(Self {
            dir,
            reserve_mb: DEFAULT_RESERVE_MB,
            max_mb: DEFAULT_CACHE_MAX_MB,
        })
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
    pub fn chunk_bitmap(&self, sha256: &str, total_chunks: u32) -> HashSet<u32> {
        // If the complete blob exists, all chunks are available.
        if self.has_blob(sha256) {
            return (0..total_chunks).collect();
        }
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
        {
            use std::io::Write;
            let mut out = fs::File::create(&tmp).context("creating assembly temp file")?;
            for i in 0..total_chunks {
                let chunk_path = self.chunk_path(sha256, i);
                let data = fs::read(&chunk_path)
                    .with_context(|| format!("reading chunk {} during assembly", i))?;
                out.write_all(&data)?;
            }
            out.flush()?;
        }

        // Verify SHA256.
        let computed = hash_file(&tmp)?;
        if computed != sha256 {
            fs::remove_file(&tmp).ok();
            bail!(
                "SHA256 mismatch after assembly: expected {}, got {}",
                &sha256[..16],
                &computed[..16]
            );
        }

        // Verify size.
        let actual_size = fs::metadata(&tmp)?.len();
        if actual_size != expected_size {
            fs::remove_file(&tmp).ok();
            bail!(
                "size mismatch after assembly: expected {}, got {}",
                expected_size,
                actual_size
            );
        }

        // Atomic rename.
        fs::rename(&tmp, &dest).context("renaming assembled blob")?;

        // Clean up partial directory.
        if partial.is_dir() {
            fs::remove_dir_all(&partial).ok();
        }

        info!(sha256 = &sha256[..12], size = expected_size, "blob assembled and verified");
        Ok(dest)
    }

    /// List all complete blobs in the cache.
    pub fn list_blobs(&self) -> Vec<String> {
        let mut blobs = Vec::new();
        if let Ok(entries) = fs::read_dir(&self.dir) {
            for entry in entries.flatten() {
                let name = entry.file_name();
                let s = name.to_string_lossy();
                if s.len() == 64 && s.chars().all(|c| c.is_ascii_hexdigit() && !c.is_uppercase())
                    && entry.path().is_file()
                {
                    blobs.push(s.to_string());
                }
            }
        }
        blobs
    }

    /// List in-progress partial blobs (sha256 hashes with a .partial directory).
    pub fn list_partials(&self) -> Vec<String> {
        let mut partials = Vec::new();
        if let Ok(entries) = fs::read_dir(&self.dir) {
            for entry in entries.flatten() {
                let name = entry.file_name();
                let s = name.to_string_lossy();
                if let Some(sha) = s.strip_suffix(".partial") {
                    if sha.len() == 64 && sha.chars().all(|c| c.is_ascii_hexdigit() && !c.is_uppercase()) {
                        partials.push(sha.to_string());
                    }
                }
            }
        }
        partials
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

        let by_name_dir = self.dir.parent().unwrap_or(&self.dir).join("files");
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
    }

    /// Delete only the partial directory for a blob (preserving the complete blob if it exists).
    /// Used after assembly failure to force re-download of all chunks.
    pub fn delete_partial(&self, sha256: &str) {
        fs::remove_dir_all(self.partial_dir(sha256)).ok();
        fs::remove_file(self.sources_path(sha256)).ok();
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
    pub fn check_disk_space(&self, size: u64) -> Result<()> {
        let available = available_disk_space(&self.dir)?;
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
    Ok(stat.f_bavail as u64 * stat.f_frsize as u64)
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
    use std::io::Write;

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
        let cache = BlobCache::new(dir.path().to_path_buf()).unwrap();

        // Checking for a reasonable size should succeed (we have disk space in tests).
        assert!(cache.check_disk_space(1024).is_ok());

        // Checking for an absurd size should fail.
        assert!(cache.check_disk_space(u64::MAX / 2).is_err());
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
