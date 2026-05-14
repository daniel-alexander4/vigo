// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! `vigo swarm curator <verb>` — ADR-024 founder + consumer CLI.
//!
//! Slice 4b is the **read** half: `status`, `list`, `inspect`, `versions`,
//! `resolve`, `pull`. Mutating verbs (`push` / `tag` / `untag` /
//! `set-recipients` / `set-target` / `rm` / `prune` / `gc` / `stale` /
//! `purge`) land in slice 4c — they extend the helper-daemon socket
//! protocol and sign through the puddle session helper.
//!
//! Read verbs are unauthenticated against the operator-side `curator: true`
//! usercrate flag: artifacts are fleet-readable (the publish gate is what
//! `curator:` controls; `recipients` enforcement is deferred to the post-v1
//! encrypted-blob feature, per the slice 3b commit). `pull` *materializes*
//! into `/var/lib/vigo/artifacts/`, a root-owned tree, so it bails with a
//! clear pointer when invoked as a non-root user.
//!
//! State sources, in priority order:
//!
//! 1. The local `~<user>/.vigo-curator/<aid>/entry.json` for every Unix user
//!    on this envoy — same shape the `swarm_curator` trait collector reads.
//!    Entries the local user publishes appear here first, before they reach
//!    the server-side fleet catalog.
//! 2. `/var/lib/vigo/artifacts/catalog.json` — the cached fleet catalog the
//!    check-in loop refreshes from the server's `curatormesh` aggregator.
//!    Lets resolve / inspect / versions work through a server outage on any
//!    swarm-enabled envoy.
//! 3. `/var/lib/vigo/artifacts/cache.json` — the materialization index; what
//!    `pull` has already laid down. Read by `status` for the materialized
//!    artifact count.
//!
//! Verbs `resolve` and `pull` go through [`super::resolver::Resolver`], which
//! prefers the server `resolve` endpoint and falls back to the cached
//! catalog on a network error (still verifying the signed entry).

use std::io::{Read, Write};
use std::os::unix::net::UnixStream;
use std::path::{Path, PathBuf};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use anyhow::{anyhow, bail, Context, Result};
use sha2::{Digest, Sha256};

use super::catalog::{
    canonical_bytes, validate_artifact_name, ArtifactVersion, CatalogEntry, CatalogEntryBody,
    Platform, ID_LEN, SIG_LEN,
};
use super::catalog_cache;
use super::helper::socket_path as helper_socket_path;
use super::resolver::Resolver;
use super::state;
use crate::swarm::anchor::envelopes::DELEGATION_MAX_LEN;
use crate::swarm::puddle;
use crate::traits::gitback::passwd_path;

/// Top-level dispatch for `vigo swarm curator …`. Called from `agent/src/main.rs`.
pub fn run(args: Vec<String>) -> Result<()> {
    let verb = args.first().map(String::as_str).unwrap_or("");
    match verb {
        "" | "--help" | "-h" | "help" => {
            print_usage();
            Ok(())
        }
        "status" => cmd_status(&args[1..]),
        "list" => cmd_list(&args[1..]),
        "inspect" => cmd_inspect(&args[1..]),
        "versions" => cmd_versions(&args[1..]),
        "resolve" => cmd_resolve(&args[1..]),
        "pull" => cmd_pull(&args[1..]),
        "push" => with_curator_authz(|| cmd_push(&args[1..])),
        "tag" => with_curator_authz(|| cmd_tag(&args[1..])),
        "untag" => with_curator_authz(|| cmd_untag(&args[1..])),
        "set-recipients" => with_curator_authz(|| cmd_set_recipients(&args[1..])),
        "set-target" => with_curator_authz(|| cmd_set_target(&args[1..])),
        "rm" => with_curator_authz(|| cmd_rm(&args[1..])),
        "stale" => cmd_stale(&args[1..]),
        "prune" => with_curator_authz(|| cmd_prune(&args[1..])),
        "gc" => with_curator_authz(|| cmd_gc(&args[1..])),
        "purge" => with_curator_authz(|| cmd_purge(&args[1..])),
        other => bail!(
            "unknown subcommand `vigo swarm curator {}`. \
             Run `vigo swarm curator --help` for usage.",
            other
        ),
    }
}

/// Refuse early if the invoking user lacks `curator: true` in their usercrate.
/// The daemon enforces the same gate again on the socket — this front-loads the
/// error so the operator sees it before the CLI builds + signs an entry that
/// would just be rejected. Skipped for root (admin running curator verbs
/// against the envoy-wide root-owned state isn't acting as a usercrate-managed
/// user — same carve-out gitback uses).
fn with_curator_authz<F, T>(f: F) -> Result<T>
where
    F: FnOnce() -> Result<T>,
{
    let caller = resolve_caller()?;
    if !caller.is_root {
        crate::swarm::policy::require_curator(&caller.username)?;
    }
    f()
}

fn print_usage() {
    eprintln!(
        "vigo swarm curator — ADR-024 P2P artifact registry CLI

USAGE:
    vigo swarm curator <verb> [args]

READ-ONLY VERBS:
    status                          Envoy-wide curator posture (publish state,
                                    cached fleet catalog, helper-daemon socket,
                                    materialized artifact count).
    list                            One row per artifact the local envoy knows
                                    (locally published + cached from the fleet).
    inspect <artifact_id|name>      Full signed entry for one artifact.
    versions <artifact_id|name>     Version + tag table for one artifact.
    resolve <ref> <tag-or-ver> <os> <arch>
                                    Show the blob coordinates a pull would fetch.
                                    Does not download bytes.
    pull <ref> <tag-or-ver> <os> <arch>
                                    Resolve + fetch + verify + materialize under
                                    /var/lib/vigo/artifacts/<aid>/.../<os>-<arch>.
                                    Requires root (or sudo) — the materialization
                                    tree is root-owned.

MUTATING VERBS (require `curator: true` on your usercrate + an unlocked puddle):
    push <name> --file <path> --version <v> --os <os> --arch <arch>
        [--tag <tag>] [--target <glob>] [--recipients <list>]
                                    Publish a (name, version, os, arch) build.
                                    Adds the platform to the artifact's catalog
                                    entry, signs it, streams the bytes through
                                    the helper daemon, and the daemon stages
                                    the blob + records a manifest seed with
                                    the artifact's target.
    tag <ref> <tag>=<version>       Move/set a tag; re-signs the entry.
    untag <ref> <tag>               Remove a tag; re-signs the entry.
    set-recipients <ref> <list>     Replace `recipients`. Pass `*` or a comma-
                                    separated list of hex puddle pubkeys.
    set-target <ref> <glob>         Replace the DR-replica target glob; the
                                    helper daemon refreshes the manifest seed
                                    for each of this artifact's blobs that we
                                    seed, so filecast redistributes on the
                                    next Manager pass.
    rm <ref> [--version <v>]        Without --version: scrub local publish
                                    state for this artifact (does not unpublish
                                    cluster-wide — peers keep the prior entry).
                                    With --version: re-sign without that
                                    version (and drop any tags that pointed
                                    at it), so consumers stop resolving it.

LOCAL-CLEANUP VERBS:
    stale                           Read-only: list .pending sentinels older
                                    than the reactor-sweep window, dangling
                                    tags, and corrupt local entry files.
                                    Doesn't change anything.
    prune                           Remove the .stale items above (corrupt
                                    entries, .pending older than the sweep
                                    window). Per-user (your local state).
    gc                              Free blob-cache disk: revoke `curator:`
                                    manifest entries WE SEEDED that no current
                                    catalog entry — local or cached fleet —
                                    references anymore. Peers age them out via
                                    the manifest's revocation gossip.
    purge --yes                     Power-user wipe. As a non-root user,
                                    scrubs `~/.vigo-curator/`. As root, also
                                    wipes /var/lib/vigo/artifacts/catalog.json
                                    + cache.json and revokes every `curator:`
                                    blob this envoy seeds. Refuses without
                                    `--yes`.
"
    );
}

// ============================================================
// status
// ============================================================

fn cmd_status(_args: &[String]) -> Result<()> {
    let passwd = std::fs::read_to_string(passwd_path()).unwrap_or_default();
    let local = collect_local_entries(&passwd);
    let cached = catalog_cache::load_catalog();
    let cache_map = catalog_cache::load_cache_map();

    println!("Curator on this envoy");
    println!();
    println!("  Local publish state:");
    println!("    Users with publish state: {}", local.users_with_state);
    println!("    Local catalog entries:    {}", local.entries.len());
    println!("    Pending sentinels:        {}", local.pending);
    println!();
    println!("  Helper daemon:");
    let socket = super::helper::socket_path();
    if socket.exists() {
        println!("    Socket present at {} (publish-ready)", socket.display());
    } else {
        println!(
            "    Socket NOT present at {} (publishing disabled here — \
             swarm.curator.enabled may not match this envoy)",
            socket.display()
        );
    }
    println!();
    println!("  Cached fleet catalog ({}):", catalog_cache::catalog_path_in(&catalog_cache::artifacts_dir()).display());
    println!("    Cached entries: {}", cached.len());
    println!();
    println!("  Materialization cache ({}):", catalog_cache::cache_map_path_in(&catalog_cache::artifacts_dir()).display());
    println!("    Materialized refs: {}", cache_map.entries.len());
    Ok(())
}

// ============================================================
// list
// ============================================================

fn cmd_list(_args: &[String]) -> Result<()> {
    let passwd = std::fs::read_to_string(passwd_path()).unwrap_or_default();
    let local = collect_local_entries(&passwd);
    let cached = catalog_cache::load_catalog();

    // Merge local + cached, dedupe by artifact_id, prefer the higher issued_at.
    let merged = merge_entries(local.entries.clone(), cached);
    if merged.is_empty() {
        println!("No curator artifacts on this envoy (local or cached).");
        return Ok(());
    }
    println!("{:<24} {:<10} {:<10} {:<22} {:<8} ARTIFACT_ID", "NAME", "VERSIONS", "TAGS", "PLATFORMS", "ORIGIN");
    // Index local ones so we can tag the ORIGIN column.
    let local_set: std::collections::HashSet<String> = local
        .entries
        .iter()
        .filter_map(|e| e.artifact_id_hex().ok())
        .collect();
    for e in &merged {
        let aid = e.artifact_id_hex().unwrap_or_else(|_| "?".repeat(12));
        let platforms = platform_summary(e);
        let origin = if local_set.contains(&aid) { "local" } else { "cached" };
        println!(
            "{:<24} {:<10} {:<10} {:<22} {:<8} {}",
            truncate(&e.body.name, 24),
            e.body.versions.len(),
            e.body.tags.len(),
            truncate(&platforms, 22),
            origin,
            short_sha(&aid),
        );
    }
    Ok(())
}

// ============================================================
// inspect
// ============================================================

fn cmd_inspect(args: &[String]) -> Result<()> {
    let target = args
        .first()
        .context("usage: vigo swarm curator inspect <artifact_id|name>")?;
    let entry = resolve_ref(target)?;
    let aid = entry.artifact_id_hex()?;
    println!("Name:           {}", entry.body.name);
    println!("Artifact ID:    {}", aid);
    println!("Founder puddle: ed25519:{}", entry.body.founder_puddle_pubkey);
    if !entry
        .body
        .signing_puddle_pubkey
        .eq_ignore_ascii_case(&entry.body.founder_puddle_pubkey)
    {
        println!(
            "Signing puddle: ed25519:{}  (rekeyed — delegation chain bridges)",
            entry.body.signing_puddle_pubkey
        );
    }
    println!("Target:         {}", entry.body.target);
    println!("Recipients:     {}", entry.body.recipients.join(", "));
    println!("Issued at:      {}", format_millis(entry.body.issued_at));

    // Reverse tag map for the row's TAGS column.
    let mut tags_by_version: std::collections::BTreeMap<String, Vec<String>> = Default::default();
    for (tag, v) in &entry.body.tags {
        tags_by_version.entry(v.clone()).or_default().push(tag.clone());
    }
    for ts in tags_by_version.values_mut() {
        ts.sort();
    }
    if !entry.body.versions.is_empty() {
        println!();
        println!("Versions:");
        println!("  {:<14} {:<14} {:<14} {:<10} SHA", "VERSION", "TAGS", "OS/ARCH", "SIZE");
        for (v, av) in &entry.body.versions {
            let tag_col = tags_by_version
                .get(v)
                .map(|ts| ts.join(","))
                .unwrap_or_else(|| "—".into());
            if av.platforms.is_empty() {
                println!("  {:<14} {:<14} {:<14} {:<10} —", v, tag_col, "—", "—");
                continue;
            }
            for (i, p) in av.platforms.iter().enumerate() {
                let (v_col, t_col) = if i == 0 {
                    (v.clone(), tag_col.clone())
                } else {
                    (String::new(), String::new())
                };
                println!(
                    "  {:<14} {:<14} {}/{:<8} {:<10} {}",
                    v_col,
                    t_col,
                    p.os,
                    p.arch,
                    format_bytes(p.size),
                    short_sha(&p.blob_sha),
                );
            }
        }
    }

    let mut dangling: Vec<String> = Vec::new();
    for (tag, v) in &entry.body.tags {
        if !entry.body.versions.contains_key(v) {
            dangling.push(format!("{tag} → {v}"));
        }
    }
    if !dangling.is_empty() {
        dangling.sort();
        println!();
        println!("Note: {} dangling tag(s): {}", dangling.len(), dangling.join(", "));
    }
    Ok(())
}

// ============================================================
// versions
// ============================================================

fn cmd_versions(args: &[String]) -> Result<()> {
    let target = args
        .first()
        .context("usage: vigo swarm curator versions <artifact_id|name>")?;
    let entry = resolve_ref(target)?;
    if entry.body.versions.is_empty() {
        println!("No versions for {}.", entry.body.name);
        return Ok(());
    }
    println!("Artifact: {}  (id {})", entry.body.name, short_sha(&entry.artifact_id_hex()?));
    println!();
    let mut tags_by_version: std::collections::BTreeMap<String, Vec<String>> = Default::default();
    for (tag, v) in &entry.body.tags {
        tags_by_version.entry(v.clone()).or_default().push(tag.clone());
    }
    for ts in tags_by_version.values_mut() {
        ts.sort();
    }
    println!("{:<14} {:<14} PLATFORMS", "VERSION", "TAGS");
    for (v, av) in &entry.body.versions {
        let tag_col = tags_by_version
            .get(v)
            .map(|ts| ts.join(","))
            .unwrap_or_else(|| "—".into());
        let mut plats: Vec<String> = av.platforms.iter().map(|p| format!("{}/{}", p.os, p.arch)).collect();
        plats.sort();
        println!("{:<14} {:<14} {}", v, tag_col, plats.join(", "));
    }
    Ok(())
}

// ============================================================
// resolve / pull
// ============================================================

fn cmd_resolve(args: &[String]) -> Result<()> {
    let (aref, tag_or_ver, os, arch) = parse_resolve_args(args, "resolve")?;
    let aid = resolve_aid(&aref)?;
    let resolver = build_resolver()?;
    let r = resolver
        .resolve(&aid, &tag_or_ver, &os, &arch)
        .with_context(|| format!("resolve {}@{} on {}/{}", aid, tag_or_ver, os, arch))?;
    println!("Resolved {}/{} @ {} on {}/{}", short_sha(&r.artifact_id_hex), r.name, r.version, r.os, r.arch);
    println!("  blob_sha: {}", r.blob_sha);
    println!("  size:     {}", format_bytes(r.size));
    if let Some(t) = &r.tag {
        println!("  tag:      {}", t);
    }
    Ok(())
}

fn cmd_pull(args: &[String]) -> Result<()> {
    require_root_for("pull")?;
    let (aref, tag_or_ver, os, arch) = parse_resolve_args(args, "pull")?;
    let aid = resolve_aid(&aref)?;
    let resolver = build_resolver()?;
    let dest = resolver
        .pull(&aid, &tag_or_ver, &os, &arch)
        .with_context(|| format!("pull {}@{} on {}/{}", aid, tag_or_ver, os, arch))?;
    println!("Materialized → {}", dest.display());
    Ok(())
}

fn parse_resolve_args(args: &[String], verb: &str) -> Result<(String, String, String, String)> {
    if args.len() != 4 {
        bail!(
            "usage: vigo swarm curator {verb} <artifact_id|name> <tag-or-version> <os> <arch>"
        );
    }
    Ok((args[0].clone(), args[1].clone(), args[2].clone(), args[3].clone()))
}

// ============================================================
// internals
// ============================================================

#[derive(Default)]
struct LocalState {
    entries: Vec<CatalogEntry>,
    pending: usize,
    users_with_state: usize,
}

/// Walk every Unix user's `~/.vigo-curator/` and return the self-verifying
/// entries (by `entry.json`) plus a count of pending sentinels. Same dropping
/// rules as `traits::curator::collect_artifacts` — corrupt / misfiled /
/// non-self-verifying entries are skipped.
fn collect_local_entries(passwd_body: &str) -> LocalState {
    let mut out = LocalState::default();
    let mut by_id: std::collections::BTreeMap<String, CatalogEntry> = Default::default();
    for line in passwd_body.lines() {
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        let fields: Vec<&str> = line.split(':').collect();
        if fields.len() < 7 {
            continue;
        }
        let dir = state::user_curator_dir(Path::new(fields[5]));
        let rd = match std::fs::read_dir(&dir) {
            Ok(r) => r,
            Err(_) => continue,
        };
        let mut found_any = false;
        for ent in rd.flatten() {
            let p = ent.path();
            if !p.is_dir() {
                continue;
            }
            found_any = true;
            let entry_path = p.join(state::ENTRY_FILE);
            let pending_path = p.join(state::ENTRY_PENDING_FILE);
            if pending_path.exists() {
                out.pending += 1;
            }
            let raw = match std::fs::read(&entry_path) {
                Ok(r) => r,
                Err(_) => continue,
            };
            let entry: CatalogEntry = match serde_json::from_slice(&raw) {
                Ok(e) => e,
                Err(_) => continue,
            };
            let aid = match entry.artifact_id_hex() {
                Ok(a) => a,
                Err(_) => continue,
            };
            if p.file_name().and_then(|n| n.to_str()) != Some(aid.as_str()) {
                continue;
            }
            if entry.verify(DELEGATION_MAX_LEN).is_err() {
                continue;
            }
            // Newer entry wins if two users share a puddle key + artifact name.
            match by_id.get(&aid) {
                Some(existing) if existing.body.issued_at >= entry.body.issued_at => {}
                _ => {
                    by_id.insert(aid, entry);
                }
            }
        }
        if found_any {
            out.users_with_state += 1;
        }
    }
    out.entries = by_id.into_values().collect();
    out
}

/// Merge local entries on top of the cached fleet catalog, deduped by
/// artifact_id, keeping the higher `issued_at`.
fn merge_entries(local: Vec<CatalogEntry>, cached: Vec<CatalogEntry>) -> Vec<CatalogEntry> {
    let mut by_id: std::collections::BTreeMap<String, CatalogEntry> = Default::default();
    for e in cached.into_iter().chain(local.into_iter()) {
        let aid = match e.artifact_id_hex() {
            Ok(a) => a,
            Err(_) => continue,
        };
        match by_id.get(&aid) {
            Some(existing) if existing.body.issued_at >= e.body.issued_at => {}
            _ => {
                by_id.insert(aid, e);
            }
        }
    }
    by_id.into_values().collect()
}

/// Resolve a `<artifact_id|name|prefix>` ref to a verified [`CatalogEntry`].
/// Hex artifact_id wins; otherwise an exact name (errors on ambiguity), then
/// a unique prefix. Reads the real passwd + cached catalog; tests drive the
/// `_in` variant below to stay hermetic without poking env vars.
fn resolve_ref(target: &str) -> Result<CatalogEntry> {
    let passwd = std::fs::read_to_string(passwd_path()).unwrap_or_default();
    let cached = catalog_cache::load_catalog();
    resolve_ref_in(target, &passwd, cached)
}

fn resolve_ref_in(target: &str, passwd_body: &str, cached: Vec<CatalogEntry>) -> Result<CatalogEntry> {
    let local = collect_local_entries(passwd_body);
    let pool = merge_entries(local.entries, cached);
    if pool.is_empty() {
        bail!("no curator artifacts known to this envoy (no local entries, empty cached catalog)");
    }
    if is_hex64(target) {
        let want = target.to_ascii_lowercase();
        return pool
            .into_iter()
            .find(|e| e.artifact_id_hex().map(|a| a == want).unwrap_or(false))
            .ok_or_else(|| anyhow!("no artifact matches artifact_id {target}"));
    }
    let exact: Vec<CatalogEntry> = pool.iter().filter(|e| e.body.name == target).cloned().collect();
    if exact.len() == 1 {
        return Ok(exact.into_iter().next().unwrap());
    }
    if exact.len() > 1 {
        let ids: Vec<String> = exact
            .iter()
            .filter_map(|e| e.artifact_id_hex().ok().map(|a| short_sha(&a).to_string()))
            .collect();
        bail!(
            "name {target:?} is published by {} different founders (artifact_ids {}); pass the full artifact_id",
            exact.len(),
            ids.join(", ")
        );
    }
    let prefix: Vec<CatalogEntry> = pool
        .iter()
        .filter(|e| e.body.name.starts_with(target))
        .cloned()
        .collect();
    if prefix.len() == 1 {
        return Ok(prefix.into_iter().next().unwrap());
    }
    if prefix.len() > 1 {
        let names: Vec<String> = prefix.iter().map(|e| e.body.name.clone()).collect();
        bail!("prefix {target:?} is ambiguous; candidates: {}", names.join(", "));
    }
    bail!("no curator artifact matches {target:?}")
}

/// Resolve a ref into a hex artifact_id — same matching rules as
/// [`resolve_ref`] but returns just the id for callers that go on to hit the
/// server's `resolve` endpoint with it.
fn resolve_aid(target: &str) -> Result<String> {
    if is_hex64(target) {
        return Ok(target.to_ascii_lowercase());
    }
    resolve_ref(target)?.artifact_id_hex()
}

/// CLI-side alias for the shared resolver constructor. Slice 6 lifted the
/// implementation into `executor_resolver` so both the CLI and the
/// nonrepo_package / source_package executors share one source of truth.
fn build_resolver() -> Result<Resolver> {
    super::executor_resolver::build_resolver_from_agent_config()
}

fn require_root_for(verb: &str) -> Result<()> {
    #[cfg(unix)]
    {
        use nix::unistd::Uid;
        if !Uid::current().is_root() {
            bail!(
                "`vigo swarm curator {verb}` materializes into /var/lib/vigo/artifacts/ \
                 (root-owned). Re-run as root, e.g. `sudo vigo swarm curator {verb} …`"
            );
        }
    }
    Ok(())
}

// ============================================================
// rendering helpers (small, kept private)
// ============================================================

fn platform_summary(e: &CatalogEntry) -> String {
    use std::collections::BTreeSet;
    let mut seen: BTreeSet<String> = BTreeSet::new();
    for av in e.body.versions.values() {
        for p in &av.platforms {
            seen.insert(format!("{}/{}", p.os, p.arch));
        }
    }
    if seen.is_empty() {
        return "—".into();
    }
    seen.into_iter().collect::<Vec<_>>().join(",")
}

fn short_sha(s: &str) -> String {
    if s.len() <= 12 {
        s.to_string()
    } else {
        s[..12].to_string()
    }
}

fn truncate(s: &str, max: usize) -> String {
    if max <= 1 || s.chars().count() <= max {
        return s.to_string();
    }
    let mut out: String = s.chars().take(max - 1).collect();
    out.push('…');
    out
}

fn format_bytes(n: u64) -> String {
    const UNITS: &[&str] = &["B", "KiB", "MiB", "GiB", "TiB"];
    if n < 1024 {
        return format!("{n} B");
    }
    let mut v = n as f64;
    let mut i = 0;
    while v >= 1024.0 && i + 1 < UNITS.len() {
        v /= 1024.0;
        i += 1;
    }
    format!("{:.1} {}", v, UNITS[i])
}

/// Render unix-milliseconds as `YYYY-MM-DD HH:MM:SS UTC`. Self-contained — the
/// agent doesn't pull in chrono, and a date/time crate just to print a publish
/// timestamp would be inappropriate. Covers 1970..2099 cleanly; anything
/// outside that range falls back to printing the raw millis.
fn format_millis(ms: i64) -> String {
    if ms <= 0 {
        return format!("(ms={ms})");
    }
    let secs = (ms / 1000) as i64;
    let day_secs: i64 = 86_400;
    let mut days = secs / day_secs;
    let mut time_secs = secs % day_secs;
    if time_secs < 0 {
        time_secs += day_secs;
        days -= 1;
    }
    let hours = (time_secs / 3600) as u32;
    let minutes = ((time_secs % 3600) / 60) as u32;
    let seconds = (time_secs % 60) as u32;

    // Days since 1970-01-01 → (year, month, day) using the Howard Hinnant
    // shift-by-March-1 algorithm. Constant-time, no leap-year branch maze.
    let z = days + 719_468;
    let era = if z >= 0 { z / 146_097 } else { (z - 146_096) / 146_097 };
    let doe = (z - era * 146_097) as u64;
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
    let mut year = (yoe as i64) + era * 400;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let day = (doy - (153 * mp + 2) / 5 + 1) as u32;
    let month = if mp < 10 { mp + 3 } else { mp - 9 } as u32;
    if month <= 2 {
        year += 1;
    }
    if !(1970..=2099).contains(&year) {
        return format!("(ms={ms})");
    }
    format!(
        "{year:04}-{month:02}-{day:02} {hours:02}:{minutes:02}:{seconds:02} UTC"
    )
}

fn is_hex64(s: &str) -> bool {
    s.len() == 64 && s.bytes().all(|b| b.is_ascii_hexdigit())
}

// ============================================================
// mutating verbs (slice 4c)
// ============================================================

/// Resolved identity of the invoking Unix user — `getuid()` is the source of
/// truth (publish verbs run as the user, not root, so SO_PEERCRED in the
/// daemon agrees with what we see locally).
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
        bail!("curator CLI is Unix-only")
    }
}

/// Read the caller's local puddle pubkey as raw 32 bytes. Verbs that mint or
/// re-sign catalog entries need it — both as the founder anchor (when creating
/// a new artifact) and to refuse mutating someone else's artifact.
fn local_puddle_pubkey_bytes(home: &Path) -> Result<[u8; ID_LEN]> {
    let text = puddle::local_puddle_pubkey(home)?.ok_or_else(|| {
        anyhow!(
            "puddle is not initialized for this user. \
             Run `vigo swarm puddle init` (or `join --from <host>`) first."
        )
    })?;
    let hexpart = text
        .strip_prefix("ed25519:")
        .ok_or_else(|| anyhow!("malformed puddle pubkey: missing ed25519: prefix"))?;
    if hexpart.len() != ID_LEN * 2 {
        bail!("puddle pubkey is wrong length: {}", hexpart);
    }
    let bytes = hex::decode(hexpart).context("decode puddle pubkey hex")?;
    let mut out = [0u8; ID_LEN];
    out.copy_from_slice(&bytes);
    Ok(out)
}

/// Sign `payload` with the user's puddle key via the session helper. The
/// session helper is the only place the private key lives — the CLI never
/// unwraps it directly. Same shape gitback's `sign_with_session` uses.
fn sign_with_session(payload: &[u8]) -> Result<[u8; SIG_LEN]> {
    let sig = puddle::session::request_sign(payload).map_err(|e| {
        anyhow!(
            "could not sign with puddle identity: {}\n\
             Run `vigo swarm puddle unlock` to start the session helper.",
            e
        )
    })?;
    if sig.len() != SIG_LEN {
        bail!(
            "session helper returned a {}-byte signature, expected {}",
            sig.len(),
            SIG_LEN
        );
    }
    let mut out = [0u8; SIG_LEN];
    out.copy_from_slice(&sig);
    Ok(out)
}

fn now_millis() -> i64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_millis() as i64)
        .unwrap_or(0)
}

/// Load the caller's local entry for `artifact_id` if one exists. Returns
/// `Ok(None)` when the file is absent (= "no prior publish; we're founding").
fn load_local_entry(home: &Path, artifact_id_hex: &str) -> Result<Option<CatalogEntry>> {
    let path = state::entry_path(home, artifact_id_hex);
    let raw = match std::fs::read(&path) {
        Ok(r) => r,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(None),
        Err(e) => return Err(anyhow!("reading {}: {e}", path.display())),
    };
    let entry: CatalogEntry =
        serde_json::from_slice(&raw).with_context(|| format!("parsing {}", path.display()))?;
    Ok(Some(entry))
}

/// Re-sign `body` with the user's puddle key via the session helper. The
/// resulting [`CatalogEntry`] self-verifies before this returns.
fn issue_via_session(body: CatalogEntryBody) -> Result<CatalogEntry> {
    let msg = canonical_bytes(&body).context("canonical_bytes")?;
    let sig = sign_with_session(&msg)?;
    let entry = CatalogEntry {
        body,
        signature: hex::encode(sig),
    };
    entry
        .verify(DELEGATION_MAX_LEN)
        .context("re-signed catalog entry failed self-verification")?;
    Ok(entry)
}

/// Bump `issued_at` so the LWW merge picks our re-sign. Uses Unix-millis now,
/// falling back to `existing + 1` when wall-clock isn't strictly greater.
fn next_issued_at(existing: i64) -> i64 {
    let now = now_millis();
    if now > existing {
        now
    } else {
        existing.saturating_add(1)
    }
}

/// Take a `<ref>` (hex artifact_id, exact name, or unique name prefix) and
/// return `(artifact_id_hex, base_entry)` where `base_entry` is the caller's
/// local entry for that artifact. Errors when the caller has no local state
/// for the resolved artifact (because mutating verbs are about *re-signing
/// our own publication*, not someone else's).
fn resolve_local_ref(home: &Path, target: &str) -> Result<(String, CatalogEntry)> {
    if is_hex64(target) {
        let aid = target.to_ascii_lowercase();
        let e = load_local_entry(home, &aid)?.ok_or_else(|| {
            anyhow!("no local catalog entry for artifact_id {aid} — publish it first with `push`")
        })?;
        return Ok((aid, e));
    }
    // Walk the caller's `~/.vigo-curator/` for entries whose `name` matches.
    let dir = state::user_curator_dir(home);
    let rd = std::fs::read_dir(&dir).ok();
    let mut exact: Vec<(String, CatalogEntry)> = Vec::new();
    let mut prefix: Vec<(String, CatalogEntry)> = Vec::new();
    if let Some(rd) = rd {
        for ent in rd.flatten() {
            let p = ent.path();
            if !p.is_dir() {
                continue;
            }
            let aid = match p.file_name().and_then(|n| n.to_str()) {
                Some(s) => s.to_string(),
                None => continue,
            };
            let e = match load_local_entry(home, &aid) {
                Ok(Some(e)) => e,
                _ => continue,
            };
            if e.body.name == target {
                exact.push((aid, e));
            } else if e.body.name.starts_with(target) {
                prefix.push((aid.clone(), e));
            }
        }
    }
    if exact.len() == 1 {
        return Ok(exact.into_iter().next().unwrap());
    }
    if exact.len() > 1 {
        let ids: Vec<String> = exact.iter().map(|(a, _)| short_sha(a)).collect();
        bail!(
            "name {target:?} matches {} of your local artifacts (artifact_ids {}); pass the full artifact_id",
            exact.len(),
            ids.join(", ")
        );
    }
    if prefix.len() == 1 {
        return Ok(prefix.into_iter().next().unwrap());
    }
    if prefix.len() > 1 {
        let names: Vec<String> = prefix.iter().map(|(_, e)| e.body.name.clone()).collect();
        bail!("prefix {target:?} is ambiguous; candidates: {}", names.join(", "));
    }
    bail!(
        "no local artifact matches {target:?}. If you're trying to mutate someone else's \
         artifact, you can't — mutating verbs only re-sign your own publications. \
         If this is yours but the local state is gone, re-found it with `push --new` \
         (you'd be minting a fresh artifact_id, since the genesis-pubkey anchor is in \
         the hash)."
    )
}

// ----- helper-daemon client ----------------------------------------------

fn helper_socket() -> Result<UnixStream> {
    let path = helper_socket_path();
    if !path.exists() {
        bail!(
            "curator helper socket not present at {}. Is the agent running, \
             and does `swarm.curator.enabled` match this envoy?",
            path.display()
        );
    }
    let s = UnixStream::connect(&path)
        .with_context(|| format!("connecting to {}", path.display()))?;
    s.set_read_timeout(Some(Duration::from_secs(120))).ok();
    s.set_write_timeout(Some(Duration::from_secs(120))).ok();
    Ok(s)
}

/// Send a PUSH: header line + raw blob bytes from `blob_path`. The daemon
/// answers `READY\n` after the header validates, then we stream the file and
/// it answers `OK\n` once the bytes hash to the entry's recorded sha and the
/// manifest seed has been recorded.
fn submit_push(
    entry: &CatalogEntry,
    version: &str,
    os: &str,
    arch: &str,
    blob_path: &Path,
    blob_size: u64,
) -> Result<()> {
    let mut stream = helper_socket()?;
    let header = serde_json::json!({
        "entry": entry,
        "version": version,
        "os": os,
        "arch": arch,
        "blob_size": blob_size,
    });
    let mut buf = b"PUSH\n".to_vec();
    buf.extend_from_slice(serde_json::to_string(&header)?.as_bytes());
    buf.push(b'\n');
    stream.write_all(&buf).context("writing PUSH header")?;
    stream.flush().ok();

    // Expect READY (or an early ERR).
    let line = read_line_from(&mut stream)?;
    if line.trim() != "READY" {
        bail!("daemon did not say READY: {line:?}");
    }

    // Stream the file.
    let mut f = std::fs::File::open(blob_path)
        .with_context(|| format!("opening {}", blob_path.display()))?;
    let mut chunk = vec![0u8; 64 * 1024];
    let mut sent: u64 = 0;
    while sent < blob_size {
        let want = (blob_size - sent).min(chunk.len() as u64) as usize;
        let n = f.read(&mut chunk[..want]).context("reading blob bytes")?;
        if n == 0 {
            bail!(
                "blob file ended after {sent} of {blob_size} bytes — did it change while we were sending?"
            );
        }
        stream.write_all(&chunk[..n]).context("streaming blob")?;
        sent += n as u64;
    }
    stream.flush().ok();

    // Expect OK.
    let line = read_line_from(&mut stream)?;
    let trimmed = line.trim();
    if trimmed == "OK" {
        Ok(())
    } else if let Some(msg) = trimmed.strip_prefix("ERR ") {
        bail!("daemon: {msg}")
    } else {
        bail!("unexpected daemon response: {trimmed:?}")
    }
}

/// Send a header-only re-sign via the ENTRY verb. The daemon validates +
/// drops it as `*.pending`; the reactor promotes (LWW) on its next tick.
fn submit_entry(entry: &CatalogEntry) -> Result<()> {
    let mut stream = helper_socket()?;
    let mut buf = b"ENTRY\n".to_vec();
    buf.extend_from_slice(serde_json::to_string(entry)?.as_bytes());
    buf.push(b'\n');
    stream.write_all(&buf).context("writing ENTRY")?;
    stream.flush().ok();
    let line = read_line_from(&mut stream)?;
    let trimmed = line.trim();
    if trimmed == "OK" {
        Ok(())
    } else if let Some(msg) = trimmed.strip_prefix("ERR ") {
        bail!("daemon: {msg}")
    } else {
        bail!("unexpected daemon response: {trimmed:?}")
    }
}

fn read_line_from(s: &mut UnixStream) -> Result<String> {
    use std::io::BufRead;
    let mut br = std::io::BufReader::new(s);
    let mut line = String::new();
    let n = br.read_line(&mut line).context("reading daemon response")?;
    if n == 0 {
        bail!("daemon closed the connection without responding");
    }
    Ok(line)
}

// ----- verbs -------------------------------------------------------------

fn cmd_push(args: &[String]) -> Result<()> {
    // push <name> --file <path> --version <v> --os <os> --arch <arch>
    //             [--tag <tag>] [--target <glob>] [--recipients <list>]
    let mut name_arg: Option<String> = None;
    let mut file_arg: Option<String> = None;
    let mut version_arg: Option<String> = None;
    let mut os_arg: Option<String> = None;
    let mut arch_arg: Option<String> = None;
    let mut tag_arg: Option<String> = None;
    let mut target_arg: Option<String> = None;
    let mut recipients_arg: Option<String> = None;
    let mut i = 0;
    while i < args.len() {
        match args[i].as_str() {
            "--file" => { i += 1; file_arg = Some(args.get(i).context("--file requires a value")?.clone()); }
            "--version" => { i += 1; version_arg = Some(args.get(i).context("--version requires a value")?.clone()); }
            "--os" => { i += 1; os_arg = Some(args.get(i).context("--os requires a value")?.clone()); }
            "--arch" => { i += 1; arch_arg = Some(args.get(i).context("--arch requires a value")?.clone()); }
            "--tag" => { i += 1; tag_arg = Some(args.get(i).context("--tag requires a value")?.clone()); }
            "--target" => { i += 1; target_arg = Some(args.get(i).context("--target requires a value")?.clone()); }
            "--recipients" => { i += 1; recipients_arg = Some(args.get(i).context("--recipients requires a value")?.clone()); }
            other if !other.starts_with('-') && name_arg.is_none() => name_arg = Some(other.to_string()),
            other => bail!("unknown push flag: {other}"),
        }
        i += 1;
    }
    let name = name_arg.context(
        "usage: vigo swarm curator push <name> --file <path> --version <v> --os <os> --arch <arch> \
         [--tag <tag>] [--target <glob>] [--recipients <list>]",
    )?;
    validate_artifact_name(&name)?;
    let file_path = PathBuf::from(file_arg.context("--file is required")?);
    let version = version_arg.context("--version is required")?;
    let os = os_arg.context("--os is required")?;
    let arch = arch_arg.context("--arch is required")?;

    let caller = resolve_caller()?;
    let founder_pub = local_puddle_pubkey_bytes(&caller.home)?;
    let artifact_id_hex = hex::encode(super::catalog::artifact_id_for(&founder_pub, &name));

    // Hash + size the file we're about to ship.
    let (blob_sha, blob_size) = hash_file(&file_path).with_context(|| {
        format!("hashing artifact file {}", file_path.display())
    })?;

    // Build the new body: start from the existing entry if any, replace this
    // platform's record under the named version, optionally apply tag/target/
    // recipients overrides. A fresh push (no prior entry) needs a target or
    // defaults to "*" (fleet-replicate) and recipients to ["*"] (fleet-read).
    let existing = load_local_entry(&caller.home, &artifact_id_hex)?;
    let mut body = match existing {
        Some(e) => {
            if e.body.name != name {
                // artifact_id matches but the entry's name doesn't — shouldn't
                // happen under normal use (artifact_id is sha256(pubkey ‖ name)),
                // but worth refusing loudly rather than silently rewriting.
                bail!(
                    "existing entry's name {:?} disagrees with this push's name {:?}",
                    e.body.name,
                    name
                );
            }
            e.body
        }
        None => CatalogEntryBody {
            schema_version: 1,
            name: name.clone(),
            founder_puddle_pubkey: hex::encode(founder_pub),
            signing_puddle_pubkey: hex::encode(founder_pub),
            versions: Default::default(),
            tags: Default::default(),
            recipients: vec!["*".into()],
            target: "*".into(),
            issued_at: 0,
            delegations: vec![],
        },
    };

    // Slot the platform in. Replaces any same-{os,arch} entry under this
    // version, so re-pushing a fixed build for the same triplet works.
    let new_platform = Platform {
        os: os.clone(),
        arch: arch.clone(),
        blob_sha: blob_sha.clone(),
        size: blob_size,
    };
    let av = body
        .versions
        .entry(version.clone())
        .or_insert_with(|| ArtifactVersion { platforms: vec![] });
    av.platforms.retain(|p| !(p.os == os && p.arch == arch));
    av.platforms.push(new_platform);

    if let Some(t) = tag_arg {
        body.tags.insert(t, version.clone());
    }
    if let Some(t) = target_arg {
        body.target = t;
    }
    if let Some(rs) = recipients_arg {
        body.recipients = parse_recipients(&rs)?;
    }
    body.issued_at = next_issued_at(body.issued_at);

    // Sign + ship. The CLI prepares everything; the daemon enforces (signature,
    // delegation chain, blob size cap, blob sha match) before it stages.
    let entry = issue_via_session(body)?;
    submit_push(&entry, &version, &os, &arch, &file_path, blob_size)?;

    println!("✓ Published {}@{} on {}/{} ({})", name, version, os, arch, format_bytes(blob_size));
    println!("  artifact_id: {}", artifact_id_hex);
    println!("  blob_sha:    {}", blob_sha);
    Ok(())
}

fn cmd_tag(args: &[String]) -> Result<()> {
    // tag <ref> <tag>=<version>
    if args.len() < 2 {
        bail!("usage: vigo swarm curator tag <artifact_id|name> <tag>=<version>");
    }
    let assignment = &args[1];
    let (tag, version) = assignment
        .split_once('=')
        .ok_or_else(|| anyhow!("tag assignment must be `<tag>=<version>`, got {assignment:?}"))?;
    if tag.is_empty() {
        bail!("tag must be non-empty");
    }
    let caller = resolve_caller()?;
    let (aid, base) = resolve_local_ref(&caller.home, &args[0])?;
    if !base.body.versions.contains_key(version) {
        bail!(
            "version {version:?} isn't in this artifact's catalog entry. Versions: {}",
            base.body
                .versions
                .keys()
                .cloned()
                .collect::<Vec<_>>()
                .join(", ")
        );
    }
    let mut body = base.body.clone();
    body.tags.insert(tag.to_string(), version.to_string());
    body.issued_at = next_issued_at(body.issued_at);
    let entry = issue_via_session(body)?;
    submit_entry(&entry)?;
    println!("✓ Tagged {} {} → {}", short_sha(&aid), tag, version);
    Ok(())
}

fn cmd_untag(args: &[String]) -> Result<()> {
    // untag <ref> <tag>
    if args.len() < 2 {
        bail!("usage: vigo swarm curator untag <artifact_id|name> <tag>");
    }
    let tag = &args[1];
    let caller = resolve_caller()?;
    let (aid, base) = resolve_local_ref(&caller.home, &args[0])?;
    if !base.body.tags.contains_key(tag) {
        bail!("tag {tag:?} is not set on this artifact");
    }
    let mut body = base.body.clone();
    body.tags.remove(tag);
    body.issued_at = next_issued_at(body.issued_at);
    let entry = issue_via_session(body)?;
    submit_entry(&entry)?;
    println!("✓ Removed tag {} from {}", tag, short_sha(&aid));
    Ok(())
}

fn cmd_set_recipients(args: &[String]) -> Result<()> {
    // set-recipients <ref> <list-or-*>
    if args.len() < 2 {
        bail!("usage: vigo swarm curator set-recipients <artifact_id|name> <* | <pubkey>[,<pubkey>...]>");
    }
    let list = parse_recipients(&args[1])?;
    let caller = resolve_caller()?;
    let (aid, base) = resolve_local_ref(&caller.home, &args[0])?;
    let mut body = base.body.clone();
    body.recipients = list.clone();
    body.issued_at = next_issued_at(body.issued_at);
    let entry = issue_via_session(body)?;
    submit_entry(&entry)?;
    println!("✓ Set recipients on {} to {}", short_sha(&aid), list.join(", "));
    Ok(())
}

fn cmd_set_target(args: &[String]) -> Result<()> {
    // set-target <ref> <glob>
    if args.len() < 2 {
        bail!("usage: vigo swarm curator set-target <artifact_id|name> <hostname-glob-or-trait-filter>");
    }
    let target = args[1].clone();
    if target.is_empty() {
        bail!("target glob must be non-empty");
    }
    let caller = resolve_caller()?;
    let (aid, base) = resolve_local_ref(&caller.home, &args[0])?;
    let mut body = base.body.clone();
    body.target = target.clone();
    body.issued_at = next_issued_at(body.issued_at);
    let entry = issue_via_session(body)?;
    submit_entry(&entry)?;
    println!("✓ Set target on {} to {}", short_sha(&aid), target);
    println!(
        "  Manifest seeds refreshed; filecast Manager will redistribute on the next pass."
    );
    Ok(())
}

fn cmd_rm(args: &[String]) -> Result<()> {
    // rm <ref> [--version <v>]
    let mut ref_arg: Option<String> = None;
    let mut version_arg: Option<String> = None;
    let mut i = 0;
    while i < args.len() {
        match args[i].as_str() {
            "--version" => { i += 1; version_arg = Some(args.get(i).context("--version requires a value")?.clone()); }
            other if !other.starts_with('-') && ref_arg.is_none() => ref_arg = Some(other.to_string()),
            other => bail!("unknown rm flag: {other}"),
        }
        i += 1;
    }
    let target = ref_arg.context("usage: vigo swarm curator rm <artifact_id|name> [--version <v>]")?;
    let caller = resolve_caller()?;
    let (aid, base) = resolve_local_ref(&caller.home, &target)?;

    if let Some(v) = version_arg {
        if !base.body.versions.contains_key(&v) {
            bail!("version {v:?} isn't in this artifact's catalog entry");
        }
        let mut body = base.body.clone();
        body.versions.remove(&v);
        // Drop any tags pointing at the removed version — leaving a dangling
        // tag would resolve to nothing on consumers anyway.
        body.tags.retain(|_, ver| ver != &v);
        body.issued_at = next_issued_at(body.issued_at);
        let entry = issue_via_session(body)?;
        submit_entry(&entry)?;
        println!("✓ Dropped version {} from {}", v, short_sha(&aid));
        println!(
            "  Note: the blob still sits in the swarm cache until retention/GC expires it (slice 4d)."
        );
        return Ok(());
    }

    // No --version: scrub local publish state for this artifact. Consumers
    // keep their last-known entry (LWW; we have no entry to win with). This
    // matches the ADR's "you can't recall by losing a flag — same as gitback"
    // stance.
    let dir = state::artifact_dir(&caller.home, &aid);
    std::fs::remove_dir_all(&dir).with_context(|| format!("removing {}", dir.display()))?;
    println!("✓ Scrubbed local publish state for {}", short_sha(&aid));
    println!("  ({}.)", dir.display());
    println!("  Note: this does NOT unpublish across the fleet — peers retain the last entry");
    println!("        they saw. To stop a specific version from resolving, use");
    println!("        `vigo swarm curator rm <ref> --version <v>` before this.");
    Ok(())
}

// ----- mutating-verb helpers ---------------------------------------------

/// Hash a file once to compute (sha256-hex, byte-count). Streams 64 KiB at a
/// time so a 2 GiB image doesn't blow the heap.
fn hash_file(path: &Path) -> Result<(String, u64)> {
    let mut f = std::fs::File::open(path).with_context(|| format!("opening {}", path.display()))?;
    let mut hasher = Sha256::new();
    let mut chunk = vec![0u8; 64 * 1024];
    let mut total: u64 = 0;
    loop {
        let n = f.read(&mut chunk).context("reading file for hashing")?;
        if n == 0 {
            break;
        }
        hasher.update(&chunk[..n]);
        total += n as u64;
    }
    Ok((hex::encode(hasher.finalize()), total))
}

/// Parse a `--recipients` argument: `*` ⇒ fleet-readable, or a comma-separated
/// list of hex puddle pubkeys (with optional `ed25519:` prefix). Validates
/// each pubkey is 32 bytes of hex so a typo can't sneak in.
fn parse_recipients(arg: &str) -> Result<Vec<String>> {
    let arg = arg.trim();
    if arg == "*" {
        return Ok(vec!["*".into()]);
    }
    let mut out = Vec::new();
    for item in arg.split(',') {
        let item = item.trim();
        if item.is_empty() {
            continue;
        }
        if item == "*" {
            bail!("`*` in a recipient list is redundant — pass either `*` alone or a list of pubkeys");
        }
        let hex_part = item.strip_prefix("ed25519:").unwrap_or(item);
        if hex_part.len() != ID_LEN * 2 {
            bail!("recipient pubkey {item:?} is wrong length (need 64 hex characters)");
        }
        if hex::decode(hex_part).is_err() {
            bail!("recipient pubkey {item:?} isn't valid hex");
        }
        out.push(hex_part.to_ascii_lowercase());
    }
    if out.is_empty() {
        bail!("recipients list must be `*` or at least one pubkey");
    }
    Ok(out)
}

// ----- cleanup verbs (slice 4d) ------------------------------------------

/// How long a `*.pending` sentinel can sit before we treat it as orphaned.
/// The reactor sweeps once a minute; allow 3 sweeps before flagging.
const PENDING_STALE_SECS: u64 = 180;

#[derive(Default, Debug)]
struct StaleReport {
    pending_too_old: Vec<(PathBuf, u64)>,        // (path, age_seconds)
    corrupt_entries: Vec<(PathBuf, String)>,     // (path, reason)
    dangling_tags: Vec<(String, String, String)>, // (artifact_name, tag, missing_version)
    orphan_blobs: Vec<(String, String, u64)>,    // (label, sha, size_bytes) — slice "would gc"
}

fn cmd_stale(_args: &[String]) -> Result<()> {
    let caller = resolve_caller()?;
    let report = collect_stale(&caller.home, SystemTime::now())?;
    print_stale_report(&report);
    if report_is_empty(&report) {
        println!("Curator state is clean — nothing to prune.");
    }
    Ok(())
}

fn cmd_prune(_args: &[String]) -> Result<()> {
    let caller = resolve_caller()?;
    let report = collect_stale(&caller.home, SystemTime::now())?;
    let mut pending_removed = 0usize;
    for (p, _) in &report.pending_too_old {
        if std::fs::remove_file(p).is_ok() {
            pending_removed += 1;
        }
    }
    let mut corrupt_removed = 0usize;
    for (p, _) in &report.corrupt_entries {
        // `p` is the entry.json (or .pending) path; remove the file, leaving
        // any sibling pending/canonical alone (different failure modes get
        // pruned independently).
        if std::fs::remove_file(p).is_ok() {
            corrupt_removed += 1;
        }
    }
    println!(
        "Pruned {pending_removed} orphan .pending sentinel(s) and {corrupt_removed} corrupt entry file(s)."
    );
    if !report.dangling_tags.is_empty() {
        println!(
            "Note: {} dangling tag(s) require a re-sign (use `vigo swarm curator untag …`):",
            report.dangling_tags.len()
        );
        for (name, tag, ver) in &report.dangling_tags {
            println!("  {name}: {tag} → {ver}");
        }
    }
    Ok(())
}

fn cmd_gc(_args: &[String]) -> Result<()> {
    let caller = resolve_caller()?;
    if !caller.is_root {
        bail!(
            "`vigo swarm curator gc` revokes manifest entries this envoy seeds; \
             must run as root. Re-run with sudo."
        );
    }
    let manifest = crate::swarm::manifest::Manifest::load(&blobs_dir_parent());
    let live_shas = build_live_shas(&caller.home)?;
    let envoy_id = current_envoy_id().unwrap_or_default();
    if envoy_id.is_empty() {
        bail!(
            "this envoy isn't enrolled (no envoy id in /var/lib/vigo/state) — \
             can't authorize manifest revocations"
        );
    }
    let mut revoked = 0usize;
    let mut bytes_freed: u64 = 0;
    for entry in manifest.snapshot().entries.values() {
        if !entry.label.starts_with("curator:") {
            continue;
        }
        if entry.seeder_id != envoy_id {
            continue; // only revoke what we seeded
        }
        let sha = entry.sha256.to_ascii_lowercase();
        if live_shas.contains(&sha) {
            continue;
        }
        if entry.status == crate::swarm::manifest::BlobStatus::Revoked {
            continue;
        }
        if manifest.revoke(&entry.sha256, &envoy_id) {
            revoked += 1;
            bytes_freed = bytes_freed.saturating_add(entry.size);
        }
    }
    println!(
        "Revoked {revoked} curator blob(s) we seeded that no current catalog entry references ({}).",
        format_bytes(bytes_freed)
    );
    println!(
        "Peers will evict their copies via the manifest's revocation gossip; local cache reclaim happens \
         on the next retention prune cycle."
    );
    Ok(())
}

fn cmd_purge(args: &[String]) -> Result<()> {
    if !args.iter().any(|a| a == "--yes") {
        bail!(
            "`vigo swarm curator purge` wipes all local curator state. \
             Pass `--yes` to confirm."
        );
    }
    let caller = resolve_caller()?;
    // User scope: scrub ~/.vigo-curator/ for the calling user. Always done.
    let user_dir = state::user_curator_dir(&caller.home);
    let user_scrubbed = if user_dir.exists() {
        std::fs::remove_dir_all(&user_dir).with_context(|| format!("removing {}", user_dir.display()))?;
        true
    } else {
        false
    };
    if user_scrubbed {
        println!("✓ Scrubbed {}", user_dir.display());
    } else {
        println!("  ({} was already absent)", user_dir.display());
    }
    if !caller.is_root {
        println!(
            "Note: re-run as root to also wipe /var/lib/vigo/artifacts/ and revoke this envoy's seeded \
             curator blobs in the manifest."
        );
        return Ok(());
    }
    // Root scope: wipe the agent-side cache + revoke our seeded blobs.
    let arts = catalog_cache::artifacts_dir();
    for f in ["catalog.json", "cache.json"] {
        let p = arts.join(f);
        if p.exists() {
            std::fs::remove_file(&p).with_context(|| format!("removing {}", p.display()))?;
            println!("✓ Removed {}", p.display());
        }
    }
    let envoy_id = current_envoy_id().unwrap_or_default();
    if !envoy_id.is_empty() {
        let manifest = crate::swarm::manifest::Manifest::load(&blobs_dir_parent());
        let mut revoked = 0usize;
        for entry in manifest.snapshot().entries.values() {
            if !entry.label.starts_with("curator:") {
                continue;
            }
            if entry.seeder_id != envoy_id {
                continue;
            }
            if entry.status == crate::swarm::manifest::BlobStatus::Revoked {
                continue;
            }
            if manifest.revoke(&entry.sha256, &envoy_id) {
                revoked += 1;
            }
        }
        println!("✓ Revoked {revoked} curator blob(s) this envoy seeded.");
    }
    Ok(())
}

// ----- cleanup-verb helpers ----------------------------------------------

fn report_is_empty(r: &StaleReport) -> bool {
    r.pending_too_old.is_empty()
        && r.corrupt_entries.is_empty()
        && r.dangling_tags.is_empty()
        && r.orphan_blobs.is_empty()
}

fn print_stale_report(r: &StaleReport) {
    if !r.pending_too_old.is_empty() {
        println!(
            "Orphan .pending sentinels (older than {PENDING_STALE_SECS}s — reactor should have promoted them):"
        );
        for (p, age) in &r.pending_too_old {
            println!("  {p} (age {age}s)", p = p.display());
        }
        println!();
    }
    if !r.corrupt_entries.is_empty() {
        println!("Corrupt or unverifiable local entries:");
        for (p, reason) in &r.corrupt_entries {
            println!("  {p}: {reason}", p = p.display());
        }
        println!();
    }
    if !r.dangling_tags.is_empty() {
        println!("Dangling tags (point at a version that doesn't exist):");
        for (name, tag, ver) in &r.dangling_tags {
            println!("  {name}: {tag} → {ver}");
        }
        println!();
    }
}

/// Walk `~<caller>/.vigo-curator/` and identify everything `prune` would
/// remove plus what `gc`/`stale` would flag. Pure read. `now` is the clock
/// reference used to age `.pending` sentinels — production callers pass
/// `SystemTime::now()`; tests pass a known instant.
fn collect_stale(home: &Path, now: SystemTime) -> Result<StaleReport> {
    let mut out = StaleReport::default();
    let curator_dir = state::user_curator_dir(home);
    let rd = match std::fs::read_dir(&curator_dir) {
        Ok(rd) => rd,
        Err(_) => return Ok(out),
    };
    for ent in rd.flatten() {
        let p = ent.path();
        if !p.is_dir() {
            continue;
        }
        let dir_name = match p.file_name().and_then(|n| n.to_str()) {
            Some(s) => s.to_string(),
            None => continue,
        };
        // .pending — age it.
        let pending = p.join(state::ENTRY_PENDING_FILE);
        if let Ok(meta) = std::fs::metadata(&pending) {
            if let Ok(mtime) = meta.modified() {
                if let Ok(age) = now.duration_since(mtime) {
                    if age.as_secs() > PENDING_STALE_SECS {
                        out.pending_too_old.push((pending.clone(), age.as_secs()));
                    }
                }
            }
        }
        // entry.json — must self-verify + match its dir name.
        let entry_path = p.join(state::ENTRY_FILE);
        let raw = match std::fs::read(&entry_path) {
            Ok(r) => r,
            Err(_) => continue,
        };
        let entry: CatalogEntry = match serde_json::from_slice(&raw) {
            Ok(e) => e,
            Err(_) => {
                out.corrupt_entries
                    .push((entry_path.clone(), "not valid JSON".into()));
                continue;
            }
        };
        match entry.artifact_id_hex() {
            Ok(aid) if aid == dir_name => {}
            Ok(aid) => {
                out.corrupt_entries.push((
                    entry_path.clone(),
                    format!("filed under {dir_name} but its artifact_id is {aid}"),
                ));
                continue;
            }
            Err(e) => {
                out.corrupt_entries
                    .push((entry_path.clone(), format!("artifact_id: {e:#}")));
                continue;
            }
        };
        if let Err(e) = entry.verify(DELEGATION_MAX_LEN) {
            out.corrupt_entries
                .push((entry_path.clone(), format!("verification: {e:#}")));
            continue;
        }
        // Dangling tags.
        for (tag, v) in &entry.body.tags {
            if !entry.body.versions.contains_key(v) {
                out.dangling_tags
                    .push((entry.body.name.clone(), tag.clone(), v.clone()));
            }
        }
    }
    Ok(out)
}

/// Build the set of blob shas referenced by ANY currently-known catalog entry
/// (the caller's local entries + the cached fleet catalog). `gc` revokes blobs
/// outside this set.
fn build_live_shas(home: &Path) -> Result<std::collections::HashSet<String>> {
    let passwd = std::fs::read_to_string(passwd_path()).unwrap_or_default();
    let local = collect_local_entries(&passwd);
    let cached = catalog_cache::load_catalog();
    let pool = merge_entries(local.entries, cached);
    // Include this caller's home explicitly in case it isn't in passwd.
    let mut local2: Vec<CatalogEntry> = Vec::new();
    if let Ok(rd) = std::fs::read_dir(state::user_curator_dir(home)) {
        for ent in rd.flatten() {
            let p = ent.path();
            if !p.is_dir() {
                continue;
            }
            if let Ok(raw) = std::fs::read(p.join(state::ENTRY_FILE)) {
                if let Ok(e) = serde_json::from_slice::<CatalogEntry>(&raw) {
                    local2.push(e);
                }
            }
        }
    }
    let merged = merge_entries(local2, pool);
    let mut out = std::collections::HashSet::new();
    for e in &merged {
        for av in e.body.versions.values() {
            for p in &av.platforms {
                out.insert(p.blob_sha.to_ascii_lowercase());
            }
        }
    }
    Ok(out)
}

/// `/var/lib/vigo/swarm/` — the directory the swarm manifest persists under.
/// `Manifest::load(dir)` reads `dir/manifest.json`.
fn blobs_dir_parent() -> PathBuf {
    crate::platform::paths::default_blobs_dir()
        .parent()
        .map(PathBuf::from)
        .unwrap_or_else(|| PathBuf::from("/var/lib/vigo/swarm"))
}

/// This envoy's enrollment id, the `seeder_id` `serve_push` records on every
/// curator blob. Same `state/envoy.id` file the rest of the agent reads.
fn current_envoy_id() -> Option<String> {
    let path = crate::platform::paths::default_state_dir().join("envoy.id");
    std::fs::read_to_string(&path).ok().map(|s| s.trim().to_string())
}

// path helpers we re-export so the agent can mark these dirs read-from
#[allow(dead_code)]
fn artifacts_dir_for_tests() -> PathBuf {
    catalog_cache::artifacts_dir()
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::swarm::curator::catalog::{ArtifactVersion, CatalogEntryBody, Platform};
    use ed25519_dalek::SigningKey;
    use rand::rngs::OsRng;
    use std::collections::BTreeMap;
    use tempfile::TempDir;

    fn pub_hex(k: &SigningKey) -> String {
        hex::encode(k.verifying_key().to_bytes())
    }

    fn entry_for(k: &SigningKey, name: &str, issued_at: i64) -> CatalogEntry {
        let mut versions = BTreeMap::new();
        versions.insert(
            "1.0.0".to_string(),
            ArtifactVersion {
                platforms: vec![Platform {
                    os: "linux".into(),
                    arch: "amd64".into(),
                    blob_sha: "a".repeat(64),
                    size: 1024,
                }],
            },
        );
        versions.insert(
            "2.0.0".to_string(),
            ArtifactVersion {
                platforms: vec![Platform {
                    os: "linux".into(),
                    arch: "amd64".into(),
                    blob_sha: "b".repeat(64),
                    size: 2048,
                }],
            },
        );
        let mut tags = BTreeMap::new();
        tags.insert("latest".to_string(), "2.0.0".to_string());
        let body = CatalogEntryBody {
            schema_version: 1,
            name: name.into(),
            founder_puddle_pubkey: pub_hex(k),
            signing_puddle_pubkey: pub_hex(k),
            versions,
            tags,
            recipients: vec!["*".into()],
            target: "*".into(),
            issued_at,
            delegations: vec![],
        };
        CatalogEntry::issue(k, body).unwrap()
    }

    fn write_local(home: &Path, aid: &str, entry: &CatalogEntry) {
        let dir = state::artifact_dir(home, aid);
        std::fs::create_dir_all(&dir).unwrap();
        std::fs::write(
            dir.join(state::ENTRY_FILE),
            serde_json::to_vec(entry).unwrap(),
        )
        .unwrap();
    }

    fn passwd_line(user: &str, uid: u32, home: &Path) -> String {
        format!("{user}:x:{uid}:{uid}::{}:/bin/sh\n", home.display())
    }

    #[test]
    fn collect_local_entries_picks_up_valid_only() {
        let home = TempDir::new().unwrap();
        let k = SigningKey::generate(&mut OsRng);

        // 1. Valid + self-verifying.
        let good = entry_for(&k, "vigo-agent", 100);
        write_local(home.path(), &good.artifact_id_hex().unwrap(), &good);

        // 2. Valid but filed under the wrong artifact_id directory (misfiled).
        let other = entry_for(&k, "elsewhere", 100);
        write_local(home.path(), &"b".repeat(64), &other);

        // 3. Corrupt JSON.
        let bad_dir = state::artifact_dir(home.path(), &"c".repeat(64));
        std::fs::create_dir_all(&bad_dir).unwrap();
        std::fs::write(bad_dir.join(state::ENTRY_FILE), b"not json").unwrap();

        // 4. A pending sentinel (counts toward `pending`, not `entries`).
        let pending_dir = state::artifact_dir(home.path(), &"d".repeat(64));
        std::fs::create_dir_all(&pending_dir).unwrap();
        std::fs::write(pending_dir.join(state::ENTRY_PENDING_FILE), b"{}").unwrap();

        let s = collect_local_entries(&passwd_line("dan", 1000, home.path()));
        assert_eq!(s.entries.len(), 1, "only the well-formed entry counts");
        assert_eq!(s.entries[0].body.name, "vigo-agent");
        assert_eq!(s.pending, 1);
        assert_eq!(s.users_with_state, 1);
    }

    #[test]
    fn merge_entries_dedupes_by_artifact_id_keeping_higher_issued_at() {
        let k = SigningKey::generate(&mut OsRng);
        let older = entry_for(&k, "shared", 100);
        let newer = entry_for(&k, "shared", 200);

        // Cached has the older; local has the newer ⇒ local wins.
        let merged = merge_entries(vec![newer.clone()], vec![older.clone()]);
        assert_eq!(merged.len(), 1);
        assert_eq!(merged[0].body.issued_at, 200);

        // Reverse: cached has the newer; local has the older ⇒ cached wins.
        let merged = merge_entries(vec![older.clone()], vec![newer.clone()]);
        assert_eq!(merged.len(), 1);
        assert_eq!(merged[0].body.issued_at, 200);
    }

    #[test]
    fn resolve_ref_via_hex_and_via_name_and_via_prefix() {
        let home = TempDir::new().unwrap();
        let k = SigningKey::generate(&mut OsRng);
        let entry = entry_for(&k, "vigo-agent", 100);
        let aid = entry.artifact_id_hex().unwrap();
        write_local(home.path(), &aid, &entry);
        let passwd = passwd_line("dan", 1000, home.path());

        // Hex match.
        let r = resolve_ref_in(&aid, &passwd, vec![]).unwrap();
        assert_eq!(r.body.name, "vigo-agent");
        // Exact name match.
        let r = resolve_ref_in("vigo-agent", &passwd, vec![]).unwrap();
        assert_eq!(r.artifact_id_hex().unwrap(), aid);
        // Unique prefix match.
        let r = resolve_ref_in("vigo-ag", &passwd, vec![]).unwrap();
        assert_eq!(r.artifact_id_hex().unwrap(), aid);
        // No match.
        assert!(resolve_ref_in("nope", &passwd, vec![]).is_err());
        // Hex artifact_id that doesn't exist.
        assert!(resolve_ref_in(&"f".repeat(64), &passwd, vec![]).is_err());
    }

    #[test]
    fn resolve_ref_ambiguous_name_errors() {
        let home = TempDir::new().unwrap();
        let k1 = SigningKey::generate(&mut OsRng);
        let k2 = SigningKey::generate(&mut OsRng);
        let e1 = entry_for(&k1, "redis", 1);
        let e2 = entry_for(&k2, "redis", 1);
        write_local(home.path(), &e1.artifact_id_hex().unwrap(), &e1);
        write_local(home.path(), &e2.artifact_id_hex().unwrap(), &e2);
        let passwd = passwd_line("dan", 1000, home.path());

        let err = resolve_ref_in("redis", &passwd, vec![]).unwrap_err();
        assert!(format!("{err:#}").contains("different founders"), "got: {err:#}");
    }

    #[test]
    fn resolve_ref_falls_back_to_cached_catalog() {
        // No local state at all — passwd points at an empty home — but a
        // cached fleet catalog has the entry. Read paths must still work.
        let home = TempDir::new().unwrap();
        let k = SigningKey::generate(&mut OsRng);
        let entry = entry_for(&k, "ops-tool", 100);
        let passwd = passwd_line("dan", 1000, home.path());

        let r = resolve_ref_in("ops-tool", &passwd, vec![entry.clone()]).unwrap();
        assert_eq!(r.body.name, "ops-tool");
        // Empty passwd + empty cache ⇒ explicit error.
        assert!(resolve_ref_in("anything", "", vec![]).is_err());
    }

    #[test]
    fn truncate_handles_unicode() {
        assert_eq!(truncate("abc", 10), "abc");
        assert_eq!(truncate("abcdef", 4), "abc…");
        // Single-char-cap is a no-op (degenerate input).
        assert_eq!(truncate("abc", 1), "abc");
    }

    #[test]
    fn format_bytes_scales() {
        assert_eq!(format_bytes(0), "0 B");
        assert_eq!(format_bytes(1023), "1023 B");
        assert_eq!(format_bytes(1024), "1.0 KiB");
        assert_eq!(format_bytes(1024 * 1024), "1.0 MiB");
    }

    #[test]
    fn is_hex64_rules() {
        assert!(is_hex64(&"a".repeat(64)));
        assert!(is_hex64(&"A".repeat(64)));
        assert!(!is_hex64(&"a".repeat(63)));
        assert!(!is_hex64(&"a".repeat(65)));
        assert!(!is_hex64(&format!("g{}", "a".repeat(63))));
    }

    // ----- slice 4c helpers -----

    #[test]
    fn parse_recipients_accepts_star_or_pubkey_list() {
        assert_eq!(parse_recipients("*").unwrap(), vec!["*".to_string()]);
        let pk1 = "a".repeat(64);
        let pk2 = format!("ed25519:{}", "b".repeat(64));
        let got = parse_recipients(&format!("{pk1},{pk2}")).unwrap();
        assert_eq!(got, vec!["a".repeat(64), "b".repeat(64)]);
        // Whitespace tolerated; case normalized.
        let got = parse_recipients(&format!(" {pk1} , {pk2} ")).unwrap();
        assert_eq!(got.len(), 2);
        // Errors.
        assert!(parse_recipients("").is_err());
        assert!(parse_recipients("not-hex").is_err());
        assert!(parse_recipients(&"a".repeat(63)).is_err()); // wrong length
        assert!(parse_recipients(&format!("*,{pk1}")).is_err(), "* alongside an explicit list is rejected");
    }

    #[test]
    fn hash_file_streams_and_returns_sha_and_size() {
        let tmp = TempDir::new().unwrap();
        let payload = b"the artifact bytes".to_vec();
        let p = tmp.path().join("blob");
        std::fs::write(&p, &payload).unwrap();
        let (sha, size) = hash_file(&p).unwrap();
        assert_eq!(size, payload.len() as u64);
        let want = hex::encode(Sha256::digest(&payload));
        assert_eq!(sha, want);
    }

    #[test]
    fn next_issued_at_is_strictly_monotonic() {
        let now = now_millis();
        // If the existing is in the future, we still strictly advance.
        let future = now + 1_000_000;
        assert_eq!(next_issued_at(future), future + 1);
        // If wall-clock is ahead, we use wall-clock.
        let old = now - 10_000;
        assert!(next_issued_at(old) > old);
    }

    fn write_canonical_entry(home: &Path, aid: &str, entry: &CatalogEntry) {
        // Promote it past .pending so resolve_local_ref sees it.
        let dir = state::artifact_dir(home, aid);
        std::fs::create_dir_all(&dir).unwrap();
        std::fs::write(state::entry_path(home, aid), serde_json::to_vec(entry).unwrap()).unwrap();
    }

    #[test]
    fn resolve_local_ref_matches_hex_name_and_prefix() {
        let home = TempDir::new().unwrap();
        let k = SigningKey::generate(&mut OsRng);
        let entry = entry_for(&k, "vigo-agent", 100);
        let aid = entry.artifact_id_hex().unwrap();
        write_canonical_entry(home.path(), &aid, &entry);

        // Hex.
        let (got_aid, got_e) = resolve_local_ref(home.path(), &aid).unwrap();
        assert_eq!(got_aid, aid);
        assert_eq!(got_e.body.name, "vigo-agent");
        // Exact name.
        let (got_aid, _) = resolve_local_ref(home.path(), "vigo-agent").unwrap();
        assert_eq!(got_aid, aid);
        // Unique prefix.
        let (got_aid, _) = resolve_local_ref(home.path(), "vigo-a").unwrap();
        assert_eq!(got_aid, aid);
        // No match — clear error pointing at `push`.
        let err = resolve_local_ref(home.path(), "nope").unwrap_err();
        assert!(
            format!("{err:#}").contains("no local artifact"),
            "got: {err:#}"
        );
    }

    #[test]
    fn resolve_local_ref_ambiguous_name_errors() {
        let home = TempDir::new().unwrap();
        let k1 = SigningKey::generate(&mut OsRng);
        let k2 = SigningKey::generate(&mut OsRng);
        let e1 = entry_for(&k1, "redis", 1);
        let e2 = entry_for(&k2, "redis", 1);
        let aid1 = e1.artifact_id_hex().unwrap();
        let aid2 = e2.artifact_id_hex().unwrap();
        write_canonical_entry(home.path(), &aid1, &e1);
        write_canonical_entry(home.path(), &aid2, &e2);
        let err = resolve_local_ref(home.path(), "redis").unwrap_err();
        assert!(format!("{err:#}").contains("matches 2"), "got: {err:#}");
    }

    #[test]
    fn load_local_entry_handles_missing_and_present() {
        let home = TempDir::new().unwrap();
        let k = SigningKey::generate(&mut OsRng);
        let entry = entry_for(&k, "x", 100);
        let aid = entry.artifact_id_hex().unwrap();
        assert!(load_local_entry(home.path(), &aid).unwrap().is_none());
        write_canonical_entry(home.path(), &aid, &entry);
        let got = load_local_entry(home.path(), &aid).unwrap().unwrap();
        assert_eq!(got.body.name, "x");
    }

    // ----- slice 4d cleanup helpers -----

    #[test]
    fn collect_stale_finds_corrupt_misfiled_and_dangling() {
        let home = TempDir::new().unwrap();
        let k = SigningKey::generate(&mut OsRng);

        // 1. A clean entry — must NOT be flagged for anything by itself.
        let clean = entry_for(&k, "clean", 100);
        let clean_aid = clean.artifact_id_hex().unwrap();
        write_canonical_entry(home.path(), &clean_aid, &clean);

        // 2. An entry with a dangling tag (`stable → 9.9.9`, no such version).
        let mut with_dangling = clean.body.clone();
        with_dangling.name = "with-dangling".into();
        with_dangling
            .tags
            .insert("stable".into(), "9.9.9".into());
        with_dangling.issued_at += 1;
        let signed = CatalogEntry::issue(&k, with_dangling).unwrap();
        write_canonical_entry(home.path(), &signed.artifact_id_hex().unwrap(), &signed);

        // 3. A corrupt entry (not JSON).
        let bad_dir = state::artifact_dir(home.path(), &"a".repeat(64));
        std::fs::create_dir_all(&bad_dir).unwrap();
        std::fs::write(bad_dir.join(state::ENTRY_FILE), b"not json").unwrap();

        // 4. A misfiled entry (filed under the wrong artifact_id dir).
        let misfiled = entry_for(&k, "misfiled", 1);
        let misfiled_dir = state::artifact_dir(home.path(), &"b".repeat(64));
        std::fs::create_dir_all(&misfiled_dir).unwrap();
        std::fs::write(
            misfiled_dir.join(state::ENTRY_FILE),
            serde_json::to_vec(&misfiled).unwrap(),
        )
        .unwrap();

        let r = collect_stale(home.path(), SystemTime::now()).unwrap();
        assert_eq!(r.corrupt_entries.len(), 2, "corrupt + misfiled both reported: {:?}", r.corrupt_entries);
        assert_eq!(r.dangling_tags.len(), 1, "only 'with-dangling' is dangling");
        assert_eq!(r.dangling_tags[0].0, "with-dangling");
        assert_eq!(r.dangling_tags[0].1, "stable");
        assert_eq!(r.dangling_tags[0].2, "9.9.9");
    }

    #[test]
    fn collect_stale_flags_old_pending_sentinels() {
        // Use a `now` set FAR in the future to make today's file mtime look
        // ancient — avoids touching the filesystem clock or shimming utimes.
        let home = TempDir::new().unwrap();
        let dir = state::artifact_dir(home.path(), &"c".repeat(64));
        std::fs::create_dir_all(&dir).unwrap();
        let pending = dir.join(state::ENTRY_PENDING_FILE);
        std::fs::write(&pending, b"{}").unwrap();

        // 10 years in the future ⇒ the fresh file looks ~315M seconds old.
        let far_future = SystemTime::now() + Duration::from_secs(60 * 60 * 24 * 365 * 10);
        let r = collect_stale(home.path(), far_future).unwrap();
        assert!(
            r.pending_too_old.iter().any(|(p, _)| p == &pending),
            "old .pending must be flagged when now is well past the threshold: {r:?}"
        );

        // A fresh-clock collect_stale must NOT flag the same file.
        let r2 = collect_stale(home.path(), SystemTime::now()).unwrap();
        assert!(
            r2.pending_too_old.is_empty(),
            "fresh .pending must not be flagged when now is current: {r2:?}"
        );
    }

    #[test]
    fn report_is_empty_works() {
        assert!(report_is_empty(&StaleReport::default()));
        let mut r = StaleReport::default();
        r.corrupt_entries.push((PathBuf::from("/x"), "bad".into()));
        assert!(!report_is_empty(&r));
    }
}
