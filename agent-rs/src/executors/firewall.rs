// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Firewall executor — manages UFW rules idempotently.

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::Instant;

pub struct FirewallExecutor {
    pub runner: Box<dyn CommandRunner>,
}

impl FirewallExecutor {
    pub fn new() -> Self {
        Self {
            runner: Box::new(DefaultRunner),
        }
    }

    fn rule_exists(&self, port: &str, proto: &str) -> bool {
        let output = match self.runner.run("ufw", &["status", "verbose"]) {
            Ok(o) if o.status.success() => String::from_utf8_lossy(&o.stdout).to_string(),
            _ => return false,
        };

        let needle = if proto.is_empty() || proto == "any" {
            port.to_string()
        } else {
            format!("{}/{}", port, proto)
        };

        output.lines().any(|line| line.contains(&needle))
    }
}

impl Executor for FirewallExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let port = params
            .get("port")
            .or_else(|| params.get("name"))
            .cloned()
            .unwrap_or_default();

        let mut result = ResourceResult::new("firewall", &port);

        if port.is_empty() {
            result.error = Some("firewall executor: port param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");
        let proto = params.get("proto").map(|s| s.as_str()).unwrap_or("any");
        let action = params.get("action").cloned().unwrap_or_default();
        let from = params.get("from").map(|s| s.as_str()).unwrap_or("any");
        let direction = params.get("direction").map(|s| s.as_str()).unwrap_or("in");
        let comment = params.get("comment").cloned().unwrap_or_default();

        let proto_str = if proto == "any" { "" } else { proto };

        match state {
            "present" => {
                if action.is_empty() {
                    result.error = Some("firewall executor: action param is required for state=present".into());
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                if self.rule_exists(&port, proto_str) {
                    result.action = Action::None;
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                result.action = Action::Created;
                result.changed = true;

                if dry_run {
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                let mut args = vec!["--force".to_string(), action.clone(), direction.to_string()];
                if from != "any" {
                    args.extend(["from".into(), from.into()]);
                }
                args.extend(["to".into(), "any".into(), "port".into(), port.clone()]);
                if !proto_str.is_empty() {
                    args.extend(["proto".into(), proto_str.into()]);
                }
                if !comment.is_empty() {
                    args.extend(["comment".into(), comment.clone()]);
                }

                let arg_refs: Vec<&str> = args.iter().map(|s| s.as_str()).collect();
                match self.runner.run("ufw", &arg_refs) {
                    Ok(o) if !o.status.success() => {
                        let stderr = String::from_utf8_lossy(&o.stderr);
                        result.error = Some(format!("ufw: {}", stderr));
                    }
                    Err(e) => result.error = Some(format!("ufw: {}", e)),
                    _ => {}
                }
            }
            "absent" => {
                if !self.rule_exists(&port, proto_str) {
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

                let mut args = vec!["--force".to_string(), "delete".into(), action.clone(), direction.to_string()];
                args.extend(["to".into(), "any".into(), "port".into(), port.clone()]);
                if !proto_str.is_empty() {
                    args.extend(["proto".into(), proto_str.into()]);
                }

                let arg_refs: Vec<&str> = args.iter().map(|s| s.as_str()).collect();
                match self.runner.run("ufw", &arg_refs) {
                    Ok(o) if !o.status.success() => {
                        let stderr = String::from_utf8_lossy(&o.stderr);
                        result.error = Some(format!("ufw delete: {}", stderr));
                    }
                    Err(e) => result.error = Some(format!("ufw delete: {}", e)),
                    _ => {}
                }
            }
            _ => {
                result.error = Some(format!("firewall executor: unknown state {:?}", state));
            }
        }

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}
