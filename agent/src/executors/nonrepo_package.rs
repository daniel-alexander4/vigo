// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Non-repo package executor — installs .deb/.rpm/.pkg/.msi packages from URLs.
//!
//! Uses `dpkg -i` / `rpm -U` / `installer -pkg` / `msiexec /i` instead of
//! package manager repos. Downloads are handled by the shared
//! [`download_with_retry()`](super::download::download_with_retry) helper.

use super::download::{download_with_retry, sanitize_url_filename, DownloadConfig};
use super::{
    Action, CommandRunner, DefaultRunner, Executor, ResourceResult, PKG_MUTATE_TIMEOUT, PKG_NICE,
    PKG_QUERY_TIMEOUT,
};
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
        let artifact = params.get("artifact").cloned().unwrap_or_default();
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

        let mut result = ResourceResult::new("nonrepo_package", if !artifact.is_empty() { &artifact } else { &url });

        // Source resolution: either `url:` (download to /tmp) or `artifact:`
        // (curator-materialized at /var/lib/vigo/artifacts/…). Mutually
        // exclusive — operator must pick one. `sha256:` is redundant with
        // `artifact:` because curator already verified the blob_sha; reject
        // the combo to keep the integrity story unambiguous (slice 6,
        // ADR-024).
        if url.is_empty() && artifact.is_empty() {
            result.error = Some(
                "nonrepo_package executor: one of `url:` or `artifact:` is required".into(),
            );
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }
        if !url.is_empty() && !artifact.is_empty() {
            result.error = Some(
                "nonrepo_package executor: `url:` and `artifact:` are mutually exclusive".into(),
            );
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }
        if !artifact.is_empty() && !sha256_expected.is_empty() {
            result.error = Some(
                "nonrepo_package executor: `sha256:` is redundant with `artifact:` — \
                 the catalog entry's signed blob_sha is already verified by curator. \
                 Drop one or the other.".into(),
            );
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        // Resolve install source up front: format, default package name,
        // and (for `artifact:`) the persistent materialized path. The URL
        // path defers its download until after pre_install / cleanup —
        // see further below — so we leave `install_path` empty here and
        // signal the URL path with `pending_download`.
        let (format, default_pkg_name, install_path_pre_download, pending_download): (
            PackageFormat,
            String,
            String,
            bool,
        ) = if !artifact.is_empty() {
            let resolver = match super::super::swarm::curator::executor_resolver::build_resolver_from_agent_config() {
                Ok(r) => r,
                Err(e) => {
                    result.error = Some(format!(
                        "nonrepo_package executor: cannot construct curator resolver: {e:#}"
                    ));
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }
            };
            let resolved = match super::super::swarm::curator::executor_resolver::resolve_artifact_param(&artifact, &resolver) {
                Ok(r) => r,
                Err(e) => {
                    result.error = Some(format!(
                        "nonrepo_package executor: resolving `artifact: {artifact}`: {e:#}"
                    ));
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }
            };
            let path = resolved.path.to_string_lossy().to_string();
            let sniffed = match detect_format_magic(&path) {
                Some(f) => f,
                None => {
                    result.error = Some(format!(
                        "nonrepo_package executor: cannot detect package format from \
                         artifact's first 8 bytes at {} (supported magic prefixes: \
                         .deb `!<arch>`, .rpm `ed ab ee db`, .pkg `xar!`, .msi `D0 CF 11 E0`). \
                         Re-publish the artifact with one of those formats, or use \
                         `url:` instead of `artifact:`.",
                        path
                    ));
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }
            };
            (sniffed, resolved.name, path, false)
        } else {
            let f = match detect_format(&url) {
                Some(f) => f,
                None => {
                    result.error = Some(format!(
                        "nonrepo_package executor: cannot detect package format from URL \
                         (expected .deb, .rpm, .pkg, or .msi): {}",
                        url
                    ));
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }
            };
            (f, package_name_from_url(&url, f), String::new(), true)
        };

        // Auto-detect package name from URL/artifact or use explicit param.
        let pkg_name = params
            .get("package")
            .cloned()
            .unwrap_or(default_pkg_name);
        if pkg_name.is_empty() {
            let src = if !artifact.is_empty() { format!("artifact:{artifact}") } else { format!("url:{url}") };
            result.error = Some(format!(
                "nonrepo_package executor: could not derive package name from {src}; \
                 set the `package:` param explicitly."
            ));
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        // Update result name to the package name for clearer reporting.
        result.resource_name = pkg_name.clone();

        // Validate state.
        if state == "latest" {
            result.error = Some(
                "nonrepo_package executor: state: latest was retired — use version: latest"
                    .to_string(),
            );
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }
        if !matches!(state, "present" | "absent") {
            result.error = Some(format!(
                "nonrepo_package executor: invalid state {:?} (expected present or absent)",
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

        // Check if already installed and at the right version. A query that
        // FAILED (vs answered "absent") is a hard stop before any download or
        // install decision — acting on it is how a niced dpkg -s hiccup
        // becomes a phantom fresh-install of the pinned version, i.e. a
        // downgrade of a hand-upgraded box (raspberrypi, 2026-08-08). The
        // next convergence run re-asks.
        let (installed, installed_version) = match check_installed(&*self.runner, &pkg_name, format) {
            Ok(v) => v,
            Err(why) => {
                result.error = Some(format!(
                    "nonrepo_package executor: cannot determine whether {} is installed ({}); \
                     refusing to act — a failed query must not read as an absent package",
                    pkg_name, why
                ));
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
        };

        if installed {
            if version == "latest" {
                // version: latest — always proceed to download; the sha256
                // comparison downstream avoids a redundant install.
            } else if !version.is_empty() {
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
            } else {
                // No version specified, already installed — no-op.
                debug!(
                    package = %pkg_name,
                    "nonrepo_package: already installed"
                );
                result.action = Action::None;
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
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
            match self.runner.run_niced_timeout("sh", &["-c", &pre_install], PKG_NICE, PKG_MUTATE_TIMEOUT) {
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

        // Resolve the install source. URL path downloads to a per-cycle
        // tmp dir; artifact path reuses the curator-materialized file
        // (persistent under /var/lib/vigo/artifacts/, no cleanup). The
        // `tmp_dir_cleanup` Option signals whether we own a tmp dir that
        // needs removing on every exit path below.
        let (install_path, tmp_dir_cleanup): (String, Option<String>) = if !pending_download {
            // artifact: → install_path already populated above; nothing to
            // download and nothing to clean up. Curator already verified
            // the blob_sha during pull, so the sha256 verification block
            // below is skipped (mutual-exclusion enforced at the top).
            (install_path_pre_download, None)
        } else {
            // URL path — original behavior.
            cleanup_stale_tmp_dirs();
            let tmp_dir = format!("/tmp/vigo-nonrepo-pkg-{}", uuid::Uuid::new_v4());
            if let Err(e) = std::fs::create_dir_all(&tmp_dir) {
                result.error = Some(format!("create temp dir: {}", e));
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
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
            // Verify SHA-256 if specified (URL path only — `artifact:`
            // gets its integrity guarantee from the signed catalog entry).
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
            (download_path, Some(tmp_dir))
        };

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
            &install_path,
            format,
            allow_downgrade,
            &flags,
        );

        // Clean up our tmp dir if we own one (URL path). The artifact:
        // path's install_path lives under /var/lib/vigo/artifacts/ and
        // must NOT be deleted — curator owns retention there.
        if let Some(d) = &tmp_dir_cleanup {
            let _ = std::fs::remove_dir_all(d);
        }

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
            Err(e) if format == PackageFormat::Deb && is_dependency_error(&e) => {
                // `dpkg -i` exits non-zero when the .deb's own dependencies
                // aren't installed yet: the package lands unpacked-but-
                // unconfigured. That is the ordinary path for any .deb with
                // repo dependencies, and it is precisely what `apt-get
                // install -f` repairs — so run the fixer here too, not only
                // after a clean install, and re-verify before reporting.
                tracing::warn!(
                    package = %pkg_name,
                    "nonrepo_package: unmet dependencies after install, resolving from repos"
                );
                if let Err(fe) = fix_dependencies(&*self.runner, format) {
                    result.error = Some(format!(
                        "nonrepo_package executor: dependency resolution failed: {} (install reported: {})",
                        fe, e
                    ));
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                // The fixer exiting 0 doesn't prove this package configured —
                // apt may have resolved other broken packages and left this
                // one alone. Confirm against dpkg before claiming success.
                let now_installed = matches!(
                    check_installed(&*self.runner, &pkg_name, format),
                    Ok((true, _))
                );
                if !now_installed {
                    result.error = Some(e);
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
            match self.runner.run_niced_timeout("sh", &["-c", &post_install], PKG_NICE, PKG_MUTATE_TIMEOUT) {
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

/// Detect package format from a file's magic bytes (first 8 bytes are
/// enough for all four supported formats). Used by the `artifact:` path —
/// curator-materialized files have no extension, so URL-style detection
/// can't run. Magic prefixes:
///
/// - `.deb`  — `21 3c 61 72 63 68 3e 0a` (`!<arch>\n`, the ar(1) signature)
/// - `.rpm`  — `ed ab ee db` (RPM lead magic)
/// - `.pkg`  — `78 61 72 21` (`xar!`, the xar archive signature)
/// - `.msi`  — `d0 cf 11 e0` (Compound File Binary header)
///
/// Returns `None` for anything unrecognized; the executor surfaces a
/// clear "supported formats" error in that case.
fn detect_format_magic(path: &str) -> Option<PackageFormat> {
    let mut f = std::fs::File::open(path).ok()?;
    let mut buf = [0u8; 8];
    // A short file (< 8 bytes) can't be any of the supported formats,
    // but `read_exact` would fail in a way that loses the partial info.
    // Use `read` and accept whatever we got — buf has trailing zeros if
    // the file was shorter, which won't match any magic prefix.
    if f.read(&mut buf).is_err() {
        return None;
    }
    if buf.starts_with(&[0x21, 0x3c, 0x61, 0x72, 0x63, 0x68, 0x3e, 0x0a]) {
        Some(PackageFormat::Deb)
    } else if buf.starts_with(&[0xed, 0xab, 0xee, 0xdb]) {
        Some(PackageFormat::Rpm)
    } else if buf.starts_with(&[0x78, 0x61, 0x72, 0x21]) {
        Some(PackageFormat::Pkg)
    } else if buf.starts_with(&[0xd0, 0xcf, 0x11, 0xe0]) {
        Some(PackageFormat::Msi)
    } else {
        None
    }
}

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
            let parts: Vec<&str> = base.split(['-', '_']).collect();
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

/// Check whether a package is installed: `Ok((installed, version))` from a
/// query that ANSWERED, `Err(why)` from a query that FAILED.
///
/// The distinction is load-bearing (the pkgmgr family learned it first — see
/// `run_pkg`'s doc comment): `execute()` fresh-installs over "not installed",
/// so folding a timeout or an unclassifiable exit into `(false, None)` turns
/// a wobbly `dpkg -s` into a phantom install of the crate's pinned version —
/// which is a DOWNGRADE on any box hand-upgraded past it. Exactly that
/// happened on raspberrypi (2026-08-08 02:43 UTC: run-result
/// `action=created` for a package dpkg's log shows installed for hours;
/// hesplay 0.45.11 silently replaced by 0.45.10). Only a query that plainly
/// says "not installed" may answer absent; everything else is an error the
/// caller must refuse to act on — the next convergence run re-asks.
fn check_installed(
    runner: &dyn CommandRunner,
    name: &str,
    format: PackageFormat,
) -> Result<(bool, Option<String>), String> {
    match format {
        PackageFormat::Deb => {
            // dpkg -s <name>
            match runner.run_niced_timeout("dpkg", &["-s", name], PKG_NICE, PKG_QUERY_TIMEOUT) {
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
                        return Ok((true, ver));
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
                        let _ = runner.run_niced_timeout("dpkg", &["--configure", name], PKG_NICE, PKG_MUTATE_TIMEOUT);
                        // Re-check after repair. A re-check that FAILS must
                        // propagate — the old code swallowed it into "not
                        // installed", the same fail-open in a second coat.
                        return match runner.run_niced_timeout("dpkg", &["-s", name], PKG_NICE, PKG_QUERY_TIMEOUT) {
                            Ok(o2) if o2.status.success() => {
                                let stdout2 = String::from_utf8_lossy(&o2.stdout);
                                if stdout2.lines().any(|l| l.starts_with("Status:") && l.contains("install ok installed")) {
                                    let ver = stdout2
                                        .lines()
                                        .find(|l| l.starts_with("Version:"))
                                        .map(|l| l.trim_start_matches("Version:").trim().to_string());
                                    Ok((true, ver))
                                } else {
                                    // The query answered: known package, still
                                    // not properly installed — installing is
                                    // the remediation.
                                    Ok((false, None))
                                }
                            }
                            Ok(o2) => classify_deb_query_failure(&o2),
                            Err(e) => Err(format!("dpkg -s {} (post-repair re-check): {}", name, e)),
                        };
                    }

                    // The query answered with a known-but-not-installed state.
                    Ok((false, None))
                }
                Ok(o) => classify_deb_query_failure(&o),
                Err(e) => Err(format!("dpkg -s {}: {}", name, e)),
            }
        }
        PackageFormat::Rpm => {
            // rpm -q <name> --queryformat '%{VERSION}-%{RELEASE}'
            match runner.run_niced_timeout("rpm", &["-q", name, "--queryformat", "%{VERSION}-%{RELEASE}"], PKG_NICE, PKG_QUERY_TIMEOUT) {
                Ok(o) if o.status.success() => {
                    let version = String::from_utf8_lossy(&o.stdout).trim().to_string();
                    if version.is_empty() || version.contains("not installed") {
                        Ok((false, None))
                    } else {
                        Ok((true, Some(version)))
                    }
                }
                // rpm -q exits non-zero for a genuinely absent package, saying
                // so on stdout; anything else non-zero is a failed query.
                Ok(o) => {
                    let text = format!(
                        "{}{}",
                        String::from_utf8_lossy(&o.stdout),
                        String::from_utf8_lossy(&o.stderr)
                    );
                    if text.contains("not installed") {
                        Ok((false, None))
                    } else {
                        Err(format!(
                            "rpm -q {} exited {:?}: {}",
                            name,
                            o.status.code(),
                            text.trim()
                        ))
                    }
                }
                Err(e) => Err(format!("rpm -q {}: {}", name, e)),
            }
        }
        PackageFormat::Pkg => {
            // macOS: pkgutil --pkg-info <name>
            match runner.run_niced_timeout("pkgutil", &["--pkg-info", name], PKG_NICE, PKG_QUERY_TIMEOUT) {
                Ok(o) if o.status.success() => {
                    let stdout = String::from_utf8_lossy(&o.stdout);
                    let ver = stdout
                        .lines()
                        .find(|l| l.starts_with("version:"))
                        .map(|l| l.trim_start_matches("version:").trim().to_string());
                    Ok((true, ver))
                }
                // pkgutil exits non-zero with "No receipt for ..." when the
                // package is genuinely absent.
                Ok(o) => {
                    let text = format!(
                        "{}{}",
                        String::from_utf8_lossy(&o.stdout),
                        String::from_utf8_lossy(&o.stderr)
                    );
                    if text.contains("No receipt") {
                        Ok((false, None))
                    } else {
                        Err(format!(
                            "pkgutil --pkg-info {} exited {:?}: {}",
                            name,
                            o.status.code(),
                            text.trim()
                        ))
                    }
                }
                Err(e) => Err(format!("pkgutil --pkg-info {}: {}", name, e)),
            }
        }
        PackageFormat::Msi => {
            // Windows: check via wmic or registry. Simplified: assume not installed
            // unless we can confirm via PowerShell/wmic. For test purposes, return false.
            #[cfg(windows)]
            {
                // Try wmic product where Name='<name>' get Version
                match runner.run_niced_timeout(
                    "wmic",
                    &["product", "where", &format!("Name='{}'", name), "get", "Version", "/value"],
                    PKG_NICE,
                    PKG_QUERY_TIMEOUT,
                ) {
                    Ok(o) if o.status.success() => {
                        let stdout = String::from_utf8_lossy(&o.stdout);
                        let ver = stdout
                            .lines()
                            .find(|l| l.starts_with("Version="))
                            .map(|l| l.trim_start_matches("Version=").trim().to_string());
                        if ver.is_some() {
                            Ok((true, ver))
                        } else {
                            Ok((false, None))
                        }
                    }
                    Ok(o) => Err(format!(
                        "wmic product query for {} exited {:?}",
                        name,
                        o.status.code()
                    )),
                    Err(e) => Err(format!("wmic product query for {}: {}", name, e)),
                }
            }
            #[cfg(not(windows))]
            {
                Ok((false, None))
            }
        }
    }
}

/// Classify a non-success `dpkg -s` exit: dpkg says "is not installed" for a
/// genuinely absent package (a clean answer), while a locked status database,
/// an OOM-killed child or any other failure is a query that did NOT answer —
/// and must never read as absence.
fn classify_deb_query_failure(o: &std::process::Output) -> Result<(bool, Option<String>), String> {
    let text = format!(
        "{}{}",
        String::from_utf8_lossy(&o.stdout),
        String::from_utf8_lossy(&o.stderr)
    );
    if text.contains("is not installed") {
        Ok((false, None))
    } else {
        Err(format!(
            "dpkg -s exited {:?}: {}",
            o.status.code(),
            text.trim()
        ))
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

/// Check if a `dpkg -i` failure is an unmet-dependency failure — the package
/// unpacked fine but couldn't be configured because its dependencies aren't
/// installed. Repairable by `apt-get install -f`; distinct from a corrupt
/// archive, an arch mismatch, or a conffile prompt, which are not.
fn is_dependency_error(msg: &str) -> bool {
    msg.contains("dependency problems") || msg.contains("Unmet dependencies")
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
                runner.run_niced_timeout("dpkg", &args, PKG_NICE, PKG_MUTATE_TIMEOUT)
            }
            PackageFormat::Rpm => {
                let mut args = vec!["-U"];
                if allow_downgrade {
                    args.push("--oldpackage");
                }
                args.extend(&extra);
                args.push(path);
                runner.run_niced_timeout("rpm", &args, PKG_NICE, PKG_MUTATE_TIMEOUT)
            }
            PackageFormat::Pkg => {
                let mut args = vec!["-pkg", path, "-target", "/"];
                args.extend(&extra);
                runner.run_niced_timeout("installer", &args, PKG_NICE, PKG_MUTATE_TIMEOUT)
            }
            PackageFormat::Msi => {
                let mut args = vec!["/i", path, "/qn"];
                args.extend(&extra);
                runner.run_niced_timeout("msiexec", &args, PKG_NICE, PKG_MUTATE_TIMEOUT)
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

    let installed = match check_installed(runner, name, format) {
        Ok((i, _)) => i,
        Err(why) => {
            // Fail safe in the other direction too: an unanswerable query
            // must not settle as "already absent" — that would hide real
            // drift behind a wobbly dpkg.
            result.error = Some(format!(
                "nonrepo_package executor: cannot determine whether {} is installed ({}); \
                 refusing to act on state: absent",
                name, why
            ));
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }
    };
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
            runner.run_niced_timeout("dpkg", &args, PKG_NICE, PKG_MUTATE_TIMEOUT)
        }
        PackageFormat::Rpm => {
            let mut args = vec!["-e"];
            args.extend(&extra);
            args.push(name);
            runner.run_niced_timeout("rpm", &args, PKG_NICE, PKG_MUTATE_TIMEOUT)
        }
        PackageFormat::Pkg => {
            // macOS has no clean uninstall for .pkg, but pkgutil --forget removes receipt.
            let mut args = vec!["--forget"];
            args.extend(&extra);
            args.push(name);
            runner.run_niced_timeout("pkgutil", &args, PKG_NICE, PKG_MUTATE_TIMEOUT)
        }
        PackageFormat::Msi => {
            let mut args = vec!["/x", name, "/qn"];
            args.extend(&extra);
            runner.run_niced_timeout("msiexec", &args, PKG_NICE, PKG_MUTATE_TIMEOUT)
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
        PackageFormat::Deb => runner.run_niced_timeout("apt-mark", &["hold", name], PKG_NICE, PKG_MUTATE_TIMEOUT),
        PackageFormat::Rpm => {
            // Try dnf versionlock first, fall back to yum versionlock.
            match runner.run_niced_timeout("dnf", &["versionlock", "add", name], PKG_NICE, PKG_MUTATE_TIMEOUT) {
                Ok(o) if o.status.success() => return Ok(()),
                _ => runner.run_niced_timeout("yum", &["versionlock", "add", name], PKG_NICE, PKG_MUTATE_TIMEOUT),
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
            let output = runner.run_niced_timeout(
                "env",
                &["DEBIAN_FRONTEND=noninteractive", "apt-get", "install", "-f", "-y", "-qq"],
                PKG_NICE,
                PKG_MUTATE_TIMEOUT,
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
            let check = runner.run_niced_timeout("rpm", &["--verify", "--nodeps"], PKG_NICE, PKG_MUTATE_TIMEOUT);
            match check {
                Ok(o) if o.status.success() => Ok(()), // No broken deps.
                _ => {
                    // Try dnf first, fall back to yum.
                    match runner.run_niced_timeout("dnf", &["install", "-y", "--best", "--allowerasing"], PKG_NICE, PKG_MUTATE_TIMEOUT) {
                        Ok(o) if o.status.success() => Ok(()),
                        _ => match runner.run_niced_timeout("yum", &["install", "-y"], PKG_NICE, PKG_MUTATE_TIMEOUT) {
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
        PackageFormat::Deb => runner.run_niced_timeout("env", &["DEBIAN_FRONTEND=noninteractive", "apt-get", "update", "-qq"], PKG_NICE, PKG_MUTATE_TIMEOUT),
        PackageFormat::Rpm => {
            // dnf/yum check-update returns 100 when updates are available — not an error.
            match runner.run_niced_timeout("dnf", &["check-update"], PKG_NICE, PKG_MUTATE_TIMEOUT) {
                Ok(o) if o.status.success() || o.status.code() == Some(100) => return Ok(()),
                _ => runner.run_niced_timeout("yum", &["check-update"], PKG_NICE, PKG_MUTATE_TIMEOUT),
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
        PackageFormat::Deb => runner.run_niced_timeout("env", &["DEBIAN_FRONTEND=noninteractive", "apt-get", "autoremove", "-y", "-qq"], PKG_NICE, PKG_MUTATE_TIMEOUT),
        PackageFormat::Rpm => {
            match runner.run_niced_timeout("dnf", &["autoremove", "-y"], PKG_NICE, PKG_MUTATE_TIMEOUT) {
                Ok(o) if o.status.success() => return Ok(()),
                _ => runner.run_niced_timeout("yum", &["autoremove", "-y"], PKG_NICE, PKG_MUTATE_TIMEOUT),
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
    fn detect_magic_recognizes_all_four_formats() {
        use std::io::Write;
        let cases: &[(&str, &[u8], PackageFormat)] = &[
            ("deb-magic.bin", &[0x21, 0x3c, 0x61, 0x72, 0x63, 0x68, 0x3e, 0x0a, 0xff, 0xff], PackageFormat::Deb),
            ("rpm-magic.bin", &[0xed, 0xab, 0xee, 0xdb, 0x00, 0x00, 0x00, 0x00], PackageFormat::Rpm),
            ("pkg-magic.bin", &[0x78, 0x61, 0x72, 0x21, 0x00, 0x1c, 0x00, 0x01], PackageFormat::Pkg),
            ("msi-magic.bin", &[0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1], PackageFormat::Msi),
        ];
        for (name, bytes, expected) in cases {
            let dir = tempfile::tempdir().unwrap();
            let p = dir.path().join(name);
            std::fs::File::create(&p).unwrap().write_all(bytes).unwrap();
            assert_eq!(detect_format_magic(p.to_str().unwrap()), Some(*expected), "{name}");
        }
    }

    #[test]
    fn detect_magic_returns_none_for_unknown_or_short() {
        use std::io::Write;
        let dir = tempfile::tempdir().unwrap();

        // Unknown magic — looks like a tarball.
        let tar = dir.path().join("tar.bin");
        std::fs::File::create(&tar)
            .unwrap()
            .write_all(&[0x1f, 0x8b, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00])
            .unwrap();
        assert_eq!(detect_format_magic(tar.to_str().unwrap()), None);

        // Empty file — can't match anything.
        let empty = dir.path().join("empty.bin");
        std::fs::File::create(&empty).unwrap();
        assert_eq!(detect_format_magic(empty.to_str().unwrap()), None);

        // 1-byte file — short read shouldn't false-positive any magic.
        let one = dir.path().join("one.bin");
        std::fs::File::create(&one).unwrap().write_all(&[0x21]).unwrap();
        assert_eq!(detect_format_magic(one.to_str().unwrap()), None);

        // Nonexistent file — returns None, not a panic.
        assert_eq!(detect_format_magic("/no/such/path/here"), None);
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
            fail_output("dpkg-query: package 'myapp' is not installed and no information is available"),  // dpkg -s (not installed)
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

    // --- present: dpkg -i fails on unmet deps -> apt-get -f repairs it ---

    const DPKG_DEP_FAILURE: &str = "dpkg: dependency problems prevent configuration of hesplay:\n \
        hesplay depends on mpv | ffmpeg; however:\n  Package mpv is not installed.\n";

    #[test]
    fn present_unmet_deps_resolved_by_fixer() {
        let mock = MockRunner::new(vec![
            fail_output("dpkg-query: package 'myapp' is not installed and no information is available"), // dpkg -s (not installed)
            ok_output(""),                        // curl download
            fail_output(DPKG_DEP_FAILURE),        // dpkg -i (unpacked, unconfigured)
            ok_output(""),                        // env apt-get install -f (dep fix)
            ok_output("Status: install ok installed\nVersion: 1.2.3\n"), // dpkg -s re-verify
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = NonRepoPackageExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("url", deb_url()), ("state", "present")]),
            false,
        );
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        assert!(result.changed);
        assert_eq!(result.action, Action::Created);

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 5);
        assert_eq!(calls[3].0, "env");
        assert!(calls[3].1.contains(&"-f".to_string()));
        // The re-verify is what separates "apt fixed it" from "apt fixed
        // something else and left this package unconfigured".
        assert_eq!(calls[4].0, "dpkg");
        assert!(calls[4].1.contains(&"-s".to_string()));
    }

    #[test]
    fn present_unmet_deps_still_broken_after_fixer_errors() {
        let mock = MockRunner::new(vec![
            fail_output("dpkg-query: package 'myapp' is not installed and no information is available"), // dpkg -s (not installed)
            ok_output(""),                        // curl download
            fail_output(DPKG_DEP_FAILURE),        // dpkg -i (unpacked, unconfigured)
            ok_output(""),                        // env apt-get install -f (claims success)
            fail_output("dpkg-query: package 'myapp' is not installed and no information is available"), // dpkg -s re-verify: still not configured
        ]);
        let exec = NonRepoPackageExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("url", deb_url()), ("state", "present")]),
            false,
        );
        let err = result.error.expect("expected the install failure to surface");
        assert!(err.contains("dependency problems"), "unexpected error: {}", err);
        assert!(!result.changed);
    }

    #[test]
    fn non_dependency_install_failure_skips_fixer() {
        let mock = MockRunner::new(vec![
            fail_output("dpkg-query: package 'myapp' is not installed and no information is available"), // dpkg -s (not installed)
            ok_output(""),                        // curl download
            fail_output("dpkg: error: cannot access archive: No such file or directory"),
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = NonRepoPackageExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("url", deb_url()), ("state", "present")]),
            false,
        );
        assert!(result.error.is_some());
        // A corrupt archive is not repairable by `apt-get install -f`; the
        // fixer must not run and mutate unrelated packages.
        assert_eq!(calls.lock().unwrap().len(), 3);
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
            fail_output("dpkg-query: package 'myapp' is not installed and no information is available"), // dpkg -s
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
            fail_output("dpkg-query: package 'myapp' is not installed"), // dpkg -s
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
            fail_output("dpkg-query: package 'myapp' is not installed"), // dpkg -s
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
            fail_output("dpkg-query: package 'myapp' is not installed"), // dpkg -s
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
            fail_output("dpkg-query: package 'myapp' is not installed"), // dpkg -s
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
        // Slice 6: error names both source options now (one of url: or artifact:).
        assert!(
            result.error.as_deref().unwrap().contains("`url:` or `artifact:`"),
            "unexpected error: {:?}",
            result.error
        );
    }

    #[test]
    fn url_and_artifact_together_errors() {
        let mock = MockRunner::new(vec![]);
        let exec = NonRepoPackageExecutor { runner: Box::new(mock) };
        let result = exec.execute(
            &make_params(&[
                ("url", "https://example.com/x.deb"),
                ("artifact", "alexander4/x:latest"),
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
        let exec = NonRepoPackageExecutor { runner: Box::new(mock) };
        let result = exec.execute(
            &make_params(&[
                ("artifact", "alexander4/x:latest"),
                ("sha256", "abc"),
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
            fail_output("dpkg-query: package 'myapp' is not installed"), // dpkg -s
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
            fail_output("dpkg-query: package 'myapp' is not installed"), // dpkg -s
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
            fail_output("dpkg-query: package 'myapp' is not installed"), // dpkg -s
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
            fail_output("dpkg-query: package 'myapp' is not installed"),                               // dpkg -s
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
            fail_output("dpkg-query: package 'myapp' is not installed"),  // dpkg -s
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

    // --- check_installed fail-safe: a failed query must never read as absent ---
    //
    // The raspberrypi incident (2026-08-08 02:43 UTC): a dpkg -s that did not
    // answer was folded into "not installed", and the executor fresh-installed
    // the crate's pinned version over a newer hand-installed build — a phantom
    // downgrade. These pin the fix: only a query that plainly says the package
    // is absent may trigger an install; timeouts and unclassifiable failures
    // are a resource error and NOTHING is downloaded or installed.

    #[test]
    fn query_timeout_refuses_to_install() {
        let mock = MockRunner::empty();
        mock.responses.lock().unwrap().push(Err(std::io::Error::new(
            std::io::ErrorKind::TimedOut,
            "dpkg -s timed out after 30s",
        )));
        let calls = Arc::clone(&mock.calls);
        let exec = NonRepoPackageExecutor { runner: Box::new(mock) };
        let result = exec.execute(
            &make_params(&[("url", deb_url()), ("state", "present")]),
            false,
        );
        let err = result.error.expect("a timed-out query must be a resource error");
        assert!(err.contains("cannot determine"), "unexpected error text: {err}");
        assert!(!result.changed);
        // The one call is the query itself — no download, no dpkg -i.
        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1, "acted on an unanswered query: {calls:?}");
        assert_eq!(calls[0].0, "dpkg");
        assert!(calls[0].1.contains(&"-s".to_string()));
    }

    #[test]
    fn unclassifiable_query_failure_refuses_to_install() {
        // Non-zero exit WITHOUT dpkg's "is not installed" answer — a locked
        // status database, an OOM-killed child, garbage. Must refuse, not
        // install.
        let mock = MockRunner::new(vec![
            fail_output("dpkg: error: dpkg status database is locked by another process"),
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = NonRepoPackageExecutor { runner: Box::new(mock) };
        let result = exec.execute(
            &make_params(&[("url", deb_url()), ("state", "present")]),
            false,
        );
        assert!(result.error.is_some(), "a failed query must surface as an error");
        assert!(!result.changed);
        assert_eq!(calls.lock().unwrap().len(), 1);
    }

    #[test]
    fn clean_not_installed_answer_still_installs() {
        // The genuine-absence path must keep working: dpkg's real wording on a
        // clean "absent" answer proceeds to install (dry_run keeps the test to
        // the decision itself).
        let mock = MockRunner::new(vec![fail_output(
            "dpkg-query: package 'myapp' is not installed and no information is available",
        )]);
        let exec = NonRepoPackageExecutor { runner: Box::new(mock) };
        let result = exec.execute(
            &make_params(&[("url", deb_url()), ("state", "present")]),
            true,
        );
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        assert!(result.changed);
        assert_eq!(result.action, Action::Created);
    }

    #[test]
    fn repair_recheck_failure_refuses_to_install() {
        // Broken-state repair path: the post-repair re-check failing must
        // propagate — the old code swallowed it into "not installed".
        let mock = MockRunner::empty();
        {
            let mut r = mock.responses.lock().unwrap();
            r.push(Ok(ok_output("Status: install ok half-installed\n"))); // dpkg -s
            r.push(Ok(ok_output("")));                                     // dpkg --configure
            r.push(Err(std::io::Error::new(
                std::io::ErrorKind::TimedOut,
                "dpkg -s timed out",
            ))); // re-check
        }
        let calls = Arc::clone(&mock.calls);
        let exec = NonRepoPackageExecutor { runner: Box::new(mock) };
        let result = exec.execute(
            &make_params(&[("url", deb_url()), ("state", "present")]),
            false,
        );
        assert!(result.error.is_some());
        assert!(!result.changed);
        assert_eq!(calls.lock().unwrap().len(), 3, "must stop after the failed re-check");
    }

    #[test]
    fn absent_refuses_on_failed_query() {
        // The other direction of the same rule: state: absent must not settle
        // as "already gone" off a query that never answered.
        let mock = MockRunner::empty();
        mock.responses.lock().unwrap().push(Err(std::io::Error::new(
            std::io::ErrorKind::TimedOut,
            "dpkg -s timed out",
        )));
        let exec = NonRepoPackageExecutor { runner: Box::new(mock) };
        let result = exec.execute(
            &make_params(&[("url", deb_url()), ("state", "absent")]),
            false,
        );
        assert!(result.error.is_some());
        assert!(!result.changed);
        assert_eq!(result.action, Action::None);
    }
}
