// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0
#![cfg(windows)]

//! IIS Site executor — manages IIS web sites idempotently via PowerShell
//! WebAdministration module.
//!
//! Parameters:
//! - `site` (required): IIS site name
//! - `physical_path`: Filesystem path for site content (required for present)
//! - `bindings`: Comma-separated bindings in `protocol:port:hostname` format
//!   (e.g., "http:80:", "https:443:www.example.com")
//! - `app_pool`: Application pool name (created if it doesn't exist)
//! - `state`: "started" (default), "stopped", "absent"
//!
//! Prerequisites: IIS-WebServer feature must be installed. Use `windows_feature`
//! executor with `depends_on` to ensure it.

use super::{Action, Executor, ResourceResult, run_command};
use std::collections::HashMap;
use std::time::Instant;

pub struct IisSiteExecutor;

impl IisSiteExecutor {
    pub fn new() -> Self {
        Self
    }

    /// Run a PowerShell command with WebAdministration module imported.
    fn ps(script: &str) -> Result<String, String> {
        let full = format!("Import-Module WebAdministration -ErrorAction SilentlyContinue; {}", script);
        match run_command("powershell", &["-NoProfile", "-Command", &full]) {
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

    /// Check if a site exists and get its state.
    fn site_state(site: &str) -> Option<String> {
        let script = format!(
            "if (Test-Path 'IIS:\\Sites\\{}') {{ (Get-WebItemState 'IIS:\\Sites\\{}').Value }} else {{ Write-Output 'NOTFOUND' }}",
            site.replace('\'', "''"),
            site.replace('\'', "''"),
        );
        match Self::ps(&script) {
            Ok(s) if s == "NOTFOUND" => None,
            Ok(s) => Some(s),
            Err(_) => None,
        }
    }
}

impl Executor for IisSiteExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let site = params.get("site").cloned().unwrap_or_default();
        let physical_path = params.get("physical_path").cloned().unwrap_or_default();
        let bindings_str = params.get("bindings").cloned().unwrap_or("http:80:".to_string());
        let app_pool = params.get("app_pool").cloned().unwrap_or_default();
        let state = params.get("state").map(|s| s.as_str()).unwrap_or("started");
        let resource_name = params.get("name").map(|s| s.as_str()).unwrap_or(&site);

        let mut result = ResourceResult::new("iis_site", resource_name);

        if site.is_empty() {
            result.error = Some("iis_site executor: site param is required".to_string());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let current = Self::site_state(&site);

        match state {
            "absent" => {
                if current.is_none() {
                    result.action = Action::None;
                } else {
                    result.action = Action::Deleted;
                    result.changed = true;
                    if !dry_run {
                        let script = format!(
                            "Remove-WebSite -Name '{}'",
                            site.replace('\'', "''"),
                        );
                        if let Err(e) = Self::ps(&script) {
                            result.error = Some(format!("Remove-WebSite {}: {}", site, e));
                        }
                    }
                }
            }
            "started" | "stopped" => {
                if physical_path.is_empty() && current.is_none() {
                    result.error = Some("iis_site executor: physical_path is required when creating a site".to_string());
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                if current.is_none() {
                    // Create the site.
                    result.action = Action::Created;
                    result.changed = true;
                    if !dry_run {
                        // Ensure app pool exists.
                        if !app_pool.is_empty() {
                            let pool_script = format!(
                                "if (-not (Test-Path 'IIS:\\AppPools\\{}')) {{ New-WebAppPool -Name '{}' }}",
                                app_pool.replace('\'', "''"),
                                app_pool.replace('\'', "''"),
                            );
                            if let Err(e) = Self::ps(&pool_script) {
                                result.error = Some(format!("New-WebAppPool {}: {}", app_pool, e));
                                result.duration_ms = start.elapsed().as_millis() as i32;
                                return result;
                            }
                        }

                        // Parse first binding for New-WebSite.
                        let first_binding = bindings_str.split(',').next().unwrap_or("http:80:");
                        let binding_parts: Vec<&str> = first_binding.split(':').collect();
                        let protocol = binding_parts.first().unwrap_or(&"http");
                        let port = binding_parts.get(1).unwrap_or(&"80");

                        let mut create_script = format!(
                            "New-WebSite -Name '{}' -PhysicalPath '{}' -Port {} -Protocol {}",
                            site.replace('\'', "''"),
                            physical_path.replace('\'', "''"),
                            port,
                            protocol,
                        );
                        if !app_pool.is_empty() {
                            create_script.push_str(&format!(
                                " -ApplicationPool '{}'",
                                app_pool.replace('\'', "''"),
                            ));
                        }

                        if let Err(e) = Self::ps(&create_script) {
                            result.error = Some(format!("New-WebSite {}: {}", site, e));
                            result.duration_ms = start.elapsed().as_millis() as i32;
                            return result;
                        }

                        // Set desired state.
                        if state == "stopped" {
                            let _ = Self::ps(&format!("Stop-WebSite -Name '{}'", site.replace('\'', "''")));
                        }
                    }
                } else {
                    // Site exists — check if state matches.
                    let current_state = current.unwrap_or_default().to_lowercase();
                    let desired_state = state.to_lowercase();

                    if current_state == desired_state {
                        result.action = Action::None;
                    } else {
                        result.action = Action::Updated;
                        result.changed = true;
                        if !dry_run {
                            let cmd = if desired_state == "started" {
                                format!("Start-WebSite -Name '{}'", site.replace('\'', "''"))
                            } else {
                                format!("Stop-WebSite -Name '{}'", site.replace('\'', "''"))
                            };
                            if let Err(e) = Self::ps(&cmd) {
                                result.error = Some(format!("{}: {}", cmd, e));
                            }
                        }
                    }
                }
            }
            other => {
                result.error = Some(format!(
                    "iis_site executor: unknown state {:?} (must be started, stopped, or absent)", other
                ));
            }
        }

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}
