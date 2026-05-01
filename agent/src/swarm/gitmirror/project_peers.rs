// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Project-keyed peer index — `<project_id> → [host, …]`.
//!
//! Built from `GITMIRRORPROJECT:<host>:<project_id_hex>` lines that
//! envoys emit on the swarm multicast. Each line announces "I (host)
//! hold a local copy of project `pid_hex`." Receivers drop the line
//! into [`ProjectPeers`] so the helper daemon's post-receive-pack
//! fan-out hook can ask "who else has this project?" without needing
//! `cross_subnet_members:` to be configured by hand.
//!
//! ### Lifecycle
//!
//! - Emitted once per held project per announce cycle (~30 s).
//! - Receivers update `last_seen` on every sighting; a stale entry
//!   (older than [`STALE_SECS`]) is filtered out at lookup time and
//!   GC'd by [`ProjectPeers::prune_stale`].
//! - When the gitmirror gate flips off this index is cleared
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
//! interval — same UX cost as cryptsync's peer table.

use std::collections::HashMap;
use std::net::IpAddr;
use std::sync::{Arc, Mutex, OnceLock};

/// Stale threshold — drop entries older than this on lookup or
/// prune. 5 minutes covers the typical announce interval (30 s) ten
/// times over so a temporary multicast hiccup doesn't evict every
/// peer. Tighter than the generic peer-table TTL because gitmirror
/// pushes are infrequent and a stale fan-out target wastes a
/// connection attempt.
pub const STALE_SECS: i64 = 5 * 60;

/// One peer's view from the project_peers index.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ProjectPeerEntry {
    pub host: String,
    pub addr: Option<IpAddr>,
    pub last_seen: i64,
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
    /// `GITMIRRORPROJECT:` line. Returns `true` if the entry was new
    /// or its address changed (caller may emit a debug log on first
    /// sightings; idempotent refreshes stay quiet).
    pub fn record(&self, pid_hex: &str, host: &str, addr: Option<IpAddr>, now: i64) -> bool {
        let mut guard = self.inner.lock().unwrap_or_else(|p| p.into_inner());
        let map = guard.entry(pid_hex.to_string()).or_default();
        match map.get_mut(host) {
            Some(existing) => {
                let addr_changed = existing.addr != addr && addr.is_some();
                existing.last_seen = now;
                if addr.is_some() {
                    existing.addr = addr;
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

    /// Wipe the entire index. Used when the gitmirror gate flips off.
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
// Discovery's listener parses GITMIRRORPROJECT lines and records into
// the singleton. The helper-daemon fan-out hook reads from it. Both
// modules avoid parameter-plumbing through discovery.rs's spawn
// signature (which already has a dozen args).
//
// Mirrors the pattern cryptsync uses for INCOMING_ANNOUNCE — set
// once at agent startup, read by everyone afterwards. Tests that
// need isolation construct their own ProjectPeers directly.

static GLOBAL: OnceLock<Arc<ProjectPeers>> = OnceLock::new();

/// Initialize the process-wide [`ProjectPeers`] singleton. Called
/// once from `gitmirror::spawn` at agent startup. Idempotent — a
/// second call is a no-op (the existing handle keeps serving).
pub fn init_global() -> Arc<ProjectPeers> {
    GLOBAL
        .get_or_init(|| Arc::new(ProjectPeers::new()))
        .clone()
}

/// Get the process-wide [`ProjectPeers`] handle, or `None` if
/// gitmirror hasn't been spawned yet (early-startup, test fixtures
/// that don't init).
pub fn global() -> Option<Arc<ProjectPeers>> {
    GLOBAL.get().cloned()
}

/// Convenience: record into the global table if it exists. Quietly
/// no-ops when discovery is parsing announces before `init_global`
/// has run (gitmirror gate off, etc.).
pub fn record_global(pid_hex: &str, host: &str, addr: Option<IpAddr>, now: i64) {
    if let Some(g) = global() {
        let _ = g.record(pid_hex, host, addr, now);
    }
}

// ---- announce line format ----

/// Format the `GITMIRRORPROJECT:` line that rides on the swarm
/// multicast. Shape:
///
/// ```text
/// GITMIRRORPROJECT:<host>:<project_id_hex>
/// ```
///
/// `host` follows the same constraints as the existing `GITMIRROR:`
/// line (no internal colons; conventionally a Unix hostname).
/// `project_id_hex` is 64-char lowercase hex; format is enforced by
/// the parser, the formatter trusts the caller.
pub fn format_project_line(host: &str, pid_hex: &str) -> String {
    format!("GITMIRRORPROJECT:{}:{}", host, pid_hex)
}

/// Parse a `GITMIRRORPROJECT:` line into `(host, pid_hex)`. Returns
/// `None` for malformed input — wrong prefix, missing colons,
/// empty fields, non-canonical pid_hex (anything other than exactly
/// 64 lowercase hex chars).
pub fn parse_project_line(line: &str) -> Option<(String, String)> {
    let rest = line.strip_prefix("GITMIRRORPROJECT:")?;
    let mut parts = rest.splitn(2, ':');
    let host = parts.next()?.trim().to_string();
    let pid_hex = parts.next()?.trim().to_string();
    if host.is_empty() || pid_hex.is_empty() {
        return None;
    }
    if pid_hex.len() != 64 {
        return None;
    }
    if !pid_hex
        .chars()
        .all(|c| c.is_ascii_digit() || ('a'..='f').contains(&c))
    {
        return None;
    }
    Some((host, pid_hex))
}

// ---- enumerate locally-held projects ----

/// Walk every user's `~/.vigo-gitmirror/` and return the set of
/// `project_id_hex` strings this envoy currently has local state for.
/// Used by the announce loop to populate `GITMIRRORPROJECT:` lines.
///
/// Reads `/etc/passwd` directly (same convention as the helper
/// daemon's home-lookup path) — avoids NSS-plugin latency. Filesystem
/// errors per-user are logged at debug and skipped, so one bad home
/// dir doesn't poison the whole sweep.
pub fn enumerate_local_projects() -> Vec<String> {
    let body = match std::fs::read_to_string("/etc/passwd") {
        Ok(s) => s,
        Err(_) => return Vec::new(),
    };
    enumerate_from_passwd(&body)
}

pub(crate) fn enumerate_from_passwd(passwd_body: &str) -> Vec<String> {
    let mut out = std::collections::BTreeSet::new();
    for line in passwd_body.lines() {
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        let fields: Vec<&str> = line.split(':').collect();
        if fields.len() < 7 {
            continue;
        }
        let home = std::path::PathBuf::from(fields[5]);
        let dot_dir = home.join(".vigo-gitmirror");
        let entries = match std::fs::read_dir(&dot_dir) {
            Ok(it) => it,
            Err(_) => continue,
        };
        for entry in entries.flatten() {
            let p = entry.path();
            if !p.is_dir() {
                continue;
            }
            if let Some(name) = p.file_name().and_then(|n| n.to_str()) {
                if name.len() == 64
                    && name
                        .chars()
                        .all(|c| c.is_ascii_digit() || ('a'..='f').contains(&c))
                {
                    out.insert(name.to_string());
                }
            }
        }
    }
    out.into_iter().collect()
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
        assert!(p.record(&pid(0xAA), "alpha", Some(IpAddr::V4(Ipv4Addr::new(10, 0, 0, 1))), now()));
        let peers = p.peers_for(&pid(0xAA), now());
        assert_eq!(peers.len(), 1);
        assert_eq!(peers[0].host, "alpha");
    }

    #[test]
    fn record_idempotent_refresh_returns_false() {
        let p = ProjectPeers::new();
        assert!(p.record(&pid(0xBB), "host", None, now()));
        assert!(!p.record(&pid(0xBB), "host", None, now()));
    }

    #[test]
    fn record_addr_change_returns_true() {
        let p = ProjectPeers::new();
        let a = IpAddr::V4(Ipv4Addr::new(10, 0, 0, 1));
        let b = IpAddr::V4(Ipv4Addr::new(10, 0, 0, 2));
        p.record(&pid(0xCC), "host", Some(a), now());
        assert!(p.record(&pid(0xCC), "host", Some(b), now() + 1));
    }

    #[test]
    fn peers_for_excludes_stale_entries() {
        let p = ProjectPeers::new();
        p.record(&pid(0xDD), "fresh", None, now());
        p.record(&pid(0xDD), "old", None, now() - STALE_SECS - 1);
        let live = p.peers_for(&pid(0xDD), now());
        assert_eq!(live.len(), 1);
        assert_eq!(live[0].host, "fresh");
    }

    #[test]
    fn prune_stale_removes_old_and_empty_buckets() {
        let p = ProjectPeers::new();
        p.record(&pid(0x11), "old", None, now() - STALE_SECS - 1);
        p.record(&pid(0x22), "fresh", None, now());
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
        p.record(&pid(0x77), "h", None, now());
        p.record(&pid(0x88), "h", None, now());
        p.clear();
        assert!(p.is_empty());
    }

    #[test]
    fn format_round_trips_canonical() {
        let line = format_project_line("alpha.local", &pid(0xAB));
        let (host, pid_hex) = parse_project_line(&line).unwrap();
        assert_eq!(host, "alpha.local");
        assert_eq!(pid_hex, pid(0xAB));
    }

    #[test]
    fn parse_rejects_wrong_prefix() {
        assert!(parse_project_line("GITMIRROR:host:abcd").is_none());
    }

    #[test]
    fn parse_rejects_short_pid() {
        assert!(parse_project_line("GITMIRRORPROJECT:host:deadbeef").is_none());
    }

    #[test]
    fn parse_rejects_uppercase_pid() {
        let line = format!("GITMIRRORPROJECT:host:{}", "A".repeat(64));
        assert!(parse_project_line(&line).is_none());
    }

    #[test]
    fn parse_rejects_empty_host() {
        let line = format!("GITMIRRORPROJECT::{}", pid(0xAA));
        assert!(parse_project_line(&line).is_none());
    }

    #[test]
    fn enumerate_walks_user_dirs() {
        let tmp = tempfile::tempdir().unwrap();
        let dan_home = tmp.path().join("home/dan");
        let alice_home = tmp.path().join("home/alice");
        std::fs::create_dir_all(dan_home.join(".vigo-gitmirror").join(pid(0x01))).unwrap();
        std::fs::create_dir_all(dan_home.join(".vigo-gitmirror").join(pid(0x02))).unwrap();
        std::fs::create_dir_all(alice_home.join(".vigo-gitmirror").join(pid(0x01))).unwrap();
        // Throw in a non-pid-shaped subdir to verify it's ignored.
        std::fs::create_dir_all(dan_home.join(".vigo-gitmirror").join("not-a-pid")).unwrap();

        let passwd_body = format!(
            "root:x:0:0:root:/root:/bin/bash\n\
             dan:x:1000:1000:Dan:{}:/bin/sh\n\
             alice:x:1001:1001:Alice:{}:/bin/sh\n\
             nodir:x:1002:1002::/missing/home:/bin/sh\n",
            dan_home.display(),
            alice_home.display(),
        );
        let projects = enumerate_from_passwd(&passwd_body);
        assert_eq!(projects.len(), 2, "expected two distinct project ids");
        assert!(projects.contains(&pid(0x01)));
        assert!(projects.contains(&pid(0x02)));
    }
}
