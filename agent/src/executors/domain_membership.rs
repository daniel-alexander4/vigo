// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0
#![cfg(windows)]

//! Active Directory domain membership executor — manages domain join/leave
//! idempotently via PowerShell.
//!
//! Parameters:
//! - `domain` (required): Domain name (e.g., "example.com", "ad.corp.local")
//! - `state`: "present" (default) or "absent"
//! - `username`: Domain admin username for join (required for present).
//!   Can be "user@domain" or "DOMAIN\\user".
//! - `password`: Domain admin password (required for present)
//! - `ou_path`: Organizational Unit path (optional, e.g., "OU=Servers,DC=example,DC=com")
//! - `restart_after_join`: "true"/"false" (default "true") — restart after join/leave

use super::{Action, Executor, ResourceResult, run_command};
use std::collections::HashMap;
use std::time::Instant;

pub struct DomainMembershipExecutor;

impl DomainMembershipExecutor {
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

    /// Check current domain membership. Returns (is_domain_joined, current_domain).
    fn check_domain() -> (bool, String) {
        let script = "$cs = Get-CimInstance Win32_ComputerSystem; if ($cs.PartOfDomain) { $cs.Domain } else { 'WORKGROUP' }";
        match Self::ps(script) {
            Ok(s) if s == "WORKGROUP" || s.is_empty() => (false, String::new()),
            Ok(s) => (true, s.to_lowercase()),
            Err(_) => (false, String::new()),
        }
    }
}

impl Executor for DomainMembershipExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let domain = params.get("domain").cloned().unwrap_or_default();
        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");
        let username = params.get("username").cloned().unwrap_or_default();
        let password = params.get("password").cloned().unwrap_or_default();
        let ou_path = params.get("ou_path").cloned().unwrap_or_default();
        let restart = params.get("restart_after_join").map(|s| s.as_str()).unwrap_or("true") == "true";

        let mut result = ResourceResult::new("domain_membership", &domain);

        if domain.is_empty() {
            result.error = Some(
                "domain_membership executor: domain param is required".to_string(),
            );
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        if !matches!(state, "present" | "absent") {
            result.error = Some(format!(
                "domain_membership executor: unknown state {:?} (must be present or absent)",
                state
            ));
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        if state == "present" && (username.is_empty() || password.is_empty()) {
            result.error = Some(
                "domain_membership executor: username and password are required for state=present"
                    .to_string(),
            );
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let (is_joined, current_domain) = Self::check_domain();
        let desired_domain = domain.to_lowercase();

        match state {
            "present" => {
                if is_joined && current_domain == desired_domain {
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

                // Build the join command.
                let cred = format!(
                    "$cred = New-Object System.Management.Automation.PSCredential('{}', (ConvertTo-SecureString '{}' -AsPlainText -Force))",
                    username.replace('\'', "''"),
                    password.replace('\'', "''"),
                );
                let mut join = format!(
                    "Add-Computer -DomainName '{}' -Credential $cred -Force",
                    domain.replace('\'', "''"),
                );
                if !ou_path.is_empty() {
                    join.push_str(&format!(
                        " -OUPath '{}'",
                        ou_path.replace('\'', "''"),
                    ));
                }

                let mut full_script = format!("{}; {}", cred, join);
                if restart {
                    full_script.push_str("; Restart-Computer -Force");
                }

                if let Err(e) = Self::ps(&full_script) {
                    result.error = Some(format!("domain_membership join {}: {}", domain, e));
                }
            }
            "absent" => {
                if !is_joined {
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

                let mut leave_script = String::new();
                if !username.is_empty() && !password.is_empty() {
                    let cred = format!(
                        "$cred = New-Object System.Management.Automation.PSCredential('{}', (ConvertTo-SecureString '{}' -AsPlainText -Force))",
                        username.replace('\'', "''"),
                        password.replace('\'', "''"),
                    );
                    leave_script.push_str(&format!(
                        "{}; Remove-Computer -UnjoinDomainCredential $cred -Force",
                        cred,
                    ));
                } else {
                    leave_script.push_str("Remove-Computer -Force");
                }

                if restart {
                    leave_script.push_str("; Restart-Computer -Force");
                }

                if let Err(e) = Self::ps(&leave_script) {
                    result.error = Some(format!(
                        "domain_membership leave {}: {}", current_domain, e
                    ));
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
    fn missing_domain_param() {
        let exec = DomainMembershipExecutor::new();
        let params = HashMap::new();
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("domain param is required"));
    }

    #[test]
    fn invalid_state() {
        let exec = DomainMembershipExecutor::new();
        let mut params = HashMap::new();
        params.insert("domain".to_string(), "example.com".to_string());
        params.insert("state".to_string(), "bogus".to_string());
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("unknown state"));
    }

    #[test]
    fn present_requires_credentials() {
        let exec = DomainMembershipExecutor::new();
        let mut params = HashMap::new();
        params.insert("domain".to_string(), "example.com".to_string());
        params.insert("state".to_string(), "present".to_string());
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result
            .error
            .unwrap()
            .contains("username and password are required"));
    }
}
