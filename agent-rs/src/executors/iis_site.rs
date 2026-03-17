// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0
#![cfg(windows)]

//! IIS Site executor — manages IIS websites idempotently via PowerShell
//! WebAdministration module.
//!
//! Parameters:
//! - `site` (required): Site name (e.g., "Default Web Site", "MyApp")
//! - `state`: "started" (default), "stopped", "absent"
//! - `physical_path`: Root directory (e.g., "C:\\inetpub\\wwwroot\\myapp"). Required for new sites.
//! - `port`: Binding port (default "80")
//! - `protocol`: "http" (default) or "https"
//! - `host_header`: Host header for the binding (optional, e.g., "app.example.com")
//! - `app_pool`: Application pool name (optional, defaults to site name)
//! - `ssl_cert_thumbprint`: Certificate thumbprint for HTTPS bindings (required when protocol=https)
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

    /// Check if a site exists. Returns Some(state) if it exists, None otherwise.
    fn site_state(site: &str) -> Option<String> {
        let script = format!(
            "$s = Get-Website -Name '{}' -ErrorAction SilentlyContinue; if ($s) {{ $s.State }} else {{ 'NOTFOUND' }}",
            site.replace('\'', "''"),
        );
        match Self::ps(&script) {
            Ok(s) if s == "NOTFOUND" => None,
            Ok(s) => Some(s.to_lowercase()),
            Err(_) => None,
        }
    }
}

impl Executor for IisSiteExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let site = params.get("site").cloned().unwrap_or_default();
        let state = params.get("state").map(|s| s.as_str()).unwrap_or("started");
        let physical_path = params.get("physical_path").cloned().unwrap_or_default();
        let port = params.get("port").cloned().unwrap_or_else(|| "80".to_string());
        let protocol = params.get("protocol").map(|s| s.as_str()).unwrap_or("http");
        let host_header = params.get("host_header").cloned().unwrap_or_default();
        let app_pool = params.get("app_pool").cloned().unwrap_or_else(|| site.clone());
        let ssl_cert_thumbprint = params.get("ssl_cert_thumbprint").cloned().unwrap_or_default();

        let mut result = ResourceResult::new("iis_site", &site);

        if site.is_empty() {
            result.error = Some("iis_site executor: site param is required".to_string());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        if !matches!(state, "started" | "stopped" | "absent") {
            result.error = Some(format!(
                "iis_site executor: unknown state {:?} (must be started, stopped, or absent)",
                state
            ));
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        if protocol == "https" && ssl_cert_thumbprint.is_empty() {
            result.error = Some(
                "iis_site executor: ssl_cert_thumbprint is required when protocol=https".to_string(),
            );
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
                            "Remove-Website -Name '{}'",
                            site.replace('\'', "''"),
                        );
                        if let Err(e) = Self::ps(&script) {
                            result.error = Some(format!("Remove-Website {}: {}", site, e));
                        }
                    }
                }
            }
            "started" | "stopped" => {
                if physical_path.is_empty() && current.is_none() {
                    result.error = Some(
                        "iis_site executor: physical_path is required when creating a site"
                            .to_string(),
                    );
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                if current.is_none() {
                    // Create the site.
                    result.action = Action::Created;
                    result.changed = true;
                    if !dry_run {
                        // Ensure app pool exists.
                        let pool_script = format!(
                            "if (-not (Test-Path 'IIS:\\AppPools\\{}')) {{ New-WebAppPool -Name '{}' }}",
                            app_pool.replace('\'', "''"),
                            app_pool.replace('\'', "''"),
                        );
                        if let Err(e) = Self::ps(&pool_script) {
                            result.error =
                                Some(format!("New-WebAppPool {}: {}", app_pool, e));
                            result.duration_ms = start.elapsed().as_millis() as i32;
                            return result;
                        }

                        // Create the site.
                        let mut create_script = format!(
                            "New-Website -Name '{}' -PhysicalPath '{}' -Port {} -ApplicationPool '{}'",
                            site.replace('\'', "''"),
                            physical_path.replace('\'', "''"),
                            port,
                            app_pool.replace('\'', "''"),
                        );
                        if !host_header.is_empty() {
                            create_script.push_str(&format!(
                                " -HostHeader '{}'",
                                host_header.replace('\'', "''"),
                            ));
                        }

                        if let Err(e) = Self::ps(&create_script) {
                            result.error = Some(format!("New-Website {}: {}", site, e));
                            result.duration_ms = start.elapsed().as_millis() as i32;
                            return result;
                        }

                        // Add HTTPS binding if needed.
                        if protocol == "https" {
                            let https_script = format!(
                                "New-WebBinding -Name '{}' -Protocol https -Port {} -SslFlags 0; \
                                 (Get-ChildItem IIS:\\SslBindings\\0.0.0.0!{} -ErrorAction SilentlyContinue) | Remove-Item; \
                                 Get-Item 'Cert:\\LocalMachine\\My\\{}' | New-Item 'IIS:\\SslBindings\\0.0.0.0!{}'",
                                site.replace('\'', "''"),
                                port,
                                port,
                                ssl_cert_thumbprint,
                                port,
                            );
                            if let Err(e) = Self::ps(&https_script) {
                                result.error = Some(format!(
                                    "HTTPS binding for {}: {}", site, e
                                ));
                                result.duration_ms = start.elapsed().as_millis() as i32;
                                return result;
                            }
                        }

                        // Set desired state.
                        if state == "stopped" {
                            let _ = Self::ps(&format!(
                                "Stop-Website -Name '{}'",
                                site.replace('\'', "''")
                            ));
                        }
                    }
                } else {
                    // Site exists — check if state matches.
                    let current_state = current.unwrap_or_default();
                    if current_state == state {
                        result.action = Action::None;
                    } else {
                        result.action = Action::Updated;
                        result.changed = true;
                        if !dry_run {
                            let cmd = if state == "started" {
                                format!(
                                    "Start-Website -Name '{}'",
                                    site.replace('\'', "''")
                                )
                            } else {
                                format!(
                                    "Stop-Website -Name '{}'",
                                    site.replace('\'', "''")
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
    fn missing_site_param() {
        let exec = IisSiteExecutor::new();
        let params = HashMap::new();
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("site param is required"));
    }

    #[test]
    fn invalid_state() {
        let exec = IisSiteExecutor::new();
        let mut params = HashMap::new();
        params.insert("site".to_string(), "TestSite".to_string());
        params.insert("state".to_string(), "bogus".to_string());
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("unknown state"));
    }

    #[test]
    fn https_requires_cert_hash() {
        let exec = IisSiteExecutor::new();
        let mut params = HashMap::new();
        params.insert("site".to_string(), "SecureSite".to_string());
        params.insert("protocol".to_string(), "https".to_string());
        params.insert("physical_path".to_string(), "C:\\web".to_string());
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("ssl_cert_thumbprint is required"));
    }
}
