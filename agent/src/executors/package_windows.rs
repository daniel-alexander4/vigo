// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary
#![cfg(windows)]

//! Windows package executor — manages packages via Chocolatey, winget, or Scoop.
//!
//! Auto-detects the available package manager in priority order:
//! chocolatey > winget > scoop. Supports install, uninstall, and upgrade.

use super::{Action, Executor, ResourceResult, run_command};
use std::collections::HashMap;
use std::time::Instant;

/// Which Windows package manager backend to use.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Backend {
    Chocolatey,
    Winget,
    Scoop,
}

/// Manages Windows packages using the first available package manager.
pub struct PackageWindowsExecutor;

impl PackageWindowsExecutor {
    pub fn new() -> Self {
        Self
    }

    /// Detect the available package manager by probing PATH.
    fn detect_backend() -> Result<Backend, String> {
        // Chocolatey: choco.exe
        if Self::command_exists("choco.exe") {
            return Ok(Backend::Chocolatey);
        }
        // winget: winget.exe
        if Self::command_exists("winget.exe") {
            return Ok(Backend::Winget);
        }
        // Scoop: scoop.cmd
        if Self::command_exists("scoop.cmd") || Self::command_exists("scoop") {
            return Ok(Backend::Scoop);
        }
        Err("package_windows executor: no supported package manager found (tried choco.exe, winget.exe, scoop.cmd)".into())
    }

    fn command_exists(name: &str) -> bool {
        run_command("where.exe", &[name])
            .map(|o| o.status.success())
            .unwrap_or(false)
    }

    /// Check if a package is installed. Returns (installed, version).
    fn is_installed(backend: Backend, name: &str) -> Result<(bool, String), String> {
        match backend {
            Backend::Chocolatey => {
                let output = run_command("choco.exe", &["list", "--local-only", "--exact", name])
                    .map_err(|e| format!("choco list {}: {}", name, e))?;
                let stdout = String::from_utf8_lossy(&output.stdout);
                // choco list output: "package version" lines, then "N packages installed."
                for line in stdout.lines() {
                    let parts: Vec<&str> = line.split_whitespace().collect();
                    if parts.len() >= 2
                        && parts[0].eq_ignore_ascii_case(name)
                    {
                        return Ok((true, parts[1].to_string()));
                    }
                }
                Ok((false, String::new()))
            }
            Backend::Winget => {
                let output = run_command(
                    "winget.exe",
                    &["list", "--exact", "--id", name, "--accept-source-agreements"],
                )
                .map_err(|e| format!("winget list {}: {}", name, e))?;
                if !output.status.success() {
                    return Ok((false, String::new()));
                }
                let stdout = String::from_utf8_lossy(&output.stdout);
                // winget list shows a table; if the package name appears, it's installed.
                let found = stdout.lines().any(|line| line.contains(name));
                Ok((found, String::new()))
            }
            Backend::Scoop => {
                let output = run_command("scoop", &["info", name])
                    .map_err(|e| format!("scoop info {}: {}", name, e))?;
                let stdout = String::from_utf8_lossy(&output.stdout);
                // Look for "Installed:" in the scoop info output.
                let installed = stdout.lines().any(|line| {
                    let trimmed = line.trim();
                    trimmed.starts_with("Installed:")
                        && !trimmed.contains("No")
                });
                // Try to extract version.
                let version = stdout
                    .lines()
                    .find(|line| line.trim().starts_with("Version:"))
                    .map(|line| {
                        line.trim()
                            .strip_prefix("Version:")
                            .unwrap_or("")
                            .trim()
                            .to_string()
                    })
                    .unwrap_or_default();
                Ok((installed, version))
            }
        }
    }

    fn install(backend: Backend, name: &str) -> Result<(), String> {
        let (cmd, args): (&str, Vec<&str>) = match backend {
            Backend::Chocolatey => (
                "choco.exe",
                vec!["install", name, "-y", "--no-progress"],
            ),
            Backend::Winget => (
                "winget.exe",
                vec![
                    "install",
                    "--exact",
                    "--id",
                    name,
                    "--accept-source-agreements",
                    "--accept-package-agreements",
                    "--silent",
                ],
            ),
            Backend::Scoop => ("scoop", vec!["install", name]),
        };

        let output = run_command(cmd, &args)
            .map_err(|e| format!("{} install {}: {}", cmd, name, e))?;
        if !output.status.success() {
            let stderr = String::from_utf8_lossy(&output.stderr);
            let stdout = String::from_utf8_lossy(&output.stdout);
            return Err(format!(
                "{} install {} failed: {} {}",
                cmd, name, stderr, stdout
            ));
        }
        Ok(())
    }

    fn uninstall(backend: Backend, name: &str) -> Result<(), String> {
        let (cmd, args): (&str, Vec<&str>) = match backend {
            Backend::Chocolatey => (
                "choco.exe",
                vec!["uninstall", name, "-y", "--no-progress"],
            ),
            Backend::Winget => (
                "winget.exe",
                vec![
                    "uninstall",
                    "--exact",
                    "--id",
                    name,
                    "--accept-source-agreements",
                    "--silent",
                ],
            ),
            Backend::Scoop => ("scoop", vec!["uninstall", name]),
        };

        let output = run_command(cmd, &args)
            .map_err(|e| format!("{} uninstall {}: {}", cmd, name, e))?;
        if !output.status.success() {
            let stderr = String::from_utf8_lossy(&output.stderr);
            let stdout = String::from_utf8_lossy(&output.stdout);
            return Err(format!(
                "{} uninstall {} failed: {} {}",
                cmd, name, stderr, stdout
            ));
        }
        Ok(())
    }

    fn upgrade(backend: Backend, name: &str) -> Result<(), String> {
        let (cmd, args): (&str, Vec<&str>) = match backend {
            Backend::Chocolatey => (
                "choco.exe",
                vec!["upgrade", name, "-y", "--no-progress"],
            ),
            Backend::Winget => (
                "winget.exe",
                vec![
                    "upgrade",
                    "--exact",
                    "--id",
                    name,
                    "--accept-source-agreements",
                    "--accept-package-agreements",
                    "--silent",
                ],
            ),
            Backend::Scoop => ("scoop", vec!["update", name]),
        };

        let output = run_command(cmd, &args)
            .map_err(|e| format!("{} upgrade {}: {}", cmd, name, e))?;
        if !output.status.success() {
            let stderr = String::from_utf8_lossy(&output.stderr);
            let stdout = String::from_utf8_lossy(&output.stdout);
            return Err(format!(
                "{} upgrade {} failed: {} {}",
                cmd, name, stderr, stdout
            ));
        }
        Ok(())
    }
}

impl Executor for PackageWindowsExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let name = params
            .get("package")
            .cloned()
            .unwrap_or_default();

        let mut result = ResourceResult::new("package_windows", &name);

        if name.is_empty() {
            result.error = Some(
                "package_windows executor: package param is required".to_string(),
            );
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let state = params
            .get("state")
            .map(|s| s.as_str())
            .unwrap_or("present");
        let version = params.get("version").map(|s| s.as_str()).unwrap_or("");

        let backend = match Self::detect_backend() {
            Ok(b) => b,
            Err(e) => {
                result.error = Some(e);
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
        };

        let (installed, _version) = match Self::is_installed(backend, &name) {
            Ok(r) => r,
            Err(e) => {
                result.error = Some(e);
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
        };

        match state {
            "present" if version == "latest" => {
                if !installed {
                    // Not installed at all — install it.
                    result.action = Action::Created;
                    result.changed = true;

                    if dry_run {
                        result.duration_ms = start.elapsed().as_millis() as i32;
                        return result;
                    }

                    if let Err(e) = Self::install(backend, &name) {
                        result.error = Some(e);
                    }
                } else {
                    // Already installed — attempt upgrade.
                    result.action = Action::Updated;
                    result.changed = true;

                    if dry_run {
                        result.duration_ms = start.elapsed().as_millis() as i32;
                        return result;
                    }

                    if let Err(e) = Self::upgrade(backend, &name) {
                        result.error = Some(e);
                    }
                }
            }
            "present" => {
                if installed {
                    result.action = Action::None;
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                result.action = Action::Created;
                result.changed = true;

                if dry_run {
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                if let Err(e) = Self::install(backend, &name) {
                    result.error = Some(e);
                }
            }
            "absent" => {
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

                if let Err(e) = Self::uninstall(backend, &name) {
                    result.error = Some(e);
                }
            }
            "latest" => {
                result.error = Some(
                    "package_windows executor: state: latest was retired — use version: latest"
                        .to_string(),
                );
            }
            _ => {
                result.error = Some(format!(
                    "package_windows executor: unknown state {:?}",
                    state
                ));
            }
        }

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn missing_package() {
        let exec = PackageWindowsExecutor::new();
        let params = HashMap::new();
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result
            .error
            .unwrap()
            .contains("package param is required"));
    }

    #[test]
    fn empty_package_string() {
        let exec = PackageWindowsExecutor::new();
        let mut params = HashMap::new();
        params.insert("package".to_string(), "".to_string());
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result
            .error
            .unwrap()
            .contains("package param is required"));
    }
}
