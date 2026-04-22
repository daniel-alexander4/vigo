// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! In-memory peer table for LAN-native vigorepo selection.
//!
//! Every vigorepo-enabled envoy announces its local info (hostname,
//! user list, tag list) on the existing swarm multicast channel. The
//! listener ingests those announcements into this table, which the
//! source-side selector reads to build the candidate pool for each
//! repo. There is no server involvement: source envoys pick mirrors
//! directly from the peer table using the same deterministic sha256
//! algorithm the deleted `server/vigorepo/claimtable.go` used.
//!
//! ### Ownership
//!
//! Constructed once at agent startup (`agent/src/main.rs`) and shared
//! between:
//!
//! - The vigorepo reactor (reader — consumed by the source-side
//!   selector in Phase 6).
//! - The swarm multicast listener (writer — ingests any `VIGOREPO:`
//!   lines from received announcements).
//!
//! Both sides hold `Arc<PeerTable>`. Internal locking is a
//! single `RwLock` over the `HashMap`; readers are non-blocking
//! except when a write lock is held for ingest or eviction.
//!
//! ### TTL
//!
//! Entries expire after [`PEER_TTL_SECS`] (5 minutes) without a
//! refresh. Eviction is lazy: pruning happens on the next
//! [`PeerTable::prune_expired`] call, which callers should run every
//! few seconds (the vigorepo reactor tick is the natural host).
//! This keeps the write lock short — we don't run a separate
//! sweeper task.
//!
//! ### Capability semantics
//!
//! Presence in the table **is** the capability. A dormant
//! vigorepo subsystem (matchfile var missing / false) never
//! announces, so its envoy never lands in any peer's table.
//! This collapses "announce + capability bit" into a single fact:
//! if you see it, you can pick it.

use std::collections::HashMap;
use std::sync::RwLock;
use std::time::{SystemTime, UNIX_EPOCH};

/// Drop peer entries not refreshed within this window. 5 minutes
/// matches the plan and is long enough to ride out a routine agent
/// restart (agents re-announce within the first 5-second swarm tick
/// after spawn).
pub const PEER_TTL_SECS: u64 = 5 * 60;

/// One vigorepo-capable envoy's self-reported info. All fields are
/// received verbatim from the announcer; the listener is
/// responsible only for sanity-checking the shape, not for
/// authenticating identity — mTLS and the fleet CA do that at the
/// swarm peer transport layer.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct PeerEntry {
    /// Canonical lowercased hostname as reported by the peer.
    pub hostname: String,
    /// Unix human usernames present on the peer. Populated from
    /// `/etc/passwd` at the peer's startup — see
    /// [`local_human_users`].
    pub users: Vec<String>,
    /// Tag list inherited from the peer's trait collection
    /// (sourced from matchfile + custom trait collectors).
    pub tags: Vec<String>,
    /// Unix seconds the peer was last observed. Updated on every
    /// refresh; compared against [`PEER_TTL_SECS`] at prune time.
    pub last_seen: u64,
}

/// Shared in-memory peer table. Holds up to a reasonable number of
/// LAN peers — a typical vigorepo-enabled fleet is 3-20 envoys, and
/// the table grows only by DNS-unique hostname, so no explicit size
/// cap is enforced. TTL-based eviction bounds it in practice.
#[derive(Debug, Default)]
pub struct PeerTable {
    inner: RwLock<HashMap<String, PeerEntry>>,
}

impl PeerTable {
    /// Construct an empty peer table.
    pub fn new() -> Self {
        Self::default()
    }

    /// Insert or refresh a peer entry. Hostname is canonicalized
    /// (lowercased + trimmed) before use so the same envoy announcing
    /// under slightly different spellings collapses to one row.
    /// If an entry with the same hostname already exists, it's
    /// replaced — we trust the latest announcement to reflect the
    /// current truth about that peer.
    pub fn update(&self, mut entry: PeerEntry) {
        entry.hostname = canonicalize_hostname(&entry.hostname);
        if entry.hostname.is_empty() {
            return;
        }
        entry.last_seen = unix_seconds_now();
        let mut w = self.inner.write().unwrap_or_else(|e| e.into_inner());
        w.insert(entry.hostname.clone(), entry);
    }

    /// Remove every entry whose `last_seen` is older than
    /// [`PEER_TTL_SECS`]. Call periodically from the reactor tick.
    /// Returns the number of entries removed.
    pub fn prune_expired(&self) -> usize {
        self.prune_expired_at(unix_seconds_now())
    }

    /// Variant of [`prune_expired`] that accepts an explicit `now`,
    /// used by tests to exercise the TTL boundary without waiting.
    pub fn prune_expired_at(&self, now: u64) -> usize {
        let mut w = self.inner.write().unwrap_or_else(|e| e.into_inner());
        let before = w.len();
        w.retain(|_, e| now.saturating_sub(e.last_seen) <= PEER_TTL_SECS);
        before - w.len()
    }

    /// Drop every entry. Called when the vigorepo gate flips off so
    /// a deactivated envoy doesn't hold a stale view of its LAN
    /// neighbors; when the gate flips back on the table repopulates
    /// from the next few multicast announces.
    pub fn clear(&self) {
        let mut w = self.inner.write().unwrap_or_else(|e| e.into_inner());
        w.clear();
    }

    /// Snapshot every current entry (cloned). Used by the
    /// source-side selector in Phase 6 to avoid holding the read
    /// lock across the full selection pipeline.
    pub fn snapshot(&self) -> Vec<PeerEntry> {
        let r = self.inner.read().unwrap_or_else(|e| e.into_inner());
        r.values().cloned().collect()
    }

    /// Number of currently-known peers. Cheap — just a lock + len.
    /// Does not prune.
    pub fn len(&self) -> usize {
        self.inner.read().unwrap_or_else(|e| e.into_inner()).len()
    }

    /// Returns true if the table is empty (no non-pruned peers).
    pub fn is_empty(&self) -> bool {
        self.len() == 0
    }

    /// Filter the peer table against a sidecar's targeting rules +
    /// the repo's `owner` field. Returns the eligible peer list.
    ///
    /// Rules applied (all must pass for a peer to be eligible):
    ///
    /// 1. Hostname must NOT match any `exclude_envoys` glob.
    /// 2. If `include_envoys` is non-empty, hostname must match at
    ///    least one entry. Empty `include_envoys` means "all peers
    ///    allowed by the other rules".
    /// 3. `owner` must be in the peer's `users` list. A repo owned
    ///    by `dan` can only mirror on envoys where `dan` is a local
    ///    account.
    ///
    /// `when:` expression evaluation is **not** performed here. In
    /// LAN mode the filter language is scoped to hostname/tags/user
    /// presence; anything richer would require shipping the full
    /// trait set via multicast, which is out of scope. Sources that
    /// need `when:` expressions over full traits should use the
    /// `cross_subnet_mirrors:` explicit list (Phase 7) instead.
    pub fn filter_candidates(
        &self,
        owner: &str,
        include_envoys: &[String],
        exclude_envoys: &[String],
    ) -> Vec<PeerEntry> {
        let snapshot = self.snapshot();
        snapshot
            .into_iter()
            .filter(|p| peer_matches_rules(p, owner, include_envoys, exclude_envoys))
            .collect()
    }
}

/// Canonicalize a hostname: trim whitespace, lowercase ASCII.
/// Applied to both ingested and locally-computed hostnames so two
/// envoys reporting `Webnode-01` and `webnode-01` resolve to the
/// same table row.
pub fn canonicalize_hostname(s: &str) -> String {
    s.trim().to_ascii_lowercase()
}

/// Core per-peer rule check factored out for unit testing. Does
/// not touch the peer table lock.
fn peer_matches_rules(
    peer: &PeerEntry,
    owner: &str,
    include_envoys: &[String],
    exclude_envoys: &[String],
) -> bool {
    // 1. exclude wins
    for pattern in exclude_envoys {
        if hostname_matches_glob(&peer.hostname, pattern) {
            return false;
        }
    }
    // 2. include whitelist (if any)
    if !include_envoys.is_empty() {
        let ok = include_envoys
            .iter()
            .any(|p| hostname_matches_glob(&peer.hostname, p));
        if !ok {
            return false;
        }
    }
    // 3. owner must exist on the peer
    peer.users.iter().any(|u| u == owner)
}

/// Simple hostname glob match: `*` matches zero or more characters.
/// Deliberately limited — we are not building a full fnmatch.
/// Supports the common idioms:
///
/// - `*.home.arpa` → matches any `foo.home.arpa`
/// - `web-*` → matches any `web-1`, `web-01`, `web-prod`, etc.
/// - `*` → matches everything
/// - `exact-name` → exact match
///
/// Consecutive wildcards collapse; both the pattern and the name
/// are compared ASCII-lowercase.
pub fn hostname_matches_glob(hostname: &str, pattern: &str) -> bool {
    let hn = hostname.to_ascii_lowercase();
    let pat = pattern.to_ascii_lowercase();
    glob_match(&hn, &pat)
}

fn glob_match(s: &str, pat: &str) -> bool {
    // Standard two-pointer wildcard match with backtracking.
    let sb = s.as_bytes();
    let pb = pat.as_bytes();
    let (mut si, mut pi) = (0usize, 0usize);
    let (mut star, mut match_i) = (None::<usize>, 0usize);
    while si < sb.len() {
        if pi < pb.len() && (pb[pi] == sb[si] || pb[pi] == b'?') {
            si += 1;
            pi += 1;
        } else if pi < pb.len() && pb[pi] == b'*' {
            star = Some(pi);
            match_i = si;
            pi += 1;
        } else if let Some(sp) = star {
            pi = sp + 1;
            match_i += 1;
            si = match_i;
        } else {
            return false;
        }
    }
    while pi < pb.len() && pb[pi] == b'*' {
        pi += 1;
    }
    pi == pb.len()
}

/// Current unix seconds. Extracted so tests can stub it.
fn unix_seconds_now() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0)
}

/// Enumerate human Unix users on this host by walking `/etc/passwd`
/// once. Returns usernames whose UID is in `[1000, 65534)` — the
/// conventional human-user range on Linux/macOS. Root, system
/// accounts (bin, daemon, systemd-*), and the nobody user are
/// excluded. Called once at vigorepo startup and cached for the
/// lifetime of the process — adding a user to the host requires a
/// vigo-envoy restart for the announcement to pick it up.
///
/// On non-Unix platforms, or if `/etc/passwd` can't be read, returns
/// an empty list. Vigorepo is Unix-first; Windows receivers have
/// the publisher/receiver plumbing but not the user enumeration.
pub fn local_human_users() -> Vec<String> {
    #[cfg(unix)]
    {
        let contents = match std::fs::read_to_string("/etc/passwd") {
            Ok(s) => s,
            Err(_) => return Vec::new(),
        };
        parse_human_users_from_passwd(&contents)
    }
    #[cfg(not(unix))]
    {
        Vec::new()
    }
}

/// Pure helper: parse the `/etc/passwd` byte slice and return the
/// sorted/deduped human user list. Broken out of [`local_human_users`]
/// so tests can exercise the parse logic without relying on the real
/// `/etc/passwd` (which varies between hosts and may lack a
/// conventional dev user).
pub fn parse_human_users_from_passwd(contents: &str) -> Vec<String> {
    let mut out = Vec::new();
    for line in contents.lines() {
        // Standard passwd format has 7 colon-delimited fields:
        // name:x:uid:gid:gecos:home:shell. Anything shorter is
        // malformed and we skip it — partial lines can't be
        // trusted to produce a valid username.
        let parts: Vec<&str> = line.splitn(7, ':').collect();
        if parts.len() < 7 {
            continue;
        }
        let name = parts[0];
        let uid: u32 = match parts[2].parse() {
            Ok(n) => n,
            Err(_) => continue,
        };
        if (1000..65534).contains(&uid) {
            out.push(name.to_string());
        }
    }
    out.sort();
    out.dedup();
    out
}

/// The local envoy's self-reported info for outgoing announcements.
/// Built once at vigorepo startup from the agent's hostname + the
/// `/etc/passwd` scan + trait-derived tags. Passed to the swarm
/// discovery spawn so the announcer can append a `VIGOREPO:` line
/// to every outgoing multicast.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct LocalInfo {
    pub hostname: String,
    pub users: Vec<String>,
    pub tags: Vec<String>,
}

impl LocalInfo {
    /// Format the line that rides on the swarm multicast. Shape:
    ///
    /// ```text
    /// VIGOREPO:<hostname>:<user1,user2,...>:<tag1,tag2,...>
    /// ```
    ///
    /// No internal colons allowed in usernames or tags (Unix user
    /// names can't contain `:` by convention since it's the passwd
    /// field separator; tags are already constrained by vigorepo
    /// sidecar validation to alphanumerics + `-`/`_`). Empty user
    /// or tag lists serialize as empty substrings.
    pub fn format_announce_line(&self) -> String {
        format!(
            "VIGOREPO:{}:{}:{}",
            self.hostname,
            self.users.join(","),
            self.tags.join(","),
        )
    }

    /// Parse a `VIGOREPO:` line into a [`LocalInfo`]. Returns
    /// `None` if the shape is wrong (wrong prefix, wrong number of
    /// fields, empty hostname). Malformed lines are silently
    /// dropped — the listener must not crash on bad peer input.
    pub fn parse_announce_line(line: &str) -> Option<LocalInfo> {
        let rest = line.strip_prefix("VIGOREPO:")?;
        let mut parts = rest.splitn(3, ':');
        let hostname = parts.next()?.trim().to_string();
        if hostname.is_empty() {
            return None;
        }
        let users_csv = parts.next()?;
        let tags_csv = parts.next()?;
        let users: Vec<String> = users_csv
            .split(',')
            .map(|s| s.trim())
            .filter(|s| !s.is_empty())
            .map(|s| s.to_string())
            .collect();
        let tags: Vec<String> = tags_csv
            .split(',')
            .map(|s| s.trim())
            .filter(|s| !s.is_empty())
            .map(|s| s.to_string())
            .collect();
        Some(LocalInfo {
            hostname: canonicalize_hostname(&hostname),
            users,
            tags,
        })
    }

    /// Convert a parsed `LocalInfo` into a full `PeerEntry` ready
    /// to hand to `PeerTable::update`. `last_seen` is left at 0
    /// because `update` overwrites it with the current time.
    pub fn into_peer_entry(self) -> PeerEntry {
        PeerEntry {
            hostname: self.hostname,
            users: self.users,
            tags: self.tags,
            last_seen: 0,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn entry(hostname: &str, users: &[&str], tags: &[&str]) -> PeerEntry {
        PeerEntry {
            hostname: hostname.to_string(),
            users: users.iter().map(|s| s.to_string()).collect(),
            tags: tags.iter().map(|s| s.to_string()).collect(),
            last_seen: 0,
        }
    }

    // --- canonicalization ---

    #[test]
    fn clear_drops_all_entries() {
        // Invariant: when the vigorepo gate flips off, the reactor
        // calls clear() so a dormant envoy doesn't hold a stale
        // view of its LAN peers. Verify the table is actually
        // empty afterwards.
        let t = PeerTable::new();
        t.update(entry("a", &["dan"], &[]));
        t.update(entry("b", &["ann"], &[]));
        assert_eq!(t.len(), 2);
        t.clear();
        assert!(t.is_empty());
        // Re-add works post-clear, so the table is usable after a
        // disable/enable cycle without any rebuild step.
        t.update(entry("c", &[], &[]));
        assert_eq!(t.len(), 1);
    }

    #[test]
    fn canonicalize_lowercases_and_trims() {
        assert_eq!(canonicalize_hostname(" Webnode-01 "), "webnode-01");
        assert_eq!(canonicalize_hostname("plex.home.arpa"), "plex.home.arpa");
        assert_eq!(canonicalize_hostname("FOO"), "foo");
    }

    // --- hostname glob ---

    #[test]
    fn glob_star_matches_suffix() {
        assert!(hostname_matches_glob("plex.home.arpa", "*.home.arpa"));
        assert!(hostname_matches_glob("danlap.home.arpa", "*.home.arpa"));
        assert!(!hostname_matches_glob("plex.example.com", "*.home.arpa"));
    }

    #[test]
    fn glob_star_matches_prefix() {
        assert!(hostname_matches_glob("web-01", "web-*"));
        assert!(hostname_matches_glob("web-prod", "web-*"));
        assert!(!hostname_matches_glob("api-01", "web-*"));
    }

    #[test]
    fn glob_bare_star_matches_everything() {
        assert!(hostname_matches_glob("anything", "*"));
        assert!(hostname_matches_glob("", "*"));
    }

    #[test]
    fn glob_exact_match() {
        assert!(hostname_matches_glob("danlap", "danlap"));
        assert!(!hostname_matches_glob("danlap", "plex"));
    }

    #[test]
    fn glob_is_case_insensitive() {
        assert!(hostname_matches_glob("DANLAP", "danlap"));
        assert!(hostname_matches_glob("danlap", "DANLAP"));
    }

    // --- update / snapshot / len ---

    #[test]
    fn update_canonicalizes_hostname_and_sets_last_seen() {
        let t = PeerTable::new();
        t.update(entry(" WEBNODE-01 ", &["dan"], &[]));
        let snap = t.snapshot();
        assert_eq!(snap.len(), 1);
        assert_eq!(snap[0].hostname, "webnode-01");
        assert!(snap[0].last_seen > 0);
    }

    #[test]
    fn update_replaces_existing_entry_for_same_hostname() {
        let t = PeerTable::new();
        t.update(entry("plex", &["old"], &[]));
        t.update(entry("plex", &["dan", "alice"], &["mediaserver"]));
        let snap = t.snapshot();
        assert_eq!(snap.len(), 1);
        assert_eq!(snap[0].users, vec!["dan".to_string(), "alice".to_string()]);
        assert_eq!(snap[0].tags, vec!["mediaserver".to_string()]);
    }

    #[test]
    fn update_rejects_empty_hostname() {
        let t = PeerTable::new();
        t.update(entry("   ", &["dan"], &[]));
        assert!(t.is_empty());
    }

    // --- TTL eviction ---

    #[test]
    fn prune_expired_at_drops_stale_entries_only() {
        let t = PeerTable::new();
        t.update(entry("fresh", &["dan"], &[]));
        t.update(entry("stale", &["dan"], &[]));

        // Force the stale entry's last_seen into the past by
        // rewriting it directly through the write lock.
        {
            let mut w = t.inner.write().unwrap();
            if let Some(e) = w.get_mut("stale") {
                e.last_seen = 1;
            }
        }

        // TTL window is 5 min; pick a `now` far past that.
        let now = PEER_TTL_SECS + 10;
        let pruned = t.prune_expired_at(now);
        assert_eq!(pruned, 1);
        let remaining: Vec<String> =
            t.snapshot().into_iter().map(|e| e.hostname).collect();
        assert_eq!(remaining, vec!["fresh".to_string()]);
    }

    #[test]
    fn prune_expired_at_keeps_entries_within_ttl() {
        let t = PeerTable::new();
        t.update(entry("a", &["dan"], &[]));
        // Mimic a recent announcement (1 second ago).
        let recent = unix_seconds_now().saturating_sub(1);
        {
            let mut w = t.inner.write().unwrap();
            if let Some(e) = w.get_mut("a") {
                e.last_seen = recent;
            }
        }
        let pruned = t.prune_expired_at(unix_seconds_now());
        assert_eq!(pruned, 0);
        assert_eq!(t.len(), 1);
    }

    // --- filter ---

    #[test]
    fn filter_requires_owner_in_peer_users() {
        let t = PeerTable::new();
        t.update(entry("p1", &["alice"], &[]));
        t.update(entry("p2", &["alice", "dan"], &[]));
        t.update(entry("p3", &["bob"], &[]));
        let r = t.filter_candidates("dan", &[], &[]);
        let hostnames: Vec<String> = r.into_iter().map(|p| p.hostname).collect();
        assert_eq!(hostnames, vec!["p2".to_string()]);
    }

    #[test]
    fn filter_applies_exclude_globs() {
        let t = PeerTable::new();
        t.update(entry("plex.home.arpa", &["dan"], &[]));
        t.update(entry("raspberry1", &["dan"], &[]));
        t.update(entry("raspberry2", &["dan"], &[]));
        let r = t.filter_candidates("dan", &[], &["raspberry*".to_string()]);
        let hostnames: Vec<String> = r.into_iter().map(|p| p.hostname).collect();
        assert_eq!(hostnames, vec!["plex.home.arpa".to_string()]);
    }

    #[test]
    fn filter_applies_include_globs_as_whitelist() {
        let t = PeerTable::new();
        t.update(entry("plex.home.arpa", &["dan"], &[]));
        t.update(entry("danlap.home.arpa", &["dan"], &[]));
        t.update(entry("offsite.example.net", &["dan"], &[]));
        let r = t.filter_candidates("dan", &["*.home.arpa".to_string()], &[]);
        let mut hostnames: Vec<String> = r.into_iter().map(|p| p.hostname).collect();
        hostnames.sort();
        assert_eq!(
            hostnames,
            vec![
                "danlap.home.arpa".to_string(),
                "plex.home.arpa".to_string(),
            ],
        );
    }

    #[test]
    fn filter_exclude_wins_over_include() {
        let t = PeerTable::new();
        t.update(entry("raspberry-pi.home.arpa", &["dan"], &[]));
        t.update(entry("plex.home.arpa", &["dan"], &[]));
        let r = t.filter_candidates(
            "dan",
            &["*.home.arpa".to_string()],
            &["raspberry*".to_string()],
        );
        let hostnames: Vec<String> = r.into_iter().map(|p| p.hostname).collect();
        assert_eq!(hostnames, vec!["plex.home.arpa".to_string()]);
    }

    // --- parse / format announce line ---

    #[test]
    fn format_and_parse_round_trip() {
        let li = LocalInfo {
            hostname: "danlap".to_string(),
            users: vec!["dan".to_string(), "alice".to_string()],
            tags: vec!["workstation".to_string(), "primary".to_string()],
        };
        let line = li.format_announce_line();
        assert_eq!(
            line,
            "VIGOREPO:danlap:dan,alice:workstation,primary"
        );
        let parsed = LocalInfo::parse_announce_line(&line).unwrap();
        assert_eq!(parsed, li);
    }

    #[test]
    fn parse_handles_empty_users_and_tags() {
        let line = "VIGOREPO:plex::";
        let parsed = LocalInfo::parse_announce_line(line).unwrap();
        assert_eq!(parsed.hostname, "plex");
        assert!(parsed.users.is_empty());
        assert!(parsed.tags.is_empty());
    }

    #[test]
    fn parse_rejects_missing_prefix() {
        assert!(LocalInfo::parse_announce_line("danlap:dan:ws").is_none());
    }

    #[test]
    fn parse_rejects_empty_hostname() {
        assert!(LocalInfo::parse_announce_line("VIGOREPO::dan:ws").is_none());
    }

    #[test]
    fn parse_rejects_too_few_fields() {
        assert!(LocalInfo::parse_announce_line("VIGOREPO:danlap").is_none());
        assert!(LocalInfo::parse_announce_line("VIGOREPO:danlap:dan").is_none());
    }

    #[test]
    fn parse_canonicalizes_hostname() {
        let parsed = LocalInfo::parse_announce_line("VIGOREPO:DANLAP::").unwrap();
        assert_eq!(parsed.hostname, "danlap");
    }

    // --- human users ---

    #[test]
    fn parse_human_users_from_passwd_skips_root_and_system() {
        let passwd = "\
root:x:0:0:root:/root:/bin/bash
daemon:x:1:1:daemon:/usr/sbin:/usr/sbin/nologin
systemd-network:x:100:102:systemd:/run/systemd:/usr/sbin/nologin
dan:x:1000:1000:Dan:/home/dan:/bin/bash
alice:x:1001:1001:Alice:/home/alice:/bin/zsh
nobody:x:65534:65534:nobody:/nonexistent:/usr/sbin/nologin
";
        let users = parse_human_users_from_passwd(passwd);
        assert_eq!(users, vec!["alice".to_string(), "dan".to_string()]);
    }

    #[test]
    fn parse_human_users_from_passwd_handles_malformed_lines() {
        let passwd = "\
valid:x:1000:1000::/home/valid:/bin/bash
garbage line without colons
short:x:1001
too:few:fields
";
        let users = parse_human_users_from_passwd(passwd);
        assert_eq!(users, vec!["valid".to_string()]);
    }

    #[test]
    fn parse_human_users_from_passwd_dedups() {
        let passwd = "\
dan:x:1000:1000::/home/dan:/bin/bash
dan:x:1001:1000::/home/dan2:/bin/bash
";
        let users = parse_human_users_from_passwd(passwd);
        assert_eq!(users, vec!["dan".to_string()]);
    }
}
