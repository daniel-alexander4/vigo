// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! OS detection and init system identification.

/// The init system used by the host OS.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum InitSystem {
    Systemd,
    Launchd,
    RcD,
    Rcctl,
    Smf,
    Unknown,
}

/// Returns the OS name as a static string (e.g. "linux", "macos", "freebsd").
pub fn os_name() -> &'static str {
    std::env::consts::OS
}

/// Detect the init system available on this host.
pub fn detect_init_system() -> InitSystem {
    match os_name() {
        "macos" => InitSystem::Launchd,
        "openbsd" => {
            if command_exists("rcctl") {
                InitSystem::Rcctl
            } else {
                InitSystem::Unknown
            }
        }
        "freebsd" | "netbsd" => {
            if std::path::Path::new("/etc/rc.d").is_dir() {
                InitSystem::RcD
            } else {
                InitSystem::Unknown
            }
        }
        "illumos" => {
            if command_exists("svcs") {
                InitSystem::Smf
            } else {
                InitSystem::Unknown
            }
        }
        "linux" => {
            if command_exists("systemctl") {
                InitSystem::Systemd
            } else if std::path::Path::new("/etc/rc.d").is_dir() {
                InitSystem::RcD
            } else {
                InitSystem::Unknown
            }
        }
        _ => InitSystem::Unknown,
    }
}

fn command_exists(name: &str) -> bool {
    std::process::Command::new("which")
        .arg(name)
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .status()
        .map(|s| s.success())
        .unwrap_or(false)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn os_name_is_non_empty() {
        assert!(!os_name().is_empty());
    }

    #[test]
    fn detect_init_returns_something() {
        let init = detect_init_system();
        // On Linux CI, should be Systemd; on macOS, Launchd; otherwise at least doesn't panic.
        let _ = init;
    }

    #[test]
    fn init_system_debug() {
        assert_eq!(format!("{:?}", InitSystem::Systemd), "Systemd");
        assert_eq!(format!("{:?}", InitSystem::Launchd), "Launchd");
        assert_eq!(format!("{:?}", InitSystem::RcD), "RcD");
        assert_eq!(format!("{:?}", InitSystem::Rcctl), "Rcctl");
        assert_eq!(format!("{:?}", InitSystem::Smf), "Smf");
        assert_eq!(format!("{:?}", InitSystem::Unknown), "Unknown");
    }
}
