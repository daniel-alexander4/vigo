// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Project-keyed peer index — `<project_id> → [host, …]`.
//!
//! Built from `GITBACKPROJECT:<host>:<project_id_hex>:<handle>:<dr_scope>`
//! lines that envoys emit on the swarm multicast (the pre-0.49.0 form
//! omitted the trailing `:<handle>:<dr_scope>` — see ADR-023). Each
//! line announces "I (host) hold a local copy of project `pid_hex`."
//! Receivers drop the line into [`ProjectPeers`] so two consumers can
//! use it: the helper daemon's post-receive-pack fan-out hook ("who
//! else has this project?", without hand-configured
//! `cross_subnet_members:`), and the ADR-023 announced-projects cache
//! that `puddle unlock` reads to auto-adopt the projects this puddle
//! founded.
//!
//! ### Lifecycle
//!
//! - Emitted once per held project per announce cycle (~30 s).
//! - Receivers update `last_seen` on every sighting; a stale entry
//!   (older than [`STALE_SECS`]) is filtered out at lookup time and
//!   GC'd by [`ProjectPeers::prune_stale`].
//! - When the gitback gate flips off this index is cleared
//!   alongside the peer-table reset (no project_peers means no
//!   fan-out targets; the post-receive hook degrades to whatever
//!   `cross_subnet_members` lists, i.e. potentially nothing).
//!
//! ### Trust model
//!
//! Membership in the index is a hint for fan-out targeting, not an
//! authorization decision. The receiving daemon still verifies the
//! token chain on the bundle; a peer that announces holding project
//! P but doesn't actually hold the chain just rejects the push when
//! it arrives. Spoofing announcements buys the attacker nothing
//! beyond mild bandwidth wastage on the source.
//!
//! ### Persistence
//!
//! In-memory only. Discovery already restores hostnames into the
//! generic peer table; project_peers fills in fresh on each
//! announce-cycle. Restarts lose the index for one announce
//! interval — same UX cost as lockbox's peer table.

use std::collections::{BTreeMap, HashMap};
use std::net::IpAddr;
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex, OnceLock};

use serde::{Deserialize, Serialize};

/// Stale threshold — drop entries older than this on lookup or
/// prune. 5 minutes covers the typical announce interval (30 s) ten
/// times over so a temporary multicast hiccup doesn't evict every
/// peer. Tighter than the generic peer-table TTL because gitback
/// pushes are infrequent and a stale fan-out target wastes a
/// connection attempt.
pub const STALE_SECS: i64 = 5 * 60;

/// One peer's view from the project_peers index.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ProjectPeerEntry {
    pub host: String,
    pub addr: Option<IpAddr>,
    pub last_seen: i64,
    /// Project handle, if the announcer used the ADR-023 4-field
    /// `GITBACKPROJECT:` form. `None` from an envoy still on the
    /// pre-0.49.0 2-field form (host + pid only). The handle is what an
    /// adopting envoy needs to mint the founder-bootstrap token and to
    /// verify `sha256(its_puddle_pubkey ‖ handle) == pid`.
    pub handle: Option<String>,
    /// DR scope (`swarm-wide` | `members-only`) from the same 4-field
    /// form — carried so an adopted `project.json` matches the
    /// founder's intent. `None` whenever `handle` is `None`.
    pub dr_scope: Option<String>,
}

/// In-memory `<pid_hex> → host → entry` index. Locking is fine-grained
/// at the table level; the maps inside are small (typically &lt;100
/// projects, &lt;100 peers) so a coarse lock won't be a bottleneck.
#[derive(Default)]
pub struct ProjectPeers {
    inner: Mutex<HashMap<String, HashMap<String, ProjectPeerEntry>>>,
}

impl ProjectPeers {
    pub fn new() -> Self {
        Self::default()
    }

    /// Record (or refresh) a peer's claim that it holds `pid_hex`.
    /// Called by the discovery listener on every parsed
    /// `GITBACKPROJECT:` line. `handle` / `dr_scope` are `Some` only
    /// for the ADR-023 4-field form. Returns `true` if the entry was
    /// new or its address changed (caller may emit a debug log on first
    /// sightings; idempotent refreshes stay quiet).
    pub fn record(
        &self,
        pid_hex: &str,
        host: &str,
        addr: Option<IpAddr>,
        handle: Option<&str>,
        dr_scope: Option<&str>,
        now: i64,
    ) -> bool {
        let mut guard = self.inner.lock().unwrap_or_else(|p| p.into_inner());
        let map = guard.entry(pid_hex.to_string()).or_default();
        match map.get_mut(host) {
            Some(existing) => {
                let addr_changed = existing.addr != addr && addr.is_some();
                existing.last_seen = now;
                if addr.is_some() {
                    existing.addr = addr;
                }
                // Last non-`None` wins: a peer upgrading to the 4-field
                // form fills in handle/scope, and a later 3-field
                // sighting from an older peer doesn't clobber it back.
                if handle.is_some() {
                    existing.handle = handle.map(str::to_string);
                }
                if dr_scope.is_some() {
                    existing.dr_scope = dr_scope.map(str::to_string);
                }
                addr_changed
            }
            None => {
                map.insert(
                    host.to_string(),
                    ProjectPeerEntry {
                        host: host.to_string(),
                        addr,
                        last_seen: now,
                        handle: handle.map(str::to_string),
                        dr_scope: dr_scope.map(str::to_string),
                    },
                );
                true
            }
        }
    }

    /// Return every non-stale peer holding `pid_hex` at `now`. Stale
    /// entries are filtered (not yet GC'd — see [`prune_stale`]).
    pub fn peers_for(&self, pid_hex: &str, now: i64) -> Vec<ProjectPeerEntry> {
        let guard = self.inner.lock().unwrap_or_else(|p| p.into_inner());
        let cutoff = now - STALE_SECS;
        match guard.get(pid_hex) {
            None => Vec::new(),
            Some(map) => map
                .values()
                .filter(|e| e.last_seen >= cutoff)
                .cloned()
                .collect(),
        }
    }

    /// Every non-stale project we're currently hearing announced, as
    /// `(pid_hex, handle, dr_scope)` — restricted to entries that
    /// carry a handle (the pre-0.49.0 3-field form is handle-less and
    /// can't be auto-adopted). Deduped by pid; the first non-stale,
    /// handle-carrying host wins. Feeds [`write_announced_cache`].
    pub fn all_announced(&self, now: i64) -> Vec<(String, String, String)> {
        let guard = self.inner.lock().unwrap_or_else(|p| p.into_inner());
        let cutoff = now - STALE_SECS;
        let mut out = Vec::new();
        for (pid, hosts) in guard.iter() {
            if let Some(entry) = hosts
                .values()
                .find(|e| e.last_seen >= cutoff && e.handle.is_some())
            {
                let handle = entry.handle.clone().unwrap_or_default();
                let scope = entry
                    .dr_scope
                    .clone()
                    .unwrap_or_else(|| "swarm-wide".to_string());
                out.push((pid.clone(), handle, scope));
            }
        }
        out
    }

    /// Drop entries older than `now - STALE_SECS`. Returns count
    /// removed. Called periodically from the discovery / fan-out
    /// reactors; safe to call from anywhere.
    pub fn prune_stale(&self, now: i64) -> usize {
        let mut guard = self.inner.lock().unwrap_or_else(|p| p.into_inner());
        let cutoff = now - STALE_SECS;
        let mut dropped = 0;
        guard.retain(|_pid, map| {
            let before = map.len();
            map.retain(|_host, entry| entry.last_seen >= cutoff);
            dropped += before - map.len();
            !map.is_empty()
        });
        dropped
    }

    /// Wipe the entire index. Used when the gitback gate flips off.
    pub fn clear(&self) {
        let mut guard = self.inner.lock().unwrap_or_else(|p| p.into_inner());
        guard.clear();
    }

    /// Total entries across all projects (for /metrics + tests).
    pub fn len(&self) -> usize {
        let guard = self.inner.lock().unwrap_or_else(|p| p.into_inner());
        guard.values().map(|m| m.len()).sum()
    }

    pub fn is_empty(&self) -> bool {
        self.len() == 0
    }
}

// ---- process-wide singleton ----
//
// Discovery's listener parses GITBACKPROJECT lines and records into
// the singleton. The helper-daemon fan-out hook reads from it. Both
// modules avoid parameter-plumbing through discovery.rs's spawn
// signature (which already has a dozen args).
//
// Mirrors the pattern lockbox uses for INCOMING_ANNOUNCE — set
// once at agent startup, read by everyone afterwards. Tests that
// need isolation construct their own ProjectPeers directly.

static GLOBAL: OnceLock<Arc<ProjectPeers>> = OnceLock::new();

/// Initialize the process-wide [`ProjectPeers`] singleton. Called
/// once from `gitback::spawn` at agent startup. Idempotent — a
/// second call is a no-op (the existing handle keeps serving).
pub fn init_global() -> Arc<ProjectPeers> {
    GLOBAL
        .get_or_init(|| Arc::new(ProjectPeers::new()))
        .clone()
}

/// Get the process-wide [`ProjectPeers`] handle, or `None` if
/// gitback hasn't been spawned yet (early-startup, test fixtures
/// that don't init).
pub fn global() -> Option<Arc<ProjectPeers>> {
    GLOBAL.get().cloned()
}

/// Convenience: record into the global table if it exists. Quietly
/// no-ops when discovery is parsing announces before `init_global`
/// has run (gitback gate off, etc.).
pub fn record_global(
    pid_hex: &str,
    host: &str,
    addr: Option<IpAddr>,
    handle: Option<&str>,
    dr_scope: Option<&str>,
    now: i64,
) {
    if let Some(g) = global() {
        let _ = g.record(pid_hex, host, addr, handle, dr_scope, now);
    }
}

// ---- announce line format ----

/// Format the ADR-023 `GITBACKPROJECT:` line that rides on the swarm
/// multicast. Shape:
///
/// ```text
/// GITBACKPROJECT:<host>:<project_id_hex>:<handle>:<dr_scope>
/// ```
///
/// `host` follows the same constraints as the existing `GITBACK:`
/// line (no internal colons; conventionally a Unix hostname).
/// `project_id_hex` is 64-char lowercase hex. `handle` is the project
/// handle (ASCII alnum + `-_.`, no leading dot) and `dr_scope` is
/// `swarm-wide` or `members-only` — both colon-free by construction.
/// Format is enforced by the parser; the formatter trusts the caller.
pub fn format_project_line(host: &str, pid_hex: &str, handle: &str, dr_scope: &str) -> String {
    format!(
        "GITBACKPROJECT:{}:{}:{}:{}",
        host, pid_hex, handle, dr_scope
    )
}

/// 64-char lowercase-hex check for a project id.
fn pid_hex_ok(s: &str) -> bool {
    s.len() == 64
        && s.chars()
            .all(|c| c.is_ascii_digit() || ('a'..='f').contains(&c))
}

/// Lenient handle check used by [`parse_project_line`] — mirrors
/// `gitback::cli::validate_handle` without pulling it in. A malformed
/// handle in a cleartext multicast line means we drop the line.
fn handle_chars_ok(h: &str) -> bool {
    !h.is_empty()
        && h.len() <= 64
        && !h.starts_with('.')
        && h.chars()
            .all(|c| c.is_ascii_alphanumeric() || c == '-' || c == '_' || c == '.')
}

fn dr_scope_ok(s: &str) -> bool {
    s == "swarm-wide" || s == "members-only"
}

/// Parse a `GITBACKPROJECT:` line into `(host, pid_hex, handle,
/// dr_scope)`. The ADR-023 4-field form yields `Some(handle)` /
/// `Some(dr_scope)`; the pre-0.49.0 2-field form (`host:pid`) yields
/// `None` for both, so a rolling agent upgrade keeps recording peers
/// (such a project just isn't auto-adoptable until the announcer is
/// upgraded too). Returns `None` for anything malformed — wrong
/// prefix, wrong field count, empty host, non-canonical pid_hex, bad
/// handle chars, or an unrecognized dr_scope.
pub fn parse_project_line(line: &str) -> Option<(String, String, Option<String>, Option<String>)> {
    let rest = line.strip_prefix("GITBACKPROJECT:")?;
    let parts: Vec<&str> = rest.split(':').map(str::trim).collect();
    match parts.len() {
        2 => {
            let (host, pid_hex) = (parts[0], parts[1]);
            if host.is_empty() || !pid_hex_ok(pid_hex) {
                return None;
            }
            Some((host.to_string(), pid_hex.to_string(), None, None))
        }
        4 => {
            let (host, pid_hex, handle, dr_scope) = (parts[0], parts[1], parts[2], parts[3]);
            if host.is_empty()
                || !pid_hex_ok(pid_hex)
                || !handle_chars_ok(handle)
                || !dr_scope_ok(dr_scope)
            {
                return None;
            }
            Some((
                host.to_string(),
                pid_hex.to_string(),
                Some(handle.to_string()),
                Some(dr_scope.to_string()),
            ))
        }
        _ => None,
    }
}

// ---- enumerate locally-held projects ----

/// Walk every user's `~/.vigo-gitback/` and return the projects this
/// envoy currently has per-user state for, as `(pid_hex, handle,
/// dr_scope)`. Used by the announce loop to populate the ADR-023
/// 4-field `GITBACKPROJECT:` lines.
///
/// Reads `/etc/passwd` directly (same convention as the helper
/// daemon's home-lookup path) — avoids NSS-plugin latency. Per-user
/// filesystem errors are skipped, so one bad home dir doesn't poison
/// the whole sweep; a project dir whose `project.json` is missing or
/// version-mismatched is skipped — we can't announce a handle we
/// can't read.
pub fn enumerate_local_projects() -> Vec<(String, String, String)> {
    let body = match std::fs::read_to_string("/etc/passwd") {
        Ok(s) => s,
        Err(_) => return Vec::new(),
    };
    enumerate_from_passwd(&body)
}

pub(crate) fn enumerate_from_passwd(passwd_body: &str) -> Vec<(String, String, String)> {
    // pid_hex → (handle, dr_scope). Multiple users can hold the same
    // project; first readable config wins (they should agree).
    let mut out: BTreeMap<String, (String, String)> = BTreeMap::new();
    for line in passwd_body.lines() {
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        let fields: Vec<&str> = line.split(':').collect();
        if fields.len() < 7 {
            continue;
        }
        let home = std::path::PathBuf::from(fields[5]);
        let dot_dir = home.join(".vigo-gitback");
        let entries = match std::fs::read_dir(&dot_dir) {
            Ok(it) => it,
            Err(_) => continue,
        };
        for entry in entries.flatten() {
            let p = entry.path();
            if !p.is_dir() {
                continue;
            }
            let name = match p.file_name().and_then(|n| n.to_str()) {
                Some(n) if pid_hex_ok(n) => n.to_string(),
                _ => continue,
            };
            if out.contains_key(&name) {
                continue;
            }
            let pid = match super::project_state::parse_hex_id(&name) {
                Ok(p) => p,
                Err(_) => continue,
            };
            if let Ok(Some(cfg)) = super::project_state::ProjectConfig::load(&home, &pid) {
                out.insert(name, (cfg.handle, cfg.dr_scope.as_str().to_string()));
            }
        }
    }
    out.into_iter()
        .map(|(pid, (handle, scope))| (pid, handle, scope))
        .collect()
}

// ---- announced-projects cache (ADR-023) ----
//
// The catch-up reactor flushes this once per tick (~45 s) from the
// global [`ProjectPeers`] index. `puddle unlock` reads it — running as
// the user, with the signing key just made usable by the session
// helper — and auto-adopts any project whose
// `project_id == sha256(my_puddle_pubkey ‖ handle)` that this user has
// no local state for yet. That's the root-agent → user handoff: the
// root agent has the swarm announce index, the user has the key.
//
// World-readable (0644). Project ids + handles are public by
// construction — they're the `gitback://<pid>/<handle>` URL form, and
// `GITBACKPROJECT:` lines are cleartext multicast already.

/// One entry of the announced-projects cache.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct AnnouncedProject {
    pub project_id: String,
    pub handle: String,
    pub dr_scope: String,
}

/// `/var/lib/vigo/swarm/puddle/gitback-announced.json` — co-located
/// with the ADR-022 names cache in the 0755 puddle state dir.
pub fn announced_cache_path() -> PathBuf {
    crate::swarm::puddle::names_cache::puddle_state_dir().join("gitback-announced.json")
}

/// Flush the current announced-projects view to disk. No-op when
/// gitback hasn't been spawned (no global index), and best-effort:
/// any I/O error is swallowed — the next tick retries and a stale
/// cache is harmless (worst case `puddle unlock` adopts nothing this
/// round).
pub fn write_announced_cache(now: i64) {
    let Some(g) = global() else {
        return;
    };
    let entries: Vec<AnnouncedProject> = g
        .all_announced(now)
        .into_iter()
        .map(|(project_id, handle, dr_scope)| AnnouncedProject {
            project_id,
            handle,
            dr_scope,
        })
        .collect();
    let _ = write_announced_cache_at(&announced_cache_path(), &entries);
}

/// Atomic write: serialize, write a sibling tmpfile, chmod 0644,
/// rename over the target. Creates the parent dir if missing.
pub(crate) fn write_announced_cache_at(
    path: &Path,
    entries: &[AnnouncedProject],
) -> std::io::Result<()> {
    if let Some(parent) = path.parent() {
        std::fs::create_dir_all(parent)?;
    }
    let body = serde_json::to_vec_pretty(entries)
        .map_err(|e| std::io::Error::new(std::io::ErrorKind::InvalidData, e))?;
    let tmp = path.with_extension("json.tmp");
    std::fs::write(&tmp, &body)?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(&tmp, std::fs::Permissions::from_mode(0o644))?;
    }
    std::fs::rename(&tmp, path)?;
    Ok(())
}

/// Read the announced-projects cache. Empty Vec on any error (missing
/// file, parse failure, permissions) — `puddle unlock` then adopts
/// nothing this round and tries again next unlock.
pub fn load_announced_cache() -> Vec<AnnouncedProject> {
    load_announced_cache_at(&announced_cache_path())
}

pub(crate) fn load_announced_cache_at(path: &Path) -> Vec<AnnouncedProject> {
    match std::fs::read(path) {
        Ok(bytes) => serde_json::from_slice(&bytes).unwrap_or_default(),
        Err(_) => Vec::new(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::net::Ipv4Addr;

    fn now() -> i64 {
        1_700_000_000
    }

    fn pid(byte: u8) -> String {
        let mut s = String::with_capacity(64);
        for _ in 0..32 {
            s.push_str(&format!("{:02x}", byte));
        }
        s
    }

    #[test]
    fn record_and_lookup_round_trips() {
        let p = ProjectPeers::new();
        assert!(p.record(
            &pid(0xAA),
            "alpha",
            Some(IpAddr::V4(Ipv4Addr::new(10, 0, 0, 1))),
            Some("vigo"),
            Some("swarm-wide"),
            now(),
        ));
        let peers = p.peers_for(&pid(0xAA), now());
        assert_eq!(peers.len(), 1);
        assert_eq!(peers[0].host, "alpha");
        assert_eq!(peers[0].handle.as_deref(), Some("vigo"));
        assert_eq!(peers[0].dr_scope.as_deref(), Some("swarm-wide"));
    }

    #[test]
    fn record_idempotent_refresh_returns_false() {
        let p = ProjectPeers::new();
        assert!(p.record(&pid(0xBB), "host", None, None, None, now()));
        assert!(!p.record(&pid(0xBB), "host", None, None, None, now()));
    }

    #[test]
    fn record_addr_change_returns_true() {
        let p = ProjectPeers::new();
        let a = IpAddr::V4(Ipv4Addr::new(10, 0, 0, 1));
        let b = IpAddr::V4(Ipv4Addr::new(10, 0, 0, 2));
        p.record(&pid(0xCC), "host", Some(a), None, None, now());
        assert!(p.record(&pid(0xCC), "host", Some(b), None, None, now() + 1));
    }

    #[test]
    fn record_last_non_none_handle_wins() {
        let p = ProjectPeers::new();
        // A 3-field (handle-less) sighting, then a 4-field one fills in.
        p.record(&pid(0xEE), "host", None, None, None, now());
        p.record(
            &pid(0xEE),
            "host",
            None,
            Some("vigo"),
            Some("members-only"),
            now() + 1,
        );
        let peers = p.peers_for(&pid(0xEE), now() + 1);
        assert_eq!(peers[0].handle.as_deref(), Some("vigo"));
        assert_eq!(peers[0].dr_scope.as_deref(), Some("members-only"));
        // A later 3-field sighting must not clobber it back to None.
        p.record(&pid(0xEE), "host", None, None, None, now() + 2);
        let peers = p.peers_for(&pid(0xEE), now() + 2);
        assert_eq!(peers[0].handle.as_deref(), Some("vigo"));
    }

    #[test]
    fn peers_for_excludes_stale_entries() {
        let p = ProjectPeers::new();
        p.record(&pid(0xDD), "fresh", None, None, None, now());
        p.record(&pid(0xDD), "old", None, None, None, now() - STALE_SECS - 1);
        let live = p.peers_for(&pid(0xDD), now());
        assert_eq!(live.len(), 1);
        assert_eq!(live[0].host, "fresh");
    }

    #[test]
    fn prune_stale_removes_old_and_empty_buckets() {
        let p = ProjectPeers::new();
        p.record(&pid(0x11), "old", None, None, None, now() - STALE_SECS - 1);
        p.record(&pid(0x22), "fresh", None, None, None, now());
        let dropped = p.prune_stale(now());
        assert_eq!(dropped, 1);
        assert!(p.peers_for(&pid(0x11), now()).is_empty());
        assert_eq!(p.peers_for(&pid(0x22), now()).len(), 1);
        // Bucket for 0x11 should be gone entirely after prune (no
        // stale ghost entries with empty maps left behind).
        assert_eq!(p.len(), 1);
    }

    #[test]
    fn clear_drops_everything() {
        let p = ProjectPeers::new();
        p.record(&pid(0x77), "h", None, None, None, now());
        p.record(&pid(0x88), "h", None, None, None, now());
        p.clear();
        assert!(p.is_empty());
    }

    #[test]
    fn all_announced_dedups_and_drops_handleless() {
        let p = ProjectPeers::new();
        // Two hosts hold the same handle-bearing project → one entry.
        p.record(&pid(0xA1), "h1", None, Some("vigo"), Some("swarm-wide"), now());
        p.record(&pid(0xA1), "h2", None, Some("vigo"), Some("swarm-wide"), now());
        // A handle-less (legacy) project → not announced.
        p.record(&pid(0xA2), "h3", None, None, None, now());
        // A stale handle-bearing project → not announced.
        p.record(
            &pid(0xA3),
            "h4",
            None,
            Some("old"),
            Some("members-only"),
            now() - STALE_SECS - 1,
        );
        let mut got = p.all_announced(now());
        got.sort();
        assert_eq!(
            got,
            vec![(pid(0xA1), "vigo".to_string(), "swarm-wide".to_string())]
        );
    }

    #[test]
    fn format_round_trips_canonical() {
        let line = format_project_line("alpha.local", &pid(0xAB), "vigo-web", "swarm-wide");
        let (host, pid_hex, handle, scope) = parse_project_line(&line).unwrap();
        assert_eq!(host, "alpha.local");
        assert_eq!(pid_hex, pid(0xAB));
        assert_eq!(handle.as_deref(), Some("vigo-web"));
        assert_eq!(scope.as_deref(), Some("swarm-wide"));
    }

    #[test]
    fn parse_accepts_legacy_two_field_form() {
        let line = format!("GITBACKPROJECT:danlap:{}", pid(0xCD));
        let (host, pid_hex, handle, scope) = parse_project_line(&line).unwrap();
        assert_eq!(host, "danlap");
        assert_eq!(pid_hex, pid(0xCD));
        assert!(handle.is_none());
        assert!(scope.is_none());
    }

    #[test]
    fn parse_rejects_wrong_prefix() {
        assert!(parse_project_line("GITBACK:host:abcd").is_none());
    }

    #[test]
    fn parse_rejects_short_pid() {
        assert!(parse_project_line("GITBACKPROJECT:host:deadbeef").is_none());
    }

    #[test]
    fn parse_rejects_uppercase_pid() {
        let line = format!("GITBACKPROJECT:host:{}", "A".repeat(64));
        assert!(parse_project_line(&line).is_none());
    }

    #[test]
    fn parse_rejects_empty_host() {
        let line = format!("GITBACKPROJECT::{}", pid(0xAA));
        assert!(parse_project_line(&line).is_none());
    }

    #[test]
    fn parse_rejects_three_field_form() {
        // host:pid:handle with no dr_scope is malformed, not legacy.
        let line = format!("GITBACKPROJECT:host:{}:vigo", pid(0xAA));
        assert!(parse_project_line(&line).is_none());
    }

    #[test]
    fn parse_rejects_bad_handle_and_unknown_scope() {
        let bad_handle = format!("GITBACKPROJECT:host:{}:bad/handle:swarm-wide", pid(0xAA));
        assert!(parse_project_line(&bad_handle).is_none());
        let bad_scope = format!("GITBACKPROJECT:host:{}:vigo:fleet-wide", pid(0xAA));
        assert!(parse_project_line(&bad_scope).is_none());
    }

    #[test]
    fn enumerate_walks_user_dirs_and_reads_project_json() {
        use crate::swarm::gitback::project_state::{DrScope, ProjectConfig, PROJECT_STATE_VERSION};
        let tmp = tempfile::tempdir().unwrap();
        let dan_home = tmp.path().join("home/dan");
        let alice_home = tmp.path().join("home/alice");

        let mk = |home: &std::path::Path, byte: u8, handle: &str, scope: DrScope| {
            ProjectConfig {
                version: PROJECT_STATE_VERSION,
                project_id: pid(byte),
                handle: handle.to_string(),
                founder_puddle_pubkey: pid(0xFF),
                members: vec![pid(0xFF)],
                dr_scope: scope,
                cross_subnet_members: vec![],
                updated_at: 0,
                tombstoned_at: None,
            }
            .save(home)
            .unwrap();
        };
        mk(&dan_home, 0x01, "vigo", DrScope::SwarmWide);
        mk(&dan_home, 0x02, "dotfiles", DrScope::MembersOnly);
        mk(&alice_home, 0x01, "vigo", DrScope::SwarmWide);
        // A non-pid-shaped subdir is ignored.
        std::fs::create_dir_all(dan_home.join(".vigo-gitback").join("not-a-pid")).unwrap();
        // A pid-shaped dir with no project.json is skipped — we can't
        // announce a handle we can't read.
        std::fs::create_dir_all(dan_home.join(".vigo-gitback").join(pid(0x09))).unwrap();

        let passwd_body = format!(
            "root:x:0:0:root:/root:/bin/bash\n\
             dan:x:1000:1000:Dan:{}:/bin/sh\n\
             alice:x:1001:1001:Alice:{}:/bin/sh\n\
             nodir:x:1002:1002::/missing/home:/bin/sh\n",
            dan_home.display(),
            alice_home.display(),
        );
        // Returned from a BTreeMap → sorted by pid_hex; pid(0x01) < pid(0x02).
        let projects = enumerate_from_passwd(&passwd_body);
        assert_eq!(
            projects,
            vec![
                (pid(0x01), "vigo".to_string(), "swarm-wide".to_string()),
                (
                    pid(0x02),
                    "dotfiles".to_string(),
                    "members-only".to_string()
                ),
            ]
        );
    }

    #[test]
    fn announced_cache_round_trips() {
        let tmp = tempfile::tempdir().unwrap();
        let path = tmp.path().join("nested/dir/gitback-announced.json");
        let entries = vec![
            AnnouncedProject {
                project_id: pid(0x01),
                handle: "vigo".to_string(),
                dr_scope: "swarm-wide".to_string(),
            },
            AnnouncedProject {
                project_id: pid(0x02),
                handle: "dotfiles".to_string(),
                dr_scope: "members-only".to_string(),
            },
        ];
        write_announced_cache_at(&path, &entries).unwrap();
        assert!(path.exists());
        assert_eq!(load_announced_cache_at(&path), entries);
        // Missing file → empty.
        assert!(load_announced_cache_at(&tmp.path().join("nope.json")).is_empty());
        // Garbage → empty.
        std::fs::write(&path, b"not json").unwrap();
        assert!(load_announced_cache_at(&path).is_empty());
    }
}
