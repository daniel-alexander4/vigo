// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0

//! Self-protection guardrails — hardcoded, non-configurable by the server.
//!
//! Prevents configs or ad-hoc tasks from destroying the hobnob agent, server,
//! or their dependencies on managed nodes. The agent enforces these regardless
//! of what the server sends.

use std::collections::HashMap;

/// Paths that must never be deleted, overwritten, or have permissions changed
/// by any resource. A resource targets a protected path if its target is equal
/// to or is a child of any entry here.
#[cfg(unix)]
const PROTECTED_PATHS: &[&str] = &[
    // Agent (Linux)
    "/usr/local/sbin/hobnob",
    "/etc/hobnob-thrall/",
    "/etc/hobnob/",
    "/var/lib/hobnob/",
    "/etc/systemd/system/hobnob-thrall.service",
    // Agent (macOS)
    "/usr/local/etc/hobnob-thrall/",
    "/Library/Hobnob/",
    "/Library/LaunchDaemons/com.hobnob.thrall.plist",
    // Agent (FreeBSD)
    "/usr/local/etc/rc.d/hobnob_thrall",
    // Agent (illumos)
    "/var/svc/manifest/system/hobnob-thrall.xml",
    // Server
    "/usr/local/sbin/hobd",
    "/usr/local/bin/hobnobcli",
    "/usr/local/sbin/hobnob-license-manager",
    "/srv/hobnob/",
    "/etc/systemd/system/hobd.service",
];

#[cfg(windows)]
const PROTECTED_PATHS: &[&str] = &[
    // Agent (Windows standard paths)
    r"C:\Program Files\Hobnob\",
    r"C:\ProgramData\Hobnob\",
];

/// Services that must never be stopped, disabled, or masked.
const PROTECTED_SERVICES: &[&str] = &["hobnob-thrall", "hobd"];

/// Packages that must never be removed.
const PROTECTED_PACKAGES: &[&str] = &["hobnob", "hobd", "hobnobcli"];

/// Dangerous command fragments that target hobnob processes or files.
/// Used for heuristic scanning of exec commands and ad-hoc tasks.
#[cfg(unix)]
const DANGEROUS_PATTERNS: &[&str] = &[
    // systemd (Linux)
    "systemctl stop hobnob",
    "systemctl disable hobnob",
    "systemctl mask hobnob",
    "systemctl stop hobd",
    "systemctl disable hobd",
    "systemctl mask hobd",
    // rc.d / service (BSD)
    "service hobnob-thrall stop",
    "service hobnob_thrall stop",
    "service hobd stop",
    // rcctl (OpenBSD)
    "rcctl stop hobnob",
    "rcctl disable hobnob",
    // launchctl (macOS)
    "launchctl unload com.hobnob",
    "launchctl remove com.hobnob",
    "launchctl bootout com.hobnob",
    // SMF (illumos)
    "svcadm disable hobnob",
    "svccfg delete hobnob",
    // process kill
    "pkill hobnob",
    "pkill hobd",
    "killall hobnob",
    "killall hobd",
    "kill -9 hobnob",
    "kill -9 hobd",
    // file removal
    "rm /usr/local/sbin/hobnob",
    "rm /usr/local/sbin/hobd",
    "rm /usr/local/bin/hobnobcli",
    "rm /usr/local/sbin/hobnob-license-manager",
    "rm -rf /etc/hobnob",
    "rm -rf /var/lib/hobnob",
    "rm -rf /srv/hobnob",
    "rm -rf /etc/hobnob-thrall",
    "rm -rf /library/hobnob",
];

#[cfg(windows)]
const DANGEROUS_PATTERNS: &[&str] = &[
    "sc stop hobnob",
    "sc delete hobnob",
    "taskkill /im hobnob",
    "del /f hobnob.exe",
    "rmdir /s hobnob",
];

/// Result of a guardrail check.
#[derive(Debug, Clone)]
pub struct Violation {
    pub resource_name: String,
    pub resource_type: String,
    pub reason: String,
}

impl std::fmt::Display for Violation {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(
            f,
            "GUARDRAIL BLOCKED {}/{}: {}",
            self.resource_type, self.resource_name, self.reason
        )
    }
}

/// Check whether a resource violates self-protection guardrails.
///
/// Returns `Some(Violation)` if the resource must be blocked, `None` if safe.
pub fn check_resource(
    resource_name: &str,
    resource_type: &str,
    params: &HashMap<String, String>,
) -> Option<Violation> {
    match resource_type {
        "file" | "directory" | "symlink" | "file_line" | "blockinfile" | "ini" | "json_file" | "replace" | "field_edit" => {
            check_file_resource(resource_name, resource_type, params)
        }
        "service" => check_service_resource(resource_name, resource_type, params),
        "package" => check_package_resource(resource_name, resource_type, params),
        "exec" => check_exec_resource(resource_name, resource_type, params),
        _ => None,
    }
}

/// Check whether an ad-hoc push command violates guardrails.
///
/// Returns `Some(reason)` if the command must be rejected.
pub fn check_push_command(command: &str) -> Option<String> {
    let lower = command.to_lowercase();
    for pattern in DANGEROUS_PATTERNS {
        if lower.contains(pattern) {
            return Some(format!(
                "command matches dangerous pattern: {:?}",
                pattern
            ));
        }
    }

    // Check for rm/unlink targeting protected paths.
    for path in PROTECTED_PATHS {
        let trimmed = path.trim_end_matches('/');
        if (lower.contains("rm ") || lower.contains("unlink ")) && lower.contains(trimmed) {
            return Some(format!("command targets protected path: {}", path));
        }
    }

    None
}

/// Resolve a path to its canonical form for guardrail checking.
/// If the full path exists, canonicalize it. If not, try canonicalizing
/// the parent and appending the filename (handles files that don't exist yet).
/// Falls back to the raw path if nothing resolves.
fn resolve_path(path: &str) -> String {
    use std::path::Path;
    let p = Path::new(path);
    if let Ok(canon) = p.canonicalize() {
        return canon.to_string_lossy().to_string();
    }
    // File doesn't exist yet — try canonicalizing the parent directory.
    if let Some(parent) = p.parent() {
        if let Ok(canon_parent) = parent.canonicalize() {
            if let Some(file_name) = p.file_name() {
                return canon_parent.join(file_name).to_string_lossy().to_string();
            }
        }
    }
    path.to_string()
}

fn is_protected_path(target: &str) -> Option<&'static str> {
    let resolved = resolve_path(target);
    let normalized = resolved.trim_end_matches('/');
    for &protected in PROTECTED_PATHS {
        let pnorm = protected.trim_end_matches('/');
        // Exact match or child path.
        if normalized == pnorm || normalized.starts_with(&format!("{}/", pnorm)) {
            return Some(protected);
        }
    }
    None
}

fn check_file_resource(
    resource_name: &str,
    resource_type: &str,
    params: &HashMap<String, String>,
) -> Option<Violation> {
    let target = params
        .get("targetpath")
        .or_else(|| params.get("path"))
        .map(|s| s.as_str())
        .unwrap_or("");

    if target.is_empty() {
        return None;
    }

    if let Some(protected) = is_protected_path(target) {
        return Some(Violation {
            resource_name: resource_name.to_string(),
            resource_type: resource_type.to_string(),
            reason: format!(
                "targets protected path {} (matched {})",
                target, protected
            ),
        });
    }

    None
}

fn check_service_resource(
    resource_name: &str,
    resource_type: &str,
    params: &HashMap<String, String>,
) -> Option<Violation> {
    let service = params
        .get("service")
        .or_else(|| params.get("name"))
        .map(|s| s.as_str())
        .unwrap_or("");

    if service.is_empty() {
        return None;
    }

    let state = params.get("state").map(|s| s.as_str()).unwrap_or("");

    for &protected in PROTECTED_SERVICES {
        if service == protected {
            match state {
                "stopped" | "disabled" | "masked" | "absent" => {
                    return Some(Violation {
                        resource_name: resource_name.to_string(),
                        resource_type: resource_type.to_string(),
                        reason: format!(
                            "cannot set protected service {:?} to state {:?}",
                            service, state
                        ),
                    });
                }
                _ => {}
            }
        }
    }

    None
}

fn check_package_resource(
    resource_name: &str,
    resource_type: &str,
    params: &HashMap<String, String>,
) -> Option<Violation> {
    let package = params
        .get("package")
        .or_else(|| params.get("name"))
        .map(|s| s.as_str())
        .unwrap_or("");

    if package.is_empty() {
        return None;
    }

    let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");

    if state == "absent" {
        for &protected in PROTECTED_PACKAGES {
            if package == protected {
                return Some(Violation {
                    resource_name: resource_name.to_string(),
                    resource_type: resource_type.to_string(),
                    reason: format!("cannot remove protected package {:?}", package),
                });
            }
        }
    }

    None
}

fn check_exec_resource(
    resource_name: &str,
    resource_type: &str,
    params: &HashMap<String, String>,
) -> Option<Violation> {
    let command = params
        .get("command")
        .or_else(|| params.get("cmd"))
        .map(|s| s.as_str())
        .unwrap_or("");

    if command.is_empty() {
        return None;
    }

    if let Some(reason) = check_push_command(command) {
        return Some(Violation {
            resource_name: resource_name.to_string(),
            resource_type: resource_type.to_string(),
            reason,
        });
    }

    None
}

#[cfg(test)]
mod tests {
    use super::*;

    fn params(pairs: &[(&str, &str)]) -> HashMap<String, String> {
        pairs
            .iter()
            .map(|(k, v)| (k.to_string(), v.to_string()))
            .collect()
    }

    // ── File resource tests ─────────────────────────────────────────

    #[test]
    fn file_targeting_agent_binary_blocked() {
        let p = params(&[("targetpath", "/usr/local/sbin/hobnob")]);
        let v = check_resource("bad-file", "file", &p);
        assert!(v.is_some());
        assert!(v.unwrap().reason.contains("protected path"));
    }

    #[test]
    fn file_targeting_server_binary_blocked() {
        let p = params(&[("targetpath", "/usr/local/sbin/hobd")]);
        let v = check_resource("bad-file", "file", &p);
        assert!(v.is_some());
    }

    #[test]
    fn file_targeting_server_config_dir_blocked() {
        let p = params(&[("targetpath", "/srv/hobnob/server.yaml")]);
        let v = check_resource("bad-file", "file", &p);
        assert!(v.is_some());
        assert!(v.unwrap().reason.contains("/srv/hobnob/"));
    }

    #[test]
    fn file_targeting_agent_config_dir_blocked() {
        let p = params(&[("targetpath", "/etc/hobnob-thrall/config.yaml")]);
        let v = check_resource("bad-file", "file", &p);
        assert!(v.is_some());
    }

    #[test]
    fn file_targeting_lmdb_state_blocked() {
        let p = params(&[("targetpath", "/var/lib/hobnob/state/data.mdb")]);
        let v = check_resource("bad-file", "file", &p);
        assert!(v.is_some());
    }

    #[test]
    fn file_targeting_safe_path_allowed() {
        let p = params(&[
            ("targetpath", "/etc/nginx/nginx.conf"),
            ("owner", "root"),
            ("group", "root"),
            ("mode", "0644"),
            ("content", "server {}"),
        ]);
        let v = check_resource("nginx-conf", "file", &p);
        assert!(v.is_none());
    }

    #[test]
    fn file_line_targeting_agent_config_blocked() {
        let p = params(&[("path", "/etc/hobnob-thrall/config.yaml")]);
        let v = check_resource("bad-line", "file_line", &p);
        assert!(v.is_some());
    }

    #[test]
    fn ini_targeting_server_config_blocked() {
        let p = params(&[("path", "/srv/hobnob/settings.ini")]);
        let v = check_resource("bad-ini", "ini", &p);
        assert!(v.is_some());
    }

    // ── Service resource tests ──────────────────────────────────────

    #[test]
    fn service_stop_agent_blocked() {
        let p = params(&[("service", "hobnob-thrall"), ("state", "stopped")]);
        let v = check_resource("stop-agent", "service", &p);
        assert!(v.is_some());
        assert!(v.unwrap().reason.contains("hobnob-thrall"));
    }

    #[test]
    fn service_disable_server_blocked() {
        let p = params(&[("service", "hobd"), ("state", "disabled")]);
        let v = check_resource("disable-server", "service", &p);
        assert!(v.is_some());
    }

    #[test]
    fn service_restart_agent_allowed() {
        let p = params(&[("service", "hobnob-thrall"), ("state", "restarted")]);
        let v = check_resource("restart-agent", "service", &p);
        assert!(v.is_none());
    }

    #[test]
    fn service_start_agent_allowed() {
        let p = params(&[("service", "hobnob-thrall"), ("state", "running")]);
        let v = check_resource("start-agent", "service", &p);
        assert!(v.is_none());
    }

    #[test]
    fn service_stop_unrelated_allowed() {
        let p = params(&[("service", "nginx"), ("state", "stopped")]);
        let v = check_resource("stop-nginx", "service", &p);
        assert!(v.is_none());
    }

    // ── Package resource tests ──────────────────────────────────────

    #[test]
    fn package_remove_hobnob_blocked() {
        let p = params(&[("package", "hobnob"), ("state", "absent")]);
        let v = check_resource("rm-pkg", "package", &p);
        assert!(v.is_some());
    }

    #[test]
    fn package_remove_hobd_blocked() {
        let p = params(&[("package", "hobd"), ("state", "absent")]);
        let v = check_resource("rm-pkg", "package", &p);
        assert!(v.is_some());
    }

    #[test]
    fn package_install_hobnob_allowed() {
        let p = params(&[("package", "hobnob"), ("state", "present")]);
        let v = check_resource("install-pkg", "package", &p);
        assert!(v.is_none());
    }

    #[test]
    fn package_remove_unrelated_allowed() {
        let p = params(&[("package", "vim"), ("state", "absent")]);
        let v = check_resource("rm-vim", "package", &p);
        assert!(v.is_none());
    }

    // ── Exec resource tests ─────────────────────────────────────────

    #[test]
    fn exec_systemctl_stop_agent_blocked() {
        let p = params(&[("command", "systemctl stop hobnob-thrall")]);
        let v = check_resource("bad-exec", "exec", &p);
        assert!(v.is_some());
    }

    #[test]
    fn exec_systemctl_stop_server_blocked() {
        let p = params(&[("command", "systemctl stop hobd")]);
        let v = check_resource("bad-exec", "exec", &p);
        assert!(v.is_some());
    }

    #[test]
    fn exec_rm_agent_binary_blocked() {
        let p = params(&[("command", "rm /usr/local/sbin/hobnob")]);
        let v = check_resource("bad-exec", "exec", &p);
        assert!(v.is_some());
    }

    #[test]
    fn exec_rm_rf_server_config_blocked() {
        let p = params(&[("command", "rm -rf /srv/hobnob")]);
        let v = check_resource("bad-exec", "exec", &p);
        assert!(v.is_some());
    }

    #[test]
    fn exec_pkill_blocked() {
        let p = params(&[("command", "pkill hobnob")]);
        let v = check_resource("bad-exec", "exec", &p);
        assert!(v.is_some());
    }

    #[test]
    fn exec_safe_command_allowed() {
        let p = params(&[("command", "systemctl restart nginx")]);
        let v = check_resource("safe-exec", "exec", &p);
        assert!(v.is_none());
    }

    // ── Push command tests ──────────────────────────────────────────

    #[test]
    fn push_stop_agent_blocked() {
        let r = check_push_command("systemctl stop hobnob-thrall && echo done");
        assert!(r.is_some());
    }

    #[test]
    fn push_stop_server_blocked() {
        let r = check_push_command("systemctl stop hobd");
        assert!(r.is_some());
    }

    #[test]
    fn push_rm_server_config_blocked() {
        let r = check_push_command("rm -rf /srv/hobnob/db");
        assert!(r.is_some());
    }

    #[test]
    fn push_safe_command_allowed() {
        let r = check_push_command("df -h && free -m");
        assert!(r.is_none());
    }

    #[test]
    fn push_case_insensitive() {
        let r = check_push_command("Systemctl Stop Hobnob-Thrall");
        assert!(r.is_some());
    }

    // ── Path matching edge cases ────────────────────────────────────

    #[test]
    fn path_exact_match() {
        assert!(is_protected_path("/usr/local/sbin/hobnob").is_some());
    }

    #[test]
    fn path_child_match() {
        assert!(is_protected_path("/srv/hobnob/certs/ca.pem").is_some());
    }

    #[test]
    fn path_prefix_not_child_allowed() {
        // /srv/hobnobXYZ should NOT match /srv/hobnob/
        assert!(is_protected_path("/srv/hobnobXYZ/foo").is_none());
    }

    #[test]
    fn path_similar_name_allowed() {
        // /usr/local/sbin/hobnob-helper should NOT match /usr/local/sbin/hobnob
        assert!(is_protected_path("/usr/local/sbin/hobnob-helper").is_none());
    }

    #[test]
    fn path_var_lib_child_blocked() {
        assert!(is_protected_path("/var/lib/hobnob/snapshots/foo.tar").is_some());
    }

    #[test]
    fn symlink_to_protected_path_blocked() {
        use std::os::unix::fs::symlink;
        let dir = std::env::temp_dir().join("hobnob_guardrail_test_symlink");
        let _ = std::fs::remove_file(&dir);
        symlink("/srv/hobnob", &dir).unwrap();
        let target = format!("{}/server.yaml", dir.display());
        let result = is_protected_path(&target);
        let _ = std::fs::remove_file(&dir);
        assert!(result.is_some(), "symlink to protected dir should be blocked");
    }

    #[test]
    fn nonexistent_file_under_symlinked_dir_blocked() {
        use std::os::unix::fs::symlink;
        let dir = std::env::temp_dir().join("hobnob_guardrail_test_symlink2");
        let _ = std::fs::remove_file(&dir);
        symlink("/srv/hobnob", &dir).unwrap();
        // File doesn't exist, but parent dir resolves through symlink.
        let target = format!("{}/nonexistent.yaml", dir.display());
        let result = is_protected_path(&target);
        let _ = std::fs::remove_file(&dir);
        assert!(result.is_some(), "nonexistent file under symlinked protected dir should be blocked");
    }

    #[test]
    fn normal_path_still_works() {
        // A non-symlink, non-protected path should still be allowed.
        assert!(is_protected_path("/tmp/safe_file.txt").is_none());
    }
}
