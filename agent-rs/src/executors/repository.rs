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
