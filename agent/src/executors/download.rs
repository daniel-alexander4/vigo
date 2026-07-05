// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Shared download helper — curl with wget fallback and network retry.
//!
//! Used by `source_package` and `nonrepo_package` executors for URL downloads.
//! Retries on transient network errors (DNS failure, connection timeout, etc.)
//! with exponential backoff.

use super::{CommandRunner, PKG_NICE};
use std::time::Duration;
use tracing::warn;

/// Configuration for a download operation.
pub struct DownloadConfig<'a> {
    /// URL to download from.
    pub url: &'a str,
    /// Local path to write the downloaded file.
    pub dest: &'a str,
    /// Maximum number of retries on transient network errors.
    pub retries: u32,
    /// Initial delay between retries (doubles each attempt).
    pub retry_delay: Duration,
    /// Download timeout in seconds (passed to curl --max-time / wget --timeout).
    pub timeout_secs: u64,
    /// Optional HTTP headers (e.g., "Authorization: Bearer token").
    pub headers: &'a [&'a str],
}

impl<'a> Default for DownloadConfig<'a> {
    fn default() -> Self {
        Self {
            url: "",
            dest: "",
            retries: 2,
            retry_delay: Duration::from_secs(5),
            timeout_secs: 300,
            headers: &[],
        }
    }
}

/// Download a file from a URL using curl (with wget fallback).
///
/// Retries on transient network errors with exponential backoff.
/// Returns `Ok(())` on success, `Err(message)` on final failure.
pub fn download_with_retry(
    runner: &dyn CommandRunner,
    config: &DownloadConfig,
) -> Result<(), String> {
    let mut delay = config.retry_delay;

    for attempt in 0..=config.retries {
        match try_download(runner, config) {
            Ok(()) => return Ok(()),
            Err(e) => {
                if attempt < config.retries && is_transient_error(&e) {
                    warn!(
                        url = config.url,
                        "download failed (transient), retrying in {}s (attempt {}/{}): {}",
                        delay.as_secs(),
                        attempt + 1,
                        config.retries,
                        e,
                    );
                    std::thread::sleep(delay);
                    delay *= 2;
                    continue;
                }
                return Err(e);
            }
        }
    }

    Err(format!(
        "download {} failed after {} retries",
        config.url, config.retries
    ))
}

/// Attempt a single download: try curl first, fall back to wget.
fn try_download(runner: &dyn CommandRunner, config: &DownloadConfig) -> Result<(), String> {
    let timeout_str = config.timeout_secs.to_string();
    // Tree-kill backstop 30s beyond curl/wget's own --max-time/--timeout, so the
    // process self-exits first and this only fires on a wedged download.
    let tree_kill = Duration::from_secs(config.timeout_secs.saturating_add(30));

    // Build curl args.
    let mut curl_args = vec!["-fsSL", "--max-time", &timeout_str, "-o", config.dest];
    for header in config.headers {
        curl_args.push("-H");
        curl_args.push(header);
    }
    curl_args.push(config.url);

    match runner.run_niced_timeout("curl", &curl_args, PKG_NICE, tree_kill) {
        Ok(o) if o.status.success() => Ok(()),
        Ok(o) => {
            let stderr = String::from_utf8_lossy(&o.stderr);
            // curl failed — try wget before giving up.
            let mut wget_args = vec!["-qO", config.dest, "--timeout", &timeout_str];
            for header in config.headers {
                wget_args.push("--header");
                wget_args.push(header);
            }
            wget_args.push(config.url);

            match runner.run_niced_timeout("wget", &wget_args, PKG_NICE, tree_kill) {
                Ok(o2) if o2.status.success() => Ok(()),
                Ok(o2) => {
                    let stderr2 = String::from_utf8_lossy(&o2.stderr);
                    // Return the more informative error message.
                    let msg = if !stderr.is_empty() {
                        stderr.to_string()
                    } else {
                        stderr2.to_string()
                    };
                    Err(format!("download {} failed: {}", config.url, msg.trim()))
                }
                Err(e) => {
                    // wget not available, report curl's error.
                    if !stderr.is_empty() {
                        Err(format!("download {} failed: {}", config.url, stderr.trim()))
                    } else {
                        Err(format!("download {}: {}", config.url, e))
                    }
                }
            }
        }
        Err(e) => {
            // curl not available — try wget directly.
            let mut wget_args = vec!["-qO", config.dest, "--timeout", &timeout_str];
            for header in config.headers {
                wget_args.push("--header");
                wget_args.push(header);
            }
            wget_args.push(config.url);

            match runner.run_niced_timeout("wget", &wget_args, PKG_NICE, tree_kill) {
                Ok(o) if o.status.success() => Ok(()),
                Ok(o) => {
                    let stderr = String::from_utf8_lossy(&o.stderr);
                    Err(format!("download {} failed: {}", config.url, stderr.trim()))
                }
                Err(e2) => Err(format!(
                    "download {}: neither curl ({}) nor wget ({}) available",
                    config.url, e, e2
                )),
            }
        }
    }
}

/// Classify whether a download error is transient (worth retrying).
fn is_transient_error(error: &str) -> bool {
    let lower = error.to_lowercase();
    lower.contains("dns")
        || lower.contains("resolve")
        || lower.contains("timed out")
        || lower.contains("timeout")
        || lower.contains("connection refused")
        || lower.contains("connection reset")
        || lower.contains("network is unreachable")
        || lower.contains("temporary failure")
        || lower.contains("could not connect")
        || lower.contains("ssl handshake")
        // HTTP 502/503/504 are transient server errors.
        || lower.contains("502")
        || lower.contains("503")
        || lower.contains("504")
}

/// Sanitize a filename extracted from a URL path.
/// Returns `Err` if the filename is unsafe (path traversal, absolute path, empty).
pub fn sanitize_url_filename(url: &str) -> Result<String, String> {
    let raw = url.rsplit('/').next().unwrap_or("download");
    let filename = std::path::Path::new(raw)
        .file_name()
        .and_then(|f| f.to_str())
        .unwrap_or("download");
    if filename.contains("..") || filename.starts_with('/') || filename.is_empty() {
        return Err(format!("unsafe filename in URL: {}", raw));
    }
    Ok(filename.to_string())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn transient_errors_detected() {
        assert!(is_transient_error("DNS resolution failed"));
        assert!(is_transient_error("Connection timed out"));
        assert!(is_transient_error("Could not resolve host"));
        assert!(is_transient_error("503 Service Unavailable"));
        assert!(is_transient_error("Connection refused"));
        assert!(is_transient_error("Network is unreachable"));
    }

    #[test]
    fn permanent_errors_not_transient() {
        assert!(!is_transient_error("404 Not Found"));
        assert!(!is_transient_error("403 Forbidden"));
        assert!(!is_transient_error("401 Unauthorized"));
    }

    #[test]
    fn sanitize_normal_filename() {
        assert_eq!(
            sanitize_url_filename("https://example.com/foo-1.0.tar.gz").unwrap(),
            "foo-1.0.tar.gz"
        );
    }

    #[test]
    fn sanitize_traversal_falls_back_to_download() {
        // ".." as the final path component: file_name() returns None, falls back safely.
        assert_eq!(
            sanitize_url_filename("https://example.com/..").unwrap(),
            "download"
        );
    }

    #[test]
    fn sanitize_extracts_filename_from_path() {
        // Path traversal in parent components is fine — we only extract the filename.
        assert_eq!(
            sanitize_url_filename("https://example.com/../etc/passwd").unwrap(),
            "passwd"
        );
    }

    #[test]
    fn sanitize_rejects_dotdot_in_filename() {
        // A filename containing ".." is rejected.
        assert!(sanitize_url_filename("https://example.com/..foo..bar").is_err());
    }

    #[test]
    fn sanitize_handles_no_path() {
        // URL with no path component after host.
        assert_eq!(
            sanitize_url_filename("https://example.com/").unwrap(),
            "download"
        );
    }
}
