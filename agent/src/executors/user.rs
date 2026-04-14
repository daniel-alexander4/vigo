// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! User executor — manages system users idempotently via useradd/usermod/userdel.

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use std::collections::HashMap;
use std::io::{BufRead, BufReader};
use std::time::Instant;

pub struct UserExecutor {
    pub runner: Box<dyn CommandRunner>,
    pub passwd_path: String,
    pub shadow_path: String,
    pub sudoers_dir: String,
}

struct PasswdEntry {
    uid: String,
    _gid: String,
    gecos: String,
    home: String,
    shell: String,
}

impl UserExecutor {
    pub fn new() -> Self {
        Self {
            runner: Box::new(DefaultRunner),
            sudoers_dir: "/etc/sudoers.d".into(),
            passwd_path: "/etc/passwd".into(),
            shadow_path: "/etc/shadow".into(),
        }
    }

    fn lookup_user(&self, name: &str) -> Result<Option<PasswdEntry>, String> {
        let f = std::fs::File::open(&self.passwd_path)
            .map_err(|e| format!("open passwd: {}", e))?;

        for line in BufReader::new(f).lines().flatten() {
            let fields: Vec<&str> = line.split(':').collect();
            if fields.len() >= 7 && fields[0] == name {
                return Ok(Some(PasswdEntry {
                    uid: fields[2].to_string(),
                    _gid: fields[3].to_string(),
                    gecos: fields[4].to_string(),
                    home: fields[5].to_string(),
                    shell: fields[6].to_string(),
                }));
            }
        }
        Ok(None)
    }

    fn lookup_shadow_hash(&self, name: &str) -> Result<String, String> {
        let f = match std::fs::File::open(&self.shadow_path) {
            Ok(f) => f,
            Err(e) => return Err(format!("open shadow: {}", e)),
        };
        for line in BufReader::new(f).lines().flatten() {
            let fields: Vec<&str> = line.splitn(3, ':').collect();
            if fields.len() >= 2 && fields[0] == name {
                return Ok(fields[1].to_string());
            }
        }
        Ok(String::new())
    }

    fn current_groups(&self, name: &str) -> Vec<String> {
        match self.runner.run("id", &["-nG", name]) {
            Ok(o) if o.status.success() => {
                String::from_utf8_lossy(&o.stdout)
                    .split_whitespace()
                    .map(|s| s.to_string())
                    .collect()
            }
            _ => Vec::new(),
        }
    }

    /// Normalize a `groups:` attribute value to a comma-joined string.
    ///
    /// Accepts two on-the-wire forms:
    ///   - Comma-separated string  — `"plex, docker"` (legacy, from YAML string form)
    ///   - JSON array              — `r#"["plex","docker"]"#` (from YAML list form,
    ///     which the server serializes via json.Marshal when building the policy bundle)
    ///
    /// Returns a plain comma-joined string suitable for `usermod -aG` and
    /// `split(',')` consumers. Whitespace around group names is trimmed;
    /// empty entries are dropped.
    fn normalize_groups(raw: &str) -> String {
        let s = raw.trim();
        if s.is_empty() {
            return String::new();
        }
        // JSON array form: parse and rejoin.
        if s.starts_with('[') && s.ends_with(']') {
            if let Ok(v) = serde_json::from_str::<Vec<serde_json::Value>>(s) {
                let items: Vec<String> = v
                    .into_iter()
                    .filter_map(|item| match item {
                        serde_json::Value::String(g) => Some(g),
                        other => Some(other.to_string()),
                    })
                    .map(|g| g.trim().to_string())
                    .filter(|g| !g.is_empty())
                    .collect();
                return items.join(",");
            }
            // Fall through — malformed JSON, let the comma path handle it.
        }
        // Comma-separated form: trim each entry, drop empties.
        s.split(',')
            .map(|g| g.trim())
            .filter(|g| !g.is_empty())
            .collect::<Vec<_>>()
            .join(",")
    }

    fn resolve_home(name: &str, params: &HashMap<String, String>, entry: Option<&PasswdEntry>) -> String {
        if let Some(h) = params.get("home") {
            if !h.is_empty() {
                return h.clone();
            }
        }
        if let Some(e) = entry {
            if !e.home.is_empty() {
                return e.home.clone();
            }
        }
        format!("/home/{}", name)
    }

    fn build_useradd_args(name: &str, params: &HashMap<String, String>) -> Vec<String> {
        let mut args = Vec::new();
        let system = params.get("system").map(|s| s == "true").unwrap_or(false);

        if system {
            args.push("--system".into());
        }
        for (flag, key) in [("-u", "uid"), ("-s", "shell"), ("-d", "home"), ("-c", "comment"), ("-g", "group"), ("-p", "password"), ("--expiredate", "expiredate")] {
            if let Some(v) = params.get(key) {
                if !v.is_empty() {
                    args.push(flag.into());
                    args.push(v.clone());
                }
            }
        }
        let manage_home = params.get("manage_home").map(|s| s != "false").unwrap_or(true);
        if !system && manage_home {
            args.push("-m".into()); // create home dir
        }
        args.push(name.into());
        args
    }

    /// Map a desktop name to the exec command for ~/.xsession.
    fn session_exec(session: &str) -> Option<&'static str> {
        match session.to_lowercase().as_str() {
            "cinnamon" => Some("cinnamon-session"),
            "gnome" => Some("gnome-session"),
            "xfce" | "xfce4" => Some("startxfce4"),
            "mate" => Some("mate-session"),
            "plasma" | "kde" => Some("startplasma-x11"),
            "lxde" => Some("startlxde"),
            "lxqt" => Some("startlxqt"),
            "fvwm" => Some("fvwm"),
            "i3" => Some("i3"),
            "sway" => Some("sway"),
            "openbox" => Some("openbox-session"),
            "budgie" => Some("budgie-desktop"),
            _ => None,
        }
    }

    fn write_xsession(&self, name: &str, home: &str, exec_cmd: &str) -> Result<(), String> {
        let path = format!("{}/.xsession", home);
        let content = format!("#!/bin/sh\nexec {}\n", exec_cmd);
        std::fs::write(&path, &content)
            .map_err(|e| format!("write {}: {}", path, e))?;

        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let _ = std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o755));
        }

        match self.runner.run("chown", &[&format!("{}:", name), &path]) {
            Ok(o) if !o.status.success() => {
                let stderr = String::from_utf8_lossy(&o.stderr);
                Err(format!("chown {}: {}", path, stderr))
            }
            Err(e) => Err(format!("chown {}: {}", path, e)),
            _ => Ok(()),
        }
    }

    fn write_authorized_keys(&self, name: &str, home: &str, keys: &str) -> Result<(), String> {
        let ssh_dir = format!("{}/.ssh", home);
        std::fs::create_dir_all(&ssh_dir)
            .map_err(|e| format!("mkdir {}: {}", ssh_dir, e))?;

        let ak_path = format!("{}/authorized_keys", ssh_dir);

        // Remove symlinks for security.
        if let Ok(meta) = std::fs::symlink_metadata(&ak_path) {
            if meta.file_type().is_symlink() {
                std::fs::remove_file(&ak_path)
                    .map_err(|e| format!("remove symlink {}: {}", ak_path, e))?;
            }
        }

        // Preserve any active Scrier ephemeral keys so convergence doesn't
        // break in-progress remote access sessions.
        let marker = crate::tunnel::sshauth::EPHEMERAL_MARKER;
        let mut content = keys.to_string();
        if let Ok(existing) = std::fs::read_to_string(&ak_path) {
            for line in existing.lines() {
                if line.contains(marker) {
                    if !content.ends_with('\n') {
                        content.push('\n');
                    }
                    content.push_str(line);
                    content.push('\n');
                }
            }
        }

        std::fs::write(&ak_path, &content)
            .map_err(|e| format!("write {}: {}", ak_path, e))?;

        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let _ = std::fs::set_permissions(&ak_path, std::fs::Permissions::from_mode(0o600));
            let _ = std::fs::set_permissions(&ssh_dir, std::fs::Permissions::from_mode(0o700));
        }

        match self.runner.run("chown", &["-R", &format!("{}:", name), &ssh_dir]) {
            Ok(o) if !o.status.success() => {
                let stderr = String::from_utf8_lossy(&o.stderr);
                Err(format!("chown {}: {}", ssh_dir, stderr))
            }
            Err(e) => Err(format!("chown {}: {}", ssh_dir, e)),
            _ => Ok(()),
        }
    }

    fn desired_sudoers_content(name: &str, params: &HashMap<String, String>) -> Result<Option<String>, String> {
        let sudo_passwd = params.get("sudo_passwd").map(|s| s == "true").unwrap_or(false);
        let sudo_nopasswd = params.get("sudo_nopasswd").map(|s| s == "true").unwrap_or(false);

        if sudo_passwd && sudo_nopasswd {
            return Err("sudo_passwd and sudo_nopasswd are mutually exclusive".into());
        }

        if sudo_passwd {
            Ok(Some(format!("{} ALL=(ALL) ALL\n", name)))
        } else if sudo_nopasswd {
            Ok(Some(format!("{} ALL=(ALL) NOPASSWD: ALL\n", name)))
        } else {
            Ok(None)
        }
    }

    fn sudoers_path(&self, name: &str) -> String {
        format!("{}/{}", self.sudoers_dir, name)
    }

    fn apply_sudoers(&self, name: &str, params: &HashMap<String, String>) -> Result<bool, String> {
        let desired = Self::desired_sudoers_content(name, params)?;
        let path = self.sudoers_path(name);

        match desired {
            Some(content) => {
                // Check if already correct.
                if let Ok(existing) = std::fs::read_to_string(&path) {
                    if existing == content {
                        return Ok(false);
                    }
                }
                std::fs::write(&path, &content)
                    .map_err(|e| format!("writing {}: {}", path, e))?;
                #[cfg(unix)]
                {
                    use std::os::unix::fs::PermissionsExt;
                    let _ = std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o440));
                }
                Ok(true)
            }
            None => {
                // No sudo requested — remove file if it exists.
                if std::fs::metadata(&path).is_ok() {
                    std::fs::remove_file(&path)
                        .map_err(|e| format!("removing {}: {}", path, e))?;
                    return Ok(true);
                }
                Ok(false)
            }
        }
    }

    fn remove_sudoers(&self, name: &str) {
        let path = self.sudoers_path(name);
        let _ = std::fs::remove_file(&path);
    }

    fn find_available_system_uid(&self) -> Result<i32, String> {
        let f = std::fs::File::open(&self.passwd_path)
            .map_err(|e| format!("open passwd: {}", e))?;

        let mut used = std::collections::HashSet::new();
        for line in BufReader::new(f).lines().flatten() {
            let fields: Vec<&str> = line.split(':').collect();
            if fields.len() >= 3 {
                if let Ok(uid) = fields[2].parse::<i32>() {
                    used.insert(uid);
                }
            }
        }

        for uid in (100..=999).rev() {
            if !used.contains(&uid) {
                return Ok(uid);
            }
        }
        Err("no available system UID in range 100-999".into())
    }
}

impl Executor for UserExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let name = params
            .get("username")
            .cloned()
            .unwrap_or_default();

        let mut result = ResourceResult::new("user", &name);

        if name.is_empty() {
            result.error = Some("user executor: username param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        // Validate sudo params early.
        if let Err(e) = Self::desired_sudoers_content(&name, params) {
            result.error = Some(format!("user executor: {}", e));
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");

        let entry = match self.lookup_user(&name) {
            Ok(e) => e,
            Err(e) => {
                result.error = Some(e);
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
        };
        let exists = entry.is_some();

        match state {
            "present" => {
                if !exists {
                    // Create user.
                    result.action = Action::Created;
                    result.changed = true;

                    if dry_run {
                        result.duration_ms = start.elapsed().as_millis() as i32;
                        return result;
                    }

                    let args = Self::build_useradd_args(&name, params);
                    let arg_refs: Vec<&str> = args.iter().map(|s| s.as_str()).collect();
                    match self.runner.run("useradd", &arg_refs) {
                        Ok(o) if !o.status.success() => {
                            let stderr = String::from_utf8_lossy(&o.stderr);
                            result.error = Some(format!("useradd {}: {}", name, stderr));
                            result.duration_ms = start.elapsed().as_millis() as i32;
                            return result;
                        }
                        Err(e) => {
                            result.error = Some(format!("useradd {}: {}", name, e));
                            result.duration_ms = start.elapsed().as_millis() as i32;
                            return result;
                        }
                        _ => {}
                    }

                    // Password max age.
                    if let Some(max_age) = params.get("password_max_age") {
                        if !max_age.is_empty() {
                            match self.runner.run("chage", &["-M", max_age, &name]) {
                                Ok(o) if !o.status.success() => {
                                    let stderr = String::from_utf8_lossy(&o.stderr);
                                    result.error = Some(format!("chage -M: {}", stderr));
                                    result.duration_ms = start.elapsed().as_millis() as i32;
                                    return result;
                                }
                                Err(e) => {
                                    result.error = Some(format!("chage -M: {}", e));
                                    result.duration_ms = start.elapsed().as_millis() as i32;
                                    return result;
                                }
                                _ => {}
                            }
                        }
                    }

                    // Supplementary groups.
                    if let Some(groups_raw) = params.get("groups") {
                        let normalized = Self::normalize_groups(groups_raw);
                        if !normalized.is_empty() {
                            match self.runner.run("usermod", &["-aG", &normalized, &name]) {
                                Ok(o) if !o.status.success() => {
                                    let stderr = String::from_utf8_lossy(&o.stderr);
                                    result.error = Some(format!("usermod -aG: {}", stderr));
                                    result.duration_ms = start.elapsed().as_millis() as i32;
                                    return result;
                                }
                                Err(e) => {
                                    result.error = Some(format!("usermod -aG: {}", e));
                                    result.duration_ms = start.elapsed().as_millis() as i32;
                                    return result;
                                }
                                _ => {}
                            }
                        }
                    }

                    // Authorized keys.
                    if let Some(keys) = params.get("authorized_keys") {
                        if !keys.is_empty() {
                            let home = match self.lookup_user(&name) {
                                Ok(e) => Self::resolve_home(&name, params, e.as_ref()),
                                Err(_) => Self::resolve_home(&name, params, None),
                            };
                            if let Err(e) = self.write_authorized_keys(&name, &home, keys) {
                                result.error = Some(e);
                            }
                        }
                    }

                    // Desktop session (.xsession for xrdp).
                    if let Some(ds) = params.get("desktop") {
                        if !ds.is_empty() {
                            if let Some(exec_cmd) = Self::session_exec(ds) {
                                let home = match self.lookup_user(&name) {
                                    Ok(e) => Self::resolve_home(&name, params, e.as_ref()),
                                    Err(_) => Self::resolve_home(&name, params, None),
                                };
                                if let Err(e) = self.write_xsession(&name, &home, exec_cmd) {
                                    result.error = Some(e);
                                }
                            } else {
                                result.error = Some(format!("unknown desktop {:?} — use cinnamon, gnome, xfce, mate, plasma, fvwm, i3, etc.", ds));
                            }
                        }
                    }

                    // Sudoers.
                    if let Err(e) = self.apply_sudoers(&name, params) {
                        result.error = Some(e);
                    }
                } else {
                    // Update existing user.
                    let entry = entry.as_ref().unwrap();
                    let mut needs_update = false;
                    let mut mod_args: Vec<String> = Vec::new();

                    if let Some(uid) = params.get("uid") {
                        if !uid.is_empty() && *uid != entry.uid {
                            needs_update = true;
                            mod_args.extend(["-u".into(), uid.clone()]);
                        }
                    }

                    // System user UID reassignment.
                    if params.get("system").map(|s| s == "true").unwrap_or(false) {
                        if let Ok(current_uid) = entry.uid.parse::<i32>() {
                            if current_uid >= 1000 {
                                match self.find_available_system_uid() {
                                    Ok(new_uid) => {
                                        needs_update = true;
                                        mod_args.extend(["-u".into(), new_uid.to_string()]);
                                    }
                                    Err(e) => {
                                        result.error = Some(format!(
                                            "system user {} has non-system UID {}: {}",
                                            name, entry.uid, e
                                        ));
                                        result.duration_ms = start.elapsed().as_millis() as i32;
                                        return result;
                                    }
                                }
                            }
                        }
                    }

                    for (flag, key, field) in [
                        ("-s", "shell", &entry.shell),
                        ("-d", "home", &entry.home),
                        ("-c", "comment", &entry.gecos),
                    ] {
                        if let Some(v) = params.get(key) {
                            if !v.is_empty() && v != field {
                                needs_update = true;
                                mod_args.extend([flag.into(), v.clone()]);
                            }
                        }
                    }

                    if let Some(group) = params.get("group") {
                        if !group.is_empty() {
                            needs_update = true;
                            mod_args.extend(["-g".into(), group.clone()]);
                        }
                    }

                    if let Some(expiredate) = params.get("expiredate") {
                        if !expiredate.is_empty() {
                            needs_update = true;
                            mod_args.extend(["--expiredate".into(), expiredate.clone()]);
                        }
                    }

                    // Check supplementary groups.
                    let purge_groups = params.get("purge_groups").map(|s| s == "true").unwrap_or(false);
                    let mut groups_to_add = Vec::new();
                    let mut groups_to_purge = false;
                    if let Some(groups_raw) = params.get("groups") {
                        let normalized = Self::normalize_groups(groups_raw);
                        if !normalized.is_empty() {
                            let desired: Vec<&str> = normalized.split(',').map(|s| s.trim()).filter(|s| !s.is_empty()).collect();
                            let current = self.current_groups(&name);
                            for g in &desired {
                                if !current.iter().any(|c| c == g) {
                                    groups_to_add.push(g.to_string());
                                }
                            }
                            if purge_groups {
                                // Check if user has extra groups not in the desired list.
                                // Exclude the user's primary group (same as username) from purge check.
                                for c in &current {
                                    if c != &name && !desired.iter().any(|d| d == c) {
                                        groups_to_purge = true;
                                        break;
                                    }
                                }
                            }
                            if !groups_to_add.is_empty() || groups_to_purge {
                                needs_update = true;
                            }
                        }
                    }

                    // Check password.
                    let mut password_changed = false;
                    let mut desired_password = String::new();
                    if let Some(pw) = params.get("password") {
                        if !pw.is_empty() {
                            desired_password = pw.clone();
                            match self.lookup_shadow_hash(&name) {
                                Ok(current_hash) => {
                                    if current_hash != *pw {
                                        password_changed = true;
                                        needs_update = true;
                                    }
                                }
                                Err(e) => {
                                    result.error = Some(e);
                                    result.duration_ms = start.elapsed().as_millis() as i32;
                                    return result;
                                }
                            }
                        }
                    }

                    // Check password_max_age (always apply if set — chage is idempotent).
                    if let Some(max_age) = params.get("password_max_age") {
                        if !max_age.is_empty() {
                            needs_update = true;
                        }
                    }

                    // Check authorized_keys. Strip Scrier ephemeral keys before
                    // comparing so active remote sessions don't trigger spurious updates.
                    let mut keys_changed = false;
                    let mut desired_keys = String::new();
                    if let Some(keys) = params.get("authorized_keys") {
                        if !keys.is_empty() {
                            desired_keys = keys.clone();
                            let home = Self::resolve_home(&name, params, Some(entry));
                            let ak_path = format!("{}/.ssh/authorized_keys", home);
                            let marker = crate::tunnel::sshauth::EPHEMERAL_MARKER;
                            match std::fs::read_to_string(&ak_path) {
                                Ok(existing) => {
                                    let stripped: String = existing
                                        .lines()
                                        .filter(|l| !l.contains(marker))
                                        .collect::<Vec<_>>()
                                        .join("\n")
                                        + if existing.ends_with('\n') { "\n" } else { "" };
                                    if stripped != *keys {
                                        keys_changed = true;
                                        needs_update = true;
                                    }
                                }
                                _ => {
                                    keys_changed = true;
                                    needs_update = true;
                                }
                            }
                        }
                    }

                    // Check desktop (.xsession).
                    let mut xsession_changed = false;
                    let mut xsession_exec = String::new();
                    if let Some(ds) = params.get("desktop") {
                        if !ds.is_empty() {
                            if let Some(exec_cmd) = Self::session_exec(ds) {
                                xsession_exec = exec_cmd.to_string();
                                let home = Self::resolve_home(&name, params, Some(entry));
                                let path = format!("{}/.xsession", home);
                                let desired = format!("#!/bin/sh\nexec {}\n", exec_cmd);
                                match std::fs::read_to_string(&path) {
                                    Ok(existing) if existing == desired => {}
                                    _ => {
                                        xsession_changed = true;
                                        needs_update = true;
                                    }
                                }
                            }
                        }
                    }

                    // Check sudoers.
                    let mut sudoers_changed = false;
                    if let Ok(desired) = Self::desired_sudoers_content(&name, params) {
                        let path = self.sudoers_path(&name);
                        let current = std::fs::read_to_string(&path).ok();
                        match (&desired, &current) {
                            (Some(d), Some(c)) if d == c => {}
                            (None, None) => {}
                            _ => {
                                sudoers_changed = true;
                                needs_update = true;
                            }
                        }
                    }

                    if !needs_update {
                        result.action = Action::None;
                        result.duration_ms = start.elapsed().as_millis() as i32;
                        return result;
                    }

                    result.action = Action::Updated;
                    result.changed = true;

                    if dry_run {
                        result.duration_ms = start.elapsed().as_millis() as i32;
                        return result;
                    }

                    // Apply core attribute changes.
                    if !mod_args.is_empty() {
                        mod_args.push(name.clone());
                        let arg_refs: Vec<&str> = mod_args.iter().map(|s| s.as_str()).collect();
                        match self.runner.run("usermod", &arg_refs) {
                            Ok(o) if !o.status.success() => {
                                let stderr = String::from_utf8_lossy(&o.stderr);
                                // Retry after pkill if user has running processes.
                                if stderr.contains("currently used by process") {
                                    let _ = self.runner.run("pkill", &["-u", &name]);
                                    match self.runner.run("usermod", &arg_refs) {
                                        Ok(o2) if !o2.status.success() => {
                                            let stderr2 = String::from_utf8_lossy(&o2.stderr);
                                            result.error = Some(format!("usermod {}: {}", name, stderr2));
                                            result.duration_ms = start.elapsed().as_millis() as i32;
                                            return result;
                                        }
                                        Err(e) => {
                                            result.error = Some(format!("usermod {}: {}", name, e));
                                            result.duration_ms = start.elapsed().as_millis() as i32;
                                            return result;
                                        }
                                        _ => {}
                                    }
                                } else {
                                    result.error = Some(format!("usermod {}: {}", name, stderr));
                                    result.duration_ms = start.elapsed().as_millis() as i32;
                                    return result;
                                }
                            }
                            Err(e) => {
                                result.error = Some(format!("usermod {}: {}", name, e));
                                result.duration_ms = start.elapsed().as_millis() as i32;
                                return result;
                            }
                            _ => {}
                        }
                    }

                    // Apply supplementary groups.
                    if groups_to_purge {
                        // Set exact group list (purge mode) — use -G without -a.
                        let desired_list = match params.get("groups") {
                            Some(g) => Self::normalize_groups(g),
                            None => {
                                result.error = Some("groups param missing for purge".to_string());
                                result.duration_ms = start.elapsed().as_millis() as i32;
                                return result;
                            }
                        };
                        match self.runner.run("usermod", &["-G", &desired_list, &name]) {
                            Ok(o) if !o.status.success() => {
                                let stderr = String::from_utf8_lossy(&o.stderr);
                                result.error = Some(format!("usermod -G: {}", stderr));
                                result.duration_ms = start.elapsed().as_millis() as i32;
                                return result;
                            }
                            Err(e) => {
                                result.error = Some(format!("usermod -G: {}", e));
                                result.duration_ms = start.elapsed().as_millis() as i32;
                                return result;
                            }
                            _ => {}
                        }
                    } else if !groups_to_add.is_empty() {
                        let joined = groups_to_add.join(",");
                        match self.runner.run("usermod", &["-aG", &joined, &name]) {
                            Ok(o) if !o.status.success() => {
                                let stderr = String::from_utf8_lossy(&o.stderr);
                                result.error = Some(format!("usermod -aG: {}", stderr));
                                result.duration_ms = start.elapsed().as_millis() as i32;
                                return result;
                            }
                            Err(e) => {
                                result.error = Some(format!("usermod -aG: {}", e));
                                result.duration_ms = start.elapsed().as_millis() as i32;
                                return result;
                            }
                            _ => {}
                        }
                    }

                    // Apply password.
                    if password_changed {
                        match self.runner.run("usermod", &["-p", &desired_password, &name]) {
                            Ok(o) if !o.status.success() => {
                                let stderr = String::from_utf8_lossy(&o.stderr);
                                result.error = Some(format!("usermod -p: {}", stderr));
                                result.duration_ms = start.elapsed().as_millis() as i32;
                                return result;
                            }
                            Err(e) => {
                                result.error = Some(format!("usermod -p: {}", e));
                                result.duration_ms = start.elapsed().as_millis() as i32;
                                return result;
                            }
                            _ => {}
                        }
                    }

                    // Apply password max age.
                    if let Some(max_age) = params.get("password_max_age") {
                        if !max_age.is_empty() {
                            match self.runner.run("chage", &["-M", max_age, &name]) {
                                Ok(o) if !o.status.success() => {
                                    let stderr = String::from_utf8_lossy(&o.stderr);
                                    result.error = Some(format!("chage -M: {}", stderr));
                                    result.duration_ms = start.elapsed().as_millis() as i32;
                                    return result;
                                }
                                Err(e) => {
                                    result.error = Some(format!("chage -M: {}", e));
                                    result.duration_ms = start.elapsed().as_millis() as i32;
                                    return result;
                                }
                                _ => {}
                            }
                        }
                    }

                    // Apply authorized_keys.
                    if keys_changed {
                        let home = Self::resolve_home(&name, params, Some(entry));
                        if let Err(e) = self.write_authorized_keys(&name, &home, &desired_keys) {
                            result.error = Some(e);
                        }
                    }

                    // Apply desktop.
                    if xsession_changed && !xsession_exec.is_empty() {
                        let home = Self::resolve_home(&name, params, Some(entry));
                        if let Err(e) = self.write_xsession(&name, &home, &xsession_exec) {
                            result.error = Some(e);
                        }
                    }

                    // Apply sudoers.
                    if sudoers_changed {
                        if let Err(e) = self.apply_sudoers(&name, params) {
                            result.error = Some(e);
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
                        let manage_home = params.get("manage_home").map(|s| s != "false").unwrap_or(true);
                        let userdel_args: Vec<&str> = if manage_home {
                            vec!["-r", &name]
                        } else {
                            vec![&name]
                        };
                        match self.runner.run("userdel", &userdel_args) {
                            Ok(o) if !o.status.success() => {
                                let stderr = String::from_utf8_lossy(&o.stderr);
                                result.error = Some(format!("userdel {}: {}", name, stderr));
                            }
                            Err(e) => {
                                result.error = Some(format!("userdel {}: {}", name, e));
                            }
                            _ => {}
                        }
                        self.remove_sudoers(&name);
                    }
                }
            }
            _ => {
                result.error = Some(format!("user executor: unknown state {:?}", state));
            }
        }

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::executors::test_support::mock::*;
    use std::sync::Arc;

    #[test]
    fn normalize_groups_comma_string() {
        assert_eq!(UserExecutor::normalize_groups("plex,docker"), "plex,docker");
        assert_eq!(UserExecutor::normalize_groups("plex, docker"), "plex,docker");
        assert_eq!(
            UserExecutor::normalize_groups(" plex ,  docker , "),
            "plex,docker"
        );
        assert_eq!(UserExecutor::normalize_groups(""), "");
        assert_eq!(UserExecutor::normalize_groups("   "), "");
    }

    #[test]
    fn normalize_groups_json_array() {
        // YAML list form gets JSON-serialized on the wire by the server.
        assert_eq!(
            UserExecutor::normalize_groups(r#"["plex","docker"]"#),
            "plex,docker"
        );
        assert_eq!(UserExecutor::normalize_groups(r#"["plex"]"#), "plex");
        assert_eq!(UserExecutor::normalize_groups(r#"[]"#), "");
        assert_eq!(
            UserExecutor::normalize_groups(r#"[ "plex" , "docker" , "kvm" ]"#),
            "plex,docker,kvm"
        );
    }

    #[test]
    fn normalize_groups_malformed_json_falls_back_to_comma_split() {
        // Looks like JSON but doesn't parse — the comma-split fallback
        // still returns something reasonable rather than erroring.
        let out = UserExecutor::normalize_groups(r#"[plex, docker]"#);
        // brackets survive as part of group names because they weren't stripped
        // by JSON parsing, but the comma split is still applied.
        assert!(out.contains("plex"));
        assert!(out.contains("docker"));
    }

    #[test]
    fn missing_name_errors() {
        let dir = tempfile::TempDir::new().unwrap();
        let passwd = dir.path().join("passwd");
        let shadow = dir.path().join("shadow");
        std::fs::write(&passwd, "root:x:0:0:root:/root:/bin/bash\n").unwrap();
        std::fs::write(&shadow, "root:*:19000:0:99999:7:::\n").unwrap();

        let mock = MockRunner::empty();
        let exec = UserExecutor {
            runner: Box::new(mock),
            passwd_path: passwd.to_str().unwrap().to_string(),
            shadow_path: shadow.to_str().unwrap().to_string(),
            sudoers_dir: dir.path().join("sudoers.d").to_str().unwrap().to_string(),
        };

        let result = exec.execute(&make_params(&[]), false);
        assert!(result.error.is_some());
        assert!(
            result.error.as_deref().unwrap().contains("username param is required"),
            "unexpected error: {:?}",
            result.error
        );
    }

    #[test]
    fn present_creates_new_user() {
        let dir = tempfile::TempDir::new().unwrap();
        let passwd = dir.path().join("passwd");
        let shadow = dir.path().join("shadow");
        std::fs::write(&passwd, "root:x:0:0:root:/root:/bin/bash\n").unwrap();
        std::fs::write(&shadow, "root:*:19000:0:99999:7:::\n").unwrap();

        // useradd succeeds
        let mock = MockRunner::new(vec![ok_output("")]);
        let calls = Arc::clone(&mock.calls);
        let exec = UserExecutor {
            runner: Box::new(mock),
            passwd_path: passwd.to_str().unwrap().to_string(),
            shadow_path: shadow.to_str().unwrap().to_string(),
            sudoers_dir: dir.path().join("sudoers.d").to_str().unwrap().to_string(),
        };

        let result = exec.execute(
            &make_params(&[
                ("username", "deploy"),
                ("shell", "/bin/bash"),
                ("home", "/home/deploy"),
                ("state", "present"),
            ]),
            false,
        );

        assert_eq!(result.action, Action::Created);
        assert!(result.changed);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1);
        assert_eq!(calls[0].0, "useradd");
        // Verify shell and home flags are present
        let args = &calls[0].1;
        assert!(args.contains(&"-s".to_string()));
        assert!(args.contains(&"/bin/bash".to_string()));
        assert!(args.contains(&"-d".to_string()));
        assert!(args.contains(&"/home/deploy".to_string()));
        assert!(args.contains(&"deploy".to_string()));
    }

    #[test]
    fn present_user_already_matches() {
        let dir = tempfile::TempDir::new().unwrap();
        let passwd = dir.path().join("passwd");
        let shadow = dir.path().join("shadow");
        std::fs::write(
            &passwd,
            "root:x:0:0:root:/root:/bin/bash\ndeploy:x:1001:1001:Deploy User:/home/deploy:/bin/bash\n",
        )
        .unwrap();
        std::fs::write(
            &shadow,
            "root:*:19000:0:99999:7:::\ndeploy:$6$abc:19000:0:99999:7:::\n",
        )
        .unwrap();

        // No runner calls expected — user already matches
        let mock = MockRunner::empty();
        let calls = Arc::clone(&mock.calls);
        let exec = UserExecutor {
            runner: Box::new(mock),
            passwd_path: passwd.to_str().unwrap().to_string(),
            shadow_path: shadow.to_str().unwrap().to_string(),
            sudoers_dir: dir.path().join("sudoers.d").to_str().unwrap().to_string(),
        };

        let result = exec.execute(
            &make_params(&[
                ("username", "deploy"),
                ("shell", "/bin/bash"),
                ("home", "/home/deploy"),
                ("comment", "Deploy User"),
                ("state", "present"),
            ]),
            false,
        );

        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        assert!(
            calls.lock().unwrap().is_empty(),
            "no commands should be called when user already matches"
        );
    }

    #[test]
    fn present_updates_shell() {
        let dir = tempfile::TempDir::new().unwrap();
        let passwd = dir.path().join("passwd");
        let shadow = dir.path().join("shadow");
        std::fs::write(
            &passwd,
            "root:x:0:0:root:/root:/bin/bash\ndeploy:x:1001:1001::/home/deploy:/bin/sh\n",
        )
        .unwrap();
        std::fs::write(
            &shadow,
            "root:*:19000:0:99999:7:::\ndeploy:$6$abc:19000:0:99999:7:::\n",
        )
        .unwrap();

        // usermod succeeds
        let mock = MockRunner::new(vec![ok_output("")]);
        let calls = Arc::clone(&mock.calls);
        let exec = UserExecutor {
            runner: Box::new(mock),
            passwd_path: passwd.to_str().unwrap().to_string(),
            shadow_path: shadow.to_str().unwrap().to_string(),
            sudoers_dir: dir.path().join("sudoers.d").to_str().unwrap().to_string(),
        };

        let result = exec.execute(
            &make_params(&[
                ("username", "deploy"),
                ("shell", "/bin/bash"),
                ("state", "present"),
            ]),
            false,
        );

        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1);
        assert_eq!(calls[0].0, "usermod");
        assert!(calls[0].1.contains(&"-s".to_string()));
        assert!(calls[0].1.contains(&"/bin/bash".to_string()));
        assert!(calls[0].1.contains(&"deploy".to_string()));
    }

    #[test]
    fn absent_user_not_found() {
        let dir = tempfile::TempDir::new().unwrap();
        let passwd = dir.path().join("passwd");
        let shadow = dir.path().join("shadow");
        std::fs::write(&passwd, "root:x:0:0:root:/root:/bin/bash\n").unwrap();
        std::fs::write(&shadow, "root:*:19000:0:99999:7:::\n").unwrap();

        let mock = MockRunner::empty();
        let calls = Arc::clone(&mock.calls);
        let exec = UserExecutor {
            runner: Box::new(mock),
            passwd_path: passwd.to_str().unwrap().to_string(),
            shadow_path: shadow.to_str().unwrap().to_string(),
            sudoers_dir: dir.path().join("sudoers.d").to_str().unwrap().to_string(),
        };

        let result = exec.execute(
            &make_params(&[("username", "ghost"), ("state", "absent")]),
            false,
        );

        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());
        assert!(calls.lock().unwrap().is_empty());
    }

    #[test]
    fn absent_deletes_user() {
        let dir = tempfile::TempDir::new().unwrap();
        let passwd = dir.path().join("passwd");
        let shadow = dir.path().join("shadow");
        std::fs::write(
            &passwd,
            "root:x:0:0:root:/root:/bin/bash\ndeploy:x:1001:1001::/home/deploy:/bin/bash\n",
        )
        .unwrap();
        std::fs::write(
            &shadow,
            "root:*:19000:0:99999:7:::\ndeploy:$6$abc:19000:0:99999:7:::\n",
        )
        .unwrap();

        // userdel succeeds
        let mock = MockRunner::new(vec![ok_output("")]);
        let calls = Arc::clone(&mock.calls);
        let exec = UserExecutor {
            runner: Box::new(mock),
            passwd_path: passwd.to_str().unwrap().to_string(),
            shadow_path: shadow.to_str().unwrap().to_string(),
            sudoers_dir: dir.path().join("sudoers.d").to_str().unwrap().to_string(),
        };

        let result = exec.execute(
            &make_params(&[("username", "deploy"), ("state", "absent")]),
            false,
        );

        assert_eq!(result.action, Action::Deleted);
        assert!(result.changed);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1);
        assert_eq!(calls[0].0, "userdel");
        // Default manage_home is true, so -r flag is included.
        assert_eq!(calls[0].1, vec!["-r", "deploy"]);
    }

    #[test]
    fn dry_run_does_not_call_useradd() {
        let dir = tempfile::TempDir::new().unwrap();
        let passwd = dir.path().join("passwd");
        let shadow = dir.path().join("shadow");
        std::fs::write(&passwd, "root:x:0:0:root:/root:/bin/bash\n").unwrap();
        std::fs::write(&shadow, "root:*:19000:0:99999:7:::\n").unwrap();

        let mock = MockRunner::empty();
        let calls = Arc::clone(&mock.calls);
        let exec = UserExecutor {
            runner: Box::new(mock),
            passwd_path: passwd.to_str().unwrap().to_string(),
            shadow_path: shadow.to_str().unwrap().to_string(),
            sudoers_dir: dir.path().join("sudoers.d").to_str().unwrap().to_string(),
        };

        let result = exec.execute(
            &make_params(&[("username", "deploy"), ("state", "present")]),
            true,
        );

        assert_eq!(result.action, Action::Created);
        assert!(result.changed);
        assert!(result.error.is_none());
        assert!(
            calls.lock().unwrap().is_empty(),
            "dry_run should not invoke any commands"
        );
    }

    #[test]
    fn manage_home_false_skips_m_flag() {
        let dir = tempfile::TempDir::new().unwrap();
        let passwd = dir.path().join("passwd");
        let shadow = dir.path().join("shadow");
        std::fs::write(&passwd, "root:x:0:0:root:/root:/bin/bash\n").unwrap();
        std::fs::write(&shadow, "root:*:19000:0:99999:7:::\n").unwrap();

        let mock = MockRunner::new(vec![ok_output("")]);
        let calls = Arc::clone(&mock.calls);
        let exec = UserExecutor {
            runner: Box::new(mock),
            passwd_path: passwd.to_str().unwrap().to_string(),
            shadow_path: shadow.to_str().unwrap().to_string(),
            sudoers_dir: dir.path().join("sudoers.d").to_str().unwrap().to_string(),
        };

        let result = exec.execute(
            &make_params(&[
                ("username", "deploy"),
                ("state", "present"),
                ("manage_home", "false"),
            ]),
            false,
        );

        assert_eq!(result.action, Action::Created);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);

        let calls = calls.lock().unwrap();
        assert_eq!(calls[0].0, "useradd");
        assert!(
            !calls[0].1.contains(&"-m".to_string()),
            "useradd should not contain -m when manage_home is false, got: {:?}",
            calls[0].1
        );
    }

    #[test]
    fn manage_home_true_removes_home_on_delete() {
        let dir = tempfile::TempDir::new().unwrap();
        let passwd = dir.path().join("passwd");
        let shadow = dir.path().join("shadow");
        std::fs::write(
            &passwd,
            "root:x:0:0:root:/root:/bin/bash\ndeploy:x:1001:1001::/home/deploy:/bin/bash\n",
        )
        .unwrap();
        std::fs::write(
            &shadow,
            "root:*:19000:0:99999:7:::\ndeploy:$6$abc:19000:0:99999:7:::\n",
        )
        .unwrap();

        // userdel succeeds
        let mock = MockRunner::new(vec![ok_output("")]);
        let calls = Arc::clone(&mock.calls);
        let exec = UserExecutor {
            runner: Box::new(mock),
            passwd_path: passwd.to_str().unwrap().to_string(),
            shadow_path: shadow.to_str().unwrap().to_string(),
            sudoers_dir: dir.path().join("sudoers.d").to_str().unwrap().to_string(),
        };

        // Explicit manage_home: "true" (also the default).
        let result = exec.execute(
            &make_params(&[
                ("username", "deploy"),
                ("state", "absent"),
                ("manage_home", "true"),
            ]),
            false,
        );

        assert_eq!(result.action, Action::Deleted);
        assert!(result.changed);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);

        let calls = calls.lock().unwrap();
        assert_eq!(calls[0].0, "userdel");
        assert_eq!(calls[0].1, vec!["-r", "deploy"]);
    }

    #[test]
    fn manage_home_false_plain_userdel() {
        let dir = tempfile::TempDir::new().unwrap();
        let passwd = dir.path().join("passwd");
        let shadow = dir.path().join("shadow");
        std::fs::write(
            &passwd,
            "root:x:0:0:root:/root:/bin/bash\ndeploy:x:1001:1001::/home/deploy:/bin/bash\n",
        )
        .unwrap();
        std::fs::write(
            &shadow,
            "root:*:19000:0:99999:7:::\ndeploy:$6$abc:19000:0:99999:7:::\n",
        )
        .unwrap();

        let mock = MockRunner::new(vec![ok_output("")]);
        let calls = Arc::clone(&mock.calls);
        let exec = UserExecutor {
            runner: Box::new(mock),
            passwd_path: passwd.to_str().unwrap().to_string(),
            shadow_path: shadow.to_str().unwrap().to_string(),
            sudoers_dir: dir.path().join("sudoers.d").to_str().unwrap().to_string(),
        };

        let result = exec.execute(
            &make_params(&[
                ("username", "deploy"),
                ("state", "absent"),
                ("manage_home", "false"),
            ]),
            false,
        );

        assert_eq!(result.action, Action::Deleted);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);

        let calls = calls.lock().unwrap();
        assert_eq!(calls[0].0, "userdel");
        // No -r flag when manage_home is false.
        assert_eq!(calls[0].1, vec!["deploy"]);
    }

    #[test]
    fn password_max_age_runs_chage() {
        let dir = tempfile::TempDir::new().unwrap();
        let passwd = dir.path().join("passwd");
        let shadow = dir.path().join("shadow");
        std::fs::write(&passwd, "root:x:0:0:root:/root:/bin/bash\n").unwrap();
        std::fs::write(&shadow, "root:*:19000:0:99999:7:::\n").unwrap();

        // useradd succeeds, chage succeeds
        let mock = MockRunner::new(vec![ok_output(""), ok_output("")]);
        let calls = Arc::clone(&mock.calls);
        let exec = UserExecutor {
            runner: Box::new(mock),
            passwd_path: passwd.to_str().unwrap().to_string(),
            shadow_path: shadow.to_str().unwrap().to_string(),
            sudoers_dir: dir.path().join("sudoers.d").to_str().unwrap().to_string(),
        };

        let result = exec.execute(
            &make_params(&[
                ("username", "deploy"),
                ("state", "present"),
                ("password_max_age", "90"),
            ]),
            false,
        );

        assert_eq!(result.action, Action::Created);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 2);
        assert_eq!(calls[0].0, "useradd");
        assert_eq!(calls[1].0, "chage");
        assert_eq!(calls[1].1, vec!["-M", "90", "deploy"]);
    }

    #[test]
    fn password_max_age_runs_chage_on_update() {
        let dir = tempfile::TempDir::new().unwrap();
        let passwd = dir.path().join("passwd");
        let shadow = dir.path().join("shadow");
        std::fs::write(
            &passwd,
            "root:x:0:0:root:/root:/bin/bash\ndeploy:x:1001:1001::/home/deploy:/bin/bash\n",
        )
        .unwrap();
        std::fs::write(
            &shadow,
            "root:*:19000:0:99999:7:::\ndeploy:$6$abc:19000:0:99999:7:::\n",
        )
        .unwrap();

        // chage succeeds
        let mock = MockRunner::new(vec![ok_output("")]);
        let calls = Arc::clone(&mock.calls);
        let exec = UserExecutor {
            runner: Box::new(mock),
            passwd_path: passwd.to_str().unwrap().to_string(),
            shadow_path: shadow.to_str().unwrap().to_string(),
            sudoers_dir: dir.path().join("sudoers.d").to_str().unwrap().to_string(),
        };

        let result = exec.execute(
            &make_params(&[
                ("username", "deploy"),
                ("state", "present"),
                ("password_max_age", "60"),
            ]),
            false,
        );

        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1);
        assert_eq!(calls[0].0, "chage");
        assert_eq!(calls[0].1, vec!["-M", "60", "deploy"]);
    }

    #[test]
    fn expiredate_passed_to_useradd() {
        let dir = tempfile::TempDir::new().unwrap();
        let passwd = dir.path().join("passwd");
        let shadow = dir.path().join("shadow");
        std::fs::write(&passwd, "root:x:0:0:root:/root:/bin/bash\n").unwrap();
        std::fs::write(&shadow, "root:*:19000:0:99999:7:::\n").unwrap();

        let mock = MockRunner::new(vec![ok_output("")]);
        let calls = Arc::clone(&mock.calls);
        let exec = UserExecutor {
            runner: Box::new(mock),
            passwd_path: passwd.to_str().unwrap().to_string(),
            shadow_path: shadow.to_str().unwrap().to_string(),
            sudoers_dir: dir.path().join("sudoers.d").to_str().unwrap().to_string(),
        };

        let result = exec.execute(
            &make_params(&[
                ("username", "deploy"),
                ("state", "present"),
                ("expiredate", "2026-12-31"),
            ]),
            false,
        );

        assert_eq!(result.action, Action::Created);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);

        let calls = calls.lock().unwrap();
        assert_eq!(calls[0].0, "useradd");
        let args = &calls[0].1;
        let idx = args.iter().position(|a| a == "--expiredate").expect("--expiredate not found");
        assert_eq!(args[idx + 1], "2026-12-31");
    }

    #[test]
    fn purge_groups_uses_set_not_append() {
        let dir = tempfile::TempDir::new().unwrap();
        let passwd = dir.path().join("passwd");
        let shadow = dir.path().join("shadow");
        std::fs::write(
            &passwd,
            "root:x:0:0:root:/root:/bin/bash\ndeploy:x:1001:1001::/home/deploy:/bin/bash\n",
        )
        .unwrap();
        std::fs::write(
            &shadow,
            "root:*:19000:0:99999:7:::\ndeploy:$6$abc:19000:0:99999:7:::\n",
        )
        .unwrap();

        // id -nG returns current groups (deploy is primary + docker,wheel as supplementary)
        // usermod -G succeeds
        let mock = MockRunner::new(vec![
            ok_output("deploy docker wheel"),
            ok_output(""),
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = UserExecutor {
            runner: Box::new(mock),
            passwd_path: passwd.to_str().unwrap().to_string(),
            shadow_path: shadow.to_str().unwrap().to_string(),
            sudoers_dir: dir.path().join("sudoers.d").to_str().unwrap().to_string(),
        };

        // Desired groups: docker only. wheel should be purged.
        let result = exec.execute(
            &make_params(&[
                ("username", "deploy"),
                ("state", "present"),
                ("groups", "docker"),
                ("purge_groups", "true"),
            ]),
            false,
        );

        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);

        let calls = calls.lock().unwrap();
        // First call: id -nG deploy (group check)
        assert_eq!(calls[0].0, "id");
        // Second call: usermod -G docker deploy (set, not append)
        assert_eq!(calls[1].0, "usermod");
        assert_eq!(calls[1].1, vec!["-G", "docker", "deploy"]);
        // Verify it's NOT -aG
        assert!(
            !calls[1].1.contains(&"-aG".to_string()),
            "purge_groups should use -G, not -aG"
        );
    }

    #[test]
    fn unknown_state_errors() {
        let dir = tempfile::TempDir::new().unwrap();
        let passwd = dir.path().join("passwd");
        let shadow = dir.path().join("shadow");
        std::fs::write(&passwd, "root:x:0:0:root:/root:/bin/bash\n").unwrap();
        std::fs::write(&shadow, "root:*:19000:0:99999:7:::\n").unwrap();

        let mock = MockRunner::empty();
        let exec = UserExecutor {
            runner: Box::new(mock),
            passwd_path: passwd.to_str().unwrap().to_string(),
            shadow_path: shadow.to_str().unwrap().to_string(),
            sudoers_dir: dir.path().join("sudoers.d").to_str().unwrap().to_string(),
        };

        let result = exec.execute(
            &make_params(&[("username", "deploy"), ("state", "bogus")]),
            false,
        );

        assert!(result.error.is_some());
        assert!(
            result.error.as_deref().unwrap().contains("unknown state"),
            "unexpected error: {:?}",
            result.error
        );
    }

    #[test]
    fn sudo_passwd_creates_sudoers_file() {
        let dir = tempfile::TempDir::new().unwrap();
        let passwd = dir.path().join("passwd");
        let shadow = dir.path().join("shadow");
        let sudoers = dir.path().join("sudoers.d");
        std::fs::create_dir_all(&sudoers).unwrap();
        std::fs::write(&passwd, "root:x:0:0:root:/root:/bin/bash\n").unwrap();
        std::fs::write(&shadow, "root:*:19000:0:99999:7:::\n").unwrap();

        let mock = MockRunner::new(vec![ok_output("")]);
        let exec = UserExecutor {
            runner: Box::new(mock),
            passwd_path: passwd.to_str().unwrap().to_string(),
            shadow_path: shadow.to_str().unwrap().to_string(),
            sudoers_dir: sudoers.to_str().unwrap().to_string(),
        };

        let result = exec.execute(
            &make_params(&[("username", "deploy"), ("sudo_passwd", "true")]),
            false,
        );

        assert!(result.changed);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        let content = std::fs::read_to_string(sudoers.join("deploy")).unwrap();
        assert_eq!(content, "deploy ALL=(ALL) ALL\n");
    }

    #[test]
    fn sudo_nopasswd_creates_nopasswd_sudoers() {
        let dir = tempfile::TempDir::new().unwrap();
        let passwd = dir.path().join("passwd");
        let shadow = dir.path().join("shadow");
        let sudoers = dir.path().join("sudoers.d");
        std::fs::create_dir_all(&sudoers).unwrap();
        std::fs::write(&passwd, "root:x:0:0:root:/root:/bin/bash\n").unwrap();
        std::fs::write(&shadow, "root:*:19000:0:99999:7:::\n").unwrap();

        let mock = MockRunner::new(vec![ok_output("")]);
        let exec = UserExecutor {
            runner: Box::new(mock),
            passwd_path: passwd.to_str().unwrap().to_string(),
            shadow_path: shadow.to_str().unwrap().to_string(),
            sudoers_dir: sudoers.to_str().unwrap().to_string(),
        };

        let result = exec.execute(
            &make_params(&[("username", "deploy"), ("sudo_nopasswd", "true")]),
            false,
        );

        assert!(result.changed);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        let content = std::fs::read_to_string(sudoers.join("deploy")).unwrap();
        assert_eq!(content, "deploy ALL=(ALL) NOPASSWD: ALL\n");
    }

    #[test]
    fn sudo_passwd_and_nopasswd_is_error() {
        let dir = tempfile::TempDir::new().unwrap();
        let passwd = dir.path().join("passwd");
        let shadow = dir.path().join("shadow");
        std::fs::write(&passwd, "root:x:0:0:root:/root:/bin/bash\n").unwrap();
        std::fs::write(&shadow, "root:*:19000:0:99999:7:::\n").unwrap();

        let mock = MockRunner::empty();
        let exec = UserExecutor {
            runner: Box::new(mock),
            passwd_path: passwd.to_str().unwrap().to_string(),
            shadow_path: shadow.to_str().unwrap().to_string(),
            sudoers_dir: dir.path().join("sudoers.d").to_str().unwrap().to_string(),
        };

        let result = exec.execute(
            &make_params(&[("username", "deploy"), ("sudo_passwd", "true"), ("sudo_nopasswd", "true")]),
            false,
        );

        assert!(result.error.is_some());
        assert!(result.error.as_deref().unwrap().contains("mutually exclusive"));
    }

    #[test]
    fn sudo_idempotent_no_change() {
        let dir = tempfile::TempDir::new().unwrap();
        let passwd = dir.path().join("passwd");
        let shadow = dir.path().join("shadow");
        let sudoers = dir.path().join("sudoers.d");
        std::fs::create_dir_all(&sudoers).unwrap();
        std::fs::write(&passwd, "deploy:x:1001:1001::/home/deploy:/bin/bash\n").unwrap();
        std::fs::write(&shadow, "deploy:$6$abc:19000:0:99999:7:::\n").unwrap();
        std::fs::write(sudoers.join("deploy"), "deploy ALL=(ALL) ALL\n").unwrap();

        let mock = MockRunner::empty();
        let calls = Arc::clone(&mock.calls);
        let exec = UserExecutor {
            runner: Box::new(mock),
            passwd_path: passwd.to_str().unwrap().to_string(),
            shadow_path: shadow.to_str().unwrap().to_string(),
            sudoers_dir: sudoers.to_str().unwrap().to_string(),
        };

        let result = exec.execute(
            &make_params(&[("username", "deploy"), ("shell", "/bin/bash"), ("sudo_passwd", "true")]),
            false,
        );

        assert!(!result.changed);
        assert_eq!(result.action, Action::None);
        assert!(calls.lock().unwrap().is_empty());
    }

    #[test]
    fn absent_removes_sudoers_file() {
        let dir = tempfile::TempDir::new().unwrap();
        let passwd = dir.path().join("passwd");
        let shadow = dir.path().join("shadow");
        let sudoers = dir.path().join("sudoers.d");
        std::fs::create_dir_all(&sudoers).unwrap();
        std::fs::write(&passwd, "deploy:x:1001:1001::/home/deploy:/bin/bash\n").unwrap();
        std::fs::write(&shadow, "deploy:$6$abc:19000:0:99999:7:::\n").unwrap();
        std::fs::write(sudoers.join("deploy"), "deploy ALL=(ALL) ALL\n").unwrap();

        let mock = MockRunner::new(vec![ok_output("")]);
        let exec = UserExecutor {
            runner: Box::new(mock),
            passwd_path: passwd.to_str().unwrap().to_string(),
            shadow_path: shadow.to_str().unwrap().to_string(),
            sudoers_dir: sudoers.to_str().unwrap().to_string(),
        };

        let result = exec.execute(
            &make_params(&[("username", "deploy"), ("state", "absent")]),
            false,
        );

        assert!(result.changed);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        assert!(!sudoers.join("deploy").exists());
    }
}
