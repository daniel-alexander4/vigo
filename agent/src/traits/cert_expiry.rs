// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Certificate expiry trait collector.
//!
//! Scans well-known certificate directories for .pem and .crt files,
//! extracts expiry date and subject via openssl, computes days remaining.

use anyhow::Result;
use serde_json::{json, Value};
use std::path::Path;
use std::process::Command;

/// Well-known certificate directories to scan.
const CERT_DIRS: &[&str] = &["/etc/ssl/certs/", "/etc/pki/tls/certs/"];

pub fn collect() -> Result<Value> {
    let mut certificates = Vec::new();

    for dir in CERT_DIRS {
        let dir_path = Path::new(dir);
        if !dir_path.is_dir() {
            continue;
        }

        let entries = match std::fs::read_dir(dir_path) {
            Ok(e) => e,
            Err(_) => continue,
        };

        for entry in entries.flatten() {
            let path = entry.path();
            if !path.is_file() {
                continue;
            }

            // Only process .pem and .crt files.
            let ext = path
                .extension()
                .and_then(|e| e.to_str())
                .unwrap_or("");
            if ext != "pem" && ext != "crt" {
                continue;
            }

            // Skip symlinks to avoid duplicates (e.g., /etc/ssl/certs/ hash links).
            if path.symlink_metadata().map(|m| m.file_type().is_symlink()).unwrap_or(false) {
                continue;
            }

            let path_str = path.to_string_lossy().to_string();

            // Get expiry date.
            let enddate = match get_enddate(&path_str) {
                Some(d) => d,
                None => continue,
            };

            // Get subject.
            let subject = get_subject(&path_str).unwrap_or_default();

            // Parse the date and compute days remaining.
            if let Some((expires_iso, days_remaining)) = parse_openssl_date(&enddate) {
                certificates.push(json!({
                    "path": path_str,
                    "subject": subject,
                    "expires": expires_iso,
                    "days_remaining": days_remaining,
                }));
            }
        }
    }

    Ok(json!({ "certificates": certificates }))
}

/// Run `openssl x509 -noout -enddate -in <path>` and return the date string.
fn get_enddate(path: &str) -> Option<String> {
    let output = Command::new("openssl")
        .args(["x509", "-noout", "-enddate", "-in", path])
        .output()
        .ok()?;

    if !output.status.success() {
        return None;
    }

    let stdout = String::from_utf8_lossy(&output.stdout);
    // Format: "notAfter=Mon DD HH:MM:SS YYYY GMT"
    let line = stdout.trim();
    line.strip_prefix("notAfter=").map(|s| s.to_string())
}

/// Run `openssl x509 -noout -subject -in <path>` and return the subject string.
fn get_subject(path: &str) -> Option<String> {
    let output = Command::new("openssl")
        .args(["x509", "-noout", "-subject", "-in", path])
        .output()
        .ok()?;

    if !output.status.success() {
        return None;
    }

    let stdout = String::from_utf8_lossy(&output.stdout);
    let line = stdout.trim();
    // Format: "subject=CN = foo" or "subject= /CN=foo"
    line.strip_prefix("subject=")
        .or_else(|| line.strip_prefix("subject= "))
        .map(|s| s.trim().to_string())
}

/// Parse an openssl date string like "Dec  1 00:00:00 2026 GMT" into
/// an ISO 8601 string and days remaining from now.
fn parse_openssl_date(date_str: &str) -> Option<(String, i64)> {
    // OpenSSL date format: "Mon DD HH:MM:SS YYYY GMT" or "MonDD HH:MM:SS YYYY GMT"
    // Use openssl's own parsing by comparing against current time.
    let parts: Vec<&str> = date_str.split_whitespace().collect();
    if parts.len() < 4 {
        return None;
    }

    // Parse month.
    let month = match parts[0] {
        "Jan" => 1,
        "Feb" => 2,
        "Mar" => 3,
        "Apr" => 4,
        "May" => 5,
        "Jun" => 6,
        "Jul" => 7,
        "Aug" => 8,
        "Sep" => 9,
        "Oct" => 10,
        "Nov" => 11,
        "Dec" => 12,
        _ => return None,
    };

    let day: u32 = parts[1].parse().ok()?;
    let time = parts[2]; // HH:MM:SS
    let year: u32 = parts[3].parse().ok()?;

    let expires_iso = format!("{:04}-{:02}-{:02}T{}Z", year, month, day, time);

    // Compute days remaining using Unix timestamps.
    use std::time::{SystemTime, UNIX_EPOCH};
    let now_secs = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs() as i64;

    // Approximate the expiry as seconds since epoch.
    // Simple calculation: days from epoch to the expiry date.
    let expiry_secs = date_to_epoch_approx(year, month, day);
    let days_remaining = (expiry_secs - now_secs) / 86400;

    Some((expires_iso, days_remaining))
}

/// Approximate conversion of a date to Unix epoch seconds.
fn date_to_epoch_approx(year: u32, month: u32, day: u32) -> i64 {
    // Days from 1970 to the start of the given year.
    let mut days: i64 = 0;
    for y in 1970..year {
        days += if is_leap(y) { 366 } else { 365 };
    }

    // Days in months before the given month.
    let month_days = [0, 31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31];
    for m in 1..month {
        days += month_days[m as usize] as i64;
        if m == 2 && is_leap(year) {
            days += 1;
        }
    }

    days += (day - 1) as i64;
    days * 86400
}

fn is_leap(y: u32) -> bool {
    (y % 4 == 0 && y % 100 != 0) || y % 400 == 0
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_parse_openssl_date() {
        let result = parse_openssl_date("Dec  1 00:00:00 2026 GMT");
        assert!(result.is_some());
        let (iso, _days) = result.unwrap();
        assert_eq!(iso, "2026-12-01T00:00:00Z");
    }

    #[test]
    fn test_parse_openssl_date_invalid() {
        assert!(parse_openssl_date("").is_none());
        assert!(parse_openssl_date("not a date").is_none());
    }

    #[test]
    fn test_collect_returns_certificates_key() {
        let val = collect().unwrap();
        let obj = val.as_object().unwrap();
        assert!(obj.contains_key("certificates"));
        assert!(obj["certificates"].is_array());
    }
}
