// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0

//! User executor for macOS — manages users via `dscl`.

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::Instant;

pub struct UserMacosExecutor {
    pub runner: Box<dyn CommandRunner>,
}

impl UserMacosExecutor {
    pub fn new() -> Self {
        Self { runner: Box::new(DefaultRunner) }
    }

    fn user_exists(&self, name: &str) -> bool {
        self.runner.run("dscl", &[".", "-read", &format!("/Users/{}", name)])
            .map(|o| o.status.success())
            .unwrap_or(false)
    }

    fn get_user_attr(&self, name: &str, attr: &str) -> Option<String> {
        self.runner.run("dscl", &[".", "-read", &format!("/Users/{}", name), attr])
            .ok()
            .filter(|o| o.status.success())
            .map(|o| {
                let stdout = String::from_utf8_lossy(&o.stdout);
                // Output format: "AttrName: value"
                stdout.lines()
                    .find_map(|line| line.split_once(": ").map(|(_, v)| v.trim().to_string()))
                    .unwrap_or_default()
            })
    }

    fn next_uid(&self) -> String {
        // Find max UID >= 500 and add 1
        let output = self.runner.run("dscl", &[".", "-list", "/Users", "UniqueID"]);
        let max = output.ok()
            .map(|o| {
                let stdout = String::from_utf8_lossy(&o.stdout);
                stdout.lines()
                    .filter_map(|line| line.split_whitespace().last()?.parse::<u32>().ok())
                    .filter(|&uid| uid >= 500)
                    .max()
                    .unwrap_or(500)
            })
            .unwrap_or(500);
        (max + 1).to_string()
    }
}

impl Executor for UserMacosExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let username = params.get("username").cloned().unwrap_or_default();
        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");

        let mut result = ResourceResult::new("user", &username);

        if username.is_empty() {
            result.error = Some("user executor: username param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let exists = self.user_exists(&username);

        match state {
            "present" => {
                if exists {
                    // Check for attribute changes (shell, home, etc.)
                    let mut changed = false;
                    if let Some(desired_shell) = params.get("shell") {
                        if self.get_user_attr(&username, "UserShell").as_deref() != Some(desired_shell) {
                            if !dry_run {
                                if let Err(e) = self.runner.run("dscl", &[".", "-create", &format!("/Users/{}", username), "UserShell", desired_shell]).map_err(|e| e.to_string()).and_then(|o| if o.status.success() { Ok(()) } else { Err(String::from_utf8_lossy(&o.stderr).to_string()) }) {
                                    result.error = Some(format!("set shell: {}", e));
                                    result.duration_ms = start.elapsed().as_millis() as i32;
                                    return result;
                                }
                            }
                            changed = true;
                        }
                    }
                    if let Some(desired_home) = params.get("home") {
                        if self.get_user_attr(&username, "NFSHomeDirectory").as_deref() != Some(desired_home) {
                            if !dry_run {
                                if let Err(e) = self.runner.run("dscl", &[".", "-create", &format!("/Users/{}", username), "NFSHomeDirectory", desired_home]).map_err(|e| e.to_string()).and_then(|o| if o.status.success() { Ok(()) } else { Err(String::from_utf8_lossy(&o.stderr).to_string()) }) {
                                    result.error = Some(format!("set home: {}", e));
                                    result.duration_ms = start.elapsed().as_millis() as i32;
                                    return result;
                                }
                            }
                            changed = true;
                        }
                    }
                    if changed {
                        result.action = Action::Updated;
                        result.changed = true;
                    }
                } else {
                    result.action = Action::Created;
                    result.changed = true;
                    if !dry_run {
                        let uid = params.get("uid").cloned().unwrap_or_else(|| self.next_uid());
                        let gid = params.get("gid").cloned().unwrap_or_else(|| "20".to_string()); // staff group
                        let shell = params.get("shell").map(|s| s.as_str()).unwrap_or("/bin/zsh");
                        let home = params.get("home").cloned().unwrap_or_else(|| format!("/Users/{}", username));
                        let comment = params.get("comment").map(|s| s.as_str()).unwrap_or(&username);

                        let user_path = format!("/Users/{}", username);
                        let cmds: Vec<(&[&str], &str)> = vec![
                            (&[".", "-create", &user_path], "create user"),
                            (&[".", "-create", &user_path, "UniqueID", &uid], "set uid"),
                            (&[".", "-create", &user_path, "PrimaryGroupID", &gid], "set gid"),
                            (&[".", "-create", &user_path, "UserShell", shell], "set shell"),
                            (&[".", "-create", &user_path, "NFSHomeDirectory", &home], "set home"),
                            (&[".", "-create", &user_path, "RealName", comment], "set comment"),
                        ];

                        for (args, desc) in cmds {
                            match self.runner.run("dscl", args) {
                                Ok(o) if o.status.success() => {}
                                Ok(o) => {
                                    let stderr = String::from_utf8_lossy(&o.stderr);
                                    result.error = Some(format!("dscl {}: {}", desc, stderr));
                                    break;
                                }
                                Err(e) => {
                                    result.error = Some(format!("dscl {}: {}", desc, e));
                                    break;
                                }
                            }
                        }

                        // Create home directory
                        if result.error.is_none() {
                            let _ = self.runner.run("createhomedir", &["-c", "-u", &username]);
                        }
                    }
                }
            }
            "absent" => {
                if !exists {
                    result.action = Action::None;
                } else {
                    result.action = Action::Deleted;
                    result.changed = true;
                    if !dry_run {
                        match self.runner.run("dscl", &[".", "-delete", &format!("/Users/{}", username)]) {
                            Ok(o) if o.status.success() => {}
                            Ok(o) => {
                                let stderr = String::from_utf8_lossy(&o.stderr);
                                result.error = Some(format!("dscl delete: {}", stderr));
                            }
                            Err(e) => {
                                result.error = Some(format!("dscl delete: {}", e));
                            }
                        }
                    }
                }
            }
            other => {
                result.error = Some(format!("unknown user state: {}", other));
            }
        }

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn missing_username_errors() {
        let exec = UserMacosExecutor::new();
        let result = exec.execute(&HashMap::new(), false);
        assert!(result.error.is_some());
    }
}
