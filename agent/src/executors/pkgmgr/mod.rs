// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Package manager abstraction layer.
//!
//! Provides a [`PackageManager`] trait and auto-detection via [`detect`].
//! Backends: apt, dnf, yum, apk, pacman, zypper, pkg (FreeBSD), pkg_add
//! (OpenBSD), pkgin (NetBSD), brew (macOS).

pub mod apt;
pub mod errors;

// Discipline constants (PKG_NICE, PKG_QUERY_TIMEOUT, PKG_MUTATE_TIMEOUT) are
// single-sourced in the parent `executors` module — shared with the
// package/repository/source executors so the budgets can't drift.
use super::{PKG_MUTATE_TIMEOUT, PKG_NICE, PKG_QUERY_TIMEOUT};

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
    /// Purge a package (remove including config files).
    /// Default implementation falls back to remove.
    fn purge(&self, name: &str) -> Result<(), String> {
        self.remove(name)
    }
    /// Update the package cache before install/upgrade operations.
    /// Default implementation is a no-op (not all managers need it).
    fn update_cache(&self) -> Result<(), String> {
        Ok(())
    }
    /// Upgrade a package to the latest available version (no specific target).
    /// Default implementation calls upgrade with an empty version string.
    fn upgrade_latest(&self, name: &str) -> Result<(), String> {
        self.upgrade(name, "")
    }
    /// Hold (lock) a package at its current version.
    /// Default implementation is a no-op.
    fn hold(&self, name: &str) -> Result<(), String> {
        let _ = name;
        Ok(())
    }
    /// Unhold (unlock) a package version lock.
    /// Default implementation is a no-op.
    fn unhold(&self, name: &str) -> Result<(), String> {
        let _ = name;
        Ok(())
    }
    /// Upgrade all installed packages to their latest available versions.
    /// Used when `package: "*"` with `state: latest`.
    fn upgrade_all(&self) -> Result<(), String> {
        Err("upgrade_all not supported by this package manager".to_string())
    }
    /// Snapshot every installed package + version as a stable, sorted
    /// string. Callers diff the before/after snapshots around an upgrade
    /// to decide whether anything actually changed. Default errors — a
    /// backend without a snapshot degrades to "assume changed".
    fn installed_snapshot(&self) -> Result<String, String> {
        Err("installed_snapshot not supported by this package manager".to_string())
    }
    /// Remove orphaned dependencies after install/upgrade.
    /// APT: `apt-get autoremove -y`, DNF: `dnf autoremove -y`, etc.
    fn post_cleanup(&self) -> Result<(), String> {
        Ok(()) // no-op for backends that don't support it
    }
    /// Set extra flags to append to install/upgrade/remove commands.
    /// Default implementation is a no-op (flags are stored per-backend).
    /// Uses interior mutability so it can be called from &self contexts.
    fn set_flags(&self, _flags: &[String]) {}
    /// Clear any previously set extra flags.
    fn clear_flags(&self) {
        self.set_flags(&[]);
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

/// Run a package-manager command with the house discipline: nice+ionice
/// (PKG_NICE), own process group, oom-adj, output drained concurrently,
/// bounded by `timeout` with a SIGTERM→SIGKILL tree kill. `envs` are set on
/// the child. A timeout surfaces as `Err` (`io::ErrorKind::TimedOut`) —
/// callers must NEVER fold it into a "non-zero exit means not installed"
/// interpretation (see `AptManager::parse_dpkg_status`).
fn run_pkg(
    program: &str,
    args: &[&str],
    envs: &[(&str, &str)],
    timeout: std::time::Duration,
) -> std::io::Result<std::process::Output> {
    let mut cmd = std::process::Command::new(program);
    cmd.args(args);
    for (k, v) in envs {
        cmd.env(k, v);
    }
    super::run_built_command_niced_timeout(cmd, program, PKG_NICE, timeout)
}

fn command_exists(name: &str) -> bool {
    run_pkg("which", &[name], &[], PKG_QUERY_TIMEOUT)
        .map(|o| o.status.success())
        .unwrap_or(false)
}

/// Normalize package-list output into a stable, comparable string: trim
/// each line, drop blanks, sort. Lets a raw before/after diff detect a
/// real change without tripping on output ordering.
fn normalize_snapshot(raw: &str) -> String {
    let mut lines: Vec<&str> = raw
        .lines()
        .map(str::trim_end)
        .filter(|l| !l.is_empty())
        .collect();
    lines.sort_unstable();
    lines.join("\n")
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
    /// Optional upgrade args (for `state: latest` without a target version).
    /// If None, falls back to install args.
    upgrade_args: Option<&'static [&'static str]>,
    /// Optional cache update command + args. If set, `update_cache()` runs these before install.
    cache_update_args: Option<&'static [&'static str]>,
    /// Optional hold command + args. `hold()` runs `[hold_cmd] [hold_args...] <pkg>`.
    hold_cmd: Option<&'static str>,
    hold_args: Option<&'static [&'static str]>,
    /// Optional unhold command + args.
    unhold_cmd: Option<&'static str>,
    unhold_args: Option<&'static [&'static str]>,
    /// Optional cleanup args (autoremove orphaned deps). If set, `post_cleanup()` runs `[cmd] [cleanup_args...]`.
    cleanup_args: Option<&'static [&'static str]>,
    /// Args for "upgrade everything" when they differ from `upgrade_args`
    /// (which targets a named package — e.g. apk's `add --upgrade`, or
    /// pacman/pkg_add which have no named-upgrade form). When `None`,
    /// `upgrade_all` falls back to `upgrade_args`.
    upgrade_all_args: Option<&'static [&'static str]>,
    /// Args for listing every installed package with its version, run as
    /// `[query_cmd] [snapshot_args...]`. Used to snapshot state before and
    /// after an upgrade so `state: latest` can report `changed` honestly.
    snapshot_args: &'static [&'static str],
    /// Extra flags appended to install/upgrade/remove commands.
    /// Mutex for interior mutability — set per-invocation from the executor wrapper.
    extra_flags: std::sync::Mutex<Vec<String>>,
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
            upgrade_args: Some(&["upgrade", "-y"]),
            cache_update_args: Some(&["check-update"]),
            hold_cmd: Some("dnf"),
            hold_args: Some(&["versionlock", "add"]),
            unhold_cmd: Some("dnf"),
            unhold_args: Some(&["versionlock", "delete"]),
            cleanup_args: Some(&["autoremove", "-y"]),
            upgrade_all_args: None,
            snapshot_args: &["-qa"],
            extra_flags: std::sync::Mutex::new(Vec::new()),
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
            upgrade_args: Some(&["upgrade", "-y"]),
            cache_update_args: Some(&["check-update"]),
            hold_cmd: Some("yum"),
            hold_args: Some(&["versionlock", "add"]),
            unhold_cmd: Some("yum"),
            unhold_args: Some(&["versionlock", "delete"]),
            cleanup_args: Some(&["autoremove", "-y"]),
            upgrade_all_args: None,
            snapshot_args: &["-qa"],
            extra_flags: std::sync::Mutex::new(Vec::new()),
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
            upgrade_args: Some(&["add", "--no-cache", "--upgrade"]),
            cache_update_args: Some(&["update"]),
            hold_cmd: None,
            hold_args: None,
            unhold_cmd: None,
            unhold_args: None,
            cleanup_args: None,
            upgrade_all_args: Some(&["upgrade"]),
            snapshot_args: &["list", "-I"],
            extra_flags: std::sync::Mutex::new(Vec::new()),
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
            upgrade_args: None, // pacman -S already upgrades
            cache_update_args: Some(&["-Sy", "--noconfirm"]),
            hold_cmd: None,
            hold_args: None,
            unhold_cmd: None,
            unhold_args: None,
            cleanup_args: None,
            upgrade_all_args: Some(&["-Syu", "--noconfirm"]),
            snapshot_args: &["-Q"],
            extra_flags: std::sync::Mutex::new(Vec::new()),
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
            upgrade_args: Some(&["update", "-y"]),
            cache_update_args: Some(&["refresh"]),
            hold_cmd: Some("zypper"),
            hold_args: Some(&["addlock"]),
            unhold_cmd: Some("zypper"),
            unhold_args: Some(&["removelock"]),
            cleanup_args: None,
            upgrade_all_args: None,
            snapshot_args: &["-qa"],
            extra_flags: std::sync::Mutex::new(Vec::new()),
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
            upgrade_args: Some(&["upgrade", "-y"]),
            cache_update_args: Some(&["update"]),
            hold_cmd: Some("pkg"),
            hold_args: Some(&["lock"]),
            unhold_cmd: Some("pkg"),
            unhold_args: Some(&["unlock"]),
            cleanup_args: Some(&["autoremove", "-y"]),
            upgrade_all_args: None,
            snapshot_args: &["info"],
            extra_flags: std::sync::Mutex::new(Vec::new()),
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
            upgrade_args: None,
            cache_update_args: None,
            hold_cmd: None,
            hold_args: None,
            unhold_cmd: None,
            unhold_args: None,
            cleanup_args: None,
            upgrade_all_args: Some(&["-u"]),
            snapshot_args: &[],
            extra_flags: std::sync::Mutex::new(Vec::new()),
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
            upgrade_args: Some(&["-y", "upgrade"]),
            cache_update_args: Some(&["update"]),
            hold_cmd: None,
            hold_args: None,
            unhold_cmd: None,
            unhold_args: None,
            cleanup_args: Some(&["-y", "autoremove"]),
            upgrade_all_args: None,
            snapshot_args: &[],
            extra_flags: std::sync::Mutex::new(Vec::new()),
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
            upgrade_args: Some(&["update", "--accept"]),
            cache_update_args: Some(&["refresh"]),
            hold_cmd: Some("pkg"),
            hold_args: Some(&["freeze"]),
            unhold_cmd: Some("pkg"),
            unhold_args: Some(&["unfreeze"]),
            cleanup_args: None,
            upgrade_all_args: None,
            snapshot_args: &["list", "-H"],
            extra_flags: std::sync::Mutex::new(Vec::new()),
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
            upgrade_args: Some(&["upgrade"]),
            cache_update_args: Some(&["update"]),
            hold_cmd: Some("brew"),
            hold_args: Some(&["pin"]),
            unhold_cmd: Some("brew"),
            unhold_args: Some(&["unpin"]),
            cleanup_args: Some(&["cleanup"]),
            upgrade_all_args: None,
            snapshot_args: &["list", "--versions"],
            extra_flags: std::sync::Mutex::new(Vec::new()),
        }
    }
}

impl GenericPkgMgr {
    /// Format a package spec with an optional version for this package manager.
    fn format_pkg(&self, name: &str, version: &str) -> String {
        if version.is_empty() {
            return name.to_string();
        }
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
    }

    /// Build args from static base args + extra_flags + the package spec.
    fn build_args(&self, base: &[&str], pkg: &str) -> Vec<String> {
        let mut args: Vec<String> = base.iter().map(|s| s.to_string()).collect();
        let flags = self.extra_flags.lock().unwrap();
        for f in flags.iter() {
            args.push(f.clone());
        }
        args.push(pkg.to_string());
        args
    }
}

impl PackageManager for GenericPkgMgr {
    fn is_installed(&self, name: &str) -> Result<(bool, String), String> {
        let mut args: Vec<&str> = self.query_args.to_vec();
        args.push(name);
        let output = run_pkg(self.query_cmd, &args, &[], PKG_QUERY_TIMEOUT)
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
        let pkg = self.format_pkg(name, version);
        let args = self.build_args(self.install_args, &pkg);
        let str_args: Vec<&str> = args.iter().map(|s| s.as_str()).collect();
        let output = run_pkg(self.cmd, &str_args, &[], PKG_MUTATE_TIMEOUT)
            .map_err(|e| format!("{} install {}: {}", self.cmd, name, e))?;

        if !output.status.success() {
            let stderr = String::from_utf8_lossy(&output.stderr);
            return Err(format!("{} install {} failed: {}", self.cmd, name, stderr));
        }
        Ok(())
    }

    fn remove(&self, name: &str) -> Result<(), String> {
        let mut args: Vec<String> = self.remove_args.iter().map(|s| s.to_string()).collect();
        {
            let flags = self.extra_flags.lock().unwrap();
            for f in flags.iter() {
                args.push(f.clone());
            }
        }
        args.push(name.to_string());
        let str_args: Vec<&str> = args.iter().map(|s| s.as_str()).collect();
        let output = run_pkg(self.remove_cmd, &str_args, &[], PKG_MUTATE_TIMEOUT)
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

        let pkg = self.format_pkg(name, version);
        let args = self.build_args(dg_args, &pkg);
        let str_args: Vec<&str> = args.iter().map(|s| s.as_str()).collect();
        let output = run_pkg(self.cmd, &str_args, &[], PKG_MUTATE_TIMEOUT)
            .map_err(|e| format!("{} downgrade {}: {}", self.cmd, name, e))?;

        if !output.status.success() {
            let stderr = String::from_utf8_lossy(&output.stderr);
            return Err(format!("{} downgrade {} failed: {}", self.cmd, name, stderr));
        }
        Ok(())
    }

    fn update_cache(&self) -> Result<(), String> {
        let Some(cache_args) = self.cache_update_args else {
            return Ok(());
        };

        let output = run_pkg(self.cmd, cache_args, &[], PKG_MUTATE_TIMEOUT)
            .map_err(|e| format!("{} update cache: {}", self.cmd, e))?;

        // dnf/yum check-update exits 100 when updates are available — not an error.
        let ok = output.status.success()
            || (matches!(self.cmd, "dnf" | "yum")
                && output.status.code() == Some(100));

        if !ok {
            let stderr = String::from_utf8_lossy(&output.stderr);
            return Err(format!("{} update cache failed: {}", self.cmd, stderr));
        }
        Ok(())
    }

    fn upgrade_latest(&self, name: &str) -> Result<(), String> {
        let Some(ug_args) = self.upgrade_args else {
            return self.install(name, "");
        };

        let args = self.build_args(ug_args, name);
        let str_args: Vec<&str> = args.iter().map(|s| s.as_str()).collect();
        let output = run_pkg(self.cmd, &str_args, &[], PKG_MUTATE_TIMEOUT)
            .map_err(|e| format!("{} upgrade {}: {}", self.cmd, name, e))?;

        if !output.status.success() {
            let stderr = String::from_utf8_lossy(&output.stderr);
            return Err(format!("{} upgrade {} failed: {}", self.cmd, name, stderr));
        }
        Ok(())
    }

    fn hold(&self, name: &str) -> Result<(), String> {
        let (Some(cmd), Some(args)) = (self.hold_cmd, self.hold_args) else {
            return Ok(());
        };
        let mut full_args: Vec<&str> = args.to_vec();
        full_args.push(name);
        let output = run_pkg(cmd, &full_args, &[], PKG_MUTATE_TIMEOUT)
            .map_err(|e| format!("{} hold {}: {}", cmd, name, e))?;

        if !output.status.success() {
            let stderr = String::from_utf8_lossy(&output.stderr);
            return Err(format!("{} hold {} failed: {}", cmd, name, stderr));
        }
        Ok(())
    }

    fn unhold(&self, name: &str) -> Result<(), String> {
        let (Some(cmd), Some(args)) = (self.unhold_cmd, self.unhold_args) else {
            return Ok(());
        };
        let mut full_args: Vec<&str> = args.to_vec();
        full_args.push(name);
        let output = run_pkg(cmd, &full_args, &[], PKG_MUTATE_TIMEOUT)
            .map_err(|e| format!("{} unhold {}: {}", cmd, name, e))?;

        if !output.status.success() {
            let stderr = String::from_utf8_lossy(&output.stderr);
            return Err(format!("{} unhold {} failed: {}", cmd, name, stderr));
        }
        Ok(())
    }

    fn upgrade_all(&self) -> Result<(), String> {
        // Prefer a dedicated upgrade-all command; fall back to upgrade_args
        // for backends whose named-upgrade form upgrades everything when
        // given no package (dnf, pkg, brew, ...).
        let Some(ug_args) = self.upgrade_all_args.or(self.upgrade_args) else {
            return Err(format!("{}: upgrade_all not supported", self.cmd));
        };

        // Build args without a package name — upgrade everything.
        let mut args: Vec<String> = ug_args.iter().map(|s| s.to_string()).collect();
        let flags = self.extra_flags.lock().unwrap();
        for f in flags.iter() {
            args.push(f.clone());
        }

        let str_args: Vec<&str> = args.iter().map(|s| s.as_str()).collect();
        let output = run_pkg(self.cmd, &str_args, &[], PKG_MUTATE_TIMEOUT)
            .map_err(|e| format!("{} upgrade all: {}", self.cmd, e))?;

        if !output.status.success() {
            let stderr = String::from_utf8_lossy(&output.stderr);
            return Err(format!("{} upgrade all failed: {}", self.cmd, stderr));
        }
        Ok(())
    }

    fn post_cleanup(&self) -> Result<(), String> {
        let Some(args) = self.cleanup_args else {
            return Ok(());
        };

        let output = run_pkg(self.cmd, args, &[], PKG_MUTATE_TIMEOUT)
            .map_err(|e| format!("{} post_cleanup: {}", self.cmd, e))?;

        if !output.status.success() {
            let stderr = String::from_utf8_lossy(&output.stderr);
            return Err(format!("{} post_cleanup failed: {}", self.cmd, stderr));
        }
        Ok(())
    }

    fn installed_snapshot(&self) -> Result<String, String> {
        let output = run_pkg(self.query_cmd, self.snapshot_args, &[], PKG_QUERY_TIMEOUT)
            .map_err(|e| format!("{} snapshot: {}", self.query_cmd, e))?;

        if !output.status.success() {
            let stderr = String::from_utf8_lossy(&output.stderr);
            return Err(format!("{} snapshot failed: {}", self.query_cmd, stderr));
        }
        Ok(normalize_snapshot(&String::from_utf8_lossy(&output.stdout)))
    }

    fn set_flags(&self, flags: &[String]) {
        *self.extra_flags.lock().unwrap() = flags.to_vec();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[cfg(unix)]
    #[test]
    fn run_pkg_kills_wedged_command() {
        // A wedged package command (held dpkg lock, dead mirror) must be
        // killed at the deadline, not hang the converge thread forever.
        let start = std::time::Instant::now();
        let err = run_pkg("sleep", &["60"], &[], std::time::Duration::from_secs(1)).unwrap_err();
        assert_eq!(err.kind(), std::io::ErrorKind::TimedOut);
        // 1s budget + 5s SIGTERM grace + slack — nowhere near the sleep's 60s.
        assert!(start.elapsed() < std::time::Duration::from_secs(20));
    }

    #[cfg(unix)]
    #[test]
    fn run_pkg_sets_envs_and_passes_output() {
        let out = run_pkg(
            "sh",
            &["-c", "printf %s \"$DEBIAN_FRONTEND\""],
            &[("DEBIAN_FRONTEND", "noninteractive")],
            std::time::Duration::from_secs(10),
        )
        .unwrap();
        assert!(out.status.success());
        assert_eq!(String::from_utf8_lossy(&out.stdout), "noninteractive");
    }

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

    #[test]
    fn upgrade_all_args_set_for_compound_managers() {
        // apk/pacman/pkg_add can't upgrade-all via their named-upgrade
        // form, so each must carry a dedicated upgrade_all_args.
        assert_eq!(GenericPkgMgr::apk().upgrade_all_args, Some(&["upgrade"][..]));
        assert_eq!(
            GenericPkgMgr::pacman().upgrade_all_args,
            Some(&["-Syu", "--noconfirm"][..]),
        );
        assert_eq!(GenericPkgMgr::pkg_add().upgrade_all_args, Some(&["-u"][..]));
        // Managers whose upgrade_args already upgrades everything keep None.
        assert_eq!(GenericPkgMgr::dnf().upgrade_all_args, None);
        assert_eq!(GenericPkgMgr::brew().upgrade_all_args, None);
    }
}
