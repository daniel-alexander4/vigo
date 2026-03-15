// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0
#![cfg(windows)]

//! Windows ACL executor — manages file and directory access control lists
//! idempotently via `icacls.exe`.
//!
//! Parameters:
//! - `path`: File or directory path to manage ACLs on
//! - `entries`: Comma-separated ACL entries in `principal:access` format
//!   Access levels: F (Full), M (Modify), RX (Read+Execute), R (Read), W (Write)
//!   Example: "BUILTIN\\Administrators:F,NT AUTHORITY\\SYSTEM:F,AppUser:RX"
//! - `inherit`: "true" (default) or "false" — enable/disable inheritance
//! - `purge`: "true" or "false" (default) — remove entries not in the list
//! - `state`: "present" (default) or "absent"

use super::{Action, Executor, ResourceResult, run_command};
use std::collections::HashMap;
use std::time::Instant;

pub struct AclWindowsExecutor;

impl AclWindowsExecutor {
    pub fn new() -> Self {
        Self
    }

    /// Parse entries string into (principal, access) pairs.
    fn parse_entries(entries: &str) -> Vec<(String, String)> {
        entries
            .split(',')
            .filter_map(|e| {
                let e = e.trim();
                if e.is_empty() {
                    return None;
                }
                let parts: Vec<&str> = e.rsplitn(2, ':').collect();
                if parts.len() == 2 {
                    Some((parts[1].to_string(), parts[0].to_string()))
                } else {
                    None
                }
            })
            .collect()
    }

    /// Query current ACL entries via icacls. Returns list of (principal, permissions) as raw strings.
    fn current_acl(path: &str) -> Vec<String> {
        match run_command("icacls", &[path]) {
            Ok(o) if o.status.success() => {
                let stdout = String::from_utf8_lossy(&o.stdout);
                // icacls output format: "C:\path principal:(permissions)"
                // Skip the first line (path header) and last line (summary).
                stdout
                    .lines()
                    .filter(|l| l.contains(":("))
                    .map(|l| l.trim().to_string())
                    .collect()
            }
            _ => vec![],
        }
    }

    /// Check if a principal already has the specified access level.
    fn has_access(acl_lines: &[String], principal: &str, access: &str) -> bool {
        let access_upper = access.to_uppercase();
        for line in acl_lines {
            if line.to_uppercase().contains(&principal.to_uppercase()) {
                // Map short codes to icacls output patterns.
                let has = match access_upper.as_str() {
                    "F" => line.contains("(F)") || line.contains("(OI)(CI)(F)"),
                    "M" => line.contains("(M)") || line.contains("(OI)(CI)(M)"),
                    "RX" => line.contains("(RX)") || line.contains("(OI)(CI)(RX)"),
                    "R" => line.contains("(R)") || line.contains("(OI)(CI)(R)"),
                    "W" => line.contains("(W)") || line.contains("(OI)(CI)(W)"),
                    _ => line.to_uppercase().contains(&format!("({})", access_upper)),
                };
                if has {
                    return true;
                }
            }
        }
        false
    }
}

impl Executor for AclWindowsExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let path = params.get("path").cloned().unwrap_or_default();
        let entries_str = params.get("entries").cloned().unwrap_or_default();
        let inherit = params.get("inherit").map(|s| s != "false").unwrap_or(true);
        let purge = params.get("purge").map(|s| s == "true").unwrap_or(false);
        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");
        let resource_name = params.get("name").map(|s| s.as_str()).unwrap_or(&path);

        let mut result = ResourceResult::new("acl", resource_name);

        if path.is_empty() {
            result.error = Some("acl executor: path param is required".to_string());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let entries = Self::parse_entries(&entries_str);
        if entries.is_empty() && state == "present" {
            result.error = Some("acl executor: entries param is required for state: present".to_string());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let current = Self::current_acl(&path);

        match state {
            "present" => {
                // Check which entries need to be added.
                let mut needs_change = false;
                let mut grants: Vec<(&str, &str)> = Vec::new();

                for (principal, access) in &entries {
                    if !Self::has_access(&current, principal, access) {
                        needs_change = true;
                        grants.push((principal, access));
                    }
                }

                // Check inheritance state.
                // icacls /inheritance:e enables, /inheritance:d disables.
                // We check by looking for "(I)" markers in ACL lines.
                let has_inherited = current.iter().any(|l| l.contains("(I)"));
                if inherit && !has_inherited && !current.is_empty() {
                    // Can't reliably detect this without inherited entries, skip.
                } else if !inherit && has_inherited {
                    needs_change = true;
                }

                if purge {
                    // In purge mode, any entry not in our list is drift.
                    for line in &current {
                        let found = entries.iter().any(|(p, _)| {
                            line.to_uppercase().contains(&p.to_uppercase())
                        });
                        if !found && !line.contains("(I)") {
                            needs_change = true;
                            break;
                        }
                    }
                }

                if !needs_change {
                    result.action = Action::None;
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                result.action = Action::Updated;
                result.changed = true;

                if dry_run {
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                // Apply: if purge, reset then grant. Otherwise just grant missing.
                if purge {
                    // Reset: remove all explicit ACLs.
                    if let Err(e) = run_command("icacls", &[&path, "/reset", "/T", "/Q"])
                        .map_err(|e| e.to_string())
                        .and_then(|o| if o.status.success() { Ok(()) } else {
                            Err(String::from_utf8_lossy(&o.stderr).to_string())
                        })
                    {
                        result.error = Some(format!("icacls reset {}: {}", path, e));
                        result.duration_ms = start.elapsed().as_millis() as i32;
                        return result;
                    }
                }

                // Disable inheritance if requested.
                if !inherit {
                    let _ = run_command("icacls", &[&path, "/inheritance:d", "/Q"]);
                }

                // Grant each entry.
                for (principal, access) in &entries {
                    let grant_spec = format!("{}:(OI)(CI){}", principal, access);
                    match run_command("icacls", &[&path, "/grant", &grant_spec, "/Q"]) {
                        Ok(o) if !o.status.success() => {
                            result.error = Some(format!(
                                "icacls grant {} on {}: {}",
                                grant_spec, path, String::from_utf8_lossy(&o.stderr)
                            ));
                            result.duration_ms = start.elapsed().as_millis() as i32;
                            return result;
                        }
                        Err(e) => {
                            result.error = Some(format!("icacls grant {}: {}", grant_spec, e));
                            result.duration_ms = start.elapsed().as_millis() as i32;
                            return result;
                        }
                        _ => {}
                    }
                }
            }
            "absent" => {
                // Remove specified entries.
                let mut needs_change = false;
                for (principal, access) in &entries {
                    if Self::has_access(&current, principal, access) {
                        needs_change = true;
                    }
                }

                if !needs_change {
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

                for (principal, _) in &entries {
                    match run_command("icacls", &[&path, "/remove", principal, "/Q"]) {
                        Ok(o) if !o.status.success() => {
                            result.error = Some(format!(
                                "icacls remove {} from {}: {}",
                                principal, path, String::from_utf8_lossy(&o.stderr)
                            ));
                            result.duration_ms = start.elapsed().as_millis() as i32;
                            return result;
                        }
                        Err(e) => {
                            result.error = Some(format!("icacls remove {}: {}", principal, e));
                            result.duration_ms = start.elapsed().as_millis() as i32;
                            return result;
                        }
                        _ => {}
                    }
                }
            }
            other => {
                result.error = Some(format!(
                    "acl executor: unknown state {:?} (must be present or absent)", other
                ));
            }
        }

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}
