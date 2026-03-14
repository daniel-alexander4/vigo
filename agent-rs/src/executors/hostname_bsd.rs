// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Hostname executor for BSD (FreeBSD/NetBSD) — writes `/etc/rc.conf` via sysrc (FreeBSD)
//! or `/etc/myname` (OpenBSD/NetBSD).

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::Instant;

pub struct HostnameBsdExecutor {
    pub runner: Box<dyn CommandRunner>,
}

impl HostnameBsdExecutor {
    pub fn new() -> Self {
        Self { runner: Box::new(DefaultRunner) }
    }

    fn current_hostname(&self) -> String {
        hostname::get()
            .map(|h| h.to_string_lossy().to_string())
            .unwrap_or_default()
    }
}

impl Executor for HostnameBsdExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let desired = params.get("hostname").or_else(|| params.get("name")).cloned().unwrap_or_default();
        let mut result = ResourceResult::new("hostname", &desired);

        if desired.is_empty() {
            result.error = Some("hostname executor: hostname param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        if self.current_hostname() == desired {
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        result.action = Action::Updated;
        result.changed = true;

        if dry_run {
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        // Set runtime hostname.
        if let Ok(o) = self.runner.run("hostname", &[&desired]) {
            if !o.status.success() {
                let stderr = String::from_utf8_lossy(&o.stderr);
                result.error = Some(format!("hostname: {}", stderr));
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
        }

        // Persist based on OS.
        match std::env::consts::OS {
            "freebsd" => {
                // sysrc hostname="desired"
                if let Ok(o) = self.runner.run("sysrc", &[&format!("hostname={}", desired)]) {
                    if !o.status.success() {
                        let stderr = String::from_utf8_lossy(&o.stderr);
                        result.error = Some(format!("sysrc: {}", stderr));
                    }
                }
            }
            "openbsd" | "netbsd" => {
                // Write /etc/myname
                if let Err(e) = std::fs::write("/etc/myname", format!("{}\n", desired)) {
                    result.error = Some(format!("write /etc/myname: {}", e));
                }
            }
            _ => {
                // Generic: try /etc/hostname
                if let Err(e) = std::fs::write("/etc/hostname", format!("{}\n", desired)) {
                    result.error = Some(format!("write /etc/hostname: {}", e));
                }
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
    fn missing_hostname_errors() {
        let exec = HostnameBsdExecutor::new();
        let result = exec.execute(&HashMap::new(), false);
        assert!(result.error.is_some());
    }
}
