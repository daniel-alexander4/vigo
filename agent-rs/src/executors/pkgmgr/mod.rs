// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Package manager abstraction layer.
//!
//! Provides a [`PackageManager`] trait and auto-detection via [`detect`].
//! Backends: apt, dnf, yum, apk, pacman, zypper.

pub mod apt;

/// Abstraction over OS package managers.
pub trait PackageManager: Send + Sync {
    /// Check if a package is installed. Returns (installed, version).
    fn is_installed(&self, name: &str) -> Result<(bool, String), String>;
    /// Install a package, optionally at a specific version.
    fn install(&self, name: &str, version: &str) -> Result<(), String>;
    /// Remove a package.
    fn remove(&self, name: &str) -> Result<(), String>;
    /// Upgrade a package to a specific version.
    fn upgrade(&self, name: &str, version: &str) -> Result<(), String>;
}

/// Detect the appropriate package manager for this system.
pub fn detect(os_family: &str, os_distro: &str) -> Result<Box<dyn PackageManager>, String> {
    let family = os_family.to_lowercase();
    let distro = os_distro.to_lowercase();

    match family.as_str() {
        "debian" | "ubuntu" => Ok(Box::new(apt::AptManager::new())),
        "redhat" | "fedora" => {
            // dnf is preferred on modern RHEL/Fedora
            if command_exists("dnf") {
                Ok(Box::new(GenericPkgMgr::dnf()))
            } else {
                Ok(Box::new(GenericPkgMgr::yum()))
            }
        }
        "suse" | "opensuse" => Ok(Box::new(GenericPkgMgr::zypper())),
        "alpine" => Ok(Box::new(GenericPkgMgr::apk())),
        "arch" | "archlinux" => Ok(Box::new(GenericPkgMgr::pacman())),
        _ => {
            // Fall back to distro name (gopsutil may report distro as family).
            match distro.as_str() {
                "ubuntu" | "debian" | "linuxmint" | "pop" => {
                    Ok(Box::new(apt::AptManager::new()))
                }
                "fedora" | "rhel" | "centos" | "rocky" | "almalinux" | "oracle" => {
                    if command_exists("dnf") {
                        Ok(Box::new(GenericPkgMgr::dnf()))
                    } else {
                        Ok(Box::new(GenericPkgMgr::yum()))
                    }
                }
                "alpine" => Ok(Box::new(GenericPkgMgr::apk())),
                "arch" | "archlinux" | "manjaro" | "endeavouros" => {
                    Ok(Box::new(GenericPkgMgr::pacman()))
                }
                "opensuse" | "sles" => Ok(Box::new(GenericPkgMgr::zypper())),
                _ => Err(format!(
                    "unsupported OS: family={}, distro={}",
                    os_family, os_distro
                )),
            }
        }
    }
}

fn command_exists(name: &str) -> bool {
    std::process::Command::new("which")
        .arg(name)
        .output()
        .map(|o| o.status.success())
        .unwrap_or(false)
}

/// Generic package manager that wraps common install/remove/query patterns.
/// Covers dnf, yum, apk, pacman, zypper.
pub struct GenericPkgMgr {
    /// Command name (dnf, yum, apk, pacman, zypper).
    cmd: &'static str,
    /// Install args: [cmd] [install_args...] <package>
    install_args: &'static [&'static str],
    /// Remove args.
    remove_args: &'static [&'static str],
    /// Query installed command + args. Exit 0 means installed.
    query_cmd: &'static str,
    query_args: &'static [&'static str],
    /// How to extract version from query output.
    version_parser: fn(&str) -> String,
}

impl GenericPkgMgr {
    fn dnf() -> Self {
        Self {
            cmd: "dnf",
            install_args: &["install", "-y"],
            remove_args: &["remove", "-y"],
            query_cmd: "rpm",
            query_args: &["-q", "--queryformat", "%{VERSION}-%{RELEASE}"],
            version_parser: |s| s.trim().to_string(),
        }
    }

    fn yum() -> Self {
        Self {
            cmd: "yum",
            install_args: &["install", "-y"],
            remove_args: &["remove", "-y"],
            query_cmd: "rpm",
            query_args: &["-q", "--queryformat", "%{VERSION}-%{RELEASE}"],
            version_parser: |s| s.trim().to_string(),
        }
    }

    fn apk() -> Self {
        Self {
            cmd: "apk",
            install_args: &["add", "--no-cache"],
            remove_args: &["del"],
            query_cmd: "apk",
            query_args: &["info", "-e"],
            version_parser: |_| String::new(), // apk info -e just exits 0/1
        }
    }

    fn pacman() -> Self {
        Self {
            cmd: "pacman",
            install_args: &["-S", "--noconfirm"],
            remove_args: &["-R", "--noconfirm"],
            query_cmd: "pacman",
            query_args: &["-Q"],
            version_parser: |s| {
                // Output: "package version"
                s.split_whitespace().nth(1).unwrap_or("").to_string()
            },
        }
    }

    fn zypper() -> Self {
        Self {
            cmd: "zypper",
            install_args: &["install", "-y"],
            remove_args: &["remove", "-y"],
            query_cmd: "rpm",
            query_args: &["-q", "--queryformat", "%{VERSION}-%{RELEASE}"],
            version_parser: |s| s.trim().to_string(),
        }
    }
}

impl PackageManager for GenericPkgMgr {
    fn is_installed(&self, name: &str) -> Result<(bool, String), String> {
        let mut args: Vec<&str> = self.query_args.to_vec();
        args.push(name);
        let output = std::process::Command::new(self.query_cmd)
            .args(&args)
            .output()
            .map_err(|e| format!("query {}: {}", name, e))?;

        if output.status.success() {
            let stdout = String::from_utf8_lossy(&output.stdout);
            let version = (self.version_parser)(&stdout);
            Ok((true, version))
        } else {
            Ok((false, String::new()))
        }
    }

    fn install(&self, name: &str, version: &str) -> Result<(), String> {
        let pkg = if version.is_empty() {
            name.to_string()
        } else {
            // Most package managers accept name-version or name=version.
            match self.cmd {
                "dnf" | "yum" | "zypper" => format!("{}-{}", name, version),
                "pacman" => format!("{}={}", name, version),
                "apk" => format!("{}={}", name, version),
                _ => name.to_string(),
            }
        };

        let mut args: Vec<&str> = self.install_args.to_vec();
        args.push(&pkg);
        let output = std::process::Command::new(self.cmd)
            .args(&args)
            .output()
            .map_err(|e| format!("{} install {}: {}", self.cmd, name, e))?;

        if !output.status.success() {
            let stderr = String::from_utf8_lossy(&output.stderr);
            return Err(format!("{} install {} failed: {}", self.cmd, name, stderr));
        }
        Ok(())
    }

    fn remove(&self, name: &str) -> Result<(), String> {
        let mut args: Vec<&str> = self.remove_args.to_vec();
        args.push(name);
        let output = std::process::Command::new(self.cmd)
            .args(&args)
            .output()
            .map_err(|e| format!("{} remove {}: {}", self.cmd, name, e))?;

        if !output.status.success() {
            let stderr = String::from_utf8_lossy(&output.stderr);
            return Err(format!("{} remove {} failed: {}", self.cmd, name, stderr));
        }
        Ok(())
    }

    fn upgrade(&self, name: &str, version: &str) -> Result<(), String> {
        self.install(name, version)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn detect_debian() {
        let pm = detect("debian", "");
        assert!(pm.is_ok());
    }

    #[test]
    fn detect_ubuntu_by_distro() {
        let pm = detect("", "ubuntu");
        assert!(pm.is_ok());
    }

    #[test]
    fn detect_unsupported() {
        let pm = detect("unknown", "unknown");
        assert!(pm.is_err());
    }
}
