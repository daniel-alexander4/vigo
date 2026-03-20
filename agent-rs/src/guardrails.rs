// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Self-protection guardrails — hardcoded, non-configurable by the server.
//!
//! Prevents configs or ad-hoc tasks from destroying the vigo agent, server,
//! or their dependencies on managed nodes. The agent enforces these regardless
//! of what the server sends.

use std::collections::HashMap;

/// Paths that must never be deleted, overwritten, or have permissions changed
/// by any resource. A resource targets a protected path if its target is equal
/// to or is a child of any entry here.
#[cfg(unix)]
const PROTECTED_PATHS: &[&str] = &[
    // Agent (Linux)
    "/usr/local/sbin/vigo",
    "/etc/vigo-envoy/",
    "/etc/vigo/",
    "/var/lib/vigo/",
    "/etc/systemd/system/vigo-envoy.service",
    // Agent (macOS)
    "/usr/local/etc/vigo-envoy/",
    "/Library/Vigo/",
    "/Library/LaunchDaemons/com.vigo.envoy.plist",
    // Agent (FreeBSD)
    "/usr/local/etc/rc.d/vigo_envoy",
    // Agent (illumos)
    "/var/svc/manifest/system/vigo-envoy.xml",
    // Server
    "/usr/local/sbin/vigosrv",
    "/usr/local/bin/vigogo",
    "/usr/local/sbin/vigo-license-manager",
    "/srv/vigo/",
    "/etc/systemd/system/vigosrv.service",
];

#[cfg(windows)]
const PROTECTED_PATHS: &[&str] = &[
    // Agent (Windows standard paths)
    r"C:\Program Files\Vigo\",
    r"C:\ProgramData\Vigo\",
];

/// Services that must never be stopped, disabled, or masked.
const PROTECTED_SERVICES: &[&str] = &["vigo-envoy", "vigosrv"];

/// Packages that must never be removed.
const PROTECTED_PACKAGES: &[&str] = &["vigo", "vigosrv", "vigogo"];

/// Dangerous command fragments that target vigo processes or files.
/// Used for heuristic scanning of exec commands and ad-hoc tasks.
#[cfg(unix)]
const DANGEROUS_PATTERNS: &[&str] = &[
    // systemd (Linux)
    "systemctl stop vigo",
    "systemctl disable vigo",
    "systemctl mask vigo",
    "systemctl stop vigosrv",
    "systemctl disable vigosrv",
    "systemctl mask vigosrv",
    // rc.d / service (BSD)
    "service vigo-envoy stop",
    "service vigo_envoy stop",
    "service vigosrv stop",
    // rcctl (OpenBSD)
    "rcctl stop vigo",
    "rcctl disable vigo",
    // launchctl (macOS)
    "launchctl unload com.vigo",
    "launchctl remove com.vigo",
    "launchctl bootout com.vigo",
    // SMF (illumos)
    "svcadm disable vigo",
    "svccfg delete vigo",
    // process kill
    "pkill vigo",
    "pkill vigosrv",
    "killall vigo",
    "killall vigosrv",
    "kill -9 vigo",
    "kill -9 vigosrv",
    // file removal
    "rm /usr/local/sbin/vigo",
    "rm /usr/local/sbin/vigosrv",
    "rm /usr/local/bin/vigogo",
    "rm /usr/local/sbin/vigo-license-manager",
    "rm -rf /etc/vigo",
    "rm -rf /var/lib/vigo",
    "rm -rf /srv/vigo",
    "rm -rf /etc/vigo-envoy",
    "rm -rf /library/vigo",
];

#[cfg(windows)]
const DANGEROUS_PATTERNS: &[&str] = &[
    "sc stop vigo",
    "sc delete vigo",
    "taskkill /im vigo",
    "del /f vigo.exe",
    "rmdir /s vigo",
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
        let p = params(&[("targetpath", "/usr/local/sbin/vigo")]);
        let v = check_resource("bad-file", "file", &p);
        assert!(v.is_some());
        assert!(v.unwrap().reason.contains("protected path"));
    }

    #[test]
    fn file_targeting_server_binary_blocked() {
        let p = params(&[("targetpath", "/usr/local/sbin/vigosrv")]);
        let v = check_resource("bad-file", "file", &p);
        assert!(v.is_some());
    }

    #[test]
    fn file_targeting_server_config_dir_blocked() {
        let p = params(&[("targetpath", "/srv/vigo/server.yaml")]);
        let v = check_resource("bad-file", "file", &p);
        assert!(v.is_some());
        assert!(v.unwrap().reason.contains("/srv/vigo/"));
    }

    #[test]
    fn file_targeting_agent_config_dir_blocked() {
        let p = params(&[("targetpath", "/etc/vigo-envoy/config.yaml")]);
        let v = check_resource("bad-file", "file", &p);
        assert!(v.is_some());
    }

    #[test]
    fn file_targeting_lmdb_state_blocked() {
        let p = params(&[("targetpath", "/var/lib/vigo/state/data.mdb")]);
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
        let p = params(&[("path", "/etc/vigo-envoy/config.yaml")]);
        let v = check_resource("bad-line", "file_line", &p);
        assert!(v.is_some());
    }

    #[test]
    fn ini_targeting_server_config_blocked() {
        let p = params(&[("path", "/srv/vigo/settings.ini")]);
        let v = check_resource("bad-ini", "ini", &p);
        assert!(v.is_some());
    }

    // ── Service resource tests ──────────────────────────────────────

    #[test]
    fn service_stop_agent_blocked() {
        let p = params(&[("service", "vigo-envoy"), ("state", "stopped")]);
        let v = check_resource("stop-agent", "service", &p);
        assert!(v.is_some());
        assert!(v.unwrap().reason.contains("vigo-envoy"));
    }

    #[test]
    fn service_disable_server_blocked() {
        let p = params(&[("service", "vigosrv"), ("state", "disabled")]);
        let v = check_resource("disable-server", "service", &p);
        assert!(v.is_some());
    }

    #[test]
    fn service_restart_agent_allowed() {
        let p = params(&[("service", "vigo-envoy"), ("state", "restarted")]);
        let v = check_resource("restart-agent", "service", &p);
        assert!(v.is_none());
    }

    #[test]
    fn service_start_agent_allowed() {
        let p = params(&[("service", "vigo-envoy"), ("state", "running")]);
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
    fn package_remove_vigo_blocked() {
        let p = params(&[("package", "vigo"), ("state", "absent")]);
        let v = check_resource("rm-pkg", "package", &p);
        assert!(v.is_some());
    }

    #[test]
    fn package_remove_vigosrv_blocked() {
        let p = params(&[("package", "vigosrv"), ("state", "absent")]);
        let v = check_resource("rm-pkg", "package", &p);
        assert!(v.is_some());
    }

    #[test]
    fn package_install_vigo_allowed() {
        let p = params(&[("package", "vigo"), ("state", "present")]);
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
        let p = params(&[("command", "systemctl stop vigo-envoy")]);
        let v = check_resource("bad-exec", "exec", &p);
        assert!(v.is_some());
    }

    #[test]
    fn exec_systemctl_stop_server_blocked() {
        let p = params(&[("command", "systemctl stop vigosrv")]);
        let v = check_resource("bad-exec", "exec", &p);
        assert!(v.is_some());
    }

    #[test]
    fn exec_rm_agent_binary_blocked() {
        let p = params(&[("command", "rm /usr/local/sbin/vigo")]);
        let v = check_resource("bad-exec", "exec", &p);
        assert!(v.is_some());
    }

    #[test]
    fn exec_rm_rf_server_config_blocked() {
        let p = params(&[("command", "rm -rf /srv/vigo")]);
        let v = check_resource("bad-exec", "exec", &p);
        assert!(v.is_some());
    }

    #[test]
    fn exec_pkill_blocked() {
        let p = params(&[("command", "pkill vigo")]);
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
        let r = check_push_command("systemctl stop vigo-envoy && echo done");
        assert!(r.is_some());
    }

    #[test]
    fn push_stop_server_blocked() {
        let r = check_push_command("systemctl stop vigosrv");
        assert!(r.is_some());
    }

    #[test]
    fn push_rm_server_config_blocked() {
        let r = check_push_command("rm -rf /srv/vigo/db");
        assert!(r.is_some());
    }

    #[test]
    fn push_safe_command_allowed() {
        let r = check_push_command("df -h && free -m");
        assert!(r.is_none());
    }

    #[test]
    fn push_case_insensitive() {
        let r = check_push_command("Systemctl Stop Vigo-Envoy");
        assert!(r.is_some());
    }

    // ── Path matching edge cases ────────────────────────────────────

    #[test]
    fn path_exact_match() {
        assert!(is_protected_path("/usr/local/sbin/vigo").is_some());
    }

    #[test]
    fn path_child_match() {
        assert!(is_protected_path("/srv/vigo/certs/ca.pem").is_some());
    }

    #[test]
    fn path_prefix_not_child_allowed() {
        // /srv/vigoXYZ should NOT match /srv/vigo/
        assert!(is_protected_path("/srv/vigoXYZ/foo").is_none());
    }

    #[test]
    fn path_similar_name_allowed() {
        // /usr/local/sbin/vigo-helper should NOT match /usr/local/sbin/vigo
        assert!(is_protected_path("/usr/local/sbin/vigo-helper").is_none());
    }

    #[test]
    fn path_var_lib_child_blocked() {
        assert!(is_protected_path("/var/lib/vigo/snapshots/foo.tar").is_some());
    }

    #[test]
    fn symlink_to_protected_path_blocked() {
        use std::os::unix::fs::symlink;
        let dir = std::env::temp_dir().join("vigo_guardrail_test_symlink");
        let _ = std::fs::remove_file(&dir);
        symlink("/srv/vigo", &dir).unwrap();
        let target = format!("{}/server.yaml", dir.display());
        let result = is_protected_path(&target);
        let _ = std::fs::remove_file(&dir);
        assert!(result.is_some(), "symlink to protected dir should be blocked");
    }

    #[test]
    fn nonexistent_file_under_symlinked_dir_blocked() {
        use std::os::unix::fs::symlink;
        let dir = std::env::temp_dir().join("vigo_guardrail_test_symlink2");
        let _ = std::fs::remove_file(&dir);
        symlink("/srv/vigo", &dir).unwrap();
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
