// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! `vigo swarm gitmirror project <verb>` — ADR-015 unified-substrate CLI.
//!
//! ```text
//! vigo swarm gitmirror project init <handle> [--fanout fleet-wide|members-only]
//! vigo swarm gitmirror project invite <project_id> --fleet <fp> --role <r> [--expires <Nd>]
//! vigo swarm gitmirror project join <project_id> [--invitation <file|->]
//! vigo swarm gitmirror project leave <project_id> [--purge] [--yes]
//! vigo swarm gitmirror project list [--json]
//! vigo swarm gitmirror project status <project_id>
//! vigo swarm gitmirror project revoke <project_id> --token-id <hex> [--reason <text>]
//! vigo swarm gitmirror project reanchor <project_id>
//! ```
//!
//! The verbs that mint or sign envelopes (`init`, `invite`,
//! `revoke`, `leave`, `reanchor`) require the user's self-fleet
//! identity to be unlocked — the session helper at
//! `$XDG_RUNTIME_DIR/vigo/self-fleet.sock` is the only place the
//! Ed25519 private key lives; CLI doesn't unwrap directly.
//!
//! Step 3 of the ADR-015 arc lands the local-state surface only.
//! Verbs that depend on the helper-daemon-mediated transport (push,
//! fetch, fan-out, gossip) write a sentinel file the daemon will pick
//! up in step 4 — same pattern cryptsync uses for `leave.pending` /
//! `reencrypt.pending`.

use std::fs;
use std::io::{Read, Write};
use std::path::{Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

use anyhow::{anyhow, bail, Context, Result};
use serde::{Deserialize, Serialize};

use super::project_state::{
    self, hex_id, parse_hex_id, project_repo_path, projects_dir, purge_user_state,
    DrFanout, ProjectConfig, TokenChain,
};
use super::tokens::{
    canonical_bytes, issue_child as token_issue_child, project_id_for, verify_chain,
    Role, Token, TokenBody, ID_LEN, NEVER_EXPIRES, SIG_LEN,
};
use crate::swarm::self_fleet;

/// Top-level dispatch for `vigo swarm gitmirror …`. Called from
/// `agent/src/main.rs`. Argument shape is `[verb, …]`; an empty or
/// help-form `verb` prints usage and returns Ok.
pub fn run(args: Vec<String>) -> Result<()> {
    let verb = args.first().map(String::as_str).unwrap_or("");
    match verb {
        "" | "--help" | "-h" | "help" => {
            print_usage();
            Ok(())
        }
        "project" => dispatch_project(&args[1..]),
        // Hidden verb invoked by the server via TaskDispatch when an
        // envoy newly matches `swarm.gitmirror.blacklist`. Wipes every
        // locally-held project bare repo and the `projects/` directory.
        // Idempotent — re-runs are no-ops.
        "_apply-blacklist" => cmd_apply_blacklist(&args[1..]),
        other => bail!(
            "unknown subcommand `vigo swarm gitmirror {}`. \
             Run `vigo swarm gitmirror --help` for usage.",
            other
        ),
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
        "invite" => cmd_invite(&args[1..]),
        "join" => cmd_join(&args[1..]),
        "leave" => cmd_leave(&args[1..]),
        "list" => cmd_list(&args[1..]),
        "status" => cmd_status(&args[1..]),
        "revoke" => cmd_revoke(&args[1..]),
        "reanchor" => cmd_reanchor(&args[1..]),
        other => bail!(
            "unknown subcommand `vigo swarm gitmirror project {}`. \
             Run `vigo swarm gitmirror project --help` for usage.",
            other
        ),
    }
}

fn print_usage() {
    eprintln!(
        "vigo swarm gitmirror — ADR-015 unified-substrate gitmirror CLI

USAGE:
    vigo swarm gitmirror project <verb> [args]

Run `vigo swarm gitmirror project --help` to see the project verb tree.
"
    );
}

fn print_project_usage() {
    eprintln!(
        "vigo swarm gitmirror project — manage gitmirror projects

USAGE:
    vigo swarm gitmirror project <verb> [args]

VERBS:
    init <handle> [--fanout fleet-wide|members-only]
        Found a new project. Self-fleet must be unlocked. Prints the
        canonical project URL `vigo://<project_id>/<handle>`.

    invite <project_id> --fleet <fp> --role admin|member [--expires <Nd>]
        Mint a child token for another fleet. Prints an invitation
        artifact (JSON) the recipient pipes into `project join`.
        Requires the local fleet to hold an admin token on the project.

    join <project_id> [--invitation <file|->]
        Adopt an invitation issued by an admin. Verifies the chain,
        persists local project state. Self-fleet must be initialized.

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
            home: user.dir,
            is_root: uid.is_root(),
        })
    }
    #[cfg(not(unix))]
    {
        bail!("gitmirror CLI is Unix-only")
    }
}

/// Read the local fleet pubkey as raw 32 bytes. Errors when self-fleet
/// has not been initialized for this user — the verbs that call this
/// can't proceed without an identity.
fn local_fleet_pubkey_bytes(home: &Path) -> Result<[u8; ID_LEN]> {
    let text = self_fleet::local_fleet_pubkey(home)?
        .ok_or_else(|| anyhow!(
            "self-fleet is not initialized for this user. \
             Run `vigo swarm self-fleet init` (or `join --from <host>`) first."
        ))?;
    let hexpart = text
        .strip_prefix("ed25519:")
        .ok_or_else(|| anyhow!("malformed self-fleet pubkey '{}': missing ed25519: prefix", text))?;
    if hexpart.len() != ID_LEN * 2 {
        bail!("self-fleet pubkey is wrong length: {}", hexpart);
    }
    let bytes = hex::decode(hexpart).context("decode self-fleet pubkey hex")?;
    let mut out = [0u8; ID_LEN];
    out.copy_from_slice(&bytes);
    Ok(out)
}

/// Sign `payload` with the user's fleet key via the session helper.
/// Returns the raw 64-byte Ed25519 signature.
///
/// Errors with a clear message when the helper isn't running — the
/// user needs to `self-fleet unlock` first.
fn sign_with_session(payload: &[u8]) -> Result<[u8; SIG_LEN]> {
    let sig_vec = self_fleet::session::request_sign(payload).map_err(|e| {
        anyhow!(
            "could not sign with self-fleet identity: {}\n\
             Run `vigo swarm self-fleet unlock` to start the session helper.",
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

/// Strip an optional `ed25519:` prefix from a fleet pubkey argument
/// and parse it as 64-char lowercase hex. Accepts either
/// `<64 hex chars>` or `ed25519:<64 hex chars>` for ergonomics — the
/// `ed25519:` form is what `self-fleet pubkey` prints.
fn parse_fleet_pubkey_arg(s: &str) -> Result<[u8; ID_LEN]> {
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
#[derive(Debug, Clone, Serialize, Deserialize)]
struct Invitation {
    pub version: u32,
    pub project: ProjectConfig,
    pub chain: TokenChain,
}

const INVITATION_VERSION: u32 = 1;

// ============================================================
// project init
// ============================================================

fn cmd_init(args: &[String]) -> Result<()> {
    let mut handle: Option<String> = None;
    let mut fanout = DrFanout::FleetWide;
    let mut i = 0;
    while i < args.len() {
        match args[i].as_str() {
            "--fanout" => {
                i += 1;
                let v = args.get(i).context("--fanout requires a value")?;
                fanout = v.parse()?;
            }
            other if !other.starts_with('-') && handle.is_none() => {
                handle = Some(other.to_string());
            }
            other => bail!("unknown init flag: {}", other),
        }
        i += 1;
    }
    let handle = handle.context("usage: vigo swarm gitmirror project init <handle> [--fanout …]")?;
    validate_handle(&handle)?;

    let caller = resolve_caller()?;
    let founder_pub = local_fleet_pubkey_bytes(&caller.home)?;
    let project_id = project_id_for(&founder_pub);

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
        subject_fleet_pubkey: founder_pub,
        role: Role::Admin,
        issued_at: now,
        expires_at: None,
        signer_fleet_pubkey: founder_pub,
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
        founder_fleet_pubkey: hex_id(&founder_pub),
        members: vec![hex_id(&founder_pub)],
        dr_fanout: fanout,
        cross_subnet_members: vec![],
        updated_at: now,
    };
    cfg.save(&caller.home)?;
    let chain = TokenChain {
        version: project_state::PROJECT_STATE_VERSION,
        chain: vec![bootstrap],
    };
    chain.save(&caller.home, &project_id)?;

    println!("✓ Founded project {}", handle);
    println!("  URL:        vigo://{}/{}", hex_id(&project_id), handle);
    println!("  Project ID: {}", hex_id(&project_id));
    println!("  Founder:    ed25519:{}", hex_id(&founder_pub));
    println!("  Fan-out:    {}", fanout.as_str());
    println!();
    println!("State written to {}", project_state::user_project_dir(&caller.home, &project_id).display());
    println!();
    println!("Note: the bare repo and helper-daemon-mediated transport land in step 4");
    println!("of the ADR-015 arc; for now this verb only persists the local auth surface.");
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
// project invite
// ============================================================

fn cmd_invite(args: &[String]) -> Result<()> {
    let mut project_arg: Option<String> = None;
    let mut fleet_arg: Option<String> = None;
    let mut role_arg: Option<String> = None;
    let mut expires_days: Option<u64> = None;
    let mut i = 0;
    while i < args.len() {
        match args[i].as_str() {
            "--fleet" => {
                i += 1;
                fleet_arg = Some(args.get(i).context("--fleet requires a value")?.to_string());
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
        "usage: vigo swarm gitmirror project invite <project_id> --fleet <fp> --role admin|member [--expires <Nd>]",
    )?;
    let fleet = parse_fleet_pubkey_arg(
        &fleet_arg.context("--fleet is required")?,
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

    // The leaf's subject must equal our local fleet pubkey — sanity
    // check that the chain on disk really authorizes us.
    let local_pub = local_fleet_pubkey_bytes(&caller.home)?;
    if parent.body.subject_fleet_pubkey != local_pub {
        bail!(
            "leaf token subject does not match local fleet pubkey \
             (chain expects ed25519:{:.16}…, local is ed25519:{:.16}…)",
            hex_id(&parent.body.subject_fleet_pubkey),
            hex_id(&local_pub)
        );
    }

    // Build child body, sign via session helper.
    let now = now_secs();
    let expires_at = expires_days.map(|d| now + (d as i64) * 86400);
    let body = TokenBody {
        project_id,
        subject_fleet_pubkey: fleet,
        role,
        issued_at: now,
        expires_at,
        signer_fleet_pubkey: local_pub,
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
    };

    let json = serde_json::to_string_pretty(&invitation)?;
    println!("{}", json);
    eprintln!();
    eprintln!("Invitation for project '{}'.", cfg.handle);
    eprintln!("Pipe the JSON above to the recipient and have them run:");
    eprintln!("  vigo swarm gitmirror project join {} --invitation -", hex_id(&project_id));
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
        .context("usage: vigo swarm gitmirror project join <project_id> [--invitation <file|->]")?;
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
    let local_pub = local_fleet_pubkey_bytes(&caller.home)?;

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
    // project_id from founder and rejects the chain if it doesn't
    // match — defense against a malicious invitation that swaps the
    // founder pubkey to a key the attacker controls.
    let founder = parse_hex_id(&invitation.project.founder_fleet_pubkey)?;
    verify_chain(&claimed_id, &founder, &invitation.chain.chain, now_secs())
        .map_err(|e| anyhow!("invitation chain failed verification: {}", e))?;

    // Leaf must be granting access to *our* fleet pubkey.
    let leaf = invitation.chain.chain.last().ok_or_else(|| anyhow!("empty chain"))?;
    if leaf.body.subject_fleet_pubkey != local_pub {
        bail!(
            "this invitation grants access to ed25519:{:.16}…, but our local fleet is ed25519:{:.16}…",
            hex_id(&leaf.body.subject_fleet_pubkey),
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

    println!("✓ Joined project {}", invitation.project.handle);
    println!("  URL:        vigo://{}/{}", invitation.project.project_id, invitation.project.handle);
    println!("  Role:       {:?}", leaf.body.role);
    println!("  Members:    {}", invitation.project.members.len());
    println!("  Fan-out:    {}", invitation.project.dr_fanout.as_str());
    println!();
    println!("State written to {}", project_state::user_project_dir(&caller.home, &claimed_id).display());
    println!();
    println!("Note: actual `git fetch` / `git push` against this remote requires the helper");
    println!("daemon, which lands in step 4 of the ADR-015 arc.");
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
        .context("usage: vigo swarm gitmirror project leave <project_id> [--purge] [--yes]")?;

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

    // Sign the LEAVE envelope inline via the user's self-fleet
    // session helper (CLI runs as the user; root-side reactor cannot
    // reach a user's session helper). Sentinel content IS the signed
    // envelope — the helper daemon's reactor moves it to
    // /var/lib/vigo/swarm/gitmirror/envelopes/<pid>/leaves/ verbatim,
    // re-verifying on the way in.
    let leaver_pub = local_fleet_pubkey_bytes(&caller.home)?;
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
        // /var/lib/vigo/swarm/gitmirror/projects/ is 0700 root:root.
        let base = crate::platform::paths::default_gitmirror_dir();
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
            project_repo_path(&crate::platform::paths::default_gitmirror_dir(), &project_id).display()
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
        println!("no gitmirror projects on this envoy (for {})", caller.home.display());
        return Ok(());
    }
    println!(
        "{:<24} {:<14} {:<14} {:<8} {}",
        "HANDLE", "FAN-OUT", "MEMBERS", "TOKENS", "PROJECT_ID"
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
            p.dr_fanout.as_str(),
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
        .context("usage: vigo swarm gitmirror project status <project_id>")?;
    let caller = resolve_caller()?;
    let project_id = resolve_project_id(&caller.home, project_arg)?;
    let cfg = ProjectConfig::load(&caller.home, &project_id)?
        .ok_or_else(|| anyhow!("not a member of project {}", hex_id(&project_id)))?;
    let chain = TokenChain::load(&caller.home, &project_id)?;

    println!("Project:           {}", cfg.handle);
    println!("URL:               vigo://{}/{}", cfg.project_id, cfg.handle);
    println!("Project ID:        {}", cfg.project_id);
    println!("Founder:           ed25519:{}", cfg.founder_fleet_pubkey);
    println!("Fan-out:           {}", cfg.dr_fanout.as_str());
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
            println!("Leaf subject:      ed25519:{}", hex_id(&leaf.body.subject_fleet_pubkey));
            println!("Leaf issued_at:    {}", leaf.body.issued_at);
            match leaf.body.expires_at {
                None => println!("Leaf expires_at:   never"),
                Some(NEVER_EXPIRES) => println!("Leaf expires_at:   never"),
                Some(t) => println!("Leaf expires_at:   {}", t),
            }
        }
    } else {
        println!("Token chain:       (missing — re-run join)");
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
        "usage: vigo swarm gitmirror project revoke <project_id> --token-id <hex> [--reason <text>]",
    )?;
    let token_id_hex = token_id_hex.context("--token-id is required")?;
    let token_id = parse_hex_id(&token_id_hex).context("invalid --token-id hex")?;

    let caller = resolve_caller()?;
    let project_id = resolve_project_id(&caller.home, &project_arg)?;
    let cfg = ProjectConfig::load(&caller.home, &project_id)?
        .ok_or_else(|| anyhow!("not a member of project {}", hex_id(&project_id)))?;

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

    // Sign the REVOKE envelope inline via the user's self-fleet
    // session helper. The leaf admin token's subject_fleet_pubkey IS
    // this user's fleet pubkey (verified above by the role check) —
    // signing with that key produces an envelope the receiving side
    // can authorize against the project's token tree.
    let admin_pub = local_fleet_pubkey_bytes(&caller.home)?;
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
        .context("usage: vigo swarm gitmirror project reanchor <project_id>")?;

    let caller = resolve_caller()?;
    let project_id = resolve_project_id(&caller.home, project_arg)?;
    let cfg = ProjectConfig::load(&caller.home, &project_id)?
        .ok_or_else(|| anyhow!("not a member of project {}", hex_id(&project_id)))?;

    // The new founder is the calling user's self-fleet identity. If
    // that's the same as the existing founder, reanchor is a no-op
    // (and would overwrite local state under the same dir) — refuse.
    let new_founder_pub = local_fleet_pubkey_bytes(&caller.home)?;
    let new_project_id = project_id_for(&new_founder_pub);
    if new_project_id == project_id {
        bail!(
            "this user is already the founder of project '{}' — reanchor is meant for \
             surviving members to take over after the original founder fleet is lost. \
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

  • The current URL `vigo://{}/{}` becomes permanently dead.
  • New URL will be `vigo://{}/{}`.
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
        subject_fleet_pubkey: new_founder_pub,
        role: Role::Admin,
        issued_at: now,
        expires_at: None,
        signer_fleet_pubkey: new_founder_pub,
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
    // fleet — surviving members from the old project must be invited
    // back via fresh `project invite` calls.
    let new_cfg = ProjectConfig {
        version: project_state::PROJECT_STATE_VERSION,
        project_id: hex_id(&new_project_id),
        handle: cfg.handle.clone(),
        founder_fleet_pubkey: hex_id(&new_founder_pub),
        members: vec![hex_id(&new_founder_pub)],
        dr_fanout: cfg.dr_fanout,
        cross_subnet_members: cfg.cross_subnet_members.clone(),
        updated_at: now,
    };
    new_cfg.save(&caller.home)?;
    let new_chain = TokenChain {
        version: project_state::PROJECT_STATE_VERSION,
        chain: vec![bootstrap],
    };
    new_chain.save(&caller.home, &new_project_id)?;

    // Byte migration of the bare repo (root-only). The old repo at
    // /var/lib/vigo/swarm/gitmirror/projects/<old_pid>.git is read
    // into the new repo at projects/<new_pid>.git via
    // `git -C <new> fetch <old> +refs/*:refs/*`. Skip cleanly when the
    // old repo doesn't exist locally (this envoy was a non-fanout
    // member that never received the bytes) or when the caller isn't
    // root (the bare repos are 0700 root:root).
    let base = crate::platform::paths::default_gitmirror_dir();
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
    println!("  Old URL:    vigo://{}/{}", cfg.project_id, cfg.handle);
    println!("  New URL:    vigo://{}/{}", hex_id(&new_project_id), cfg.handle);
    println!("  New founder: ed25519:{}", hex_id(&new_founder_pub));
    if migrated_refs > 0 {
        println!("  Migrated {} ref(s) from old bare repo into new bare repo", migrated_refs);
    } else if caller.is_root && old_repo.exists() {
        println!("  Old bare repo had no refs to migrate (empty repo).");
    }
    println!();
    println!("Next steps:");
    println!("  • Re-invite each surviving member fleet via");
    println!("    `vigo swarm gitmirror project invite {} --fleet <fp> --role <r>`",
             hex_id(&new_project_id));
    println!("  • Each invitee runs `project join {}` and re-adds the remote.",
             hex_id(&new_project_id));
    println!("  • The old project state at {} is left in place — drop it via `project leave {}` once you confirm the new project is healthy.",
             cfg.project_id, cfg.project_id);
    Ok(())
}

// ============================================================
// _apply-blacklist (hidden agent verb)
// ============================================================

fn cmd_apply_blacklist(_args: &[String]) -> Result<()> {
    // Wipe every locally-held bare repo. In step 3 the helper daemon
    // hasn't shipped yet, so projects/<project_id>.git directories
    // typically do not exist — the verb is a no-op in practice but
    // the framework needs to be in place for the server's TaskDispatch
    // path to keep working without errors.
    let base = crate::platform::paths::default_gitmirror_dir();
    let projects = projects_dir(&base);
    if !projects.exists() {
        println!("gitmirror: no projects directory; nothing to evict");
        return Ok(());
    }
    let mut removed = 0usize;
    let mut failed = 0usize;
    let entries = match fs::read_dir(&projects) {
        Ok(e) => e,
        Err(e) => bail!("read {}: {}", projects.display(), e),
    };
    for entry in entries.flatten() {
        let path = entry.path();
        match fs::remove_dir_all(&path) {
            Ok(()) => removed += 1,
            Err(e) => {
                eprintln!("failed to remove {}: {}", path.display(), e);
                failed += 1;
            }
        }
    }
    println!(
        "gitmirror: blacklist eviction complete — {} project(s) removed, {} failed",
        removed, failed
    );
    if failed > 0 {
        std::process::exit(1);
    }
    Ok(())
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
    fn parse_fleet_pubkey_arg_accepts_with_and_without_prefix() {
        let raw = "ab".repeat(32);
        let with_prefix = format!("ed25519:{}", raw);
        assert_eq!(parse_fleet_pubkey_arg(&raw).unwrap(), [0xABu8; 32]);
        assert_eq!(parse_fleet_pubkey_arg(&with_prefix).unwrap(), [0xABu8; 32]);
    }

    #[test]
    fn parse_fleet_pubkey_arg_rejects_short() {
        assert!(parse_fleet_pubkey_arg("deadbeef").is_err());
        assert!(parse_fleet_pubkey_arg("ed25519:deadbeef").is_err());
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
        use crate::swarm::gitmirror::tokens::{issue_bootstrap, issue_child, project_id_for, Role};
        use ed25519_dalek::SigningKey;
        use rand::rngs::OsRng;

        let founder = SigningKey::generate(&mut OsRng);
        let team = SigningKey::generate(&mut OsRng);
        let founder_pub = founder.verifying_key().to_bytes();
        let team_pub = team.verifying_key().to_bytes();
        let pid = project_id_for(&founder_pub);

        let bootstrap = issue_bootstrap(&founder, 100, None).unwrap();
        let member = issue_child(&founder, &bootstrap, &team_pub, Role::Member, 150, None).unwrap();

        let invitation = Invitation {
            version: INVITATION_VERSION,
            project: ProjectConfig {
                version: project_state::PROJECT_STATE_VERSION,
                project_id: hex_id(&pid),
                handle: "vigo-web".to_string(),
                founder_fleet_pubkey: hex_id(&founder_pub),
                members: vec![hex_id(&founder_pub), hex_id(&team_pub)],
                dr_fanout: DrFanout::FleetWide,
                cross_subnet_members: vec![],
                updated_at: 1_744_560_000,
            },
            chain: TokenChain {
                version: project_state::PROJECT_STATE_VERSION,
                chain: vec![bootstrap, member],
            },
        };

        let json = serde_json::to_string(&invitation).unwrap();
        let parsed: Invitation = serde_json::from_str(&json).unwrap();
        assert_eq!(parsed.project.handle, "vigo-web");
        assert_eq!(parsed.chain.chain.len(), 2);
        verify_chain(&pid, &founder_pub, &parsed.chain.chain, 200).unwrap();
    }
}
