// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0
#![cfg(windows)]

//! SMB share executor — manages SMB file shares idempotently via PowerShell.
//!
//! Parameters:
//! - `share` (required): Share name (e.g., "Data", "Public")
//! - `path`: Local directory path (required for state=present)
//! - `state`: "present" (default) or "absent"
//! - `description`: Share description (optional)
//! - `full_access`: Comma-separated users/groups with full access
//! - `read_access`: Comma-separated users/groups with read-only access
//! - `change_access`: Comma-separated users/groups with change access
//! - `no_access`: Comma-separated users/groups denied access
//!
//! Check: `Get-SmbShare -Name '<share>' -ErrorAction SilentlyContinue`
//! Create: `New-SmbShare -Name '<share>' -Path '<path>' [access params]`
//! Update: If path differs, remove and recreate. If permissions differ, use
//!         `Grant-SmbShareAccess` / `Revoke-SmbShareAccess`.
//! Remove: `Remove-SmbShare -Name '<share>' -Force`

use super::{Action, Executor, ResourceResult, run_command};
use std::collections::HashMap;
use std::time::Instant;

pub struct SmbShareExecutor;

impl SmbShareExecutor {
    pub fn new() -> Self {
        Self
    }

    /// Check if a share exists and return its current path (if any).
    fn get_share(name: &str) -> Option<String> {
        let ps = format!(
            "$s = Get-SmbShare -Name '{}' -ErrorAction SilentlyContinue; if ($s) {{ $s.Path }} else {{ 'NOT_FOUND' }}",
            name.replace('\'', "''")
        );
        let output = match run_command(
            "powershell.exe",
            &["-NoProfile", "-NonInteractive", "-Command", &ps],
        ) {
            Ok(o) if o.status.success() => o,
            _ => return None,
        };

        let stdout = String::from_utf8_lossy(&output.stdout).trim().to_string();
        if stdout == "NOT_FOUND" || stdout.is_empty() {
            None
        } else {
            Some(stdout)
        }
    }

    /// Build access parameter arguments for New-SmbShare.
    fn build_access_args(params: &HashMap<String, String>) -> String {
        let mut args = String::new();

        if let Some(full) = params.get("full_access") {
            let full = full.trim();
            if !full.is_empty() {
                args.push_str(&format!(
                    " -FullAccess {}",
                    Self::quote_access_list(full)
                ));
            }
        }
        if let Some(read) = params.get("read_access") {
            let read = read.trim();
            if !read.is_empty() {
                args.push_str(&format!(
                    " -ReadAccess {}",
                    Self::quote_access_list(read)
                ));
            }
        }
        if let Some(change) = params.get("change_access") {
            let change = change.trim();
            if !change.is_empty() {
                args.push_str(&format!(
                    " -ChangeAccess {}",
                    Self::quote_access_list(change)
                ));
            }
        }
        if let Some(no) = params.get("no_access") {
            let no = no.trim();
            if !no.is_empty() {
                args.push_str(&format!(
                    " -NoAccess {}",
                    Self::quote_access_list(no)
                ));
            }
        }

        args
    }

    /// Format a comma-separated list of users into PowerShell-quoted arguments.
    /// "Admin,DOMAIN\\Users" → "'Admin','DOMAIN\\Users'"
    fn quote_access_list(csv: &str) -> String {
        csv.split(',')
            .map(|s| format!("'{}'", s.trim().replace('\'', "''")))
            .collect::<Vec<_>>()
            .join(",")
    }
}

impl Executor for SmbShareExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let share = params.get("share").cloned().unwrap_or_default();
        let path = params.get("path").cloned().unwrap_or_default();
        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");
        let description = params.get("description").cloned().unwrap_or_default();

        let mut result = ResourceResult::new("smb_share", &share);

        if share.is_empty() {
            result.error = Some(
                "smb_share executor: share param is required".to_string(),
            );
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        match state {
            "present" => {
                if path.is_empty() {
                    result.error = Some(
                        "smb_share executor: path param is required when state=present".to_string(),
                    );
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                let existing_path = Self::get_share(&share);

                match existing_path {
                    Some(ref current_path)
                        if current_path.eq_ignore_ascii_case(&path) =>
                    {
                        // Share exists with the correct path — no change needed.
                        result.action = Action::None;
                        result.duration_ms = start.elapsed().as_millis() as i32;
                        return result;
                    }
                    Some(_) => {
                        // Share exists but path differs — remove and recreate.
                        result.action = Action::Updated;
                        result.changed = true;

                        if dry_run {
                            result.duration_ms = start.elapsed().as_millis() as i32;
                            return result;
                        }

                        // Remove existing share.
                        let remove_ps = format!(
                            "Remove-SmbShare -Name '{}' -Force -ErrorAction Stop",
                            share.replace('\'', "''")
                        );
                        match run_command(
                            "powershell.exe",
                            &["-NoProfile", "-NonInteractive", "-Command", &remove_ps],
                        ) {
                            Ok(o) if !o.status.success() => {
                                let stderr = String::from_utf8_lossy(&o.stderr);
                                result.error = Some(format!(
                                    "smb_share remove {}: {}",
                                    share,
                                    stderr.trim()
                                ));
                                result.duration_ms = start.elapsed().as_millis() as i32;
                                return result;
                            }
                            Err(e) => {
                                result.error = Some(format!(
                                    "smb_share remove {}: {}",
                                    share, e
                                ));
                                result.duration_ms = start.elapsed().as_millis() as i32;
                                return result;
                            }
                            _ => {}
                        }

                        // Fall through to create below.
                    }
                    None => {
                        // Share does not exist — create it.
                        result.action = Action::Created;
                        result.changed = true;

                        if dry_run {
                            result.duration_ms = start.elapsed().as_millis() as i32;
                            return result;
                        }
                    }
                }

                // Create the share.
                let mut create_ps = format!(
                    "New-SmbShare -Name '{}' -Path '{}'",
                    share.replace('\'', "''"),
                    path.replace('\'', "''"),
                );

                if !description.is_empty() {
                    create_ps.push_str(&format!(
                        " -Description '{}'",
                        description.replace('\'', "''")
                    ));
                }

                create_ps.push_str(&Self::build_access_args(params));
                create_ps.push_str(" -ErrorAction Stop");

                match run_command(
                    "powershell.exe",
                    &["-NoProfile", "-NonInteractive", "-Command", &create_ps],
                ) {
                    Ok(o) if !o.status.success() => {
                        let stderr = String::from_utf8_lossy(&o.stderr);
                        let stdout = String::from_utf8_lossy(&o.stdout);
                        result.error = Some(format!(
                            "smb_share create {}: {} {}",
                            share,
                            stderr.trim(),
                            stdout.trim()
                        ));
                    }
                    Err(e) => {
                        result.error = Some(format!("smb_share create {}: {}", share, e));
                    }
                    _ => {}
                }
            }
            "absent" => {
                if Self::get_share(&share).is_none() {
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

                let ps = format!(
                    "Remove-SmbShare -Name '{}' -Force -ErrorAction Stop",
                    share.replace('\'', "''")
                );

                match run_command(
                    "powershell.exe",
                    &["-NoProfile", "-NonInteractive", "-Command", &ps],
                ) {
                    Ok(o) if !o.status.success() => {
                        let stderr = String::from_utf8_lossy(&o.stderr);
                        let stdout = String::from_utf8_lossy(&o.stdout);
                        result.error = Some(format!(
                            "smb_share remove {}: {} {}",
                            share,
                            stderr.trim(),
                            stdout.trim()
                        ));
                    }
                    Err(e) => {
                        result.error = Some(format!("smb_share remove {}: {}", share, e));
                    }
                    _ => {}
                }
            }
            other => {
                result.error = Some(format!(
                    "smb_share executor: unknown state {:?} (must be present or absent)",
                    other
                ));
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
    fn missing_share_param() {
        let exec = SmbShareExecutor::new();
        let params = HashMap::new();
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("share param is required"));
    }

    #[test]
    fn missing_path_for_present() {
        let exec = SmbShareExecutor::new();
        let mut params = HashMap::new();
        params.insert("share".to_string(), "Data".to_string());
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(
            result
                .error
                .unwrap()
                .contains("path param is required when state=present")
        );
    }

    #[test]
    fn invalid_state() {
        let exec = SmbShareExecutor::new();
        let mut params = HashMap::new();
        params.insert("share".to_string(), "Data".to_string());
        params.insert("path".to_string(), "D:\\Shares\\Data".to_string());
        params.insert("state".to_string(), "bogus".to_string());
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("unknown state"));
    }

    #[test]
    fn quote_access_list_formats_correctly() {
        let result = SmbShareExecutor::quote_access_list("Admin,DOMAIN\\Users");
        assert_eq!(result, "'Admin','DOMAIN\\Users'");
    }
}
