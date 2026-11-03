// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Shared sysctl utilities for macOS/FreeBSD/OpenBSD/NetBSD trait collectors.

use std::process::Command;

/// Run `sysctl -n <key>` and return trimmed stdout, or None on failure.
pub fn sysctl_raw(key: &str) -> Option<String> {
    Command::new("sysctl")
        .args(["-n", key])
        .output()
        .ok()
        .filter(|o| o.status.success())
        .map(|o| String::from_utf8_lossy(&o.stdout).trim().to_string())
        .filter(|s| !s.is_empty())
}

/// Run `sysctl -n <key>` and return the result as a string.
pub fn sysctl_string(key: &str) -> String {
    sysctl_raw(key).unwrap_or_default()
}

/// Run `sysctl -n <key>` and parse as u64.
pub fn sysctl_u64(key: &str) -> Option<u64> {
    sysctl_raw(key).and_then(|s| s.parse().ok())
}

/// Run `sysctl -n <key>` and parse as f64.
pub fn sysctl_f64(key: &str) -> Option<f64> {
    sysctl_raw(key).and_then(|s| s.parse().ok())
}

/// Run `sysctl -n <key>` and parse as i64.
pub fn sysctl_i64(key: &str) -> Option<i64> {
    sysctl_raw(key).and_then(|s| s.parse().ok())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn sysctl_raw_nonexistent_returns_none() {
        assert!(sysctl_raw("nonexistent.key.that.does.not.exist.12345").is_none());
    }

    #[test]
    fn sysctl_string_nonexistent_returns_empty() {
        assert!(sysctl_string("nonexistent.key.that.does.not.exist.12345").is_empty());
    }

    #[test]
    fn sysctl_u64_nonexistent_returns_none() {
        assert!(sysctl_u64("nonexistent.key.that.does.not.exist.12345").is_none());
    }
}
