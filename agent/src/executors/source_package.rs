// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Source package executor — downloads and extracts archives idempotently.

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use sha2::{Digest, Sha256};
use std::collections::HashMap;
use std::io::Read;
use std::time::Instant;

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

impl Executor for SourcePackageExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let source = params.get("source").cloned().unwrap_or_default();
        let url = params.get("url").cloned().unwrap_or_default();
        let target = params
            .get("target_path")
            .cloned()
            .unwrap_or_default();

        // Determine the blob SHA256 from source: "blob:sha256:<hex>" if present.
        let blob_sha = source
            .strip_prefix("blob:sha256:")
            .filter(|s| s.len() == 64)
            .map(|s| s.to_string());

        let effective_name = if !target.is_empty() {
            target.clone()
        } else if blob_sha.is_some() {
            source.clone()
        } else {
            url.clone()
        };
        let name = &effective_name;
        let mut result = ResourceResult::new("source_package", name);

        if url.is_empty() && blob_sha.is_none() {
            result.error = Some("source_package executor: url or source param is required".into());
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
                }
            }
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let binary = params.get("binary_path").cloned().unwrap_or_default();
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

        // For .deb/.rpm URLs, check if the package is already installed.
        let url_lower = url.to_lowercase();
        if url_lower.ends_with(".deb") || url_lower.ends_with(".rpm") {
            if let Some(pkg_name) = package_name_from_url(&url) {
                let installed = if url_lower.ends_with(".deb") {
                    is_deb_installed(&self.runner, &pkg_name)
                } else {
                    is_rpm_installed(&self.runner, &pkg_name)
                };
                if installed {
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
        let tmp_dir = format!("/tmp/vigo-archive-{}", uuid::Uuid::new_v4());
        if let Err(e) = std::fs::create_dir_all(&tmp_dir) {
            result.error = Some(format!("create temp dir: {}", e));
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let download_path = if let Some(ref sha) = blob_sha {
            // Blob source: copy from swarm cache instead of downloading.
            let blob_path = crate::platform::paths::default_blobs_dir().join(sha);
            if !blob_path.exists() {
                let _ = std::fs::remove_dir_all(&tmp_dir);
                result.error = Some(format!(
                    "source_package executor: blob not in cache: {} (run swarm download first)",
                    &sha[..12]
                ));
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
            // Detect extension from label or url for format detection later.
            let label = params.get("label").cloned().unwrap_or_default();
            let ext_hint = if !label.is_empty() { &label } else if !url.is_empty() { &url } else { "archive.tar.gz" };
            let filename = ext_hint.rsplit('/').next().unwrap_or("archive");
            let dest = format!("{}/{}", tmp_dir, filename);
            if let Err(e) = std::fs::copy(&blob_path, &dest) {
                let _ = std::fs::remove_dir_all(&tmp_dir);
                result.error = Some(format!("copy blob to temp: {}", e));
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
            dest
        } else {
            let raw_filename = url.rsplit('/').next().unwrap_or("download");
            // Sanitize filename: reject path traversal and absolute paths.
            let filename = std::path::Path::new(raw_filename)
                .file_name()
                .and_then(|f| f.to_str())
                .unwrap_or("download");
            if filename.contains("..") || filename.starts_with('/') || filename.is_empty() {
                result.error = Some(format!("unsafe filename in URL: {}", raw_filename));
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
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
            download_path
        };

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

/// Extract the package name from a .deb or .rpm URL.
///
/// .deb convention: `name_version_arch.deb` (underscore-separated)
/// .rpm convention: `name-version-release.arch.rpm` (last three dashes are version-release-arch)
fn package_name_from_url(url: &str) -> Option<String> {
    let filename = url.rsplit('/').next()?;
    let lower = filename.to_lowercase();
    if lower.ends_with(".deb") {
        // name_version_arch.deb — split on first underscore
        let stem = &filename[..filename.len() - 4];
        let name = stem.split('_').next()?;
        if name.is_empty() {
            return None;
        }
        Some(name.to_string())
    } else if lower.ends_with(".rpm") {
        // name-version-release.arch.rpm — walk backwards past arch, release, version
        let stem = &filename[..filename.len() - 4];
        // Remove .arch suffix (e.g., ".x86_64", ".noarch")
        let stem = match stem.rfind('.') {
            Some(i) if i > 0 => &stem[..i],
            _ => stem,
        };
        // Remove -release suffix
        let stem = match stem.rfind('-') {
            Some(i) if i > 0 => &stem[..i],
            _ => return Some(stem.to_string()),
        };
        // Remove -version suffix
        let name = match stem.rfind('-') {
            Some(i) if i > 0 => &stem[..i],
            _ => stem,
        };
        if name.is_empty() {
            return None;
        }
        Some(name.to_string())
    } else {
        None
    }
}

/// Check if a .deb package is installed via dpkg -s.
fn is_deb_installed(runner: &Box<dyn CommandRunner>, name: &str) -> bool {
    if let Ok(o) = runner.run("dpkg", &["-s", name]) {
        if o.status.success() {
            let stdout = String::from_utf8_lossy(&o.stdout);
            return stdout.contains("install ok installed");
        }
    }
    false
}

/// Check if an .rpm package is installed via rpm -q.
fn is_rpm_installed(runner: &Box<dyn CommandRunner>, name: &str) -> bool {
    if let Ok(o) = runner.run("rpm", &["-q", name]) {
        return o.status.success();
    }
    false
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
        assert!(
            result.error.as_deref().unwrap().contains("url or source param is required"),
            "unexpected error: {:?}",
            result.error
        );
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
    fn package_name_from_deb_url() {
        assert_eq!(
            package_name_from_url("https://example.com/waveterm_0.9.2_amd64.deb"),
            Some("waveterm".into())
        );
        assert_eq!(
            package_name_from_url("https://example.com/google-chrome-stable_120.0_amd64.deb"),
            Some("google-chrome-stable".into())
        );
        assert_eq!(
            package_name_from_url("https://example.com/simple.deb"),
            Some("simple".into())
        );
    }

    #[test]
    fn package_name_from_rpm_url() {
        assert_eq!(
            package_name_from_url("https://example.com/nginx-1.24.0-1.el9.x86_64.rpm"),
            Some("nginx".into())
        );
        assert_eq!(
            package_name_from_url("https://example.com/google-chrome-stable-120.0-1.x86_64.rpm"),
            Some("google-chrome-stable".into())
        );
        assert_eq!(
            package_name_from_url("https://example.com/simple-1.0-1.noarch.rpm"),
            Some("simple".into())
        );
    }

    #[test]
    fn deb_installed_skips_download() {
        let dir = tempfile::TempDir::new().unwrap();
        let target = dir.path().join("placeholder");

        let mock = MockRunner::new(vec![
            ok_output("Status: install ok installed\n"), // dpkg -s waveterm
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = SourcePackageExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("url", "https://example.com/waveterm_0.9.2_amd64.deb"),
                ("target_path", target.to_str().unwrap()),
            ]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());
        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1);
        assert_eq!(calls[0].0, "dpkg");
        assert_eq!(calls[0].1, vec!["-s", "waveterm"]);
    }

    #[test]
    fn rpm_installed_skips_download() {
        let dir = tempfile::TempDir::new().unwrap();
        let target = dir.path().join("placeholder");

        let mock = MockRunner::new(vec![
            ok_output("nginx-1.24.0-1.el9.x86_64\n"), // rpm -q nginx
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = SourcePackageExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("url", "https://example.com/nginx-1.24.0-1.el9.x86_64.rpm"),
                ("target_path", target.to_str().unwrap()),
            ]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());
        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1);
        assert_eq!(calls[0].0, "rpm");
        assert_eq!(calls[0].1, vec!["-q", "nginx"]);
    }

    #[test]
    fn deb_not_installed_proceeds() {
        let dir = tempfile::TempDir::new().unwrap();
        let target = dir.path().join("placeholder");

        let mock = MockRunner::new(vec![
            fail_output("dpkg-query: no info\n"), // dpkg -s fails
            ok_output(""),                         // curl download
            ok_output(""),                         // dpkg -i install
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = SourcePackageExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("url", "https://example.com/waveterm_0.9.2_amd64.deb"),
                ("target_path", target.to_str().unwrap()),
            ]),
            false,
        );
        assert!(result.changed);
        let calls = calls.lock().unwrap();
        // dpkg -s, curl, dpkg -i
        assert!(calls.len() >= 2, "expected at least dpkg -s + curl, got {:?}", *calls);
        assert_eq!(calls[0].0, "dpkg");
        assert_eq!(calls[0].1, vec!["-s", "waveterm"]);
        assert_eq!(calls[1].0, "curl");
    }
}
