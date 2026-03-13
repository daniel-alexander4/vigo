// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! SSH key executor — manages ssh_known_hosts entries idempotently.

use super::{Action, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::Instant;

pub struct SshKeyExecutor;

impl Executor for SshKeyExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let hostname = params
            .get("hostname")
            .or_else(|| params.get("name"))
            .cloned()
            .unwrap_or_default();

        let mut result = ResourceResult::new("sshkey", &hostname);

        if hostname.is_empty() {
            result.error = Some("sshkey executor: hostname param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");
        let key_type = params.get("type").cloned().unwrap_or_default();
        let key_data = params.get("key").cloned().unwrap_or_default();
        let aliases = params.get("aliases").cloned().unwrap_or_default();
        let file = params
            .get("file")
            .map(|s| s.as_str())
            .unwrap_or("/etc/ssh/ssh_known_hosts");

        let content = std::fs::read_to_string(file).unwrap_or_default();

        match state {
            "present" => {
                if key_type.is_empty() || key_data.is_empty() {
                    result.error = Some("sshkey executor: type and key params required for state=present".into());
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                let host_field = if aliases.is_empty() {
                    hostname.clone()
                } else {
                    format!("{},{}", hostname, aliases.replace(' ', ","))
                };
                let desired_line = format!("{} {} {}", host_field, key_type, key_data);

                // Check if exact line exists.
                if content.lines().any(|l| l.trim() == desired_line.trim()) {
                    result.action = Action::None;
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                // Remove old entries for this hostname+type, then append.
                let mut lines: Vec<&str> = content
                    .lines()
                    .filter(|line| {
                        let fields: Vec<&str> = line.split_whitespace().collect();
                        if fields.len() >= 2 {
                            let hosts = fields[0];
                            let ltype = fields[1];
                            !(hosts.split(',').any(|h| h == hostname) && ltype == key_type)
                        } else {
                            true
                        }
                    })
                    .collect();

                let had_old = lines.len() < content.lines().count();
                lines.push(&desired_line);

                result.action = if had_old {
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
                    if let Err(e) = std::fs::write(file, output) {
                        result.error = Some(format!("write {}: {}", file, e));
                    }
                }
            }
            "absent" => {
                let filtered: Vec<&str> = content
                    .lines()
                    .filter(|line| {
                        let fields: Vec<&str> = line.split_whitespace().collect();
                        if fields.is_empty() {
                            return true;
                        }
                        !fields[0].split(',').any(|h| h == hostname)
                    })
                    .collect();

                if filtered.len() == content.lines().count() {
                    result.action = Action::None;
                } else {
                    result.action = Action::Deleted;
                    result.changed = true;

                    if !dry_run {
                        let mut output = filtered.join("\n");
                        if !output.ends_with('\n') && !output.is_empty() {
                            output.push('\n');
                        }
                        if let Err(e) = std::fs::write(file, output) {
                            result.error = Some(format!("write {}: {}", file, e));
                        }
                    }
                }
            }
            _ => {
                result.error = Some(format!("sshkey executor: unknown state {:?}", state));
            }
        }

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::TempDir;

    fn make_params(pairs: &[(&str, &str)]) -> HashMap<String, String> {
        pairs
            .iter()
            .map(|(k, v)| (k.to_string(), v.to_string()))
            .collect()
    }

    #[test]
    fn missing_hostname_errors() {
        let exec = SshKeyExecutor;
        let result = exec.execute(&make_params(&[]), false);
        assert!(result.error.is_some());
        assert!(
            result.error.as_deref().unwrap().contains("hostname param is required"),
            "unexpected error: {:?}",
            result.error
        );
    }

    #[test]
    fn add_key_to_empty_file() {
        let dir = TempDir::new().unwrap();
        let file_path = dir.path().join("known_hosts");
        std::fs::write(&file_path, "").unwrap();
        let exec = SshKeyExecutor;
        let result = exec.execute(
            &make_params(&[
                ("hostname", "github.com"),
                ("type", "ssh-ed25519"),
                ("key", "AAAAC3NzaC1lZDI1NTE5AAAAITest"),
                ("file", file_path.to_str().unwrap()),
            ]),
            false,
        );
        assert_eq!(result.action, Action::Created);
        assert!(result.changed);
        assert!(result.error.is_none());
        let content = std::fs::read_to_string(&file_path).unwrap();
        assert!(
            content.contains("github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITest"),
            "expected key not found in:\n{}",
            content
        );
    }

    #[test]
    fn key_already_present() {
        let dir = TempDir::new().unwrap();
        let file_path = dir.path().join("known_hosts");
        std::fs::write(&file_path, "github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITest\n").unwrap();
        let exec = SshKeyExecutor;
        let result = exec.execute(
            &make_params(&[
                ("hostname", "github.com"),
                ("type", "ssh-ed25519"),
                ("key", "AAAAC3NzaC1lZDI1NTE5AAAAITest"),
                ("file", file_path.to_str().unwrap()),
            ]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());
    }

    #[test]
    fn update_existing_key() {
        let dir = TempDir::new().unwrap();
        let file_path = dir.path().join("known_hosts");
        std::fs::write(&file_path, "github.com ssh-ed25519 OLDKEYdata\n").unwrap();
        let exec = SshKeyExecutor;
        let result = exec.execute(
            &make_params(&[
                ("hostname", "github.com"),
                ("type", "ssh-ed25519"),
                ("key", "NEWKEYdata"),
                ("file", file_path.to_str().unwrap()),
            ]),
            false,
        );
        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);
        assert!(result.error.is_none());
        let content = std::fs::read_to_string(&file_path).unwrap();
        assert!(content.contains("github.com ssh-ed25519 NEWKEYdata"));
        assert!(!content.contains("OLDKEYdata"));
    }

    #[test]
    fn add_key_with_aliases() {
        let dir = TempDir::new().unwrap();
        let file_path = dir.path().join("known_hosts");
        std::fs::write(&file_path, "").unwrap();
        let exec = SshKeyExecutor;
        let result = exec.execute(
            &make_params(&[
                ("hostname", "github.com"),
                ("type", "ssh-ed25519"),
                ("key", "AAAAC3NzaC1lZDI1NTE5AAAAITest"),
                ("aliases", "gh.com ghproxy.com"),
                ("file", file_path.to_str().unwrap()),
            ]),
            false,
        );
        assert_eq!(result.action, Action::Created);
        assert!(result.changed);
        assert!(result.error.is_none());
        let content = std::fs::read_to_string(&file_path).unwrap();
        assert!(
            content.contains("github.com,gh.com,ghproxy.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITest"),
            "expected aliased host field not found in:\n{}",
            content
        );
    }

    #[test]
    fn absent_removes_entry() {
        let dir = TempDir::new().unwrap();
        let file_path = dir.path().join("known_hosts");
        std::fs::write(
            &file_path,
            "github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITest\ngitlab.com ssh-rsa AAAAB3Nza\n",
        )
        .unwrap();
        let exec = SshKeyExecutor;
        let result = exec.execute(
            &make_params(&[
                ("hostname", "github.com"),
                ("state", "absent"),
                ("file", file_path.to_str().unwrap()),
            ]),
            false,
        );
        assert_eq!(result.action, Action::Deleted);
        assert!(result.changed);
        assert!(result.error.is_none());
        let content = std::fs::read_to_string(&file_path).unwrap();
        assert!(!content.contains("github.com"));
        assert!(content.contains("gitlab.com"));
    }

    #[test]
    fn absent_noop_when_missing() {
        let dir = TempDir::new().unwrap();
        let file_path = dir.path().join("known_hosts");
        std::fs::write(&file_path, "").unwrap();
        let exec = SshKeyExecutor;
        let result = exec.execute(
            &make_params(&[
                ("hostname", "github.com"),
                ("state", "absent"),
                ("file", file_path.to_str().unwrap()),
            ]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());
    }
}
