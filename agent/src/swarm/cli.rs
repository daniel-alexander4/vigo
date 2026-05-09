// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Read-only diagnostic verbs for the swarm substrate (0.41.1+).
//!
//! ```text
//! vigo swarm status              # gates, manifest version, peer count, last checkin
//! vigo swarm seeds list          # one line per active blob this envoy holds
//! vigo swarm seeds show <key>    # full detail for one blob (sha-prefix or label)
//! vigo swarm peers list          # known peers from the multicast / gossip table
//! ```
//!
//! Read-only against three daemon-canonical files written under
//! `/var/lib/vigo/swarm/`:
//!
//! - `state.json` — resolved subsystem gates (this envoy's view)
//! - `manifest.json` — LWW-Map of active blob entries
//! - `peers.json` — known peers + blob coverage
//!
//! All three are mode 0644 since 0.41.1 — these verbs work for any
//! local user. The only thing that's still root-only is the blob cache
//! directory itself (`/var/lib/vigo/swarm/.blobs/`); status reports
//! `(root-only)` for cache size when stat fails.
//!
//! No daemon socket, no LMDB read, no server round-trip. Pure file
//! reads against state the agent already maintains.

use std::fs;
use std::path::{Path, PathBuf};

use anyhow::{bail, Context, Result};

use super::manifest::{BlobEntry, BlobStatus, ManifestData};
use super::state_mirror;

const SWARM_DIR: &str = "/var/lib/vigo/swarm";

pub fn run(args: Vec<String>) -> Result<()> {
    let verb = args.first().map(String::as_str).unwrap_or("");
    match verb {
        "" | "help" | "--help" | "-h" => {
            print_usage();
            Ok(())
        }
        "status" => cmd_status(&args[1..]),
        "seeds" => dispatch_seeds(&args[1..]),
        "peers" => dispatch_peers(&args[1..]),
        other => bail!(
            "unknown `vigo swarm` subcommand: {other}\n\
             run `vigo swarm --help` for usage"
        ),
    }
}

fn print_usage() {
    eprintln!(
        "vigo swarm — diagnostic + per-user content verbs for the swarm substrate

USAGE:
    vigo swarm <SUBCOMMAND>

DIAGNOSTIC (envoy-wide; read-only):
    status                Show resolved gate state, manifest version,
                          active-blob count, peer count, last checkin.
    seeds list            One line per active blob this envoy holds.
    seeds show <key>      Full detail for one blob. <key> is a sha256
                          prefix (>=4 chars) or a label match.
    peers list            Known peers from the gossip / multicast table.

PER-USER CONTENT:
    puddle <verb>         Per-user puddle identity (ADR-014).
    cryptsync <verb>      Per-user encrypted P2P sync (ADR-016 / 020).
    gitmirror <verb>      Git-over-swarm projects (ADR-015).

The diagnostic verbs read /var/lib/vigo/swarm/{{state,manifest,peers}}.json
(mode 0644). No sudo required."
    );
}

// ---------------- status ----------------

fn cmd_status(args: &[String]) -> Result<()> {
    if args.iter().any(|a| matches!(a.as_str(), "-h" | "--help")) {
        eprintln!("vigo swarm status — show this envoy's swarm posture");
        return Ok(());
    }

    let state = state_mirror::read(&state_mirror::default_path()).ok();
    let manifest = read_manifest_data(&manifest_path()).ok();
    let peers = read_peers(&peers_path()).unwrap_or_default();

    println!("Swarm posture for this envoy");
    println!();
    if let Some(ref s) = state {
        println!("  agent version:    {}", s.agent_version);
        println!("  policy version:   {}", s.policy_version);
        println!("  last checkin:     {}", format_unix_ts(s.last_checkin_ts));
        println!();
        println!("  Gates (resolved from server pattern lists):");
        println!("    swarm:        {}", on_off(s.gates.swarm));
        println!("    payload:      {}", on_off(s.gates.payload));
        println!("    longdrawer:     {}", on_off(s.gates.longdrawer));
        println!("    puddle:       {}  (cryptsync follows)", on_off(s.gates.puddle));
        println!("    gitmirror:    {}", on_off(s.gates.gitmirror));
    } else {
        println!("  state.json:       not found (agent has not completed a check-in yet?)");
        println!("                    expected at {}",
                 state_mirror::default_path().display());
    }
    println!();

    if let Some(m) = manifest {
        let active = m.entries.values().filter(|e| matches!(e.status, BlobStatus::Active)).count();
        let revoked = m.entries.values().filter(|e| matches!(e.status, BlobStatus::Revoked)).count();
        println!("  Manifest:");
        println!("    version:      {}", m.version);
        println!("    active blobs: {active}");
        println!("    revoked:      {revoked}");
    } else {
        println!("  manifest.json:    not found at {}", manifest_path().display());
    }
    println!();

    let unique_addrs = peers
        .iter()
        .map(|p| p.address.as_str())
        .collect::<std::collections::HashSet<_>>()
        .len();
    println!("  Peers:");
    println!("    known addresses: {unique_addrs}");
    println!("    known (addr × blob) pairs: {}", peers.len());
    println!();

    let blobs_dir = Path::new(SWARM_DIR).join(".blobs");
    match dir_size(&blobs_dir) {
        Ok(size) => println!("  Blob cache:       {} ({})", human_bytes(size), blobs_dir.display()),
        Err(_) => println!("  Blob cache:       (root-only) {}", blobs_dir.display()),
    }
    Ok(())
}

// ---------------- seeds ----------------

fn dispatch_seeds(args: &[String]) -> Result<()> {
    let sub = args.first().map(String::as_str).unwrap_or("");
    match sub {
        "" | "help" | "--help" | "-h" => {
            eprintln!(
                "vigo swarm seeds — inspect blobs this envoy holds\n\n\
                 USAGE:\n    vigo swarm seeds list\n    vigo swarm seeds show <sha-prefix-or-label>"
            );
            Ok(())
        }
        "list" => cmd_seeds_list(&args[1..]),
        "show" => cmd_seeds_show(&args[1..]),
        other => bail!(
            "unknown `vigo swarm seeds` subcommand: {other}\n\
             run `vigo swarm seeds --help` for usage"
        ),
    }
}

fn cmd_seeds_list(_args: &[String]) -> Result<()> {
    let m = read_manifest_data(&manifest_path())
        .with_context(|| format!("reading {}", manifest_path().display()))?;
    let mut active: Vec<&BlobEntry> = m
        .entries
        .values()
        .filter(|e| matches!(e.status, BlobStatus::Active))
        .collect();
    active.sort_by_key(|e| std::cmp::Reverse(e.updated_at));

    if active.is_empty() {
        println!("(no active seeds in manifest)");
        return Ok(());
    }
    println!("{:<14} {:<20} {:<24} {:<8} {}",
             "SHA", "LABEL", "TARGET", "AGE", "RETENTION");
    for e in active {
        println!(
            "{:<14} {:<20} {:<24} {:<8} {}",
            short_sha(&e.sha256),
            truncate(&e.label, 20),
            truncate(&e.target, 24),
            age_string(e.updated_at),
            e.retention,
        );
    }
    Ok(())
}

fn cmd_seeds_show(args: &[String]) -> Result<()> {
    let key = args
        .first()
        .context("vigo swarm seeds show <sha-prefix-or-label>")?;
    let m = read_manifest_data(&manifest_path())
        .with_context(|| format!("reading {}", manifest_path().display()))?;

    let matches: Vec<&BlobEntry> = m
        .entries
        .values()
        .filter(|e| {
            e.sha256.starts_with(key.as_str()) || e.label == *key
        })
        .collect();

    let entry = match matches.len() {
        0 => bail!("no manifest entry matches {key:?} (try a longer sha prefix or `seeds list`)"),
        1 => matches[0],
        n => {
            let mut msg = format!("{key:?} is ambiguous — matches {n} entries:\n");
            for e in matches.iter().take(8) {
                msg.push_str(&format!("  {} {}\n", short_sha(&e.sha256), e.label));
            }
            bail!("{msg}use a longer sha prefix or the full label");
        }
    };

    println!("sha256:        {}", entry.sha256);
    println!("label:         {}", entry.label);
    println!("target:        {}", entry.target);
    println!("retention:     {}", entry.retention);
    println!("size:          {} ({})", human_bytes(entry.size), entry.size);
    println!("status:        {:?}", entry.status);
    println!("seeder:        {}", entry.seeder_id);
    if !entry.seeder_address.is_empty() {
        println!("seeder addr:   {}", entry.seeder_address);
    }
    println!("updated_at:    {}", format_unix_ts((entry.updated_at / 1000) as i64));
    println!("updated_by:    {}", entry.updated_by);

    let peers = read_peers(&peers_path()).unwrap_or_default();
    let blob_peers: Vec<&SavedPeer> = peers.iter().filter(|p| p.sha256 == entry.sha256).collect();
    if !blob_peers.is_empty() {
        println!();
        println!("Peers holding this blob:");
        println!("  {:<24} {:>8}  {:<10}  AGE", "ADDRESS", "CHUNKS", "SOURCE");
        for p in blob_peers {
            println!(
                "  {:<24} {:>8}  {:<10}  {}",
                truncate(&p.address, 24),
                p.chunk_count,
                truncate(&p.source, 10),
                format_age_secs(p.age_secs),
            );
        }
    }
    Ok(())
}

// ---------------- peers ----------------

fn dispatch_peers(args: &[String]) -> Result<()> {
    let sub = args.first().map(String::as_str).unwrap_or("");
    match sub {
        "" | "help" | "--help" | "-h" => {
            eprintln!(
                "vigo swarm peers — inspect known peers from the gossip table\n\n\
                 USAGE:\n    vigo swarm peers list"
            );
            Ok(())
        }
        "list" => cmd_peers_list(&args[1..]),
        other => bail!(
            "unknown `vigo swarm peers` subcommand: {other}\n\
             run `vigo swarm peers --help` for usage"
        ),
    }
}

fn cmd_peers_list(_args: &[String]) -> Result<()> {
    let peers = read_peers(&peers_path())
        .with_context(|| format!("reading {}", peers_path().display()))?;
    if peers.is_empty() {
        println!("(no peers in gossip table)");
        return Ok(());
    }
    // Group by address: count blobs known + most-recent age.
    use std::collections::BTreeMap;
    let mut by_addr: BTreeMap<&str, (usize, u64)> = BTreeMap::new();
    for p in &peers {
        let entry = by_addr.entry(&p.address).or_insert((0, u64::MAX));
        entry.0 += 1;
        entry.1 = entry.1.min(p.age_secs);
    }
    println!("{:<24} {:>8}  LAST SEEN", "ADDRESS", "BLOBS");
    for (addr, (blobs, age)) in by_addr {
        println!("{:<24} {:>8}  {}", addr, blobs, format_age_secs(age));
    }
    Ok(())
}

// ---------------- helpers ----------------

fn manifest_path() -> PathBuf {
    Path::new(SWARM_DIR).join("manifest.json")
}

fn peers_path() -> PathBuf {
    Path::new(SWARM_DIR).join("peers.json")
}

fn read_manifest_data(path: &Path) -> Result<ManifestData> {
    let bytes = fs::read(path).with_context(|| format!("reading {}", path.display()))?;
    serde_json::from_slice(&bytes).with_context(|| format!("parsing {}", path.display()))
}

/// Mirror of the on-disk shape produced by `PeerMap::save`. Kept local
/// so the CLI doesn't take a dependency on the private `peermap`
/// types (and so the file format and the CLI parser change together).
#[derive(serde::Deserialize)]
struct SavedPeer {
    sha256: String,
    address: String,
    chunk_count: u32,
    source: String,
    age_secs: u64,
}

fn read_peers(path: &Path) -> Result<Vec<SavedPeer>> {
    let bytes = fs::read(path).with_context(|| format!("reading {}", path.display()))?;
    serde_json::from_slice(&bytes).with_context(|| format!("parsing {}", path.display()))
}

fn on_off(b: bool) -> &'static str {
    if b {
        "on"
    } else {
        "off"
    }
}

fn short_sha(s: &str) -> String {
    s.chars().take(12).collect()
}

fn truncate(s: &str, max: usize) -> String {
    if s.chars().count() <= max {
        s.to_string()
    } else {
        let mut out: String = s.chars().take(max.saturating_sub(1)).collect();
        out.push('…');
        out
    }
}

fn human_bytes(n: u64) -> String {
    const UNITS: &[&str] = &["B", "KiB", "MiB", "GiB", "TiB"];
    let mut v = n as f64;
    let mut i = 0;
    while v >= 1024.0 && i < UNITS.len() - 1 {
        v /= 1024.0;
        i += 1;
    }
    if i == 0 {
        format!("{n} B")
    } else {
        format!("{v:.1} {}", UNITS[i])
    }
}

fn format_unix_ts(ts: i64) -> String {
    if ts <= 0 {
        return "(unknown)".to_string();
    }
    use std::time::{Duration, SystemTime, UNIX_EPOCH};
    let now = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs() as i64)
        .unwrap_or(0);
    let age = (now - ts).max(0) as u64;
    let dt = UNIX_EPOCH + Duration::from_secs(ts as u64);
    // No chrono dep — produce ISO-ish via SystemTime arithmetic isn't
    // portable, so fall back to "<age> ago" plus a raw stamp.
    let _ = dt;
    format!("{} ago (unix:{ts})", format_age_secs(age))
}

fn age_string(updated_at_millis: u64) -> String {
    use std::time::{SystemTime, UNIX_EPOCH};
    let now_millis = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_millis() as u64)
        .unwrap_or(0);
    let age_secs = now_millis.saturating_sub(updated_at_millis) / 1000;
    format_age_secs(age_secs)
}

fn format_age_secs(secs: u64) -> String {
    if secs == u64::MAX {
        return "?".to_string();
    }
    if secs < 60 {
        format!("{secs}s")
    } else if secs < 3600 {
        format!("{}m", secs / 60)
    } else if secs < 86400 {
        format!("{}h", secs / 3600)
    } else {
        format!("{}d", secs / 86400)
    }
}

fn dir_size(path: &Path) -> Result<u64> {
    let mut total = 0u64;
    for entry in fs::read_dir(path).with_context(|| format!("reading {}", path.display()))? {
        let entry = entry?;
        let meta = entry.metadata()?;
        if meta.is_file() {
            total += meta.len();
        } else if meta.is_dir() {
            total += dir_size(&entry.path()).unwrap_or(0);
        }
    }
    Ok(total)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn human_bytes_scales_correctly() {
        assert_eq!(human_bytes(0), "0 B");
        assert_eq!(human_bytes(512), "512 B");
        assert_eq!(human_bytes(1024), "1.0 KiB");
        assert_eq!(human_bytes(1024 * 1024), "1.0 MiB");
        assert_eq!(human_bytes(5 * 1024 * 1024 * 1024), "5.0 GiB");
    }

    #[test]
    fn format_age_secs_buckets() {
        assert_eq!(format_age_secs(0), "0s");
        assert_eq!(format_age_secs(45), "45s");
        assert_eq!(format_age_secs(120), "2m");
        assert_eq!(format_age_secs(7200), "2h");
        assert_eq!(format_age_secs(86400 * 3), "3d");
    }

    #[test]
    fn truncate_handles_overflow() {
        assert_eq!(truncate("hello", 10), "hello");
        assert_eq!(truncate("hello world", 7), "hello …");
    }

    #[test]
    fn short_sha_takes_12() {
        assert_eq!(short_sha("0123456789abcdef0123456789abcdef"), "0123456789ab");
        assert_eq!(short_sha("abcd"), "abcd");
    }

    #[test]
    fn on_off_strings() {
        assert_eq!(on_off(true), "on");
        assert_eq!(on_off(false), "off");
    }

    #[test]
    fn read_manifest_round_trips_via_disk() {
        let dir = tempfile::TempDir::new().unwrap();
        let path = dir.path().join("manifest.json");
        let m = ManifestData {
            version: 7,
            entries: std::collections::HashMap::new(),
        };
        fs::write(&path, serde_json::to_vec_pretty(&m).unwrap()).unwrap();
        let got = read_manifest_data(&path).unwrap();
        assert_eq!(got.version, 7);
    }

    #[test]
    fn read_peers_decodes_saved_shape() {
        let dir = tempfile::TempDir::new().unwrap();
        let path = dir.path().join("peers.json");
        fs::write(
            &path,
            r#"[{"sha256":"abc","address":"10.0.0.1:1531","chunk_count":3,"source":"multicast","age_secs":12}]"#,
        )
        .unwrap();
        let got = read_peers(&path).unwrap();
        assert_eq!(got.len(), 1);
        assert_eq!(got[0].address, "10.0.0.1:1531");
    }

    #[test]
    fn unknown_subcommand_errors() {
        let err = run(vec!["bogus".to_string()]).unwrap_err();
        assert!(format!("{err}").contains("bogus"));
    }
}
