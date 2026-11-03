// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Package source audit trait collector (Linux-only).
//!
//! Enumerates configured package-manager sources (apt `sources.list` +
//! `sources.list.d/`, yum/dnf `yum.repos.d/`) and emits per-source rows
//! with type, url, suite, components, and enabled status. URL userinfo
//! (`https://user:pass@host/...`) is stripped before emission so
//! authentication material never appears in trait payloads.
//!
//! Server-side allowlist filtering is handled by a separate computed
//! field; this collector emits the raw source inventory only.

use anyhow::Result;
use serde_json::{json, Value};
use std::path::Path;

/// Enumerate package sources under the default Linux roots and return the
/// top-level `{"sources": [...]}` JSON shape.
pub fn collect() -> Result<Value> {
    let sources = collect_from_root("/");
    Ok(json!({ "sources": sources }))
}

/// Walk apt + yum/dnf source configuration under `root` and return a
/// deterministic per-source row list. `root` is overridable so tests can
/// point at a tempdir fixture.
fn collect_from_root(root: &str) -> Vec<Value> {
    let mut out = Vec::new();
    let root = Path::new(root);

    // apt: /etc/apt/sources.list (one-line format)
    read_apt_list(root.join("etc/apt/sources.list").as_path(), &mut out);

    // apt: /etc/apt/sources.list.d/*.list (one-line) and *.sources (deb822).
    let sources_list_d = root.join("etc/apt/sources.list.d");
    if let Ok(entries) = std::fs::read_dir(&sources_list_d) {
        let mut paths: Vec<_> = entries.flatten().map(|e| e.path()).collect();
        paths.sort();
        for path in paths {
            match path.extension().and_then(|e| e.to_str()) {
                Some("list") => read_apt_list(&path, &mut out),
                Some("sources") => read_apt_deb822(&path, &mut out),
                _ => {}
            }
        }
    }

    // yum/dnf: /etc/yum.repos.d/*.repo (INI format)
    let yum_repos_d = root.join("etc/yum.repos.d");
    if let Ok(entries) = std::fs::read_dir(&yum_repos_d) {
        let mut paths: Vec<_> = entries.flatten().map(|e| e.path()).collect();
        paths.sort();
        for path in paths {
            if path.extension().and_then(|e| e.to_str()) == Some("repo") {
                read_yum_repo(&path, &mut out);
            }
        }
    }

    out
}

/// Parse a classic apt `sources.list` style file. Each non-comment,
/// non-blank line is either `deb URL SUITE COMPONENTS...` or
/// `deb-src URL SUITE COMPONENTS...`. Trailing options in `[...]` are
/// ignored for the trait (kept only as the source file location).
fn read_apt_list(path: &Path, out: &mut Vec<Value>) {
    let content = match std::fs::read_to_string(path) {
        Ok(c) => c,
        Err(_) => return,
    };
    let path_str = path.to_string_lossy().to_string();

    for (i, raw) in content.lines().enumerate() {
        let line_no = i + 1;
        let trimmed = raw.trim();
        if trimmed.is_empty() || trimmed.starts_with('#') {
            continue;
        }

        let tokens = apt_list_tokens(trimmed);
        if tokens.len() < 3 {
            continue;
        }
        let kind = tokens[0];
        if kind != "deb" && kind != "deb-src" {
            continue;
        }

        let url = strip_url_userinfo(tokens[1]);
        let suite = tokens[2].to_string();
        let components = if tokens.len() > 3 {
            tokens[3..].join(" ")
        } else {
            String::new()
        };

        out.push(json!({
            "type": "apt",
            "kind": kind,
            "file": path_str,
            "line": line_no,
            "url": url,
            "suite": suite,
            "components": components,
            "enabled": true,
        }));
    }
}

/// Tokenize an apt sources.list line, skipping bracketed option blocks
/// (`[arch=amd64 signed-by=/usr/share/keyrings/...]`). Returns the
/// remaining whitespace-separated tokens: kind, url, suite, components…
fn apt_list_tokens(line: &str) -> Vec<&str> {
    let mut out = Vec::new();
    let mut in_brackets = false;
    let mut start: Option<usize> = None;

    for (i, c) in line.char_indices() {
        if in_brackets {
            if c == ']' {
                in_brackets = false;
            }
            continue;
        }
        match c {
            '[' => {
                in_brackets = true;
                if let Some(s) = start.take() {
                    out.push(&line[s..i]);
                }
            }
            c if c.is_whitespace() => {
                if let Some(s) = start.take() {
                    out.push(&line[s..i]);
                }
            }
            _ => {
                if start.is_none() {
                    start = Some(i);
                }
            }
        }
    }
    if let Some(s) = start {
        out.push(&line[s..]);
    }
    out
}

/// Parse the deb822 `.sources` format: stanzas separated by blank lines,
/// each key-value pair on its own line (`URIs: https://...`). Each stanza
/// yields one trait row per URI × Suite combination (flattened if either
/// field has multiple values, which deb822 permits).
fn read_apt_deb822(path: &Path, out: &mut Vec<Value>) {
    let content = match std::fs::read_to_string(path) {
        Ok(c) => c,
        Err(_) => return,
    };
    let path_str = path.to_string_lossy().to_string();

    for stanza in content.split("\n\n") {
        let mut types: Vec<String> = Vec::new();
        let mut uris: Vec<String> = Vec::new();
        let mut suites: Vec<String> = Vec::new();
        let mut components: Vec<String> = Vec::new();
        let mut enabled = true;

        for line in stanza.lines() {
            let trimmed = line.trim();
            if trimmed.is_empty() || trimmed.starts_with('#') {
                continue;
            }
            let (key, value) = match trimmed.split_once(':') {
                Some((k, v)) => (k.trim().to_ascii_lowercase(), v.trim().to_string()),
                None => continue,
            };
            match key.as_str() {
                "types" => types = value.split_whitespace().map(str::to_string).collect(),
                "uris" => uris = value.split_whitespace().map(str::to_string).collect(),
                "suites" => suites = value.split_whitespace().map(str::to_string).collect(),
                "components" => {
                    components = value.split_whitespace().map(str::to_string).collect()
                }
                "enabled" => {
                    enabled = !matches!(value.to_ascii_lowercase().as_str(), "no" | "false" | "0")
                }
                _ => {}
            }
        }

        if uris.is_empty() {
            continue;
        }
        let kind = types.first().cloned().unwrap_or_else(|| "deb".into());
        let components_joined = components.join(" ");

        for uri in &uris {
            for suite in &suites {
                out.push(json!({
                    "type": "apt",
                    "kind": kind,
                    "file": path_str,
                    "line": 0,
                    "url": strip_url_userinfo(uri),
                    "suite": suite,
                    "components": components_joined,
                    "enabled": enabled,
                }));
            }
            if suites.is_empty() {
                out.push(json!({
                    "type": "apt",
                    "kind": kind,
                    "file": path_str,
                    "line": 0,
                    "url": strip_url_userinfo(uri),
                    "suite": "",
                    "components": components_joined,
                    "enabled": enabled,
                }));
            }
        }
    }
}

/// Parse a yum/dnf `.repo` INI file. Each `[section]` header begins a new
/// repo entry; `baseurl=` or `mirrorlist=`/`metalink=` become the URL.
/// `enabled=0` disables the repo; any other value (including absent) is
/// considered enabled, matching yum's default.
fn read_yum_repo(path: &Path, out: &mut Vec<Value>) {
    let content = match std::fs::read_to_string(path) {
        Ok(c) => c,
        Err(_) => return,
    };
    let path_str = path.to_string_lossy().to_string();

    let mut current_id: Option<String> = None;
    let mut current_name = String::new();
    let mut current_url = String::new();
    let mut current_enabled = true;

    let flush = |id: &Option<String>,
                 name: &str,
                 url: &str,
                 enabled: bool,
                 out: &mut Vec<Value>,
                 path: &str| {
        if let Some(id) = id {
            out.push(json!({
                "type": "yum",
                "file": path,
                "id": id,
                "name": name,
                "url": strip_url_userinfo(url),
                "enabled": enabled,
            }));
        }
    };

    for line in content.lines() {
        let trimmed = line.trim();
        if trimmed.is_empty() || trimmed.starts_with('#') || trimmed.starts_with(';') {
            continue;
        }
        if let Some(stripped) = trimmed.strip_prefix('[').and_then(|s| s.strip_suffix(']')) {
            flush(
                &current_id,
                &current_name,
                &current_url,
                current_enabled,
                out,
                &path_str,
            );
            current_id = Some(stripped.to_string());
            current_name = String::new();
            current_url = String::new();
            current_enabled = true;
            continue;
        }
        if let Some((key, value)) = trimmed.split_once('=') {
            let key = key.trim().to_ascii_lowercase();
            let value = value.trim();
            match key.as_str() {
                "name" => current_name = value.to_string(),
                "baseurl" | "mirrorlist" | "metalink" => {
                    if current_url.is_empty() {
                        current_url = value.to_string();
                    }
                }
                "enabled" => {
                    current_enabled = !matches!(value, "0" | "false" | "no" | "False" | "No")
                }
                _ => {}
            }
        }
    }
    flush(
        &current_id,
        &current_name,
        &current_url,
        current_enabled,
        out,
        &path_str,
    );
}

/// Strip `user:password@` from the authority portion of a URL so
/// credentials embedded in apt/yum source lines never appear in trait
/// payloads. Handles schemes (`http://`, `https://`) and leaves
/// scheme-less values alone.
fn strip_url_userinfo(url: &str) -> String {
    let split = url.find("://");
    if split.is_none() {
        return url.to_string();
    }
    let (scheme, rest) = url.split_at(split.unwrap() + 3);
    // Userinfo ends at the first '@', before any '/'. '@' after the first
    // '/' is part of the path and must not be trimmed.
    let authority_end = rest.find('/').unwrap_or(rest.len());
    let authority = &rest[..authority_end];
    let path = &rest[authority_end..];
    match authority.find('@') {
        Some(at) => format!("{}{}{}", scheme, &authority[at + 1..], path),
        None => url.to_string(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;

    #[test]
    fn test_collect_returns_sources_key() {
        let val = collect().unwrap();
        let obj = val.as_object().unwrap();
        assert!(obj.contains_key("sources"));
        assert!(obj["sources"].is_array());
    }

    #[test]
    fn test_strip_url_userinfo() {
        assert_eq!(
            strip_url_userinfo("https://user:pass@repo.example.com/ubuntu"),
            "https://repo.example.com/ubuntu"
        );
        assert_eq!(
            strip_url_userinfo("https://repo.example.com/ubuntu"),
            "https://repo.example.com/ubuntu"
        );
        // '@' in path must be preserved.
        assert_eq!(
            strip_url_userinfo("https://repo.example.com/x/y@z"),
            "https://repo.example.com/x/y@z"
        );
        // Scheme-less is passed through.
        assert_eq!(
            strip_url_userinfo("repo.example.com/ubuntu"),
            "repo.example.com/ubuntu"
        );
    }

    #[test]
    fn test_apt_list_parse() {
        let dir = tempfile::tempdir().unwrap();
        let apt = dir.path().join("etc/apt");
        fs::create_dir_all(&apt).unwrap();
        fs::write(
            apt.join("sources.list"),
            "\
# Default Ubuntu
deb http://archive.ubuntu.com/ubuntu jammy main restricted
deb-src http://archive.ubuntu.com/ubuntu jammy main restricted

# Credentials that must be stripped
deb [arch=amd64] https://user:pass@private.example.com/ubuntu jammy main
",
        )
        .unwrap();

        let sources =
            collect_from_root(dir.path().to_str().unwrap());
        assert_eq!(sources.len(), 3);

        assert_eq!(sources[0]["kind"], "deb");
        assert_eq!(sources[0]["url"], "http://archive.ubuntu.com/ubuntu");
        assert_eq!(sources[0]["suite"], "jammy");
        assert_eq!(sources[0]["components"], "main restricted");

        assert_eq!(sources[1]["kind"], "deb-src");

        // Credentials stripped.
        assert_eq!(sources[2]["url"], "https://private.example.com/ubuntu");
    }

    #[test]
    fn test_apt_deb822_parse() {
        let dir = tempfile::tempdir().unwrap();
        let d = dir.path().join("etc/apt/sources.list.d");
        fs::create_dir_all(&d).unwrap();
        fs::write(
            d.join("ubuntu.sources"),
            "\
Types: deb
URIs: http://archive.ubuntu.com/ubuntu
Suites: jammy jammy-updates
Components: main universe
Signed-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg
",
        )
        .unwrap();

        let sources = collect_from_root(dir.path().to_str().unwrap());
        assert_eq!(sources.len(), 2);
        assert_eq!(sources[0]["suite"], "jammy");
        assert_eq!(sources[1]["suite"], "jammy-updates");
        assert_eq!(sources[0]["components"], "main universe");
    }

    #[test]
    fn test_yum_repo_parse() {
        let dir = tempfile::tempdir().unwrap();
        let d = dir.path().join("etc/yum.repos.d");
        fs::create_dir_all(&d).unwrap();
        fs::write(
            d.join("docker-ce.repo"),
            "\
[docker-ce-stable]
name=Docker CE Stable - $basearch
baseurl=https://download.docker.com/linux/centos/$releasever/$basearch/stable
enabled=1
gpgcheck=1

[docker-ce-test]
name=Docker CE Test
baseurl=https://auth:secret@download.docker.com/linux/centos/$releasever/$basearch/test
enabled=0
",
        )
        .unwrap();

        let sources = collect_from_root(dir.path().to_str().unwrap());
        assert_eq!(sources.len(), 2);
        assert_eq!(sources[0]["id"], "docker-ce-stable");
        assert_eq!(sources[0]["enabled"], true);
        assert_eq!(sources[1]["id"], "docker-ce-test");
        assert_eq!(sources[1]["enabled"], false);
        // Credentials stripped.
        assert_eq!(
            sources[1]["url"],
            "https://download.docker.com/linux/centos/$releasever/$basearch/test"
        );
    }
}
