// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0
#![cfg(windows)]

//! Windows ACL executor — manages NTFS ACL permissions idempotently via
//! PowerShell `Get-Acl`/`Set-Acl`.
//!
//! Parameters:
//! - `path` (required): File or directory path
//! - `identity` (required): User or group (e.g., "BUILTIN\\Administrators", "IIS_IUSRS")
//! - `rights` (required): Comma-separated rights: "FullControl", "Modify",
//!   "ReadAndExecute", "Read", "Write"
//! - `type`: "Allow" (default) or "Deny"
//! - `inheritance`: Inheritance flags — "ContainerInherit,ObjectInherit" (default
//!   for directories), "None" (default for files)
//! - `state`: "present" (default) or "absent"
//!
//! Check: `(Get-Acl '<path>').Access | Where-Object { $_.IdentityReference -eq '<identity>' }`
//! Set: Build an `AccessRule` object and call `$acl.SetAccessRule()` then `Set-Acl`.
//! Remove: `$acl.RemoveAccessRule()`.

use super::{Action, Executor, ResourceResult, run_command};
use std::collections::HashMap;
use std::time::Instant;

pub struct AclWindowsExecutor;

impl AclWindowsExecutor {
    pub fn new() -> Self {
        Self
    }

    /// Check if the specified ACL rule already exists with matching rights and type.
    /// Returns true if an ACL entry matches identity, rights, and access control type.
    fn rule_exists(path: &str, identity: &str, rights: &str, access_type: &str) -> bool {
        let ps = format!(
            "$acl = Get-Acl -Path '{}'; \
             $match = $acl.Access | Where-Object {{ \
                 $_.IdentityReference.Value -eq '{}' -and \
                 $_.FileSystemRights.ToString() -eq '{}' -and \
                 $_.AccessControlType.ToString() -eq '{}' \
             }}; \
             if ($match) {{ Write-Output 'FOUND' }} else {{ Write-Output 'MISSING' }}",
            path.replace('\'', "''"),
            identity.replace('\'', "''"),
            rights.replace('\'', "''"),
            access_type,
        );
        match run_command("powershell.exe", &["-NoProfile", "-NonInteractive", "-Command", &ps]) {
            Ok(o) if o.status.success() => {
                let stdout = String::from_utf8_lossy(&o.stdout).trim().to_string();
                stdout == "FOUND"
            }
            _ => false,
        }
    }

    /// Determine the default inheritance flags based on whether the path is a
    /// directory or a file.
    fn default_inheritance(path: &str) -> &'static str {
        let ps = format!(
            "if (Test-Path -Path '{}' -PathType Container) {{ Write-Output 'DIR' }} else {{ Write-Output 'FILE' }}",
            path.replace('\'', "''"),
        );
        match run_command("powershell.exe", &["-NoProfile", "-NonInteractive", "-Command", &ps]) {
            Ok(o) if o.status.success() => {
                let stdout = String::from_utf8_lossy(&o.stdout).trim().to_string();
                if stdout == "DIR" {
                    "ContainerInherit,ObjectInherit"
                } else {
                    "None"
                }
            }
            _ => "None",
        }
    }
}

impl Executor for AclWindowsExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let path = params.get("path").cloned().unwrap_or_default();
        let identity = params.get("identity").cloned().unwrap_or_default();
        let rights = params.get("rights").cloned().unwrap_or_default();
        let access_type = params.get("type").map(|s| s.as_str()).unwrap_or("Allow");
        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");
        let resource_name = if !identity.is_empty() {
            format!("{} on {}", identity, path)
        } else {
            path.clone()
        };

        let mut result = ResourceResult::new("acl", &resource_name);

        if path.is_empty() {
            result.error = Some("acl executor: path param is required".to_string());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        if identity.is_empty() {
            result.error = Some("acl executor: identity param is required".to_string());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        if rights.is_empty() {
            result.error = Some("acl executor: rights param is required".to_string());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        // Validate access type.
        if access_type != "Allow" && access_type != "Deny" {
            result.error = Some(format!(
                "acl executor: type must be Allow or Deny, got {:?}",
                access_type
            ));
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let inheritance = params
            .get("inheritance")
            .map(|s| s.as_str())
            .unwrap_or_else(|| Self::default_inheritance(&path));

        match state {
            "present" => {
                if Self::rule_exists(&path, &identity, &rights, access_type) {
                    result.action = Action::None;
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                result.action = Action::Created;
                result.changed = true;

                if dry_run {
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                let ps = format!(
                    "$acl = Get-Acl -Path '{}'; \
                     $rule = New-Object System.Security.AccessControl.FileSystemAccessRule(\
                         '{}', '{}', '{}', 'None', '{}'); \
                     $acl.SetAccessRule($rule); \
                     Set-Acl -Path '{}' -AclObject $acl",
                    path.replace('\'', "''"),
                    identity.replace('\'', "''"),
                    rights.replace('\'', "''"),
                    inheritance,
                    access_type,
                    path.replace('\'', "''"),
                );
                match run_command("powershell.exe", &["-NoProfile", "-NonInteractive", "-Command", &ps]) {
                    Ok(o) if !o.status.success() => {
                        let stderr = String::from_utf8_lossy(&o.stderr);
                        result.error = Some(format!(
                            "acl SetAccessRule on {}: {}",
                            path, stderr.trim()
                        ));
                    }
                    Err(e) => {
                        result.error = Some(format!(
                            "acl SetAccessRule on {}: {}",
                            path, e
                        ));
                    }
                    _ => {}
                }
            }
            "absent" => {
                if !Self::rule_exists(&path, &identity, &rights, access_type) {
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
                    "$acl = Get-Acl -Path '{}'; \
                     $rule = New-Object System.Security.AccessControl.FileSystemAccessRule(\
                         '{}', '{}', '{}', 'None', '{}'); \
                     $acl.RemoveAccessRule($rule) | Out-Null; \
                     Set-Acl -Path '{}' -AclObject $acl",
                    path.replace('\'', "''"),
                    identity.replace('\'', "''"),
                    rights.replace('\'', "''"),
                    inheritance,
                    access_type,
                    path.replace('\'', "''"),
                );
                match run_command("powershell.exe", &["-NoProfile", "-NonInteractive", "-Command", &ps]) {
                    Ok(o) if !o.status.success() => {
                        let stderr = String::from_utf8_lossy(&o.stderr);
                        result.error = Some(format!(
                            "acl RemoveAccessRule on {}: {}",
                            path, stderr.trim()
                        ));
                    }
                    Err(e) => {
                        result.error = Some(format!(
                            "acl RemoveAccessRule on {}: {}",
                            path, e
                        ));
                    }
                    _ => {}
                }
            }
            other => {
                result.error = Some(format!(
                    "acl executor: unknown state {:?} (must be present or absent)",
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
    fn missing_path() {
        let exec = AclWindowsExecutor::new();
        let mut params = HashMap::new();
        params.insert("identity".to_string(), "BUILTIN\\Administrators".to_string());
        params.insert("rights".to_string(), "FullControl".to_string());
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("path param is required"));
    }

    #[test]
    fn missing_identity() {
        let exec = AclWindowsExecutor::new();
        let mut params = HashMap::new();
        params.insert("path".to_string(), "C:\\test".to_string());
        params.insert("rights".to_string(), "FullControl".to_string());
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("identity param is required"));
    }

    #[test]
    fn missing_rights() {
        let exec = AclWindowsExecutor::new();
        let mut params = HashMap::new();
        params.insert("path".to_string(), "C:\\test".to_string());
        params.insert("identity".to_string(), "Users".to_string());
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("rights param is required"));
    }

    #[test]
    fn invalid_access_type() {
        let exec = AclWindowsExecutor::new();
        let mut params = HashMap::new();
        params.insert("path".to_string(), "C:\\test".to_string());
        params.insert("identity".to_string(), "Users".to_string());
        params.insert("rights".to_string(), "Read".to_string());
        params.insert("type".to_string(), "Maybe".to_string());
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("type must be Allow or Deny"));
    }

    #[test]
    fn invalid_state() {
        let exec = AclWindowsExecutor::new();
        let mut params = HashMap::new();
        params.insert("path".to_string(), "C:\\test".to_string());
        params.insert("identity".to_string(), "Users".to_string());
        params.insert("rights".to_string(), "Read".to_string());
        params.insert("state".to_string(), "bogus".to_string());
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("unknown state"));
    }
}
