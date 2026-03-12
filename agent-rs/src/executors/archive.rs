// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Archive executor — downloads and extracts archives idempotently.

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use sha2::{Digest, Sha256};
use std::collections::HashMap;
use std::io::Read;
use std::time::Instant;

pub struct ArchiveExecutor {
    pub runner: Box<dyn CommandRunner>,
}

impl ArchiveExecutor {
    pub fn new() -> Self {
        Self {
            runner: Box::new(DefaultRunner),
        }
    }
}

impl Executor for ArchiveExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let url = params.get("url").cloned().unwrap_or_default();
        let target = params
            .get("targetpath")
            .cloned()
            .unwrap_or_default();

        let name = if !target.is_empty() { &target } else { &url };
        let mut result = ResourceResult::new("archive", name);

        if url.is_empty() {
            result.error = Some("archive executor: url param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }
        if target.is_empty() {
            result.error = Some("archive executor: targetpath param is required".into());
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
                }
            }
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let binary = params.get("binary").cloned().unwrap_or_default();
        let sha256 = params.get("sha256").cloned().unwrap_or_default();
        let unless_cmd = params.get("unless").cloned().unwrap_or_default();
        let mode = params.get("mode").map(|s| s.as_str()).unwrap_or("0755");

        // Check unless guard.
        if !unless_cmd.is_empty() {
            if let Ok(o) = self.runner.run("sh", &["-c", &unless_cmd]) {
                if o.status.success() {
                    result.action = Action::None;
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }
            }
        }

        // Check if file exists with correct hash.
        if std::path::Path::new(&target).exists() && !sha256.is_empty() {
            if let Ok(hash) = file_sha256(&target) {
                if hash == sha256 {
                    result.action = Action::None;
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }
            }
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

        // Download to temp dir.
        let tmp_dir = format!("/tmp/hobnob-archive-{}", uuid::Uuid::new_v4());
        if let Err(e) = std::fs::create_dir_all(&tmp_dir) {
            result.error = Some(format!("create temp dir: {}", e));
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let filename = url.rsplit('/').next().unwrap_or("download");
        let download_path = format!("{}/{}", tmp_dir, filename);

        // Download.
        match self.runner.run("curl", &["-fsSL", "-o", &download_path, &url]) {
            Ok(o) if !o.status.success() => {
                // Try wget.
                match self.runner.run("wget", &["-qO", &download_path, &url]) {
                    Ok(o2) if !o2.status.success() => {
                        let _ = std::fs::remove_dir_all(&tmp_dir);
                        result.error = Some(format!("failed to download {}", url));
                        result.duration_ms = start.elapsed().as_millis() as i32;
                        return result;
                    }
                    Err(e) => {
                        let _ = std::fs::remove_dir_all(&tmp_dir);
                        result.error = Some(format!("download {}: {}", url, e));
                        result.duration_ms = start.elapsed().as_millis() as i32;
                        return result;
                    }
                    _ => {}
                }
            }
            Err(e) => {
                let _ = std::fs::remove_dir_all(&tmp_dir);
                result.error = Some(format!("download {}: {}", url, e));
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
            _ => {}
        }

        // Detect format and extract.
        let extract_dir = format!("{}/extracted", tmp_dir);
        let _ = std::fs::create_dir_all(&extract_dir);

        let dl_lower = download_path.to_lowercase();
        let extract_result = if dl_lower.ends_with(".tar.gz") || dl_lower.ends_with(".tgz") {
            self.runner.run("tar", &["-xzf", &download_path, "-C", &extract_dir])
        } else if dl_lower.ends_with(".tar.bz2") {
            self.runner.run("tar", &["-xjf", &download_path, "-C", &extract_dir])
        } else if dl_lower.ends_with(".tar.xz") {
            self.runner.run("tar", &["-xJf", &download_path, "-C", &extract_dir])
        } else if dl_lower.ends_with(".zip") {
            self.runner.run("unzip", &["-o", &download_path, "-d", &extract_dir])
        } else if dl_lower.ends_with(".deb") {
            self.runner.run("dpkg", &["-i", &download_path])
        } else if dl_lower.ends_with(".rpm") {
            self.runner.run("rpm", &["-i", &download_path])
        } else {
            // Unknown format — just copy the downloaded file.
            if let Err(e) = std::fs::copy(&download_path, &target) {
                let _ = std::fs::remove_dir_all(&tmp_dir);
                result.error = Some(format!("copy {} to {}: {}", download_path, target, e));
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
            let _ = std::fs::remove_dir_all(&tmp_dir);
            set_file_mode(&target, mode);
            verify_sha256(&target, &sha256, &mut result);
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
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

        // For deb/rpm, the file is installed directly.
        if dl_lower.ends_with(".deb") || dl_lower.ends_with(".rpm") {
            let _ = std::fs::remove_dir_all(&tmp_dir);
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        // Find the target binary in extracted files.
        let source_file = if !binary.is_empty() {
            find_file_recursive(&extract_dir, &binary)
        } else {
            find_first_executable(&extract_dir)
        };

        match source_file {
            Some(src) => {
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
                verify_sha256(&target, &sha256, &mut result);
            }
            None => {
                result.error = Some("archive executor: no suitable file found in extracted archive".into());
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
