// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Package manager abstraction layer.
//!
//! Provides a [`PackageManager`] trait and auto-detection via [`detect`].
//! Backends: apt, dnf, yum, apk, pacman, zypper, pkg (FreeBSD), pkg_add
//! (OpenBSD), pkgin (NetBSD), brew (macOS).

pub mod apt;
pub mod errors;

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
    /// Downgrade a package to a specific (older) version.
    /// Default implementation falls back to install.
    fn downgrade(&self, name: &str, version: &str) -> Result<(), String> {
        self.install(name, version)
    }
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
            // Check by OS name for non-Linux platforms.
            match std::env::consts::OS {
                "freebsd" => return Ok(Box::new(GenericPkgMgr::pkg())),
                "openbsd" => return Ok(Box::new(GenericPkgMgr::pkg_add())),
                "netbsd" => return Ok(Box::new(GenericPkgMgr::pkgin())),
                "macos" => return Ok(Box::new(GenericPkgMgr::brew())),
                "illumos" => return Ok(Box::new(GenericPkgMgr::ips())),
                _ => {}
            }

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
/// Covers dnf, yum, apk, pacman, zypper, pkg, pkg_add, pkgin, brew.
pub struct GenericPkgMgr {
    /// Command name for install (dnf, yum, apk, pacman, zypper, pkg, pkg_add, pkgin, brew).
    cmd: &'static str,
    /// Install args: [cmd] [install_args...] <package>
    install_args: &'static [&'static str],
    /// Command name for remove (usually same as cmd, except pkg_add→pkg_delete).
    remove_cmd: &'static str,
    /// Remove args.
    remove_args: &'static [&'static str],
    /// Query installed command + args. Exit 0 means installed.
    query_cmd: &'static str,
    query_args: &'static [&'static str],
    /// How to extract version from query output.
    version_parser: fn(&str) -> String,
    /// Optional downgrade args. If set, `downgrade()` runs `[cmd] [downgrade_args...] <pkg>`.
    /// If None, downgrade falls back to install.
    downgrade_args: Option<&'static [&'static str]>,
}

impl GenericPkgMgr {
    fn dnf() -> Self {
        Self {
            cmd: "dnf",
            install_args: &["install", "-y"],
            remove_cmd: "dnf",
            remove_args: &["remove", "-y"],
            query_cmd: "rpm",
            query_args: &["-q", "--queryformat", "%{VERSION}-%{RELEASE}"],
            version_parser: |s| s.trim().to_string(),
            downgrade_args: Some(&["downgrade", "-y"]),
        }
    }

    fn yum() -> Self {
        Self {
            cmd: "yum",
            install_args: &["install", "-y"],
            remove_cmd: "yum",
            remove_args: &["remove", "-y"],
            query_cmd: "rpm",
            query_args: &["-q", "--queryformat", "%{VERSION}-%{RELEASE}"],
            version_parser: |s| s.trim().to_string(),
            downgrade_args: Some(&["downgrade", "-y"]),
        }
    }

    fn apk() -> Self {
        Self {
            cmd: "apk",
            install_args: &["add", "--no-cache"],
            remove_cmd: "apk",
            remove_args: &["del"],
            query_cmd: "apk",
            query_args: &["info", "-e"],
            version_parser: |_| String::new(), // apk info -e just exits 0/1
            downgrade_args: None,
        }
    }

    fn pacman() -> Self {
        Self {
            cmd: "pacman",
            install_args: &["-S", "--noconfirm"],
            remove_cmd: "pacman",
            remove_args: &["-R", "--noconfirm"],
            query_cmd: "pacman",
            query_args: &["-Q"],
            version_parser: |s| {
                // Output: "package version"
                s.split_whitespace().nth(1).unwrap_or("").to_string()
            },
            downgrade_args: None,
        }
    }

    fn zypper() -> Self {
        Self {
            cmd: "zypper",
            install_args: &["install", "-y"],
            remove_cmd: "zypper",
            remove_args: &["remove", "-y"],
            query_cmd: "rpm",
            query_args: &["-q", "--queryformat", "%{VERSION}-%{RELEASE}"],
            version_parser: |s| s.trim().to_string(),
            downgrade_args: None,
        }
    }

    fn pkg() -> Self {
        Self {
            cmd: "pkg",
            install_args: &["install", "-y"],
            remove_cmd: "pkg",
            remove_args: &["delete", "-y"],
            query_cmd: "pkg",
            query_args: &["info"],
            version_parser: |s| {
                // pkg info output: "name-version\n..."
                // The version is after the last hyphen in the first line.
                s.lines()
                    .next()
                    .and_then(|line| line.rsplit_once('-'))
                    .map(|(_, v)| v.to_string())
                    .unwrap_or_default()
            },
            downgrade_args: None,
        }
    }

    fn pkg_add() -> Self {
        Self {
            cmd: "pkg_add",
            install_args: &[],
            remove_cmd: "pkg_delete",
            remove_args: &[],
            query_cmd: "pkg_info",
            query_args: &[],
            version_parser: |s| {
                // pkg_info <name> output: "name-version ..."
                s.lines()
                    .next()
                    .and_then(|line| {
                        let word = line.split_whitespace().next()?;
                        word.rsplit_once('-').map(|(_, v)| v.to_string())
                    })
                    .unwrap_or_default()
            },
            downgrade_args: None,
        }
    }

    fn pkgin() -> Self {
        Self {
            cmd: "pkgin",
            install_args: &["-y", "install"],
            remove_cmd: "pkgin",
            remove_args: &["-y", "remove"],
            query_cmd: "pkg_info",
            query_args: &["-e"],
            version_parser: |_| String::new(), // -e just exits 0/1
            downgrade_args: None,
        }
    }

    fn ips() -> Self {
        Self {
            cmd: "pkg",
            install_args: &["install", "--accept"],
            remove_cmd: "pkg",
            remove_args: &["uninstall"],
            query_cmd: "pkg",
            query_args: &["list", "-H"],
            version_parser: |s| {
                // pkg list -H output: "NAME (PUBLISHER)  VERSION  IFO"
                // or: "NAME  VERSION  IFO"
                s.lines()
                    .next()
                    .and_then(|line| {
                        let parts: Vec<&str> = line.split_whitespace().collect();
                        // Version is typically the second-to-last or second field.
                        if parts.len() >= 2 {
                            Some(parts[1].to_string())
                        } else {
                            None
                        }
                    })
                    .unwrap_or_default()
            },
            downgrade_args: None,
        }
    }

    fn brew() -> Self {
        Self {
            cmd: "brew",
            install_args: &["install"],
            remove_cmd: "brew",
            remove_args: &["uninstall"],
            query_cmd: "brew",
            query_args: &["list", "--versions"],
            version_parser: |s| {
                // "name version1 version2" — take first version
                s.split_whitespace().nth(1).unwrap_or("").to_string()
            },
            downgrade_args: None,
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
                "pacman" | "apk" | "pkgin" => format!("{}={}", name, version),
                "brew" => format!("{}@{}", name, version),
                // IPS uses @ for versioning when cmd is "pkg" on illumos.
                "pkg" if std::env::consts::OS == "illumos" => format!("{}@{}", name, version),
                "pkg" => format!("{}-{}", name, version),
                "pkg_add" => format!("{}-{}", name, version),
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
        let output = std::process::Command::new(self.remove_cmd)
            .args(&args)
            .output()
            .map_err(|e| format!("{} remove {}: {}", self.remove_cmd, name, e))?;

        if !output.status.success() {
            let stderr = String::from_utf8_lossy(&output.stderr);
            return Err(format!("{} remove {} failed: {}", self.remove_cmd, name, stderr));
        }
        Ok(())
    }

    fn upgrade(&self, name: &str, version: &str) -> Result<(), String> {
        self.install(name, version)
    }

    fn downgrade(&self, name: &str, version: &str) -> Result<(), String> {
        let Some(dg_args) = self.downgrade_args else {
            return self.install(name, version);
        };

        let pkg = if version.is_empty() {
            name.to_string()
        } else {
            match self.cmd {
                "dnf" | "yum" | "zypper" => format!("{}-{}", name, version),
                _ => format!("{}={}", name, version),
            }
        };

        let mut args: Vec<&str> = dg_args.to_vec();
        args.push(&pkg);
        let output = std::process::Command::new(self.cmd)
            .args(&args)
            .output()
            .map_err(|e| format!("{} downgrade {}: {}", self.cmd, name, e))?;

        if !output.status.success() {
            let stderr = String::from_utf8_lossy(&output.stderr);
            return Err(format!("{} downgrade {} failed: {}", self.cmd, name, stderr));
        }
        Ok(())
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
