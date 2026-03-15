// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0
#![cfg(windows)]

//! IIS Application Pool executor — manages IIS application pools idempotently
//! via PowerShell WebAdministration module.
//!
//! Parameters:
//! - `app_pool` (required): Pool name
//! - `state`: "started" (default), "stopped", "absent"
//! - `runtime_version`: .NET CLR version (e.g., "v4.0", "v2.0", "" for No Managed Code)
//! - `pipeline_mode`: "Integrated" (default) or "Classic"
//! - `identity_type`: "ApplicationPoolIdentity" (default), "LocalSystem",
//!   "LocalService", "NetworkService", "SpecificUser"
//! - `username`: Username when identity_type=SpecificUser
//! - `password`: Password when identity_type=SpecificUser
//! - `idle_timeout`: Minutes before idle shutdown (default "20", "0" = no timeout)
//! - `recycle_interval`: Minutes between periodic recycling (default "1740" = 29h, "0" = no recycle)
//!
//! Prerequisites: IIS-WebServer feature must be installed.

use super::{Action, Executor, ResourceResult, run_command};
use std::collections::HashMap;
use std::time::Instant;

pub struct IisAppPoolExecutor;

impl IisAppPoolExecutor {
    pub fn new() -> Self {
        Self
    }

    /// Run a PowerShell command with WebAdministration module imported.
    fn ps(script: &str) -> Result<String, String> {
        let full = format!(
            "Import-Module WebAdministration -ErrorAction SilentlyContinue; {}",
            script
        );
        match run_command(
            "powershell.exe",
            &["-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", &full],
        ) {
            Ok(o) if o.status.success() => {
                Ok(String::from_utf8_lossy(&o.stdout).trim().to_string())
            }
            Ok(o) => {
                let stderr = String::from_utf8_lossy(&o.stderr).trim().to_string();
                let stdout = String::from_utf8_lossy(&o.stdout).trim().to_string();
                Err(format!("{} {}", stderr, stdout))
            }
            Err(e) => Err(e.to_string()),
        }
    }

    /// Check if an app pool exists. Returns Some(state) if it exists, None otherwise.
    /// State is "Started" or "Stopped".
    fn pool_state(pool: &str) -> Option<String> {
        let script = format!(
            "try {{ (Get-WebAppPoolState '{}').Value }} catch {{ Write-Output 'NOTFOUND' }}",
            pool.replace('\'', "''"),
        );
        match Self::ps(&script) {
            Ok(s) if s == "NOTFOUND" || s.is_empty() => None,
            Ok(s) => Some(s.to_lowercase()),
            Err(_) => None,
        }
    }

    /// Map identity_type param to the IIS numeric ProcessModel.IdentityType.
    fn identity_type_value(identity: &str) -> &str {
        match identity {
            "LocalSystem" => "0",
            "LocalService" => "1",
            "NetworkService" => "2",
            "SpecificUser" => "3",
            _ => "4", // ApplicationPoolIdentity
        }
    }
}

impl Executor for IisAppPoolExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let pool = params.get("app_pool").cloned().unwrap_or_default();
        let state = params.get("state").map(|s| s.as_str()).unwrap_or("started");
        let runtime_version = params.get("runtime_version").cloned();
        let pipeline_mode = params.get("pipeline_mode").map(|s| s.as_str()).unwrap_or("Integrated");
        let identity_type = params.get("identity_type").map(|s| s.as_str()).unwrap_or("ApplicationPoolIdentity");
        let username = params.get("username").cloned().unwrap_or_default();
        let password = params.get("password").cloned().unwrap_or_default();
        let idle_timeout = params.get("idle_timeout").cloned().unwrap_or_else(|| "20".to_string());
        let recycle_interval = params.get("recycle_interval").cloned().unwrap_or_else(|| "1740".to_string());

        let mut result = ResourceResult::new("iis_app_pool", &pool);

        if pool.is_empty() {
            result.error = Some("iis_app_pool executor: app_pool param is required".to_string());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        if !matches!(state, "started" | "stopped" | "absent") {
            result.error = Some(format!(
                "iis_app_pool executor: unknown state {:?} (must be started, stopped, or absent)",
                state
            ));
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        if identity_type == "SpecificUser" && (username.is_empty() || password.is_empty()) {
            result.error = Some(
                "iis_app_pool executor: username and password are required when identity_type=SpecificUser".to_string(),
            );
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let current = Self::pool_state(&pool);

        match state {
            "absent" => {
                if current.is_none() {
                    result.action = Action::None;
                } else {
                    result.action = Action::Deleted;
                    result.changed = true;
                    if !dry_run {
                        // Stop pool first if running, then remove.
                        let script = format!(
                            "Stop-WebAppPool -Name '{}' -ErrorAction SilentlyContinue; Remove-WebAppPool -Name '{}'",
                            pool.replace('\'', "''"),
                            pool.replace('\'', "''"),
                        );
                        if let Err(e) = Self::ps(&script) {
                            result.error = Some(format!("Remove-WebAppPool {}: {}", pool, e));
                        }
                    }
                }
            }
            "started" | "stopped" => {
                if current.is_none() {
                    // Create the pool.
                    result.action = Action::Created;
                    result.changed = true;
                    if !dry_run {
                        let create_script = format!(
                            "New-WebAppPool -Name '{}'",
                            pool.replace('\'', "''"),
                        );
                        if let Err(e) = Self::ps(&create_script) {
                            result.error = Some(format!("New-WebAppPool {}: {}", pool, e));
                            result.duration_ms = start.elapsed().as_millis() as i32;
                            return result;
                        }

                        // Set pool properties.
                        let pool_path = format!("IIS:\\AppPools\\{}", pool.replace('\'', "''"));
                        let mut props = Vec::new();

                        if let Some(ref rv) = runtime_version {
                            props.push(format!(
                                "Set-ItemProperty '{}' managedRuntimeVersion '{}'",
                                pool_path, rv.replace('\'', "''"),
                            ));
                        }

                        props.push(format!(
                            "Set-ItemProperty '{}' managedPipelineMode '{}'",
                            pool_path, pipeline_mode,
                        ));

                        let id_val = Self::identity_type_value(identity_type);
                        props.push(format!(
                            "Set-ItemProperty '{}' processModel.identityType {}",
                            pool_path, id_val,
                        ));

                        if identity_type == "SpecificUser" {
                            props.push(format!(
                                "Set-ItemProperty '{}' processModel.userName '{}'",
                                pool_path, username.replace('\'', "''"),
                            ));
                            props.push(format!(
                                "Set-ItemProperty '{}' processModel.password '{}'",
                                pool_path, password.replace('\'', "''"),
                            ));
                        }

                        props.push(format!(
                            "Set-ItemProperty '{}' processModel.idleTimeout (New-TimeSpan -Minutes {})",
                            pool_path, idle_timeout,
                        ));

                        props.push(format!(
                            "Set-ItemProperty '{}' recycling.periodicRestart.time (New-TimeSpan -Minutes {})",
                            pool_path, recycle_interval,
                        ));

                        let props_script = props.join("; ");
                        if let Err(e) = Self::ps(&props_script) {
                            result.error = Some(format!(
                                "iis_app_pool configure {}: {}", pool, e
                            ));
                            result.duration_ms = start.elapsed().as_millis() as i32;
                            return result;
                        }

                        // Set desired state.
                        if state == "stopped" {
                            let _ = Self::ps(&format!(
                                "Stop-WebAppPool -Name '{}'",
                                pool.replace('\'', "''")
                            ));
                        }
                    }
                } else {
                    // Pool exists — check if state matches.
                    let current_state = current.unwrap_or_default();
                    if current_state == state {
                        result.action = Action::None;
                    } else {
                        result.action = Action::Updated;
                        result.changed = true;
                        if !dry_run {
                            let cmd = if state == "started" {
                                format!(
                                    "Start-WebAppPool -Name '{}'",
                                    pool.replace('\'', "''")
                                )
                            } else {
                                format!(
                                    "Stop-WebAppPool -Name '{}'",
                                    pool.replace('\'', "''")
                                )
                            };
                            if let Err(e) = Self::ps(&cmd) {
                                result.error = Some(format!("{}: {}", cmd, e));
                            }
                        }
                    }
                }
            }
            _ => unreachable!(), // Already validated above.
        }

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn missing_app_pool_param() {
        let exec = IisAppPoolExecutor::new();
        let params = HashMap::new();
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("app_pool param is required"));
    }

    #[test]
    fn invalid_state() {
        let exec = IisAppPoolExecutor::new();
        let mut params = HashMap::new();
        params.insert("app_pool".to_string(), "TestPool".to_string());
        params.insert("state".to_string(), "bogus".to_string());
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("unknown state"));
    }

    #[test]
    fn specific_user_requires_credentials() {
        let exec = IisAppPoolExecutor::new();
        let mut params = HashMap::new();
        params.insert("app_pool".to_string(), "TestPool".to_string());
        params.insert("identity_type".to_string(), "SpecificUser".to_string());
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("username and password are required"));
    }

    #[test]
    fn identity_type_mapping() {
        assert_eq!(IisAppPoolExecutor::identity_type_value("LocalSystem"), "0");
        assert_eq!(IisAppPoolExecutor::identity_type_value("LocalService"), "1");
        assert_eq!(IisAppPoolExecutor::identity_type_value("NetworkService"), "2");
        assert_eq!(IisAppPoolExecutor::identity_type_value("SpecificUser"), "3");
        assert_eq!(IisAppPoolExecutor::identity_type_value("ApplicationPoolIdentity"), "4");
    }
}
