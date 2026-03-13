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

#[cfg(test)]
mod tests {
    use super::*;
    use std::os::unix::process::ExitStatusExt;
    use std::process::Output;
    use std::sync::{Arc, Mutex};

    struct MockRunner {
        responses: Mutex<Vec<std::io::Result<Output>>>,
        calls: Arc<Mutex<Vec<(String, Vec<String>)>>>,
    }

    impl MockRunner {
        fn new(responses: Vec<Output>) -> Self {
            Self {
                responses: Mutex::new(responses.into_iter().map(Ok).collect()),
                calls: Arc::new(Mutex::new(Vec::new())),
            }
        }

        fn ok_output(stdout: &str) -> Output {
            Output {
                status: std::process::ExitStatus::from_raw(0),
                stdout: stdout.as_bytes().to_vec(),
                stderr: Vec::new(),
            }
        }

        fn fail_output(stderr: &str) -> Output {
            Output {
                status: std::process::ExitStatus::from_raw(256),
                stdout: Vec::new(),
                stderr: stderr.as_bytes().to_vec(),
            }
        }
    }

    impl CommandRunner for MockRunner {
        fn run(&self, name: &str, args: &[&str]) -> std::io::Result<Output> {
            self.calls.lock().unwrap().push((
                name.to_string(),
                args.iter().map(|s| s.to_string()).collect(),
            ));
            self.responses.lock().unwrap().remove(0)
        }

        fn run_with_stdin(&self, _: &[u8], name: &str, args: &[&str]) -> std::io::Result<Output> {
            self.run(name, args)
        }
    }

    fn make_params(pairs: &[(&str, &str)]) -> HashMap<String, String> {
        pairs.iter().map(|(k, v)| (k.to_string(), v.to_string())).collect()
    }

    #[test]
    fn missing_path_errors() {
        let mock = MockRunner::new(vec![]);
        let exec = GitExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(&make_params(&[]), false);
        assert!(result.error.is_some());
        assert!(
            result.error.as_deref().unwrap().contains("path param is required"),
            "unexpected error: {:?}",
            result.error
        );
    }

    #[test]
    fn clone_new_repo() {
        let mock = MockRunner::new(vec![
            MockRunner::ok_output(""), // git clone
        ]);
        let calls = Arc::clone(&mock.calls);
        let dir = tempfile::tempdir().unwrap();
        let clone_path = dir.path().join("myrepo");

        let exec = GitExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("path", clone_path.to_str().unwrap()),
                ("repo", "https://github.com/example/repo.git"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::Created);
        assert!(result.changed);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1);
        assert_eq!(calls[0].0, "git");
        assert!(calls[0].1.contains(&"clone".to_string()));
        assert!(calls[0].1.contains(&"https://github.com/example/repo.git".to_string()));
    }

    #[test]
    fn clone_with_branch_and_depth() {
        let mock = MockRunner::new(vec![
            MockRunner::ok_output(""), // git clone
        ]);
        let calls = Arc::clone(&mock.calls);
        let dir = tempfile::tempdir().unwrap();
        let clone_path = dir.path().join("myrepo");

        let exec = GitExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("path", clone_path.to_str().unwrap()),
                ("repo", "https://github.com/example/repo.git"),
                ("branch", "develop"),
                ("depth", "1"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::Created);
        assert!(result.changed);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1);
        let args = &calls[0].1;
        assert!(args.contains(&"--branch".to_string()));
        assert!(args.contains(&"develop".to_string()));
        assert!(args.contains(&"--depth".to_string()));
        assert!(args.contains(&"1".to_string()));
    }

    #[test]
    fn present_already_up_to_date() {
        let mock = MockRunner::new(vec![
            MockRunner::ok_output(""),     // git -C <path> fetch
            MockRunner::ok_output("abc123"), // git -C <path> rev-parse HEAD
            MockRunner::ok_output("abc123"), // git -C <path> rev-parse @{u}
        ]);
        let calls = Arc::clone(&mock.calls);
        let dir = tempfile::tempdir().unwrap();
        // Create .git subdirectory so executor sees it as existing repo.
        std::fs::create_dir_all(dir.path().join(".git")).unwrap();

        let exec = GitExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("path", dir.path().to_str().unwrap()),
                ("repo", "https://github.com/example/repo.git"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 3);
    }

    #[test]
    fn present_needs_pull() {
        let mock = MockRunner::new(vec![
            MockRunner::ok_output(""),     // git -C <path> fetch
            MockRunner::ok_output("abc123"), // git -C <path> rev-parse HEAD (local)
            MockRunner::ok_output("def456"), // git -C <path> rev-parse @{u} (remote)
            MockRunner::ok_output(""),     // git -C <path> pull
        ]);
        let calls = Arc::clone(&mock.calls);
        let dir = tempfile::tempdir().unwrap();
        std::fs::create_dir_all(dir.path().join(".git")).unwrap();

        let exec = GitExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("path", dir.path().to_str().unwrap()),
                ("repo", "https://github.com/example/repo.git"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);

        let calls = calls.lock().unwrap();
        // fetch, rev-parse HEAD, rev-parse @{u}, pull
        assert_eq!(calls.len(), 4);
        assert!(calls[3].1.contains(&"pull".to_string()));
    }

    #[test]
    fn absent_removes_dir() {
        let dir = tempfile::tempdir().unwrap();
        let target = dir.path().join("to_remove");
        std::fs::create_dir_all(&target).unwrap();
        assert!(target.exists());

        let mock = MockRunner::new(vec![]);
        let exec = GitExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("path", target.to_str().unwrap()),
                ("state", "absent"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::Deleted);
        assert!(result.changed);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        assert!(!target.exists());
    }

    #[test]
    fn absent_noop_when_missing() {
        let dir = tempfile::tempdir().unwrap();
        let nonexistent = dir.path().join("does_not_exist");

        let mock = MockRunner::new(vec![]);
        let exec = GitExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("path", nonexistent.to_str().unwrap()),
                ("state", "absent"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());
    }

    #[test]
    fn dry_run_clone_no_commands() {
        let mock = MockRunner::new(vec![]);
        let calls = Arc::clone(&mock.calls);
        let dir = tempfile::tempdir().unwrap();
        let clone_path = dir.path().join("myrepo");

        let exec = GitExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("path", clone_path.to_str().unwrap()),
                ("repo", "https://github.com/example/repo.git"),
            ]),
            true,
        );
        assert_eq!(result.action, Action::Created);
        assert!(result.changed);
        assert!(result.error.is_none());

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 0, "dry_run should not call any git commands");
    }
}
