// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! User executor — manages system users idempotently via useradd/usermod/userdel.

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use sha_crypt::{sha512_check, sha512_simple, Sha512Params};
use std::collections::{HashMap, HashSet};
use std::fs::File;
use std::io::{BufRead, BufReader};
use std::time::Instant;

// password_matches_shadow returns true iff the SHA-512-crypt entry in
// /etc/shadow represents the given plaintext. Any non-$6$ entry (locked "!",
// no-password "*", legacy md5/sha256, malformed) is treated as a mismatch so
// the caller re-applies. Constant-time comparison happens inside
// sha512_check; we only inspect Ok-vs-Err here.
fn password_matches_shadow(plaintext: &str, shadow_entry: &str) -> bool {
    if !shadow_entry.starts_with("$6$") {
        return false;
    }
    sha512_check(plaintext, shadow_entry).is_ok()
}

// compute_shadow_hash produces a `$6$<random-salt>$<hash>` string suitable
// for `usermod -p`. A fresh random salt is generated each call — operators
// who reset to the same plaintext still get a different on-disk entry.
fn compute_shadow_hash(plaintext: &str) -> Result<String, String> {
    sha512_simple(plaintext, &Sha512Params::default())
        .map_err(|e| format!("sha512_simple: {:?}", e))
}

pub struct UserExecutor {
    pub runner: Box<dyn CommandRunner>,
    pub passwd_path: String,
    pub shadow_path: String,
    pub sudoers_dir: String,
    /// Directory for the per-user usercrate-policy cache (0.41.0+).
    /// Defaults to [`crate::swarm::policy::POLICY_DIR`]; tests
    /// override to a tempdir.
    pub policy_dir: String,
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
            policy_dir: crate::swarm::policy::POLICY_DIR.into(),
        }
    }

    fn lookup_user(&self, name: &str) -> Result<Option<PasswdEntry>, String> {
        let f = std::fs::File::open(&self.passwd_path)
            .map_err(|e| format!("open passwd: {}", e))?;

        for line in BufReader::new(f).lines().map_while(Result::ok) {
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
        for line in BufReader::new(f).lines().map_while(Result::ok) {
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

    /// Return the set of group names that exist on the system (via NSS),
    /// used to filter `groups:` requests so a missing group on one envoy
    /// doesn't cascade into a failed usermod that takes the rest of the
    /// user resource down with it. Queries `getent group` first (covers
    /// LDAP/AD/SSSD groups in addition to /etc/group); falls back to
    /// reading /etc/group directly if getent isn't available or returns
    /// nothing useful.
    ///
    /// This is intentionally separate from `current_groups` — that lists
    /// groups the *user* is currently in; this lists groups that *exist*
    /// on the system at all.
    fn known_groups(&self) -> HashSet<String> {
        let mut groups = HashSet::new();
        if let Ok(o) = self.runner.run("getent", &["group"]) {
            if o.status.success() {
                for line in String::from_utf8_lossy(&o.stdout).lines() {
                    if let Some((name, _)) = line.split_once(':') {
                        if !name.is_empty() {
                            groups.insert(name.to_string());
                        }
                    }
                }
            }
        }
        // Fallback for environments without getent (musl, container
        // images stripped to busybox, etc.) — the format is identical.
        if groups.is_empty() {
            if let Ok(f) = File::open("/etc/group") {
                for line in BufReader::new(f).lines().map_while(Result::ok) {
                    if let Some((name, _)) = line.split_once(':') {
                        if !name.is_empty() {
                            groups.insert(name.to_string());
                        }
                    }
                }
            }
        }
        groups
    }

    /// Partition a desired-groups list into (present_on_system, missing).
    /// The missing list is reported as a partial error and skipped at
    /// usermod time, so a single missing group on one envoy doesn't break
    /// the entire user update path. See `known_groups` for the filter
    /// source.
    fn partition_groups(&self, desired: &[String]) -> (Vec<String>, Vec<String>) {
        let known = self.known_groups();
        // If we couldn't read NSS at all, don't filter — passing the
        // request through to usermod preserves the old behavior for
        // operators relying on that path. The accumulator on the caller
        // side will still record the failure rather than early-return.
        if known.is_empty() {
            return (desired.to_vec(), Vec::new());
        }
        let mut present = Vec::new();
        let mut missing = Vec::new();
        for g in desired {
            if known.contains(g) {
                present.push(g.clone());
            } else {
                missing.push(g.clone());
            }
        }
        (present, missing)
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
                    .map(|item| match item {
                        serde_json::Value::String(g) => g,
                        other => other.to_string(),
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
        for (flag, key) in [("-u", "uid"), ("-s", "shell"), ("-d", "home"), ("-c", "comment"), ("-g", "group"), ("--expiredate", "expiredate")] {
            if let Some(v) = params.get(key) {
                if !v.is_empty() {
                    args.push(flag.into());
                    args.push(v.clone());
                }
            }
        }
        // `password:` is plaintext; useradd -p wants SHA-512-crypt. Hash at
        // call time. Skip silently on hash error — the apply-pass later will
        // surface the failure via partial_errors after the user exists.
        if let Some(pw) = params.get("password") {
            if !pw.is_empty() {
                if let Ok(hashed) = compute_shadow_hash(pw) {
                    args.push("-p".into());
                    args.push(hashed);
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
                // No sudo params on this resource — leave any existing
                // sudoers file alone. A user resource that doesn't
                // mention sudo isn't claiming "no sudo"; it's just
                // declining to manage that aspect, the same way it
                // leaves shell/groups/password alone when those
                // params are absent.
                //
                // Background: dan.vgo defines two `user` resources for
                // the same `username: dan` — one with sudo_nopasswd
                // (the main `dan` resource) and one without (the
                // `dan-desktop` resource that only sets `desktop:
                // cinnamon`). Before this fix, dan-desktop would
                // execute right after dan and delete the sudoers file
                // dan had just written, because its check phase saw
                // `(None, Some(existing))` → sudoers_changed → apply
                // → remove. Operators lost NOPASSWD access on every
                // convergence cycle on envoys with cinnamon-session
                // installed.
                //
                // Operators who actually want a sudoers file removed
                // can set `sudo_nopasswd: false` and `sudo_passwd:
                // false` explicitly, or use a separate file resource
                // with `state: absent` against /etc/sudoers.d/<name>.
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
        for line in BufReader::new(f).lines().map_while(Result::ok) {
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

    /// Reconcile the per-user usercrate-policy cache (0.41.0+) and
    /// drive the destructive scrub when an authorization flag flips
    /// from true → false. Called at the end of `execute` for every
    /// reconcile pass.
    ///
    /// Returns a list of operator-facing error strings; the caller
    /// folds them into `ResourceResult.error` so a scrub failure
    /// surfaces in the run report. The reconcile itself is best-effort
    /// — a failure here never blocks the user-resource result, so a
    /// transient issue (root-owned dir already half-scrubbed, missing
    /// home for a partly-deprovisioned user, etc.) doesn't poison
    /// the rest of the convergence pass.
    fn reconcile_swarm_policy(
        &self,
        username: &str,
        state: &str,
        params: &HashMap<String, String>,
    ) -> Vec<String> {
        let mut errors = Vec::new();
        let policy_dir = std::path::Path::new(&self.policy_dir);
        if state == "absent" {
            // The user is going away; drop the policy cache entry. The
            // `userdel -r` above already nuked the user's home, so
            // there's nothing left to scrub.
            if let Err(e) = crate::swarm::policy::remove_user_policy_at(policy_dir, username) {
                errors.push(format!("usercrate-policy remove: {e:#}"));
            }
            return errors;
        }
        if state != "present" {
            return errors;
        }

        let new_policy = crate::swarm::policy::UserPolicy {
            puddle: parse_bool_flag(params.get("puddle")),
            gitback: parse_bool_flag(params.get("gitback")),
            curator: parse_bool_flag(params.get("curator")),
            poolq: parse_bool_flag(params.get("poolq")),
        };
        // Read the previously-cached policy so we can detect flag
        // flips. Missing-or-unreadable cache is treated as all-false:
        // a fresh user gets no scrub on first reconcile, regardless
        // of which flags they currently carry.
        let old_policy = crate::swarm::policy::read_user_policy_at(policy_dir, username)
            .unwrap_or_default();

        if old_policy.puddle && !new_policy.puddle {
            errors.extend(self.scrub_swarm_state(username, ScrubScope::PuddleAndLockbox));
        }
        if old_policy.gitback && !new_policy.gitback {
            errors.extend(self.scrub_swarm_state(username, ScrubScope::Gitback));
        }
        // A `puddle: true→false` scrub already wiped ~/.vigo-curator/
        // (see ScrubScope::PuddleAndLockbox); only run the curator-only
        // scrub when puddle stays on but `curator:` was withdrawn.
        if old_policy.curator && !new_policy.curator && new_policy.puddle {
            errors.extend(self.scrub_swarm_state(username, ScrubScope::Curator));
        }
        // Same logic for poolq (ADR-029): a `puddle: true→false` scrub
        // already wiped ~/.vigo-poolq/ via ScrubScope::PuddleAndLockbox
        // below; only run the poolq-only scrub when puddle stays on but
        // `poolq:` was withdrawn.
        if old_policy.poolq && !new_policy.poolq && new_policy.puddle {
            errors.extend(self.scrub_swarm_state(username, ScrubScope::Poolq));
        }

        if let Err(e) =
            crate::swarm::policy::write_user_policy_at(policy_dir, username, &new_policy)
        {
            errors.push(format!("usercrate-policy write: {e:#}"));
        }
        errors
    }

    /// Run the destructive scrub triggered by a usercrate flag going
    /// from true → false. Best-effort: every step is wrapped so a
    /// single failure doesn't abort the rest. Failures collect into
    /// the returned vec and surface in the run report.
    ///
    /// Lockbox LEAVE: queues a `~/.lockbox/leave.pending` sentinel
    /// for the daemon's reactor to pick up on its next tick. The
    /// daemon signs the envelope under the user's identity and fans
    /// it out to live peers — root-here doesn't have to know the key.
    /// Offline peers age the envoy out via heartbeat absence
    /// (puddle_members + per-peer silent-for, 0.39.12).
    fn scrub_swarm_state(&self, username: &str, scope: ScrubScope) -> Vec<String> {
        let mut errors = Vec::new();
        let home = match crate::swarm::lockbox::home_for_user_pub(username) {
            Some(h) => h,
            None => {
                errors.push(format!(
                    "swarm scrub: cannot resolve home for user {username} \
                     (skipping puddle/gitback/curator cleanup)"
                ));
                return errors;
            }
        };

        let scrub_puddle = matches!(scope, ScrubScope::PuddleAndLockbox);
        let scrub_gitback = matches!(scope, ScrubScope::Gitback);
        let scrub_curator = matches!(scope, ScrubScope::Curator);
        let scrub_poolq = matches!(scope, ScrubScope::Poolq);

        if scrub_puddle {
            // Queue lockbox LEAVE for live peers. Skip if the user
            // never enrolled (no identity.age means no LEAVE to sign).
            let cs_identity = home.join(".lockbox").join("identity.age");
            if cs_identity.exists() {
                if let Err(e) = crate::swarm::lockbox::cli::enqueue_leave(
                    &home,
                    username,
                    /* keep_identity */ false,
                    /* purge_content */ true,
                ) {
                    errors.push(format!(
                        "lockbox LEAVE enqueue for {username}: {e:#}"
                    ));
                }
            }
            for dir in [".vigo-puddle", ".lockbox", "lockbox", ".vigo-gitback", ".vigo-curator", ".vigo-poolq"] {
                let path = home.join(dir);
                if path.exists() {
                    if let Err(e) = std::fs::remove_dir_all(&path) {
                        errors.push(format!(
                            "removing {} for {username}: {e}",
                            path.display()
                        ));
                    }
                }
            }
        }

        if scrub_gitback {
            let gm = home.join(".vigo-gitback");
            if gm.exists() {
                if let Err(e) = std::fs::remove_dir_all(&gm) {
                    errors.push(format!(
                        "removing {} for {username}: {e}",
                        gm.display()
                    ));
                }
            }
        }

        if scrub_curator {
            let cm = home.join(".vigo-curator");
            if cm.exists() {
                if let Err(e) = std::fs::remove_dir_all(&cm) {
                    errors.push(format!(
                        "removing {} for {username}: {e}",
                        cm.display()
                    ));
                }
            }
        }

        if scrub_poolq {
            let pq = home.join(".vigo-poolq");
            if pq.exists() {
                if let Err(e) = std::fs::remove_dir_all(&pq) {
                    errors.push(format!(
                        "removing {} for {username}: {e}",
                        pq.display()
                    ));
                }
            }
        }

        errors
    }
}

/// Scope of a swarm-state scrub triggered by a usercrate flag flip.
///
/// `PuddleAndLockbox` covers the heavier removal: puddle keypair,
/// lockbox identity + ciphertext, the per-user gitback state dir, the
/// per-user curator publish-state dir, and the per-user poolq state
/// dir (gitback projects, curator artifacts, and poolq topics are all
/// puddle-anchored — losing puddle means losing them).
///
/// `Gitback` covers only `~/.vigo-gitback/`; `Curator` covers only
/// `~/.vigo-curator/`; `Poolq` covers only `~/.vigo-poolq/`. A user
/// who keeps `puddle: true` but loses one of `gitback: true` /
/// `curator: true` / `poolq: true` retains everything else
/// (already-published artifacts / projects / messages stay — losing a
/// flag doesn't recall them, per ADR-024 / ADR-029).
enum ScrubScope {
    PuddleAndLockbox,
    Gitback,
    Curator,
    Poolq,
}

/// Parse a boolean usercrate flag. Accepts `"true"` / `"false"`;
/// missing or any other shape decodes to `false` (default-deny).
fn parse_bool_flag(v: Option<&String>) -> bool {
    matches!(v.map(String::as_str), Some("true"))
}

impl Executor for UserExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let mut result = self.execute_inner(params, dry_run);
        // 0.41.0 usercrate-policy reconcile. Always runs after the
        // user-resource pass, including the no-change and dry-run
        // early-returns inside `execute_inner` (so the cache stays
        // current even when the user reconciles to "no diff").
        // dry_run still skips the destructive scrub + on-disk write
        // — there's nothing to flip if we're not changing state.
        if !dry_run {
            let name = params.get("username").map(|s| s.as_str()).unwrap_or("");
            let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");
            if !name.is_empty() {
                let scrub_errors = self.reconcile_swarm_policy(name, state, params);
                if !scrub_errors.is_empty() {
                    let joined = scrub_errors.join("; ");
                    result.error = Some(match result.error.take() {
                        Some(prev) => format!("{prev}; {joined}"),
                        None => joined,
                    });
                }
            }
        }
        result
    }
}

impl UserExecutor {
    fn execute_inner(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
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
                    //
                    // partial_errors collects failures from the post-creation
                    // steps (groups, password, keys, sudoers, etc.) without
                    // letting any single failure cascade into skipping the
                    // others. The original early-return pattern meant that
                    // a missing group cascade would lose the user's
                    // authorized_keys, sudoers, and desktop config. Now
                    // each step contributes to a joined error message at
                    // the end, but every step that CAN apply DOES apply.
                    let mut partial_errors: Vec<String> = Vec::new();
                    result.action = Action::Created;
                    result.changed = true;

                    if dry_run {
                        result.duration_ms = start.elapsed().as_millis() as i32;
                        return result;
                    }

                    // useradd is the one step where early-return is correct:
                    // if we can't create the user, the rest of the steps
                    // have nothing to operate on.
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
                                    partial_errors.push(format!("chage -M: {}", String::from_utf8_lossy(&o.stderr)));
                                }
                                Err(e) => {
                                    partial_errors.push(format!("chage -M: {}", e));
                                }
                                _ => {}
                            }
                        }
                    }

                    // Supplementary groups. Filter out groups that don't
                    // exist on the system before the usermod call so a
                    // single missing group (e.g. `docker` on an envoy
                    // where docker isn't installed) doesn't fail the
                    // whole call. Missing groups are recorded as a
                    // partial error so the operator still sees them in
                    // the run output.
                    if let Some(groups_raw) = params.get("groups") {
                        let normalized = Self::normalize_groups(groups_raw);
                        if !normalized.is_empty() {
                            let desired: Vec<String> = normalized
                                .split(',')
                                .map(|s| s.trim().to_string())
                                .filter(|s| !s.is_empty())
                                .collect();
                            let (present, missing) = self.partition_groups(&desired);
                            if !missing.is_empty() {
                                partial_errors.push(format!(
                                    "groups not found on system, skipped: {}",
                                    missing.join(", ")
                                ));
                            }
                            if !present.is_empty() {
                                let joined = present.join(",");
                                match self.runner.run("usermod", &["-aG", &joined, &name]) {
                                    Ok(o) if !o.status.success() => {
                                        partial_errors.push(format!("usermod -aG: {}", String::from_utf8_lossy(&o.stderr)));
                                    }
                                    Err(e) => {
                                        partial_errors.push(format!("usermod -aG: {}", e));
                                    }
                                    _ => {}
                                }
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
                                partial_errors.push(e);
                            }
                        }
                    }

                    // Sudoers — the step that the original cascading bug
                    // was preventing. Critically, this now runs even if
                    // groups, password, or keys failed above.
                    if let Err(e) = self.apply_sudoers(&name, params) {
                        partial_errors.push(e);
                    }

                    // Lockbox enrollment used to be opt-in via
                    // `lockbox: true` on the user resource (dropped
                    // a `~/.lockbox/enroll.pending` sentinel). Per
                    // ADR-020 + ADR-021 (0.40.0), lockbox is a
                    // mandatory capability of puddle membership: it
                    // bootstraps automatically on `puddle unlock` for
                    // any puddle-enrolled user. The flag and its
                    // sentinel-drop logic are retired; existing
                    // usercrates carrying `lockbox: true` continue
                    // to load (serde silently ignores the unknown
                    // field) but the flag has no effect.

                    if !partial_errors.is_empty() {
                        result.error = Some(partial_errors.join("; "));
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

                    // Check password. `password:` carries plaintext (the
                    // operator-side vault stores plaintext too); the agent
                    // computes SHA-512-crypt at apply time so /etc/shadow
                    // ends up with the form Linux expects. Idempotency
                    // compares the plaintext against the current shadow
                    // entry by re-running sha512_check with the entry's
                    // own salt — a stable on-disk hash stays unchanged.
                    let mut password_changed = false;
                    let mut desired_password = String::new();
                    if let Some(pw) = params.get("password") {
                        if !pw.is_empty() {
                            desired_password = pw.clone();
                            match self.lookup_shadow_hash(&name) {
                                Ok(current_hash) => {
                                    if !password_matches_shadow(pw, &current_hash) {
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

                    // Check sudoers. Mirror of `apply_sudoers`'s
                    // semantics: only mark sudoers_changed when this
                    // resource ACTIVELY manages sudo (desired = Some)
                    // and the current state differs. A resource with
                    // no sudo params (`desired = None`) leaves the
                    // file alone — it doesn't claim "no sudo", it
                    // just declines to manage that aspect. See the
                    // long comment in `apply_sudoers` for the dan +
                    // dan-desktop interaction this prevents.
                    let mut sudoers_changed = false;
                    if let Ok(desired) = Self::desired_sudoers_content(&name, params) {
                        let path = self.sudoers_path(&name);
                        let current = std::fs::read_to_string(&path).ok();
                        match (&desired, &current) {
                            (Some(d), Some(c)) if d == c => {}
                            (Some(_), None) | (Some(_), Some(_)) => {
                                // Desired is Some and either missing
                                // or different — write needed.
                                sudoers_changed = true;
                                needs_update = true;
                            }
                            (None, _) => {
                                // No sudo params on this resource —
                                // don't touch the file regardless of
                                // whether it exists.
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

                    // partial_errors collects post-existence-check failures
                    // (groups, password, keys, sudoers, etc.) so a single
                    // failed step doesn't cascade into skipping the
                    // others. Original behavior was: usermod -aG fails on
                    // a missing group → early return → sudoers never
                    // applied → operator loses NOPASSWD access. Now
                    // every step runs to completion and all error
                    // messages join into result.error at the end.
                    let mut partial_errors: Vec<String> = Vec::new();

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
                                            partial_errors.push(format!("usermod {}: {}", name, String::from_utf8_lossy(&o2.stderr)));
                                        }
                                        Err(e) => {
                                            partial_errors.push(format!("usermod {}: {}", name, e));
                                        }
                                        _ => {}
                                    }
                                } else {
                                    partial_errors.push(format!("usermod {}: {}", name, stderr));
                                }
                            }
                            Err(e) => {
                                partial_errors.push(format!("usermod {}: {}", name, e));
                            }
                            _ => {}
                        }
                    }

                    // Apply supplementary groups. Filter missing groups
                    // out of the request so a single missing group on
                    // one envoy doesn't take down the rest of the user
                    // resource. The same `partition_groups` filter is
                    // used for both the purge (-G) and add (-aG) paths.
                    if groups_to_purge {
                        if let Some(groups_raw) = params.get("groups") {
                            let normalized = Self::normalize_groups(groups_raw);
                            let desired: Vec<String> = normalized
                                .split(',')
                                .map(|s| s.trim().to_string())
                                .filter(|s| !s.is_empty())
                                .collect();
                            let (present, missing) = self.partition_groups(&desired);
                            if !missing.is_empty() {
                                partial_errors.push(format!(
                                    "groups not found on system, skipped from purge: {}",
                                    missing.join(", ")
                                ));
                            }
                            let desired_list = present.join(",");
                            // Even with all groups missing, run usermod
                            // -G with an empty list to remove any
                            // existing extras. Empty list is valid for
                            // -G (means "no supplementary groups").
                            match self.runner.run("usermod", &["-G", &desired_list, &name]) {
                                Ok(o) if !o.status.success() => {
                                    partial_errors.push(format!("usermod -G: {}", String::from_utf8_lossy(&o.stderr)));
                                }
                                Err(e) => {
                                    partial_errors.push(format!("usermod -G: {}", e));
                                }
                                _ => {}
                            }
                        } else {
                            partial_errors.push("groups param missing for purge".to_string());
                        }
                    } else if !groups_to_add.is_empty() {
                        let (present, missing) = self.partition_groups(&groups_to_add);
                        if !missing.is_empty() {
                            partial_errors.push(format!(
                                "groups not found on system, skipped: {}",
                                missing.join(", ")
                            ));
                        }
                        if !present.is_empty() {
                            let joined = present.join(",");
                            match self.runner.run("usermod", &["-aG", &joined, &name]) {
                                Ok(o) if !o.status.success() => {
                                    partial_errors.push(format!("usermod -aG: {}", String::from_utf8_lossy(&o.stderr)));
                                }
                                Err(e) => {
                                    partial_errors.push(format!("usermod -aG: {}", e));
                                }
                                _ => {}
                            }
                        }
                    }

                    // Apply password — compute the SHA-512-crypt envelope
                    // here so `usermod -p` receives the shadow-format string
                    // Linux requires.
                    if password_changed {
                        match compute_shadow_hash(&desired_password) {
                            Ok(hashed) => {
                                match self.runner.run("usermod", &["-p", &hashed, &name]) {
                                    Ok(o) if !o.status.success() => {
                                        partial_errors.push(format!("usermod -p: {}", String::from_utf8_lossy(&o.stderr)));
                                    }
                                    Err(e) => {
                                        partial_errors.push(format!("usermod -p: {}", e));
                                    }
                                    _ => {}
                                }
                            }
                            Err(e) => {
                                partial_errors.push(format!("hashing password: {}", e));
                            }
                        }
                    }

                    // Apply password max age.
                    if let Some(max_age) = params.get("password_max_age") {
                        if !max_age.is_empty() {
                            match self.runner.run("chage", &["-M", max_age, &name]) {
                                Ok(o) if !o.status.success() => {
                                    partial_errors.push(format!("chage -M: {}", String::from_utf8_lossy(&o.stderr)));
                                }
                                Err(e) => {
                                    partial_errors.push(format!("chage -M: {}", e));
                                }
                                _ => {}
                            }
                        }
                    }

                    // Apply authorized_keys.
                    if keys_changed {
                        let home = Self::resolve_home(&name, params, Some(entry));
                        if let Err(e) = self.write_authorized_keys(&name, &home, &desired_keys) {
                            partial_errors.push(e);
                        }
                    }

                    // Apply sudoers — the step the original cascading
                    // bug was preventing. Now runs unconditionally even
                    // if everything above failed.
                    if sudoers_changed {
                        if let Err(e) = self.apply_sudoers(&name, params) {
                            partial_errors.push(e);
                        }
                    }

                    // Lockbox enrollment (ADR-020 + ADR-021): no
                    // longer driven by a `lockbox:` flag; bootstraps
                    // automatically on the user's first `puddle
                    // unlock`. See the present-block comment above.

                    if !partial_errors.is_empty() {
                        result.error = Some(partial_errors.join("; "));
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
    fn shadow_hash_round_trips() {
        let hashed = compute_shadow_hash("hunter2!Hunter2").unwrap();
        assert!(hashed.starts_with("$6$"), "got: {}", hashed);
        assert!(password_matches_shadow("hunter2!Hunter2", &hashed));
        assert!(!password_matches_shadow("WrongPass$1", &hashed));
    }

    #[test]
    fn shadow_hash_mismatch_on_non_sha512_entries() {
        // Locked / no-password / legacy entries must always report mismatch
        // so the apply path re-runs `usermod -p`.
        assert!(!password_matches_shadow("anything", "!"));
        assert!(!password_matches_shadow("anything", "*"));
        assert!(!password_matches_shadow("anything", ""));
        assert!(!password_matches_shadow("anything", "$1$abcd$malformed"));
    }

    #[test]
    fn useradd_args_hashes_password() {
        let mut params = HashMap::new();
        params.insert("password".to_string(), "MyPlain$Pass1".to_string());
        let args = UserExecutor::build_useradd_args("alice", &params);
        let p_idx = args.iter().position(|a| a == "-p").expect("-p flag present");
        let hash = &args[p_idx + 1];
        assert!(hash.starts_with("$6$"), "useradd -p got non-shadow form: {}", hash);
        assert!(sha512_check("MyPlain$Pass1", hash).is_ok());
    }

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
            policy_dir: dir.path().join("policy").to_str().unwrap().to_string(),
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
            policy_dir: dir.path().join("policy").to_str().unwrap().to_string(),
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
            policy_dir: dir.path().join("policy").to_str().unwrap().to_string(),
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
            policy_dir: dir.path().join("policy").to_str().unwrap().to_string(),
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
            policy_dir: dir.path().join("policy").to_str().unwrap().to_string(),
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
            policy_dir: dir.path().join("policy").to_str().unwrap().to_string(),
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
            policy_dir: dir.path().join("policy").to_str().unwrap().to_string(),
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
            policy_dir: dir.path().join("policy").to_str().unwrap().to_string(),
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
            policy_dir: dir.path().join("policy").to_str().unwrap().to_string(),
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
            policy_dir: dir.path().join("policy").to_str().unwrap().to_string(),
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
            policy_dir: dir.path().join("policy").to_str().unwrap().to_string(),
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
            policy_dir: dir.path().join("policy").to_str().unwrap().to_string(),
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
            policy_dir: dir.path().join("policy").to_str().unwrap().to_string(),
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

        // Mock call sequence after the missing-group filter landed in
        // 0.21.20:
        //   1. id -nG deploy            → current groups (check phase)
        //   2. getent group             → known groups on system (apply phase,
        //                                  feeds partition_groups so a missing
        //                                  group filters out before usermod)
        //   3. usermod -G docker deploy → purge mode set
        //
        // The getent response includes `docker` so the desired group
        // survives the filter.
        let mock = MockRunner::new(vec![
            ok_output("deploy docker wheel"),
            ok_output("root:x:0:\ndeploy:x:1001:\ndocker:x:998:deploy\nwheel:x:10:deploy\n"),
            ok_output(""),
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = UserExecutor {
            runner: Box::new(mock),
            passwd_path: passwd.to_str().unwrap().to_string(),
            shadow_path: shadow.to_str().unwrap().to_string(),
            sudoers_dir: dir.path().join("sudoers.d").to_str().unwrap().to_string(),
            policy_dir: dir.path().join("policy").to_str().unwrap().to_string(),
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
        // First call: id -nG deploy (group check phase)
        assert_eq!(calls[0].0, "id");
        // Second call: getent group (known-groups filter for partition_groups)
        assert_eq!(calls[1].0, "getent");
        assert_eq!(calls[1].1, vec!["group"]);
        // Third call: usermod -G docker deploy (set, not append)
        assert_eq!(calls[2].0, "usermod");
        assert_eq!(calls[2].1, vec!["-G", "docker", "deploy"]);
        // Verify it's NOT -aG
        assert!(
            !calls[2].1.contains(&"-aG".to_string()),
            "purge_groups should use -G, not -aG"
        );
    }

    /// Regression for the dan.vgo / girlslaptop "lost sudo_nopasswd" cascade
    /// observed in 0.21.x. Original behavior: a `groups: [docker]` request
    /// against an envoy without docker installed → `usermod -aG docker dan`
    /// fails with "group 'docker' does not exist" → user executor early-
    /// returns → sudoers step never runs → operator loses NOPASSWD access.
    ///
    /// New behavior: `partition_groups` filters `docker` out of the
    /// usermod call before it runs (because getent returned a known-groups
    /// set that doesn't contain docker), records "groups not found"
    /// as a partial error, and the sudoers step still runs. The result
    /// reports `error = Some(...)` with the partial-error context, but
    /// the sudoers file IS written and the user retains sudo access.
    #[test]
    fn missing_group_does_not_block_sudoers() {
        let dir = tempfile::TempDir::new().unwrap();
        let passwd = dir.path().join("passwd");
        let shadow = dir.path().join("shadow");
        let sudoers_dir = dir.path().join("sudoers.d");
        std::fs::write(
            &passwd,
            "root:x:0:0:root:/root:/bin/bash\ndan:x:1001:1001::/home/dan:/bin/bash\n",
        )
        .unwrap();
        std::fs::write(
            &shadow,
            "root:*:19000:0:99999:7:::\ndan:$6$abc:19000:0:99999:7:::\n",
        )
        .unwrap();
        std::fs::create_dir_all(&sudoers_dir).unwrap();

        // Mock call sequence:
        //   1. id -nG dan       → "dan" (no supplementary groups)
        //   2. getent group     → known groups: root, dan, audio (NOT docker)
        // After these two, partition_groups filters `docker` out of the
        // request entirely, so usermod is NOT called at all. The next
        // expected call would be sudoers (which is written via std::fs,
        // not via the runner) — so no further mock responses needed.
        let mock = MockRunner::new(vec![
            ok_output("dan"),
            ok_output("root:x:0:\ndan:x:1001:\naudio:x:29:\n"),
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = UserExecutor {
            runner: Box::new(mock),
            passwd_path: passwd.to_str().unwrap().to_string(),
            shadow_path: shadow.to_str().unwrap().to_string(),
            sudoers_dir: sudoers_dir.to_str().unwrap().to_string(),
            policy_dir: dir.path().join("policy").to_str().unwrap().to_string(),
        };

        let result = exec.execute(
            &make_params(&[
                ("username", "dan"),
                ("state", "present"),
                ("groups", "docker"),
                ("sudo_nopasswd", "true"),
            ]),
            false,
        );

        // Action is Updated because we did some work (sudoers).
        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);

        // Error is set because the missing group is recorded as a
        // partial error — operator visibility is preserved.
        let err = result.error.expect("partial error should be set");
        assert!(
            err.contains("groups not found on system"),
            "expected partial error to mention missing groups, got: {err}"
        );
        assert!(
            err.contains("docker"),
            "expected partial error to name the missing group, got: {err}"
        );

        // Critical: the sudoers file MUST exist on disk. This is the
        // step that the original cascading-failure bug was preventing.
        let sudoers_path = sudoers_dir.join("dan");
        let sudoers_content =
            std::fs::read_to_string(&sudoers_path).expect("sudoers file must exist");
        assert!(
            sudoers_content.contains("NOPASSWD"),
            "sudoers content must include NOPASSWD, got: {sudoers_content}"
        );

        // Critical: usermod was NOT called for the missing group. The
        // mock had only two queued responses (id, getent) so any
        // additional runner call would have panicked the test. We assert
        // explicitly here for clarity.
        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 2, "expected exactly id + getent, got: {:?}", calls);
        assert_eq!(calls[0].0, "id");
        assert_eq!(calls[1].0, "getent");
    }

    /// A failing usermod -aG (e.g. group exists in NSS but usermod
    /// returns an error for some other reason) must not prevent the
    /// sudoers step from running. This covers the case where the
    /// known-groups filter passes a group through but usermod itself
    /// fails — the partial-error accumulator still applies.
    #[test]
    fn usermod_failure_does_not_block_sudoers() {
        let dir = tempfile::TempDir::new().unwrap();
        let passwd = dir.path().join("passwd");
        let shadow = dir.path().join("shadow");
        let sudoers_dir = dir.path().join("sudoers.d");
        std::fs::write(
            &passwd,
            "root:x:0:0:root:/root:/bin/bash\ndan:x:1001:1001::/home/dan:/bin/bash\n",
        )
        .unwrap();
        std::fs::write(
            &shadow,
            "root:*:19000:0:99999:7:::\ndan:$6$abc:19000:0:99999:7:::\n",
        )
        .unwrap();
        std::fs::create_dir_all(&sudoers_dir).unwrap();

        // docker is in getent output, so partition_groups passes it
        // through to usermod. usermod then returns failure (mocked).
        // The sudoers step must still run.
        let mock = MockRunner::new(vec![
            ok_output("dan"),                                                       // id -nG
            ok_output("root:x:0:\ndan:x:1001:\ndocker:x:998:\n"),                   // getent group
            fail_output("usermod: failed to allocate memory or similar"),           // usermod -aG
        ]);
        let exec = UserExecutor {
            runner: Box::new(mock),
            passwd_path: passwd.to_str().unwrap().to_string(),
            shadow_path: shadow.to_str().unwrap().to_string(),
            sudoers_dir: sudoers_dir.to_str().unwrap().to_string(),
            policy_dir: dir.path().join("policy").to_str().unwrap().to_string(),
        };

        let result = exec.execute(
            &make_params(&[
                ("username", "dan"),
                ("state", "present"),
                ("groups", "docker"),
                ("sudo_nopasswd", "true"),
            ]),
            false,
        );

        assert_eq!(result.action, Action::Updated);
        let err = result.error.expect("usermod failure should be in partial errors");
        assert!(err.contains("usermod -aG"), "expected usermod error, got: {err}");

        // sudoers file MUST still exist despite usermod failure.
        let sudoers_path = sudoers_dir.join("dan");
        assert!(sudoers_path.exists(), "sudoers file must exist even after usermod failure");
    }

    /// Regression for the dan + dan-desktop sudoers-deletion bug
    /// (0.21.21). Two `user` resources for the same target user, where
    /// only one sets `sudo_nopasswd`, used to clobber each other:
    /// the second resource's check phase saw `desired = None` against
    /// an existing sudoers file, marked `sudoers_changed = true`, and
    /// the apply phase deleted the file via `apply_sudoers`'s `None`
    /// branch. After the fix, a user resource with no sudo params
    /// leaves any existing sudoers file alone — the same way it
    /// leaves shell, groups, and password alone when those params are
    /// absent.
    #[test]
    fn no_sudo_params_does_not_remove_existing_sudoers() {
        let dir = tempfile::TempDir::new().unwrap();
        let passwd = dir.path().join("passwd");
        let shadow = dir.path().join("shadow");
        let sudoers_dir = dir.path().join("sudoers.d");
        std::fs::write(
            &passwd,
            "root:x:0:0:root:/root:/bin/bash\ndan:x:1001:1001::/home/dan:/bin/bash\n",
        )
        .unwrap();
        std::fs::write(
            &shadow,
            "root:*:19000:0:99999:7:::\ndan:$6$abc:19000:0:99999:7:::\n",
        )
        .unwrap();
        std::fs::create_dir_all(&sudoers_dir).unwrap();

        // Pre-populate the sudoers file the way the `dan` resource
        // would have written it earlier in the convergence cycle.
        let sudoers_path = sudoers_dir.join("dan");
        std::fs::write(&sudoers_path, "dan ALL=(ALL) NOPASSWD: ALL\n").unwrap();

        // Now run the equivalent of the `dan-desktop` resource: same
        // username, NO sudo params, no other params that would force
        // an update either (in real life dan-desktop has desktop:
        // cinnamon but we're isolating the sudoers logic). The
        // executor should NOT touch the sudoers file.
        let mock = MockRunner::new(vec![
            ok_output("dan"), // id -nG (current_groups during groups check)
        ]);
        let exec = UserExecutor {
            runner: Box::new(mock),
            passwd_path: passwd.to_str().unwrap().to_string(),
            shadow_path: shadow.to_str().unwrap().to_string(),
            sudoers_dir: sudoers_dir.to_str().unwrap().to_string(),
            policy_dir: dir.path().join("policy").to_str().unwrap().to_string(),
        };

        let result = exec.execute(
            &make_params(&[
                ("username", "dan"),
                ("state", "present"),
                // intentionally NO sudo_nopasswd, NO sudo_passwd
            ]),
            false,
        );

        // Critical assertion: the sudoers file must STILL exist with
        // its original content. The `dan-desktop`-style resource
        // hasn't claimed any opinion on sudo, so it must leave it alone.
        assert!(
            sudoers_path.exists(),
            "sudoers file was deleted by a user resource that had no sudo params"
        );
        let content = std::fs::read_to_string(&sudoers_path).unwrap();
        assert_eq!(
            content, "dan ALL=(ALL) NOPASSWD: ALL\n",
            "sudoers file content was modified by a no-sudo-params resource"
        );

        // The action should be None — there was nothing for this
        // resource to do (no shell change, no groups, no keys, etc.,
        // and explicitly no sudo claim either).
        assert_eq!(
            result.action, Action::None,
            "no-op resource should report action=None, got: {:?}",
            result.action
        );
        assert!(
            !result.changed,
            "no-op resource should report changed=false"
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
            policy_dir: dir.path().join("policy").to_str().unwrap().to_string(),
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
            policy_dir: dir.path().join("policy").to_str().unwrap().to_string(),
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
            policy_dir: dir.path().join("policy").to_str().unwrap().to_string(),
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
            policy_dir: dir.path().join("policy").to_str().unwrap().to_string(),
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
            policy_dir: dir.path().join("policy").to_str().unwrap().to_string(),
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
            policy_dir: dir.path().join("policy").to_str().unwrap().to_string(),
        };

        let result = exec.execute(
            &make_params(&[("username", "deploy"), ("state", "absent")]),
            false,
        );

        assert!(result.changed);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        assert!(!sudoers.join("deploy").exists());
    }

    // ---- 0.41.0 usercrate-policy reconcile -----------------------

    /// Build a UserExecutor whose passwd/shadow/sudoers/policy paths
    /// all live inside `dir`. Tests of the policy reconcile use this
    /// to keep `policy_dir` under tempdir control.
    fn user_exec_with_policy_dir(
        runner: MockRunner,
        dir: &std::path::Path,
    ) -> (UserExecutor, std::path::PathBuf) {
        let passwd = dir.join("passwd");
        let shadow = dir.join("shadow");
        std::fs::write(&passwd, "root:x:0:0:root:/root:/bin/bash\ndan:x:1001:1001:Dan:/home/dan:/bin/bash\n").unwrap();
        std::fs::write(&shadow, "root:*:19000:0:99999:7:::\ndan:!:19000:0:99999:7:::\n").unwrap();
        let policy_dir = dir.join("policy");
        let exec = UserExecutor {
            runner: Box::new(runner),
            passwd_path: passwd.to_str().unwrap().to_string(),
            shadow_path: shadow.to_str().unwrap().to_string(),
            sudoers_dir: dir.join("sudoers.d").to_str().unwrap().to_string(),
            policy_dir: policy_dir.to_str().unwrap().to_string(),
        };
        (exec, policy_dir)
    }

    #[test]
    fn reconcile_writes_policy_cache_for_present_user() {
        let dir = tempfile::TempDir::new().unwrap();
        let mock = MockRunner::new(vec![
            ok_output("dan"),
            ok_output("root:x:0:\ndan:x:1001:\n"),
        ]);
        let (exec, policy_dir) = user_exec_with_policy_dir(mock, dir.path());
        let result = exec.execute(
            &make_params(&[
                ("username", "dan"),
                ("state", "present"),
                ("home", "/home/dan"),
                ("puddle", "true"),
                ("gitback", "false"),
                ("curator", "true"),
            ]),
            false,
        );
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        let cached = crate::swarm::policy::read_user_policy_at(&policy_dir, "dan").unwrap();
        assert!(cached.puddle);
        assert!(!cached.gitback);
        assert!(cached.curator);
    }

    #[test]
    fn reconcile_omits_unset_flags_as_false() {
        let dir = tempfile::TempDir::new().unwrap();
        let mock = MockRunner::new(vec![
            ok_output("dan"),
            ok_output("root:x:0:\ndan:x:1001:\n"),
        ]);
        let (exec, policy_dir) = user_exec_with_policy_dir(mock, dir.path());
        // No `puddle:` / `gitback:` / `curator:` keys → all default false.
        exec.execute(
            &make_params(&[
                ("username", "dan"),
                ("state", "present"),
                ("home", "/home/dan"),
            ]),
            false,
        );
        let cached = crate::swarm::policy::read_user_policy_at(&policy_dir, "dan").unwrap();
        assert!(!cached.puddle);
        assert!(!cached.gitback);
        assert!(!cached.curator);
    }

    #[test]
    fn reconcile_absent_state_removes_cache() {
        let dir = tempfile::TempDir::new().unwrap();
        // Pre-seed a cache file.
        let policy_dir = dir.path().join("policy");
        crate::swarm::policy::write_user_policy_at(
            &policy_dir,
            "ghost",
            &crate::swarm::policy::UserPolicy { puddle: true, gitback: true, curator: true, poolq: true },
        )
        .unwrap();
        assert!(policy_dir.join("ghost.json").exists());

        let mock = MockRunner::new(vec![ok_output("")]);
        let (exec, _) = user_exec_with_policy_dir(mock, dir.path());
        // ghost doesn't exist in passwd → state: absent is a no-op
        // for userdel, but the policy reconcile still scrubs the
        // orphaned cache (the wrapper runs after the inner whether
        // or not anything changed).
        exec.execute(
            &make_params(&[("username", "ghost"), ("state", "absent")]),
            false,
        );
        assert!(
            !policy_dir.join("ghost.json").exists(),
            "absent state must drop the policy cache file"
        );
    }

    #[test]
    fn reconcile_dry_run_does_not_write_cache() {
        let dir = tempfile::TempDir::new().unwrap();
        let mock = MockRunner::empty();
        let (exec, policy_dir) = user_exec_with_policy_dir(mock, dir.path());
        exec.execute(
            &make_params(&[
                ("username", "dan"),
                ("state", "present"),
                ("home", "/home/dan"),
                ("puddle", "true"),
            ]),
            true, // dry_run
        );
        assert!(!policy_dir.join("dan.json").exists());
    }
}
