// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! APT package manager backend for Debian/Ubuntu.
//!
//! Uses `dpkg -s` for status queries and `apt-get` for install/remove.
//! Includes smart repair: detects broken (half-configured) packages and
//! runs `dpkg --configure` before falling back to `apt-get install`.
//! Network transient errors are retried with backoff before giving up.

use super::PackageManager;
use super::errors::{ErrorClass, classify_apt_error};
use std::process::Command;
use std::sync::Mutex;
use tracing::warn;

/// APT package manager for Debian-family systems.
pub struct AptManager {
    /// Extra flags appended to install/upgrade/remove commands.
    /// Mutex for interior mutability — set per-invocation from the executor wrapper.
    extra_flags: Mutex<Vec<String>>,
}

impl AptManager {
    pub fn new() -> Self {
        Self {
            extra_flags: Mutex::new(Vec::new()),
        }
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

/// Check if apt/dpkg stderr indicates a lock contention error.
fn is_lock_error(stderr: &str) -> bool {
    stderr.contains("Could not get lock") || stderr.contains("Unable to acquire")
}

/// Check if stderr indicates a network transient error.
fn is_network_error(stderr: &str) -> bool {
    matches!(classify_apt_error(stderr), ErrorClass::NetworkTransient)
}

/// Run an apt-get command with retry on dpkg lock contention and network errors.
/// Lock retries: up to 3 times with exponential backoff (3s, 6s, 12s).
/// Network retries: up to 3 times with backoff (5s, 10s, 20s).
fn apt_get_with_retry(args: &[&str], desc: &str) -> Result<(), String> {
    const MAX_LOCK_RETRIES: u32 = 3;
    const MAX_NETWORK_RETRIES: u32 = 3;
    let mut lock_delay = std::time::Duration::from_secs(3);
    let mut network_delay = std::time::Duration::from_secs(5);
    let mut lock_retries = 0u32;
    let mut network_retries = 0u32;

    loop {
        let output = Command::new("apt-get")
            .args(args)
            .env("DEBIAN_FRONTEND", "noninteractive")
            .output()
            .map_err(|e| format!("apt-get {}: {}", desc, e))?;

        if output.status.success() {
            return Ok(());
        }

        let stderr = String::from_utf8_lossy(&output.stderr);

        // Retry on lock contention.
        if is_lock_error(&stderr) && lock_retries < MAX_LOCK_RETRIES {
            warn!(
                "dpkg lock held, retrying in {}s (attempt {}/{}): {}",
                lock_delay.as_secs(),
                lock_retries + 1,
                MAX_LOCK_RETRIES,
                desc,
            );
            std::thread::sleep(lock_delay);
            lock_delay *= 2;
            lock_retries += 1;
            continue;
        }

        // Retry on network transient errors.
        if is_network_error(&stderr) && network_retries < MAX_NETWORK_RETRIES {
            warn!(
                "network error, retrying in {}s (attempt {}/{}): {}",
                network_delay.as_secs(),
                network_retries + 1,
                MAX_NETWORK_RETRIES,
                desc,
            );
            std::thread::sleep(network_delay);
            network_delay *= 2;
            network_retries += 1;
            continue;
        }

        // No more retries — return the error with the raw stderr for classification.
        return Err(stderr.to_string());
    }
}

impl AptManager {
    /// Build apt-get args: base args + extra_flags + package spec.
    fn build_apt_args(&self, base: &[&str], pkg: &str) -> Vec<String> {
        let mut args: Vec<String> = base.iter().map(|s| s.to_string()).collect();
        let flags = self.extra_flags.lock().unwrap();
        for f in flags.iter() {
            args.push(f.clone());
        }
        args.push(pkg.to_string());
        args
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

        // Local .deb files must be installed via dpkg, not apt-get.
        if name.ends_with(".deb") && std::path::Path::new(name).exists() {
            let output = Command::new("dpkg")
                .args(["-i", name])
                .output()
                .map_err(|e| format!("dpkg -i {}: {}", name, e))?;
            if !output.status.success() {
                let stderr = String::from_utf8_lossy(&output.stderr);
                // Try to fix missing dependencies.
                let _ = Command::new("apt-get")
                    .args(["install", "-y", "--fix-broken"])
                    .output();
                return Err(stderr.to_string());
            }
            return Ok(());
        }

        let pkg = if version.is_empty() {
            name.to_string()
        } else {
            format!("{}={}", name, version)
        };

        let args = self.build_apt_args(&["install", "-y"], &pkg);
        let str_args: Vec<&str> = args.iter().map(|s| s.as_str()).collect();
        apt_get_with_retry(
            &str_args,
            &format!("install {}", name),
        )
    }

    fn remove(&self, name: &str) -> Result<(), String> {
        let args = self.build_apt_args(&["remove", "-y"], name);
        let str_args: Vec<&str> = args.iter().map(|s| s.as_str()).collect();
        apt_get_with_retry(
            &str_args,
            &format!("remove {}", name),
        )
    }

    fn upgrade(&self, name: &str, version: &str) -> Result<(), String> {
        self.install(name, version)
    }

    fn downgrade(&self, name: &str, version: &str) -> Result<(), String> {
        let pkg = if version.is_empty() {
            name.to_string()
        } else {
            format!("{}={}", name, version)
        };

        let args = self.build_apt_args(&["install", "-y", "--allow-downgrades"], &pkg);
        let str_args: Vec<&str> = args.iter().map(|s| s.as_str()).collect();
        apt_get_with_retry(
            &str_args,
            &format!("downgrade {}", name),
        )
    }

    fn purge(&self, name: &str) -> Result<(), String> {
        let args = self.build_apt_args(&["purge", "-y"], name);
        let str_args: Vec<&str> = args.iter().map(|s| s.as_str()).collect();
        apt_get_with_retry(
            &str_args,
            &format!("purge {}", name),
        )
    }

    fn update_cache(&self) -> Result<(), String> {
        apt_get_with_retry(
            &["update", "-qq"],
            "update cache",
        )
    }

    fn upgrade_latest(&self, name: &str) -> Result<(), String> {
        let args = self.build_apt_args(&["install", "--only-upgrade", "-y"], name);
        let str_args: Vec<&str> = args.iter().map(|s| s.as_str()).collect();
        apt_get_with_retry(
            &str_args,
            &format!("upgrade {}", name),
        )
    }

    fn hold(&self, name: &str) -> Result<(), String> {
        let output = Command::new("apt-mark")
            .args(["hold", name])
            .output()
            .map_err(|e| format!("apt-mark hold {}: {}", name, e))?;

        if !output.status.success() {
            let stderr = String::from_utf8_lossy(&output.stderr);
            return Err(format!("apt-mark hold {} failed: {}", name, stderr));
        }
        Ok(())
    }

    fn unhold(&self, name: &str) -> Result<(), String> {
        let output = Command::new("apt-mark")
            .args(["unhold", name])
            .output()
            .map_err(|e| format!("apt-mark unhold {}: {}", name, e))?;

        if !output.status.success() {
            let stderr = String::from_utf8_lossy(&output.stderr);
            return Err(format!("apt-mark unhold {} failed: {}", name, stderr));
        }
        Ok(())
    }

    fn post_cleanup(&self) -> Result<(), String> {
        apt_get_with_retry(&["autoremove", "-y", "-qq"], "autoremove")
    }

    fn upgrade_all(&self) -> Result<(), String> {
        let args = self.build_apt_args(&["upgrade", "-y"], "");
        // Remove trailing empty string from args (build_apt_args appends the package name).
        let args: Vec<String> = args.into_iter().filter(|s| !s.is_empty()).collect();
        let str_args: Vec<&str> = args.iter().map(|s| s.as_str()).collect();
        apt_get_with_retry(&str_args, "upgrade all")
    }

    fn set_flags(&self, flags: &[String]) {
        *self.extra_flags.lock().unwrap() = flags.to_vec();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn lock_error_detected() {
        assert!(is_lock_error(
            "E: Could not get lock /var/lib/dpkg/lock-frontend. It is held by process 1234 (apt-get)"
        ));
        assert!(is_lock_error(
            "E: Unable to acquire the dpkg frontend lock (/var/lib/dpkg/lock-frontend)"
        ));
        assert!(is_lock_error(
            "E: Could not get lock /var/lib/apt/lists/lock"
        ));
    }

    #[test]
    fn non_lock_error_not_detected() {
        assert!(!is_lock_error("E: Package 'foo' has no installation candidate"));
        assert!(!is_lock_error("E: Unable to locate package bar"));
    }

    #[test]
    fn network_error_detected() {
        assert!(is_network_error(
            "E: Failed to fetch http://archive.ubuntu.com  Temporary failure resolving 'archive.ubuntu.com'"
        ));
        assert!(is_network_error(
            "E: Could not resolve 'archive.ubuntu.com'"
        ));
    }

    #[test]
    fn non_network_error_not_detected() {
        assert!(!is_network_error("E: Unable to locate package foo"));
        assert!(!is_network_error("E: Package 'foo' has no installation candidate"));
    }
}
