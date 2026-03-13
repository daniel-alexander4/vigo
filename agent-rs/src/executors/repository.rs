// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Repository executor — manages package repository configurations.
//! Delegates to the RepoManager trait implementations per package manager.

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::Instant;

/// Trait for managing package repositories.
pub trait RepoManager: Send + Sync {
    fn is_present(&self, name: &str) -> bool;
    fn add(&self, name: &str, repo: &str, key_url: &str) -> Result<(), String>;
    fn remove(&self, name: &str) -> Result<(), String>;
}

/// APT repository manager.
pub struct AptRepoManager {
    pub runner: Box<dyn CommandRunner>,
}

impl AptRepoManager {
    pub fn new() -> Self {
        Self {
            runner: Box::new(DefaultRunner),
        }
    }
}

impl RepoManager for AptRepoManager {
    fn is_present(&self, name: &str) -> bool {
        let list_path = format!("/etc/apt/sources.list.d/{}.list", name);
        let sources_path = format!("/etc/apt/sources.list.d/{}.sources", name);
        std::path::Path::new(&list_path).exists() || std::path::Path::new(&sources_path).exists()
    }

    fn add(&self, name: &str, repo: &str, key_url: &str) -> Result<(), String> {
        // Import GPG key if provided.
        if !key_url.is_empty() {
            let key_path = format!("/etc/apt/keyrings/{}.gpg", name);
            let _ = std::fs::create_dir_all("/etc/apt/keyrings");

            // Download key.
            match self.runner.run("curl", &["-fsSL", key_url, "-o", &key_path]) {
                Ok(o) if !o.status.success() => {
                    // Try wget as fallback.
                    match self.runner.run("wget", &["-qO", &key_path, key_url]) {
                        Ok(o2) if !o2.status.success() => {
                            return Err(format!("failed to download GPG key from {}", key_url));
                        }
                        Err(e) => return Err(format!("download GPG key: {}", e)),
                        _ => {}
                    }
                }
                Err(e) => return Err(format!("download GPG key: {}", e)),
                _ => {}
            }
        }

        let list_path = format!("/etc/apt/sources.list.d/{}.list", name);
        std::fs::write(&list_path, format!("{}\n", repo))
            .map_err(|e| format!("write {}: {}", list_path, e))?;

        // Refresh.
        let _ = self.runner.run("apt-get", &["update", "-qq"]);

        Ok(())
    }

    fn remove(&self, name: &str) -> Result<(), String> {
        let list_path = format!("/etc/apt/sources.list.d/{}.list", name);
        let sources_path = format!("/etc/apt/sources.list.d/{}.sources", name);
        let key_path = format!("/etc/apt/keyrings/{}.gpg", name);

        let _ = std::fs::remove_file(&list_path);
        let _ = std::fs::remove_file(&sources_path);
        let _ = std::fs::remove_file(&key_path);

        Ok(())
    }
}

/// DNF/YUM repository manager.
pub struct DnfRepoManager {
    pub runner: Box<dyn CommandRunner>,
}

impl DnfRepoManager {
    pub fn new() -> Self {
        Self {
            runner: Box::new(DefaultRunner),
        }
    }
}

impl RepoManager for DnfRepoManager {
    fn is_present(&self, name: &str) -> bool {
        let path = format!("/etc/yum.repos.d/{}.repo", name);
        std::path::Path::new(&path).exists()
    }

    fn add(&self, name: &str, repo: &str, key_url: &str) -> Result<(), String> {
        if !key_url.is_empty() {
            let _ = self.runner.run("rpm", &["--import", key_url]);
        }

        let path = format!("/etc/yum.repos.d/{}.repo", name);
        std::fs::write(&path, format!("{}\n", repo))
            .map_err(|e| format!("write {}: {}", path, e))
    }

    fn remove(&self, name: &str) -> Result<(), String> {
        let path = format!("/etc/yum.repos.d/{}.repo", name);
        if std::path::Path::new(&path).exists() {
            std::fs::remove_file(&path).map_err(|e| format!("remove {}: {}", path, e))?;
        }
        Ok(())
    }
}

pub struct RepositoryExecutor {
    pub repo_mgr: Box<dyn RepoManager>,
}

impl RepositoryExecutor {
    /// Create a RepositoryExecutor with the right RepoManager for the OS.
    pub fn for_os(os_family: &str) -> Self {
        let repo_mgr: Box<dyn RepoManager> = match os_family.to_lowercase().as_str() {
            "debian" => Box::new(AptRepoManager::new()),
            "redhat" | "fedora" | "suse" => Box::new(DnfRepoManager::new()),
            _ => Box::new(AptRepoManager::new()), // default to apt
        };
        Self { repo_mgr }
    }
}

impl Executor for RepositoryExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let name = params
            .get("repo_name")
            .or_else(|| params.get("name"))
            .cloned()
            .unwrap_or_default();

        let mut result = ResourceResult::new("repository", &name);

        if name.is_empty() {
            result.error = Some("repository executor: repo_name param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");
        let repo = params.get("repo").cloned().unwrap_or_default();
        let key_url = params.get("key_url").cloned().unwrap_or_default();

        match state {
            "present" => {
                if self.repo_mgr.is_present(&name) {
                    result.action = Action::None;
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                if repo.is_empty() {
                    result.error = Some("repository executor: repo param is required for state=present".into());
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                result.action = Action::Created;
                result.changed = true;

                if !dry_run {
                    if let Err(e) = self.repo_mgr.add(&name, &repo, &key_url) {
                        result.error = Some(e);
                    }
                }
            }
            "absent" => {
                if !self.repo_mgr.is_present(&name) {
                    result.action = Action::None;
                } else {
                    result.action = Action::Deleted;
                    result.changed = true;
                    if !dry_run {
                        if let Err(e) = self.repo_mgr.remove(&name) {
                            result.error = Some(e);
                        }
                    }
                }
            }
            _ => {
                result.error = Some(format!("repository executor: unknown state {:?}", state));
            }
        }

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::{Arc, Mutex};

    struct MockRepoManager {
        present: Mutex<Vec<String>>,
        add_calls: Arc<Mutex<Vec<(String, String, String)>>>,
        remove_calls: Arc<Mutex<Vec<String>>>,
    }

    impl MockRepoManager {
        fn new(present: Vec<&str>) -> Self {
            Self {
                present: Mutex::new(present.into_iter().map(|s| s.to_string()).collect()),
                add_calls: Arc::new(Mutex::new(Vec::new())),
                remove_calls: Arc::new(Mutex::new(Vec::new())),
            }
        }
    }

    impl RepoManager for MockRepoManager {
        fn is_present(&self, name: &str) -> bool {
            self.present.lock().unwrap().contains(&name.to_string())
        }

        fn add(&self, name: &str, repo: &str, key_url: &str) -> Result<(), String> {
            self.add_calls
                .lock()
                .unwrap()
                .push((name.into(), repo.into(), key_url.into()));
            Ok(())
        }

        fn remove(&self, name: &str) -> Result<(), String> {
            self.remove_calls.lock().unwrap().push(name.into());
            Ok(())
        }
    }

    fn make_params(pairs: &[(&str, &str)]) -> HashMap<String, String> {
        pairs
            .iter()
            .map(|(k, v)| (k.to_string(), v.to_string()))
            .collect()
    }

    #[test]
    fn missing_name_errors() {
        let mgr = MockRepoManager::new(vec![]);
        let exec = RepositoryExecutor {
            repo_mgr: Box::new(mgr),
        };
        let result = exec.execute(&make_params(&[]), false);
        assert!(result.error.is_some());
        assert!(
            result.error.as_deref().unwrap().contains("repo_name param is required"),
            "unexpected error: {:?}",
            result.error
        );
    }

    #[test]
    fn add_new_repo() {
        let mgr = MockRepoManager::new(vec![]);
        let add_calls = Arc::clone(&mgr.add_calls);
        let exec = RepositoryExecutor {
            repo_mgr: Box::new(mgr),
        };
        let result = exec.execute(
            &make_params(&[
                ("name", "docker-ce"),
                ("repo", "deb https://download.docker.com/linux/ubuntu jammy stable"),
                ("key_url", "https://download.docker.com/linux/ubuntu/gpg"),
                ("state", "present"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::Created);
        assert!(result.changed);
        assert!(result.error.is_none());
        let add_calls = add_calls.lock().unwrap();
        assert_eq!(add_calls.len(), 1);
        assert_eq!(add_calls[0].0, "docker-ce");
        assert!(add_calls[0].1.contains("download.docker.com"));
    }

    #[test]
    fn repo_already_present() {
        let mgr = MockRepoManager::new(vec!["docker-ce"]);
        let exec = RepositoryExecutor {
            repo_mgr: Box::new(mgr),
        };
        let result = exec.execute(
            &make_params(&[
                ("name", "docker-ce"),
                ("state", "present"),
                ("repo", "deb https://download.docker.com/linux/ubuntu jammy stable"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());
    }

    #[test]
    fn missing_repo_param() {
        let mgr = MockRepoManager::new(vec![]);
        let exec = RepositoryExecutor {
            repo_mgr: Box::new(mgr),
        };
        let result = exec.execute(
            &make_params(&[
                ("name", "docker-ce"),
                ("state", "present"),
            ]),
            false,
        );
        assert!(result.error.is_some());
        assert!(
            result.error.as_deref().unwrap().contains("repo param is required"),
            "unexpected error: {:?}",
            result.error
        );
    }

    #[test]
    fn absent_removes_repo() {
        let mgr = MockRepoManager::new(vec!["docker-ce"]);
        let remove_calls = Arc::clone(&mgr.remove_calls);
        let exec = RepositoryExecutor {
            repo_mgr: Box::new(mgr),
        };
        let result = exec.execute(
            &make_params(&[
                ("name", "docker-ce"),
                ("state", "absent"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::Deleted);
        assert!(result.changed);
        assert!(result.error.is_none());
        let remove_calls = remove_calls.lock().unwrap();
        assert_eq!(remove_calls.len(), 1);
        assert_eq!(remove_calls[0], "docker-ce");
    }

    #[test]
    fn absent_noop_when_missing() {
        let mgr = MockRepoManager::new(vec![]);
        let exec = RepositoryExecutor {
            repo_mgr: Box::new(mgr),
        };
        let result = exec.execute(
            &make_params(&[
                ("name", "docker-ce"),
                ("state", "absent"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());
    }
}
