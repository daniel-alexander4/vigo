// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! `vigo swarm swarmgit project <verb>` — ADR-015 unified-substrate CLI.
//!
//! ```text
//! vigo swarm swarmgit project init <handle> [--scope swarm-wide|members-only] [--repo <path>] [--no-push]
//! vigo swarm swarmgit project push [<handle>] [--repo <path>]
//! vigo swarm swarmgit project invite <project_id> --puddle <fp> --role <r> [--expires <Nd>]
//! vigo swarm swarmgit project join <project_id> [--invitation <file|->]
//! vigo swarm swarmgit project leave <project_id> [--purge] [--yes]
//! vigo swarm swarmgit project list [--json]
//! vigo swarm swarmgit project status <project_id>
//! vigo swarm swarmgit project revoke <project_id> --token-id <hex> [--reason <text>]
//! vigo swarm swarmgit project reanchor <project_id>
//! ```
//!
//! The verbs that mint or sign envelopes (`init`, `invite`,
//! `revoke`, `leave`, `reanchor`) require the user's puddle
//! identity to be unlocked — the session helper at
//! `$XDG_RUNTIME_DIR/vigo/puddle.sock` is the only place the
//! Ed25519 private key lives; CLI doesn't unwrap directly.
//!
//! `init` (and `push`), when run from inside a git work tree, also
//! add the `swarm` remote and run `git push swarm --all && --tags`
//! through the helper daemon — so founding a project and publishing
//! it is one command. `revoke` / `leave` still write a `*.pending`
//! sentinel the daemon's reactor picks up and signs.

use std::fs;
use std::io::{Read, Write};
use std::path::{Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

use anyhow::{anyhow, bail, Context, Result};
use serde::{Deserialize, Serialize};

use super::envelopes::DelegationEnvelope;
use super::helper::socket_path as helper_socket_path;
use super::project_state::{
    self, hex_id, parse_hex_id, project_repo_path, projects_dir, purge_user_state,
    DelegationChain, DrScope, ProjectConfig, TokenChain,
};
use crate::traits::swarmgit::{collect_projects, passwd_path, ProjectEntry};
use super::tokens::{
    canonical_bytes, issue_child as token_issue_child, project_id_for, verify_chain,
    Role, Token, TokenBody, ID_LEN, NEVER_EXPIRES, SIG_LEN,
};
use crate::swarm::puddle;

/// Top-level dispatch for `vigo swarm swarmgit …`. Called from
/// `agent/src/main.rs`. Argument shape is `[verb, …]`; an empty or
/// help-form `verb` prints usage and returns Ok.
pub fn run(args: Vec<String>) -> Result<()> {
    let verb = args.first().map(String::as_str).unwrap_or("");
    match verb {
        "" | "--help" | "-h" | "help" => {
            print_usage();
            Ok(())
        }
        "project" => with_swarmgit_authz(|| dispatch_project(&args[1..])),
        // Top-level envoy posture — parallel to `vigo swarm lockbox
        // status`. Walks every user's `~/.vigo-swarmgit/` joined with
        // the per-envoy bare repos + queued envelopes, plus the
        // helper-daemon socket check. Distinct from `vigo swarm
        // swarmgit project status <id>`, which shows one project's
        // local state in detail.
        "status" => with_swarmgit_authz(|| cmd_status_top(&args[1..])),
        // Power-user wipe of local swarmgit state on this envoy.
        // Distinct from per-project `leave --purge` (one project's
        // bare repo) and `_apply-blacklist` (server-driven). User
        // mode wipes `~/.vigo-swarmgit/` for the caller; root mode
        // also wipes the envoy-wide root-owned state.
        "purge" => with_swarmgit_authz(|| cmd_purge(&args[1..])),
        other => bail!(
            "unknown subcommand `vigo swarm swarmgit {}`. \
             Run `vigo swarm swarmgit --help` for usage.",
            other
        ),
    }
}

/// Refuse early if the invoking user lacks `swarmgit: true` in their
/// usercrate (0.41.0+). The gate consults the daemon-canonical cache
/// at `/var/lib/vigo/usercrate-policy/<user>.json` written by the
/// user executor on each reconcile pass — no server round-trip
/// required. Skipped for root: an admin running swarmgit verbs
/// against the envoy-wide root-owned state isn't acting as a
/// usercrate-managed user.
fn with_swarmgit_authz<F, T>(f: F) -> Result<T>
where
    F: FnOnce() -> Result<T>,
{
    let caller = resolve_caller()?;
    if !caller.is_root {
        let username = invoking_username()?;
        crate::swarm::policy::require_swarmgit(&username)?;
    }
    f()
}

/// Resolve the invoking user's name. Used only for the authorization
/// gate; the rest of the swarmgit CLI works in terms of the caller's
/// home dir resolved from `getuid()`.
fn invoking_username() -> Result<String> {
    #[cfg(unix)]
    {
        use nix::unistd::{Uid, User};
        let user = User::from_uid(Uid::current())
            .ok()
            .flatten()
            .context("cannot resolve current Unix user")?;
        Ok(user.name)
    }
    #[cfg(not(unix))]
    {
        bail!("swarmgit CLI is Unix-only")
    }
}

fn dispatch_project(args: &[String]) -> Result<()> {
    let sub = args.first().map(String::as_str).unwrap_or("");
    match sub {
        "" | "--help" | "-h" | "help" => {
            print_project_usage();
            Ok(())
        }
        "init" => cmd_init(&args[1..]),
        "push" => cmd_push(&args[1..]),
        "invite" => cmd_invite(&args[1..]),
        "join" => cmd_join(&args[1..]),
        "leave" => cmd_leave(&args[1..]),
        "list" => cmd_list(&args[1..]),
        "status" => cmd_status(&args[1..]),
        "revoke" => cmd_revoke(&args[1..]),
        "reanchor" => cmd_reanchor(&args[1..]),
        // ADR-019 slice 4 — multi-puddle rejoin support after a founder rekey.
        "re-invite-all" => cmd_re_invite_all(&args[1..]),
        "rejoin" => cmd_rejoin(&args[1..]),
        // ADR-019 slice 5 — escape hatch when a project's delegation
        // chain is at cap. Mints a fresh project_id from the current
        // puddle pubkey, byte-migrates, tombstones the old one.
        "re-found" => cmd_re_found(&args[1..]),
        other => bail!(
            "unknown subcommand `vigo swarm swarmgit project {}`. \
             Run `vigo swarm swarmgit project --help` for usage.",
            other
        ),
    }
}

fn print_usage() {
    eprintln!(
        "vigo swarm swarmgit — ADR-015 unified-substrate swarmgit CLI

USAGE:
    vigo swarm swarmgit <verb> [args]

VERBS:
    status              Show envoy-wide swarmgit posture (publisher +
                        receiver state, helper-daemon socket).
    purge --yes         Wipe ALL local swarmgit state (no gossip). As a
                        non-root user, scrubs ~/.vigo-swarmgit/ for the
                        calling user. As root, also scrubs the envoy-wide
                        root-owned state under /var/lib/vigo/swarm/swarmgit/.
    project <verb>      Manage swarmgit projects. Run
                        `vigo swarm swarmgit project --help` for the
                        project verb tree.
"
    );
}

fn print_project_usage() {
    eprintln!(
        "vigo swarm swarmgit project — manage swarmgit projects

USAGE:
    vigo swarm swarmgit project <verb> [args]

VERBS:
    init <handle> [--scope swarm-wide|members-only] [--repo <path>] [--no-push]
        Found a new project. Puddle must be unlocked. From inside a git
        work tree (or with --repo <path>), also adds the `swarm` remote
        pointing at swarmgit://<project_id>/<handle> and runs
        `git push swarm --all && git push swarm --tags` to publish it.
        --no-push registers the project only (publish later with
        `project push`).

    push [<handle>] [--repo <path>]
        Publish (or re-publish) a project from its working repo: adds
        the `swarm` remote if missing — or re-points it after a
        re-found / reanchor changes the project_id — then
        `git push swarm --all && git push swarm --tags`. Run from
        inside the repo, or pass --repo <path>. With no <handle>,
        pushes to the existing `swarm` remote as-is. Idempotent.

    invite <project_id> --puddle <fp> --role admin|member [--expires <Nd>]
        Mint a child token for another fleet. Prints an invitation
        artifact (JSON) the recipient pipes into `project join`.
        Requires the local fleet to hold an admin token on the project.

    join <project_id> [--invitation <file|->]
        Adopt an invitation issued by an admin. Verifies the chain,
        persists local project state. Puddle must be initialized.

    leave <project_id> [--purge] [--yes]
        Drop local membership. Writes a leave.pending sentinel the
        helper daemon (step 4) picks up to sign the LEAVE envelope.
        --purge also wipes the bare repo bytes on this envoy.

    list [--json]
        List every project this user has joined locally.

    status <project_id>
        Show one project's local state.

    revoke <project_id> --token-id <hex> [--reason <text>]
        Queue a revocation for a previously-issued token. Writes a
        revoke.pending sentinel the helper daemon (step 4) signs and
        gossips. Requires an admin token on the project.

    reanchor <project_id>
        Hard-fork the project under a new founder. Interactive — must
        type the project handle to confirm. Old URL becomes dead.

    re-invite-all [<project_id>]
        ADR-019. Founder side, post-rekey. Walks every multi-puddle
        project the local fleet founded; mints fresh invitations for
        every cross-puddle member with the current delegation chain
        attached. Prints (member fleet, project handle, invitation)
        triples for OOB delivery — same trust hop as the original
        `invite` flow. Optional <project_id> restricts to one project.

    rejoin <project_id> [--invitation <file|->]
        ADR-019. Member side, after the founder rekeyed. Idempotent
        extension of `join` that updates the local token chain and
        delegation chain in place without bailing on existing
        membership. Required when the founder of a multi-puddle
        project you are a member of has rekeyed and re-invited you.

    re-found <project_id> [--force]
        ADR-019. Escape hatch when a founded project's delegation
        chain is at the cap (DELEGATION_MAX_LEN = 5) or has been
        tombstoned by a --force-orphan-projects rekey. Mints a fresh
        project_id from the current puddle pubkey, byte-migrates
        from the at-cap bare repo via the same logic `reanchor`
        uses, and tombstones the old project. URL changes — every
        member must update their remote and rejoin under the new
        project_id (founder runs `re-invite-all <new_pid>` next).
        Pass --force to re-found a project below cap (manual
        chain-hygiene reset).
"
    );
}

// ============================================================
// Caller identity helpers
// ============================================================

/// Resolved identity of the calling user. CLI verbs run as the user
/// (not root), so we trust `getuid()` for username + home.
struct CallerIdentity {
    home: PathBuf,
    is_root: bool,
    username: String,
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
        bail!("swarmgit CLI is Unix-only")
    }
}

/// Read the local puddle pubkey as raw 32 bytes. Errors when puddle
/// has not been initialized for this user — the verbs that call this
/// can't proceed without an identity.
fn local_puddle_pubkey_bytes(home: &Path) -> Result<[u8; ID_LEN]> {
    let text = puddle::local_puddle_pubkey(home)?
        .ok_or_else(|| anyhow!(
            "puddle is not initialized for this user. \
             Run `vigo swarm puddle init` (or `join --from <host>`) first."
        ))?;
    let hexpart = text
        .strip_prefix("ed25519:")
        .ok_or_else(|| anyhow!("malformed puddle pubkey '{}': missing ed25519: prefix", text))?;
    if hexpart.len() != ID_LEN * 2 {
        bail!("puddle pubkey is wrong length: {}", hexpart);
    }
    let bytes = hex::decode(hexpart).context("decode puddle pubkey hex")?;
    let mut out = [0u8; ID_LEN];
    out.copy_from_slice(&bytes);
    Ok(out)
}

/// Sign `payload` with the user's fleet key via the session helper.
/// Returns the raw 64-byte Ed25519 signature.
///
/// Errors with a clear message when the helper isn't running — the
/// user needs to `puddle unlock` first.
fn sign_with_session(payload: &[u8]) -> Result<[u8; SIG_LEN]> {
    let sig_vec = puddle::session::request_sign(payload).map_err(|e| {
        anyhow!(
            "could not sign with puddle identity: {}\n\
             Run `vigo swarm puddle unlock` to start the session helper.",
            e
        )
    })?;
    if sig_vec.len() != SIG_LEN {
        bail!(
            "session helper returned a {}-byte signature, expected {}",
            sig_vec.len(),
            SIG_LEN
        );
    }
    let mut out = [0u8; SIG_LEN];
    out.copy_from_slice(&sig_vec);
    Ok(out)
}

fn now_secs() -> i64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs() as i64)
        .unwrap_or(0)
}

/// Resolve a project_id given a hex id or a unique handle prefix
/// among this user's joined projects. Used by every verb that takes
/// a project identifier.
fn resolve_project_id(home: &Path, arg: &str) -> Result<[u8; ID_LEN]> {
    // Try parsing as raw hex first.
    if let Ok(id) = parse_hex_id(arg) {
        return Ok(id);
    }
    // Otherwise look up by handle prefix.
    let projects = project_state::list_projects(home)?;
    let matches: Vec<&ProjectConfig> = projects
        .iter()
        .filter(|p| p.handle == arg || p.handle.starts_with(arg))
        .collect();
    match matches.len() {
        0 => bail!(
            "no project matches '{}' on this envoy (joined projects: {})",
            arg,
            projects
                .iter()
                .map(|p| p.handle.clone())
                .collect::<Vec<_>>()
                .join(", "),
        ),
        1 => parse_hex_id(&matches[0].project_id),
        _ => bail!(
            "handle prefix '{}' is ambiguous: {}",
            arg,
            matches
                .iter()
                .map(|p| p.handle.clone())
                .collect::<Vec<_>>()
                .join(", "),
        ),
    }
}

/// Strip an optional `ed25519:` prefix from a puddle pubkey argument
/// and parse it as 64-char lowercase hex. Accepts either
/// `<64 hex chars>` or `ed25519:<64 hex chars>` for ergonomics — the
/// `ed25519:` form is what `puddle pubkey` prints.
fn parse_puddle_pubkey_arg(s: &str) -> Result<[u8; ID_LEN]> {
    let hexpart = s.strip_prefix("ed25519:").unwrap_or(s);
    parse_hex_id(hexpart)
}

// ============================================================
// Invitation artifact (issued by `invite`, consumed by `join`)
// ============================================================

/// Self-contained artifact a recipient pipes into `project join`. The
/// recipient verifies the chain offline, then persists `project` and
/// `chain` to their per-user state. Versioned so the on-the-wire form
/// can evolve without breaking older agents — though per zero-users
/// doctrine, mismatched versions reject rather than migrate.
///
/// **ADR-019 (slice 4):** the optional `delegations` field carries
/// the founder's local delegation chain so the recipient's verifier
/// can chain a post-rekey leaf back to the genesis founder pubkey
/// baked into `project_id`. Pre-rekey founders send an empty list
/// (default for backwards compat with `INVITATION_VERSION = 1` JSON
/// that didn't have the field). Post-rekey founders send their
/// current chain. Recipients persist them on `rejoin` so subsequent
/// helper-daemon verifies use the same delegations the invitation
/// was anchored against.
#[derive(Debug, Clone, Serialize, Deserialize)]
struct Invitation {
    pub version: u32,
    pub project: ProjectConfig,
    pub chain: TokenChain,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub delegations: Vec<DelegationEnvelope>,
}

const INVITATION_VERSION: u32 = 1;

/// Refuse a mutating verb on a tombstoned project. Tombstoned
/// projects (`project.json::tombstoned_at` set, by either
/// `--force-orphan-projects` rekey or `re-found`) keep their bare
/// repo + URL on disk as an archive but reject new admin operations.
/// Operator's path forward is `re-found` (mints a fresh project_id
/// and migrates) or `project leave` (drops the archive).
fn refuse_if_tombstoned(cfg: &ProjectConfig, verb: &str) -> Result<()> {
    if let Some(when) = cfg.tombstoned_at {
        bail!(
            "project '{}' is tombstoned (since unix-ts {}); refusing `{}`. \
             Run `vigo swarm swarmgit project re-found {}` to mint a fresh \
             project_id and migrate, or `project leave {}` to drop the archive.",
            cfg.handle,
            when,
            verb,
            cfg.handle,
            cfg.project_id
        );
    }
    Ok(())
}

// ============================================================
// project init
// ============================================================

fn cmd_init(args: &[String]) -> Result<()> {
    let mut handle: Option<String> = None;
    let mut scope = DrScope::SwarmWide;
    let mut repo_flag: Option<String> = None;
    let mut no_push = false;
    let mut i = 0;
    while i < args.len() {
        match args[i].as_str() {
            "--scope" => {
                i += 1;
                let v = args.get(i).context("--scope requires a value")?;
                scope = v.parse()?;
            }
            "--repo" => {
                i += 1;
                repo_flag = Some(args.get(i).context("--repo requires a path")?.to_string());
            }
            "--no-push" => no_push = true,
            "--fanout" => bail!(
                "--fanout was renamed to --scope (and its 'fleet-wide' value to 'swarm-wide') in 0.41.32"
            ),
            other if !other.starts_with('-') && handle.is_none() => {
                handle = Some(other.to_string());
            }
            other => bail!("unknown init flag: {}", other),
        }
        i += 1;
    }
    let handle = handle.context(
        "usage: vigo swarm swarmgit project init <handle> \
         [--scope swarm-wide|members-only] [--repo <path>] [--no-push]",
    )?;
    validate_handle(&handle)?;

    let caller = resolve_caller()?;
    let founder_pub = local_puddle_pubkey_bytes(&caller.home)?;
    let project_id = project_id_for(&founder_pub, &handle);

    // Refuse to overwrite an existing project — the founder's
    // bootstrap is a one-shot artifact; re-running init would mint a
    // second bootstrap with a different `issued_at` and silently
    // change every member's authorization context.
    if project_state::ProjectConfig::load(&caller.home, &project_id)?.is_some() {
        bail!(
            "project {} already exists at {}. Run `project status {}` to inspect, \
             `project leave` to abandon, or `project reanchor` to hard-fork.",
            hex_id(&project_id),
            project_state::user_project_dir(&caller.home, &project_id).display(),
            hex_id(&project_id),
        );
    }

    // Build the founder bootstrap body (admin role, no parent, no
    // expiry) and ask the session helper to sign the canonical bytes.
    let now = now_secs();
    let body = TokenBody {
        project_id,
        handle: handle.clone(),
        subject_puddle_pubkey: founder_pub,
        role: Role::Admin,
        issued_at: now,
        expires_at: None,
        signer_puddle_pubkey: founder_pub,
        parent_token_id: None,
    };
    let bytes = canonical_bytes(&body);
    let signature = sign_with_session(&bytes)?;
    let bootstrap = Token { body, signature };
    bootstrap
        .verify_signature()
        .context("bootstrap token failed self-verification (session helper malfunction?)")?;

    // Persist project config + token chain.
    let cfg = ProjectConfig {
        version: project_state::PROJECT_STATE_VERSION,
        project_id: hex_id(&project_id),
        handle: handle.clone(),
        founder_puddle_pubkey: hex_id(&founder_pub),
        members: vec![hex_id(&founder_pub)],
        dr_scope: scope,
        cross_subnet_members: vec![],
        updated_at: now,
        tombstoned_at: None,
    };
    cfg.save(&caller.home)?;
    let chain = TokenChain {
        version: project_state::PROJECT_STATE_VERSION,
        chain: vec![bootstrap],
    };
    chain.save(&caller.home, &project_id)?;

    let hex_url = format!("swarmgit://{}/{}", hex_id(&project_id), handle);
    let friendly = local_puddle_friendly_name(&caller.home, &founder_pub);
    println!("✓ Founded project {}", handle);
    match &friendly {
        Some(name) => {
            println!("  URL:           swarmgit://{}/{}", name, handle);
            println!("  Canonical URL: {}", hex_url);
        }
        None => println!("  URL:           {}", hex_url),
    }
    println!("  Founder:       ed25519:{}", hex_id(&founder_pub));
    println!("  Scope:         {}", scope.as_str());
    println!(
        "  State:         {}",
        project_state::user_project_dir(&caller.home, &project_id).display()
    );

    // --no-push: register the auth surface only; the operator publishes
    // later from the working repo.
    if no_push {
        println!();
        println!("Registered only (--no-push). Publish from the repo with:");
        println!("  vigo swarm swarmgit project push {}", handle);
        return Ok(());
    }

    // Resolve the working repo to publish: --repo, else the cwd if it's
    // a git work tree; nothing → register-only (same end state as
    // --no-push, just inferred).
    let workdir = match detect_workdir(repo_flag.as_deref())? {
        Some(d) => d,
        None => {
            println!();
            println!("Not inside a git work tree — registered the project only. Publish later with:");
            println!("  cd <your repo> && vigo swarm swarmgit project push {}", handle);
            return Ok(());
        }
    };

    // The push rides the helper daemon; if it isn't up, register-only
    // and tell the operator how to finish once it is.
    if !helper_socket_path().exists() {
        eprintln!(
            "note: swarmgit helper socket not present at {} — is the agent running and is \
             swarmgit enabled on this host? Project registered; publish once it's up with:\n  \
             vigo swarm swarmgit project push {}",
            helper_socket_path().display(),
            handle
        );
        return Ok(());
    }

    // Warn (don't block) when the work tree's name doesn't match the
    // handle — likely a wrong-directory mistake, but the handle is the
    // operator's call.
    if let Some(base) = workdir.file_name().and_then(|n| n.to_str()) {
        if base != handle {
            eprintln!(
                "note: founding project '{}' but the work tree at {} is named '{}' — pushing it \
                 anyway (pass --repo for a different tree, or --no-push to register only)",
                handle,
                workdir.display(),
                base
            );
        }
    }

    match ensure_swarm_remote(&workdir, &hex_url)? {
        SwarmRemote::Added => println!("  Remote 'swarm' → {}", hex_url),
        SwarmRemote::AlreadyOurs => {}
        SwarmRemote::Conflicts(other) => {
            eprintln!(
                "note: remote 'swarm' already points at {} — leaving it untouched. Publish this \
                 project with:\n  git remote set-url swarm {} && git push swarm --all && git push swarm --tags",
                other, hex_url
            );
            return Ok(());
        }
    }

    push_swarm(&workdir)?;
    println!();
    println!(
        "Published. Verify replication with `vigocli swarm swarmgit show {}`.",
        handle
    );
    Ok(())
}

/// Validate a project handle. Free-form-but-sane: 1-64 chars, ASCII
/// alnum + `-` + `_` + `.`, no leading dot, no whitespace. Tighter
/// than the ADR strictly requires, but matches the existing
/// `path_guard::validate_project_name` posture for filesystem safety.
fn validate_handle(handle: &str) -> Result<()> {
    if handle.is_empty() || handle.len() > 64 {
        bail!("handle must be 1-64 characters");
    }
    if handle.starts_with('.') {
        bail!("handle must not start with '.'");
    }
    for c in handle.chars() {
        if !(c.is_ascii_alphanumeric() || c == '-' || c == '_' || c == '.') {
            bail!(
                "handle '{}' contains invalid character '{}'; \
                 use ASCII alphanumeric + '-' '_' '.'",
                handle,
                c
            );
        }
    }
    Ok(())
}

// ============================================================
// Working-repo helpers (project init / push)
//
// These run as the invoking user against the operator's own
// checkout — `git remote add` / `git push swarm …` — exactly the
// commands the operator would type. They go through
// `git_ops::build_command(RunAs::Caller)` so the swarmgit subsystem
// keeps a single chokepoint for spawning git, with the usual hook /
// prompt hardening but no privilege transition.
// ============================================================

/// The friendly name this user's puddle has claimed (ADR-022), but
/// only when it's anchored to `expected_pub` — so `project init`
/// prints `swarmgit://<name>/<handle>` alongside the hex form when
/// (and only when) the resolver would actually map it back here.
fn local_puddle_friendly_name(home: &Path, expected_pub: &[u8; ID_LEN]) -> Option<String> {
    let claim = puddle::local_name_claim(home).ok().flatten()?;
    if claim.body.puddle_pubkey.eq_ignore_ascii_case(&hex_id(expected_pub)) {
        Some(claim.body.name)
    } else {
        None
    }
}

/// A hardened `git` command rooted in `repo`, run as the calling user.
fn git_in_repo(repo: &Path) -> std::process::Command {
    let mut cmd = super::git_ops::build_command(super::git_ops::RunAs::Caller);
    cmd.current_dir(repo);
    cmd
}

/// `git -C <dir> rev-parse --show-toplevel`, trimmed. `Ok(None)` when
/// the path (or cwd, if `dir` is `None`) isn't inside a git work tree.
fn git_toplevel(dir: Option<&Path>) -> Result<Option<PathBuf>> {
    let mut cmd = super::git_ops::build_command(super::git_ops::RunAs::Caller);
    if let Some(d) = dir {
        cmd.arg("-C").arg(d);
    }
    cmd.args(["rev-parse", "--show-toplevel"])
        .stdin(std::process::Stdio::null())
        .stderr(std::process::Stdio::null());
    let out = cmd.output().context("run `git rev-parse --show-toplevel`")?;
    if !out.status.success() {
        return Ok(None);
    }
    let top = String::from_utf8_lossy(&out.stdout).trim().to_string();
    if top.is_empty() {
        return Ok(None);
    }
    Ok(Some(PathBuf::from(top)))
}

/// Resolve the working repo to publish. `--repo` must be a git work
/// tree (error otherwise); with no flag, the cwd's work-tree root if
/// there is one, else `Ok(None)` (the caller treats that as
/// register-only).
fn detect_workdir(repo_flag: Option<&str>) -> Result<Option<PathBuf>> {
    match repo_flag {
        Some(p) => Ok(Some(git_toplevel(Some(Path::new(p)))?.with_context(|| {
            format!("--repo {} is not inside a git work tree", p)
        })?)),
        None => git_toplevel(None),
    }
}

/// `git -C <repo> remote get-url <name>`, trimmed; `Ok(None)` when the
/// remote doesn't exist.
fn git_remote_url(repo: &Path, name: &str) -> Result<Option<String>> {
    let out = git_in_repo(repo)
        .args(["remote", "get-url", name])
        .stdin(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .output()
        .context("run `git remote get-url`")?;
    if !out.status.success() {
        return Ok(None);
    }
    Ok(Some(String::from_utf8_lossy(&out.stdout).trim().to_string()))
}

/// Whether a `swarmgit://` remote URL addresses the same project as
/// `canonical_url` — i.e. its trailing `/<handle>` matches. Lets us
/// leave a friendly-form remote (`swarmgit://<name>/<handle>`)
/// untouched without resolving the name.
fn remote_url_is_same_project(remote_url: &str, canonical_url: &str) -> bool {
    remote_url.starts_with("swarmgit://")
        && remote_url.rsplit('/').next() == canonical_url.rsplit('/').next()
}

/// Result of [`ensure_swarm_remote`].
enum SwarmRemote {
    /// No `swarm` remote existed; added it pointing at the canonical URL.
    Added,
    /// A `swarm` remote already addresses this project (hex or friendly form).
    AlreadyOurs,
    /// A `swarm` remote exists but points at something else; left it alone.
    Conflicts(String),
}

/// Ensure the work tree has a `swarm` remote pointing at the canonical
/// hex `swarmgit://<pid>/<handle>` URL. Adds it when absent; never
/// clobbers an existing `swarm` remote that points elsewhere — `init`
/// reports the conflict and lets the operator decide.
fn ensure_swarm_remote(repo: &Path, canonical_url: &str) -> Result<SwarmRemote> {
    match git_remote_url(repo, "swarm")? {
        None => {
            run_git(
                git_in_repo(repo).args(["remote", "add", "swarm", canonical_url]),
                "git remote add swarm",
            )?;
            Ok(SwarmRemote::Added)
        }
        Some(url) if url == canonical_url || remote_url_is_same_project(&url, canonical_url) => {
            Ok(SwarmRemote::AlreadyOurs)
        }
        Some(other) => Ok(SwarmRemote::Conflicts(other)),
    }
}

/// Run a prepared `git` command, inheriting stdio (the operator sees
/// git's own output), and bail on a non-zero exit. `what` is the human
/// label for the error message.
fn run_git(cmd: &mut std::process::Command, what: &str) -> Result<()> {
    let st = cmd
        .stdin(std::process::Stdio::null())
        .status()
        .with_context(|| format!("spawn `{}`", what))?;
    if !st.success() {
        bail!("`{}` exited with status {}", what, st);
    }
    Ok(())
}

/// `git push swarm --all` then `git push swarm --tags` against `repo`,
/// inheriting stdio. Bails on the first failure with a pointer back to
/// `project push` for the retry.
fn push_swarm(repo: &Path) -> Result<()> {
    for spec in [
        ["push", "swarm", "--all"],
        ["push", "swarm", "--tags"],
    ] {
        let st = git_in_repo(repo)
            .args(spec)
            .stdin(std::process::Stdio::null())
            .status()
            .with_context(|| format!("spawn `git {}`", spec.join(" ")))?;
        if !st.success() {
            bail!(
                "`git {}` failed (status {}) — fix the cause and retry with \
                 `vigo swarm swarmgit project push`",
                spec.join(" "),
                st
            );
        }
    }
    Ok(())
}

// ============================================================
// project push
// ============================================================

/// `vigo swarm swarmgit project push [<handle>] [--repo <path>]` —
/// publish (or re-publish) a project from its working repo: add the
/// `swarm` remote if missing, or re-point it after a `re-found` /
/// `reanchor`, then `git push swarm --all && --tags`. With no
/// `<handle>`, requires an existing `swarm` remote and pushes to it
/// as-is. Idempotent — safe to re-run.
fn cmd_push(args: &[String]) -> Result<()> {
    let mut handle_arg: Option<String> = None;
    let mut repo_flag: Option<String> = None;
    let mut i = 0;
    while i < args.len() {
        match args[i].as_str() {
            "--repo" => {
                i += 1;
                repo_flag = Some(args.get(i).context("--repo requires a path")?.to_string());
            }
            other if !other.starts_with('-') && handle_arg.is_none() => {
                handle_arg = Some(other.to_string());
            }
            other => bail!("unknown push flag: {}", other),
        }
        i += 1;
    }

    let caller = resolve_caller()?;
    let workdir = detect_workdir(repo_flag.as_deref())?.context(
        "not inside a git work tree — run `project push` from the repo, or pass --repo <path>",
    )?;

    if !helper_socket_path().exists() {
        bail!(
            "swarmgit helper socket not present at {} — is the agent running and is swarmgit \
             enabled on this host?",
            helper_socket_path().display()
        );
    }

    match handle_arg {
        Some(h) => {
            // Resolve to a project_id from this user's local state —
            // works for both founded and joined projects.
            let pid = resolve_project_id(&caller.home, &h)?;
            let cfg = ProjectConfig::load(&caller.home, &pid)?
                .ok_or_else(|| anyhow!("no local state for project {}", hex_id(&pid)))?;
            let canonical_url = format!("swarmgit://{}/{}", hex_id(&pid), cfg.handle);
            match git_remote_url(&workdir, "swarm")? {
                None => {
                    run_git(
                        git_in_repo(&workdir).args(["remote", "add", "swarm", &canonical_url]),
                        "git remote add swarm",
                    )?;
                    println!("Remote 'swarm' → {}", canonical_url);
                }
                Some(url) if url == canonical_url || remote_url_is_same_project(&url, &canonical_url) => {}
                Some(other) => {
                    run_git(
                        git_in_repo(&workdir).args(["remote", "set-url", "swarm", &canonical_url]),
                        "git remote set-url swarm",
                    )?;
                    println!("Re-pointed remote 'swarm': {} → {}", other, canonical_url);
                }
            }
        }
        None => {
            if git_remote_url(&workdir, "swarm")?.is_none() {
                bail!(
                    "no `swarm` remote in {} and no <handle> given — run \
                     `project push <handle>`, or add one with \
                     `git remote add swarm swarmgit://<project_id>/<handle>` first",
                    workdir.display()
                );
            }
        }
    }

    push_swarm(&workdir)?;
    println!("✓ Pushed {} to the swarm.", workdir.display());
    Ok(())
}

// ============================================================
// project invite
// ============================================================

fn cmd_invite(args: &[String]) -> Result<()> {
    let mut project_arg: Option<String> = None;
    let mut puddle_arg: Option<String> = None;
    let mut role_arg: Option<String> = None;
    let mut expires_days: Option<u64> = None;
    let mut i = 0;
    while i < args.len() {
        match args[i].as_str() {
            "--puddle" => {
                i += 1;
                puddle_arg = Some(args.get(i).context("--puddle requires a value")?.to_string());
            }
            "--role" => {
                i += 1;
                role_arg = Some(args.get(i).context("--role requires a value")?.to_string());
            }
            "--expires" => {
                i += 1;
                let v = args.get(i).context("--expires requires a value")?;
                expires_days = Some(parse_days(v)?);
            }
            other if !other.starts_with('-') && project_arg.is_none() => {
                project_arg = Some(other.to_string());
            }
            other => bail!("unknown invite flag: {}", other),
        }
        i += 1;
    }
    let project_arg = project_arg.context(
        "usage: vigo swarm swarmgit project invite <project_id> --puddle <fp> --role admin|member [--expires <Nd>]",
    )?;
    let fleet = parse_puddle_pubkey_arg(
        &puddle_arg.context("--puddle is required")?,
    )?;
    let role = match role_arg.as_deref() {
        Some("admin") => Role::Admin,
        Some("member") => Role::Member,
        Some(other) => bail!("--role must be 'admin' or 'member', got '{}'", other),
        None => bail!("--role is required"),
    };

    let caller = resolve_caller()?;
    let project_id = resolve_project_id(&caller.home, &project_arg)?;
    let cfg = ProjectConfig::load(&caller.home, &project_id)?
        .ok_or_else(|| anyhow!("not a member of project {}", hex_id(&project_id)))?;
    refuse_if_tombstoned(&cfg, "invite")?;

    // Caller's leaf token must be Admin to mint a child.
    let chain = TokenChain::load(&caller.home, &project_id)?
        .ok_or_else(|| anyhow!("token chain missing for {}", hex_id(&project_id)))?;
    let parent = chain
        .chain
        .last()
        .ok_or_else(|| anyhow!("token chain is empty"))?
        .clone();
    if parent.body.role != Role::Admin {
        bail!(
            "this fleet holds a {:?} token on project {}; only admins can invite",
            parent.body.role,
            cfg.handle
        );
    }

    // The leaf's subject must equal our local puddle pubkey — sanity
    // check that the chain on disk really authorizes us.
    let local_pub = local_puddle_pubkey_bytes(&caller.home)?;
    if parent.body.subject_puddle_pubkey != local_pub {
        bail!(
            "leaf token subject does not match local puddle pubkey \
             (chain expects ed25519:{:.16}…, local is ed25519:{:.16}…)",
            hex_id(&parent.body.subject_puddle_pubkey),
            hex_id(&local_pub)
        );
    }

    // Build child body, sign via session helper.
    let now = now_secs();
    let expires_at = expires_days.map(|d| now + (d as i64) * 86400);
    let body = TokenBody {
        project_id,
        handle: cfg.handle.clone(),
        subject_puddle_pubkey: fleet,
        role,
        issued_at: now,
        expires_at,
        signer_puddle_pubkey: local_pub,
        parent_token_id: Some(parent.id()),
    };
    let bytes = canonical_bytes(&body);
    let signature = sign_with_session(&bytes)?;
    let child = Token { body, signature };
    child.verify_signature().context("child token failed self-verification")?;

    // Build the invitation artifact: chain = parent chain + child;
    // project = our local view, with the recipient added to members.
    let mut full_chain: Vec<Token> = chain.chain.clone();
    full_chain.push(child);
    let mut new_members = cfg.members.clone();
    let recipient_hex = hex_id(&fleet);
    if !new_members.iter().any(|m| m == &recipient_hex) {
        new_members.push(recipient_hex);
    }
    // ADR-019: include the local delegation chain so the recipient's
    // verifier can walk a post-rekey founder identity back to the
    // genesis founder pubkey baked into project_id. Pre-rekey
    // founders have no delegations on disk and send an empty list.
    let delegations = DelegationChain::load(&caller.home, &project_id)?
        .map(|c| c.delegations)
        .unwrap_or_default();

    let invitation = Invitation {
        version: INVITATION_VERSION,
        project: ProjectConfig {
            members: new_members,
            updated_at: now,
            ..cfg.clone()
        },
        chain: TokenChain {
            version: project_state::PROJECT_STATE_VERSION,
            chain: full_chain,
        },
        delegations,
    };

    let json = serde_json::to_string_pretty(&invitation)?;
    println!("{}", json);
    eprintln!();
    eprintln!("Invitation for project '{}'.", cfg.handle);
    eprintln!("Pipe the JSON above to the recipient and have them run:");
    eprintln!("  vigo swarm swarmgit project join {} --invitation -", hex_id(&project_id));
    Ok(())
}

/// Parse `--expires` value: either `Nd` or a bare integer (days).
/// Same shape as the old prune `--older-than` parser.
fn parse_days(s: &str) -> Result<u64> {
    let trimmed = s.trim();
    if trimmed.is_empty() {
        bail!("duration is empty");
    }
    let num = trimmed.strip_suffix('d').unwrap_or(trimmed);
    let days: u64 = num
        .parse()
        .with_context(|| format!("invalid duration '{}': expected '<N>d' or bare integer", s))?;
    if days == 0 {
        bail!("duration must be at least 1 day");
    }
    Ok(days)
}

// ============================================================
// project join
// ============================================================

fn cmd_join(args: &[String]) -> Result<()> {
    let mut project_arg: Option<String> = None;
    let mut invitation_src: Option<String> = None;
    let mut i = 0;
    while i < args.len() {
        match args[i].as_str() {
            "--invitation" => {
                i += 1;
                invitation_src = Some(
                    args.get(i)
                        .context("--invitation requires a path or '-'")?
                        .to_string(),
                );
            }
            other if !other.starts_with('-') && project_arg.is_none() => {
                project_arg = Some(other.to_string());
            }
            other => bail!("unknown join flag: {}", other),
        }
        i += 1;
    }
    let project_arg = project_arg
        .context("usage: vigo swarm swarmgit project join <project_id> [--invitation <file|->]")?;
    let invitation_src = invitation_src.unwrap_or_else(|| "-".to_string());

    // Read invitation JSON from file or stdin.
    let invitation_json = if invitation_src == "-" {
        let mut buf = String::new();
        std::io::stdin()
            .read_to_string(&mut buf)
            .context("read invitation from stdin")?;
        buf
    } else {
        fs::read_to_string(&invitation_src)
            .with_context(|| format!("read invitation from {}", invitation_src))?
    };
    let invitation: Invitation = serde_json::from_str(&invitation_json)
        .context("parse invitation JSON")?;
    if invitation.version != INVITATION_VERSION {
        bail!(
            "invitation version {} not supported (expected {})",
            invitation.version,
            INVITATION_VERSION
        );
    }

    let caller = resolve_caller()?;
    let local_pub = local_puddle_pubkey_bytes(&caller.home)?;

    // Sanity-check that the project_id positional matches the
    // invitation's anchor — catches "wrong invitation pasted."
    let claimed_id = parse_hex_id(&invitation.project.project_id)?;
    let arg_id = match parse_hex_id(&project_arg) {
        Ok(id) => id,
        Err(_) => {
            bail!(
                "project_id '{}' is not valid hex. Pass the same project_id as in the invitation: {}",
                project_arg,
                invitation.project.project_id
            );
        }
    };
    if arg_id != claimed_id {
        bail!(
            "project_id mismatch: argument is {} but invitation declares {}",
            project_arg,
            invitation.project.project_id
        );
    }

    // Verify the chain back to the founder pubkey embedded in the
    // invitation's project config. `verify_chain` re-derives
    // project_id as `sha256(founder ‖ bootstrap-token handle)` and
    // rejects the chain if it doesn't match — defense against a
    // malicious invitation that swaps the founder pubkey or the handle.
    //
    // ADR-019: pass the invitation's delegation chain through to the
    // verifier so a post-rekey founder's leaf chains correctly back
    // to the genesis founder pubkey. Empty list when the founder
    // hasn't rekeyed yet — verifier behaves identically to
    // pre-ADR-019 in that case.
    let founder = parse_hex_id(&invitation.project.founder_puddle_pubkey)?;
    verify_chain(
        &claimed_id,
        &founder,
        &invitation.delegations,
        &invitation.chain.chain,
        now_secs(),
    )
    .map_err(|e| anyhow!("invitation chain failed verification: {}", e))?;

    // The handle the invitation displays must be the one the bootstrap
    // token committed to (verify_chain proved that handle reproduces
    // project_id). A mismatch isn't a security hole — project_id is the
    // cryptographic key — but it would make the local config lie.
    let bootstrap_handle = invitation.chain.chain[0].body.handle.clone();
    if invitation.project.handle != bootstrap_handle {
        bail!(
            "invitation's project handle '{}' doesn't match the handle bound into its bootstrap token ('{}')",
            invitation.project.handle, bootstrap_handle
        );
    }

    // Leaf must be granting access to *our* puddle pubkey.
    let leaf = invitation.chain.chain.last().ok_or_else(|| anyhow!("empty chain"))?;
    if leaf.body.subject_puddle_pubkey != local_pub {
        bail!(
            "this invitation grants access to ed25519:{:.16}…, but our local puddle is ed25519:{:.16}…",
            hex_id(&leaf.body.subject_puddle_pubkey),
            hex_id(&local_pub)
        );
    }

    // Refuse to overwrite an existing membership: forces an explicit
    // `leave` first. Stops "joining" silently from clobbering a chain
    // the local user already holds (which would be how a leak/replay
    // scenario downgrades an admin to a member).
    if ProjectConfig::load(&caller.home, &claimed_id)?.is_some() {
        bail!(
            "already a member of project {} — run `project leave {}` first if you want to re-join",
            invitation.project.handle,
            invitation.project.project_id
        );
    }

    invitation.project.save(&caller.home)?;
    invitation.chain.save(&caller.home, &claimed_id)?;
    // ADR-019: persist the founder's delegation chain so the helper
    // daemon (per-user verify path) and any future re-invite from
    // this member walk the same authority path the invitation
    // anchored against. Empty list = no file written.
    if !invitation.delegations.is_empty() {
        let chain = DelegationChain {
            version: project_state::PROJECT_STATE_VERSION,
            delegations: invitation.delegations.clone(),
        };
        chain.save(&caller.home, &claimed_id).context(
            "saving founder delegation chain that came with the invitation",
        )?;
    }

    println!("✓ Joined project {}", invitation.project.handle);
    println!("  URL:        swarmgit://{}/{}", invitation.project.project_id, invitation.project.handle);
    println!("  Role:       {:?}", leaf.body.role);
    println!("  Members:    {}", invitation.project.members.len());
    println!("  Scope:      {}", invitation.project.dr_scope.as_str());
    if !invitation.delegations.is_empty() {
        println!(
            "  Delegations: {} hop(s) (founder has rekeyed since project init)",
            invitation.delegations.len()
        );
    }
    println!();
    println!("State written to {}", project_state::user_project_dir(&caller.home, &claimed_id).display());
    Ok(())
}

// ============================================================
// project re-invite-all (ADR-019 slice 4)
// ============================================================

/// Walk every multi-puddle project the local fleet founded, mint a
/// fresh invitation for every cross-puddle member, print the
/// invitations as `(member_puddle_pubkey, handle, JSON)` triples for
/// out-of-band delivery. Optional `<project_id>` restricts to a
/// single project.
///
/// "Multi-fleet" = `members[]` contains a puddle pubkey other than
/// the local user's. Single-fleet personal-DR projects (founder is
/// the only member) are skipped; their members are sibling envoys
/// in the user's own puddle which already inherit trust via
/// ADR-018's pair-claim ceremony.
fn cmd_re_invite_all(args: &[String]) -> Result<()> {
    let mut project_arg: Option<String> = None;
    let mut i = 0;
    while i < args.len() {
        match args[i].as_str() {
            other if !other.starts_with('-') && project_arg.is_none() => {
                project_arg = Some(other.to_string());
            }
            other => bail!("unknown re-invite-all flag: {}", other),
        }
        i += 1;
    }

    let caller = resolve_caller()?;
    let local_pub = local_puddle_pubkey_bytes(&caller.home)?;
    let local_pub_hex = hex_id(&local_pub);

    // Resolve scope: one project or all founded multi-puddle projects.
    let projects: Vec<ProjectConfig> = if let Some(arg) = project_arg.as_deref() {
        let pid = resolve_project_id(&caller.home, arg)?;
        match ProjectConfig::load(&caller.home, &pid)? {
            Some(c) => vec![c],
            None => bail!("not a member of project {}", arg),
        }
    } else {
        project_state::list_projects(&caller.home)?
    };

    let mut printed = 0usize;
    let mut skipped_single_fleet = Vec::new();
    let mut skipped_not_founded = Vec::new();

    for cfg in projects {
        let pid = parse_hex_id(&cfg.project_id)
            .with_context(|| format!("project_id hex for {}", cfg.handle))?;

        // Skip tombstoned projects — they're frozen archives; the
        // founder needs to re-found them (mints a new project_id)
        // before issuing fresh invitations. `re-invite-all` against
        // the new project_id is the right next step.
        if cfg.tombstoned_at.is_some() {
            continue;
        }

        // Founded by us? The founder predicate uses the same logic
        // as rekey.rs: project.founder_puddle_pubkey is in our
        // historical pubkey set. For re-invite-all post-rekey, the
        // local user's CURRENT pubkey is the leaf of the delegation
        // chain — but we just need the predicate to recognize our
        // OWN founded projects, so check against the chain.last().new
        // (or the genesis if no delegations).
        let delegations = DelegationChain::load(&caller.home, &pid)?
            .map(|c| c.delegations)
            .unwrap_or_default();
        let current_founder_hex: String = if let Some(last) = delegations.last() {
            last.new_founder_pubkey.to_ascii_lowercase()
        } else {
            cfg.founder_puddle_pubkey.to_ascii_lowercase()
        };
        if current_founder_hex != local_pub_hex.to_ascii_lowercase() {
            skipped_not_founded.push(cfg.handle.clone());
            continue;
        }

        // Multi-fleet check: members[] minus the local fleet must be
        // non-empty. Members are stored as bare hex (no prefix).
        let cross_fleet_members: Vec<String> = cfg
            .members
            .iter()
            .filter(|m| m.to_ascii_lowercase() != local_pub_hex.to_ascii_lowercase())
            .cloned()
            .collect();
        if cross_fleet_members.is_empty() {
            skipped_single_fleet.push(cfg.handle.clone());
            continue;
        }

        // Load our authority chain (its leaf must be Admin under our
        // current pubkey; rekey-time hooks guarantee this for founded
        // projects).
        let chain = TokenChain::load(&caller.home, &pid)?
            .ok_or_else(|| anyhow!("token chain missing for {}", cfg.handle))?;
        let parent = chain
            .chain
            .last()
            .ok_or_else(|| anyhow!("token chain is empty for {}", cfg.handle))?
            .clone();
        if parent.body.role != Role::Admin {
            bail!(
                "this fleet holds a {:?} token on {}; only admins can re-invite",
                parent.body.role,
                cfg.handle
            );
        }
        if parent.body.subject_puddle_pubkey != local_pub {
            bail!(
                "leaf token subject does not match local puddle pubkey on {}: \
                 chain expects ed25519:{:.16}…, local is ed25519:{:.16}… — \
                 did `puddle rekey-pair` complete successfully?",
                cfg.handle,
                hex_id(&parent.body.subject_puddle_pubkey),
                hex_id(&local_pub)
            );
        }

        // Mint a fresh leaf for each cross-puddle member.
        for recipient_hex in &cross_fleet_members {
            let recipient = parse_hex_id(recipient_hex)
                .with_context(|| format!("members[] entry hex on {}", cfg.handle))?;
            let now = now_secs();
            let body = TokenBody {
                project_id: pid,
                handle: cfg.handle.clone(),
                subject_puddle_pubkey: recipient,
                role: Role::Member,
                issued_at: now,
                expires_at: None,
                signer_puddle_pubkey: local_pub,
                parent_token_id: Some(parent.id()),
            };
            let bytes = canonical_bytes(&body);
            let signature = sign_with_session(&bytes)?;
            let child = Token { body, signature };
            child
                .verify_signature()
                .context("re-invite child token failed self-verification")?;

            let mut full_chain: Vec<Token> = chain.chain.clone();
            full_chain.push(child);
            let invitation = Invitation {
                version: INVITATION_VERSION,
                project: ProjectConfig {
                    members: cfg.members.clone(),
                    updated_at: now,
                    ..cfg.clone()
                },
                chain: TokenChain {
                    version: project_state::PROJECT_STATE_VERSION,
                    chain: full_chain,
                },
                delegations: delegations.clone(),
            };
            let json = serde_json::to_string_pretty(&invitation)?;
            // Each invitation is preceded by a stderr header so a
            // tee-style operator can split them into per-recipient
            // files; stdout stays clean JSON.
            eprintln!(
                "─── re-invite for project '{}' → ed25519:{} ───",
                cfg.handle, recipient_hex
            );
            println!("{}", json);
            eprintln!();
            printed += 1;
        }
    }

    eprintln!(
        "✓ {} re-invite invitation(s) printed across founded multi-puddle project(s).",
        printed
    );
    if !skipped_single_fleet.is_empty() {
        eprintln!(
            "  Skipped {} single-fleet project(s) (no cross-puddle members): {}",
            skipped_single_fleet.len(),
            skipped_single_fleet.join(", ")
        );
    }
    if !skipped_not_founded.is_empty() {
        eprintln!(
            "  Skipped {} project(s) we don't own (not founder): {}",
            skipped_not_founded.len(),
            skipped_not_founded.join(", ")
        );
    }
    if printed > 0 {
        eprintln!();
        eprintln!("Communicate each JSON block out-of-band to the matching member fleet,");
        eprintln!("then have them run:");
        eprintln!("    vigo swarm swarmgit project rejoin <project_id> --invitation -");
    }
    Ok(())
}

// ============================================================
// project rejoin (ADR-019 slice 4)
// ============================================================

/// Idempotent extension of `cmd_join`. Required when a project's
/// founder has rekeyed: the original tokens.json is still valid for
/// the user's local helper daemon (the chain anchors to genesis), but
/// the founder cannot mint *new* tokens against the old chain shape.
/// `rejoin` adopts a fresh invitation issued against the founder's
/// new identity, including the founder's current delegation chain.
///
/// Differences from `cmd_join`:
/// - Does NOT bail when the project already exists locally — the
///   whole point is to update the chain in place.
/// - Updates `tokens.json` + `delegations.json` + `project.json` from
///   the invitation; preserves any unrelated local state (revoke /
///   leave pending sentinels).
fn cmd_rejoin(args: &[String]) -> Result<()> {
    let mut project_arg: Option<String> = None;
    let mut invitation_src: Option<String> = None;
    let mut i = 0;
    while i < args.len() {
        match args[i].as_str() {
            "--invitation" => {
                i += 1;
                invitation_src = Some(
                    args.get(i)
                        .context("--invitation requires a path or '-'")?
                        .to_string(),
                );
            }
            other if !other.starts_with('-') && project_arg.is_none() => {
                project_arg = Some(other.to_string());
            }
            other => bail!("unknown rejoin flag: {}", other),
        }
        i += 1;
    }
    let project_arg = project_arg.context(
        "usage: vigo swarm swarmgit project rejoin <project_id> [--invitation <file|->]",
    )?;
    let invitation_src = invitation_src.unwrap_or_else(|| "-".to_string());

    let invitation_json = if invitation_src == "-" {
        let mut buf = String::new();
        std::io::stdin()
            .read_to_string(&mut buf)
            .context("read invitation from stdin")?;
        buf
    } else {
        fs::read_to_string(&invitation_src)
            .with_context(|| format!("read invitation from {}", invitation_src))?
    };
    let invitation: Invitation = serde_json::from_str(&invitation_json)
        .context("parse invitation JSON")?;
    if invitation.version != INVITATION_VERSION {
        bail!(
            "invitation version {} not supported (expected {})",
            invitation.version,
            INVITATION_VERSION
        );
    }

    let caller = resolve_caller()?;
    let local_pub = local_puddle_pubkey_bytes(&caller.home)?;

    let claimed_id = parse_hex_id(&invitation.project.project_id)?;
    let arg_id = match parse_hex_id(&project_arg) {
        Ok(id) => id,
        Err(_) => bail!(
            "project_id '{}' is not valid hex. Pass the same project_id as in the invitation: {}",
            project_arg,
            invitation.project.project_id
        ),
    };
    if arg_id != claimed_id {
        bail!(
            "project_id mismatch: argument is {} but invitation declares {}",
            project_arg,
            invitation.project.project_id
        );
    }

    let founder = parse_hex_id(&invitation.project.founder_puddle_pubkey)?;
    verify_chain(
        &claimed_id,
        &founder,
        &invitation.delegations,
        &invitation.chain.chain,
        now_secs(),
    )
    .map_err(|e| anyhow!("invitation chain failed verification: {}", e))?;

    let leaf = invitation
        .chain
        .chain
        .last()
        .ok_or_else(|| anyhow!("empty chain"))?;
    if leaf.body.subject_puddle_pubkey != local_pub {
        bail!(
            "this invitation grants access to ed25519:{:.16}…, but our local puddle is ed25519:{:.16}…",
            hex_id(&leaf.body.subject_puddle_pubkey),
            hex_id(&local_pub)
        );
    }

    // Sanity: rejoin expects an existing local project state. If
    // there's no `project.json` on disk, the operator should run
    // `project join` instead — same trust hop, idempotent semantics
    // not needed.
    let pre_existing = ProjectConfig::load(&caller.home, &claimed_id)?;
    let was_member = pre_existing.is_some();

    invitation.project.save(&caller.home)?;
    invitation.chain.save(&caller.home, &claimed_id)?;
    if !invitation.delegations.is_empty() {
        let chain = DelegationChain {
            version: project_state::PROJECT_STATE_VERSION,
            delegations: invitation.delegations.clone(),
        };
        chain
            .save(&caller.home, &claimed_id)
            .context("saving founder delegation chain from rejoin invitation")?;
    } else {
        // Founder rolled back / re-founded under genesis — clear any
        // stale local delegation chain so verifier doesn't apply
        // bogus authority windows.
        let path = project_state::delegations_path(&caller.home, &claimed_id);
        if path.exists() {
            let _ = fs::remove_file(&path);
        }
    }

    if was_member {
        println!("✓ Re-joined project {} (chain refreshed)", invitation.project.handle);
    } else {
        println!("✓ Joined project {}", invitation.project.handle);
        println!(
            "  (note: rejoin called on a project we'd never joined — `join` would also have worked)"
        );
    }
    println!("  URL:        swarmgit://{}/{}", invitation.project.project_id, invitation.project.handle);
    println!("  Role:       {:?}", leaf.body.role);
    println!("  Members:    {}", invitation.project.members.len());
    println!("  Scope:      {}", invitation.project.dr_scope.as_str());
    if !invitation.delegations.is_empty() {
        println!(
            "  Delegations: {} hop(s) (chain anchors to genesis founder ed25519:{:.16}…)",
            invitation.delegations.len(),
            invitation.project.founder_puddle_pubkey
        );
    }
    println!();
    println!(
        "State updated under {}",
        project_state::user_project_dir(&caller.home, &claimed_id).display()
    );
    Ok(())
}

// ============================================================
// project leave
// ============================================================

fn cmd_leave(args: &[String]) -> Result<()> {
    let mut project_arg: Option<String> = None;
    let mut purge = false;
    let mut yes = false;
    let mut i = 0;
    while i < args.len() {
        match args[i].as_str() {
            "--purge" => purge = true,
            "--yes" | "-y" => yes = true,
            other if !other.starts_with('-') && project_arg.is_none() => {
                project_arg = Some(other.to_string());
            }
            other => bail!("unknown leave flag: {}", other),
        }
        i += 1;
    }
    let project_arg = project_arg
        .context("usage: vigo swarm swarmgit project leave <project_id> [--purge] [--yes]")?;

    let caller = resolve_caller()?;
    let project_id = resolve_project_id(&caller.home, &project_arg)?;
    let cfg = ProjectConfig::load(&caller.home, &project_id)?
        .ok_or_else(|| anyhow!("not a member of project {}", hex_id(&project_id)))?;

    if !yes {
        eprintln!("About to leave project '{}' ({}).", cfg.handle, cfg.project_id);
        if purge {
            eprintln!("  --purge: bare repo bytes on this envoy will be removed.");
        } else {
            eprintln!("  Local user state will be dropped; the bare repo on this envoy survives.");
        }
        eprintln!("  A LEAVE envelope will be queued for the helper daemon to gossip in step 4.");
        eprintln!();
        eprintln!("Re-run with --yes to confirm.");
        return Ok(());
    }

    // Sign the LEAVE envelope inline via the user's puddle
    // session helper (CLI runs as the user; root-side reactor cannot
    // reach a user's session helper). Sentinel content IS the signed
    // envelope — the helper daemon's reactor moves it to
    // /var/lib/vigo/swarm/swarmgit/envelopes/<pid>/leaves/ verbatim,
    // re-verifying on the way in.
    let leaver_pub = local_puddle_pubkey_bytes(&caller.home)?;
    let leave_time = now_secs();
    let leave_envelope = super::envelopes::LeaveEnvelope::build_with(
        &project_id,
        &leaver_pub,
        leave_time,
        |bytes| sign_with_session(bytes),
    )?;
    write_sentinel(&caller.home, &project_id, "leave.pending", &leave_envelope)?;
    println!("queued LEAVE for project '{}'", cfg.handle);

    if purge && caller.is_root {
        // Wipe the bare repo — only meaningful when run as root since
        // /var/lib/vigo/swarm/swarmgit/projects/ is 0700 root:root.
        let base = crate::platform::paths::default_swarmgit_dir();
        let repo = project_repo_path(&base, &project_id);
        if repo.exists() {
            fs::remove_dir_all(&repo)
                .with_context(|| format!("remove bare repo at {}", repo.display()))?;
            println!("removed bare repo {}", repo.display());
        }
    } else if purge {
        eprintln!(
            "warning: --purge requested but caller is not root; the bare repo at \
             {} survives. Re-run as root to wipe.",
            project_repo_path(&crate::platform::paths::default_swarmgit_dir(), &project_id).display()
        );
    }

    purge_user_state(&caller.home, &project_id)?;
    println!("dropped local state at {}", project_state::user_project_dir(&caller.home, &project_id).display());
    Ok(())
}

fn write_sentinel<T: Serialize>(
    home: &Path,
    project_id: &[u8; ID_LEN],
    name: &str,
    payload: &T,
) -> Result<()> {
    let dir = project_state::user_project_dir(home, project_id);
    fs::create_dir_all(&dir)
        .with_context(|| format!("create {}", dir.display()))?;
    let path = dir.join(name);
    let json = serde_json::to_vec_pretty(payload)?;
    let tmp = dir.join(format!("{}.tmp", name));
    {
        #[cfg(unix)]
        use std::os::unix::fs::OpenOptionsExt;
        let mut opts = fs::OpenOptions::new();
        opts.create(true).write(true).truncate(true);
        #[cfg(unix)]
        opts.mode(0o600);
        let mut f = opts.open(&tmp).with_context(|| format!("create {}", tmp.display()))?;
        f.write_all(&json)?;
        f.sync_all()?;
    }
    fs::rename(&tmp, &path).with_context(|| format!("rename to {}", path.display()))?;
    Ok(())
}

// ============================================================
// project list
// ============================================================

fn cmd_list(args: &[String]) -> Result<()> {
    let json = args.iter().any(|a| a == "--json");
    let caller = resolve_caller()?;
    let projects = project_state::list_projects(&caller.home)?;
    if json {
        println!("{}", serde_json::to_string_pretty(&projects)?);
        return Ok(());
    }
    if projects.is_empty() {
        println!("no swarmgit projects on this envoy (for {})", caller.home.display());
        return Ok(());
    }
    println!(
        "{:<24} {:<14} {:<14} {:<8} {}",
        "HANDLE", "SCOPE", "MEMBERS", "TOKENS", "PROJECT_ID"
    );
    for p in &projects {
        let pid = parse_hex_id(&p.project_id).unwrap_or([0u8; ID_LEN]);
        let token_len = TokenChain::load(&caller.home, &pid)
            .ok()
            .flatten()
            .map(|c| c.chain.len())
            .unwrap_or(0);
        println!(
            "{:<24} {:<14} {:<14} {:<8} {}",
            truncate(&p.handle, 24),
            p.dr_scope.as_str(),
            p.members.len(),
            token_len,
            short_id(&p.project_id),
        );
    }
    Ok(())
}

fn truncate(s: &str, max: usize) -> String {
    if s.chars().count() <= max {
        s.to_string()
    } else {
        let prefix: String = s.chars().take(max.saturating_sub(1)).collect();
        format!("{}…", prefix)
    }
}

fn short_id(hex_str: &str) -> String {
    if hex_str.len() <= 16 {
        hex_str.to_string()
    } else {
        format!("{}…", &hex_str[..16])
    }
}

// ============================================================
// project status
// ============================================================

fn cmd_status(args: &[String]) -> Result<()> {
    let project_arg = args
        .first()
        .context("usage: vigo swarm swarmgit project status <project_id>")?;
    let caller = resolve_caller()?;
    let project_id = resolve_project_id(&caller.home, project_arg)?;
    let cfg = ProjectConfig::load(&caller.home, &project_id)?
        .ok_or_else(|| anyhow!("not a member of project {}", hex_id(&project_id)))?;
    let chain = TokenChain::load(&caller.home, &project_id)?;

    println!("Project:           {}", cfg.handle);
    println!("URL:               swarmgit://{}/{}", cfg.project_id, cfg.handle);
    println!("Project ID:        {}", cfg.project_id);
    println!("Founder:           ed25519:{}", cfg.founder_puddle_pubkey);
    println!("Scope:             {}", cfg.dr_scope.as_str());
    if let Some(when) = cfg.tombstoned_at {
        println!(
            "TOMBSTONED:        since unix-ts {} — this project_id has been", when
        );
        println!(
            "                   retired by the founder. Existing content stays"
        );
        println!(
            "                   accessible locally; no new pushes will fan out"
        );
        println!(
            "                   to peers."
        );
        println!(
            "                   Run `vigo swarm swarmgit project re-found {}`",
            cfg.handle
        );
        println!(
            "                   to mint a fresh project_id, or"
        );
        println!(
            "                   `vigo swarm swarmgit project leave {}` to",
            cfg.project_id
        );
        println!(
            "                   drop the local archive."
        );
    }
    println!("Members:           {}", cfg.members.len());
    for m in &cfg.members {
        println!("  ed25519:{}", m);
    }
    if !cfg.cross_subnet_members.is_empty() {
        println!("Cross-subnet hosts:");
        for h in &cfg.cross_subnet_members {
            println!("  {}", h);
        }
    }
    if let Some(ch) = chain {
        println!("Token chain depth: {}", ch.chain.len());
        if let Some(leaf) = ch.chain.last() {
            println!("Leaf role:         {:?}", leaf.body.role);
            println!("Leaf subject:      ed25519:{}", hex_id(&leaf.body.subject_puddle_pubkey));
            println!("Leaf issued_at:    {}", leaf.body.issued_at);
            match leaf.body.expires_at {
                None => println!("Leaf expires_at:   never"),
                Some(NEVER_EXPIRES) => println!("Leaf expires_at:   never"),
                Some(t) => println!("Leaf expires_at:   {}", t),
            }
        }
    } else {
        println!(
            "Token chain:       (missing) — local membership token is gone,"
        );
        println!(
            "                   so this envoy can't push or fetch the project"
        );
        println!(
            "                   until you re-join. Run"
        );
        println!(
            "                   `vigo swarm swarmgit project join {}`.",
            cfg.project_id
        );
    }
    // ADR-019: founder-rekey delegation chain.
    let delegations = project_state::DelegationChain::load(&caller.home, &project_id)?
        .map(|c| c.delegations)
        .unwrap_or_default();
    let dlen = delegations.len();
    let cap = super::envelopes::DELEGATION_MAX_LEN;
    let warn = super::envelopes::DELEGATION_WARN_LEN;
    if dlen == 0 {
        println!("Delegation chain:  none (founder hasn't rekeyed since project init)");
    } else if dlen >= warn {
        println!(
            "Delegation chain:  {}/{} hops — chain is approaching the cap.", dlen, cap
        );
        println!(
            "                   Each rekey appends a hop; once at the cap"
        );
        println!(
            "                   (currently {}), the next rekey will be refused.", cap
        );
        println!(
            "                   Consider `vigo swarm swarmgit project re-found`"
        );
        println!(
            "                   to mint a fresh project_id and reset the chain."
        );
        if let Some(last) = delegations.last() {
            println!(
                "Current founder:   ed25519:{} (since unix-ts {})",
                last.new_founder_pubkey, last.delegated_at
            );
        }
        if dlen >= cap {
            println!(
                "                   AT CAP — the next rekey will be refused"
            );
            println!(
                "                   unless --force-orphan-projects is passed."
            );
        }
    } else {
        println!("Delegation chain:  {}/{} hops", dlen, cap);
        if let Some(last) = delegations.last() {
            println!(
                "Current founder:   ed25519:{} (since unix-ts {})",
                last.new_founder_pubkey, last.delegated_at
            );
        }
    }
    let state_dir = project_state::user_project_dir(&caller.home, &project_id);
    println!("State dir:         {}", state_dir.display());
    let leave_pending = state_dir.join("leave.pending").exists();
    let revoke_pending = state_dir.join("revoke.pending").exists();
    if leave_pending {
        println!("LEAVE pending:     yes (helper daemon will process in step 4)");
    }
    if revoke_pending {
        println!("REVOKE pending:    yes (helper daemon will process in step 4)");
    }
    Ok(())
}

// ============================================================
// project revoke
// ============================================================

fn cmd_revoke(args: &[String]) -> Result<()> {
    let mut project_arg: Option<String> = None;
    let mut token_id_hex: Option<String> = None;
    let mut reason = String::new();
    let mut i = 0;
    while i < args.len() {
        match args[i].as_str() {
            "--token-id" => {
                i += 1;
                token_id_hex = Some(args.get(i).context("--token-id requires a value")?.to_string());
            }
            "--reason" => {
                i += 1;
                reason = args.get(i).context("--reason requires a value")?.to_string();
            }
            other if !other.starts_with('-') && project_arg.is_none() => {
                project_arg = Some(other.to_string());
            }
            other => bail!("unknown revoke flag: {}", other),
        }
        i += 1;
    }
    let project_arg = project_arg.context(
        "usage: vigo swarm swarmgit project revoke <project_id> --token-id <hex> [--reason <text>]",
    )?;
    let token_id_hex = token_id_hex.context("--token-id is required")?;
    let token_id = parse_hex_id(&token_id_hex).context("invalid --token-id hex")?;

    let caller = resolve_caller()?;
    let project_id = resolve_project_id(&caller.home, &project_arg)?;
    let cfg = ProjectConfig::load(&caller.home, &project_id)?
        .ok_or_else(|| anyhow!("not a member of project {}", hex_id(&project_id)))?;
    refuse_if_tombstoned(&cfg, "revoke")?;

    // Caller's leaf must be Admin to revoke.
    let chain = TokenChain::load(&caller.home, &project_id)?
        .ok_or_else(|| anyhow!("token chain missing for {}", hex_id(&project_id)))?;
    let leaf = chain
        .chain
        .last()
        .ok_or_else(|| anyhow!("token chain is empty"))?;
    if leaf.body.role != Role::Admin {
        bail!("this fleet holds a {:?} token on '{}'; only admins can revoke", leaf.body.role, cfg.handle);
    }

    // Sign the REVOKE envelope inline via the user's puddle
    // session helper. The leaf admin token's subject_puddle_pubkey IS
    // this user's puddle pubkey (verified above by the role check) —
    // signing with that key produces an envelope the receiving side
    // can authorize against the project's token tree.
    let admin_pub = local_puddle_pubkey_bytes(&caller.home)?;
    let revoked_at = now_secs();
    let envelope = super::envelopes::RevokeEnvelope::build_with(
        &project_id,
        &token_id,
        revoked_at,
        &reason,
        &admin_pub,
        |bytes| sign_with_session(bytes),
    )?;
    write_sentinel(&caller.home, &project_id, "revoke.pending", &envelope)?;
    println!("queued REVOKE of token {} on project '{}'", hex_id(&token_id), cfg.handle);
    println!();
    println!("The helper daemon will persist + gossip the revocation envelope on its next sweep.");
    Ok(())
}

// Compile-time hygiene: keep `token_issue_child` imported so future
// CLI work (re-issuance, rotation) can use it without re-importing.
#[allow(dead_code)]
fn _ensure_issue_child_in_scope(
    signing_key: &ed25519_dalek::SigningKey,
    parent: &Token,
    subject: &[u8; ID_LEN],
) -> Result<Token> {
    token_issue_child(signing_key, parent, subject, Role::Member, 0, None)
}

// ============================================================
// project reanchor
// ============================================================

fn cmd_reanchor(args: &[String]) -> Result<()> {
    let project_arg = args
        .first()
        .context("usage: vigo swarm swarmgit project reanchor <project_id>")?;

    let caller = resolve_caller()?;
    let project_id = resolve_project_id(&caller.home, project_arg)?;
    let cfg = ProjectConfig::load(&caller.home, &project_id)?
        .ok_or_else(|| anyhow!("not a member of project {}", hex_id(&project_id)))?;

    // The new founder is the calling user's puddle identity. If
    // that's the same as the existing founder, reanchor is a no-op
    // (and would overwrite local state under the same dir) — refuse.
    let new_founder_pub = local_puddle_pubkey_bytes(&caller.home)?;
    let new_project_id = project_id_for(&new_founder_pub, &cfg.handle);
    if new_project_id == project_id {
        bail!(
            "this user is already the founder of project '{}' — reanchor is meant for \
             surviving members to take over after the original founder puddle is lost. \
             Nothing to do.",
            cfg.handle
        );
    }
    if project_state::ProjectConfig::load(&caller.home, &new_project_id)?.is_some() {
        bail!(
            "a project already exists under the proposed new project_id ({}). \
             Either you already reanchored (run `project status {}`) or you have a \
             different project under that anchor — abort.",
            hex_id(&new_project_id),
            hex_id(&new_project_id),
        );
    }

    eprintln!(
        "Reanchor — hard-fork project '{}' under a new founder.

  • The current URL `swarmgit://{}/{}` becomes permanently dead.
  • New URL will be `swarmgit://{}/{}`.
  • Every existing member must re-add the new remote URL by hand.
  • Existing tokens (admin or member) are NOT portable to the new project.
  • Refs + objects from the old bare repo are imported wholesale into the
    new bare repo when this command runs as root (otherwise the local
    auth surface is rebuilt and a member with root can re-run later).

To proceed, type the project handle exactly: {}",
        cfg.handle, cfg.project_id, cfg.handle,
        hex_id(&new_project_id), cfg.handle, cfg.handle
    );
    eprint!("> ");
    let _ = std::io::stderr().flush();
    let mut input = String::new();
    std::io::stdin()
        .read_line(&mut input)
        .context("read confirmation")?;
    let typed = input.trim();
    if typed != cfg.handle {
        bail!("typed value '{}' did not match project handle '{}'; reanchor aborted", typed, cfg.handle);
    }

    // Mint the new founder bootstrap admin token under the caller's
    // fleet. Same shape `cmd_init` produces — admin role, no parent,
    // no expiry — but with the new founder pubkey + new project_id.
    let now = now_secs();
    let body = TokenBody {
        project_id: new_project_id,
        handle: cfg.handle.clone(),
        subject_puddle_pubkey: new_founder_pub,
        role: Role::Admin,
        issued_at: now,
        expires_at: None,
        signer_puddle_pubkey: new_founder_pub,
        parent_token_id: None,
    };
    let bytes = canonical_bytes(&body);
    let signature = sign_with_session(&bytes)?;
    let bootstrap = Token { body, signature };
    bootstrap
        .verify_signature()
        .context("bootstrap token failed self-verification (session helper malfunction?)")?;

    // Persist new project.json + tokens.json under the caller's home,
    // anchored on the new founder. Members list starts as just this
    // puddle — surviving members from the old project must be invited
    // back via fresh `project invite` calls.
    let new_cfg = ProjectConfig {
        version: project_state::PROJECT_STATE_VERSION,
        project_id: hex_id(&new_project_id),
        handle: cfg.handle.clone(),
        founder_puddle_pubkey: hex_id(&new_founder_pub),
        members: vec![hex_id(&new_founder_pub)],
        dr_scope: cfg.dr_scope,
        cross_subnet_members: cfg.cross_subnet_members.clone(),
        updated_at: now,
        tombstoned_at: None,
    };
    new_cfg.save(&caller.home)?;
    let new_chain = TokenChain {
        version: project_state::PROJECT_STATE_VERSION,
        chain: vec![bootstrap],
    };
    new_chain.save(&caller.home, &new_project_id)?;

    // Byte migration of the bare repo (root-only). The old repo at
    // /var/lib/vigo/swarm/swarmgit/projects/<old_pid>.git is read
    // into the new repo at projects/<new_pid>.git via
    // `git -C <new> fetch <old> +refs/*:refs/*`. Skip cleanly when the
    // old repo doesn't exist locally (this envoy was a non-replica
    // member that never received the bytes) or when the caller isn't
    // root (the bare repos are 0700 root:root).
    let base = crate::platform::paths::default_swarmgit_dir();
    let old_repo = project_repo_path(&base, &project_id);
    let new_repo = project_repo_path(&base, &new_project_id);
    let mut migrated_refs = 0usize;
    if !caller.is_root {
        eprintln!(
            "warning: caller is not root, skipping bare-repo byte migration. Re-run as root \
             on this envoy (or any other member envoy) to import the old bare repo into \
             {}.",
            new_repo.display()
        );
    } else if !old_repo.exists() {
        eprintln!(
            "note: no bare repo at {} on this envoy — local auth surface rebuilt only. \
             Run `reanchor` as root on an envoy that holds the old bare repo to perform \
             the byte migration.",
            old_repo.display()
        );
    } else {
        super::fan_out::ensure_bare_repo(&new_repo)?;
        let pre = super::fan_out::snapshot_refs(&new_repo).unwrap_or_default();
        // Use git fetch <bundle-or-repo> +refs/*:refs/* to copy refs
        // + the objects backing them. The old repo is a local path,
        // git speaks file-protocol against it.
        use super::git_ops::{build_command, RunAs};
        let mut cmd = build_command(RunAs::Root);
        cmd.arg("-C")
            .arg(&new_repo)
            .arg("fetch")
            .arg(&old_repo)
            .arg("+refs/*:refs/*")
            .stdin(std::process::Stdio::null())
            .stdout(std::process::Stdio::null())
            .stderr(std::process::Stdio::piped());
        let out = cmd.output().context("git fetch <old_repo>")?;
        if !out.status.success() {
            let stderr = String::from_utf8_lossy(&out.stderr);
            bail!(
                "git fetch into new bare repo failed: {}",
                stderr.trim()
            );
        }
        let post = super::fan_out::snapshot_refs(&new_repo).unwrap_or_default();
        for (ref_name, sha) in &post {
            if pre.get(ref_name) != Some(sha) {
                migrated_refs += 1;
            }
        }
    }

    println!("✓ Reanchored project '{}'", cfg.handle);
    println!("  Old URL:    swarmgit://{}/{}", cfg.project_id, cfg.handle);
    println!("  New URL:    swarmgit://{}/{}", hex_id(&new_project_id), cfg.handle);
    println!("  New founder: ed25519:{}", hex_id(&new_founder_pub));
    if migrated_refs > 0 {
        println!("  Migrated {} ref(s) from old bare repo into new bare repo", migrated_refs);
    } else if caller.is_root && old_repo.exists() {
        println!("  Old bare repo had no refs to migrate (empty repo).");
    }
    println!();
    println!("Next steps:");
    println!("  • Re-publish from your working repo:");
    println!("    `cd <repo> && vigo swarm swarmgit project push {}`", hex_id(&new_project_id));
    println!("  • Re-invite each surviving member fleet via");
    println!("    `vigo swarm swarmgit project invite {} --puddle <fp> --role <r>`",
             hex_id(&new_project_id));
    println!("  • Each invitee runs `project join {}`, then re-points their remote with",
             hex_id(&new_project_id));
    println!("    `vigo swarm swarmgit project push {} --repo <repo>`", hex_id(&new_project_id));
    println!("  • The old project state at {} is left in place — drop it via `project leave {}` once you confirm the new project is healthy.",
             cfg.project_id, cfg.project_id);
    Ok(())
}

// ============================================================
// project re-found (ADR-019 slice 5)
// ============================================================

/// Escape hatch when a founded project's delegation chain is at
/// `DELEGATION_MAX_LEN` (or has been tombstoned by a
/// `--force-orphan-projects` rekey). Mints a fresh project_id from
/// the current puddle pubkey, byte-migrates from the at-cap bare repo
/// via the same `git fetch +refs/*:refs/*` path `reanchor` uses, and
/// tombstones the old project_id.
///
/// Differs from `reanchor`:
/// - `reanchor` is for surviving members taking over after the
///   original founder puddle is *gone*. Refuses if the calling user
///   IS the current founder.
/// - `re-found` is for the founder themselves resetting their own
///   project chain. REQUIRES that the calling user is the current
///   founder (chain.last().new_founder_pubkey or
///   founder_puddle_pubkey, depending on whether the founder has
///   rekeyed).
fn cmd_re_found(args: &[String]) -> Result<()> {
    let mut project_arg: Option<String> = None;
    let mut force = false;
    let mut i = 0;
    while i < args.len() {
        match args[i].as_str() {
            "--force" => force = true,
            other if !other.starts_with('-') && project_arg.is_none() => {
                project_arg = Some(other.to_string());
            }
            other => bail!("unknown re-found flag: {}", other),
        }
        i += 1;
    }
    let project_arg = project_arg
        .context("usage: vigo swarm swarmgit project re-found <project_id> [--force]")?;

    let caller = resolve_caller()?;
    let local_pub = local_puddle_pubkey_bytes(&caller.home)?;
    let project_id = resolve_project_id(&caller.home, &project_arg)?;
    let cfg = ProjectConfig::load(&caller.home, &project_id)?
        .ok_or_else(|| anyhow!("not a member of project {}", hex_id(&project_id)))?;

    // Caller must be the project's current founder. Predicate matches
    // re-invite-all's: chain.last().new_founder_pubkey if there are
    // delegations, else founder_puddle_pubkey.
    let delegations = DelegationChain::load(&caller.home, &project_id)?
        .map(|c| c.delegations)
        .unwrap_or_default();
    let current_founder_hex: String = if let Some(last) = delegations.last() {
        last.new_founder_pubkey.to_ascii_lowercase()
    } else {
        cfg.founder_puddle_pubkey.to_ascii_lowercase()
    };
    if current_founder_hex != hex_id(&local_pub).to_ascii_lowercase() {
        bail!(
            "this user is not the current founder of project '{}' — re-found is for founders \
             resetting their own project chain. Use `reanchor` if the original founder puddle \
             is gone and you're a surviving member taking over.",
            cfg.handle
        );
    }

    // Precondition: chain at cap, project tombstoned, or --force.
    let at_cap = delegations.len() >= super::envelopes::DELEGATION_MAX_LEN;
    let tombstoned = cfg.tombstoned_at.is_some();
    if !at_cap && !tombstoned && !force {
        bail!(
            "project '{}' is not at the delegation cap ({}/{}); re-found is normally only \
             needed when the chain has hit cap or the project has been tombstoned by \
             --force-orphan-projects. Pass --force to re-found anyway (manual \
             chain-hygiene reset).",
            cfg.handle,
            delegations.len(),
            super::envelopes::DELEGATION_MAX_LEN
        );
    }

    // The new project_id is sha256(current_puddle_pubkey). If the
    // founder hasn't rekeyed since founding (current_pubkey ==
    // genesis_pubkey), the new project_id collides with the old —
    // re-found is impossible without first rekeying the puddle.
    let new_project_id = project_id_for(&local_pub, &cfg.handle);
    if new_project_id == project_id {
        bail!(
            "the founder hasn't rekeyed since founding project '{}'; the new project_id would \
             collide with the existing one ({}). Run `vigo swarm puddle rekey-pair` first, \
             or use `reanchor` if you intended to hard-fork.",
            cfg.handle,
            cfg.project_id
        );
    }
    if ProjectConfig::load(&caller.home, &new_project_id)?.is_some() {
        bail!(
            "a project already exists under the proposed new project_id ({}). \
             Either you already re-founded (run `project status {}`) or you have a \
             different project under the current puddle identity — abort.",
            hex_id(&new_project_id),
            hex_id(&new_project_id),
        );
    }

    eprintln!(
        "Re-found — mint a fresh project_id for '{}' under your current puddle identity.

  • The current URL `swarmgit://{}/{}` becomes permanently dead.
  • New URL will be `swarmgit://{}/{}`.
  • Every existing member must update their remote URL and rejoin under
    the new project_id. Run `re-invite-all {}` after this completes.
  • Existing tokens (admin or member) are NOT portable to the new project.
  • Refs + objects from the old bare repo are imported wholesale into the
    new bare repo when this command runs as root (otherwise the local
    auth surface is rebuilt and root can re-run later).
  • The old project_id will be tombstoned: bare repo + URL stay on disk
    as an archive, but new admin operations against it are refused.

To proceed, type the project handle exactly: {}",
        cfg.handle, cfg.project_id, cfg.handle,
        hex_id(&new_project_id), cfg.handle, hex_id(&new_project_id), cfg.handle
    );
    eprint!("> ");
    let _ = std::io::stderr().flush();
    let mut input = String::new();
    std::io::stdin()
        .read_line(&mut input)
        .context("read confirmation")?;
    let typed = input.trim();
    if typed != cfg.handle {
        bail!("typed value '{}' did not match project handle '{}'; re-found aborted", typed, cfg.handle);
    }

    let now = now_secs();

    // Mint the new founder bootstrap admin token under the caller's
    // CURRENT fleet (which is the freshly-rekeyed identity). Same
    // shape `cmd_init` produces.
    let body = TokenBody {
        project_id: new_project_id,
        handle: cfg.handle.clone(),
        subject_puddle_pubkey: local_pub,
        role: Role::Admin,
        issued_at: now,
        expires_at: None,
        signer_puddle_pubkey: local_pub,
        parent_token_id: None,
    };
    let bytes = canonical_bytes(&body);
    let signature = sign_with_session(&bytes)?;
    let bootstrap = Token { body, signature };
    bootstrap
        .verify_signature()
        .context("bootstrap token failed self-verification (session helper malfunction?)")?;

    // Persist new project.json + tokens.json under the new project_id.
    // Members list starts as `[this_fleet]`; founder must run
    // `re-invite-all <new_pid>` to bring cross-puddle members along.
    let new_cfg = ProjectConfig {
        version: project_state::PROJECT_STATE_VERSION,
        project_id: hex_id(&new_project_id),
        handle: cfg.handle.clone(),
        founder_puddle_pubkey: hex_id(&local_pub),
        members: vec![hex_id(&local_pub)],
        dr_scope: cfg.dr_scope,
        cross_subnet_members: cfg.cross_subnet_members.clone(),
        updated_at: now,
        tombstoned_at: None,
    };
    new_cfg.save(&caller.home)?;
    let new_chain = TokenChain {
        version: project_state::PROJECT_STATE_VERSION,
        chain: vec![bootstrap],
    };
    new_chain.save(&caller.home, &new_project_id)?;

    // ADR-022: if the founder holds a friendly-name claim that pointed
    // at THIS project's old anchor, re-issue it to point at the new
    // anchor (== `local_pub`) carrying this project's rekey-delegation
    // chain in `supersedes` — proving the new anchor descends from the
    // old. A resolver seeing both the (stale) old claim and the new one
    // treats it as a supersession, so `swarmgit://<name>/<handle>`
    // transparently follows the re-found. Best-effort: a failure here
    // doesn't roll back the re-found; the operator can re-run
    // `vigo swarm puddle name set <name>` afterwards.
    let new_anchor_hex = hex_id(&local_pub);
    match crate::swarm::puddle::name::NameClaim::load(&caller.home) {
        Ok(Some(claim)) => {
            if claim.body.puddle_pubkey.eq_ignore_ascii_case(&new_anchor_hex) {
                println!(
                    "  Note: your puddle name '{}' already points at the new anchor — left unchanged.",
                    claim.body.name
                );
            } else if claim.body.puddle_pubkey.eq_ignore_ascii_case(&cfg.founder_puddle_pubkey) {
                let body = crate::swarm::puddle::name::NameClaimBody {
                    puddle_pubkey: new_anchor_hex.clone(),
                    name: claim.body.name.clone(),
                    claimed_at: now,
                    supersedes: delegations.clone(),
                };
                match crate::swarm::puddle::name::NameClaim::issue_with(body, |msg| {
                    sign_with_session(msg)
                }) {
                    Ok(new_claim) => match new_claim.save(&caller.home, &caller.username) {
                        Ok(()) => {
                            println!(
                                "  Re-issued puddle name '{}' → ed25519:{} (carries the rekey-delegation chain)",
                                claim.body.name, new_anchor_hex
                            );
                            println!(
                                "    swarmgit://{}/{} now follows the re-found transparently.",
                                claim.body.name, cfg.handle
                            );
                        }
                        Err(e) => eprintln!(
                            "  warning: re-issued the puddle name claim but couldn't write it ({}); \
                             run `vigo swarm puddle name set {}` after this completes.",
                            e, claim.body.name
                        ),
                    },
                    Err(e) => eprintln!(
                        "  warning: couldn't re-issue puddle name claim '{}' ({}); \
                         run `vigo swarm puddle name set {}` after this completes.",
                        claim.body.name, e, claim.body.name
                    ),
                }
            } else {
                println!(
                    "  Note: your puddle name '{}' points at a different anchor (not this project's) — left unchanged.",
                    claim.body.name
                );
            }
        }
        Ok(None) => {}
        Err(e) => eprintln!(
            "  warning: couldn't read your puddle name claim ({}); not re-issued.",
            e
        ),
    }

    // Byte migration: copy refs from the at-cap bare repo into the
    // new bare repo. Same path as `reanchor`. Root-only; non-root
    // callers see a note and the migration must be re-run by root
    // separately.
    let base = crate::platform::paths::default_swarmgit_dir();
    let old_repo = project_repo_path(&base, &project_id);
    let new_repo = project_repo_path(&base, &new_project_id);
    let mut migrated_refs = 0usize;
    if !caller.is_root {
        eprintln!(
            "warning: caller is not root, skipping bare-repo byte migration. Re-run as root \
             on this envoy to import the old bare repo into {}.",
            new_repo.display()
        );
    } else if !old_repo.exists() {
        eprintln!(
            "note: no bare repo at {} on this envoy — local auth surface rebuilt only. \
             Run `re-found` as root on an envoy that holds the old bare repo to perform \
             the byte migration.",
            old_repo.display()
        );
    } else {
        super::fan_out::ensure_bare_repo(&new_repo)?;
        let pre = super::fan_out::snapshot_refs(&new_repo).unwrap_or_default();
        use super::git_ops::{build_command, RunAs};
        let mut cmd = build_command(RunAs::Root);
        cmd.arg("-C")
            .arg(&new_repo)
            .arg("fetch")
            .arg(&old_repo)
            .arg("+refs/*:refs/*")
            .stdin(std::process::Stdio::null())
            .stdout(std::process::Stdio::null())
            .stderr(std::process::Stdio::piped());
        let out = cmd.output().context("git fetch <old_repo>")?;
        if !out.status.success() {
            let stderr = String::from_utf8_lossy(&out.stderr);
            bail!("git fetch into new bare repo failed: {}", stderr.trim());
        }
        let post = super::fan_out::snapshot_refs(&new_repo).unwrap_or_default();
        for (ref_name, sha) in &post {
            if pre.get(ref_name) != Some(sha) {
                migrated_refs += 1;
            }
        }
    }

    // Tombstone the old project on disk. The bare repo + URL stay
    // (data archive); helper daemon and CLI verbs honor the
    // tombstone marker. Survives a follow-up `project leave` if the
    // operator decides to drop the archive entirely.
    let mut old_cfg = cfg.clone();
    old_cfg.tombstoned_at = Some(now);
    old_cfg.updated_at = now;
    old_cfg
        .save(&caller.home)
        .context("tombstoning old project.json")?;

    println!("✓ Re-founded project '{}'", cfg.handle);
    println!("  Old URL:    swarmgit://{}/{}", cfg.project_id, cfg.handle);
    println!("  New URL:    swarmgit://{}/{}", hex_id(&new_project_id), cfg.handle);
    println!("  New founder: ed25519:{}", hex_id(&local_pub));
    if migrated_refs > 0 {
        println!("  Migrated {} ref(s) from old bare repo into new bare repo", migrated_refs);
    } else if caller.is_root && old_repo.exists() {
        println!("  Old bare repo had no refs to migrate (empty repo).");
    }
    println!();
    println!("Next steps:");
    println!("  • Re-publish from your working repo:");
    println!("    `cd <repo> && vigo swarm swarmgit project push {}`", hex_id(&new_project_id));
    println!("  • Run `vigo swarm swarmgit project re-invite-all {}` to mint fresh", hex_id(&new_project_id));
    println!("    invitations for every cross-puddle member of the old project.");
    println!("  • Each member runs `project rejoin {} --invitation -` with the new", hex_id(&new_project_id));
    println!("    invitation, then re-points their remote with");
    println!("    `vigo swarm swarmgit project push {} --repo <repo>`.", hex_id(&new_project_id));
    println!("  • The old project state at {} is tombstoned but kept on disk as an", cfg.project_id);
    println!("    archive. Drop it via `project leave {}` once the new project is healthy.", cfg.project_id);
    Ok(())
}

// ============================================================
// status (envoy-wide)
// ============================================================

/// Envoy-wide swarmgit posture, parallel to `vigo swarm lockbox
/// status`. Built by walking every Unix user's `~/.vigo-swarmgit/`
/// per-user state joined with the per-envoy bare repos under the
/// swarmgit base dir, plus the helper-daemon socket check. Distinct
/// from `project status <id>`, which shows one project in detail.
///
/// Read-only. Safe for any user; does not require puddle to be
/// unlocked.
fn cmd_status_top(_args: &[String]) -> Result<()> {
    let base = crate::platform::paths::default_swarmgit_dir();
    let socket = helper_socket_path();
    let passwd = std::fs::read_to_string(passwd_path()).unwrap_or_default();
    let projects = collect_projects(&passwd, &base);
    print_status_top(&base, &socket, &projects, &mut std::io::stdout())
}

fn print_status_top<W: std::io::Write>(
    base: &Path,
    socket: &Path,
    projects: &[ProjectEntry],
    out: &mut W,
) -> Result<()> {
    writeln!(out, "swarmgit status")?;
    writeln!(out, "  base:                {}", base.display())?;

    if projects.is_empty() {
        writeln!(
            out,
            "  projects:            none — no Unix user on this envoy has founded or joined one"
        )?;
    } else {
        let with_repo = projects.iter().filter(|p| p.has_bare_repo).count();
        let total_bytes: u64 = projects.iter().map(|p| p.bare_repo_size_bytes).sum();
        let revokes: usize = projects.iter().map(|p| p.envelopes.revokes).sum();
        let leaves: usize = projects.iter().map(|p| p.envelopes.leaves).sum();
        writeln!(
            out,
            "  projects:            {} ({} with a local bare repo)",
            projects.len(),
            with_repo
        )?;
        writeln!(out, "  bare repos:          {} on disk", human_bytes(total_bytes))?;
        writeln!(
            out,
            "  envelopes:           {} revoke(s), {} leave(s) queued",
            revokes, leaves
        )?;
        for p in projects {
            let repo_state = if p.has_bare_repo {
                format!(
                    "{} ref(s), {}",
                    p.head_refs.len(),
                    human_bytes(p.bare_repo_size_bytes)
                )
            } else {
                "no bare repo yet (awaiting first push or DR catch-up)".to_string()
            };
            writeln!(out, "    {:<24} {:<13} {}", p.handle, p.dr_scope, repo_state)?;
        }
    }

    // Helper daemon socket — the ADR-015 helper-daemon transport for
    // git push/fetch over swarm. Existence of the socket file is the
    // cheapest "is the daemon up?" signal we can give without a
    // round-trip.
    if socket.exists() {
        writeln!(out, "  helper daemon:       socket present ({})", socket.display())?;
    } else {
        writeln!(
            out,
            "  helper daemon:       socket absent ({}) — daemon not running",
            socket.display()
        )?;
        writeln!(
            out,
            "                       or not yet bound. Non-root git operations"
        )?;
        writeln!(
            out,
            "                       against `swarmgit://` URLs will fail until the"
        )?;
        writeln!(
            out,
            "                       agent restarts and binds the socket. Run"
        )?;
        writeln!(
            out,
            "                       `sudo systemctl restart vigo-envoy`."
        )?;
    }
    Ok(())
}

/// Human-friendly byte count for status output (base-1000, one decimal).
fn human_bytes(n: u64) -> String {
    const K: f64 = 1000.0;
    let f = n as f64;
    if f < K {
        format!("{} B", n)
    } else if f < K * K {
        format!("{:.1} KB", f / K)
    } else if f < K * K * K {
        format!("{:.1} MB", f / (K * K))
    } else {
        format!("{:.1} GB", f / (K * K * K))
    }
}

// ============================================================
// purge — full local-state wipe, parallel to lockbox purge
// ============================================================

/// Power-user verb. Wipes ALL local swarmgit state on this envoy.
/// Symmetric with `vigo swarm lockbox purge`. Distinct from
/// `project leave --purge` (one project's bare repo) and from the
/// hidden `_apply-blacklist` (server-driven, all projects but root-
/// owned only).
///
/// **As a non-root caller** wipes `~/.vigo-swarmgit/` for the
/// invoking user — every joined project's `project.json`, `tokens.json`,
/// `delegations.json`, and any pending sentinels. Root-owned bare repos
/// under `/var/lib/vigo/swarm/swarmgit/projects/` survive; rerun this
/// command under sudo to wipe those too.
///
/// **As root** ALSO wipes envoy-wide root-owned state: every bare repo
/// under `projects/`. The user-side dot-dirs are not touched in root
/// mode (each user's home is theirs to manage).
///
/// Refuses without `--yes`. Does not gossip — peers age this envoy
/// out via the swarm staleness window unless the operator runs
/// `project leave [--yes]` per project first.
fn cmd_purge(args: &[String]) -> Result<()> {
    let mut yes = false;
    for a in args {
        match a.as_str() {
            "--yes" | "-y" => yes = true,
            "--help" | "-h" => {
                eprintln!(
                    "vigo swarm swarmgit purge — wipe ALL local swarmgit state\n\n\
                     USAGE:\n    vigo swarm swarmgit purge --yes\n\n\
                     As a non-root user: scrubs ~/.vigo-swarmgit/ (every joined\n\
                     project's per-user state for this user). Root-owned bare repos\n\
                     under /var/lib/vigo/swarm/swarmgit/projects/ survive — rerun\n\
                     under sudo to scrub those too.\n\n\
                     As root: also scrubs the envoy-wide projects/ dir.\n\n\
                     Refuses without --yes. Does NOT gossip — run `project leave`\n\
                     per project first if you want peers to learn of the departure."
                );
                return Ok(());
            }
            other if other.starts_with("--") => bail!("unknown flag: {}", other),
            _ => bail!("vigo swarm swarmgit purge takes no positional arguments; got {}", a),
        }
    }

    let caller = resolve_caller()?;
    let base = crate::platform::paths::default_swarmgit_dir();
    do_purge(&caller.home, &base, caller.is_root, yes, &mut std::io::stdout())
}

fn do_purge<W: std::io::Write>(
    home: &Path,
    base: &Path,
    is_root: bool,
    yes: bool,
    out: &mut W,
) -> Result<()> {
    let user_dot = project_state::user_dot_dir(home);

    let user_list: Vec<PathBuf> = if user_dot.exists() {
        vec![user_dot.clone()]
    } else {
        Vec::new()
    };

    let root_list: Vec<PathBuf> = if is_root {
        [projects_dir(base)]
            .into_iter()
            .filter(|p: &PathBuf| p.exists())
            .collect()
    } else {
        Vec::new()
    };

    if user_list.is_empty() && root_list.is_empty() {
        if is_root {
            writeln!(
                out,
                "nothing to purge — neither {} nor any state under {} exists.",
                user_dot.display(),
                base.display()
            )?;
        } else {
            writeln!(
                out,
                "nothing to purge — {} does not exist for this user.",
                user_dot.display()
            )?;
        }
        return Ok(());
    }

    if !yes {
        writeln!(out, "This will WIPE local swarmgit state on this envoy:")?;
        for p in &user_list {
            writeln!(out, "  {}  (per-user project state)", p.display())?;
        }
        for p in &root_list {
            writeln!(out, "  {}  (envoy-wide, root-owned)", p.display())?;
        }
        if !is_root {
            writeln!(out)?;
            writeln!(
                out,
                "Note: running as a non-root user. Root-owned envoy-wide state under"
            )?;
            writeln!(
                out,
                "      {} is NOT included — rerun under sudo to scrub it too.",
                base.display()
            )?;
        }
        writeln!(out)?;
        writeln!(
            out,
            "Re-run with --yes to proceed. Peers will NOT learn of the departure unless"
        )?;
        writeln!(
            out,
            "you also run `vigo swarm swarmgit project leave [--yes]` per project first."
        )?;
        return Ok(());
    }

    let pending = scan_pending_sentinels(&user_dot);

    let mut wiped: Vec<PathBuf> = Vec::new();
    let mut failed: Vec<(PathBuf, String)> = Vec::new();

    for target in user_list.iter().chain(root_list.iter()) {
        let res = if target.is_dir() {
            fs::remove_dir_all(target)
        } else {
            fs::remove_file(target)
        };
        match res {
            Ok(()) => wiped.push(target.clone()),
            Err(e) => failed.push((target.clone(), e.to_string())),
        }
    }

    for target in &wiped {
        writeln!(out, "wiped {}", target.display())?;
    }
    for (target, err) in &failed {
        writeln!(out, "failed to wipe {}: {}", target.display(), err)?;
    }

    if pending > 0 {
        writeln!(out)?;
        writeln!(
            out,
            "note: {} pending gossip sentinel(s) existed at purge time — peers will not learn",
            pending
        )?;
        writeln!(
            out,
            "      of this envoy's departure unless you announced it out-of-band."
        )?;
    }

    if !failed.is_empty() {
        bail!("purge incomplete — {} target(s) failed", failed.len());
    }
    Ok(())
}

/// Count `leave.pending` and `revoke.pending` sentinels under
/// `~/.vigo-swarmgit/<project_id>/`. Used by `purge` to warn the
/// operator that running purge instead of `project leave` skips
/// the gossip step.
fn scan_pending_sentinels(user_dot: &Path) -> usize {
    let mut count = 0usize;
    let entries = match fs::read_dir(user_dot) {
        Ok(e) => e,
        Err(_) => return 0,
    };
    for entry in entries.flatten() {
        let dir = entry.path();
        if !dir.is_dir() {
            continue;
        }
        for name in ["leave.pending", "revoke.pending"] {
            if dir.join(name).exists() {
                count += 1;
            }
        }
    }
    count
}

// ============================================================
// Tests
// ============================================================

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn validate_handle_accepts_normal_names() {
        assert!(validate_handle("vigo-web").is_ok());
        assert!(validate_handle("dotfiles").is_ok());
        assert!(validate_handle("notes_2026").is_ok());
        assert!(validate_handle("v1.0").is_ok());
    }

    #[test]
    fn validate_handle_rejects_empty_and_long() {
        assert!(validate_handle("").is_err());
        assert!(validate_handle(&"x".repeat(65)).is_err());
    }

    #[test]
    fn validate_handle_rejects_leading_dot() {
        assert!(validate_handle(".hidden").is_err());
    }

    #[test]
    fn validate_handle_rejects_unsafe_chars() {
        for bad in ["foo/bar", "foo bar", "foo:bar", "foo$bar", "foo\nbar", "foo\0bar"] {
            assert!(validate_handle(bad).is_err(), "should reject: {:?}", bad);
        }
    }

    #[test]
    fn parse_days_accepts_d_suffix_and_bare() {
        assert_eq!(parse_days("30d").unwrap(), 30);
        assert_eq!(parse_days("60").unwrap(), 60);
        assert_eq!(parse_days("1d").unwrap(), 1);
    }

    #[test]
    fn parse_days_rejects_zero_and_garbage() {
        assert!(parse_days("0").is_err());
        assert!(parse_days("0d").is_err());
        assert!(parse_days("abc").is_err());
        assert!(parse_days("").is_err());
        assert!(parse_days("1w").is_err());
    }

    #[test]
    fn parse_puddle_pubkey_arg_accepts_with_and_without_prefix() {
        let raw = "ab".repeat(32);
        let with_prefix = format!("ed25519:{}", raw);
        assert_eq!(parse_puddle_pubkey_arg(&raw).unwrap(), [0xABu8; 32]);
        assert_eq!(parse_puddle_pubkey_arg(&with_prefix).unwrap(), [0xABu8; 32]);
    }

    #[test]
    fn parse_puddle_pubkey_arg_rejects_short() {
        assert!(parse_puddle_pubkey_arg("deadbeef").is_err());
        assert!(parse_puddle_pubkey_arg("ed25519:deadbeef").is_err());
    }

    #[test]
    fn truncate_short_unchanged() {
        assert_eq!(truncate("short", 10), "short");
    }

    #[test]
    fn truncate_long_gets_ellipsis() {
        let s = truncate("this is a long string", 10);
        assert!(s.ends_with('…'));
        assert_eq!(s.chars().count(), 10);
    }

    #[test]
    fn short_id_truncates_long_hex() {
        let long = "a".repeat(64);
        let s = short_id(&long);
        assert!(s.ends_with('…'));
        assert_eq!(s.len(), "a".repeat(16).len() + '…'.len_utf8());
    }

    #[test]
    fn short_id_passes_through_short_strings() {
        assert_eq!(short_id("abc"), "abc");
    }

    #[test]
    fn invitation_roundtrips_through_json() {
        // The shape that flows over `invite | join`. Build a synthetic
        // chain (uses real Ed25519) and verify deserialization +
        // chain re-verification both succeed end-to-end.
        use crate::swarm::swarmgit::tokens::{issue_bootstrap, issue_child, project_id_for, Role};
        use ed25519_dalek::SigningKey;
        use rand::rngs::OsRng;

        let founder = SigningKey::generate(&mut OsRng);
        let team = SigningKey::generate(&mut OsRng);
        let founder_pub = founder.verifying_key().to_bytes();
        let team_pub = team.verifying_key().to_bytes();
        let pid = project_id_for(&founder_pub, "proj");

        let bootstrap = issue_bootstrap(&founder, "proj", 100, None).unwrap();
        let member = issue_child(&founder, &bootstrap, &team_pub, Role::Member, 150, None).unwrap();

        let invitation = Invitation {
            version: INVITATION_VERSION,
            project: ProjectConfig {
                version: project_state::PROJECT_STATE_VERSION,
                project_id: hex_id(&pid),
                handle: "vigo-web".to_string(),
                founder_puddle_pubkey: hex_id(&founder_pub),
                members: vec![hex_id(&founder_pub), hex_id(&team_pub)],
                dr_scope: DrScope::SwarmWide,
                cross_subnet_members: vec![],
                updated_at: 1_744_560_000,
                tombstoned_at: None,
            },
            chain: TokenChain {
                version: project_state::PROJECT_STATE_VERSION,
                chain: vec![bootstrap, member],
            },
            delegations: Vec::new(),
        };

        let json = serde_json::to_string(&invitation).unwrap();
        let parsed: Invitation = serde_json::from_str(&json).unwrap();
        assert_eq!(parsed.project.handle, "vigo-web");
        assert_eq!(parsed.chain.chain.len(), 2);
        verify_chain(&pid, &founder_pub, &[], &parsed.chain.chain, 200).unwrap();
    }

    // --- top-level status -----------------------------------------

    #[test]
    fn human_bytes_buckets() {
        assert_eq!(human_bytes(0), "0 B");
        assert_eq!(human_bytes(512), "512 B");
        assert_eq!(human_bytes(1_500), "1.5 KB");
        assert_eq!(human_bytes(2_400_000), "2.4 MB");
        assert_eq!(human_bytes(3_100_000_000), "3.1 GB");
    }

    #[test]
    fn print_status_top_on_empty_envoy_does_not_error() {
        // Fresh envoy: no per-user project state, no socket.
        let base = tempfile::tempdir().unwrap();
        let socket = base.path().join("absent.sock");
        let mut buf: Vec<u8> = Vec::new();
        print_status_top(base.path(), &socket, &[], &mut buf).unwrap();
        let out = String::from_utf8(buf).unwrap();
        assert!(out.contains("swarmgit status"));
        assert!(out.contains("projects:            none"));
        assert!(out.contains("socket absent"));
    }

    #[test]
    fn print_status_top_renders_project_summary() {
        use crate::traits::swarmgit::EnvelopeSummary;
        use std::collections::BTreeMap;

        let mut main_ref = BTreeMap::new();
        main_ref.insert("refs/heads/main".to_string(), "abc123".to_string());

        let projects = vec![
            ProjectEntry {
                project_id: "aa".repeat(32),
                handle: "vigo".to_string(),
                founder_puddle_pubkey: "bb".repeat(32),
                members: vec!["bb".repeat(32)],
                dr_scope: "swarm-wide".to_string(),
                users_with_state: vec!["dan".to_string()],
                has_bare_repo: true,
                bare_repo_size_bytes: 2_500_000,
                head_refs: main_ref,
                envelopes: EnvelopeSummary {
                    revokes: 1,
                    leaves: 0,
                    digest_hex: String::new(),
                },
                leaf_role: Some("Admin".to_string()),
                token_chain_depth: 1,
                token_expires_at: None,
            },
            ProjectEntry {
                project_id: "cc".repeat(32),
                handle: "vigo-web".to_string(),
                founder_puddle_pubkey: "bb".repeat(32),
                members: vec!["bb".repeat(32)],
                dr_scope: "members-only".to_string(),
                users_with_state: vec!["dan".to_string()],
                has_bare_repo: false,
                bare_repo_size_bytes: 0,
                head_refs: BTreeMap::new(),
                envelopes: EnvelopeSummary {
                    revokes: 0,
                    leaves: 0,
                    digest_hex: String::new(),
                },
                leaf_role: Some("Admin".to_string()),
                token_chain_depth: 1,
                token_expires_at: None,
            },
        ];

        let base = tempfile::tempdir().unwrap();
        // Plant a fake socket file so the helper-daemon line takes the
        // "present" branch.
        let socket = base.path().join("swarmgit.sock");
        fs::write(&socket, b"").unwrap();

        let mut buf: Vec<u8> = Vec::new();
        print_status_top(base.path(), &socket, &projects, &mut buf).unwrap();
        let out = String::from_utf8(buf).unwrap();

        assert!(out.contains("projects:            2 (1 with a local bare repo)"));
        assert!(out.contains("2.5 MB"));
        assert!(out.contains("1 revoke(s), 0 leave(s)"));
        assert!(out.contains("vigo"));
        assert!(out.contains("vigo-web"));
        assert!(out.contains("no bare repo yet"));
        assert!(out.contains("socket present"));
    }

    // --- purge ----------------------------------------------------

    #[test]
    fn scan_pending_sentinels_counts_leave_and_revoke() {
        let home = tempfile::tempdir().unwrap();
        let dot = project_state::user_dot_dir(home.path());
        // Project A — leave.pending only.
        let a = dot.join("aa".repeat(32));
        fs::create_dir_all(&a).unwrap();
        fs::write(a.join("leave.pending"), b"").unwrap();
        // Project B — revoke.pending only.
        let b = dot.join("bb".repeat(32));
        fs::create_dir_all(&b).unwrap();
        fs::write(b.join("revoke.pending"), b"").unwrap();
        // Project C — both.
        let c = dot.join("cc".repeat(32));
        fs::create_dir_all(&c).unwrap();
        fs::write(c.join("leave.pending"), b"").unwrap();
        fs::write(c.join("revoke.pending"), b"").unwrap();
        // Project D — neither (clean).
        let d = dot.join("dd".repeat(32));
        fs::create_dir_all(&d).unwrap();

        assert_eq!(scan_pending_sentinels(&dot), 4);
    }

    #[test]
    fn scan_pending_sentinels_handles_missing_dot_dir() {
        let home = tempfile::tempdir().unwrap();
        let dot = project_state::user_dot_dir(home.path());
        // dot dir was never created.
        assert_eq!(scan_pending_sentinels(&dot), 0);
    }

    #[test]
    fn do_purge_without_yes_lists_targets_and_does_not_wipe() {
        let home = tempfile::tempdir().unwrap();
        let base = tempfile::tempdir().unwrap();
        let dot = project_state::user_dot_dir(home.path());
        fs::create_dir_all(dot.join("aa".repeat(32))).unwrap();

        let mut buf: Vec<u8> = Vec::new();
        do_purge(home.path(), base.path(), false, false, &mut buf).unwrap();
        let out = String::from_utf8(buf).unwrap();
        assert!(out.contains("This will WIPE"));
        assert!(out.contains(&dot.display().to_string()));
        assert!(out.contains("Re-run with --yes"));
        // Nothing wiped.
        assert!(dot.exists());
    }

    #[test]
    fn do_purge_user_mode_wipes_only_user_dot_dir() {
        let home = tempfile::tempdir().unwrap();
        let base = tempfile::tempdir().unwrap();
        let dot = project_state::user_dot_dir(home.path());
        fs::create_dir_all(dot.join("aa".repeat(32))).unwrap();
        // Lay down a fake root-owned bare repo. Non-root mode must
        // NOT touch it.
        fs::create_dir_all(projects_dir(base.path())).unwrap();

        let mut buf: Vec<u8> = Vec::new();
        do_purge(home.path(), base.path(), false, true, &mut buf).unwrap();

        assert!(!dot.exists(), "user dot dir must be wiped");
        assert!(projects_dir(base.path()).exists(), "root-owned projects/ must survive non-root purge");
    }

    #[test]
    fn do_purge_root_mode_wipes_envoy_state_too() {
        let home = tempfile::tempdir().unwrap();
        let base = tempfile::tempdir().unwrap();
        let dot = project_state::user_dot_dir(home.path());
        fs::create_dir_all(&dot).unwrap();
        fs::create_dir_all(projects_dir(base.path())).unwrap();

        let mut buf: Vec<u8> = Vec::new();
        do_purge(home.path(), base.path(), true, true, &mut buf).unwrap();

        assert!(!dot.exists(), "user dot dir must be wiped");
        assert!(!projects_dir(base.path()).exists(), "projects/ must be wiped under root");
    }

    #[test]
    fn do_purge_no_state_anywhere_exits_clean() {
        let home = tempfile::tempdir().unwrap();
        let base = tempfile::tempdir().unwrap();
        let mut buf: Vec<u8> = Vec::new();
        do_purge(home.path(), base.path(), false, true, &mut buf).unwrap();
        let out = String::from_utf8(buf).unwrap();
        assert!(out.contains("nothing to purge"));
    }

    #[test]
    fn do_purge_warns_when_pending_sentinels_present() {
        let home = tempfile::tempdir().unwrap();
        let base = tempfile::tempdir().unwrap();
        let dot = project_state::user_dot_dir(home.path());
        let proj = dot.join("ee".repeat(32));
        fs::create_dir_all(&proj).unwrap();
        fs::write(proj.join("leave.pending"), b"").unwrap();

        let mut buf: Vec<u8> = Vec::new();
        do_purge(home.path(), base.path(), false, true, &mut buf).unwrap();
        let out = String::from_utf8(buf).unwrap();
        assert!(out.contains("pending gossip sentinel"));
        assert!(out.contains("out-of-band"));
    }
}
