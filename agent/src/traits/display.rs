// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Display server trait collector.
//!
//! Detects whether the machine has a graphical display server available
//! (X11, Wayland, or both). Used by `has_display()` when: expressions to
//! skip GUI application modules on headless servers.
//!
//! Detection is zero-overhead: checks filesystem paths only, no subprocesses.

use anyhow::Result;
use serde_json::{json, Value};

pub fn collect() -> Result<Value> {
    let server = detect_display_server();
    Ok(json!({
        "server": server,
    }))
}

#[cfg(target_os = "macos")]
fn detect_display_server() -> &'static str {
    // macOS always has Quartz (the native display server).
    "quartz"
}

#[cfg(windows)]
fn detect_display_server() -> &'static str {
    // Windows always has a display server (DWM/GDI).
    "windows"
}

#[cfg(not(any(target_os = "macos", windows)))]
fn detect_display_server() -> &'static str {
    // Linux, FreeBSD, OpenBSD, NetBSD, illumos — check for X11 and Wayland.
    let has_x11 = detect_x11();
    let has_wayland = detect_wayland();

    match (has_x11, has_wayland) {
        (true, true) => "both",
        (true, false) => "x11",
        (false, true) => "wayland",
        (false, false) => "none",
    }
}

#[cfg(not(any(target_os = "macos", windows)))]
fn detect_x11() -> bool {
    use std::path::Path;
    // Xorg server binary.
    if Path::new("/usr/bin/Xorg").exists() || Path::new("/usr/bin/X").exists() {
        return true;
    }
    // FreeBSD/OpenBSD: Xorg may be in /usr/local/bin.
    if Path::new("/usr/local/bin/Xorg").exists() || Path::new("/usr/local/bin/X").exists() {
        return true;
    }
    // X11 session files (installed by display managers for X sessions).
    dir_has_entries("/usr/share/xsessions")
}

#[cfg(not(any(target_os = "macos", windows)))]
fn detect_wayland() -> bool {
    use std::path::Path;
    // Wayland session files (installed by display managers for Wayland sessions).
    if dir_has_entries("/usr/share/wayland-sessions") {
        return true;
    }
    // Xwayland binary (present when Wayland compositors are installed).
    Path::new("/usr/bin/Xwayland").exists() || Path::new("/usr/local/bin/Xwayland").exists()
}

#[cfg(not(any(target_os = "macos", windows)))]
fn dir_has_entries(path: &str) -> bool {
    match std::fs::read_dir(path) {
        Ok(mut entries) => entries.next().is_some(),
        Err(_) => false,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_collect_returns_valid_server() {
        let result = collect().unwrap();
        let server = result["server"].as_str().unwrap();
        assert!(
            ["x11", "wayland", "both", "none", "quartz", "windows"].contains(&server),
            "unexpected display server value: {server}"
        );
    }
}
