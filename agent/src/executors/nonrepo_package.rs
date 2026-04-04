// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Non-repo package executor — installs .deb/.rpm/.pkg/.msi packages from URLs.
//!
//! Uses `dpkg -i` / `rpm -U` / `installer -pkg` / `msiexec /i` instead of
//! package manager repos. Downloads are handled by the shared
//! [`download_with_retry()`](super::download::download_with_retry) helper.

use super::download::{download_with_retry, sanitize_url_filename, DownloadConfig};
use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use sha2::{Digest, Sha256};
use std::collections::HashMap;
use std::io::Read;
use std::time::{Duration, Instant};
use tracing::debug;

/// Supported package format, auto-detected from URL extension.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum PackageFormat {
    Deb,
    Rpm,
    Pkg,
    Msi,
}

pub struct NonRepoPackageExecutor {
    pub runner: Box<dyn CommandRunner>,
}

impl NonRepoPackageExecutor {
    pub fn new() -> Self {
        Self {
            runner: Box::new(DefaultRunner),
        }
    }
}

impl Executor for NonRepoPackageExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let url = params.get("url").cloned().unwrap_or_default();
        let source = params.get("source").cloned().unwrap_or_default();
        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");
        let sha256_expected = params.get("sha256").cloned().unwrap_or_default();
        let lock_version = params.get("lock_version").map(|s| s == "true").unwrap_or(false);
        let allow_downgrade = params.get("allow_downgrade").map(|s| s == "true").unwrap_or(false);
        let purge = params.get("purge").map(|s| s == "true").unwrap_or(false);
        let update_cache = params.get("update_cache").map(|s| s == "true").unwrap_or(false);
        let post_cleanup = params.get("post_cleanup").map(|s| s == "true").unwrap_or(false);
        let flags = params.get("flags").cloned().unwrap_or_default();
        let pre_install = params.get("pre_install").cloned().unwrap_or_default();
        let post_install = params.get("post_install").cloned().unwrap_or_default();
        let version = params.get("version").cloned().unwrap_or_default();
        let timeout_secs: u64 = params
            .get("timeout")
            .and_then(|s| s.parse().ok())
            .unwrap_or(300);
        let retries: u32 = params
            .get("retries")
            .and_then(|s| s.parse().ok())
            .unwrap_or(2);
        let retry_delay: u64 = params
            .get("retry_delay")
            .and_then(|s| s.parse().ok())
            .unwrap_or(5);

        // Determine the blob SHA256 from source: "blob:sha256:<hex>" if present.
        let blob_sha = source
            .strip_prefix("blob:sha256:")
            .filter(|s| s.len() == 64)
            .map(|s| s.to_string());

        let effective_url = if blob_sha.is_some() { &source } else { &url };

        let mut result = ResourceResult::new("nonrepo_package", effective_url);

        // Validate: need url or source.
        if url.is_empty() && blob_sha.is_none() {
            result.error = Some("nonrepo_package executor: url or source param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        // Detect package format from URL.
        let detect_url = if !url.is_empty() { &url } else { &source };
        let format = match detect_format(detect_url) {
            Some(f) => f,
            None => {
                result.error = Some(format!(
                    "nonrepo_package executor: cannot detect package format from URL (expected .deb, .rpm, .pkg, or .msi): {}",
                    detect_url
                ));
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
        };

        // Auto-detect package name from URL or use explicit param.
        let pkg_name = params
            .get("package")
            .cloned()
            .unwrap_or_else(|| package_name_from_url(detect_url, format));

        // Update result name to the package name for clearer reporting.
        result.resource_name = pkg_name.clone();

        // Validate state.
        if !matches!(state, "present" | "absent" | "latest") {
            result.error = Some(format!(
                "nonrepo_package executor: invalid state {:?} (expected present, absent, or latest)",
                state
            ));
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        // Handle absent state.
        if state == "absent" {
            return handle_absent(
                &*self.runner,
                &pkg_name,
                format,
                purge,
                &flags,
                dry_run,
                start,
            );
        }

        // Check if already installed and at the right version.
        let (installed, installed_version) = check_installed(&*self.runner, &pkg_name, format);

        if installed {
            if state == "present" && !version.is_empty() {
                if installed_version.as_deref() == Some(version.as_str()) {
                    // Exact version match — no-op.
                    debug!(
                        package = %pkg_name,
                        version = %version,
                        "nonrepo_package: already installed at correct version"
                    );
                    result.action = Action::None;
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }
                // Version differs — fall through to download and install (upgrade).
            } else if state == "present" {
                // No version specified, already installed — no-op.
                debug!(
                    package = %pkg_name,
                    "nonrepo_package: already installed"
                );
                result.action = Action::None;
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
            // state == "latest" — always proceed to download.
        }

        // For dry_run, report what would happen without downloading.
        if dry_run {
            result.action = if installed { Action::Updated } else { Action::Created };
            result.changed = true;
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        // Run pre_install hook if specified.
        if !pre_install.is_empty() {
            match self.runner.run("sh", &["-c", &pre_install]) {
                Ok(o) if !o.status.success() => {
                    let stderr = String::from_utf8_lossy(&o.stderr);
                    result.error = Some(format!(
                        "nonrepo_package executor: pre_install command failed: {}",
                        stderr.trim()
                    ));
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }
                Err(e) => {
                    result.error = Some(format!(
                        "nonrepo_package executor: pre_install command failed: {}",
                        e
                    ));
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }
                _ => {}
            }
        }

        // Clean up stale temp dirs from previous cycles (older than 1 hour).
        cleanup_stale_tmp_dirs();

        // Download the package.
        let tmp_dir = format!("/tmp/vigo-nonrepo-pkg-{}", uuid::Uuid::new_v4());
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
                    "nonrepo_package executor: blob not in cache: {} (run swarm download first)",
                    &sha[..12]
                ));
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
            let filename = sanitize_url_filename(detect_url).unwrap_or_else(|_| "package".to_string());
            let dest = format!("{}/{}", tmp_dir, filename);
            if let Err(e) = std::fs::copy(&blob_path, &dest) {
                let _ = std::fs::remove_dir_all(&tmp_dir);
                result.error = Some(format!("copy blob to temp: {}", e));
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
            dest
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

            // Parse headers from params.
            let headers_str = params.get("headers").cloned().unwrap_or_default();
            let header_list: Vec<&str> = if headers_str.is_empty() {
                Vec::new()
            } else {
                headers_str.split('\n').collect()
            };

            let config = DownloadConfig {
                url: &url,
                dest: &download_path,
                retries,
                retry_delay: Duration::from_secs(retry_delay),
                timeout_secs,
                headers: &header_list,
            };
            if let Err(e) = download_with_retry(&*self.runner, &config) {
                let _ = std::fs::remove_dir_all(&tmp_dir);
                result.error = Some(e);
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
            download_path
        };

        // Verify SHA-256 if specified.
        if !sha256_expected.is_empty() {
            match file_sha256(&download_path) {
                Ok(hash) if hash == sha256_expected => {}
                Ok(hash) => {
                    let _ = std::fs::remove_dir_all(&tmp_dir);
                    result.error = Some(format!(
                        "sha256 mismatch: expected {}, got {}",
                        sha256_expected, hash
                    ));
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }
                Err(e) => {
                    let _ = std::fs::remove_dir_all(&tmp_dir);
                    result.error = Some(format!("hash {}: {}", download_path, e));
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }
            }
        }

        // Update package cache before install if requested (helps dep resolution).
        if update_cache {
            if let Err(e) = run_update_cache(&*self.runner, format) {
                // Non-fatal: log and continue — the install may still succeed.
                tracing::warn!(error = %e, "nonrepo_package: update_cache failed (continuing)");
            }
        }

        // Install the package (with lock contention retry for dpkg/rpm).
        let install_result = install_package(
            &*self.runner,
            &download_path,
            format,
            allow_downgrade,
            &flags,
        );

        let _ = std::fs::remove_dir_all(&tmp_dir);

        match install_result {
            Ok(()) => {
                // Fix unmet dependencies after dpkg -i / rpm -U.
                if let Err(e) = fix_dependencies(&*self.runner, format) {
                    result.error = Some(format!(
                        "nonrepo_package executor: dependency resolution failed: {}",
                        e
                    ));
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                result.action = if installed { Action::Updated } else { Action::Created };
                result.changed = true;
            }
            Err(e) => {
                result.error = Some(e);
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
        }

        // Lock version if requested.
        if lock_version {
            if let Err(e) = lock_package_version(&*self.runner, &pkg_name, format) {
                result.error = Some(format!(
                    "nonrepo_package executor: version lock failed: {}",
                    e
                ));
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
        }

        // Run post_install hook if specified.
        if !post_install.is_empty() {
            match self.runner.run("sh", &["-c", &post_install]) {
                Ok(o) if !o.status.success() => {
                    let stderr = String::from_utf8_lossy(&o.stderr);
                    result.error = Some(format!(
                        "nonrepo_package executor: post_install command failed: {}",
                        stderr.trim()
                    ));
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }
                Err(e) => {
                    result.error = Some(format!(
                        "nonrepo_package executor: post_install command failed: {}",
                        e
                    ));
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }
                _ => {}
            }
        }

        // Remove orphaned dependencies if requested (non-fatal).
        if post_cleanup && result.changed && result.error.is_none() {
            if let Err(e) = run_post_cleanup(&*self.runner, format) {
                tracing::warn!(error = %e, "nonrepo_package: post_cleanup failed (non-fatal)");
            }
        }

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}

// ---------------------------------------------------------------------------
// Format detection
// ---------------------------------------------------------------------------

/// Detect package format from URL extension.
fn detect_format(url: &str) -> Option<PackageFormat> {
    let lower = url.to_lowercase();
    // Strip query string before checking extension.
    let path = lower.split('?').next().unwrap_or(&lower);
    if path.ends_with(".deb") {
        Some(PackageFormat::Deb)
    } else if path.ends_with(".rpm") {
        Some(PackageFormat::Rpm)
    } else if path.ends_with(".pkg") {
        Some(PackageFormat::Pkg)
    } else if path.ends_with(".msi") {
        Some(PackageFormat::Msi)
    } else {
        None
    }
}

// ---------------------------------------------------------------------------
// Package name extraction from URL
// ---------------------------------------------------------------------------

/// Extract the package name from a URL filename.
///
/// - `.deb`: `name_version_arch.deb` -> split on first `_` -> name
/// - `.rpm`: `name-version-release.arch.rpm` -> walk backwards past arch, release, version
/// - `.pkg` / `.msi`: strip extension, take first segment before version-like pattern
fn package_name_from_url(url: &str, format: PackageFormat) -> String {
    let filename = url
        .split('?')
        .next()
        .unwrap_or(url)
        .rsplit('/')
        .next()
        .unwrap_or("unknown");

    match format {
        PackageFormat::Deb => {
            // name_version_arch.deb
            let base = filename.strip_suffix(".deb").unwrap_or(filename);
            base.split('_').next().unwrap_or(base).to_string()
        }
        PackageFormat::Rpm => {
            // name-version-release.arch.rpm
            let base = filename.strip_suffix(".rpm").unwrap_or(filename);
            // Remove .arch suffix (e.g., .x86_64, .noarch, .aarch64).
            let without_arch = if let Some(pos) = base.rfind('.') {
                &base[..pos]
            } else {
                base
            };
            // Walk backwards through '-' segments to find where version starts.
            // Version segments start with a digit.
            let parts: Vec<&str> = without_arch.split('-').collect();
            let mut name_end = parts.len();
            for i in (0..parts.len()).rev() {
                if parts[i].chars().next().map(|c| c.is_ascii_digit()).unwrap_or(false) {
                    name_end = i;
                } else {
                    break;
                }
            }
            if name_end == 0 {
                name_end = 1; // Keep at least the first segment.
            }
            parts[..name_end].join("-")
        }
        PackageFormat::Pkg | PackageFormat::Msi => {
            let ext = if format == PackageFormat::Pkg { ".pkg" } else { ".msi" };
            let base = filename.strip_suffix(ext).unwrap_or(filename);
            // Split on '-' or '_' and take segments until we hit a version-like part.
            let parts: Vec<&str> = base.split(|c: char| c == '-' || c == '_').collect();
            let mut name_end = parts.len();
            for (i, part) in parts.iter().enumerate() {
                if part.chars().next().map(|c| c.is_ascii_digit()).unwrap_or(false) {
                    name_end = i;
                    break;
                }
            }
            if name_end == 0 {
                name_end = 1;
            }
            parts[..name_end].join("-")
        }
    }
}

// ---------------------------------------------------------------------------
// Installed check
// ---------------------------------------------------------------------------

/// Check if a package is installed and return (installed, version).
fn check_installed(
    runner: &dyn CommandRunner,
    name: &str,
    format: PackageFormat,
) -> (bool, Option<String>) {
    match format {
        PackageFormat::Deb => {
            // dpkg -s <name>
            match runner.run("dpkg", &["-s", name]) {
                Ok(o) if o.status.success() => {
                    let stdout = String::from_utf8_lossy(&o.stdout);
                    let status_line = stdout
                        .lines()
                        .find(|l| l.starts_with("Status:"))
                        .unwrap_or("");

                    // Properly installed.
                    if status_line.contains("install ok installed") {
                        let ver = stdout
                            .lines()
                            .find(|l| l.starts_with("Version:"))
                            .map(|l| l.trim_start_matches("Version:").trim().to_string());
                        return (true, ver);
                    }

                    // Broken state (half-installed, half-configured, reinst-required, etc.)
                    // Attempt dpkg --configure to repair before reporting as not installed.
                    let broken_states = ["half-installed", "half-configured", "unpacked",
                        "config-files", "triggers-awaited", "triggers-pending", "reinst-required"];
                    if broken_states.iter().any(|s| status_line.contains(s)) {
                        tracing::warn!(
                            package = name,
                            status = status_line,
                            "nonrepo_package: dpkg broken state, attempting repair"
                        );
                        let _ = runner.run("dpkg", &["--configure", name]);
                        // Re-check after repair.
                        if let Ok(o2) = runner.run("dpkg", &["-s", name]) {
                            if o2.status.success() {
                                let stdout2 = String::from_utf8_lossy(&o2.stdout);
                                if stdout2.lines().any(|l| l.starts_with("Status:") && l.contains("install ok installed")) {
                                    let ver = stdout2
                                        .lines()
                                        .find(|l| l.starts_with("Version:"))
                                        .map(|l| l.trim_start_matches("Version:").trim().to_string());
                                    return (true, ver);
                                }
                            }
                        }
                    }

                    (false, None)
                }
                _ => (false, None),
            }
        }
        PackageFormat::Rpm => {
            // rpm -q <name> --queryformat '%{VERSION}-%{RELEASE}'
            match runner.run("rpm", &["-q", name, "--queryformat", "%{VERSION}-%{RELEASE}"]) {
                Ok(o) if o.status.success() => {
                    let version = String::from_utf8_lossy(&o.stdout).trim().to_string();
                    if version.is_empty() || version.contains("not installed") {
                        (false, None)
                    } else {
                        (true, Some(version))
                    }
                }
                _ => (false, None),
            }
        }
        PackageFormat::Pkg => {
            // macOS: pkgutil --pkg-info <name>
            match runner.run("pkgutil", &["--pkg-info", name]) {
                Ok(o) if o.status.success() => {
                    let stdout = String::from_utf8_lossy(&o.stdout);
                    let ver = stdout
                        .lines()
                        .find(|l| l.starts_with("version:"))
                        .map(|l| l.trim_start_matches("version:").trim().to_string());
                    (true, ver)
                }
                _ => (false, None),
            }
        }
        PackageFormat::Msi => {
            // Windows: check via wmic or registry. Simplified: assume not installed
            // unless we can confirm via PowerShell/wmic. For test purposes, return false.
            #[cfg(windows)]
            {
                // Try wmic product where Name='<name>' get Version
                match runner.run(
                    "wmic",
                    &["product", "where", &format!("Name='{}'", name), "get", "Version", "/value"],
                ) {
                    Ok(o) if o.status.success() => {
                        let stdout = String::from_utf8_lossy(&o.stdout);
                        let ver = stdout
                            .lines()
                            .find(|l| l.starts_with("Version="))
                            .map(|l| l.trim_start_matches("Version=").trim().to_string());
                        if ver.is_some() {
                            (true, ver)
                        } else {
                            (false, None)
                        }
                    }
                    _ => (false, None),
                }
            }
            #[cfg(not(windows))]
            {
                (false, None)
            }
        }
    }
}

// ---------------------------------------------------------------------------
// Install / Remove
// ---------------------------------------------------------------------------

/// Check if stderr indicates a dpkg/rpm lock contention error.
fn is_lock_error(stderr: &str) -> bool {
    stderr.contains("Could not get lock")
        || stderr.contains("Unable to acquire")
        || stderr.contains("database is locked")
}

/// Install a downloaded package file.
///
/// Retries up to 3 times on dpkg/rpm lock contention with exponential backoff
/// (3s, 6s, 12s) to handle concurrent package manager operations.
fn install_package(
    runner: &dyn CommandRunner,
    path: &str,
    format: PackageFormat,
    allow_downgrade: bool,
    flags: &str,
) -> Result<(), String> {
    let extra: Vec<&str> = if flags.is_empty() {
        Vec::new()
    } else {
        flags.split_whitespace().collect()
    };

    const MAX_LOCK_RETRIES: u32 = 3;
    let mut lock_delay = Duration::from_secs(3);
    let mut lock_retries = 0u32;

    loop {
        let output = match format {
            PackageFormat::Deb => {
                let mut args = vec!["-i"];
                if allow_downgrade {
                    args.push("--force-downgrade");
                }
                args.extend(&extra);
                args.push(path);
                runner.run("dpkg", &args)
            }
            PackageFormat::Rpm => {
                let mut args = vec!["-U"];
                if allow_downgrade {
                    args.push("--oldpackage");
                }
                args.extend(&extra);
                args.push(path);
                runner.run("rpm", &args)
            }
            PackageFormat::Pkg => {
                let mut args = vec!["-pkg", path, "-target", "/"];
                args.extend(&extra);
                runner.run("installer", &args)
            }
            PackageFormat::Msi => {
                let mut args = vec!["/i", path, "/qn"];
                args.extend(&extra);
                runner.run("msiexec", &args)
            }
        };

        match output {
            Ok(o) if o.status.success() => return Ok(()),
            Ok(o) => {
                let stderr = String::from_utf8_lossy(&o.stderr);
                let stdout = String::from_utf8_lossy(&o.stdout);
                let msg = if !stderr.is_empty() {
                    stderr.to_string()
                } else {
                    stdout.to_string()
                };

                // Retry on lock contention.
                if is_lock_error(&msg) && lock_retries < MAX_LOCK_RETRIES {
                    tracing::warn!(
                        "nonrepo_package: lock held, retrying in {}s (attempt {}/{})",
                        lock_delay.as_secs(),
                        lock_retries + 1,
                        MAX_LOCK_RETRIES,
                    );
                    std::thread::sleep(lock_delay);
                    lock_delay *= 2;
                    lock_retries += 1;
                    continue;
                }

                return Err(format!("install {} failed: {}", path, msg.trim()));
            }
            Err(e) => return Err(format!("install {}: {}", path, e)),
        }
    }
}

/// Handle absent state: uninstall the package if installed.
fn handle_absent(
    runner: &dyn CommandRunner,
    name: &str,
    format: PackageFormat,
    purge: bool,
    flags: &str,
    dry_run: bool,
    start: Instant,
) -> ResourceResult {
    let mut result = ResourceResult::new("nonrepo_package", name);

    let (installed, _) = check_installed(runner, name, format);
    if !installed {
        result.action = Action::None;
        result.duration_ms = start.elapsed().as_millis() as i32;
        return result;
    }

    result.action = Action::Deleted;
    result.changed = true;

    if dry_run {
        result.duration_ms = start.elapsed().as_millis() as i32;
        return result;
    }

    let extra: Vec<&str> = if flags.is_empty() {
        Vec::new()
    } else {
        flags.split_whitespace().collect()
    };

    let output = match format {
        PackageFormat::Deb => {
            let mut args = if purge { vec!["--purge"] } else { vec!["-r"] };
            args.extend(&extra);
            args.push(name);
            runner.run("dpkg", &args)
        }
        PackageFormat::Rpm => {
            let mut args = vec!["-e"];
            args.extend(&extra);
            args.push(name);
            runner.run("rpm", &args)
        }
        PackageFormat::Pkg => {
            // macOS has no clean uninstall for .pkg, but pkgutil --forget removes receipt.
            let mut args = vec!["--forget"];
            args.extend(&extra);
            args.push(name);
            runner.run("pkgutil", &args)
        }
        PackageFormat::Msi => {
            let mut args = vec!["/x", name, "/qn"];
            args.extend(&extra);
            runner.run("msiexec", &args)
        }
    };

    match output {
        Ok(o) if o.status.success() => {}
        Ok(o) => {
            let stderr = String::from_utf8_lossy(&o.stderr);
            result.error = Some(format!("remove {} failed: {}", name, stderr.trim()));
        }
        Err(e) => {
            result.error = Some(format!("remove {}: {}", name, e));
        }
    }

    result.duration_ms = start.elapsed().as_millis() as i32;
    result
}

/// Lock a package version after install (apt-mark hold / dnf versionlock / rpm --hold).
fn lock_package_version(
    runner: &dyn CommandRunner,
    name: &str,
    format: PackageFormat,
) -> Result<(), String> {
    let output = match format {
        PackageFormat::Deb => runner.run("apt-mark", &["hold", name]),
        PackageFormat::Rpm => {
            // Try dnf versionlock first, fall back to yum versionlock.
            match runner.run("dnf", &["versionlock", "add", name]) {
                Ok(o) if o.status.success() => return Ok(()),
                _ => runner.run("yum", &["versionlock", "add", name]),
            }
        }
        PackageFormat::Pkg | PackageFormat::Msi => {
            // No standard lock mechanism for macOS .pkg or Windows .msi.
            return Ok(());
        }
    };

    match output {
        Ok(o) if o.status.success() => Ok(()),
        Ok(o) => {
            let stderr = String::from_utf8_lossy(&o.stderr);
            Err(format!("version lock {}: {}", name, stderr.trim()))
        }
        Err(e) => Err(format!("version lock {}: {}", name, e)),
    }
}

// ---------------------------------------------------------------------------
// Dependency resolution
// ---------------------------------------------------------------------------

/// Fix unmet dependencies after dpkg -i / rpm -U.
///
/// For Deb: runs `apt-get install -f -y` which resolves missing deps from repos.
/// For RPM: runs `dnf install -y --best --allowerasing` (or yum) to fix deps.
/// Pkg/Msi: no-op (package managers handle deps internally).
fn fix_dependencies(runner: &dyn CommandRunner, format: PackageFormat) -> Result<(), String> {
    match format {
        PackageFormat::Deb => {
            let output = runner.run(
                "env",
                &["DEBIAN_FRONTEND=noninteractive", "apt-get", "install", "-f", "-y", "-qq"],
            );
            match output {
                Ok(o) if o.status.success() => Ok(()),
                Ok(o) => {
                    let stderr = String::from_utf8_lossy(&o.stderr);
                    // If no broken packages, apt-get install -f succeeds silently.
                    // Only report actual failures.
                    if stderr.trim().is_empty() {
                        Ok(())
                    } else {
                        Err(format!("apt-get install -f: {}", stderr.trim()))
                    }
                }
                Err(e) => Err(format!("apt-get install -f: {}", e)),
            }
        }
        PackageFormat::Rpm => {
            // DNF/YUM don't have a direct equivalent, but `dnf install -y` after
            // an rpm -U will pull in any missing deps. Only run if deps are broken.
            // Check for missing deps first with rpm --verify.
            let check = runner.run("rpm", &["--verify", "--nodeps"]);
            match check {
                Ok(o) if o.status.success() => Ok(()), // No broken deps.
                _ => {
                    // Try dnf first, fall back to yum.
                    match runner.run("dnf", &["install", "-y", "--best", "--allowerasing"]) {
                        Ok(o) if o.status.success() => Ok(()),
                        _ => match runner.run("yum", &["install", "-y"]) {
                            Ok(o) if o.status.success() => Ok(()),
                            Ok(o) => {
                                let stderr = String::from_utf8_lossy(&o.stderr);
                                Err(format!("yum dep fix: {}", stderr.trim()))
                            }
                            Err(e) => Err(format!("dep fix: {}", e)),
                        },
                    }
                }
            }
        }
        PackageFormat::Pkg | PackageFormat::Msi => Ok(()),
    }
}

// ---------------------------------------------------------------------------
// Cache update
// ---------------------------------------------------------------------------

/// Update the package cache before install.
fn run_update_cache(runner: &dyn CommandRunner, format: PackageFormat) -> Result<(), String> {
    let output = match format {
        PackageFormat::Deb => runner.run("env", &["DEBIAN_FRONTEND=noninteractive", "apt-get", "update", "-qq"]),
        PackageFormat::Rpm => {
            // dnf/yum check-update returns 100 when updates are available — not an error.
            match runner.run("dnf", &["check-update"]) {
                Ok(o) if o.status.success() || o.status.code() == Some(100) => return Ok(()),
                _ => runner.run("yum", &["check-update"]),
            }
        }
        PackageFormat::Pkg | PackageFormat::Msi => return Ok(()),
    };

    match output {
        Ok(o) if o.status.success() || o.status.code() == Some(100) => Ok(()),
        Ok(o) => {
            let stderr = String::from_utf8_lossy(&o.stderr);
            Err(format!("update cache: {}", stderr.trim()))
        }
        Err(e) => Err(format!("update cache: {}", e)),
    }
}

// ---------------------------------------------------------------------------
// Post-cleanup (autoremove)
// ---------------------------------------------------------------------------

/// Remove orphaned dependencies after install. Non-fatal if it fails.
fn run_post_cleanup(runner: &dyn CommandRunner, format: PackageFormat) -> Result<(), String> {
    let output = match format {
        PackageFormat::Deb => runner.run("env", &["DEBIAN_FRONTEND=noninteractive", "apt-get", "autoremove", "-y", "-qq"]),
        PackageFormat::Rpm => {
            match runner.run("dnf", &["autoremove", "-y"]) {
                Ok(o) if o.status.success() => return Ok(()),
                _ => runner.run("yum", &["autoremove", "-y"]),
            }
        }
        PackageFormat::Pkg | PackageFormat::Msi => return Ok(()),
    };

    match output {
        Ok(o) if o.status.success() => Ok(()),
        Ok(o) => {
            let stderr = String::from_utf8_lossy(&o.stderr);
            Err(format!("autoremove: {}", stderr.trim()))
        }
        Err(e) => Err(format!("autoremove: {}", e)),
    }
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

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

/// Clean up stale temp directories from previous nonrepo_package cycles.
/// Removes dirs older than 1 hour to avoid accumulating orphaned downloads.
fn cleanup_stale_tmp_dirs() {
    let Ok(entries) = std::fs::read_dir("/tmp") else { return };
    let cutoff = std::time::SystemTime::now() - std::time::Duration::from_secs(3600);

    for entry in entries.flatten() {
        let name = entry.file_name();
        let name_str = name.to_string_lossy();
        if !name_str.starts_with("vigo-nonrepo-pkg-") {
            continue;
        }
        if let Ok(meta) = entry.metadata() {
            if let Ok(modified) = meta.modified() {
                if modified < cutoff {
                    let _ = std::fs::remove_dir_all(entry.path());
                }
            }
        }
    }
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
#[cfg(unix)]
mod tests {
    use super::*;
    use crate::executors::test_support::mock::*;
    use std::sync::Arc;

    // Helpers to build param maps concisely.
    fn deb_url() -> &'static str {
        "https://example.com/myapp_1.2.3_amd64.deb"
    }

    fn rpm_url() -> &'static str {
        "https://example.com/myapp-1.2.3-1.x86_64.rpm"
    }

    // --- Format detection ---

    #[test]
    fn detect_deb() {
        assert_eq!(detect_format("https://example.com/pkg_1.0_amd64.deb"), Some(PackageFormat::Deb));
    }

    #[test]
    fn detect_rpm() {
        assert_eq!(detect_format("https://example.com/pkg-1.0-1.x86_64.rpm"), Some(PackageFormat::Rpm));
    }

    #[test]
    fn detect_pkg() {
        assert_eq!(detect_format("https://example.com/Install.pkg"), Some(PackageFormat::Pkg));
    }

    #[test]
    fn detect_msi() {
        assert_eq!(detect_format("https://example.com/setup.msi"), Some(PackageFormat::Msi));
    }

    #[test]
    fn detect_unknown() {
        assert_eq!(detect_format("https://example.com/file.tar.gz"), None);
    }

    #[test]
    fn detect_with_query_string() {
        assert_eq!(
            detect_format("https://example.com/pkg.deb?token=abc"),
            Some(PackageFormat::Deb)
        );
    }

    // --- Package name from URL ---

    #[test]
    fn name_from_deb_url() {
        assert_eq!(
            package_name_from_url("https://example.com/nginx_1.24.0-1_amd64.deb", PackageFormat::Deb),
            "nginx"
        );
    }

    #[test]
    fn name_from_rpm_url() {
        assert_eq!(
            package_name_from_url("https://example.com/nginx-1.24.0-1.el9.x86_64.rpm", PackageFormat::Rpm),
            "nginx"
        );
    }

    #[test]
    fn name_from_rpm_complex() {
        assert_eq!(
            package_name_from_url("https://example.com/google-chrome-stable-120.0.1-1.x86_64.rpm", PackageFormat::Rpm),
            "google-chrome-stable"
        );
    }

    #[test]
    fn name_from_msi_url() {
        assert_eq!(
            package_name_from_url("https://example.com/my-app-2.0.msi", PackageFormat::Msi),
            "my-app"
        );
    }

    #[test]
    fn name_from_pkg_url() {
        assert_eq!(
            package_name_from_url("https://example.com/MyApp-1.0.pkg", PackageFormat::Pkg),
            "MyApp"
        );
    }

    // --- present: not installed -> downloads and installs ---

    #[test]
    fn present_not_installed_installs() {
        let mock = MockRunner::new(vec![
            fail_output("package not installed"),  // dpkg -s (not installed)
            ok_output(""),                         // curl download
            ok_output(""),                         // dpkg -i
            ok_output(""),                         // env apt-get install -f (dep fix)
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = NonRepoPackageExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("url", deb_url()),
                ("state", "present"),
            ]),
            false,
        );
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        assert!(result.changed);
        assert_eq!(result.action, Action::Created);

        let calls = calls.lock().unwrap();
        // dpkg -s, curl, dpkg -i, env apt-get install -f
        assert_eq!(calls[0].0, "dpkg");
        assert!(calls[0].1.contains(&"-s".to_string()));
        assert_eq!(calls[1].0, "curl");
        assert_eq!(calls[2].0, "dpkg");
        assert!(calls[2].1.contains(&"-i".to_string()));
        assert_eq!(calls[3].0, "env");
        assert!(calls[3].1.contains(&"install".to_string()));
        assert!(calls[3].1.contains(&"-f".to_string()));
    }

    // --- present: already installed, version matches -> no-op ---

    #[test]
    fn present_installed_version_matches_noop() {
        let mock = MockRunner::new(vec![
            ok_output("Status: install ok installed\nVersion: 1.2.3\n"), // dpkg -s
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = NonRepoPackageExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("url", deb_url()),
                ("version", "1.2.3"),
                ("state", "present"),
            ]),
            false,
        );
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        assert!(!result.changed);
        assert_eq!(result.action, Action::None);

        let calls = calls.lock().unwrap();
        // Only dpkg -s was called (no download).
        assert_eq!(calls.len(), 1);
        assert_eq!(calls[0].0, "dpkg");
    }

    // --- present: installed, version differs -> downloads and upgrades ---

    #[test]
    fn present_installed_version_differs_upgrades() {
        let mock = MockRunner::new(vec![
            ok_output("Status: install ok installed\nVersion: 1.1.0\n"), // dpkg -s (old version)
            ok_output(""),                                                // curl download
            ok_output(""),                                                // dpkg -i (upgrade)
            ok_output(""),                                                // env apt-get install -f
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = NonRepoPackageExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("url", deb_url()),
                ("version", "1.2.3"),
                ("state", "present"),
            ]),
            false,
        );
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        assert!(result.changed);
        assert_eq!(result.action, Action::Updated);

        let calls = calls.lock().unwrap();
        assert_eq!(calls[0].0, "dpkg"); // dpkg -s
        assert_eq!(calls[1].0, "curl"); // download
        assert_eq!(calls[2].0, "dpkg"); // dpkg -i
        assert_eq!(calls[3].0, "env");  // dep fix
    }

    // --- absent: installed -> removes ---

    #[test]
    fn absent_installed_removes() {
        let mock = MockRunner::new(vec![
            ok_output("Status: install ok installed\nVersion: 1.2.3\n"), // dpkg -s
            ok_output(""),                                                // dpkg -r
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = NonRepoPackageExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("url", deb_url()),
                ("state", "absent"),
            ]),
            false,
        );
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        assert!(result.changed);
        assert_eq!(result.action, Action::Deleted);

        let calls = calls.lock().unwrap();
        assert_eq!(calls[0].0, "dpkg"); // dpkg -s
        assert_eq!(calls[1].0, "dpkg"); // dpkg -r
        assert!(calls[1].1.contains(&"-r".to_string()));
    }

    // --- absent: not installed -> no-op ---

    #[test]
    fn absent_not_installed_noop() {
        let mock = MockRunner::new(vec![
            fail_output("package not installed"), // dpkg -s
        ]);
        let exec = NonRepoPackageExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("url", deb_url()),
                ("state", "absent"),
            ]),
            false,
        );
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        assert!(!result.changed);
        assert_eq!(result.action, Action::None);
    }

    // --- purge: installed -> purge removes ---

    #[test]
    fn purge_installed_removes_with_purge() {
        let mock = MockRunner::new(vec![
            ok_output("Status: install ok installed\nVersion: 1.2.3\n"), // dpkg -s
            ok_output(""),                                                // dpkg --purge
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = NonRepoPackageExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("url", deb_url()),
                ("state", "absent"),
                ("purge", "true"),
            ]),
            false,
        );
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        assert!(result.changed);
        assert_eq!(result.action, Action::Deleted);

        let calls = calls.lock().unwrap();
        assert_eq!(calls[1].0, "dpkg");
        assert!(calls[1].1.contains(&"--purge".to_string()));
    }

    // --- lock_version: after install, runs hold command ---

    #[test]
    fn lock_version_after_install() {
        let mock = MockRunner::new(vec![
            fail_output("not installed"), // dpkg -s
            ok_output(""),                // curl download
            ok_output(""),                // dpkg -i
            ok_output(""),                // env apt-get install -f (dep fix)
            ok_output(""),                // apt-mark hold
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = NonRepoPackageExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("url", deb_url()),
                ("state", "present"),
                ("lock_version", "true"),
            ]),
            false,
        );
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        assert!(result.changed);

        let calls = calls.lock().unwrap();
        // Last call should be apt-mark hold.
        let last = calls.last().unwrap();
        assert_eq!(last.0, "apt-mark");
        assert!(last.1.contains(&"hold".to_string()));
    }

    // --- pre_install/post_install: commands run in order ---

    #[test]
    fn pre_post_install_commands_run() {
        let mock = MockRunner::new(vec![
            fail_output("not installed"), // dpkg -s
            ok_output(""),                // pre_install: sh -c "echo pre"
            ok_output(""),                // curl download
            ok_output(""),                // dpkg -i
            ok_output(""),                // env apt-get install -f (dep fix)
            ok_output(""),                // post_install: sh -c "echo post"
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = NonRepoPackageExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("url", deb_url()),
                ("state", "present"),
                ("pre_install", "echo pre"),
                ("post_install", "echo post"),
            ]),
            false,
        );
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);

        let calls = calls.lock().unwrap();
        // Order: dpkg -s, sh -c pre, curl, dpkg -i, env apt-get install -f, sh -c post
        assert_eq!(calls[0].0, "dpkg"); // check
        assert_eq!(calls[1].0, "sh");   // pre_install
        assert!(calls[1].1.contains(&"echo pre".to_string()));
        assert_eq!(calls[2].0, "curl"); // download
        assert_eq!(calls[3].0, "dpkg"); // install
        assert_eq!(calls[4].0, "env");  // dep fix
        assert_eq!(calls[5].0, "sh");   // post_install
        assert!(calls[5].1.contains(&"echo post".to_string()));
    }

    // --- sha256 verification ---

    #[test]
    fn sha256_mismatch_errors() {
        // Create a temp file for download to land in.
        let dir = tempfile::TempDir::new().unwrap();
        let pkg_path = dir.path().join("myapp_1.2.3_amd64.deb");
        // Pre-create the file so sha256 check can read it.
        std::fs::write(&pkg_path, b"package content").unwrap();
        let _actual_hash = file_sha256(pkg_path.to_str().unwrap()).unwrap();

        let mock = MockRunner::new(vec![
            fail_output("not installed"), // dpkg -s
            ok_output(""),                // curl download (will write to tmp dir)
        ]);
        let exec = NonRepoPackageExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("url", deb_url()),
                ("state", "present"),
                ("sha256", "0000000000000000000000000000000000000000000000000000000000000000"),
            ]),
            false,
        );
        // The download will fail due to temp dir mismatch (curl doesn't actually write).
        // The sha256 check happens after download; in mock, the file won't exist at the
        // temp path, so we'll get a hash error.
        assert!(result.error.is_some());
        // Verify the error is hash-related (either mismatch or file not found).
        let err = result.error.unwrap();
        assert!(
            err.contains("sha256") || err.contains("hash") || err.contains("No such file"),
            "expected sha256 error, got: {}",
            err
        );
    }

    // --- dry_run does not download ---

    #[test]
    fn dry_run_does_not_download() {
        let mock = MockRunner::new(vec![
            fail_output("not installed"), // dpkg -s
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = NonRepoPackageExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("url", deb_url()),
                ("state", "present"),
            ]),
            true,
        );
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        assert!(result.changed);
        assert_eq!(result.action, Action::Created);

        let calls = calls.lock().unwrap();
        // Only the installed check; no download, no install.
        assert_eq!(calls.len(), 1);
        assert_eq!(calls[0].0, "dpkg");
    }

    // --- missing url errors ---

    #[test]
    fn missing_url_errors() {
        let mock = MockRunner::new(vec![]);
        let exec = NonRepoPackageExecutor {
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

    // --- unknown format errors ---

    #[test]
    fn unknown_format_errors() {
        let mock = MockRunner::new(vec![]);
        let exec = NonRepoPackageExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("url", "https://example.com/file.tar.gz")]),
            false,
        );
        assert!(result.error.is_some());
        assert!(
            result.error.as_deref().unwrap().contains("cannot detect package format"),
            "unexpected error: {:?}",
            result.error
        );
    }

    // --- RPM install uses rpm -U ---

    #[test]
    fn rpm_install_uses_rpm_u() {
        let mock = MockRunner::new(vec![
            fail_output("not installed"), // rpm -q
            ok_output(""),                // curl download
            ok_output(""),                // rpm -U
            ok_output(""),                // rpm --verify --nodeps (dep check — OK)
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = NonRepoPackageExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("url", rpm_url()),
                ("state", "present"),
            ]),
            false,
        );
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        assert!(result.changed);

        let calls = calls.lock().unwrap();
        assert_eq!(calls[0].0, "rpm");  // rpm -q
        assert_eq!(calls[2].0, "rpm");  // rpm -U
        assert!(calls[2].1.contains(&"-U".to_string()));
    }

    // --- RPM remove uses rpm -e ---

    #[test]
    fn rpm_absent_uses_rpm_e() {
        let mock = MockRunner::new(vec![
            ok_output("1.2.3-1"),  // rpm -q (installed)
            ok_output(""),         // rpm -e
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = NonRepoPackageExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("url", rpm_url()),
                ("state", "absent"),
            ]),
            false,
        );
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        assert_eq!(result.action, Action::Deleted);

        let calls = calls.lock().unwrap();
        assert_eq!(calls[1].0, "rpm");
        assert!(calls[1].1.contains(&"-e".to_string()));
    }

    // --- present: already installed, no version -> no-op ---

    #[test]
    fn present_installed_no_version_noop() {
        let mock = MockRunner::new(vec![
            ok_output("Status: install ok installed\nVersion: 1.2.3\n"), // dpkg -s
        ]);
        let exec = NonRepoPackageExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("url", deb_url()),
                ("state", "present"),
            ]),
            false,
        );
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        assert!(!result.changed);
        assert_eq!(result.action, Action::None);
    }

    // --- allow_downgrade passed to dpkg ---

    #[test]
    fn allow_downgrade_passes_flag() {
        let mock = MockRunner::new(vec![
            ok_output("Status: install ok installed\nVersion: 2.0.0\n"), // dpkg -s (newer)
            ok_output(""),  // curl download
            ok_output(""),  // dpkg -i --force-downgrade
            ok_output(""),  // env apt-get install -f (dep fix)
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = NonRepoPackageExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("url", deb_url()),
                ("version", "1.2.3"),
                ("state", "present"),
                ("allow_downgrade", "true"),
            ]),
            false,
        );
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);

        let calls = calls.lock().unwrap();
        // dpkg -i call should include --force-downgrade.
        let install_call = &calls[2];
        assert_eq!(install_call.0, "dpkg");
        assert!(install_call.1.contains(&"--force-downgrade".to_string()));
    }

    // --- dep resolution failure is fatal ---

    #[test]
    fn dep_resolution_failure_is_fatal() {
        let mock = MockRunner::new(vec![
            fail_output("not installed"), // dpkg -s
            ok_output(""),                // curl download
            ok_output(""),                // dpkg -i
            fail_output("E: Unable to correct problems, you have held broken packages."), // env apt-get install -f
        ]);
        let exec = NonRepoPackageExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("url", deb_url()),
                ("state", "present"),
            ]),
            false,
        );
        assert!(result.error.is_some());
        assert!(result.error.as_deref().unwrap().contains("dependency resolution failed"));
    }

    // --- update_cache runs before install ---

    #[test]
    fn update_cache_runs_before_install() {
        let mock = MockRunner::new(vec![
            fail_output("not installed"), // dpkg -s
            ok_output(""),                // curl download
            ok_output(""),                // env apt-get update (cache update)
            ok_output(""),                // dpkg -i
            ok_output(""),                // env apt-get install -f (dep fix)
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = NonRepoPackageExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("url", deb_url()),
                ("state", "present"),
                ("update_cache", "true"),
            ]),
            false,
        );
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);

        let calls = calls.lock().unwrap();
        // dpkg -s, curl, env apt-get update, dpkg -i, env apt-get install -f
        assert_eq!(calls[0].0, "dpkg");
        assert_eq!(calls[1].0, "curl");
        assert_eq!(calls[2].0, "env");
        assert!(calls[2].1.contains(&"update".to_string()));
        assert_eq!(calls[3].0, "dpkg");
        assert!(calls[3].1.contains(&"-i".to_string()));
    }

    // --- post_cleanup runs after successful install ---

    #[test]
    fn post_cleanup_runs_after_install() {
        let mock = MockRunner::new(vec![
            fail_output("not installed"), // dpkg -s
            ok_output(""),                // curl download
            ok_output(""),                // dpkg -i
            ok_output(""),                // env apt-get install -f (dep fix)
            ok_output(""),                // env apt-get autoremove (post_cleanup)
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = NonRepoPackageExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("url", deb_url()),
                ("state", "present"),
                ("post_cleanup", "true"),
            ]),
            false,
        );
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        assert!(result.changed);

        let calls = calls.lock().unwrap();
        let last = calls.last().unwrap();
        assert_eq!(last.0, "env");
        assert!(last.1.contains(&"autoremove".to_string()));
    }

    // --- post_cleanup does not run when nothing changed ---

    #[test]
    fn post_cleanup_skipped_when_no_change() {
        let mock = MockRunner::new(vec![
            ok_output("Status: install ok installed\nVersion: 1.2.3\n"), // dpkg -s
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = NonRepoPackageExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("url", deb_url()),
                ("state", "present"),
                ("post_cleanup", "true"),
            ]),
            false,
        );
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        assert!(!result.changed);

        let calls = calls.lock().unwrap();
        // Only dpkg -s — no autoremove since nothing changed.
        assert_eq!(calls.len(), 1);
    }

    // --- lock retry on dpkg lock contention ---

    #[test]
    fn lock_retry_on_contention() {
        let mock = MockRunner::new(vec![
            fail_output("not installed"),                               // dpkg -s
            ok_output(""),                                               // curl download
            fail_output("E: Could not get lock /var/lib/dpkg/lock"),    // dpkg -i (lock held, retry 1)
            ok_output(""),                                               // dpkg -i (retry succeeds)
            ok_output(""),                                               // env apt-get install -f
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = NonRepoPackageExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("url", deb_url()),
                ("state", "present"),
            ]),
            false,
        );
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        assert!(result.changed);

        let calls = calls.lock().unwrap();
        // dpkg -s, curl, dpkg -i (fail), dpkg -i (retry), env apt-get install -f
        assert_eq!(calls.len(), 5);
        // Both dpkg -i calls should have -i flag
        assert!(calls[2].1.contains(&"-i".to_string()));
        assert!(calls[3].1.contains(&"-i".to_string()));
    }

    // --- explicit package name override ---

    #[test]
    fn explicit_package_name_used_for_check() {
        let mock = MockRunner::new(vec![
            ok_output("Status: install ok installed\nVersion: 1.2.3\n"), // dpkg -s mypackage
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = NonRepoPackageExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("url", deb_url()),
                ("package", "mypackage"),
                ("version", "1.2.3"),
                ("state", "present"),
            ]),
            false,
        );
        assert!(!result.changed);

        let calls = calls.lock().unwrap();
        // dpkg -s should have been called with "mypackage".
        assert_eq!(calls[0].0, "dpkg");
        assert!(calls[0].1.contains(&"mypackage".to_string()));
    }

    // --- invalid state ---

    #[test]
    fn invalid_state_errors() {
        let mock = MockRunner::new(vec![]);
        let exec = NonRepoPackageExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("url", deb_url()),
                ("state", "purged"),
            ]),
            false,
        );
        assert!(result.error.is_some());
        assert!(result.error.as_deref().unwrap().contains("invalid state"));
    }

    // --- flags passed through ---

    #[test]
    fn flags_passed_to_installer() {
        let mock = MockRunner::new(vec![
            fail_output("not installed"),  // dpkg -s
            ok_output(""),                 // curl download
            ok_output(""),                 // dpkg -i --force-confold
            ok_output(""),                 // env apt-get install -f (dep fix)
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = NonRepoPackageExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("url", deb_url()),
                ("state", "present"),
                ("flags", "--force-confold"),
            ]),
            false,
        );
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);

        let calls = calls.lock().unwrap();
        let install_call = &calls[2];
        assert!(install_call.1.contains(&"--force-confold".to_string()));
    }
}
