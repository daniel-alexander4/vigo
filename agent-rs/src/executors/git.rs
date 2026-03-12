// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Git executor — clones and updates git repositories idempotently.

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::Instant;

pub struct GitExecutor {
    pub runner: Box<dyn CommandRunner>,
}

impl GitExecutor {
    pub fn new() -> Self {
        Self {
            runner: Box::new(DefaultRunner),
        }
    }

    fn run_git(&self, dir: &str, args: &[&str]) -> Result<String, String> {
        let mut full_args = vec!["-C", dir];
        full_args.extend_from_slice(args);
        match self.runner.run("git", &full_args) {
            Ok(o) if o.status.success() => {
                Ok(String::from_utf8_lossy(&o.stdout).trim().to_string())
            }
            Ok(o) => {
                let stderr = String::from_utf8_lossy(&o.stderr);
                Err(format!("git {}: {}", args.join(" "), stderr))
            }
            Err(e) => Err(format!("git {}: {}", args.join(" "), e)),
        }
    }
}

impl Executor for GitExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let path = params
            .get("path")
            .or_else(|| params.get("name"))
            .cloned()
            .unwrap_or_default();

        let mut result = ResourceResult::new("git", &path);

        if path.is_empty() {
            result.error = Some("git executor: path param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");
        let repo = params.get("repo").cloned().unwrap_or_default();
        let branch = params.get("branch").cloned().unwrap_or_default();
        let depth = params.get("depth").cloned().unwrap_or_default();
        let force = params.get("force").map(|s| s == "true").unwrap_or(false);

        let git_dir = format!("{}/.git", path);
        let exists = std::path::Path::new(&git_dir).is_dir();

        match state {
            "present" => {
                if repo.is_empty() {
                    result.error = Some("git executor: repo param is required for state=present".into());
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                if !exists {
                    // Clone.
                    result.action = Action::Created;
                    result.changed = true;

                    if dry_run {
                        result.duration_ms = start.elapsed().as_millis() as i32;
                        return result;
                    }

                    let mut args: Vec<&str> = vec!["clone"];
                    if !depth.is_empty() {
                        args.extend(["--depth", depth.as_str()]);
                    }
                    if !branch.is_empty() {
                        args.extend(["--branch", branch.as_str()]);
                    }
                    args.push(repo.as_str());
                    args.push(path.as_str());

                    match self.runner.run("git", &args) {
                        Ok(o) if !o.status.success() => {
                            let stderr = String::from_utf8_lossy(&o.stderr);
                            result.error = Some(format!("git clone: {}", stderr));
                        }
                        Err(e) => result.error = Some(format!("git clone: {}", e)),
                        _ => {}
                    }
                } else {
                    // Update existing repo.
                    let mut changed = false;

                    // Check branch.
                    if !branch.is_empty() {
                        let current = self.run_git(&path, &["rev-parse", "--abbrev-ref", "HEAD"]).unwrap_or_default();
                        if current != branch {
                            changed = true;
                            if !dry_run {
                                let _ = self.run_git(&path, &["fetch", "--all"]);
                                let mut checkout_args = vec!["checkout"];
                                if force {
                                    checkout_args.push("--force");
                                }
                                checkout_args.push(&branch);
                                if let Err(e) = self.run_git(&path, &checkout_args) {
                                    result.error = Some(e);
                                    result.duration_ms = start.elapsed().as_millis() as i32;
                                    return result;
                                }
                            }
                        }
                    }

                    // Check for new commits.
                    if !changed {
                        let _ = self.run_git(&path, &["fetch"]);
                        let local = self.run_git(&path, &["rev-parse", "HEAD"]).unwrap_or_default();
                        let remote = self.run_git(&path, &["rev-parse", "@{u}"]).unwrap_or_default();

                        if !remote.is_empty() && local != remote {
                            changed = true;
                            if !dry_run {
                                if force {
                                    if let Err(e) = self.run_git(&path, &["reset", "--hard", "@{u}"]) {
                                        result.error = Some(e);
                                        result.duration_ms = start.elapsed().as_millis() as i32;
                                        return result;
                                    }
                                } else if let Err(e) = self.run_git(&path, &["pull"]) {
                                    result.error = Some(e);
                                    result.duration_ms = start.elapsed().as_millis() as i32;
                                    return result;
                                }
                            }
                        }
                    }

                    if changed {
                        result.action = Action::Updated;
                        result.changed = true;
                    } else {
                        result.action = Action::None;
                    }
                }
            }
            "absent" => {
                if !std::path::Path::new(&path).exists() {
                    result.action = Action::None;
                } else {
                    result.action = Action::Deleted;
                    result.changed = true;
                    if !dry_run {
                        if let Err(e) = std::fs::remove_dir_all(&path) {
                            result.error = Some(format!("remove {}: {}", path, e));
                        }
                    }
                }
            }
            _ => {
                result.error = Some(format!("git executor: unknown state {:?}", state));
            }
        }

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}
