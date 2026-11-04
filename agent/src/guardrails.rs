// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Self-protection guardrails — hardcoded, non-configurable by the server.
//!
//! Prevents configs or ad-hoc tasks from destroying the vigo agent, server,
//! or their dependencies on managed nodes. The agent enforces these regardless
//! of what the server sends.

use std::collections::HashMap;

/// Paths exempt from guardrail protection. These are children of protected
/// paths that modules are allowed to write to (e.g., scan output directories).
#[cfg(unix)]
const EXEMPT_PATHS: &[&str] = &[
    "/var/lib/vigo/security/",
];

#[cfg(windows)]
const EXEMPT_PATHS: &[&str] = &[
    r"C:\ProgramData\Vigo\security\",
];

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
    // systemd drop-in dirs — a drop-in overriding ExecStart neutralizes the
    // unit without touching the .service file above (the .d dir is a sibling,
    // not a child, of the file path). systemd_dropin is guarded by unit name
    // too; these entries backstop a raw file/directory resource writing here.
    "/etc/systemd/system/vigo-envoy.service.d/",
    "/etc/systemd/system/vigosrv.service.d/",
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
    "/usr/local/bin/vigocli",
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

/// Process names that must never be killed/stopped by a `process` resource.
/// (The agent binary runs as `vigo`; the server as `vigosrv`.)
const PROTECTED_PROCESSES: &[&str] = &["vigo", "vigosrv", "vigo-envoy"];

/// Packages that must never be removed.
const PROTECTED_PACKAGES: &[&str] = &["vigo", "vigosrv", "vigocli"];

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
    "rm /usr/local/bin/vigocli",
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
    // `on_revert` runs via the reversal lever for several resource types
    // (exec/backup/db_backup/replace/ssh_exec/powershell). Scan it for EVERY
    // type so no reversal command bypasses the (best-effort) command guard.
    if let Some(reason) = params
        .get("on_revert")
        .filter(|s| !s.is_empty())
        .and_then(|c| check_push_command(c))
    {
        return Some(Violation {
            resource_name: resource_name.to_string(),
            resource_type: resource_type.to_string(),
            reason: format!("on_revert: {}", reason),
        });
    }

    match resource_type {
        "file" | "directory" | "symlink" | "file_line" | "blockinfile" | "ini" | "json_file" | "replace" | "field_edit" => {
            check_file_resource(resource_name, resource_type, params)
        }
        "service" => check_service_resource(resource_name, resource_type, params),
        "package" => check_package_resource(resource_name, resource_type, params),
        "exec" => check_exec_resource(resource_name, resource_type, params),
        "systemd_dropin" => check_systemd_dropin_resource(resource_name, resource_type, params),
        "process" => check_process_resource(resource_name, resource_type, params),
        // Types whose guardrail surface is a set of FS-path params and/or
        // free-form command params (see path_command_spec).
        other => path_command_spec(other).and_then(|(paths, commands)| {
            check_path_and_command_params(resource_name, other, params, paths, commands)
        }),
    }
}

/// True iff `check_resource` actively inspects this resource type. Every
/// dispatchable type must be either checked here or listed in
/// [`GUARDRAIL_EXEMPT`]; the `every_registry_type_is_classified` test enforces
/// it, turning "a new executor silently bypasses guardrails" into a build
/// failure. Kept in lockstep with the `check_resource` match arms.
pub fn is_checked_type(resource_type: &str) -> bool {
    matches!(
        resource_type,
        "file" | "directory" | "symlink" | "file_line" | "blockinfile" | "ini"
            | "json_file" | "replace" | "field_edit" | "service" | "package"
            | "exec" | "systemd_dropin" | "process"
    ) || path_command_spec(resource_type).is_some()
}

/// Resource types deliberately NOT guardrail-checked, each with the reason it
/// carries no protected-asset surface. A new dispatchable executor must be
/// added here (with a reason) or given a `check_resource` arm — the coverage
/// test fails otherwise.
pub const GUARDRAIL_EXEMPT: &[(&str, &str)] = &[
    ("host", "rewrites the fixed /etc/hosts; not a protected path"),
    ("repository", "writes repo config under /etc/apt|/etc/yum.repos.d; derived, non-protected paths"),
    ("reboot", "reboots the host; no target param to inspect"),
    ("pip", "language-package namespace, disjoint from vigo OS packages"),
    ("gem", "language-package namespace, disjoint from vigo OS packages"),
    ("npm", "language-package namespace, disjoint from vigo OS packages"),
    ("vm", "manages VM disk images/domains; not a protected-asset surface"),
    ("user", "no vigo runtime account is in the protected set (revisit if a dedicated vigo user is added)"),
    ("group", "no vigo runtime group is in the protected set"),
    ("hostname", "host-level setting; no protected-asset param"),
    ("timezone", "host-level setting; no protected-asset param"),
    ("sysctl", "host-level kernel param; no protected-asset param"),
    ("firewall", "host-level firewall rule; no protected-asset param"),
    ("kernel_module", "loads/unloads a kernel module; no protected-asset param"),
    ("ssh_exec", "runs on a remote host over SSH, not the local node"),
    ("network_config", "configures a remote network device, not the local node"),
    ("cisco_vlan", "configures a remote network device, not the local node"),
    ("cisco_interface", "configures a remote network device, not the local node"),
    ("cisco_acl", "configures a remote network device, not the local node"),
    ("junos_interface", "configures a remote network device, not the local node"),
    ("network_ntp", "configures a remote network device, not the local node"),
    ("network_syslog", "configures a remote network device, not the local node"),
    ("network_snmp", "configures a remote network device, not the local node"),
    ("network_user", "configures a remote network device, not the local node"),
    ("network_banner", "configures a remote network device, not the local node"),
    ("network_interface", "configures a remote network device, not the local node"),
    ("network_vlan", "configures a remote network device, not the local node"),
    ("network_acl", "configures a remote network device, not the local node"),
    ("network_static_route", "configures a remote network device, not the local node"),
    ("network_dns", "configures a remote network device, not the local node"),
];

/// Per-type guardrail spec for types whose only protected-asset surface is a
/// set of filesystem-path params (checked against protected paths) and/or
/// free-form command params (best-effort command scan). Returns
/// `(path_param_names, command_param_names)`.
fn path_command_spec(
    resource_type: &str,
) -> Option<(&'static [&'static str], &'static [&'static str])> {
    let spec: (&[&str], &[&str]) = match resource_type {
        "git" => (&["path"], &[]),
        "sshkey" => (&["file"], &[]),
        "swap" => (&["path"], &[]),
        "alternatives" => (&["link", "path"], &[]),
        "mount" => (&["path"], &[]),
        "selinux" => (&["path"], &[]),
        "logrotate" => (&["path"], &["postrotate"]),
        "source_package" => (&["target_path", "binary_path"], &["unless"]),
        "tmpfile" => (&["path"], &[]),
        "backup" => (&["target_path"], &["pre_command", "post_command"]),
        "db_backup" => (&["target_path"], &["db_dump_command"]),
        "cron" => (&[], &["command"]),
        "nonrepo_package" => (&[], &["pre_install", "post_install"]),
        "container" => (&[], &["command"]),
        "powershell" => (&[], &["command"]),
        _ => return None,
    };
    Some(spec)
}

/// Check a resource's declared FS-path params against protected paths and its
/// free-form command params against the (best-effort) command scanner.
fn check_path_and_command_params(
    resource_name: &str,
    resource_type: &str,
    params: &HashMap<String, String>,
    path_params: &[&str],
    command_params: &[&str],
) -> Option<Violation> {
    for key in path_params {
        let target = params.get(*key).map(|s| s.as_str()).unwrap_or("");
        if target.is_empty() {
            continue;
        }
        if let Some(protected) = is_protected_path(target) {
            return Some(Violation {
                resource_name: resource_name.to_string(),
                resource_type: resource_type.to_string(),
                reason: format!("{} targets protected path {} (matched {})", key, target, protected),
            });
        }
    }
    for key in command_params {
        let command = params.get(*key).map(|s| s.as_str()).unwrap_or("");
        if command.is_empty() {
            continue;
        }
        if let Some(reason) = check_push_command(command) {
            return Some(Violation {
                resource_name: resource_name.to_string(),
                resource_type: resource_type.to_string(),
                reason: format!("{}: {}", key, reason),
            });
        }
    }
    None
}

/// A systemd drop-in for a protected unit can override its ExecStart and
/// neutralize the agent without touching the .service file. Match on the unit
/// name (the `.d` dir is a sibling, not a child, of the protected file path).
fn check_systemd_dropin_resource(
    resource_name: &str,
    resource_type: &str,
    params: &HashMap<String, String>,
) -> Option<Violation> {
    let unit = params.get("unit").map(|s| s.as_str()).unwrap_or("");
    if unit.is_empty() {
        return None;
    }
    let bare = unit.strip_suffix(".service").unwrap_or(unit);
    for &protected in PROTECTED_SERVICES {
        if bare == protected {
            return Some(Violation {
                resource_name: resource_name.to_string(),
                resource_type: resource_type.to_string(),
                reason: format!(
                    "cannot install a drop-in for protected unit {:?} (would override the vigo service definition)",
                    unit
                ),
            });
        }
    }
    None
}

/// A `process` resource can kill the agent/server by name. Block a stop/kill
/// state targeting a protected process.
fn check_process_resource(
    resource_name: &str,
    resource_type: &str,
    params: &HashMap<String, String>,
) -> Option<Violation> {
    let process = params.get("process").map(|s| s.as_str()).unwrap_or("");
    if process.is_empty() {
        return None;
    }
    let state = params.get("state").map(|s| s.as_str()).unwrap_or("running");
    if matches!(state, "stopped" | "absent" | "killed" | "dead") {
        for &protected in PROTECTED_PROCESSES {
            if process == protected {
                return Some(Violation {
                    resource_name: resource_name.to_string(),
                    resource_type: resource_type.to_string(),
                    reason: format!("cannot set protected process {:?} to state {:?}", process, state),
                });
            }
        }
    }
    None
}

/// Best-effort scan of a free-form shell command for self-destructive intent.
///
/// This is a HEURISTIC, not a boundary: a hostile command author can trivially
/// evade substring matching (variable indirection, command substitution,
/// encoding, writing then sourcing a script). It exists to catch accidents and
/// unsophisticated abuse. The robust self-protection lives in the STRUCTURAL
/// checks (`check_resource` for file/service/package/process/systemd_dropin
/// against canonically-resolved targets); do not treat this scan as a security
/// boundary. Returns `Some(reason)` if the command should be rejected.
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

    // rm/unlink referencing a protected path is refused with no exempt escape
    // (fail-closed). The EXEMPT_PATHS carve-out is honored only by the
    // structural is_protected_path check, where the actual target is known; in
    // a free-form command the true target can't be isolated from the string, so
    // a prior "exempt substring appears anywhere → skip" carve-out was itself a
    // bypass (`rm -rf /var/lib/vigo && ls /var/lib/vigo/security`). Use a
    // structural file/directory resource for legitimate writes under the exempt
    // scan dir.
    if lower.contains("rm ") || lower.contains("unlink ") {
        for path in PROTECTED_PATHS {
            let trimmed = path.trim_end_matches('/');
            if lower.contains(trimmed) {
                return Some(format!("command targets protected path: {}", path));
            }
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

    // Check exemptions first — exempt paths override protected paths.
    for &exempt in EXEMPT_PATHS {
        let enorm = exempt.trim_end_matches('/');
        if normalized == enorm || normalized.starts_with(&format!("{}/", enorm)) {
            return None;
        }
    }

    for &protected in PROTECTED_PATHS {
        let pnorm = protected.trim_end_matches('/');
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
        .get("target_path")
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
        .map(|s| s.as_str())
        .unwrap_or("");

    if service.is_empty() {
        return None;
    }

    let state = params.get("state").map(|s| s.as_str()).unwrap_or("");
    let disables = params.get("enabled").is_some_and(|v| v.trim() == "false");

    // The service executors accept a comma-separated list (`service: "nginx,
    // vigo-envoy"`) and act on each name, so each name is checked — comparing
    // the whole string let a list smuggle a protected service past. A
    // `.service` unit suffix names the same unit.
    for name in service.split(',').map(str::trim).filter(|s| !s.is_empty()) {
        let unit = name.strip_suffix(".service").unwrap_or(name);
        if !PROTECTED_SERVICES.contains(&unit) {
            continue;
        }
        if matches!(state, "stopped" | "disabled" | "masked" | "absent") {
            return Some(Violation {
                resource_name: resource_name.to_string(),
                resource_type: resource_type.to_string(),
                reason: format!("cannot set protected service {:?} to state {:?}", unit, state),
            });
        }
        if disables {
            return Some(Violation {
                resource_name: resource_name.to_string(),
                resource_type: resource_type.to_string(),
                reason: format!("cannot disable protected service {:?} (enabled: false)", unit),
            });
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
    // The reversal command (`on_revert`) is scanned universally in
    // check_resource, so only the forward command is checked here.
    let command = params.get("command").map(|s| s.as_str()).unwrap_or("");
    if command.is_empty() {
        return None;
    }
    check_push_command(command).map(|reason| Violation {
        resource_name: resource_name.to_string(),
        resource_type: resource_type.to_string(),
        reason,
    })
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
        let p = params(&[("target_path", "/usr/local/sbin/vigo")]);
        let v = check_resource("bad-file", "file", &p);
        assert!(v.is_some());
        assert!(v.unwrap().reason.contains("protected path"));
    }

    #[test]
    fn file_targeting_server_binary_blocked() {
        let p = params(&[("target_path", "/usr/local/sbin/vigosrv")]);
        let v = check_resource("bad-file", "file", &p);
        assert!(v.is_some());
    }

    #[test]
    fn file_targeting_server_config_dir_blocked() {
        let p = params(&[("target_path", "/srv/vigo/server.yaml")]);
        let v = check_resource("bad-file", "file", &p);
        assert!(v.is_some());
        assert!(v.unwrap().reason.contains("/srv/vigo/"));
    }

    #[test]
    fn file_targeting_agent_config_dir_blocked() {
        let p = params(&[("target_path", "/etc/vigo-envoy/config.yaml")]);
        let v = check_resource("bad-file", "file", &p);
        assert!(v.is_some());
    }

    #[test]
    fn file_targeting_lmdb_state_blocked() {
        let p = params(&[("target_path", "/var/lib/vigo/state/data.mdb")]);
        let v = check_resource("bad-file", "file", &p);
        assert!(v.is_some());
    }

    #[test]
    fn file_targeting_safe_path_allowed() {
        let p = params(&[
            ("target_path", "/etc/nginx/nginx.conf"),
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

    #[test]
    fn exec_dangerous_on_revert_blocked() {
        // A safe forward command must not let a dangerous on_revert through —
        // the reversal lever runs on_revert, so it's guardrail-scanned too.
        let p = params(&[
            ("command", "systemctl restart nginx"),
            ("on_revert", "systemctl stop vigo-envoy"),
        ]);
        let v = check_resource("sneaky-exec", "exec", &p);
        assert!(v.is_some());
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

    // ── Coverage guard ──────────────────────────────────────────────
    // The centerpiece: every dispatchable resource type must be either
    // guardrail-checked or explicitly exempt-with-reason. A new executor
    // added to the registry fails this until it's classified, so the
    // `_ => None` fallthrough can never silently reopen a coverage gap.
    #[test]
    fn every_registry_type_is_classified() {
        let registry = crate::runner::build_executors_with_snapshots("linux", "ubuntu", None);
        // Guard against a vacuous pass if the registry ever comes back empty.
        assert!(registry.len() > 20, "registry unexpectedly small: {}", registry.len());
        assert!(registry.contains_key("process") && registry.contains_key("systemd_dropin"));
        let exempt: std::collections::HashSet<&str> =
            GUARDRAIL_EXEMPT.iter().map(|(t, _)| *t).collect();
        let mut unclassified: Vec<String> = registry
            .keys()
            .filter(|ty| !is_checked_type(ty) && !exempt.contains(ty.as_str()))
            .cloned()
            .collect();
        unclassified.sort();
        assert!(
            unclassified.is_empty(),
            "dispatchable resource types neither guardrail-checked nor in GUARDRAIL_EXEMPT — \
             classify each (add a check_resource arm or an exempt entry with a reason): {:?}",
            unclassified
        );
    }

    #[test]
    fn checked_and_exempt_sets_are_disjoint() {
        for (ty, _) in GUARDRAIL_EXEMPT {
            assert!(
                !is_checked_type(ty),
                "type {:?} is both checked and exempt — remove it from GUARDRAIL_EXEMPT",
                ty
            );
        }
    }

    // ── New structural coverage (the real boundary) ─────────────────

    #[test]
    fn process_kill_agent_blocked() {
        let p = params(&[("process", "vigo"), ("state", "stopped")]);
        assert!(check_resource("kill-agent", "process", &p).is_some());
    }

    #[test]
    fn process_kill_server_blocked() {
        let p = params(&[("process", "vigosrv"), ("state", "stopped")]);
        assert!(check_resource("kill-srv", "process", &p).is_some());
    }

    #[test]
    fn process_running_agent_allowed() {
        let p = params(&[("process", "vigo"), ("state", "running")]);
        assert!(check_resource("keep-agent", "process", &p).is_none());
    }

    #[test]
    fn process_kill_unrelated_allowed() {
        let p = params(&[("process", "nginx"), ("state", "stopped")]);
        assert!(check_resource("kill-nginx", "process", &p).is_none());
    }

    #[test]
    fn systemd_dropin_override_agent_unit_blocked() {
        // The ExecStart-hijack vector: a drop-in for the vigo unit.
        let p = params(&[("unit", "vigo-envoy.service"), ("content", "[Service]\nExecStart=")]);
        assert!(check_resource("hijack", "systemd_dropin", &p).is_some());
    }

    #[test]
    fn systemd_dropin_override_agent_unit_no_suffix_blocked() {
        let p = params(&[("unit", "vigo-envoy"), ("content", "x")]);
        assert!(check_resource("hijack2", "systemd_dropin", &p).is_some());
    }

    #[test]
    fn systemd_dropin_unrelated_unit_allowed() {
        let p = params(&[("unit", "nginx.service"), ("content", "x")]);
        assert!(check_resource("ok-dropin", "systemd_dropin", &p).is_none());
    }

    #[test]
    fn git_absent_on_protected_path_blocked() {
        // git state=absent runs remove_dir_all(path).
        let p = params(&[("path", "/var/lib/vigo/state"), ("state", "absent")]);
        assert!(check_resource("wipe-state", "git", &p).is_some());
    }

    #[test]
    fn source_package_overwriting_agent_binary_blocked() {
        let p = params(&[("target_path", "/usr/local/sbin/vigo")]);
        assert!(check_resource("overwrite-agent", "source_package", &p).is_some());
    }

    #[test]
    fn sshkey_targeting_protected_path_blocked() {
        let p = params(&[("file", "/etc/vigo/known_hosts")]);
        assert!(check_resource("bad-sshkey", "sshkey", &p).is_some());
    }

    #[test]
    fn swap_on_protected_path_blocked() {
        let p = params(&[("path", "/var/lib/vigo/swapfile")]);
        assert!(check_resource("bad-swap", "swap", &p).is_some());
    }

    #[test]
    fn path_type_on_safe_path_allowed() {
        let p = params(&[("path", "/opt/app/repo")]);
        assert!(check_resource("ok-git", "git", &p).is_none());
    }

    // ── Free-form command params + on_revert (best-effort) ──────────

    #[test]
    fn backup_pre_command_stopping_agent_blocked() {
        let p = params(&[
            ("target_path", "/opt/backups"),
            ("pre_command", "systemctl stop vigo-envoy"),
        ]);
        assert!(check_resource("sneaky-backup", "backup", &p).is_some());
    }

    #[test]
    fn cron_scheduling_agent_stop_blocked() {
        let p = params(&[
            ("name", "nightly"),
            ("command", "systemctl stop vigo-envoy"),
            ("schedule", "0 3 * * *"),
        ]);
        assert!(check_resource("sneaky-cron", "cron", &p).is_some());
    }

    #[test]
    fn on_revert_stopping_agent_blocked_for_any_type() {
        // The reversal lever runs on_revert for backup/db_backup/replace/etc.;
        // it must be scanned regardless of the resource type.
        let p = params(&[
            ("target_path", "/opt/backups"),
            ("on_revert", "systemctl stop vigo-envoy"),
        ]);
        assert!(check_resource("sneaky-revert", "backup", &p).is_some());
    }

    // ── EXEMPT_PATHS bypass is closed (fail-closed command scan) ─────

    #[test]
    fn exempt_path_mention_no_longer_unblocks_protected_rm() {
        // Regression: mentioning the exempt scan dir anywhere used to skip the
        // protected-path block, letting `rm -rf /var/lib/vigo` through.
        let r = check_push_command("rm -rf /var/lib/vigo && ls /var/lib/vigo/security");
        assert!(r.is_some(), "protected-path rm must not be unblocked by an exempt mention");
    }

    // ── Documented best-effort limit of the command scanner ─────────

    #[test]
    fn command_scanner_is_best_effort_not_a_boundary() {
        // This pins the KNOWN limitation so nobody mistakes the scan for a
        // boundary: trivial obfuscation evades it. The structural checks above
        // are the real enforcement; the free-form command surface is not.
        assert!(check_push_command("S=systemctl; $S stop vigo-envoy").is_none());
        assert!(check_push_command("kill -9 $(pidof vigo-envoy)").is_none());
    }

    #[test]
    fn protected_service_checked_per_listed_name_and_on_disable() {
        let svc = |name: &str, state: &str, enabled: Option<&str>| {
            let mut p = params(&[("service", name), ("state", state)]);
            if let Some(e) = enabled {
                p.insert("enabled".into(), e.into());
            }
            check_resource("r", "service", &p)
        };
        assert!(svc("nginx,vigo-envoy", "stopped", None).is_some(), "comma list smuggled a protected service");
        assert!(svc("nginx, vigosrv", "stopped", None).is_some());
        assert!(svc("vigo-envoy.service", "stopped", None).is_some());
        assert!(svc("vigo-envoy", "running", Some("false")).is_some(), "enabled: false must be refused");
        assert!(svc("vigo-envoy", "running", Some("true")).is_none());
        assert!(svc("nginx,postfix", "stopped", None).is_none());
        assert!(svc("nginx", "running", Some("false")).is_none());
    }
}
