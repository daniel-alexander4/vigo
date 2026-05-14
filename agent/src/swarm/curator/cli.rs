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

use std::path::{Path, PathBuf};

use anyhow::{anyhow, bail, Context, Result};

use super::catalog::CatalogEntry;
use super::catalog_cache;
use super::resolver::Resolver;
use super::state;
use crate::auth;
use crate::swarm::gitback::envelopes::DELEGATION_MAX_LEN;
use crate::traits::gitback::passwd_path;
use crate::update::TlsCerts;

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
        other => bail!(
            "unknown subcommand `vigo swarm curator {}`. \
             Run `vigo swarm curator --help` for usage.",
            other
        ),
    }
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

Mutating verbs (push / tag / untag / set-recipients / set-target / rm /
prune / gc / stale / purge) ship in slice 4c — they sign catalog entries
through the per-user puddle session helper and need an unlocked puddle
plus `curator: true` on your usercrate.
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

/// Build a [`Resolver`] pointed at this envoy's configured API + TLS. Empty
/// api_addr / missing certs are tolerated — the resolver falls back to the
/// cached catalog and the local blob cache, so read paths still work offline
/// for any artifact this envoy already holds.
fn build_resolver() -> Result<Resolver> {
    let data_dir = crate::platform::paths::default_data_dir();
    let cfg = crate::config::load(&data_dir);
    let api_addr = cfg.get("api_addr").cloned().unwrap_or_else(|| "https://localhost:8443".to_string());
    let tls = if auth::keypair::has_bootstrap_certs(&data_dir) {
        Some(TlsCerts {
            ca_cert: auth::keypair::ca_cert_path(&data_dir).to_string_lossy().to_string(),
            client_cert: auth::keypair::tls_cert_path(&data_dir).to_string_lossy().to_string(),
            client_key: auth::keypair::tls_key_path(&data_dir).to_string_lossy().to_string(),
        })
    } else {
        None
    };
    Ok(Resolver::new(api_addr, tls))
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
}
