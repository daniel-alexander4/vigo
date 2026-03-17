// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0

//! SELinux context executor — manages SELinux file contexts idempotently.
//!
//! Params:
//! - `path` (required) — file or directory path
//! - `context` — full SELinux context string (e.g., "system_u:object_r:httpd_sys_content_t:s0")
//! - `selinux_type` — just the type component (shorthand; uses semanage fcontext + restorecon)
//! - `state` — "present" (default) or "absent" (removes fcontext rules)

use super::{Action, Executor, ResourceResult};
use std::collections::HashMap;
use std::process::Command;
use std::time::Instant;

pub struct SelinuxExecutor;

impl SelinuxExecutor {
    pub fn new() -> Self {
        Self
    }
}

impl Executor for SelinuxExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let path = params.get("path").map(|s| s.as_str()).unwrap_or("");
        let mut result = ResourceResult::new("selinux", path);

        if path.is_empty() {
            result.error = Some("selinux executor: path param is required".to_string());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let context = params.get("context").map(|s| s.as_str());
        let se_type = params.get("selinux_type").map(|s| s.as_str());
        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");

        if context.is_none() && se_type.is_none() {
            result.error =
                Some("selinux executor: either context or selinux_type param is required".to_string());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let res = match state {
            "present" => {
                if let Some(ctx) = context {
                    apply_full_context(path, ctx, dry_run, &mut result)
                } else if let Some(t) = se_type {
                    apply_type_context(path, t, dry_run, &mut result)
                } else {
                    Err("selinux executor: either context or selinux_type param is required".to_string())
                }
            }
            "absent" => {
                if let Some(t) = se_type.or(context) {
                    remove_fcontext(path, t, dry_run, &mut result)
                } else {
                    Err("selinux executor: either context or selinux_type param is required for absent state".to_string())
                }
            }
            other => Err(format!(
                "selinux executor: unknown state {:?} (expected present or absent)",
                other
            )),
        };

        result.duration_ms = start.elapsed().as_millis() as i32;
        if let Err(e) = res {
            result.error = Some(e);
        }
        result
    }
}

/// Get the current SELinux context of a file via `ls -Z`.
fn get_current_context(path: &str) -> Result<String, String> {
    let output = Command::new("ls")
        .args(["-Z", "-d", path])
        .output()
        .map_err(|e| format!("ls -Z {}: {}", path, e))?;

    if !output.status.success() {
        return Err(format!(
            "ls -Z {}: {}",
            path,
            String::from_utf8_lossy(&output.stderr).trim()
        ));
    }

    let stdout = String::from_utf8_lossy(&output.stdout);
    // Format: "context path" — take the first field.
    stdout
        .split_whitespace()
        .next()
        .map(|s| s.to_string())
        .ok_or_else(|| format!("ls -Z {}: unexpected empty output", path))
}

/// Apply a full SELinux context via `chcon`.
fn apply_full_context(
    path: &str,
    context: &str,
    dry_run: bool,
    result: &mut ResourceResult,
) -> Result<(), String> {
    let current = get_current_context(path)?;
    if current == context {
        result.action = Action::None;
        result.changed = false;
        return Ok(());
    }

    result.action = Action::Updated;
    result.changed = true;

    if dry_run {
        return Ok(());
    }

    let output = Command::new("chcon")
        .args([context, path])
        .output()
        .map_err(|e| format!("chcon {} {}: {}", context, path, e))?;

    if !output.status.success() {
        return Err(format!(
            "chcon {} {}: {}",
            context,
            path,
            String::from_utf8_lossy(&output.stderr).trim()
        ));
    }

    Ok(())
}

/// Apply a persistent type context via `semanage fcontext` + `restorecon`.
fn apply_type_context(
    path: &str,
    se_type: &str,
    dry_run: bool,
    result: &mut ResourceResult,
) -> Result<(), String> {
    // Check current context for the type component.
    let current = get_current_context(path)?;
    // SELinux context format: user:role:type:level
    let current_type = current.split(':').nth(2).unwrap_or("");
    if current_type == se_type {
        result.action = Action::None;
        result.changed = false;
        return Ok(());
    }

    result.action = Action::Updated;
    result.changed = true;

    if dry_run {
        return Ok(());
    }

    // Add or modify the fcontext rule.
    let add_output = Command::new("semanage")
        .args(["fcontext", "-a", "-t", se_type, path])
        .output()
        .map_err(|e| format!("semanage fcontext -a -t {} {}: {}", se_type, path, e))?;

    if !add_output.status.success() {
        // If it already exists, try modify instead.
        let mod_output = Command::new("semanage")
            .args(["fcontext", "-m", "-t", se_type, path])
            .output()
            .map_err(|e| format!("semanage fcontext -m -t {} {}: {}", se_type, path, e))?;

        if !mod_output.status.success() {
            return Err(format!(
                "semanage fcontext -t {} {}: {}",
                se_type,
                path,
                String::from_utf8_lossy(&mod_output.stderr).trim()
            ));
        }
    }

    // Apply the context.
    let restore_output = Command::new("restorecon")
        .args(["-v", path])
        .output()
        .map_err(|e| format!("restorecon -v {}: {}", path, e))?;

    if !restore_output.status.success() {
        return Err(format!(
            "restorecon -v {}: {}",
            path,
            String::from_utf8_lossy(&restore_output.stderr).trim()
        ));
    }

    Ok(())
}

/// Remove a persistent fcontext rule via `semanage fcontext -d`.
fn remove_fcontext(
    path: &str,
    _context_or_type: &str,
    dry_run: bool,
    result: &mut ResourceResult,
) -> Result<(), String> {
    // Check if a custom fcontext rule exists for this path.
    let output = Command::new("semanage")
        .args(["fcontext", "-l"])
        .output()
        .map_err(|e| format!("semanage fcontext -l: {}", e))?;

    if !output.status.success() {
        return Err(format!(
            "semanage fcontext -l: {}",
            String::from_utf8_lossy(&output.stderr).trim()
        ));
    }

    let stdout = String::from_utf8_lossy(&output.stdout);
    let has_rule = stdout.lines().any(|line| line.starts_with(path));

    if !has_rule {
        result.action = Action::None;
        result.changed = false;
        return Ok(());
    }

    result.action = Action::Deleted;
    result.changed = true;

    if dry_run {
        return Ok(());
    }

    let del_output = Command::new("semanage")
        .args(["fcontext", "-d", path])
        .output()
        .map_err(|e| format!("semanage fcontext -d {}: {}", path, e))?;

    if !del_output.status.success() {
        return Err(format!(
            "semanage fcontext -d {}: {}",
            path,
            String::from_utf8_lossy(&del_output.stderr).trim()
        ));
    }

    // Restore default context after removing the rule.
    let _ = Command::new("restorecon").args(["-v", path]).output();

    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn make_params(pairs: &[(&str, &str)]) -> HashMap<String, String> {
        pairs
            .iter()
            .map(|(k, v)| (k.to_string(), v.to_string()))
            .collect()
    }

    #[test]
    fn missing_path_errors() {
        let exec = SelinuxExecutor::new();
        let result = exec.execute(
            &make_params(&[("context", "system_u:object_r:httpd_sys_content_t:s0")]),
            false,
        );
        assert!(result.error.is_some());
        assert!(result.error.as_ref().unwrap().contains("path"));
    }

    #[test]
    fn missing_context_and_type_errors() {
        let exec = SelinuxExecutor::new();
        let result = exec.execute(&make_params(&[("path", "/var/www")]), false);
        assert!(result.error.is_some());
        assert!(
            result.error.as_ref().unwrap().contains("context or selinux_type"),
            "got: {:?}",
            result.error
        );
    }
}
