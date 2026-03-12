// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! APT package manager backend for Debian/Ubuntu.
//!
//! Uses `dpkg -s` for status queries and `apt-get` for install/remove.
//! Includes smart repair: detects broken (half-configured) packages and
//! runs `dpkg --configure` before falling back to `apt-get install`.

use super::PackageManager;
use std::process::Command;

/// APT package manager for Debian-family systems.
pub struct AptManager;

impl AptManager {
    pub fn new() -> Self {
        Self
    }

    /// Parse dpkg -s output into (status, version).
    fn dpkg_status(name: &str) -> Result<(String, String), String> {
        let output = Command::new("dpkg")
            .args(["-s", name])
            .output()
            .map_err(|e| format!("dpkg -s {}: {}", name, e))?;

        if !output.status.success() {
            return Ok(("not-installed".to_string(), String::new()));
        }

        let stdout = String::from_utf8_lossy(&output.stdout);
        let mut status = String::new();
        let mut version = String::new();

        for line in stdout.lines() {
            if let Some(val) = line.strip_prefix("Status: ") {
                status = val.to_string();
            } else if let Some(val) = line.strip_prefix("Version: ") {
                version = val.trim().to_string();
            }
        }

        Ok((status, version))
    }

    /// Check if a package is in a broken state (half-configured, unpacked, etc).
    fn is_broken(status: &str) -> bool {
        let broken_states = [
            "half-configured",
            "unpacked",
            "half-installed",
            "config-files",
            "triggers-awaited",
            "triggers-pending",
        ];
        broken_states.iter().any(|s| status.contains(s))
    }
}

impl PackageManager for AptManager {
    fn is_installed(&self, name: &str) -> Result<(bool, String), String> {
        let (status, version) = Self::dpkg_status(name)?;

        if status.contains("install ok installed") {
            return Ok((true, version));
        }

        // Broken states are detected — the package needs repair, not a fresh install.
        if Self::is_broken(&status) {
            return Ok((false, version));
        }

        Ok((false, String::new()))
    }

    fn install(&self, name: &str, version: &str) -> Result<(), String> {
        // Check for broken state first.
        let (status, _) = Self::dpkg_status(name)?;
        if Self::is_broken(&status) {
            // Attempt dpkg --configure before full install.
            let configure = Command::new("dpkg")
                .args(["--configure", name])
                .output()
                .map_err(|e| format!("dpkg --configure {}: {}", name, e))?;

            if configure.status.success() {
                // Verify it's now properly installed.
                let (new_status, _) = Self::dpkg_status(name)?;
                if new_status.contains("install ok installed") {
                    return Ok(());
                }
            }
            // Fall through to apt-get install.
        }

        let pkg = if version.is_empty() {
            name.to_string()
        } else {
            format!("{}={}", name, version)
        };

        let output = Command::new("apt-get")
            .args(["install", "-y", &pkg])
            .env("DEBIAN_FRONTEND", "noninteractive")
            .output()
            .map_err(|e| format!("apt-get install {}: {}", name, e))?;

        if !output.status.success() {
            let stderr = String::from_utf8_lossy(&output.stderr);
            return Err(format!("apt-get install {} failed: {}", name, stderr));
        }
        Ok(())
    }

    fn remove(&self, name: &str) -> Result<(), String> {
        let output = Command::new("apt-get")
            .args(["remove", "-y", name])
            .env("DEBIAN_FRONTEND", "noninteractive")
            .output()
            .map_err(|e| format!("apt-get remove {}: {}", name, e))?;

        if !output.status.success() {
            let stderr = String::from_utf8_lossy(&output.stderr);
            return Err(format!("apt-get remove {} failed: {}", name, stderr));
        }
        Ok(())
    }

    fn upgrade(&self, name: &str, version: &str) -> Result<(), String> {
        self.install(name, version)
    }
}
