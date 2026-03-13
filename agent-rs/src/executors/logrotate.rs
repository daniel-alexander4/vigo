// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Logrotate executor — manages logrotate configs in /etc/logrotate.d/.

use super::{Action, Executor, ResourceResult};
use sha2::{Digest, Sha256};
use std::collections::HashMap;
use std::time::Instant;

pub struct LogrotateExecutor;

impl Executor for LogrotateExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let name = params.get("name").cloned().unwrap_or_default();
        let mut result = ResourceResult::new("logrotate", &name);

        if name.is_empty() {
            result.error = Some("logrotate executor: name param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let conf_path = format!("/etc/logrotate.d/{}", name);
        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");

        if state == "absent" {
            if !std::path::Path::new(&conf_path).exists() {
                result.action = Action::None;
            } else {
                result.action = Action::Deleted;
                result.changed = true;
                if !dry_run {
                    if let Err(e) = std::fs::remove_file(&conf_path) {
                        result.error = Some(format!("remove {}: {}", conf_path, e));
                    }
                }
            }
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let path = params.get("path").cloned().unwrap_or_default();
        if path.is_empty() {
            result.error = Some("logrotate executor: path param is required for state=present".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let content = build_logrotate_config(params, &path);
        let desired_hash = sha256_hex(content.as_bytes());

        let existing_matches = std::fs::read(&conf_path)
            .map(|data| sha256_hex(&data) == desired_hash)
            .unwrap_or(false);

        if existing_matches {
            result.action = Action::None;
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        result.action = if std::path::Path::new(&conf_path).exists() {
            Action::Updated
        } else {
            Action::Created
        };
        result.changed = true;

        if !dry_run {
            if let Err(e) = std::fs::write(&conf_path, &content) {
                result.error = Some(format!("write {}: {}", conf_path, e));
            }
        }

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}

fn build_logrotate_config(params: &HashMap<String, String>, path: &str) -> String {
    let frequency = params.get("frequency").map(|s| s.as_str()).unwrap_or("weekly");
    let rotate = params.get("rotate").map(|s| s.as_str()).unwrap_or("4");
    let compress = params.get("compress").map(|s| s == "true").unwrap_or(true);
    let delaycompress = params.get("delaycompress").map(|s| s == "true").unwrap_or(true);
    let missingok = params.get("missingok").map(|s| s == "true").unwrap_or(true);
    let notifempty = params.get("notifempty").map(|s| s == "true").unwrap_or(true);
    let copytruncate = params.get("copytruncate").map(|s| s == "true").unwrap_or(false);

    let mut lines = vec![format!("{} {{", path)];
    lines.push(format!("    {}", frequency));
    lines.push(format!("    rotate {}", rotate));

    if let Some(size) = params.get("size") {
        if !size.is_empty() {
            lines.push(format!("    size {}", size));
        }
    }
    if let Some(maxage) = params.get("maxage") {
        if !maxage.is_empty() {
            lines.push(format!("    maxage {}", maxage));
        }
    }

    if compress {
        lines.push("    compress".into());
    }
    if delaycompress {
        lines.push("    delaycompress".into());
    }
    if missingok {
        lines.push("    missingok".into());
    }
    if notifempty {
        lines.push("    notifempty".into());
    }
    if copytruncate {
        lines.push("    copytruncate".into());
    }

    if let Some(create) = params.get("create") {
        if !create.is_empty() {
            lines.push(format!("    create {}", create));
        }
    }

    if let Some(postrotate) = params.get("postrotate") {
        if !postrotate.is_empty() {
            lines.push("    postrotate".into());
            lines.push(format!("        {}", postrotate));
            lines.push("    endscript".into());
        }
    }

    lines.push("}".into());
    lines.push(String::new());
    lines.join("\n")
}

fn sha256_hex(data: &[u8]) -> String {
    let hash = Sha256::digest(data);
    hash.iter().map(|b| format!("{:02x}", b)).collect()
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
    fn missing_name_errors() {
        let exec = LogrotateExecutor;
        let result = exec.execute(&make_params(&[]), false);
        assert!(result.error.is_some());
        assert!(
            result.error.as_deref().unwrap().contains("name param is required"),
            "unexpected error: {:?}",
            result.error
        );
    }

    #[test]
    fn missing_path_errors() {
        let exec = LogrotateExecutor;
        let result = exec.execute(
            &make_params(&[("name", "myapp")]),
            false,
        );
        assert!(result.error.is_some());
        assert!(
            result.error.as_deref().unwrap().contains("path param is required"),
            "unexpected error: {:?}",
            result.error
        );
    }

    #[test]
    fn build_config_defaults() {
        let params = make_params(&[("name", "myapp"), ("path", "/var/log/myapp/*.log")]);
        let config = build_logrotate_config(&params, "/var/log/myapp/*.log");
        assert!(config.contains("/var/log/myapp/*.log {"), "missing path block:\n{}", config);
        assert!(config.contains("weekly"), "missing weekly:\n{}", config);
        assert!(config.contains("rotate 4"), "missing rotate 4:\n{}", config);
        assert!(config.contains("compress"), "missing compress:\n{}", config);
        assert!(config.contains("delaycompress"), "missing delaycompress:\n{}", config);
        assert!(config.contains("missingok"), "missing missingok:\n{}", config);
        assert!(config.contains("notifempty"), "missing notifempty:\n{}", config);
        assert!(!config.contains("copytruncate"), "copytruncate should be off by default:\n{}", config);
    }

    #[test]
    fn build_config_custom() {
        let params = make_params(&[
            ("name", "myapp"),
            ("path", "/var/log/myapp.log"),
            ("frequency", "daily"),
            ("rotate", "7"),
            ("compress", "false"),
            ("delaycompress", "false"),
            ("copytruncate", "true"),
        ]);
        let config = build_logrotate_config(&params, "/var/log/myapp.log");
        assert!(config.contains("daily"), "missing daily:\n{}", config);
        assert!(config.contains("rotate 7"), "missing rotate 7:\n{}", config);
        assert!(!config.contains("    compress"), "compress should be off:\n{}", config);
        assert!(!config.contains("delaycompress"), "delaycompress should be off:\n{}", config);
        assert!(config.contains("copytruncate"), "missing copytruncate:\n{}", config);
    }

    #[test]
    fn build_config_with_postrotate() {
        let params = make_params(&[
            ("name", "nginx"),
            ("path", "/var/log/nginx/*.log"),
            ("postrotate", "/usr/sbin/nginx -s reload"),
        ]);
        let config = build_logrotate_config(&params, "/var/log/nginx/*.log");
        assert!(config.contains("postrotate"), "missing postrotate block:\n{}", config);
        assert!(
            config.contains("/usr/sbin/nginx -s reload"),
            "missing postrotate command:\n{}",
            config
        );
        assert!(config.contains("endscript"), "missing endscript:\n{}", config);
    }

    #[test]
    fn dry_run_absent_noop() {
        let exec = LogrotateExecutor;
        let result = exec.execute(
            &make_params(&[
                ("name", "hobnob-test-nonexistent-logrotate-99999"),
                ("state", "absent"),
            ]),
            true,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());
    }
}
