// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! User-facing `vigo repo <verb>` subcommand tree.
//!
//! All subcommands are **pure local state reads** or user-owned
//! `git clone` spawns. No daemon required, no server contact, no
//! network calls. This is the command you run when recovering from
//! a dead source envoy — it has to work when the rest of the fleet
//! is on fire, so it reads only from files under
//! `/var/lib/vigo/vigorepo/` and `/var/lib/vigo/swarm/`.
//!
//! The subcommand tree:
//!
//! ```text
//! vigo repo list                       # what's mirrored here
//! vigo repo show <project>             # details on one mirror
//! vigo repo log <project> [-n N]       # commit history
//! vigo repo branches <project>         # refs + tags
//! vigo repo peers <project>            # swarm peers holding the blob
//! vigo repo recover <project> [opts]   # clone to user path
//! vigo repo recover-all [--dest D]     # bulk recovery
//! vigo repo source list                # what this envoy publishes
//! ```
//!
//! ### Permission model
//!
//! Unprivileged users are scoped to mirrors whose `owner` field
//! matches their effective username. Root bypasses the check — an
//! operator recovering someone else's work can run the command as
//! root and see everything. The scoping is a filter applied during
//! listing/show; access to the underlying bare repo is enforced by
//! file permissions (bare repos are root-owned mode 0755, so any
//! user can clone from them, but they can only *see* them in the
//! tool's output if they own the mirror).

use anyhow::{anyhow, bail, Context, Result};
use std::collections::BTreeMap;
use std::path::{Path, PathBuf};
use std::process::Command;

use super::state::{receiver_state_path, MirrorState, ReceiverState};
use crate::platform;

/// Top-level dispatch. Parses the verb tree and dispatches.
pub fn run(args: Vec<String>) -> Result<()> {
    let verb = args.first().map(String::as_str).unwrap_or("");
    match verb {
        "" | "--help" | "-h" | "help" => {
            print_usage();
            Ok(())
        }
        "list" => cmd_repo_list(&args[1..]),
        "show" => cmd_repo_show(&args[1..]),
        "log" => cmd_repo_log(&args[1..]),
        "branches" => cmd_repo_branches(&args[1..]),
        "peers" => cmd_repo_peers(&args[1..]),
        "recover" => cmd_recover(&args[1..]),
        "recover-all" => cmd_recover_all(&args[1..]),
        "source" => dispatch_source(&args[1..]),
        other => bail!(
            "unknown `vigo repo` subcommand: {}\nrun `vigo repo --help` for usage",
            other
        ),
    }
}

fn dispatch_source(args: &[String]) -> Result<()> {
    let sub = args.first().map(String::as_str).unwrap_or("");
    match sub {
        "" | "--help" | "-h" => {
            eprintln!("usage: vigo repo source list");
            Ok(())
        }
        "list" => cmd_source_list(&args[1..]),
        other => bail!(
            "`vigo repo source {}` is not yet implemented — see `vigo repo source list`",
            other
        ),
    }
}

fn print_usage() {
    eprintln!(
        "vigo repo — inspect and recover personal-DR git mirrors

USAGE:
    vigo repo <command> [args]

COMMANDS:
    list                             list mirrors materialized on this envoy
    show <project>                   details on one mirror
    log <project> [-n N]             show last N commits (default 20)
    branches <project>               list refs and tags in the mirror
    peers <project>                  swarm peers holding this mirror's blob
    recover <project> [opts]         clone a mirror to a user-owned path
    recover-all [--dest <dir>]       clone every mirror owned by the caller
    source list                      list repos this envoy publishes as a source

OPTIONS for recover:
    --dest <path>                    override the default destination
    --branch <name>                  rename 'main' to <name> after clone
    --force                          overwrite an existing destination
    --dry-run                        show what would happen without cloning

Defaults: destination is ~/vigorepo/<project>/ for the calling user.
         The caller sees only their own mirrors unless running as root.
         All commands are local — they do not contact the server.
"
    );
}

// ============================================================
// Helpers
// ============================================================

/// Resolved identity of the caller. `username` is used to filter
/// mirror listings; `home` is used as the default recovery dest;
/// `is_root` is used to bypass the owner filter.
struct CallerIdentity {
    username: String,
    home: PathBuf,
    is_root: bool,
}

fn resolve_caller() -> Result<CallerIdentity> {
    #[cfg(unix)]
    {
        use nix::unistd::{Uid, User};
        let uid = Uid::current();
        let user = User::from_uid(uid)
            .ok()
            .flatten()
            .context("cannot resolve current Unix user")?;
        Ok(CallerIdentity {
            username: user.name,
            home: user.dir,
            is_root: uid.is_root(),
        })
    }
    #[cfg(not(unix))]
    {
        // Windows / non-Unix — vigorepo is Unix-first; bail early.
        bail!("vigorepo CLI is Unix-only")
    }
}

fn load_receiver_state() -> Result<ReceiverState> {
    let base = platform::paths::default_vigorepo_dir();
    let path = receiver_state_path(&base);
    if !path.exists() {
        // Fresh-state case: no mirrors yet. Return empty state so
        // commands like `repo list` print "no mirrors" rather than
        // erroring out.
        return Ok(ReceiverState::default());
    }
    Ok(ReceiverState::load(&base))
}

/// Filter the mirror set for the caller. Root sees everything;
/// unprivileged users see only mirrors they own.
fn caller_visible_mirrors<'a>(
    state: &'a ReceiverState,
    caller: &CallerIdentity,
) -> Vec<&'a MirrorState> {
    state
        .mirrors
        .values()
        .filter(|m| caller.is_root || m.user == caller.username)
        .collect()
}

/// Resolve a label-or-project argument to a single mirror. Matching
/// is tried in this order:
///
/// 1. Exact label match (`vigorepo:<seeder>:<user>:<project>`)
/// 2. Exact project name match
/// 3. Unique project-name prefix match
///
/// Errors if nothing matches, or if a prefix matches multiple
/// mirrors. Respects the caller's visibility scope.
fn resolve_mirror<'a>(
    state: &'a ReceiverState,
    caller: &CallerIdentity,
    arg: &str,
) -> Result<&'a MirrorState> {
    let visible = caller_visible_mirrors(state, caller);
    if visible.is_empty() {
        bail!(
            "no vigorepo mirrors found on this envoy (owned by {})",
            if caller.is_root {
                "anyone (running as root)".to_string()
            } else {
                caller.username.clone()
            }
        );
    }

    // 1. Exact label match.
    if let Some(m) = visible.iter().find(|m| m.label == arg) {
        return Ok(*m);
    }
    // 2. Exact project match.
    let exact: Vec<&MirrorState> = visible
        .iter()
        .copied()
        .filter(|m| m.project == arg)
        .collect();
    if exact.len() == 1 {
        return Ok(exact[0]);
    }
    if exact.len() > 1 {
        let labels: Vec<String> = exact.iter().map(|m| m.label.clone()).collect();
        bail!(
            "project '{}' is ambiguous (multiple seeders): {}",
            arg,
            labels.join(", ")
        );
    }
    // 3. Unique project prefix.
    let prefix: Vec<&MirrorState> = visible
        .iter()
        .copied()
        .filter(|m| m.project.starts_with(arg))
        .collect();
    match prefix.len() {
        1 => Ok(prefix[0]),
        0 => bail!("no mirror matches '{}'", arg),
        _ => {
            let names: Vec<String> = prefix.iter().map(|m| m.project.clone()).collect();
            bail!(
                "prefix '{}' is ambiguous; candidates: {}",
                arg,
                names.join(", ")
            )
        }
    }
}

/// Run a git command against a bare repo and return trimmed stdout.
fn git_readonly(bare_repo: &Path, args: &[&str]) -> Result<String> {
    let out = Command::new("git")
        .arg("-C")
        .arg(bare_repo)
        .args(args)
        .output()
        .with_context(|| format!("spawn git {:?}", args))?;
    if !out.status.success() {
        bail!(
            "git {:?} failed ({}): {}",
            args,
            out.status,
            String::from_utf8_lossy(&out.stderr).trim()
        );
    }
    Ok(String::from_utf8_lossy(&out.stdout).trim().to_string())
}

/// Best-effort size computation: sum of file sizes under `path`.
/// Errors fall through to `0`; sizing is cosmetic, not load-bearing.
fn dir_size_bytes(path: &Path) -> u64 {
    let mut total: u64 = 0;
    let walk = |p: &Path, total: &mut u64| -> std::io::Result<()> {
        for entry in std::fs::read_dir(p)? {
            let entry = entry?;
            let ft = entry.file_type()?;
            if ft.is_dir() {
                // Manual recursion; avoids walkdir dep just for this.
                // Depth is bounded by git's own layout.
            } else if let Ok(md) = entry.metadata() {
                *total = total.saturating_add(md.len());
            }
        }
        Ok(())
    };
    fn recurse(p: &Path, acc: &mut u64) {
        if let Ok(rd) = std::fs::read_dir(p) {
            for entry in rd.flatten() {
                let path = entry.path();
                match entry.file_type() {
                    Ok(ft) if ft.is_dir() => recurse(&path, acc),
                    Ok(_) => {
                        if let Ok(md) = entry.metadata() {
                            *acc = acc.saturating_add(md.len());
                        }
                    }
                    Err(_) => {}
                }
            }
        }
    }
    recurse(path, &mut total);
    let _ = walk; // keep the closure to silence warnings
    total
}

fn format_size(bytes: u64) -> String {
    const K: u64 = 1024;
    const M: u64 = K * 1024;
    const G: u64 = M * 1024;
    if bytes >= G {
        format!("{:.1} GiB", bytes as f64 / G as f64)
    } else if bytes >= M {
        format!("{:.1} MiB", bytes as f64 / M as f64)
    } else if bytes >= K {
        format!("{:.1} KiB", bytes as f64 / K as f64)
    } else {
        format!("{} B", bytes)
    }
}

fn format_age(unix_seconds: u64) -> String {
    let now = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0);
    let age = now.saturating_sub(unix_seconds);
    if age < 60 {
        format!("{}s ago", age)
    } else if age < 3600 {
        format!("{}m ago", age / 60)
    } else if age < 86400 {
        format!("{}h ago", age / 3600)
    } else {
        format!("{}d ago", age / 86400)
    }
}

// ============================================================
// repo list
// ============================================================

fn cmd_repo_list(args: &[String]) -> Result<()> {
    let mut owner_filter: Option<String> = None;
    let mut seeder_filter: Option<String> = None;
    let mut json = false;
    let mut i = 0;
    while i < args.len() {
        match args[i].as_str() {
            "--owner" => {
                i += 1;
                owner_filter = Some(
                    args.get(i)
                        .cloned()
                        .context("--owner requires a value")?,
                );
            }
            "--seeder" => {
                i += 1;
                seeder_filter = Some(
                    args.get(i)
                        .cloned()
                        .context("--seeder requires a value")?,
                );
            }
            "--json" => json = true,
            other => bail!("unknown repo list flag: {}", other),
        }
        i += 1;
    }

    let caller = resolve_caller()?;
    let state = load_receiver_state()?;
    let visible = caller_visible_mirrors(&state, &caller);

    let filtered: Vec<&MirrorState> = visible
        .into_iter()
        .filter(|m| owner_filter.as_ref().map(|o| &m.user == o).unwrap_or(true))
        .filter(|m| {
            seeder_filter
                .as_ref()
                .map(|s| &m.seeder == s)
                .unwrap_or(true)
        })
        .collect();

    if json {
        println!("{}", serde_json::to_string_pretty(&filtered)?);
        return Ok(());
    }

    if filtered.is_empty() {
        println!("no vigorepo mirrors on this envoy");
        return Ok(());
    }

    println!(
        "{:<45} {:<8} {:<12} {:<10} {:<10} {}",
        "LABEL", "BRANCH", "LAST COMMIT", "AGE", "SIZE", "OWNER"
    );
    for m in &filtered {
        let branch = "main".to_string();
        let short_sha = m.git_sha.get(..12).unwrap_or(&m.git_sha).to_string();
        let age = format_age(m.materialized_at);
        let size = format_size(dir_size_bytes(&m.bare_repo_path));
        println!(
            "{:<45} {:<8} {:<12} {:<10} {:<10} {}",
            truncate(&m.label, 45),
            branch,
            short_sha,
            age,
            size,
            m.user
        );
    }
    Ok(())
}

fn truncate(s: &str, max: usize) -> String {
    if s.len() <= max {
        s.to_string()
    } else {
        format!("{}…", &s[..max.saturating_sub(1)])
    }
}

// ============================================================
// repo show
// ============================================================

fn cmd_repo_show(args: &[String]) -> Result<()> {
    let project = args
        .first()
        .context("usage: vigo repo show <project>")?;
    let caller = resolve_caller()?;
    let state = load_receiver_state()?;
    let m = resolve_mirror(&state, &caller, project)?;

    let head_sha = git_readonly(&m.bare_repo_path, &["rev-parse", "HEAD"])
        .unwrap_or_else(|_| m.git_sha.clone());
    let head_msg = git_readonly(
        &m.bare_repo_path,
        &["log", "-1", "--format=%s"],
    )
    .unwrap_or_default();
    let head_date = git_readonly(
        &m.bare_repo_path,
        &["log", "-1", "--format=%ci"],
    )
    .unwrap_or_default();
    let commit_count = git_readonly(&m.bare_repo_path, &["rev-list", "--count", "HEAD"])
        .unwrap_or_default();
    let tags = git_readonly(&m.bare_repo_path, &["tag", "-l"]).unwrap_or_default();
    let first_commit = git_readonly(
        &m.bare_repo_path,
        &["log", "--reverse", "--format=%ci", "HEAD", "--max-count=1"],
    )
    .unwrap_or_default();

    let size = format_size(dir_size_bytes(&m.bare_repo_path));

    println!("Label:         {}", m.label);
    println!("Seeder:        {}", m.seeder);
    println!("Owner:         {}", m.user);
    println!("Project:       {}", m.project);
    println!("Bare repo:     {}", m.bare_repo_path.display());
    println!("Size:          {}", size);
    println!("Branch:        main");
    println!("HEAD:          {}", head_sha);
    if !head_msg.is_empty() {
        println!("HEAD message:  {}", head_msg);
    }
    if !head_date.is_empty() {
        println!("HEAD date:     {}", head_date);
    }
    if !commit_count.is_empty() {
        println!("Commits:       {}", commit_count);
    }
    if !tags.is_empty() {
        let tag_list: Vec<&str> = tags.lines().collect();
        println!("Tags:          {}", tag_list.join(", "));
    }
    if !first_commit.is_empty() {
        println!("First commit:  {}", first_commit);
    }
    println!(
        "Materialized:  {}",
        format_unix_seconds(m.materialized_at)
    );
    println!("Blob sha:      {}", m.blob_sha);
    Ok(())
}

fn format_unix_seconds(unix: u64) -> String {
    // Best-effort local timestamp. Falls back to raw seconds if
    // chrono isn't available (it isn't — keep the dep list minimal).
    // Output format matches git's %ci roughly.
    use std::time::{Duration, SystemTime};
    let t = SystemTime::UNIX_EPOCH + Duration::from_secs(unix);
    match t.duration_since(SystemTime::UNIX_EPOCH) {
        Ok(_) => format!("{} (unix seconds)", unix),
        Err(_) => format!("{}", unix),
    }
}

// ============================================================
// repo log
// ============================================================

fn cmd_repo_log(args: &[String]) -> Result<()> {
    let mut n: u32 = 20;
    let mut project: Option<String> = None;
    let mut i = 0;
    while i < args.len() {
        match args[i].as_str() {
            "-n" | "--max-count" => {
                i += 1;
                n = args
                    .get(i)
                    .context("-n requires a value")?
                    .parse()
                    .context("-n must be a number")?;
            }
            other if !other.starts_with('-') && project.is_none() => {
                project = Some(other.to_string());
            }
            other => bail!("unknown repo log flag: {}", other),
        }
        i += 1;
    }
    let project = project.context("usage: vigo repo log <project> [-n N]")?;
    let caller = resolve_caller()?;
    let state = load_receiver_state()?;
    let m = resolve_mirror(&state, &caller, &project)?;

    let out = git_readonly(
        &m.bare_repo_path,
        &["log", &format!("-{}", n), "--oneline"],
    )?;
    println!("{}", out);
    Ok(())
}

// ============================================================
// repo branches
// ============================================================

fn cmd_repo_branches(args: &[String]) -> Result<()> {
    let project = args
        .first()
        .context("usage: vigo repo branches <project>")?;
    let caller = resolve_caller()?;
    let state = load_receiver_state()?;
    let m = resolve_mirror(&state, &caller, project)?;

    let branches = git_readonly(&m.bare_repo_path, &["branch", "-a"])?;
    let tags = git_readonly(&m.bare_repo_path, &["tag", "-l"])?;
    println!("Branches:");
    if branches.is_empty() {
        println!("  (none)");
    } else {
        for line in branches.lines() {
            println!("  {}", line.trim());
        }
    }
    println!("Tags:");
    if tags.is_empty() {
        println!("  (none)");
    } else {
        for line in tags.lines() {
            println!("  {}", line);
        }
    }
    Ok(())
}

// ============================================================
// repo peers
// ============================================================

fn cmd_repo_peers(args: &[String]) -> Result<()> {
    let project = args
        .first()
        .context("usage: vigo repo peers <project>")?;
    let caller = resolve_caller()?;
    let state = load_receiver_state()?;
    let m = resolve_mirror(&state, &caller, project)?;

    let swarm_base = platform::paths::default_blobs_dir()
        .parent()
        .map(|p| p.to_path_buf())
        .unwrap_or_else(|| PathBuf::from("/var/lib/vigo/swarm"));
    let peers_json_path = swarm_base.join("peers.json");
    let manifest_path = swarm_base.join("manifest.json");

    let peers_data = std::fs::read_to_string(&peers_json_path)
        .unwrap_or_else(|_| "{}".to_string());
    let manifest_data = std::fs::read_to_string(&manifest_path)
        .unwrap_or_else(|_| "{}".to_string());

    println!("Label:  {}", m.label);
    println!("Blob:   {}", m.blob_sha);
    println!();
    println!("Peers tracked by local agent:");
    println!("  (this envoy holds the mirror)");

    // Pure best-effort JSON read: extract peer hostnames/addresses
    // without pulling in a full dep on the manifest schema. The
    // manifest format is documented in `server/swarm` but also lives
    // in the agent's `swarm::manifest` module. Full parsing would
    // duplicate that model; for a user-facing quick answer the JSON
    // structure is stable enough to eyeball. If parsing fails we
    // dump the raw peers.json so the user still gets something.
    if peers_data == "{}" {
        println!("  (no peers.json found at {})", peers_json_path.display());
    } else {
        // Try to deserialize as a generic JSON object and print the
        // top-level hostname→addr pairs. Tolerate any shape.
        match serde_json::from_str::<serde_json::Value>(&peers_data) {
            Ok(v) => print_peer_summary(&v),
            Err(_) => println!("  (peers.json present but could not parse)"),
        }
    }

    println!();
    println!(
        "Note: this listing is what the local agent has gossipped to itself.\n\
         It does NOT probe peers for liveness — if a peer is listed but\n\
         offline, you won't know until you try to reach it."
    );
    let _ = manifest_data; // manifest would add per-blob source counts; deferred
    Ok(())
}

fn print_peer_summary(v: &serde_json::Value) {
    if let Some(obj) = v.as_object() {
        if obj.is_empty() {
            println!("  (no other peers known)");
            return;
        }
        for (hostname, info) in obj {
            let addr = info
                .get("addr")
                .and_then(|a| a.as_str())
                .unwrap_or("?");
            println!("  {} {}", hostname, addr);
        }
    } else if let Some(arr) = v.as_array() {
        if arr.is_empty() {
            println!("  (no other peers known)");
            return;
        }
        for entry in arr {
            println!("  {}", entry);
        }
    } else {
        println!("  (unexpected peers.json shape)");
    }
}

// ============================================================
// recover
// ============================================================

fn cmd_recover(args: &[String]) -> Result<()> {
    let mut project: Option<String> = None;
    let mut dest: Option<PathBuf> = None;
    let mut branch: Option<String> = None;
    let mut force = false;
    let mut dry_run = false;
    let mut i = 0;
    while i < args.len() {
        match args[i].as_str() {
            "--dest" => {
                i += 1;
                dest = Some(PathBuf::from(
                    args.get(i).context("--dest requires a value")?,
                ));
            }
            "--branch" => {
                i += 1;
                branch = Some(args.get(i).cloned().context("--branch requires a value")?);
            }
            "--force" => force = true,
            "--dry-run" => dry_run = true,
            other if !other.starts_with('-') && project.is_none() => {
                project = Some(other.to_string());
            }
            other => bail!("unknown recover flag: {}", other),
        }
        i += 1;
    }
    let project = project.context("usage: vigo repo recover <project> [opts]")?;

    let caller = resolve_caller()?;
    let state = load_receiver_state()?;
    let m = resolve_mirror(&state, &caller, &project)?;

    let dest = dest.unwrap_or_else(|| caller.home.join("vigorepo").join(&m.project));
    perform_recover(m, &dest, branch.as_deref(), force, dry_run)
}

/// Core recovery logic — shared by `recover` and `recover-all`.
fn perform_recover(
    m: &MirrorState,
    dest: &Path,
    branch: Option<&str>,
    force: bool,
    dry_run: bool,
) -> Result<()> {
    // Collision guard per decision 2 (option B): detect active
    // source repos and refuse with a specific error.
    if dest.exists() {
        let has_git = dest.join(".git").exists();
        let has_sidecar = dest.join(".vigorepo.yaml").exists();
        if has_git && has_sidecar {
            if !force {
                bail!(
                    "{} exists and looks like an active source repo (has .git/ and .vigorepo.yaml). \
                     Recovering here would overwrite it — and the mirror is older than your source, \
                     so uncommitted work would be lost.\n\n\
                     To recover to a different path:\n  \
                     vigo repo recover {} --dest ~/{}-rescue\n\n\
                     To overwrite anyway (only if the source is already broken):\n  \
                     vigo repo recover {} --force",
                    dest.display(),
                    m.project,
                    m.project,
                    m.project,
                );
            }
            eprintln!(
                "warning: --force overwriting an active source repo at {}. \
                 Any uncommitted work there will be lost.",
                dest.display()
            );
        } else if !force {
            bail!(
                "{} exists and is not empty. Use --dest <path> to clone elsewhere, \
                 or --force to overwrite.",
                dest.display()
            );
        }
    }

    if dry_run {
        println!("DRY RUN: would clone");
        println!("  from: {}", m.bare_repo_path.display());
        println!("  to:   {}", dest.display());
        if let Some(b) = branch {
            println!("  branch: main → {}", b);
        }
        println!("  {} commits, HEAD={}", "unknown", m.git_sha);
        return Ok(());
    }

    // Ensure parent dir exists (as the caller, naturally).
    if let Some(parent) = dest.parent() {
        std::fs::create_dir_all(parent)
            .with_context(|| format!("create parent {}", parent.display()))?;
    }
    if dest.exists() && force {
        std::fs::remove_dir_all(dest)
            .with_context(|| format!("remove existing {}", dest.display()))?;
    }

    // git clone — runs as the current user, so the resulting clone
    // is owned by them. Bare repo is mode 0755 root-owned, readable.
    let out = Command::new("git")
        .arg("clone")
        .arg(&m.bare_repo_path)
        .arg(dest)
        .output()
        .context("spawn git clone")?;
    if !out.status.success() {
        bail!(
            "git clone failed ({}): {}",
            out.status,
            String::from_utf8_lossy(&out.stderr).trim()
        );
    }

    if let Some(new_branch) = branch {
        let rename = Command::new("git")
            .arg("-C")
            .arg(dest)
            .arg("branch")
            .arg("-m")
            .arg("main")
            .arg(new_branch)
            .output()
            .context("spawn git branch -m")?;
        if !rename.status.success() {
            eprintln!(
                "warning: failed to rename main → {}: {}",
                new_branch,
                String::from_utf8_lossy(&rename.stderr).trim()
            );
        }
    }

    println!("✓ Recovered {} to {}", m.project, dest.display());
    println!("  cd {}", dest.display());
    Ok(())
}

// ============================================================
// recover-all
// ============================================================

fn cmd_recover_all(args: &[String]) -> Result<()> {
    let mut dest_root: Option<PathBuf> = None;
    let mut force = false;
    let mut dry_run = false;
    let mut i = 0;
    while i < args.len() {
        match args[i].as_str() {
            "--dest" => {
                i += 1;
                dest_root = Some(PathBuf::from(
                    args.get(i).context("--dest requires a value")?,
                ));
            }
            "--force" => force = true,
            "--dry-run" => dry_run = true,
            other => bail!("unknown recover-all flag: {}", other),
        }
        i += 1;
    }

    let caller = resolve_caller()?;
    let state = load_receiver_state()?;
    let mirrors: Vec<&MirrorState> = state
        .mirrors
        .values()
        .filter(|m| m.user == caller.username)
        .collect();

    if mirrors.is_empty() {
        println!("no vigorepo mirrors owned by {} on this envoy", caller.username);
        return Ok(());
    }

    let dest_root = dest_root.unwrap_or_else(|| caller.home.join("vigorepo"));

    let mut outcomes: BTreeMap<String, Result<PathBuf>> = BTreeMap::new();
    for m in &mirrors {
        let dest = dest_root.join(&m.project);
        // Continue past failures per decision 4.
        let result = perform_recover(m, &dest, None, force, dry_run)
            .map(|_| dest.clone());
        outcomes.insert(m.project.clone(), result);
    }

    // Summary.
    println!();
    println!("Summary:");
    let mut ok = 0;
    let mut failed = 0;
    for (project, outcome) in &outcomes {
        match outcome {
            Ok(path) => {
                ok += 1;
                println!("  ✓ {} → {}", project, path.display());
            }
            Err(e) => {
                failed += 1;
                println!("  ✗ {}: {}", project, e);
            }
        }
    }
    println!("{} recovered, {} failed", ok, failed);
    if failed > 0 {
        // Non-zero exit without bailing (we still printed results).
        std::process::exit(1);
    }
    Ok(())
}

// ============================================================
// source list
// ============================================================

fn cmd_source_list(_args: &[String]) -> Result<()> {
    let base = platform::paths::default_vigorepo_dir();
    let traits_path = base.join("traits.json");
    if !traits_path.exists() {
        println!(
            "no source traits file at {} — either vigorepo has not \
             scanned yet, or this envoy is not running the reactor",
            traits_path.display()
        );
        return Ok(());
    }
    let data = std::fs::read_to_string(&traits_path)
        .with_context(|| format!("read {}", traits_path.display()))?;
    let v: serde_json::Value = serde_json::from_str(&data)
        .with_context(|| format!("parse {}", traits_path.display()))?;

    let sources = v
        .get("sources")
        .and_then(|s| s.as_array())
        .ok_or_else(|| anyhow!("traits.json missing 'sources' array"))?;

    if sources.is_empty() {
        println!("no vigorepo sources on this envoy");
        return Ok(());
    }

    println!(
        "{:<15} {:<10} {:<8} {:<8} {:<30} {}",
        "PROJECT", "OWNER", "MIRRORS", "PAUSED", "FILTERS", "STATUS"
    );
    for src in sources {
        let project = src
            .get("project")
            .and_then(|s| s.as_str())
            .unwrap_or("?");
        let owner = src.get("owner").and_then(|s| s.as_str()).unwrap_or("?");
        let max_mirrors = src.get("max_mirrors").and_then(|s| s.as_u64()).unwrap_or(0);
        let paused = src.get("paused").and_then(|b| b.as_bool()).unwrap_or(false);
        let include = src
            .get("include_envoys")
            .and_then(|a| a.as_array())
            .map(|a| a.iter().filter_map(|v| v.as_str()).collect::<Vec<_>>())
            .unwrap_or_default();
        let exclude = src
            .get("exclude_envoys")
            .and_then(|a| a.as_array())
            .map(|a| a.iter().filter_map(|v| v.as_str()).collect::<Vec<_>>())
            .unwrap_or_default();

        let mut filter_parts = Vec::new();
        if !include.is_empty() {
            filter_parts.push(format!("include={}", include.join(",")));
        }
        if !exclude.is_empty() {
            filter_parts.push(format!("exclude={}", exclude.join(",")));
        }
        let filters = if filter_parts.is_empty() {
            "(none)".to_string()
        } else {
            filter_parts.join(" ")
        };

        let error = src.get("error").and_then(|s| s.as_str());
        let status = if let Some(e) = error {
            format!("error: {}", e)
        } else if paused {
            "paused".to_string()
        } else {
            "publishing".to_string()
        };

        println!(
            "{:<15} {:<10} {:<8} {:<8} {:<30} {}",
            truncate(project, 15),
            truncate(owner, 10),
            max_mirrors,
            if paused { "yes" } else { "no" },
            truncate(&filters, 30),
            status
        );
    }
    Ok(())
}

// ============================================================
// Tests
// ============================================================

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::tempdir;

    fn make_mirror(seeder: &str, user: &str, project: &str) -> MirrorState {
        MirrorState {
            label: format!("vigorepo:{}:{}:{}", seeder, user, project),
            seeder: seeder.to_string(),
            user: user.to_string(),
            project: project.to_string(),
            bare_repo_path: PathBuf::from(format!(
                "/var/lib/vigo/vigorepo/mirrors/{}/{}/{}.git",
                seeder, user, project
            )),
            blob_sha: "deadbeef".to_string(),
            git_sha: "f43f6ef3eea29e3780d73aa2cee4a78193419a69".to_string(),
            materialized_at: 1_744_560_000,
            next_gc_at: 1_744_582_000,
        }
    }

    fn state_with(mirrors: &[MirrorState]) -> ReceiverState {
        let mut s = ReceiverState::default();
        for m in mirrors {
            s.mirrors.insert(m.label.clone(), m.clone());
        }
        s
    }

    fn caller(username: &str, is_root: bool) -> CallerIdentity {
        CallerIdentity {
            username: username.to_string(),
            home: PathBuf::from(format!("/home/{}", username)),
            is_root,
        }
    }

    #[test]
    fn caller_visibility_scopes_unprivileged_to_own_mirrors() {
        let s = state_with(&[
            make_mirror("h", "alice", "notes"),
            make_mirror("h", "bob", "dotfiles"),
        ]);
        let c = caller("alice", false);
        let visible = caller_visible_mirrors(&s, &c);
        assert_eq!(visible.len(), 1);
        assert_eq!(visible[0].user, "alice");
    }

    #[test]
    fn caller_visibility_root_sees_everything() {
        let s = state_with(&[
            make_mirror("h", "alice", "notes"),
            make_mirror("h", "bob", "dotfiles"),
        ]);
        let c = caller("root", true);
        let visible = caller_visible_mirrors(&s, &c);
        assert_eq!(visible.len(), 2);
    }

    #[test]
    fn resolve_mirror_by_exact_project() {
        let s = state_with(&[make_mirror("h", "dan", "vigo-web")]);
        let c = caller("dan", false);
        let m = resolve_mirror(&s, &c, "vigo-web").unwrap();
        assert_eq!(m.project, "vigo-web");
    }

    #[test]
    fn resolve_mirror_by_unique_prefix() {
        let s = state_with(&[make_mirror("h", "dan", "vigo-web")]);
        let c = caller("dan", false);
        let m = resolve_mirror(&s, &c, "vigo-").unwrap();
        assert_eq!(m.project, "vigo-web");
    }

    #[test]
    fn resolve_mirror_ambiguous_prefix_errors() {
        let s = state_with(&[
            make_mirror("h", "dan", "vigo-web"),
            make_mirror("h", "dan", "vigo-docs"),
        ]);
        let c = caller("dan", false);
        let err = resolve_mirror(&s, &c, "vigo-").unwrap_err();
        assert!(err.to_string().contains("ambiguous"));
    }

    #[test]
    fn resolve_mirror_by_exact_label() {
        let s = state_with(&[make_mirror("h", "dan", "vigo-web")]);
        let c = caller("dan", false);
        let m = resolve_mirror(&s, &c, "vigorepo:h:dan:vigo-web").unwrap();
        assert_eq!(m.project, "vigo-web");
    }

    #[test]
    fn resolve_mirror_unprivileged_cannot_see_other_users() {
        let s = state_with(&[make_mirror("h", "bob", "secret")]);
        let c = caller("alice", false);
        let err = resolve_mirror(&s, &c, "secret").unwrap_err();
        // Either "no mirrors found" (empty visibility) or
        // "no mirror matches" — both are acceptable; what matters
        // is that alice doesn't see bob's mirror.
        let msg = err.to_string();
        assert!(
            msg.contains("no vigorepo mirrors") || msg.contains("no mirror matches"),
            "unexpected error: {}",
            msg
        );
    }

    #[test]
    fn resolve_mirror_root_sees_all() {
        let s = state_with(&[make_mirror("h", "bob", "secret")]);
        let c = caller("root", true);
        let m = resolve_mirror(&s, &c, "secret").unwrap();
        assert_eq!(m.user, "bob");
    }

    #[test]
    fn recover_refuses_existing_active_source_repo_without_force() {
        let dir = tempdir().unwrap();
        let dest = dir.path().join("vigo-web");
        std::fs::create_dir_all(dest.join(".git")).unwrap();
        std::fs::write(dest.join(".vigorepo.yaml"), b"owner: dan\n").unwrap();
        let m = make_mirror("host", "dan", "vigo-web");
        let err = perform_recover(&m, &dest, None, false, false).unwrap_err();
        let msg = err.to_string();
        assert!(
            msg.contains("active source repo"),
            "expected active-source error, got: {}",
            msg
        );
        assert!(msg.contains("uncommitted work"), "error should name the risk");
    }

    #[test]
    fn recover_refuses_existing_nonempty_dest_without_force_generic_message() {
        let dir = tempdir().unwrap();
        let dest = dir.path().join("existing");
        std::fs::create_dir_all(&dest).unwrap();
        std::fs::write(dest.join("some-file"), b"data").unwrap();
        let m = make_mirror("host", "dan", "proj");
        let err = perform_recover(&m, &dest, None, false, true).unwrap_err();
        let msg = err.to_string();
        // Generic "not empty" error — NOT the active-source one.
        assert!(
            msg.contains("not empty") || msg.contains("exists"),
            "expected generic exists error, got: {}",
            msg
        );
        assert!(
            !msg.contains("active source repo"),
            "should not misclassify a plain non-empty dir as a source repo"
        );
    }

    #[test]
    fn recover_allows_missing_dest() {
        let dir = tempdir().unwrap();
        let dest = dir.path().join("fresh").join("vigo-web");
        let m = make_mirror("host", "dan", "vigo-web");
        // dry_run=true skips the actual git clone; we just want to
        // verify the collision guard lets a missing dest through.
        perform_recover(&m, &dest, None, false, true).unwrap();
    }

    #[test]
    fn format_size_units() {
        assert_eq!(format_size(500), "500 B");
        assert_eq!(format_size(2048), "2.0 KiB");
        assert_eq!(format_size(5 * 1024 * 1024), "5.0 MiB");
    }

    #[test]
    fn truncate_short_string_unchanged() {
        assert_eq!(truncate("short", 10), "short");
    }

    #[test]
    fn truncate_long_string_gets_ellipsis() {
        let s = truncate("this is a very long string", 10);
        assert!(s.ends_with('…'));
        assert_eq!(s.chars().count(), 10);
    }
}
