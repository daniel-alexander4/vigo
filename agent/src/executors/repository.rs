// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Repository executor — manages package repository configurations.
//! Delegates to the RepoManager trait implementations per package manager.
//!
//! Supported backends:
//! - APT (Debian/Ubuntu) — `.list` and DEB822 `.sources` formats
//! - DNF/YUM (RHEL/Fedora/CentOS) — `.repo` INI format
//! - APK (Alpine) — `/etc/apk/repositories` line-based
//! - Pacman (Arch) — `/etc/pacman.conf` section-based
//! - Zypper (SUSE/openSUSE) — `zypper addrepo`/`removerepo` commands

use super::{
    Action, CommandRunner, DefaultRunner, Executor, ResourceResult, PKG_MUTATE_TIMEOUT, PKG_NICE,
    PKG_QUERY_TIMEOUT,
};
use std::collections::HashMap;
use std::time::Instant;

/// Trait for managing package repositories.
pub trait RepoManager: Send + Sync {
    /// Check if the repo config file/entry exists for this name.
    fn is_present(&self, name: &str) -> bool;

    /// Return the current on-disk content for this repo, if any.
    fn current_content(&self, name: &str) -> Option<String>;

    /// Build the desired content string for the given params.
    fn desired_content(
        &self,
        name: &str,
        repo: &str,
        key_url: &str,
        enabled: bool,
        format: &str,
    ) -> String;

    /// Add (or overwrite) a repository configuration.
    fn add(
        &self,
        name: &str,
        repo: &str,
        key_url: &str,
        enabled: bool,
        format: &str,
        key_fingerprint: &str,
    ) -> Result<(), String>;

    /// Remove a repository configuration.
    fn remove(&self, name: &str) -> Result<(), String>;
}

// ---------------------------------------------------------------------------
// APT (Debian/Ubuntu)
// ---------------------------------------------------------------------------

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

    /// Download and install a GPG key for a repository.
    /// Stores in /etc/apt/keyrings/{name}.gpg (modern signed-by path).
    fn fetch_gpg_key(
        &self,
        name: &str,
        key_url: &str,
        key_fingerprint: &str,
    ) -> Result<(), String> {
        let key_path = format!("/etc/apt/keyrings/{}.gpg", name);
        let _ = std::fs::create_dir_all("/etc/apt/keyrings");

        let tmp_key = format!("/tmp/vigo-key-{}.asc", name);
        let downloaded = match self.runner.run_niced_timeout("curl", &["-fsSL", key_url, "-o", &tmp_key], PKG_NICE, PKG_MUTATE_TIMEOUT) {
            Ok(o) if o.status.success() => true,
            _ => match self.runner.run_niced_timeout("wget", &["-qO", &tmp_key, key_url], PKG_NICE, PKG_MUTATE_TIMEOUT) {
                Ok(o2) if o2.status.success() => true,
                _ => {
                    return Err(format!(
                        "repository {}: failed to download GPG key from {} — check the key_url",
                        name, key_url,
                    ))
                }
            },
        };
        if downloaded {
            // Verify fingerprint before installing, if requested.
            if !key_fingerprint.is_empty() {
                self.verify_gpg_fingerprint(name, &tmp_key, key_fingerprint)?;
            }

            // Dearmor: convert ASCII-armored key to binary GPG format.
            let dearmor = self.runner.run_niced_timeout(
                "sh",
                &[
                    "-c",
                    &format!(
                        "gpg --batch --yes --dearmor -o '{}' '{}' 2>/dev/null || cp '{}' '{}'",
                        key_path, tmp_key, tmp_key, key_path
                    ),
                ],
                PKG_NICE,
                PKG_MUTATE_TIMEOUT,
            );
            let _ = std::fs::remove_file(&tmp_key);
            if let Err(e) = dearmor {
                return Err(format!(
                    "repository {}: dearmor GPG key failed — {}",
                    name, e
                ));
            }
        }
        Ok(())
    }

    /// Verify a downloaded GPG key's fingerprint matches the expected value.
    fn verify_gpg_fingerprint(
        &self,
        name: &str,
        key_path: &str,
        expected: &str,
    ) -> Result<(), String> {
        let result = self.runner.run_niced_timeout(
            "gpg",
            &[
                "--batch",
                "--with-colons",
                "--import-options",
                "show-only",
                "--import",
                key_path,
            ],
            PKG_NICE,
            PKG_QUERY_TIMEOUT,
        );
        match result {
            Ok(output) if output.status.success() => {
                let stdout = String::from_utf8_lossy(&output.stdout);
                let normalized_expected = expected.replace(" ", "").to_uppercase();
                for line in stdout.lines() {
                    if line.starts_with("fpr:") {
                        let fpr = line
                            .split(':')
                            .nth(9)
                            .unwrap_or("")
                            .to_uppercase();
                        if fpr == normalized_expected
                            || fpr.ends_with(&normalized_expected)
                        {
                            return Ok(());
                        }
                    }
                }
                let _ = std::fs::remove_file(key_path);
                Err(format!(
                    "repository {}: GPG key fingerprint mismatch — expected {} but key does not contain that fingerprint",
                    name, expected,
                ))
            }
            Ok(output) => {
                let stderr = String::from_utf8_lossy(&output.stderr);
                let _ = std::fs::remove_file(key_path);
                Err(format!(
                    "repository {}: failed to inspect GPG key — {}",
                    name,
                    stderr.lines().next().unwrap_or("unknown error"),
                ))
            }
            Err(e) => {
                let _ = std::fs::remove_file(key_path);
                Err(format!(
                    "repository {}: failed to run gpg for fingerprint check — {}",
                    name, e,
                ))
            }
        }
    }

    /// Detect whether the system prefers DEB822 format.
    /// Returns true if /etc/apt/sources.list.d/ubuntu.sources exists.
    fn system_prefers_deb822(&self) -> bool {
        std::path::Path::new("/etc/apt/sources.list.d/ubuntu.sources").exists()
    }

    /// Build a .list-format line from a repo definition.
    fn build_list_content(&self, repo: &str, enabled: bool) -> String {
        if enabled {
            format!("{}\n", repo)
        } else {
            format!("# {}\n", repo)
        }
    }

    /// Build a DEB822 .sources file from a deb line.
    /// Parses: `deb [options] <uri> <suite> <components...>`
    fn build_deb822_content(
        &self,
        name: &str,
        repo: &str,
        enabled: bool,
    ) -> String {
        // Parse the deb line. Strip "deb " or "deb-src " prefix.
        let line = repo.trim();
        let (types, rest) = if let Some(rest) = line.strip_prefix("deb-src ") {
            ("deb-src", rest)
        } else if let Some(rest) = line.strip_prefix("deb ") {
            ("deb", rest)
        } else {
            // Not a recognizable deb line — write as-is in a comment.
            return format!("# Could not parse as deb line: {}\n", repo);
        };

        let rest = rest.trim();

        // Handle [options] block.
        let (options_str, remainder) = if rest.starts_with('[') {
            if let Some(close) = rest.find(']') {
                (rest[1..close].trim().to_string(), rest[close + 1..].trim())
            } else {
                (String::new(), rest)
            }
        } else {
            (String::new(), rest)
        };

        // Parse: <uri> <suite> <component...>
        let parts: Vec<&str> = remainder.split_whitespace().collect();
        if parts.len() < 2 {
            return format!("# Could not parse deb line components: {}\n", repo);
        }
        let uri = parts[0];
        let suite = parts[1];
        let components = if parts.len() > 2 {
            parts[2..].join(" ")
        } else {
            String::new()
        };

        let mut out = String::new();
        out.push_str(&format!("Types: {}\n", types));
        out.push_str(&format!("URIs: {}\n", uri));
        out.push_str(&format!("Suites: {}\n", suite));
        if !components.is_empty() {
            out.push_str(&format!("Components: {}\n", components));
        }

        // Extract signed-by from options if present, otherwise add it.
        let mut has_signed_by = false;
        for opt in options_str.split_whitespace() {
            if let Some(val) = opt.strip_prefix("signed-by=") {
                out.push_str(&format!("Signed-By: {}\n", val));
                has_signed_by = true;
            } else if let Some(val) = opt.strip_prefix("arch=") {
                out.push_str(&format!("Architectures: {}\n", val));
            }
        }
        if !has_signed_by {
            let key_path = format!("/etc/apt/keyrings/{}.gpg", name);
            if std::path::Path::new(&key_path).exists() {
                out.push_str(&format!("Signed-By: {}\n", key_path));
            }
        }

        if !enabled {
            out.push_str("Enabled: no\n");
        }

        out
    }

    /// Resolve the effective format: "list", "deb822", or auto-detect.
    fn effective_format(&self, format: &str) -> &str {
        match format {
            "deb822" => "deb822",
            "list" => "list",
            _ => {
                // Auto-detect.
                if self.system_prefers_deb822() {
                    "deb822"
                } else {
                    "list"
                }
            }
        }
    }

    /// Return the file path for this repo given the format.
    fn repo_path(&self, name: &str, format: &str) -> String {
        let fmt = self.effective_format(format);
        if fmt == "deb822" {
            format!("/etc/apt/sources.list.d/{}.sources", name)
        } else {
            format!("/etc/apt/sources.list.d/{}.list", name)
        }
    }
}

impl RepoManager for AptRepoManager {
    fn is_present(&self, name: &str) -> bool {
        let list_path = format!("/etc/apt/sources.list.d/{}.list", name);
        let sources_path = format!("/etc/apt/sources.list.d/{}.sources", name);
        std::path::Path::new(&list_path).exists() || std::path::Path::new(&sources_path).exists()
    }

    fn current_content(&self, name: &str) -> Option<String> {
        let list_path = format!("/etc/apt/sources.list.d/{}.list", name);
        let sources_path = format!("/etc/apt/sources.list.d/{}.sources", name);
        std::fs::read_to_string(&sources_path)
            .or_else(|_| std::fs::read_to_string(&list_path))
            .ok()
    }

    fn desired_content(
        &self,
        name: &str,
        repo: &str,
        _key_url: &str,
        enabled: bool,
        format: &str,
    ) -> String {
        let fmt = self.effective_format(format);
        if fmt == "deb822" {
            self.build_deb822_content(name, repo, enabled)
        } else {
            self.build_list_content(repo, enabled)
        }
    }

    fn add(
        &self,
        name: &str,
        repo: &str,
        key_url: &str,
        enabled: bool,
        format: &str,
        key_fingerprint: &str,
    ) -> Result<(), String> {
        // Import GPG key if provided. Uses modern signed-by path
        // (/etc/apt/keyrings/) — never apt-key add.
        if !key_url.is_empty() {
            self.fetch_gpg_key(name, key_url, key_fingerprint)?;
        }

        let path = self.repo_path(name, format);
        let content = self.desired_content(name, repo, key_url, enabled, format);
        std::fs::write(&path, &content).map_err(|e| format!("write {}: {}", path, e))?;

        // Clean up the other format file if it exists (switching formats).
        let fmt = self.effective_format(format);
        if fmt == "deb822" {
            let old_list = format!("/etc/apt/sources.list.d/{}.list", name);
            let _ = std::fs::remove_file(&old_list);
        } else {
            let old_sources = format!("/etc/apt/sources.list.d/{}.sources", name);
            let _ = std::fs::remove_file(&old_sources);
        }

        // Refresh and capture errors — a GPG or repo error here is actionable.
        match self.runner.run_niced_timeout("apt-get", &["update", "-qq"], PKG_NICE, PKG_MUTATE_TIMEOUT) {
            Ok(o) if !o.status.success() => {
                let stderr = String::from_utf8_lossy(&o.stderr);
                let lower = stderr.to_lowercase();
                if lower.contains("no_pubkey") {
                    let fingerprint = stderr
                        .split("NO_PUBKEY ")
                        .nth(1)
                        .and_then(|s| s.split_whitespace().next())
                        .unwrap_or("unknown");
                    return Err(format!(
                        "repository {}: GPG key missing (NO_PUBKEY {}) — add key_url to the repository resource",
                        name, fingerprint,
                    ));
                }
                if lower.contains("expkeysig") || lower.contains("signatures were invalid") {
                    if !key_url.is_empty() {
                        tracing::info!(repo = name, "GPG key expired, re-fetching from key_url");
                        if let Err(e) = self.fetch_gpg_key(name, key_url, key_fingerprint) {
                            return Err(format!(
                                "repository {}: GPG key expired, re-fetch failed — {}",
                                name, e,
                            ));
                        }
                        match self.runner.run_niced_timeout("apt-get", &["update", "-qq"], PKG_NICE, PKG_MUTATE_TIMEOUT) {
                            Ok(o2) if o2.status.success() => return Ok(()),
                            Ok(o2) => {
                                let stderr2 = String::from_utf8_lossy(&o2.stderr);
                                return Err(format!(
                                    "repository {}: GPG key still invalid after re-fetch — {}",
                                    name,
                                    stderr2.lines().next().unwrap_or(""),
                                ));
                            }
                            _ => {}
                        }
                    }
                    return Err(format!(
                        "repository {}: GPG key expired — add or update the key_url to fix",
                        name,
                    ));
                }
            }
            _ => {}
        }

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

// ---------------------------------------------------------------------------
// DNF/YUM (RHEL/Fedora/CentOS)
// ---------------------------------------------------------------------------

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

    /// Generate a proper .repo INI file from a baseurl.
    /// If `repo` already starts with `[`, assume the user provided a full .repo
    /// file and return it as-is.
    fn build_repo_content(
        &self,
        name: &str,
        repo: &str,
        key_url: &str,
        enabled: bool,
    ) -> String {
        let trimmed = repo.trim();
        if trimmed.starts_with('[') {
            // User provided a full INI-formatted .repo file — use verbatim.
            // Still need to ensure trailing newline.
            if trimmed.ends_with('\n') {
                trimmed.to_string()
            } else {
                format!("{}\n", trimmed)
            }
        } else {
            // Generate proper INI format from a baseurl.
            let enabled_val = if enabled { 1 } else { 0 };
            let mut content = format!(
                "[{}]\nname={}\nbaseurl={}\nenabled={}\n",
                name, name, trimmed, enabled_val,
            );
            if !key_url.is_empty() {
                content.push_str(&format!("gpgcheck=1\ngpgkey={}\n", key_url));
            } else {
                content.push_str("gpgcheck=0\n");
            }
            content
        }
    }
}

impl RepoManager for DnfRepoManager {
    fn is_present(&self, name: &str) -> bool {
        let path = format!("/etc/yum.repos.d/{}.repo", name);
        std::path::Path::new(&path).exists()
    }

    fn current_content(&self, name: &str) -> Option<String> {
        let path = format!("/etc/yum.repos.d/{}.repo", name);
        std::fs::read_to_string(&path).ok()
    }

    fn desired_content(
        &self,
        name: &str,
        repo: &str,
        key_url: &str,
        enabled: bool,
        _format: &str,
    ) -> String {
        self.build_repo_content(name, repo, key_url, enabled)
    }

    fn add(
        &self,
        name: &str,
        repo: &str,
        key_url: &str,
        enabled: bool,
        _format: &str,
        _key_fingerprint: &str,
    ) -> Result<(), String> {
        if !key_url.is_empty() {
            let _ = self.runner.run_niced_timeout("rpm", &["--import", key_url], PKG_NICE, PKG_MUTATE_TIMEOUT);
        }

        let path = format!("/etc/yum.repos.d/{}.repo", name);
        let content = self.build_repo_content(name, repo, key_url, enabled);
        std::fs::write(&path, &content).map_err(|e| format!("write {}: {}", path, e))
    }

    fn remove(&self, name: &str) -> Result<(), String> {
        let path = format!("/etc/yum.repos.d/{}.repo", name);
        if std::path::Path::new(&path).exists() {
            std::fs::remove_file(&path).map_err(|e| format!("remove {}: {}", path, e))?;
        }
        Ok(())
    }
}

// ---------------------------------------------------------------------------
// APK (Alpine Linux)
// ---------------------------------------------------------------------------

/// APK repository manager — manages /etc/apk/repositories (one repo per line).
pub struct ApkRepoManager {
    pub runner: Box<dyn CommandRunner>,
}

impl ApkRepoManager {
    pub fn new() -> Self {
        Self {
            runner: Box::new(DefaultRunner),
        }
    }

    fn repo_file() -> &'static str {
        "/etc/apk/repositories"
    }

    /// Read all lines from the repositories file.
    fn read_lines(&self) -> Vec<String> {
        std::fs::read_to_string(Self::repo_file())
            .unwrap_or_default()
            .lines()
            .map(|l| l.to_string())
            .collect()
    }

    /// Write lines back to the repositories file.
    fn write_lines(&self, lines: &[String]) -> Result<(), String> {
        let mut content = lines.join("\n");
        if !content.is_empty() && !content.ends_with('\n') {
            content.push('\n');
        }
        std::fs::write(Self::repo_file(), &content)
            .map_err(|e| format!("write {}: {}", Self::repo_file(), e))
    }

    /// Find the index of a line that matches this repo (ignoring leading # for disabled).
    fn find_repo_line(&self, lines: &[String], repo: &str) -> Option<usize> {
        let trimmed = repo.trim();
        lines.iter().position(|l| {
            let l = l.trim();
            l == trimmed || l == format!("# {}", trimmed) || l.strip_prefix("# ") == Some(trimmed)
        })
    }
}

impl RepoManager for ApkRepoManager {
    fn is_present(&self, name: &str) -> bool {
        // For APK, `name` is actually checked against the repo URL in params,
        // but we also need to handle "is this repo URL in the file".
        // The executor passes `repo` content through desired_content matching.
        // Here we just check if the name appears in any line.
        let lines = self.read_lines();
        lines.iter().any(|l| {
            let stripped = l.trim().trim_start_matches("# ");
            stripped.contains(name)
        })
    }

    fn current_content(&self, name: &str) -> Option<String> {
        let lines = self.read_lines();
        // Return the matching line if found.
        lines
            .iter()
            .find(|l| {
                let stripped = l.trim().trim_start_matches("# ");
                stripped.contains(name)
            })
            .cloned()
    }

    fn desired_content(
        &self,
        _name: &str,
        repo: &str,
        _key_url: &str,
        enabled: bool,
        _format: &str,
    ) -> String {
        let trimmed = repo.trim();
        if enabled {
            trimmed.to_string()
        } else {
            format!("# {}", trimmed)
        }
    }

    fn add(
        &self,
        _name: &str,
        repo: &str,
        _key_url: &str,
        enabled: bool,
        _format: &str,
        _key_fingerprint: &str,
    ) -> Result<(), String> {
        let mut lines = self.read_lines();
        let desired = if enabled {
            repo.trim().to_string()
        } else {
            format!("# {}", repo.trim())
        };

        if let Some(idx) = self.find_repo_line(&lines, repo) {
            // Update existing line.
            lines[idx] = desired;
        } else {
            // Append new line.
            lines.push(desired);
        }

        self.write_lines(&lines)?;

        // Refresh package index.
        let _ = self.runner.run_niced_timeout("apk", &["update"], PKG_NICE, PKG_MUTATE_TIMEOUT);
        Ok(())
    }

    fn remove(&self, name: &str) -> Result<(), String> {
        let lines = self.read_lines();
        let filtered: Vec<String> = lines
            .into_iter()
            .filter(|l| {
                let stripped = l.trim().trim_start_matches("# ");
                !stripped.contains(name)
            })
            .collect();
        self.write_lines(&filtered)
    }
}

// ---------------------------------------------------------------------------
// Pacman (Arch Linux)
// ---------------------------------------------------------------------------

/// Pacman repository manager — manages sections in /etc/pacman.conf.
pub struct PacmanRepoManager {
    pub runner: Box<dyn CommandRunner>,
}

impl PacmanRepoManager {
    pub fn new() -> Self {
        Self {
            runner: Box::new(DefaultRunner),
        }
    }

    fn conf_path() -> &'static str {
        "/etc/pacman.conf"
    }

    /// Read the pacman.conf file.
    fn read_conf(&self) -> String {
        std::fs::read_to_string(Self::conf_path()).unwrap_or_default()
    }

    /// Write the pacman.conf file.
    fn write_conf(&self, content: &str) -> Result<(), String> {
        std::fs::write(Self::conf_path(), content)
            .map_err(|e| format!("write {}: {}", Self::conf_path(), e))
    }

    /// Build a pacman.conf section block.
    fn build_section(name: &str, repo: &str, enabled: bool) -> String {
        if enabled {
            format!("[{}]\nServer = {}\n", name, repo.trim())
        } else {
            format!("# [{}]\n# Server = {}\n", name, repo.trim())
        }
    }

    /// Find and extract a section block for the given repo name.
    /// Returns (start_line_idx, end_line_idx_exclusive) if found.
    fn find_section(lines: &[&str], name: &str) -> Option<(usize, usize)> {
        let section_header = format!("[{}]", name);
        let commented_header = format!("# [{}]", name);

        let start = lines.iter().position(|l| {
            let trimmed = l.trim();
            trimmed == section_header || trimmed == commented_header
        })?;

        // Find the end — next section header or EOF.
        let end = lines[start + 1..]
            .iter()
            .position(|l| {
                let trimmed = l.trim();
                let uncommented = trimmed.trim_start_matches("# ");
                uncommented.starts_with('[') && uncommented.contains(']')
            })
            .map(|i| start + 1 + i)
            .unwrap_or(lines.len());

        Some((start, end))
    }
}

impl RepoManager for PacmanRepoManager {
    fn is_present(&self, name: &str) -> bool {
        let content = self.read_conf();
        let section = format!("[{}]", name);
        let commented = format!("# [{}]", name);
        content.lines().any(|l| {
            let t = l.trim();
            t == section || t == commented
        })
    }

    fn current_content(&self, name: &str) -> Option<String> {
        let content = self.read_conf();
        let lines: Vec<&str> = content.lines().collect();
        let (start, end) = Self::find_section(&lines, name)?;
        Some(lines[start..end].join("\n") + "\n")
    }

    fn desired_content(
        &self,
        name: &str,
        repo: &str,
        _key_url: &str,
        enabled: bool,
        _format: &str,
    ) -> String {
        Self::build_section(name, repo, enabled)
    }

    fn add(
        &self,
        name: &str,
        repo: &str,
        key_url: &str,
        enabled: bool,
        _format: &str,
        key_fingerprint: &str,
    ) -> Result<(), String> {
        // Import GPG key if fingerprint provided.
        let fpr = if !key_fingerprint.is_empty() {
            key_fingerprint
        } else if !key_url.is_empty() {
            // For pacman, key_url is treated as a fingerprint if it looks like one.
            key_url
        } else {
            ""
        };
        if !fpr.is_empty() {
            let _ = self
                .runner
                .run_niced_timeout("pacman-key", &["--recv-keys", fpr], PKG_NICE, PKG_MUTATE_TIMEOUT);
            let _ = self
                .runner
                .run_niced_timeout("pacman-key", &["--lsign-key", fpr], PKG_NICE, PKG_MUTATE_TIMEOUT);
        }

        let content = self.read_conf();
        let lines: Vec<&str> = content.lines().collect();
        let section_block = Self::build_section(name, repo, enabled);

        let new_content = if let Some((start, end)) = Self::find_section(&lines, name) {
            // Replace existing section.
            let mut result: Vec<String> = lines[..start].iter().map(|l| l.to_string()).collect();
            // Add the new section (trimming trailing newline since join adds them).
            for line in section_block.lines() {
                result.push(line.to_string());
            }
            result.extend(lines[end..].iter().map(|l| l.to_string()));
            result.join("\n") + "\n"
        } else {
            // Append to end.
            let mut result = content.clone();
            if !result.ends_with('\n') {
                result.push('\n');
            }
            result.push_str(&section_block);
            result
        };

        self.write_conf(&new_content)?;

        // Refresh package database.
        let _ = self.runner.run_niced_timeout("pacman", &["-Sy", "--noconfirm"], PKG_NICE, PKG_MUTATE_TIMEOUT);
        Ok(())
    }

    fn remove(&self, name: &str) -> Result<(), String> {
        let content = self.read_conf();
        let lines: Vec<&str> = content.lines().collect();

        if let Some((start, end)) = Self::find_section(&lines, name) {
            let mut result: Vec<String> = lines[..start].iter().map(|l| l.to_string()).collect();
            result.extend(lines[end..].iter().map(|l| l.to_string()));
            let new_content = result.join("\n") + "\n";
            self.write_conf(&new_content)?;
        }
        Ok(())
    }
}

// ---------------------------------------------------------------------------
// Zypper (SUSE/openSUSE)
// ---------------------------------------------------------------------------

/// Zypper repository manager — uses zypper CLI commands.
pub struct ZypperRepoManager {
    pub runner: Box<dyn CommandRunner>,
}

impl ZypperRepoManager {
    pub fn new() -> Self {
        Self {
            runner: Box::new(DefaultRunner),
        }
    }
}

impl RepoManager for ZypperRepoManager {
    fn is_present(&self, name: &str) -> bool {
        // Check if zypper knows about this repo.
        match self.runner.run_niced_timeout("zypper", &["repos", "-u"], PKG_NICE, PKG_QUERY_TIMEOUT) {
            Ok(output) if output.status.success() => {
                let stdout = String::from_utf8_lossy(&output.stdout);
                stdout.lines().any(|l| l.contains(name))
            }
            _ => false,
        }
    }

    fn current_content(&self, name: &str) -> Option<String> {
        // Return the repo info from zypper as the "content" for comparison.
        match self.runner.run_niced_timeout("zypper", &["repos", "-d", name], PKG_NICE, PKG_QUERY_TIMEOUT) {
            Ok(output) if output.status.success() => {
                Some(String::from_utf8_lossy(&output.stdout).to_string())
            }
            _ => None,
        }
    }

    fn desired_content(
        &self,
        _name: &str,
        repo: &str,
        _key_url: &str,
        enabled: bool,
        _format: &str,
    ) -> String {
        // For zypper, content comparison is URI + enabled state.
        // Return a canonical string for comparison.
        format!("uri={} enabled={}", repo.trim(), enabled)
    }

    fn add(
        &self,
        name: &str,
        repo: &str,
        key_url: &str,
        enabled: bool,
        _format: &str,
        _key_fingerprint: &str,
    ) -> Result<(), String> {
        if !key_url.is_empty() {
            let _ = self.runner.run_niced_timeout("rpm", &["--import", key_url], PKG_NICE, PKG_MUTATE_TIMEOUT);
        }

        // Remove first if already present (to update URL).
        if self.is_present(name) {
            let _ = self
                .runner
                .run_niced_timeout("zypper", &["--non-interactive", "removerepo", name], PKG_NICE, PKG_MUTATE_TIMEOUT);
        }

        let result = self.runner.run_niced_timeout(
            "zypper",
            &["--non-interactive", "addrepo", "-f", repo.trim(), name],
            PKG_NICE,
            PKG_MUTATE_TIMEOUT,
        );
        match result {
            Ok(o) if o.status.success() => {}
            Ok(o) => {
                let stderr = String::from_utf8_lossy(&o.stderr);
                return Err(format!(
                    "repository {}: zypper addrepo failed — {}",
                    name,
                    stderr.lines().next().unwrap_or("unknown error"),
                ));
            }
            Err(e) => {
                return Err(format!(
                    "repository {}: failed to run zypper — {}",
                    name, e,
                ));
            }
        }

        if !enabled {
            let _ = self.runner.run_niced_timeout(
                "zypper",
                &["--non-interactive", "modifyrepo", "-d", name],
                PKG_NICE,
                PKG_MUTATE_TIMEOUT,
            );
        }

        // Refresh.
        let _ = self
            .runner
            .run_niced_timeout("zypper", &["--non-interactive", "refresh"], PKG_NICE, PKG_MUTATE_TIMEOUT);
        Ok(())
    }

    fn remove(&self, name: &str) -> Result<(), String> {
        if !self.is_present(name) {
            return Ok(());
        }
        match self
            .runner
            .run_niced_timeout("zypper", &["--non-interactive", "removerepo", name], PKG_NICE, PKG_MUTATE_TIMEOUT)
        {
            Ok(o) if o.status.success() => Ok(()),
            Ok(o) => {
                let stderr = String::from_utf8_lossy(&o.stderr);
                Err(format!(
                    "repository {}: zypper removerepo failed — {}",
                    name,
                    stderr.lines().next().unwrap_or("unknown error"),
                ))
            }
            Err(e) => Err(format!(
                "repository {}: failed to run zypper — {}",
                name, e,
            )),
        }
    }
}

// ---------------------------------------------------------------------------
// Executor
// ---------------------------------------------------------------------------

pub struct RepositoryExecutor {
    pub repo_mgr: Box<dyn RepoManager>,
}

impl RepositoryExecutor {
    /// Create a RepositoryExecutor with the right RepoManager for the OS.
    pub fn for_os(os_family: &str) -> Self {
        let repo_mgr: Box<dyn RepoManager> = match os_family.to_lowercase().as_str() {
            "debian" => Box::new(AptRepoManager::new()),
            "redhat" | "fedora" => Box::new(DnfRepoManager::new()),
            "suse" | "opensuse" => Box::new(ZypperRepoManager::new()),
            "alpine" => Box::new(ApkRepoManager::new()),
            "arch" | "archlinux" | "manjaro" => Box::new(PacmanRepoManager::new()),
            _ => Box::new(AptRepoManager::new()), // default to apt
        };
        Self { repo_mgr }
    }
}

impl Executor for RepositoryExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let name = params.get("repository").cloned().unwrap_or_default();

        let mut result = ResourceResult::new("repository", &name);

        if name.is_empty() {
            result.error = Some("repository executor: repository param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");
        let repo = params.get("repo").cloned().unwrap_or_default();
        let key_url = params.get("key_url").cloned().unwrap_or_default();
        let key_fingerprint = params
            .get("key_fingerprint")
            .cloned()
            .unwrap_or_default();
        let enabled = params
            .get("enabled")
            .map(|v| v != "false" && v != "0" && v != "no")
            .unwrap_or(true);
        let format = params
            .get("format")
            .map(|s| s.as_str())
            .unwrap_or("auto");

        match state {
            "present" => {
                if repo.is_empty() {
                    result.error =
                        Some("repository executor: repo param is required for state=present".into());
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                let present = self.repo_mgr.is_present(&name);

                if present {
                    // File exists — check for content drift.
                    let current = self.repo_mgr.current_content(&name);
                    let desired =
                        self.repo_mgr
                            .desired_content(&name, &repo, &key_url, enabled, format);
                    let matches = current
                        .as_ref()
                        .map(|c| c.trim() == desired.trim())
                        .unwrap_or(false);

                    if matches {
                        result.action = Action::None;
                        result.duration_ms = start.elapsed().as_millis() as i32;
                        return result;
                    }

                    // Content drift — update.
                    result.action = Action::Updated;
                    result.changed = true;

                    if !dry_run {
                        if let Err(e) = self.repo_mgr.add(
                            &name,
                            &repo,
                            &key_url,
                            enabled,
                            format,
                            &key_fingerprint,
                        ) {
                            result.error = Some(e);
                        }
                    }
                } else {
                    // File does not exist — create.
                    result.action = Action::Created;
                    result.changed = true;

                    if !dry_run {
                        if let Err(e) = self.repo_mgr.add(
                            &name,
                            &repo,
                            &key_url,
                            enabled,
                            format,
                            &key_fingerprint,
                        ) {
                            result.error = Some(e);
                        }
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

    // -----------------------------------------------------------------------
    // Mock repo manager
    // -----------------------------------------------------------------------

    /// Recorded `add` arguments: (name, url, key, enabled, components, suites).
    type AddCall = (String, String, String, bool, String, String);
    /// Shared, mutable log of recorded `add` calls.
    type AddCallLog = Arc<Mutex<Vec<AddCall>>>;

    struct MockRepoManager {
        present: Mutex<Vec<String>>,
        contents: Mutex<HashMap<String, String>>,
        add_calls: AddCallLog,
        remove_calls: Arc<Mutex<Vec<String>>>,
    }

    impl MockRepoManager {
        fn new(present: Vec<&str>) -> Self {
            Self {
                present: Mutex::new(present.iter().map(|s| s.to_string()).collect()),
                contents: Mutex::new(HashMap::new()),
                add_calls: Arc::new(Mutex::new(Vec::new())),
                remove_calls: Arc::new(Mutex::new(Vec::new())),
            }
        }

        /// Create a mock with a repo that has specific on-disk content.
        fn with_content(name: &str, content: &str) -> Self {
            let mut contents = HashMap::new();
            contents.insert(name.to_string(), content.to_string());
            Self {
                present: Mutex::new(vec![name.to_string()]),
                contents: Mutex::new(contents),
                add_calls: Arc::new(Mutex::new(Vec::new())),
                remove_calls: Arc::new(Mutex::new(Vec::new())),
            }
        }
    }

    impl RepoManager for MockRepoManager {
        fn is_present(&self, name: &str) -> bool {
            self.present.lock().unwrap().contains(&name.to_string())
        }

        fn current_content(&self, name: &str) -> Option<String> {
            self.contents.lock().unwrap().get(name).cloned()
        }

        fn desired_content(
            &self,
            _name: &str,
            repo: &str,
            _key_url: &str,
            enabled: bool,
            _format: &str,
        ) -> String {
            if enabled {
                format!("{}\n", repo)
            } else {
                format!("# {}\n", repo)
            }
        }

        fn add(
            &self,
            name: &str,
            repo: &str,
            key_url: &str,
            enabled: bool,
            format: &str,
            key_fingerprint: &str,
        ) -> Result<(), String> {
            self.add_calls.lock().unwrap().push((
                name.into(),
                repo.into(),
                key_url.into(),
                enabled,
                format.into(),
                key_fingerprint.into(),
            ));
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

    // -----------------------------------------------------------------------
    // Basic executor tests
    // -----------------------------------------------------------------------

    #[test]
    fn missing_name_errors() {
        let mgr = MockRepoManager::new(vec![]);
        let exec = RepositoryExecutor {
            repo_mgr: Box::new(mgr),
        };
        let result = exec.execute(&make_params(&[]), false);
        assert!(result.error.is_some());
        assert!(
            result.error.as_deref().unwrap().contains("repository param is required"),
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
                ("repository", "docker-ce"),
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
        assert!(add_calls[0].3); // enabled=true
    }

    #[test]
    fn missing_repo_param() {
        let mgr = MockRepoManager::new(vec![]);
        let exec = RepositoryExecutor {
            repo_mgr: Box::new(mgr),
        };
        let result = exec.execute(
            &make_params(&[("repository", "docker-ce"), ("state", "present")]),
            false,
        );
        assert!(result.error.is_some());
        assert!(
            result
                .error
                .as_deref()
                .unwrap()
                .contains("repo param is required"),
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
            &make_params(&[("repository", "docker-ce"), ("state", "absent")]),
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
            &make_params(&[("repository", "docker-ce"), ("state", "absent")]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());
    }

    // -----------------------------------------------------------------------
    // Content drift detection tests
    // -----------------------------------------------------------------------

    #[test]
    fn present_no_drift_is_noop() {
        // On-disk content matches desired — should be Action::None.
        let repo_line = "deb https://download.docker.com/linux/ubuntu jammy stable";
        let mgr = MockRepoManager::with_content("docker-ce", &format!("{}\n", repo_line));
        let exec = RepositoryExecutor {
            repo_mgr: Box::new(mgr),
        };
        let result = exec.execute(
            &make_params(&[
                ("repository", "docker-ce"),
                ("repo", repo_line),
                ("state", "present"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
    }

    #[test]
    fn present_with_drift_updates() {
        // On-disk content differs from desired — should be Action::Updated.
        let mgr = MockRepoManager::with_content(
            "docker-ce",
            "deb https://old-url.example.com/repo jammy stable\n",
        );
        let add_calls = Arc::clone(&mgr.add_calls);
        let exec = RepositoryExecutor {
            repo_mgr: Box::new(mgr),
        };
        let result = exec.execute(
            &make_params(&[
                ("repository", "docker-ce"),
                (
                    "repo",
                    "deb https://download.docker.com/linux/ubuntu jammy stable",
                ),
                ("state", "present"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);
        assert!(result.error.is_none());
        let calls = add_calls.lock().unwrap();
        assert_eq!(calls.len(), 1);
        assert!(calls[0].1.contains("download.docker.com"));
    }

    #[test]
    fn present_file_exists_but_unreadable_updates() {
        // File exists but current_content returns None — treat as drift.
        let mgr = MockRepoManager::new(vec!["docker-ce"]);
        // present=["docker-ce"] but contents is empty => current_content returns None.
        let add_calls = Arc::clone(&mgr.add_calls);
        let exec = RepositoryExecutor {
            repo_mgr: Box::new(mgr),
        };
        let result = exec.execute(
            &make_params(&[
                ("repository", "docker-ce"),
                ("repo", "deb https://example.com/repo jammy stable"),
                ("state", "present"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);
        let calls = add_calls.lock().unwrap();
        assert_eq!(calls.len(), 1);
    }

    #[test]
    fn drift_detection_dry_run_does_not_write() {
        let mgr = MockRepoManager::with_content("docker-ce", "old content\n");
        let add_calls = Arc::clone(&mgr.add_calls);
        let exec = RepositoryExecutor {
            repo_mgr: Box::new(mgr),
        };
        let result = exec.execute(
            &make_params(&[
                ("repository", "docker-ce"),
                ("repo", "deb https://new-url.example.com/repo jammy stable"),
                ("state", "present"),
            ]),
            true,
        );
        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);
        // dry_run: add() should NOT have been called.
        let calls = add_calls.lock().unwrap();
        assert_eq!(calls.len(), 0);
    }

    // -----------------------------------------------------------------------
    // Enabled/disabled param tests
    // -----------------------------------------------------------------------

    #[test]
    fn enabled_false_passes_to_manager() {
        let mgr = MockRepoManager::new(vec![]);
        let add_calls = Arc::clone(&mgr.add_calls);
        let exec = RepositoryExecutor {
            repo_mgr: Box::new(mgr),
        };
        let result = exec.execute(
            &make_params(&[
                ("repository", "test-repo"),
                ("repo", "deb https://example.com/repo jammy main"),
                ("enabled", "false"),
                ("state", "present"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::Created);
        assert!(result.changed);
        let calls = add_calls.lock().unwrap();
        assert_eq!(calls.len(), 1);
        assert!(!calls[0].3); // enabled=false
    }

    #[test]
    fn enabled_zero_is_disabled() {
        let mgr = MockRepoManager::new(vec![]);
        let add_calls = Arc::clone(&mgr.add_calls);
        let exec = RepositoryExecutor {
            repo_mgr: Box::new(mgr),
        };
        exec.execute(
            &make_params(&[
                ("repository", "test-repo"),
                ("repo", "https://example.com/repo"),
                ("enabled", "0"),
                ("state", "present"),
            ]),
            false,
        );
        let calls = add_calls.lock().unwrap();
        assert!(!calls[0].3);
    }

    #[test]
    fn enabled_no_is_disabled() {
        let mgr = MockRepoManager::new(vec![]);
        let add_calls = Arc::clone(&mgr.add_calls);
        let exec = RepositoryExecutor {
            repo_mgr: Box::new(mgr),
        };
        exec.execute(
            &make_params(&[
                ("repository", "test-repo"),
                ("repo", "https://example.com/repo"),
                ("enabled", "no"),
                ("state", "present"),
            ]),
            false,
        );
        let calls = add_calls.lock().unwrap();
        assert!(!calls[0].3);
    }

    #[test]
    fn enabled_default_is_true() {
        let mgr = MockRepoManager::new(vec![]);
        let add_calls = Arc::clone(&mgr.add_calls);
        let exec = RepositoryExecutor {
            repo_mgr: Box::new(mgr),
        };
        exec.execute(
            &make_params(&[
                ("repository", "test-repo"),
                ("repo", "https://example.com/repo"),
                ("state", "present"),
            ]),
            false,
        );
        let calls = add_calls.lock().unwrap();
        assert!(calls[0].3); // enabled=true (default)
    }

    #[test]
    fn enabled_change_triggers_drift() {
        // Repo exists with enabled content, but now we want disabled — should detect drift.
        let repo_line = "deb https://example.com/repo jammy stable";
        let mgr = MockRepoManager::with_content("test-repo", &format!("{}\n", repo_line));
        let add_calls = Arc::clone(&mgr.add_calls);
        let exec = RepositoryExecutor {
            repo_mgr: Box::new(mgr),
        };
        let result = exec.execute(
            &make_params(&[
                ("repository", "test-repo"),
                ("repo", repo_line),
                ("enabled", "false"),
                ("state", "present"),
            ]),
            false,
        );
        // desired_content with enabled=false produces "# deb ...\n" which differs.
        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);
        let calls = add_calls.lock().unwrap();
        assert!(!calls[0].3);
    }

    // -----------------------------------------------------------------------
    // key_fingerprint param tests
    // -----------------------------------------------------------------------

    #[test]
    fn key_fingerprint_passed_to_manager() {
        let mgr = MockRepoManager::new(vec![]);
        let add_calls = Arc::clone(&mgr.add_calls);
        let exec = RepositoryExecutor {
            repo_mgr: Box::new(mgr),
        };
        exec.execute(
            &make_params(&[
                ("repository", "test-repo"),
                ("repo", "deb https://example.com/repo jammy main"),
                ("key_url", "https://example.com/key.gpg"),
                ("key_fingerprint", "DEADBEEF1234"),
                ("state", "present"),
            ]),
            false,
        );
        let calls = add_calls.lock().unwrap();
        assert_eq!(calls[0].5, "DEADBEEF1234");
    }

    // -----------------------------------------------------------------------
    // DNF .repo file generation tests
    // -----------------------------------------------------------------------

    #[test]
    fn dnf_build_repo_from_url() {
        let mgr = DnfRepoManager {
            runner: Box::new(crate::executors::DefaultRunner),
        };
        let content = mgr.build_repo_content(
            "epel",
            "https://dl.fedoraproject.org/pub/epel/9/Everything/x86_64/",
            "https://dl.fedoraproject.org/pub/epel/RPM-GPG-KEY-EPEL-9",
            true,
        );
        assert!(content.starts_with("[epel]\n"));
        assert!(content.contains("name=epel\n"));
        assert!(content.contains(
            "baseurl=https://dl.fedoraproject.org/pub/epel/9/Everything/x86_64/\n"
        ));
        assert!(content.contains("enabled=1\n"));
        assert!(content.contains("gpgcheck=1\n"));
        assert!(content.contains(
            "gpgkey=https://dl.fedoraproject.org/pub/epel/RPM-GPG-KEY-EPEL-9\n"
        ));
    }

    #[test]
    fn dnf_build_repo_disabled() {
        let mgr = DnfRepoManager {
            runner: Box::new(crate::executors::DefaultRunner),
        };
        let content = mgr.build_repo_content(
            "testing",
            "https://example.com/repo",
            "",
            false,
        );
        assert!(content.contains("enabled=0\n"));
        assert!(content.contains("gpgcheck=0\n"));
    }

    #[test]
    fn dnf_build_repo_no_key() {
        let mgr = DnfRepoManager {
            runner: Box::new(crate::executors::DefaultRunner),
        };
        let content = mgr.build_repo_content("local", "https://example.com/repo", "", true);
        assert!(content.contains("gpgcheck=0\n"));
        assert!(!content.contains("gpgkey="));
    }

    #[test]
    fn dnf_passthrough_ini_content() {
        let mgr = DnfRepoManager {
            runner: Box::new(crate::executors::DefaultRunner),
        };
        let ini = "[custom-repo]\nname=Custom\nbaseurl=https://example.com\nenabled=1\n";
        let content = mgr.build_repo_content("custom-repo", ini, "", true);
        assert_eq!(content, ini);
    }

    // -----------------------------------------------------------------------
    // APT format tests
    // -----------------------------------------------------------------------

    #[test]
    fn apt_build_list_content_enabled() {
        let mgr = AptRepoManager {
            runner: Box::new(crate::executors::DefaultRunner),
        };
        let content = mgr.build_list_content(
            "deb https://download.docker.com/linux/ubuntu jammy stable",
            true,
        );
        assert_eq!(
            content,
            "deb https://download.docker.com/linux/ubuntu jammy stable\n"
        );
    }

    #[test]
    fn apt_build_list_content_disabled() {
        let mgr = AptRepoManager {
            runner: Box::new(crate::executors::DefaultRunner),
        };
        let content = mgr.build_list_content(
            "deb https://download.docker.com/linux/ubuntu jammy stable",
            false,
        );
        assert_eq!(
            content,
            "# deb https://download.docker.com/linux/ubuntu jammy stable\n"
        );
    }

    #[test]
    fn apt_build_deb822_basic() {
        let mgr = AptRepoManager {
            runner: Box::new(crate::executors::DefaultRunner),
        };
        let content = mgr.build_deb822_content(
            "docker-ce",
            "deb https://download.docker.com/linux/ubuntu jammy stable",
            true,
        );
        assert!(content.contains("Types: deb\n"));
        assert!(content.contains("URIs: https://download.docker.com/linux/ubuntu\n"));
        assert!(content.contains("Suites: jammy\n"));
        assert!(content.contains("Components: stable\n"));
        assert!(!content.contains("Enabled: no"));
    }

    #[test]
    fn apt_build_deb822_disabled() {
        let mgr = AptRepoManager {
            runner: Box::new(crate::executors::DefaultRunner),
        };
        let content = mgr.build_deb822_content(
            "docker-ce",
            "deb https://download.docker.com/linux/ubuntu jammy stable",
            false,
        );
        assert!(content.contains("Enabled: no\n"));
    }

    #[test]
    fn apt_build_deb822_with_arch_and_signed_by() {
        let mgr = AptRepoManager {
            runner: Box::new(crate::executors::DefaultRunner),
        };
        let content = mgr.build_deb822_content(
            "docker-ce",
            "deb [arch=amd64 signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu jammy stable",
            true,
        );
        assert!(content.contains("Architectures: amd64\n"));
        assert!(content.contains("Signed-By: /etc/apt/keyrings/docker.gpg\n"));
    }

    #[test]
    fn apt_build_deb822_deb_src() {
        let mgr = AptRepoManager {
            runner: Box::new(crate::executors::DefaultRunner),
        };
        let content = mgr.build_deb822_content(
            "test",
            "deb-src https://example.com/repo jammy main",
            true,
        );
        assert!(content.contains("Types: deb-src\n"));
        assert!(content.contains("URIs: https://example.com/repo\n"));
    }

    // -----------------------------------------------------------------------
    // APK backend tests
    // -----------------------------------------------------------------------

    #[test]
    fn apk_desired_content_enabled() {
        let mgr = ApkRepoManager {
            runner: Box::new(crate::executors::DefaultRunner),
        };
        let content = mgr.desired_content(
            "main",
            "https://dl-cdn.alpinelinux.org/alpine/v3.19/main",
            "",
            true,
            "",
        );
        assert_eq!(
            content,
            "https://dl-cdn.alpinelinux.org/alpine/v3.19/main"
        );
    }

    #[test]
    fn apk_desired_content_disabled() {
        let mgr = ApkRepoManager {
            runner: Box::new(crate::executors::DefaultRunner),
        };
        let content = mgr.desired_content(
            "main",
            "https://dl-cdn.alpinelinux.org/alpine/v3.19/main",
            "",
            false,
            "",
        );
        assert_eq!(
            content,
            "# https://dl-cdn.alpinelinux.org/alpine/v3.19/main"
        );
    }

    // -----------------------------------------------------------------------
    // Pacman backend tests
    // -----------------------------------------------------------------------

    #[test]
    fn pacman_build_section_enabled() {
        let section = PacmanRepoManager::build_section(
            "multilib",
            "https://mirror.example.com/$repo/os/$arch",
            true,
        );
        assert_eq!(
            section,
            "[multilib]\nServer = https://mirror.example.com/$repo/os/$arch\n"
        );
    }

    #[test]
    fn pacman_build_section_disabled() {
        let section = PacmanRepoManager::build_section(
            "multilib",
            "https://mirror.example.com/$repo/os/$arch",
            false,
        );
        assert_eq!(
            section,
            "# [multilib]\n# Server = https://mirror.example.com/$repo/os/$arch\n"
        );
    }

    #[test]
    fn pacman_find_section_exists() {
        let conf = "[options]\nHoldPkg = pacman glibc\n\n[core]\nServer = https://mirror.example.com/$repo/os/$arch\n\n[extra]\nServer = https://mirror.example.com/$repo/os/$arch\n";
        let lines: Vec<&str> = conf.lines().collect();
        let result = PacmanRepoManager::find_section(&lines, "core");
        assert!(result.is_some());
        let (start, end) = result.unwrap();
        assert_eq!(lines[start].trim(), "[core]");
        // end should be the line with [extra]
        assert!(lines[end].trim().starts_with("[extra]"));
    }

    #[test]
    fn pacman_find_section_missing() {
        let conf = "[options]\nHoldPkg = pacman glibc\n";
        let lines: Vec<&str> = conf.lines().collect();
        let result = PacmanRepoManager::find_section(&lines, "multilib");
        assert!(result.is_none());
    }

    // -----------------------------------------------------------------------
    // OS family routing tests
    // -----------------------------------------------------------------------

    #[test]
    fn for_os_routes_correctly() {
        // Just verify it doesn't panic for each supported family.
        let _ = RepositoryExecutor::for_os("debian");
        let _ = RepositoryExecutor::for_os("redhat");
        let _ = RepositoryExecutor::for_os("fedora");
        let _ = RepositoryExecutor::for_os("suse");
        let _ = RepositoryExecutor::for_os("opensuse");
        let _ = RepositoryExecutor::for_os("alpine");
        let _ = RepositoryExecutor::for_os("arch");
        let _ = RepositoryExecutor::for_os("archlinux");
        let _ = RepositoryExecutor::for_os("manjaro");
        let _ = RepositoryExecutor::for_os("unknown");
    }

    #[test]
    fn unknown_state_errors() {
        let mgr = MockRepoManager::new(vec![]);
        let exec = RepositoryExecutor {
            repo_mgr: Box::new(mgr),
        };
        let result = exec.execute(
            &make_params(&[
                ("repository", "test"),
                ("repo", "https://example.com/repo"),
                ("state", "bogus"),
            ]),
            false,
        );
        assert!(result.error.is_some());
        assert!(result.error.as_deref().unwrap().contains("unknown state"));
    }

    // -----------------------------------------------------------------------
    // Format param routing
    // -----------------------------------------------------------------------

    #[test]
    fn format_param_passed_to_manager() {
        let mgr = MockRepoManager::new(vec![]);
        let add_calls = Arc::clone(&mgr.add_calls);
        let exec = RepositoryExecutor {
            repo_mgr: Box::new(mgr),
        };
        exec.execute(
            &make_params(&[
                ("repository", "test-repo"),
                ("repo", "deb https://example.com/repo jammy main"),
                ("format", "deb822"),
                ("state", "present"),
            ]),
            false,
        );
        let calls = add_calls.lock().unwrap();
        assert_eq!(calls[0].4, "deb822");
    }

    // -----------------------------------------------------------------------
    // Regression: repo_already_present with content check
    // -----------------------------------------------------------------------

    #[test]
    fn repo_already_present_with_matching_content() {
        // This is the updated version of the old "repo_already_present" test.
        // Now that we check content, we need the mock to return matching content.
        let repo_line = "deb https://download.docker.com/linux/ubuntu jammy stable";
        let mgr = MockRepoManager::with_content("docker-ce", &format!("{}\n", repo_line));
        let exec = RepositoryExecutor {
            repo_mgr: Box::new(mgr),
        };
        let result = exec.execute(
            &make_params(&[
                ("repository", "docker-ce"),
                ("state", "present"),
                ("repo", repo_line),
            ]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());
    }
}
