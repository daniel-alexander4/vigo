// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0
#![cfg(windows)]

//! Local Security Policy executor — manages local security policies idempotently
//! via `secedit`.
//!
//! Parameters:
//! - `policy_name` (required): Policy name. Supported categories:
//!   - Password policies: "MinimumPasswordLength", "MaximumPasswordAge",
//!     "MinimumPasswordAge", "PasswordComplexity", "PasswordHistorySize"
//!   - Account lockout: "LockoutBadCount", "LockoutDuration", "ResetLockoutCount"
//!   - Audit policies: "AuditLogonEvents", "AuditObjectAccess", "AuditPrivilegeUse",
//!     "AuditPolicyChange", "AuditAccountManage"
//! - `value` (required): Policy value (integer or string depending on policy)
//!
//! Check: `secedit /export /cfg <tempfile>` then parse the INI-format output.
//! Set: Write INI template, `secedit /configure /db <tempdb> /cfg <tempfile> /quiet`.

use super::{Action, Executor, ResourceResult, run_command};
use std::collections::HashMap;
use std::time::Instant;

pub struct LocalSecurityPolicyExecutor;

/// Known policies and the INI section they belong to.
const PASSWORD_POLICIES: &[&str] = &[
    "MinimumPasswordLength",
    "MaximumPasswordAge",
    "MinimumPasswordAge",
    "PasswordComplexity",
    "PasswordHistorySize",
];

const LOCKOUT_POLICIES: &[&str] = &[
    "LockoutBadCount",
    "LockoutDuration",
    "ResetLockoutCount",
];

const AUDIT_POLICIES: &[&str] = &[
    "AuditLogonEvents",
    "AuditObjectAccess",
    "AuditPrivilegeUse",
    "AuditPolicyChange",
    "AuditAccountManage",
];

impl LocalSecurityPolicyExecutor {
    pub fn new() -> Self {
        Self
    }

    /// Determine which INI section a policy belongs to.
    fn section_for(policy: &str) -> Option<&'static str> {
        if PASSWORD_POLICIES.contains(&policy) || LOCKOUT_POLICIES.contains(&policy) {
            Some("[System Access]")
        } else if AUDIT_POLICIES.contains(&policy) {
            Some("[Event Audit]")
        } else {
            None
        }
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

    /// Export current security policy and read a specific value.
    fn get_current_value(policy: &str) -> Option<String> {
        // Export to a temp file, read the value, clean up.
        let script = format!(
            r#"$tmp = [IO.Path]::GetTempFileName()
try {{
    secedit /export /cfg $tmp /quiet 2>$null | Out-Null
    $content = Get-Content $tmp -Raw
    if ($content -match '(?m)^\s*{}\s*=\s*(.+?)\s*$') {{
        $Matches[1].Trim()
    }} else {{
        'NOTFOUND'
    }}
}} finally {{
    Remove-Item $tmp -Force -ErrorAction SilentlyContinue
}}"#,
            policy
        );

        match Self::ps(&script) {
            Ok(s) if s == "NOTFOUND" || s.is_empty() => None,
            Ok(s) => Some(s),
            Err(_) => None,
        }
    }

    /// Apply a security policy value via secedit.
    fn set_value(policy: &str, value: &str, section: &str) -> Result<(), String> {
        // Build the INI template content and apply via secedit.
        let script = format!(
            r#"$cfg = [IO.Path]::GetTempFileName()
$db = [IO.Path]::GetTempFileName()
try {{
    @"
[Unicode]
Unicode=yes
[Version]
signature="$CHICAGO$"
Revision=1
{section}
{policy} = {value}
"@ | Set-Content $cfg -Encoding Unicode
    $out = secedit /configure /db $db /cfg $cfg /quiet 2>&1
    if ($LASTEXITCODE -ne 0) {{
        throw "secedit failed: $out"
    }}
}} finally {{
    Remove-Item $cfg -Force -ErrorAction SilentlyContinue
    Remove-Item $db -Force -ErrorAction SilentlyContinue
}}"#,
            section = section,
            policy = policy,
            value = value,
        );

        Self::ps(&script).map(|_| ())
    }
}

impl Executor for LocalSecurityPolicyExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let policy = params.get("policy_name").cloned().unwrap_or_default();
        let value = params.get("value").cloned().unwrap_or_default();

        let display = format!("{}={}", policy, value);
        let mut result = ResourceResult::new("local_security_policy", &display);

        if policy.is_empty() {
            result.error = Some(
                "local_security_policy executor: policy_name param is required".to_string(),
            );
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        if value.is_empty() {
            result.error = Some(
                "local_security_policy executor: value param is required".to_string(),
            );
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let section = match Self::section_for(&policy) {
            Some(s) => s,
            None => {
                result.error = Some(format!(
                    "local_security_policy executor: unknown policy {:?}. Supported: {:?}, {:?}, {:?}",
                    policy,
                    PASSWORD_POLICIES,
                    LOCKOUT_POLICIES,
                    AUDIT_POLICIES,
                ));
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
        };

        // Check current value.
        let current = Self::get_current_value(&policy);
        if current.as_deref() == Some(&value) {
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

        if let Err(e) = Self::set_value(&policy, &value, section) {
            result.error = Some(format!(
                "local_security_policy set {}={}: {}", policy, value, e
            ));
        }

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn missing_policy_name_param() {
        let exec = LocalSecurityPolicyExecutor::new();
        let params = HashMap::new();
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("policy_name param is required"));
    }

    #[test]
    fn missing_value_param() {
        let exec = LocalSecurityPolicyExecutor::new();
        let mut params = HashMap::new();
        params.insert("policy_name".to_string(), "MinimumPasswordLength".to_string());
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("value param is required"));
    }

    #[test]
    fn unknown_policy() {
        let exec = LocalSecurityPolicyExecutor::new();
        let mut params = HashMap::new();
        params.insert("policy_name".to_string(), "BogusPolicy".to_string());
        params.insert("value".to_string(), "42".to_string());
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("unknown policy"));
    }

    #[test]
    fn section_mapping() {
        assert_eq!(
            LocalSecurityPolicyExecutor::section_for("MinimumPasswordLength"),
            Some("[System Access]")
        );
        assert_eq!(
            LocalSecurityPolicyExecutor::section_for("LockoutBadCount"),
            Some("[System Access]")
        );
        assert_eq!(
            LocalSecurityPolicyExecutor::section_for("AuditLogonEvents"),
            Some("[Event Audit]")
        );
        assert_eq!(
            LocalSecurityPolicyExecutor::section_for("NotARealPolicy"),
            None
        );
    }
}
