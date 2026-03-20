// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary
#![cfg(windows)]

//! Windows Registry executor — manages registry keys and values idempotently.
//!
//! Parameters:
//! - `path`: Registry key path (e.g., `HKLM:\SOFTWARE\Policies\Microsoft\Windows`)
//! - `name`: Value name within the key (omit to manage the key itself)
//! - `value`: Desired value data (required for state: present with a name)
//! - `type`: Value type: `string` (default), `dword`, `qword`, `binary`, `multi_string`, `expand_string`
//! - `state`: `present` (default) or `absent`
//!
//! Registry paths use PowerShell-style notation:
//!   HKLM:\ → HKEY_LOCAL_MACHINE
//!   HKCU:\ → HKEY_CURRENT_USER
//!   HKCR:\ → HKEY_CLASSES_ROOT
//!   HKU:\  → HKEY_USERS

use super::{Action, Executor, ResourceResult, run_command};
use std::collections::HashMap;
use std::time::Instant;

pub struct RegistryWindowsExecutor;

impl RegistryWindowsExecutor {
    pub fn new() -> Self {
        Self
    }

    /// Query a registry value via PowerShell. Returns None if the key or value doesn't exist.
    fn get_value(path: &str, name: &str) -> Option<String> {
        let ps = if name.is_empty() {
            // Check if the key exists.
            format!(
                "if (Test-Path '{}') {{ Write-Output 'EXISTS' }} else {{ Write-Output 'MISSING' }}",
                path.replace('\'', "''")
            )
        } else {
            format!(
                "try {{ (Get-ItemProperty -Path '{}' -Name '{}' -ErrorAction Stop).'{}' }} catch {{ Write-Output '___NOTFOUND___' }}",
                path.replace('\'', "''"),
                name.replace('\'', "''"),
                name.replace('\'', "''"),
            )
        };

        match run_command("powershell", &["-NoProfile", "-Command", &ps]) {
            Ok(o) if o.status.success() => {
                let val = String::from_utf8_lossy(&o.stdout).trim().to_string();
                if val == "___NOTFOUND___" || val == "MISSING" {
                    None
                } else if name.is_empty() && val == "EXISTS" {
                    Some(String::new())
                } else {
                    Some(val)
                }
            }
            _ => None,
        }
    }

    /// Map user-friendly type names to PowerShell registry types.
    fn ps_type(reg_type: &str) -> &str {
        match reg_type {
            "dword" => "DWord",
            "qword" => "QWord",
            "binary" => "Binary",
            "multi_string" => "MultiString",
            "expand_string" => "ExpandString",
            _ => "String",
        }
    }
}

impl Executor for RegistryWindowsExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let path = params.get("path").cloned().unwrap_or_default();
        let name = params.get("name").cloned().unwrap_or_default();
        let value = params.get("value").cloned().unwrap_or_default();
        let reg_type = params.get("type").map(|s| s.as_str()).unwrap_or("string");
        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");
        let resource_name = params.get("name").map(|s| s.as_str()).unwrap_or(&path);

        let mut result = ResourceResult::new("registry", resource_name);

        if path.is_empty() {
            result.error = Some("registry executor: path param is required".to_string());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        // Validate the registry hive prefix.
        let valid_hives = ["HKLM:\\", "HKCU:\\", "HKCR:\\", "HKU:\\", "HKCC:\\"];
        if !valid_hives.iter().any(|h| path.starts_with(h)) {
            result.error = Some(format!(
                "registry executor: path must start with a registry hive (HKLM:\\, HKCU:\\, HKCR:\\, HKU:\\, HKCC:\\), got {:?}",
                path
            ));
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        match state {
            "present" => {
                if name.is_empty() {
                    // Ensure the key exists.
                    if Self::get_value(&path, "").is_some() {
                        result.action = Action::None;
                    } else {
                        result.action = Action::Created;
                        result.changed = true;
                        if !dry_run {
                            let ps = format!(
                                "New-Item -Path '{}' -Force | Out-Null",
                                path.replace('\'', "''")
                            );
                            match run_command("powershell", &["-NoProfile", "-Command", &ps]) {
                                Ok(o) if !o.status.success() => {
                                    result.error = Some(format!(
                                        "New-Item {}: {}",
                                        path, String::from_utf8_lossy(&o.stderr)
                                    ));
                                }
                                Err(e) => {
                                    result.error = Some(format!("New-Item {}: {}", path, e));
                                }
                                _ => {}
                            }
                        }
                    }
                } else {
                    // Ensure the value exists with the desired data.
                    let current = Self::get_value(&path, &name);

                    if current.as_deref() == Some(&value) {
                        result.action = Action::None;
                    } else {
                        result.action = if current.is_some() { Action::Updated } else { Action::Created };
                        result.changed = true;
                        if !dry_run {
                            let ps_t = Self::ps_type(reg_type);
                            let ps = format!(
                                "New-Item -Path '{}' -Force | Out-Null; Set-ItemProperty -Path '{}' -Name '{}' -Value '{}' -Type {} -Force",
                                path.replace('\'', "''"),
                                path.replace('\'', "''"),
                                name.replace('\'', "''"),
                                value.replace('\'', "''"),
                                ps_t,
                            );
                            match run_command("powershell", &["-NoProfile", "-Command", &ps]) {
                                Ok(o) if !o.status.success() => {
                                    result.error = Some(format!(
                                        "Set-ItemProperty {}\\{}: {}",
                                        path, name, String::from_utf8_lossy(&o.stderr)
                                    ));
                                }
                                Err(e) => {
                                    result.error = Some(format!(
                                        "Set-ItemProperty {}\\{}: {}", path, name, e
                                    ));
                                }
                                _ => {}
                            }
                        }
                    }
                }
            }
            "absent" => {
                if name.is_empty() {
                    // Remove the entire key.
                    if Self::get_value(&path, "").is_none() {
                        result.action = Action::None;
                    } else {
                        result.action = Action::Deleted;
                        result.changed = true;
                        if !dry_run {
                            let ps = format!(
                                "Remove-Item -Path '{}' -Recurse -Force",
                                path.replace('\'', "''")
                            );
                            match run_command("powershell", &["-NoProfile", "-Command", &ps]) {
                                Ok(o) if !o.status.success() => {
                                    result.error = Some(format!(
                                        "Remove-Item {}: {}",
                                        path, String::from_utf8_lossy(&o.stderr)
                                    ));
                                }
                                Err(e) => {
                                    result.error = Some(format!("Remove-Item {}: {}", path, e));
                                }
                                _ => {}
                            }
                        }
                    }
                } else {
                    // Remove a single value.
                    if Self::get_value(&path, &name).is_none() {
                        result.action = Action::None;
                    } else {
                        result.action = Action::Deleted;
                        result.changed = true;
                        if !dry_run {
                            let ps = format!(
                                "Remove-ItemProperty -Path '{}' -Name '{}' -Force",
                                path.replace('\'', "''"),
                                name.replace('\'', "''"),
                            );
                            match run_command("powershell", &["-NoProfile", "-Command", &ps]) {
                                Ok(o) if !o.status.success() => {
                                    result.error = Some(format!(
                                        "Remove-ItemProperty {}\\{}: {}",
                                        path, name, String::from_utf8_lossy(&o.stderr)
                                    ));
                                }
                                Err(e) => {
                                    result.error = Some(format!(
                                        "Remove-ItemProperty {}\\{}: {}", path, name, e
                                    ));
                                }
                                _ => {}
                            }
                        }
                    }
                }
            }
            other => {
                result.error = Some(format!(
                    "registry executor: unknown state {:?} (must be present or absent)", other
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
        let exec = RegistryWindowsExecutor::new();
        let params = HashMap::new();
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("path param is required"));
    }

    #[test]
    fn invalid_hive_prefix() {
        let exec = RegistryWindowsExecutor::new();
        let mut params = HashMap::new();
        params.insert("path".to_string(), "BOGUS:\\SOFTWARE\\Test".to_string());
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result
            .error
            .unwrap()
            .contains("path must start with a registry hive"));
    }

    #[test]
    fn unknown_state() {
        let exec = RegistryWindowsExecutor::new();
        let mut params = HashMap::new();
        params.insert("path".to_string(), "HKLM:\\SOFTWARE\\Test".to_string());
        params.insert("state".to_string(), "bogus".to_string());
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("unknown state"));
    }

    #[test]
    fn ps_type_mapping() {
        assert_eq!(RegistryWindowsExecutor::ps_type("dword"), "DWord");
        assert_eq!(RegistryWindowsExecutor::ps_type("qword"), "QWord");
        assert_eq!(RegistryWindowsExecutor::ps_type("binary"), "Binary");
        assert_eq!(RegistryWindowsExecutor::ps_type("multi_string"), "MultiString");
        assert_eq!(RegistryWindowsExecutor::ps_type("expand_string"), "ExpandString");
        assert_eq!(RegistryWindowsExecutor::ps_type("string"), "String");
        assert_eq!(RegistryWindowsExecutor::ps_type("unknown"), "String");
    }
}
