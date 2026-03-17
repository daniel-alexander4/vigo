// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0
#![cfg(windows)]

//! Windows Certificate Store executor — manages certificates in Windows
//! certificate stores idempotently via PowerShell.
//!
//! Parameters:
//! - `thumbprint`: Certificate thumbprint (for check/remove operations)
//! - `source_path`: Path to certificate file (.pfx, .cer, .crt). Required for import.
//! - `password`: PFX password (optional, for .pfx files)
//! - `store`: Certificate store location, default "Cert:\\LocalMachine\\My"
//!   (other common: "Cert:\\LocalMachine\\Root", "Cert:\\LocalMachine\\CA")
//! - `state`: "present" (default) or "absent"
//! - `exportable`: "true"/"false" (default "false") — make private key exportable on import
//!
//! When `source_path` is provided without `thumbprint`, import the cert and report the
//! thumbprint. When `thumbprint` is provided without source_path, just verify existence.

use super::{Action, Executor, ResourceResult, run_command};
use std::collections::HashMap;
use std::time::Instant;

pub struct CertificateWindowsExecutor;

impl CertificateWindowsExecutor {
    pub fn new() -> Self {
        Self
    }

    /// Run a PowerShell command. Returns stdout on success, error message on failure.
    fn ps(script: &str) -> Result<String, String> {
        match run_command(
            "powershell.exe",
            &["-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script],
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

    /// Check if a certificate with the given thumbprint exists in the store.
    fn cert_exists(store: &str, thumbprint: &str) -> bool {
        let script = format!(
            "if (Get-ChildItem '{}' | Where-Object {{ $_.Thumbprint -eq '{}' }}) {{ 'EXISTS' }} else {{ 'MISSING' }}",
            store.replace('\'', "''"),
            thumbprint.replace('\'', "''"),
        );
        matches!(Self::ps(&script), Ok(s) if s == "EXISTS")
    }

    /// Determine if a source file is a PFX (by extension).
    fn is_pfx(source: &str) -> bool {
        source.to_lowercase().ends_with(".pfx") || source.to_lowercase().ends_with(".p12")
    }
}

impl Executor for CertificateWindowsExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let thumbprint = params.get("thumbprint").cloned().unwrap_or_default();
        let source = params.get("source_path").cloned().unwrap_or_default();
        let password = params.get("password").cloned().unwrap_or_default();
        let store = params
            .get("store")
            .cloned()
            .unwrap_or_else(|| "Cert:\\LocalMachine\\My".to_string());
        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");
        let exportable = params.get("exportable").map(|s| s == "true").unwrap_or(false);

        let display = if !thumbprint.is_empty() {
            thumbprint.clone()
        } else if !source.is_empty() {
            source.clone()
        } else {
            store.clone()
        };
        let mut result = ResourceResult::new("certificate_windows", &display);

        if thumbprint.is_empty() && source.is_empty() {
            result.error = Some(
                "certificate_windows executor: either thumbprint or source_path param is required"
                    .to_string(),
            );
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        if !matches!(state, "present" | "absent") {
            result.error = Some(format!(
                "certificate_windows executor: unknown state {:?} (must be present or absent)",
                state
            ));
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        match state {
            "present" => {
                // If thumbprint is provided, check existence.
                if !thumbprint.is_empty() && source.is_empty() {
                    if Self::cert_exists(&store, &thumbprint) {
                        result.action = Action::None;
                    } else {
                        // Cannot import without source_path — just report missing.
                        result.error = Some(format!(
                            "certificate_windows: certificate {} not found in {} (no source_path to import from)",
                            thumbprint, store
                        ));
                    }
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                // If thumbprint provided with source, check if already present.
                if !thumbprint.is_empty() && Self::cert_exists(&store, &thumbprint) {
                    result.action = Action::None;
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                // Import the certificate.
                result.action = Action::Created;
                result.changed = true;

                if dry_run {
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                let script = if Self::is_pfx(&source) {
                    let mut cmd = format!(
                        "Import-PfxCertificate -FilePath '{}' -CertStoreLocation '{}'",
                        source.replace('\'', "''"),
                        store.replace('\'', "''"),
                    );
                    if !password.is_empty() {
                        cmd.push_str(&format!(
                            " -Password (ConvertTo-SecureString '{}' -AsPlainText -Force)",
                            password.replace('\'', "''"),
                        ));
                    }
                    if exportable {
                        cmd.push_str(" -Exportable");
                    }
                    cmd.push_str(" | Select-Object -ExpandProperty Thumbprint");
                    cmd
                } else {
                    format!(
                        "Import-Certificate -FilePath '{}' -CertStoreLocation '{}' | Select-Object -ExpandProperty Thumbprint",
                        source.replace('\'', "''"),
                        store.replace('\'', "''"),
                    )
                };

                match Self::ps(&script) {
                    Ok(_) => {}
                    Err(e) => {
                        result.error =
                            Some(format!("certificate_windows import {}: {}", source, e));
                    }
                }
            }
            "absent" => {
                if thumbprint.is_empty() {
                    result.error = Some(
                        "certificate_windows executor: thumbprint is required for state=absent"
                            .to_string(),
                    );
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                if !Self::cert_exists(&store, &thumbprint) {
                    result.action = Action::None;
                } else {
                    result.action = Action::Deleted;
                    result.changed = true;
                    if !dry_run {
                        let script = format!(
                            "Get-ChildItem '{}\\{}' | Remove-Item",
                            store.replace('\'', "''"),
                            thumbprint.replace('\'', "''"),
                        );
                        if let Err(e) = Self::ps(&script) {
                            result.error = Some(format!(
                                "certificate_windows remove {}: {}", thumbprint, e
                            ));
                        }
                    }
                }
            }
            _ => unreachable!(),
        }

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn missing_both_params() {
        let exec = CertificateWindowsExecutor::new();
        let params = HashMap::new();
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result
            .error
            .unwrap()
            .contains("either thumbprint or source_path param is required"));
    }

    #[test]
    fn invalid_state() {
        let exec = CertificateWindowsExecutor::new();
        let mut params = HashMap::new();
        params.insert("thumbprint".to_string(), "ABC123".to_string());
        params.insert("state".to_string(), "bogus".to_string());
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("unknown state"));
    }

    #[test]
    fn absent_requires_thumbprint() {
        let exec = CertificateWindowsExecutor::new();
        let mut params = HashMap::new();
        params.insert("source_path".to_string(), "C:\\certs\\app.pfx".to_string());
        params.insert("state".to_string(), "absent".to_string());
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result
            .error
            .unwrap()
            .contains("thumbprint is required for state=absent"));
    }

    #[test]
    fn is_pfx_detection() {
        assert!(CertificateWindowsExecutor::is_pfx("cert.pfx"));
        assert!(CertificateWindowsExecutor::is_pfx("cert.PFX"));
        assert!(CertificateWindowsExecutor::is_pfx("cert.p12"));
        assert!(!CertificateWindowsExecutor::is_pfx("cert.cer"));
        assert!(!CertificateWindowsExecutor::is_pfx("cert.crt"));
    }
}
