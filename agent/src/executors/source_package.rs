// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Source package executor — downloads and extracts archives idempotently.

use super::download::{download_with_retry, sanitize_url_filename, DownloadConfig};
use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use sha2::{Digest, Sha256};
use std::collections::HashMap;
use std::io::Read;
use std::time::Instant;

/// Minimum available disk space (bytes) on the target filesystem before downloading.
const MIN_DISK_SPACE: u64 = 100 * 1024 * 1024; // 100 MB

pub struct SourcePackageExecutor {
    pub runner: Box<dyn CommandRunner>,
}

impl SourcePackageExecutor {
    pub fn new() -> Self {
        Self {
            runner: Box::new(DefaultRunner),
        }
    }
}

/// Archive format selector — what the extract step does with the source
/// file. Used by both the URL path (extension-derived via
/// [`archive_format_from_url`]) and the `artifact:` path (magic-byte
/// sniffed via [`archive_format_from_magic`]).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum ArchiveFormat {
    TarGz,
    TarBz2,
    TarXz,
    Zip,
    /// Anything not recognized as an archive — treated as a raw binary
    /// and copied straight to `target_path`.
    Raw,
}

/// Detect archive format from a URL or filename's extension.
fn archive_format_from_url(path: &str) -> ArchiveFormat {
    let p = path.to_lowercase();
    if p.ends_with(".tar.gz") || p.ends_with(".tgz") {
        ArchiveFormat::TarGz
    } else if p.ends_with(".tar.bz2") {
        ArchiveFormat::TarBz2
    } else if p.ends_with(".tar.xz") {
        ArchiveFormat::TarXz
    } else if p.ends_with(".zip") {
        ArchiveFormat::Zip
    } else {
        ArchiveFormat::Raw
    }
}

/// Detect archive format from a file's first few bytes — what the
/// `artifact:` path uses since curator-materialized files have no
/// extension. Magic prefixes:
///
/// - gzip — `1f 8b` (covers `.tar.gz`/`.tgz`)
/// - bzip2 — `42 5a 68` (`BZh`, covers `.tar.bz2`)
/// - xz — `fd 37 7a 58 5a 00` (covers `.tar.xz`)
/// - zip — `50 4b 03 04` (`PK\03\04`)
///
/// Note: gzip/bz2/xz magic only confirms the compression layer, not
/// that it wraps a tar. In practice every archive published to curator
/// with these compressions is a tarball; if it isn't, the `tar` call
/// will fail with a clear error and the operator can re-publish or use
/// `url:` with the explicit extension.
///
/// Unknown magic → [`ArchiveFormat::Raw`] — copy straight to target.
fn archive_format_from_magic(path: &str) -> ArchiveFormat {
    let mut f = match std::fs::File::open(path) {
        Ok(f) => f,
        Err(_) => return ArchiveFormat::Raw,
    };
    let mut buf = [0u8; 8];
    if f.read(&mut buf).is_err() {
        return ArchiveFormat::Raw;
    }
    if buf.starts_with(&[0x1f, 0x8b]) {
        ArchiveFormat::TarGz
    } else if buf.starts_with(&[0x42, 0x5a, 0x68]) {
        ArchiveFormat::TarBz2
    } else if buf.starts_with(&[0xfd, 0x37, 0x7a, 0x58, 0x5a, 0x00]) {
        ArchiveFormat::TarXz
    } else if buf.starts_with(&[0x50, 0x4b, 0x03, 0x04]) {
        ArchiveFormat::Zip
    } else {
        ArchiveFormat::Raw
    }
}

impl Executor for SourcePackageExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let url = params.get("url").cloned().unwrap_or_default();
        let artifact = params.get("artifact").cloned().unwrap_or_default();
        let target = params
            .get("target_path")
            .cloned()
            .unwrap_or_default();

        let source_desc = if !artifact.is_empty() { artifact.clone() } else { url.clone() };
        let effective_name = if !target.is_empty() {
            target.clone()
        } else {
            source_desc.clone()
        };
        let name = &effective_name;
        let mut result = ResourceResult::new("source_package", name);

        // Source resolution: either `url:` (download) or `artifact:`
        // (curator-materialized). Mutually exclusive. `sha256:` is
        // redundant alongside `artifact:` (signed catalog entry already
        // verifies blob_sha) — slice 6, ADR-024.
        if url.is_empty() && artifact.is_empty() {
            result.error = Some(
                "source_package executor: one of `url:` or `artifact:` is required".into(),
            );
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }
        if !url.is_empty() && !artifact.is_empty() {
            result.error = Some(
                "source_package executor: `url:` and `artifact:` are mutually exclusive".into(),
            );
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }
        if target.is_empty() {
            result.error = Some("source_package executor: target_path param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");
        if state == "absent" {
            if !std::path::Path::new(&target).exists() {
                result.action = Action::None;
            } else {
                result.action = Action::Deleted;
                result.changed = true;
                if !dry_run {
                    if let Err(e) = std::fs::remove_file(&target) {
                        result.error = Some(format!("remove {}: {}", target, e));
                    }
                    // Also remove marker file if present.
                    let marker = format!("{}.vigo-version", target);
                    let _ = std::fs::remove_file(&marker);
                }
            }
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let binary = params.get("binary_path").cloned().unwrap_or_default();
        let sha256 = params.get("sha256").cloned().unwrap_or_default();
        if !artifact.is_empty() && !sha256.is_empty() {
            result.error = Some(
                "source_package executor: `sha256:` is redundant with `artifact:` — \
                 the catalog entry's signed blob_sha is already verified by curator. \
                 Drop one or the other.".into(),
            );
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }
        let unless_cmd = params.get("unless").cloned().unwrap_or_default();
        let mode = params.get("mode").map(|s| s.as_str()).unwrap_or("0755");
        let version = params.get("version").cloned().unwrap_or_default();
        let owner = params.get("owner").cloned().unwrap_or_default();
        let group = params.get("group").cloned().unwrap_or_default();
        let timeout_secs: u64 = params
            .get("timeout")
            .and_then(|s| s.parse().ok())
            .unwrap_or(300);
        let strip_components: u32 = params
            .get("strip_components")
            .and_then(|s| s.parse().ok())
            .unwrap_or(0);

        // 1. Version marker check — fastest idempotency, runs before anything else.
        if !version.is_empty() {
            let marker_path = format!("{}.vigo-version", target);
            if let Ok(existing) = std::fs::read_to_string(&marker_path) {
                if existing.trim() == version {
                    result.action = Action::None;
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }
            }
        }

        // 2. Check unless guard.
        if !unless_cmd.is_empty() {
            if let Ok(o) = self.runner.run("sh", &["-c", &unless_cmd]) {
                if o.status.success() {
                    result.action = Action::None;
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }
            }
        }

        // 3. Check if file exists with correct hash.
        if std::path::Path::new(&target).exists() && !sha256.is_empty() {
            if let Ok(hash) = file_sha256(&target) {
                if hash == sha256 {
                    result.action = Action::None;
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }
            }
        }

        // 4. Disk pre-flight: check available space on the target filesystem.
        if let Err(e) = check_disk_space(&target) {
            result.error = Some(e);
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        result.action = if std::path::Path::new(&target).exists() {
            Action::Updated
        } else {
            Action::Created
        };
        result.changed = true;

        if dry_run {
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        // Resolve the source file's local path + archive format. URL path
        // downloads to a per-cycle tmp dir and detects format from URL
        // extension. `artifact:` path reuses the curator-materialized file
        // (persistent under /var/lib/vigo/artifacts/, no cleanup) and
        // sniffs format from magic bytes — the materialized file has no
        // extension by design.
        let tmp_dir = format!("/tmp/vigo-archive-{}", uuid::Uuid::new_v4());
        if let Err(e) = std::fs::create_dir_all(&tmp_dir) {
            result.error = Some(format!("create temp dir: {}", e));
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let (source_path, archive_format): (String, ArchiveFormat) = if !artifact.is_empty() {
            let resolver = match super::super::swarm::curator::executor_resolver::build_resolver_from_agent_config() {
                Ok(r) => r,
                Err(e) => {
                    let _ = std::fs::remove_dir_all(&tmp_dir);
                    result.error = Some(format!(
                        "source_package executor: cannot construct curator resolver: {e:#}"
                    ));
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }
            };
            let resolved = match super::super::swarm::curator::executor_resolver::resolve_artifact_param(&artifact, &resolver) {
                Ok(r) => r,
                Err(e) => {
                    let _ = std::fs::remove_dir_all(&tmp_dir);
                    result.error = Some(format!(
                        "source_package executor: resolving `artifact: {artifact}`: {e:#}"
                    ));
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }
            };
            let path = resolved.path.to_string_lossy().to_string();
            let fmt = archive_format_from_magic(&path);
            (path, fmt)
        } else {
            let filename = match sanitize_url_filename(&url) {
                Ok(f) => f,
                Err(e) => {
                    let _ = std::fs::remove_dir_all(&tmp_dir);
                    result.error = Some(e);
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }
            };
            let download_path = format!("{}/{}", tmp_dir, filename);
            let config = DownloadConfig {
                url: &url,
                dest: &download_path,
                timeout_secs,
                ..DownloadConfig::default()
            };
            if let Err(e) = download_with_retry(&*self.runner, &config) {
                let _ = std::fs::remove_dir_all(&tmp_dir);
                result.error = Some(e);
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
            let fmt = archive_format_from_url(&download_path);
            (download_path, fmt)
        };

        // Extract dir is always under our tmp_dir even for the artifact
        // path — we don't extract into /var/lib/vigo/artifacts/.
        let extract_dir = format!("{}/extracted", tmp_dir);
        let _ = std::fs::create_dir_all(&extract_dir);

        let strip_str = strip_components.to_string();

        let extract_result = match archive_format {
            ArchiveFormat::TarGz | ArchiveFormat::TarBz2 | ArchiveFormat::TarXz => {
                let flag = match archive_format {
                    ArchiveFormat::TarGz => "-xzf",
                    ArchiveFormat::TarBz2 => "-xjf",
                    ArchiveFormat::TarXz => "-xJf",
                    _ => unreachable!(),
                };
                let mut args = vec![flag, &source_path, "-C", &extract_dir];
                if strip_components > 0 {
                    args.push("--strip-components");
                    args.push(&strip_str);
                }
                self.runner.run("tar", &args)
            }
            ArchiveFormat::Zip => {
                let zip_result = self.runner.run("unzip", &["-o", &source_path, "-d", &extract_dir]);
                // strip_components for zip requires post-extraction move.
                if strip_components > 0 {
                    if let Ok(ref o) = zip_result {
                        if o.status.success() {
                            if let Err(e) = zip_strip_components(&extract_dir, strip_components) {
                                let _ = std::fs::remove_dir_all(&tmp_dir);
                                result.error = Some(format!("strip_components on zip: {}", e));
                                result.duration_ms = start.elapsed().as_millis() as i32;
                                return result;
                            }
                        }
                    }
                }
                zip_result
            }
            ArchiveFormat::Raw => {
                // Not an archive — copy the source straight to target.
                if let Err(e) = std::fs::copy(&source_path, &target) {
                    let _ = std::fs::remove_dir_all(&tmp_dir);
                    result.error = Some(format!("copy {} to {}: {}", source_path, target, e));
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }
                let _ = std::fs::remove_dir_all(&tmp_dir);
                set_file_mode(&target, mode);
                apply_owner_group(&target, &owner, &group, &mut result);
                verify_sha256(&target, &sha256, &mut result);
                if result.error.is_none() {
                    write_version_marker(&target, &version);
                }
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
        };

        match extract_result {
            Ok(o) if !o.status.success() => {
                let stderr = String::from_utf8_lossy(&o.stderr);
                let _ = std::fs::remove_dir_all(&tmp_dir);
                result.error = Some(format!("extract archive: {}", stderr));
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
            Err(e) => {
                let _ = std::fs::remove_dir_all(&tmp_dir);
                result.error = Some(format!("extract archive: {}", e));
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
            _ => {}
        }

        // Find the target binary in extracted files.
        let source_file = if !binary.is_empty() {
            find_file_recursive(&extract_dir, &binary)
        } else {
            find_first_executable(&extract_dir)
        };

        match source_file {
            Some(src) => {
                // Verify the extracted binary's hash BEFORE installing it —
                // a mismatched binary must never reach the target path (it
                // used to be copied + chmod/chowned first, then verified, so
                // a bad binary landed installed). Matches update::apply's
                // verify-then-write order. (src and target are byte-identical
                // once copied, so hashing src is equivalent for the match
                // case and strictly safer for the mismatch case.)
                verify_sha256(&src, &sha256, &mut result);
                if result.error.is_some() {
                    let _ = std::fs::remove_dir_all(&tmp_dir);
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                // Ensure parent dir exists.
                if let Some(parent) = std::path::Path::new(&target).parent() {
                    let _ = std::fs::create_dir_all(parent);
                }

                if let Err(e) = std::fs::copy(&src, &target) {
                    let _ = std::fs::remove_dir_all(&tmp_dir);
                    result.error = Some(format!("copy {} to {}: {}", src, target, e));
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }
                set_file_mode(&target, mode);
                apply_owner_group(&target, &owner, &group, &mut result);
                if result.error.is_none() {
                    write_version_marker(&target, &version);
                }
            }
            None => {
                result.error = Some("source_package executor: no suitable file found in extracted archive".into());
            }
        }

        let _ = std::fs::remove_dir_all(&tmp_dir);
        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}

fn file_sha256(path: &str) -> Result<String, std::io::Error> {
    let mut f = std::fs::File::open(path)?;
    let mut hasher = Sha256::new();
    let mut buf = [0u8; 8192];
    loop {
        let n = f.read(&mut buf)?;
        if n == 0 {
            break;
        }
        hasher.update(&buf[..n]);
    }
    Ok(hasher
        .finalize()
        .iter()
        .map(|b| format!("{:02x}", b))
        .collect())
}

fn verify_sha256(path: &str, expected: &str, result: &mut ResourceResult) {
    if expected.is_empty() {
        return;
    }
    match file_sha256(path) {
        Ok(hash) if hash == expected => {}
        Ok(hash) => {
            result.error = Some(format!(
                "sha256 mismatch: expected {}, got {}",
                expected, hash
            ));
        }
        Err(e) => {
            result.error = Some(format!("hash {}: {}", path, e));
        }
    }
}

fn set_file_mode(path: &str, mode: &str) {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        if let Ok(m) = u32::from_str_radix(mode.trim_start_matches('0'), 8) {
            let _ = std::fs::set_permissions(path, std::fs::Permissions::from_mode(m));
        }
    }
}

/// Apply owner/group to the target file using the shared ownership helpers from file.rs.
fn apply_owner_group(path: &str, owner: &str, group: &str, result: &mut ResourceResult) {
    #[cfg(unix)]
    {
        use super::file::{apply_ownership, lookup_gid, lookup_uid};

        let uid = if !owner.is_empty() {
            match lookup_uid(owner) {
                Some(u) => Some(u),
                None => {
                    result.error = Some(format!("source_package executor: unknown user: {}", owner));
                    return;
                }
            }
        } else {
            None
        };

        let gid = if !group.is_empty() {
            match lookup_gid(group) {
                Some(g) => Some(g),
                None => {
                    result.error = Some(format!("source_package executor: unknown group: {}", group));
                    return;
                }
            }
        } else {
            None
        };

        if uid.is_some() || gid.is_some() {
            if let Err(e) = apply_ownership(path, uid, gid) {
                result.error = Some(format!("source_package executor: {}", e));
            }
        }
    }
    #[cfg(not(unix))]
    {
        let _ = (path, owner, group, result);
    }
}

/// Write a version marker file next to the target, if a version was specified.
fn write_version_marker(target: &str, version: &str) {
    if !version.is_empty() {
        let marker_path = format!("{}.vigo-version", target);
        let _ = std::fs::write(&marker_path, version);
    }
}

/// Check available disk space on the filesystem containing the target path.
/// Returns an error if less than MIN_DISK_SPACE is available.
fn check_disk_space(target: &str) -> Result<(), String> {
    #[cfg(unix)]
    {
        // Find the closest existing ancestor to statvfs against.
        let check_path = closest_existing_ancestor(target);
        match nix::sys::statvfs::statvfs(check_path.as_str()) {
            Ok(stat) => {
                let avail = stat.blocks_available() * stat.fragment_size();
                if avail < MIN_DISK_SPACE {
                    let avail_mb = avail / (1024 * 1024);
                    return Err(format!(
                        "source_package executor: insufficient disk space on target filesystem ({} MB available, need at least {} MB)",
                        avail_mb,
                        MIN_DISK_SPACE / (1024 * 1024),
                    ));
                }
                Ok(())
            }
            Err(e) => {
                // If we can't check, proceed rather than blocking (e.g., path doesn't exist yet).
                tracing::warn!(path = target, "disk space check failed (proceeding): {}", e);
                Ok(())
            }
        }
    }
    #[cfg(not(unix))]
    {
        let _ = target;
        Ok(())
    }
}

/// Walk up from a path until we find a directory that exists, for statvfs.
#[cfg(unix)]
fn closest_existing_ancestor(path: &str) -> String {
    let mut p = std::path::PathBuf::from(path);
    loop {
        if p.exists() {
            return p.to_string_lossy().to_string();
        }
        if !p.pop() {
            return "/".to_string();
        }
    }
}

/// For zip archives: walk N levels deep and move contents up to the extract root.
fn zip_strip_components(extract_dir: &str, levels: u32) -> Result<(), String> {
    let base = std::path::PathBuf::from(extract_dir);

    // Find the directory that is `levels` deep. With strip_components=1, we want
    // to move the contents of the single top-level directory up to extract_dir.
    // Walk down `levels` times, expecting exactly one directory at each level.
    let mut current = base.clone();
    for depth in 0..levels {
        let entries: Vec<_> = std::fs::read_dir(&current)
            .map_err(|e| format!("read dir at depth {}: {}", depth, e))?
            .filter_map(|e| e.ok())
            .collect();

        // Find the first directory entry to descend into.
        let subdir = entries
            .iter()
            .find(|e| e.path().is_dir())
            .ok_or_else(|| {
                format!(
                    "strip_components={}: no directory found at depth {}",
                    levels, depth
                )
            })?;
        current = subdir.path();
    }

    // Move all contents from `current` up to `base`.
    let staging = base.join(".vigo-strip-staging");
    std::fs::rename(&current, &staging)
        .map_err(|e| format!("move stripped dir to staging: {}", e))?;

    // Remove everything else in base except staging.
    for entry in std::fs::read_dir(&base)
        .map_err(|e| format!("read extract dir: {}", e))?
        .filter_map(|e| e.ok())
    {
        if entry.path() == staging {
            continue;
        }
        let p = entry.path();
        if p.is_dir() {
            let _ = std::fs::remove_dir_all(&p);
        } else {
            let _ = std::fs::remove_file(&p);
        }
    }

    // Move staging contents into base.
    for entry in std::fs::read_dir(&staging)
        .map_err(|e| format!("read staging dir: {}", e))?
        .filter_map(|e| e.ok())
    {
        let src = entry.path();
        let dest = base.join(entry.file_name());
        std::fs::rename(&src, &dest)
            .map_err(|e| format!("move {} to {}: {}", src.display(), dest.display(), e))?;
    }
    let _ = std::fs::remove_dir_all(&staging);

    Ok(())
}

fn find_file_recursive(dir: &str, name: &str) -> Option<String> {
    for entry in walkdir(dir) {
        if let Some(fname) = std::path::Path::new(&entry).file_name() {
            if fname.to_string_lossy() == name {
                return Some(entry);
            }
        }
    }
    None
}

fn find_first_executable(dir: &str) -> Option<String> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        for entry in walkdir(dir) {
            if let Ok(meta) = std::fs::metadata(&entry) {
                if meta.is_file() && meta.permissions().mode() & 0o111 != 0 {
                    return Some(entry);
                }
            }
        }
    }
    #[cfg(not(unix))]
    {
        for entry in walkdir(dir) {
            if std::path::Path::new(&entry).is_file() {
                return Some(entry);
            }
        }
    }
    None
}

fn walkdir(dir: &str) -> Vec<String> {
    let mut files = Vec::new();
    if let Ok(entries) = std::fs::read_dir(dir) {
        for entry in entries.flatten() {
            let path = entry.path();
            if path.is_dir() {
                files.extend(walkdir(path.to_str().unwrap_or("")));
            } else {
                files.push(path.to_string_lossy().to_string());
            }
        }
    }
    files
}

#[cfg(test)]
#[cfg(unix)]
mod tests {
    use super::*;
    use crate::executors::test_support::mock::*;
    use std::sync::Arc;

    #[test]
    fn missing_url_errors() {
        let mock = MockRunner::new(vec![]);
        let exec = SourcePackageExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(&make_params(&[]), false);
        assert!(result.error.is_some());
        let msg = result.error.as_deref().unwrap();
        // Slice 6: error name covers both source options now.
        assert!(
            msg.contains("`url:` or `artifact:`"),
            "unexpected error: {:?}",
            msg
        );
    }

    #[test]
    fn url_and_artifact_together_errors() {
        let mock = MockRunner::new(vec![]);
        let exec = SourcePackageExecutor { runner: Box::new(mock) };
        let result = exec.execute(
            &make_params(&[
                ("url", "https://example.com/x.tar.gz"),
                ("artifact", "alexander4/x:latest"),
                ("target_path", "/usr/local/bin/x"),
            ]),
            false,
        );
        assert!(result.error.is_some());
        assert!(
            result.error.as_deref().unwrap().contains("mutually exclusive"),
            "unexpected error: {:?}",
            result.error
        );
    }

    #[test]
    fn sha256_alongside_artifact_errors() {
        let mock = MockRunner::new(vec![]);
        let exec = SourcePackageExecutor { runner: Box::new(mock) };
        let result = exec.execute(
            &make_params(&[
                ("artifact", "alexander4/x:latest"),
                ("target_path", "/usr/local/bin/x"),
                ("sha256", "abc123"),
            ]),
            false,
        );
        assert!(result.error.is_some());
        assert!(
            result.error.as_deref().unwrap().contains("redundant with `artifact:`"),
            "unexpected error: {:?}",
            result.error
        );
    }

    #[test]
    fn archive_format_from_url_recognizes_extensions() {
        assert_eq!(archive_format_from_url("/tmp/x.tar.gz"), ArchiveFormat::TarGz);
        assert_eq!(archive_format_from_url("/tmp/x.tgz"), ArchiveFormat::TarGz);
        assert_eq!(archive_format_from_url("/tmp/x.TAR.GZ"), ArchiveFormat::TarGz, "case-insensitive");
        assert_eq!(archive_format_from_url("/tmp/x.tar.bz2"), ArchiveFormat::TarBz2);
        assert_eq!(archive_format_from_url("/tmp/x.tar.xz"), ArchiveFormat::TarXz);
        assert_eq!(archive_format_from_url("/tmp/x.zip"), ArchiveFormat::Zip);
        assert_eq!(archive_format_from_url("/tmp/binary-no-extension"), ArchiveFormat::Raw);
    }

    #[test]
    fn archive_format_from_magic_sniffs_compression_layers() {
        use std::io::Write;
        let cases: &[(&str, &[u8], ArchiveFormat)] = &[
            ("gz", &[0x1f, 0x8b, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00], ArchiveFormat::TarGz),
            ("bz2", &[0x42, 0x5a, 0x68, 0x39, 0x31, 0x41, 0x59, 0x26], ArchiveFormat::TarBz2),
            ("xz", &[0xfd, 0x37, 0x7a, 0x58, 0x5a, 0x00, 0x00, 0x04], ArchiveFormat::TarXz),
            ("zip", &[0x50, 0x4b, 0x03, 0x04, 0x14, 0x00, 0x00, 0x00], ArchiveFormat::Zip),
        ];
        for (name, magic, expected) in cases {
            let dir = tempfile::tempdir().unwrap();
            let p = dir.path().join(name);
            std::fs::File::create(&p).unwrap().write_all(magic).unwrap();
            assert_eq!(archive_format_from_magic(p.to_str().unwrap()), *expected, "{name}");
        }
    }

    #[test]
    fn archive_format_from_magic_unknown_falls_to_raw() {
        use std::io::Write;
        let dir = tempfile::tempdir().unwrap();
        let p = dir.path().join("plain");
        // ELF header — not an archive, must be treated as raw binary.
        std::fs::File::create(&p)
            .unwrap()
            .write_all(&[0x7f, 0x45, 0x4c, 0x46, 0x02, 0x01, 0x01, 0x00])
            .unwrap();
        assert_eq!(archive_format_from_magic(p.to_str().unwrap()), ArchiveFormat::Raw);

        // Nonexistent file → Raw (lets the executor's copy-to-target
        // branch surface the actual filesystem error).
        assert_eq!(archive_format_from_magic("/no/such/path"), ArchiveFormat::Raw);

        // Empty file → Raw.
        let empty = dir.path().join("empty");
        std::fs::File::create(&empty).unwrap();
        assert_eq!(archive_format_from_magic(empty.to_str().unwrap()), ArchiveFormat::Raw);
    }

    #[test]
    fn missing_targetpath_errors() {
        let mock = MockRunner::new(vec![]);
        let exec = SourcePackageExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("url", "https://example.com/file.tar.gz")]),
            false,
        );
        assert!(result.error.is_some());
        assert!(
            result.error.as_deref().unwrap().contains("target_path param is required"),
            "unexpected error: {:?}",
            result.error
        );
    }

    #[test]
    fn absent_removes_file() {
        let dir = tempfile::TempDir::new().unwrap();
        let target = dir.path().join("mybin");
        std::fs::write(&target, b"binary content").unwrap();
        assert!(target.exists());

        let mock = MockRunner::new(vec![]);
        let exec = SourcePackageExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("url", "https://example.com/file.tar.gz"),
                ("target_path", target.to_str().unwrap()),
                ("state", "absent"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::Deleted);
        assert!(result.changed);
        assert!(result.error.is_none());
        assert!(!target.exists(), "file should have been removed");
    }

    #[test]
    fn absent_removes_version_marker() {
        let dir = tempfile::TempDir::new().unwrap();
        let target = dir.path().join("mybin");
        std::fs::write(&target, b"binary content").unwrap();
        let marker = dir.path().join("mybin.vigo-version");
        std::fs::write(&marker, "1.0.0").unwrap();
        assert!(marker.exists());

        let mock = MockRunner::new(vec![]);
        let exec = SourcePackageExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("url", "https://example.com/file.tar.gz"),
                ("target_path", target.to_str().unwrap()),
                ("state", "absent"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::Deleted);
        assert!(!target.exists());
        assert!(!marker.exists(), "marker file should have been removed");
    }

    #[test]
    fn absent_noop_when_missing() {
        let dir = tempfile::TempDir::new().unwrap();
        let target = dir.path().join("nonexistent");

        let mock = MockRunner::new(vec![]);
        let exec = SourcePackageExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("url", "https://example.com/file.tar.gz"),
                ("target_path", target.to_str().unwrap()),
                ("state", "absent"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());
    }

    #[test]
    fn dry_run_does_not_download() {
        let dir = tempfile::TempDir::new().unwrap();
        let target = dir.path().join("mybin");

        let mock = MockRunner::new(vec![]);
        let calls = Arc::clone(&mock.calls);
        let exec = SourcePackageExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("url", "https://example.com/file.tar.gz"),
                ("target_path", target.to_str().unwrap()),
            ]),
            true,
        );
        assert_eq!(result.action, Action::Created);
        assert!(result.changed);
        assert!(result.error.is_none());
        // No curl/wget calls
        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 0, "dry_run should not call any commands, got {:?}", *calls);
    }

    #[test]
    fn verify_sha256_gate() {
        // The install path verifies the extracted binary via this gate
        // BEFORE copying it to target; a mismatch sets result.error, which
        // the caller checks to bail before the copy. Pin all three arms.
        let dir = tempfile::TempDir::new().unwrap();
        let f = dir.path().join("bin");
        std::fs::write(&f, b"the real bytes").unwrap();
        let path = f.to_str().unwrap();
        let good = file_sha256(path).unwrap();

        // Match → no error.
        let mut r = ResourceResult::new("source_package", "t");
        verify_sha256(path, &good, &mut r);
        assert!(r.error.is_none());

        // Mismatch → error (the reorder relies on this to skip install).
        let mut r = ResourceResult::new("source_package", "t");
        verify_sha256(path, "0000000000000000000000000000000000000000000000000000000000000000", &mut r);
        assert!(r.error.as_deref().unwrap().contains("sha256 mismatch"));

        // Empty expected → skip (no error).
        let mut r = ResourceResult::new("source_package", "t");
        verify_sha256(path, "", &mut r);
        assert!(r.error.is_none());
    }

    #[test]
    fn sha256_match_skips_download() {
        let dir = tempfile::TempDir::new().unwrap();
        let target = dir.path().join("mybin");
        let content = b"known binary content for hash test";
        std::fs::write(&target, content).unwrap();

        // Compute the actual sha256 of the content
        let hash = file_sha256(target.to_str().unwrap()).unwrap();

        let mock = MockRunner::new(vec![]);
        let exec = SourcePackageExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("url", "https://example.com/file.tar.gz"),
                ("target_path", target.to_str().unwrap()),
                ("sha256", &hash),
            ]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());
    }

    #[test]
    fn unless_guard_skips() {
        let dir = tempfile::TempDir::new().unwrap();
        let target = dir.path().join("mybin");

        // unless command succeeds -> skip
        let mock = MockRunner::new(vec![
            ok_output(""),  // sh -c "unless_cmd" succeeds
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = SourcePackageExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("url", "https://example.com/file.tar.gz"),
                ("target_path", target.to_str().unwrap()),
                ("unless", "test -f /usr/bin/something"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());
        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1);
        assert_eq!(calls[0].0, "sh");
    }

    #[test]
    fn version_marker_skips_when_matching() {
        let dir = tempfile::TempDir::new().unwrap();
        let target = dir.path().join("consul");
        std::fs::write(&target, b"binary").unwrap();
        let marker = dir.path().join("consul.vigo-version");
        std::fs::write(&marker, "1.5.0").unwrap();

        let mock = MockRunner::new(vec![]);
        let calls = Arc::clone(&mock.calls);
        let exec = SourcePackageExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("url", "https://example.com/consul_1.5.0.zip"),
                ("target_path", target.to_str().unwrap()),
                ("version", "1.5.0"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());
        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 0, "version match should skip all commands");
    }

    #[test]
    fn version_marker_proceeds_when_different() {
        let dir = tempfile::TempDir::new().unwrap();
        let target = dir.path().join("consul");
        std::fs::write(&target, b"binary").unwrap();
        let marker = dir.path().join("consul.vigo-version");
        std::fs::write(&marker, "1.4.0").unwrap();

        // Version mismatch: should proceed to download (which will fail with no mock responses,
        // but we just check that it didn't short-circuit).
        let mock = MockRunner::new(vec![
            fail_output("curl: not found"), // curl fails
            fail_output("wget: not found"), // wget fails
        ]);
        let exec = SourcePackageExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("url", "https://example.com/consul_1.5.0.zip"),
                ("target_path", target.to_str().unwrap()),
                ("version", "1.5.0"),
            ]),
            false,
        );
        // Should have attempted download (and failed in test env), not returned Action::None.
        assert!(result.changed || result.error.is_some());
    }

    #[test]
    fn version_marker_checked_before_unless() {
        let dir = tempfile::TempDir::new().unwrap();
        let target = dir.path().join("consul");
        std::fs::write(&target, b"binary").unwrap();
        let marker = dir.path().join("consul.vigo-version");
        std::fs::write(&marker, "1.5.0").unwrap();

        // Provide an unless guard — but version marker should short-circuit first,
        // so the unless command should never run.
        let mock = MockRunner::new(vec![]);
        let calls = Arc::clone(&mock.calls);
        let exec = SourcePackageExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("url", "https://example.com/consul_1.5.0.zip"),
                ("target_path", target.to_str().unwrap()),
                ("version", "1.5.0"),
                ("unless", "consul version | grep 1.5.0"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::None);
        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 0, "version match should skip unless guard");
    }

    #[test]
    fn timeout_passed_to_download() {
        let dir = tempfile::TempDir::new().unwrap();
        let target = dir.path().join("mybin");

        // download_with_retry does: attempt 0 (curl + wget fallback),
        // then retry attempts with exponential backoff. Provide enough
        // responses for curl fail + wget fail for each attempt (3 total = 6 responses).
        let mock = MockRunner::new(vec![
            fail_output("404 Not Found"), // curl attempt 0
            fail_output("404 Not Found"), // wget attempt 0 (not transient, so no retry)
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = SourcePackageExecutor {
            runner: Box::new(mock),
        };
        let _result = exec.execute(
            &make_params(&[
                ("url", "https://example.com/file.tar.gz"),
                ("target_path", target.to_str().unwrap()),
                ("timeout", "60"),
            ]),
            false,
        );
        let calls = calls.lock().unwrap();
        // First call should be curl with --max-time 60.
        assert!(!calls.is_empty());
        assert_eq!(calls[0].0, "curl");
        assert!(
            calls[0].1.contains(&"60".to_string()),
            "expected timeout 60 in curl args: {:?}",
            calls[0].1
        );
    }

    #[test]
    fn strip_components_tar() {
        let dir = tempfile::TempDir::new().unwrap();
        let target = dir.path().join("mybin");

        let mock = MockRunner::new(vec![
            fail_output("curl: not found"), // curl fails
            fail_output("wget: not found"), // wget fails
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = SourcePackageExecutor {
            runner: Box::new(mock),
        };
        let _result = exec.execute(
            &make_params(&[
                ("url", "https://example.com/file.tar.gz"),
                ("target_path", target.to_str().unwrap()),
                ("strip_components", "1"),
            ]),
            false,
        );
        // Download will fail, but check that curl was called (meaning we got past pre-flight).
        let calls = calls.lock().unwrap();
        assert!(!calls.is_empty());
        assert_eq!(calls[0].0, "curl");
    }

    #[test]
    fn disk_space_check_passes_on_tmpdir() {
        // Test that disk space check works on a real filesystem (tmpdir should have > 100MB).
        let dir = tempfile::TempDir::new().unwrap();
        let target = dir.path().join("mybin");
        let result = check_disk_space(target.to_str().unwrap());
        assert!(result.is_ok(), "disk space check should pass on tmpdir: {:?}", result);
    }

    #[test]
    fn zip_strip_components_moves_contents() {
        let dir = tempfile::TempDir::new().unwrap();
        let extract_dir = dir.path().join("extracted");
        std::fs::create_dir_all(&extract_dir).unwrap();

        // Simulate a zip with a single top-level dir: extracted/myapp-1.0/binary
        let subdir = extract_dir.join("myapp-1.0");
        std::fs::create_dir_all(&subdir).unwrap();
        std::fs::write(subdir.join("binary"), b"test").unwrap();
        std::fs::write(subdir.join("README"), b"readme").unwrap();

        zip_strip_components(extract_dir.to_str().unwrap(), 1).unwrap();

        // After stripping, "binary" and "README" should be directly in extract_dir.
        assert!(extract_dir.join("binary").exists(), "binary should be at top level");
        assert!(extract_dir.join("README").exists(), "README should be at top level");
        assert!(!extract_dir.join("myapp-1.0").exists(), "nested dir should be gone");
    }

    #[test]
    fn closest_existing_ancestor_finds_parent() {
        let dir = tempfile::TempDir::new().unwrap();
        let deep_path = dir.path().join("a").join("b").join("c").join("target");
        let result = closest_existing_ancestor(deep_path.to_str().unwrap());
        assert_eq!(result, dir.path().to_str().unwrap());
    }
}
