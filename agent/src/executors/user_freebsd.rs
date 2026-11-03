// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! User executor for FreeBSD — manages users via `pw useradd/usermod/userdel`.

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use std::collections::HashMap;
use std::io::{BufRead, BufReader};
use std::time::Instant;

pub struct UserFreebsdExecutor {
    pub runner: Box<dyn CommandRunner>,
}

impl UserFreebsdExecutor {
    pub fn new() -> Self {
        Self { runner: Box::new(DefaultRunner) }
    }

    fn user_exists(&self, name: &str) -> bool {
        self.runner.run("pw", &["usershow", name])
            .map(|o| o.status.success())
            .unwrap_or(false)
    }

    fn get_user_info(&self, name: &str) -> Option<(String, String, String)> {
        // pw usershow returns passwd-format line
        self.runner.run("pw", &["usershow", name])
            .ok()
            .filter(|o| o.status.success())
            .and_then(|o| {
                let stdout = String::from_utf8_lossy(&o.stdout);
                let fields: Vec<&str> = stdout.trim().split(':').collect();
                if fields.len() >= 7 {
                    Some((fields[5].to_string(), fields[6].to_string(), fields[4].to_string()))
                } else {
                    None
                }
            })
    }
}

impl Executor for UserFreebsdExecutor {
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
                    // Check for modifications
                    let mut mod_args: Vec<String> = vec!["usermod".into(), username.clone()];
                    let mut needs_mod = false;

                    if let Some((home, shell, comment)) = self.get_user_info(&username) {
                        if let Some(desired) = params.get("shell") {
                            if shell != *desired {
                                mod_args.extend_from_slice(&["-s".into(), desired.clone()]);
                                needs_mod = true;
                            }
                        }
                        if let Some(desired) = params.get("home") {
                            if home != *desired {
                                mod_args.extend_from_slice(&["-d".into(), desired.clone()]);
                                needs_mod = true;
                            }
                        }
                        if let Some(desired) = params.get("comment") {
                            if comment != *desired {
                                mod_args.extend_from_slice(&["-c".into(), desired.clone()]);
                                needs_mod = true;
                            }
                        }
                    }

                    if needs_mod {
                        result.action = Action::Updated;
                        result.changed = true;
                        if !dry_run {
                            let args_ref: Vec<&str> = mod_args.iter().map(|s| s.as_str()).collect();
                            match self.runner.run("pw", &args_ref) {
                                Ok(o) if !o.status.success() => {
                                    result.error = Some(format!("pw usermod: {}", String::from_utf8_lossy(&o.stderr)));
                                }
                                Err(e) => result.error = Some(format!("pw usermod: {}", e)),
                                _ => {}
                            }
                        }
                    }
                } else {
                    result.action = Action::Created;
                    result.changed = true;
                    if !dry_run {
                        let mut args: Vec<String> = vec!["useradd".into(), username.clone(), "-m".into()];
                        if let Some(v) = params.get("uid") { args.extend_from_slice(&["-u".into(), v.clone()]); }
                        if let Some(v) = params.get("gid") { args.extend_from_slice(&["-g".into(), v.clone()]); }
                        if let Some(v) = params.get("shell") { args.extend_from_slice(&["-s".into(), v.clone()]); }
                        if let Some(v) = params.get("home") { args.extend_from_slice(&["-d".into(), v.clone()]); }
                        if let Some(v) = params.get("comment") { args.extend_from_slice(&["-c".into(), v.clone()]); }

                        let args_ref: Vec<&str> = args.iter().map(|s| s.as_str()).collect();
                        match self.runner.run("pw", &args_ref) {
                            Ok(o) if !o.status.success() => {
                                result.error = Some(format!("pw useradd: {}", String::from_utf8_lossy(&o.stderr)));
                            }
                            Err(e) => result.error = Some(format!("pw useradd: {}", e)),
                            _ => {}
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
                        match self.runner.run("pw", &["userdel", &username]) {
                            Ok(o) if !o.status.success() => {
                                result.error = Some(format!("pw userdel: {}", String::from_utf8_lossy(&o.stderr)));
                            }
                            Err(e) => result.error = Some(format!("pw userdel: {}", e)),
                            _ => {}
                        }
                    }
                }
            }
            other => result.error = Some(format!("unknown user state: {}", other)),
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
        let exec = UserFreebsdExecutor::new();
        let result = exec.execute(&HashMap::new(), false);
        assert!(result.error.is_some());
    }
}
