// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Host executor — manages /etc/hosts entries idempotently.

use super::{Action, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::Instant;

pub struct HostExecutor {
    pub hosts_file: String,
}

impl HostExecutor {
    pub fn new() -> Self {
        Self {
            hosts_file: "/etc/hosts".into(),
        }
    }
}

impl Executor for HostExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let name = params
            .get("hostname")
            .cloned()
            .unwrap_or_default();

        let mut result = ResourceResult::new("host", &name);

        if name.is_empty() {
            result.error = Some("host executor: hostname param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");
        let ip = params.get("ip").cloned().unwrap_or_default();
        let aliases = params.get("aliases").cloned().unwrap_or_default();

        let content = match std::fs::read_to_string(&self.hosts_file) {
            Ok(c) => c,
            Err(e) => {
                result.error = Some(format!("read {}: {}", self.hosts_file, e));
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
        };

        match state {
            "present" => {
                if ip.is_empty() {
                    result.error = Some("host executor: ip param is required for state=present".into());
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                let desired_line = if aliases.is_empty() {
                    format!("{}\t{}", ip, name)
                } else {
                    format!("{}\t{} {}", ip, name, aliases)
                };

                // Filter out existing lines for this hostname.
                let mut lines: Vec<String> = Vec::new();
                let mut had_entry = false;
                for line in content.lines() {
                    let fields: Vec<&str> = line.split_whitespace().collect();
                    if fields.len() >= 2 && fields[1..].iter().any(|f| *f == name) {
                        had_entry = true;
                        // Check if the existing line matches.
                        let normalized: String = fields.join(" ");
                        let desired_norm: String = desired_line.split_whitespace().collect::<Vec<_>>().join(" ");
                        if normalized == desired_norm {
                            // Already correct — put it back.
                            lines.push(line.to_string());
                        }
                        // Otherwise skip (will append new line below).
                    } else {
                        lines.push(line.to_string());
                    }
                }

                // Check if the desired entry already exists.
                let desired_norm: String = desired_line.split_whitespace().collect::<Vec<_>>().join(" ");
                let exists = lines.iter().any(|l| {
                    let norm: String = l.split_whitespace().collect::<Vec<_>>().join(" ");
                    norm == desired_norm
                });

                if exists && had_entry {
                    result.action = Action::None;
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                if !exists {
                    lines.push(desired_line);
                }

                result.action = if had_entry {
                    Action::Updated
                } else {
                    Action::Created
                };
                result.changed = true;

                if !dry_run {
                    let mut output = lines.join("\n");
                    if !output.ends_with('\n') {
                        output.push('\n');
                    }
                    if let Err(e) = std::fs::write(&self.hosts_file, output) {
                        result.error = Some(format!("write {}: {}", self.hosts_file, e));
                    }
                }
            }
            "absent" => {
                let filtered: Vec<&str> = content
                    .lines()
                    .filter(|line| {
                        let fields: Vec<&str> = line.split_whitespace().collect();
                        !(fields.len() >= 2 && fields[1..].iter().any(|f| *f == name))
                    })
                    .collect();

                if filtered.len() == content.lines().count() {
                    result.action = Action::None;
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                result.action = Action::Deleted;
                result.changed = true;

                if !dry_run {
                    let mut output = filtered.join("\n");
                    if !output.ends_with('\n') && !output.is_empty() {
                        output.push('\n');
                    }
                    if let Err(e) = std::fs::write(&self.hosts_file, output) {
                        result.error = Some(format!("write {}: {}", self.hosts_file, e));
                    }
                }
            }
            _ => {
                result.error = Some(format!("host executor: unknown state {:?}", state));
            }
        }

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
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

    fn make_executor(hosts_path: &std::path::Path) -> HostExecutor {
        HostExecutor {
            hosts_file: hosts_path.to_str().unwrap().to_string(),
        }
    }

    #[test]
    fn missing_hostname_errors() {
        let dir = tempfile::TempDir::new().unwrap();
        let hosts_path = dir.path().join("hosts");
        std::fs::write(&hosts_path, "127.0.0.1\tlocalhost\n").unwrap();
        let exec = make_executor(&hosts_path);
        let result = exec.execute(&make_params(&[]), false);
        assert!(result.error.is_some());
        assert!(
            result.error.as_deref().unwrap().contains("hostname param is required"),
            "unexpected error: {:?}",
            result.error
        );
    }

    #[test]
    fn present_missing_ip_errors() {
        let dir = tempfile::TempDir::new().unwrap();
        let hosts_path = dir.path().join("hosts");
        std::fs::write(&hosts_path, "127.0.0.1\tlocalhost\n").unwrap();
        let exec = make_executor(&hosts_path);
        let result = exec.execute(
            &make_params(&[("hostname", "myhost")]),
            false,
        );
        assert!(result.error.is_some());
        assert!(
            result.error.as_deref().unwrap().contains("ip param is required"),
            "unexpected error: {:?}",
            result.error
        );
    }

    #[test]
    fn present_adds_entry() {
        let dir = tempfile::TempDir::new().unwrap();
        let hosts_path = dir.path().join("hosts");
        std::fs::write(&hosts_path, "127.0.0.1\tlocalhost\n").unwrap();
        let exec = make_executor(&hosts_path);
        let result = exec.execute(
            &make_params(&[("hostname", "myhost"), ("ip", "192.168.1.1")]),
            false,
        );
        assert_eq!(result.action, Action::Created);
        assert!(result.changed);
        assert!(result.error.is_none());
        let content = std::fs::read_to_string(&hosts_path).unwrap();
        assert!(
            content.contains("192.168.1.1\tmyhost"),
            "expected entry not found in:\n{}",
            content
        );
        // Original entry preserved.
        assert!(content.contains("127.0.0.1\tlocalhost"));
    }

    #[test]
    fn present_entry_already_exists() {
        let dir = tempfile::TempDir::new().unwrap();
        let hosts_path = dir.path().join("hosts");
        std::fs::write(&hosts_path, "127.0.0.1\tlocalhost\n192.168.1.1\tmyhost\n").unwrap();
        let exec = make_executor(&hosts_path);
        let result = exec.execute(
            &make_params(&[("hostname", "myhost"), ("ip", "192.168.1.1")]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());
    }

    #[test]
    fn present_updates_ip() {
        let dir = tempfile::TempDir::new().unwrap();
        let hosts_path = dir.path().join("hosts");
        std::fs::write(&hosts_path, "127.0.0.1\tlocalhost\n10.0.0.1\tmyhost\n").unwrap();
        let exec = make_executor(&hosts_path);
        let result = exec.execute(
            &make_params(&[("hostname", "myhost"), ("ip", "192.168.1.1")]),
            false,
        );
        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);
        assert!(result.error.is_none());
        let content = std::fs::read_to_string(&hosts_path).unwrap();
        assert!(content.contains("192.168.1.1\tmyhost"));
        assert!(!content.contains("10.0.0.1\tmyhost"));
    }

    #[test]
    fn absent_removes_entry() {
        let dir = tempfile::TempDir::new().unwrap();
        let hosts_path = dir.path().join("hosts");
        std::fs::write(&hosts_path, "127.0.0.1\tlocalhost\n192.168.1.1\tmyhost\n").unwrap();
        let exec = make_executor(&hosts_path);
        let result = exec.execute(
            &make_params(&[("hostname", "myhost"), ("state", "absent")]),
            false,
        );
        assert_eq!(result.action, Action::Deleted);
        assert!(result.changed);
        assert!(result.error.is_none());
        let content = std::fs::read_to_string(&hosts_path).unwrap();
        assert!(!content.contains("myhost"));
        assert!(content.contains("127.0.0.1\tlocalhost"));
    }

    #[test]
    fn absent_entry_not_found() {
        let dir = tempfile::TempDir::new().unwrap();
        let hosts_path = dir.path().join("hosts");
        std::fs::write(&hosts_path, "127.0.0.1\tlocalhost\n").unwrap();
        let exec = make_executor(&hosts_path);
        let result = exec.execute(
            &make_params(&[("hostname", "myhost"), ("state", "absent")]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());
    }

    #[test]
    fn dry_run_does_not_modify_file() {
        let dir = tempfile::TempDir::new().unwrap();
        let hosts_path = dir.path().join("hosts");
        let original = "127.0.0.1\tlocalhost\n";
        std::fs::write(&hosts_path, original).unwrap();
        let exec = make_executor(&hosts_path);
        let result = exec.execute(
            &make_params(&[("hostname", "myhost"), ("ip", "192.168.1.1")]),
            true,
        );
        assert_eq!(result.action, Action::Created);
        assert!(result.changed);
        assert!(result.error.is_none());
        // File should be unchanged.
        let content = std::fs::read_to_string(&hosts_path).unwrap();
        assert_eq!(content, original, "dry_run should not modify the file");
    }
}
