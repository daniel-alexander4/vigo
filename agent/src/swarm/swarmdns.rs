// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Swarm-DNS — let each agent answer LAN name lookups for its swarm
//! peers by maintaining a delimited block in `/etc/hosts` mapping
//! every announced peer hostname to its LAN IP.
//!
//! *Additive* to the system resolver: lines outside the managed block
//! are never touched, and any name the swarm doesn't hold still
//! resolves via the OS resolver / DNS as before. It exists for the
//! case the swarm is built for — a LAN where DNS doesn't authoritatively
//! own the local domain (a home lab whose DHCP server bias-races an
//! upstream resolver, a corp segment without reverse zones) — so
//! `vigo swarm puddle join --from <host>`, `ssh <host>`, `git`, and
//! friends resolve fleet hostnames regardless.
//!
//! **Opt-in.** Off unless the operator sets `swarm.swarmdns: true` in
//! `server.yaml` (and the envoy's hostname matches `swarm.enabled`).
//! The server emits `swarm_swarmdns_enabled` in `PolicyBundle.vars_json`
//! only when both hold; the agent treats anything else — absent,
//! `false`, no policy bundle yet — as off. That's the *opposite* of
//! the fail-open default the content subsystems use, on purpose:
//! editing `/etc/hosts` is an operator decision, not something a
//! fresh-enrolled envoy does on its own. Flipping it back to `false`
//! removes the managed block on the next reactor tick.
//!
//! Data source: `HOST:<hostname>` lines on the swarm multicast
//! ([`crate::swarm::discovery`]). Every envoy in the swarm emits one
//! in its announcement preamble; receivers feed `(hostname, src_ip,
//! now)` into the [`HostMap`] singleton here. Stale entries age out
//! after [`HOST_STALE_SECS`], matching `swarmgit::project_peers`.

use std::collections::HashMap;
use std::io;
use std::net::IpAddr;
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex, OnceLock};

use tokio_util::sync::CancellationToken;

use crate::state::StateStore;

// ── HostMap: hostname → (ip, last_seen) index ─────────────────────

/// Stale threshold for host-index entries. Matches
/// `swarmgit::project_peers::STALE_SECS` — five minutes covers the
/// ~30 s announce cadence ten times over.
pub const HOST_STALE_SECS: i64 = 5 * 60;

/// One peer's view from the host index.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct HostEntry {
    pub host: String,
    pub addr: IpAddr,
    pub last_seen: i64,
}

/// In-memory `hostname → entry` index, populated from `HOST:` lines.
#[derive(Default)]
pub struct HostMap {
    inner: Mutex<HashMap<String, HostEntry>>,
}

impl HostMap {
    pub fn new() -> Self {
        Self::default()
    }

    /// Record (or refresh) `host`'s LAN address. Returns `true` if the
    /// entry was new or its address changed. Empty hostnames ignored.
    pub fn record(&self, host: &str, addr: IpAddr, now: i64) -> bool {
        let host = host.trim();
        if host.is_empty() {
            return false;
        }
        let mut guard = self.inner.lock().unwrap_or_else(|p| p.into_inner());
        match guard.get_mut(host) {
            Some(e) => {
                let changed = e.addr != addr;
                e.addr = addr;
                e.last_seen = now;
                changed
            }
            None => {
                guard.insert(
                    host.to_string(),
                    HostEntry {
                        host: host.to_string(),
                        addr,
                        last_seen: now,
                    },
                );
                true
            }
        }
    }

    /// Every non-stale entry at `now`, sorted by hostname (so the
    /// rendered `/etc/hosts` block is deterministic).
    pub fn entries(&self, now: i64) -> Vec<HostEntry> {
        let guard = self.inner.lock().unwrap_or_else(|p| p.into_inner());
        let cutoff = now - HOST_STALE_SECS;
        let mut v: Vec<HostEntry> = guard
            .values()
            .filter(|e| e.last_seen >= cutoff)
            .cloned()
            .collect();
        v.sort_by(|a, b| a.host.cmp(&b.host));
        v
    }

    /// Drop entries older than `now - HOST_STALE_SECS`. Returns count removed.
    pub fn prune_stale(&self, now: i64) -> usize {
        let mut guard = self.inner.lock().unwrap_or_else(|p| p.into_inner());
        let cutoff = now - HOST_STALE_SECS;
        let before = guard.len();
        guard.retain(|_, e| e.last_seen >= cutoff);
        before - guard.len()
    }

    pub fn clear(&self) {
        self.inner.lock().unwrap_or_else(|p| p.into_inner()).clear();
    }

    pub fn len(&self) -> usize {
        self.inner.lock().unwrap_or_else(|p| p.into_inner()).len()
    }

    pub fn is_empty(&self) -> bool {
        self.len() == 0
    }
}

static HOST_MAP: OnceLock<Arc<HostMap>> = OnceLock::new();

/// Initialize the process-wide [`HostMap`]. Called from
/// `discovery::spawn` and `swarmdns::spawn`; idempotent.
pub fn init_host_map() -> Arc<HostMap> {
    HOST_MAP.get_or_init(|| Arc::new(HostMap::new())).clone()
}

/// The process-wide [`HostMap`], if [`init_host_map`] has run.
pub fn host_map() -> Option<Arc<HostMap>> {
    HOST_MAP.get().cloned()
}

/// Record a peer hostname → address into the global index. No-op when
/// the index hasn't been initialized (tests that don't run discovery).
pub fn record_host_global(host: &str, addr: IpAddr, now: i64) {
    if let Some(m) = host_map() {
        m.record(host, addr, now);
    }
}

// ── /etc/hosts managed block ──────────────────────────────────────

fn hosts_path() -> PathBuf {
    if let Some(p) = std::env::var_os("VIGO_SWARMDNS_HOSTS_FILE") {
        return PathBuf::from(p);
    }
    PathBuf::from("/etc/hosts")
}

const BLOCK_BEGIN: &str = "# BEGIN vigo swarm peers — managed by the vigo agent; do not edit";
const BLOCK_END: &str = "# END vigo swarm peers";

/// Render the managed block body (the lines *between* the markers,
/// markers excluded) for `entries`. Loopback addresses are skipped
/// defensively — they're never announced, but a managed `/etc/hosts`
/// line shadowing `localhost` would be a footgun. Empty when there's
/// nothing to publish.
fn render_block_body(entries: &[HostEntry]) -> String {
    let mut out = String::new();
    for e in entries {
        if e.addr.is_loopback() {
            continue;
        }
        out.push_str(&format!("{}\t{}\n", e.addr, e.host));
    }
    out
}

/// Reconstruct file contents with the managed block set to `body`
/// (markers added around it). An empty `body` removes the block. Lines
/// outside the markers are preserved verbatim, in order.
fn rebuild(current: &str, body: &str) -> String {
    let kept = strip_managed_block(current);
    if body.is_empty() {
        return kept;
    }
    let mut next = kept;
    if !next.is_empty() && !next.ends_with('\n') {
        next.push('\n');
    }
    next.push_str(BLOCK_BEGIN);
    next.push('\n');
    next.push_str(body);
    if !body.ends_with('\n') {
        next.push('\n');
    }
    next.push_str(BLOCK_END);
    next.push('\n');
    next
}

/// Return `content` with any `BLOCK_BEGIN..BLOCK_END` region
/// (inclusive) removed. If no begin marker is present, returns the
/// input unchanged (so a file with no managed block is never
/// rewritten just for newline normalization). Tolerates a missing end
/// marker (drops from the begin marker to EOF) and multiple blocks.
fn strip_managed_block(content: &str) -> String {
    if !content.contains(BLOCK_BEGIN) {
        return content.to_string();
    }
    let mut out = String::new();
    let mut in_block = false;
    for line in content.lines() {
        let trimmed = line.trim_end();
        if !in_block && trimmed == BLOCK_BEGIN {
            in_block = true;
            continue;
        }
        if in_block {
            if trimmed == BLOCK_END {
                in_block = false;
            }
            continue;
        }
        out.push_str(line);
        out.push('\n');
    }
    out
}

/// Rewrite `/etc/hosts` so the managed block contains exactly `body`
/// (empty ⇒ block removed). Atomic (temp file + rename in the same
/// dir), preserving the file's current mode. Returns `Ok(true)` if
/// the file changed, `Ok(false)` if it was already in the desired
/// state (the common steady-state case — no write).
fn apply_block(body: &str) -> io::Result<bool> {
    let path = hosts_path();
    let current = match std::fs::read_to_string(&path) {
        Ok(s) => s,
        Err(e) if e.kind() == io::ErrorKind::NotFound => String::new(),
        Err(e) => return Err(e),
    };
    let desired = rebuild(&current, body);
    if desired == current {
        return Ok(false);
    }
    write_atomic(&path, &desired)?;
    Ok(true)
}

fn write_atomic(path: &Path, contents: &str) -> io::Result<()> {
    let dir = path.parent().unwrap_or_else(|| Path::new("."));
    let tmp = dir.join(format!(".vigo-hosts.{:08x}.tmp", rand::random::<u32>()));
    {
        use std::io::Write;
        let mut f = std::fs::OpenOptions::new()
            .create(true)
            .write(true)
            .truncate(true)
            .open(&tmp)?;
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let mode = std::fs::metadata(path)
                .map(|m| m.permissions().mode() & 0o777)
                .unwrap_or(0o644);
            let _ = f.set_permissions(std::fs::Permissions::from_mode(mode));
        }
        f.write_all(contents.as_bytes())?;
        f.sync_all()?;
    }
    std::fs::rename(&tmp, path)
}

// ── reactor ───────────────────────────────────────────────────────

/// How often the swarmdns reactor reconciles `/etc/hosts` with the
/// host index (and prunes stale index entries). Off the hot path; a
/// minute is plenty given the ~30 s announce cadence.
const SWARMDNS_INTERVAL: std::time::Duration = std::time::Duration::from_secs(60);

/// Spawn the swarmdns reactor. Always spawned — each tick it reads
/// `swarm_swarmdns_enabled` from the cached policy bundle (via
/// [`crate::swarm::gates::swarmdns_active`]) and either rewrites the
/// managed `/etc/hosts` block from the host index or, when disabled,
/// removes it. The host index is populated independently by
/// `discovery`'s listener parsing `HOST:` lines.
pub fn spawn(state_store: Option<Arc<StateStore>>, cancel: CancellationToken) {
    let _ = init_host_map();
    tokio::spawn(async move {
        let mut tick = tokio::time::interval(SWARMDNS_INTERVAL);
        tick.tick().await; // skip the immediate first tick
        loop {
            tokio::select! {
                _ = cancel.cancelled() => return,
                _ = tick.tick() => {
                    reconcile_once(state_store.as_deref());
                }
            }
        }
    });
}

/// One reconcile pass: render the desired managed-block body (from the
/// host index when swarmdns is enabled, empty otherwise) and apply it.
/// Pulled out so tests can drive it without the timer.
fn reconcile_once(state_store: Option<&StateStore>) {
    let now = unix_secs();
    let enabled = crate::swarm::gates::swarmdns_active(state_store);
    let body = if enabled {
        match host_map() {
            Some(m) => {
                m.prune_stale(now);
                render_block_body(&m.entries(now))
            }
            None => String::new(),
        }
    } else {
        String::new() // disabled ⇒ remove the managed block
    };
    match apply_block(&body) {
        Ok(true) => tracing::info!(
            enabled,
            entries = body.lines().count(),
            "swarmdns: updated /etc/hosts managed block"
        ),
        Ok(false) => {}
        Err(e) => tracing::warn!(error = %e, "swarmdns: failed to update /etc/hosts"),
    }
}

fn unix_secs() -> i64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs() as i64)
        .unwrap_or(0)
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::net::Ipv4Addr;

    #[test]
    fn host_map_record_entries_prune() {
        let m = HostMap::new();
        assert!(m.record("danlap", IpAddr::V4(Ipv4Addr::new(192, 168, 1, 2)), 1000));
        assert!(m.record("annlap", IpAddr::V4(Ipv4Addr::new(192, 168, 1, 74)), 1000));
        // Same addr → no change.
        assert!(!m.record("danlap", IpAddr::V4(Ipv4Addr::new(192, 168, 1, 2)), 1010));
        // New addr → change.
        assert!(m.record("danlap", IpAddr::V4(Ipv4Addr::new(192, 168, 1, 3)), 1020));
        // Empty hostname ignored.
        assert!(!m.record("   ", IpAddr::V4(Ipv4Addr::new(10, 0, 0, 1)), 1020));

        let now = 1020;
        let e = m.entries(now);
        assert_eq!(e.len(), 2);
        assert_eq!(e[0].host, "annlap"); // sorted
        assert_eq!(e[1].host, "danlap");

        // Prune: annlap last_seen=1000, danlap last_seen=1020. At
        // now=1000+HOST_STALE_SECS+1, annlap is stale.
        let pruned = m.prune_stale(1000 + HOST_STALE_SECS + 1);
        assert_eq!(pruned, 1);
        assert_eq!(m.len(), 1);
        assert_eq!(m.entries(1000 + HOST_STALE_SECS + 1)[0].host, "danlap");

        m.clear();
        assert!(m.is_empty());
    }

    #[test]
    fn render_block_body_sorted_and_skips_loopback() {
        let entries = vec![
            HostEntry { host: "zeta".into(), addr: IpAddr::V4(Ipv4Addr::new(192, 168, 1, 9)), last_seen: 0 },
            HostEntry { host: "loop".into(), addr: IpAddr::V4(Ipv4Addr::new(127, 0, 0, 5)), last_seen: 0 },
            HostEntry { host: "alpha".into(), addr: IpAddr::V4(Ipv4Addr::new(192, 168, 1, 2)), last_seen: 0 },
        ];
        // render_block_body trusts the caller's order; HostMap::entries
        // is what sorts. Feed it sorted to match real usage.
        let mut sorted = entries.clone();
        sorted.sort_by(|a, b| a.host.cmp(&b.host));
        let body = render_block_body(&sorted);
        assert_eq!(body, "192.168.1.2\talpha\n192.168.1.9\tzeta\n");
        assert!(!body.contains("loop"));
        assert!(!body.contains("127.0.0.5"));
    }

    #[test]
    fn strip_managed_block_leaves_unmanaged_content_untouched() {
        let plain = "127.0.0.1 localhost\n192.168.1.50 printer\n";
        assert_eq!(strip_managed_block(plain), plain);
        // No trailing newline: must not gain one.
        let no_nl = "127.0.0.1 localhost";
        assert_eq!(strip_managed_block(no_nl), no_nl);
    }

    #[test]
    fn strip_managed_block_removes_the_region() {
        let with_block = format!(
            "127.0.0.1 localhost\n{}\n10.0.0.1\tfoo\n{}\n192.168.1.50 printer\n",
            BLOCK_BEGIN, BLOCK_END
        );
        assert_eq!(
            strip_managed_block(&with_block),
            "127.0.0.1 localhost\n192.168.1.50 printer\n"
        );
        // Missing end marker → drop to EOF.
        let unterminated = format!("127.0.0.1 localhost\n{}\n10.0.0.1\tfoo\n", BLOCK_BEGIN);
        assert_eq!(strip_managed_block(&unterminated), "127.0.0.1 localhost\n");
    }

    #[test]
    fn rebuild_adds_updates_and_removes_block() {
        let base = "127.0.0.1 localhost\n192.168.1.50 printer\n";
        // Add.
        let added = rebuild(base, "192.168.1.2\tdanlap\n");
        assert_eq!(
            added,
            format!("{}{}\n192.168.1.2\tdanlap\n{}\n", base, BLOCK_BEGIN, BLOCK_END)
        );
        // Update in place — re-running with a different body replaces it.
        let updated = rebuild(&added, "192.168.1.3\tdanlap\n");
        assert_eq!(
            updated,
            format!("{}{}\n192.168.1.3\tdanlap\n{}\n", base, BLOCK_BEGIN, BLOCK_END)
        );
        // Remove — empty body drops the block, restoring the base.
        assert_eq!(rebuild(&updated, ""), base);
        // Idempotent: removing when there's no block is a no-op.
        assert_eq!(rebuild(base, ""), base);
    }

    #[test]
    fn apply_block_atomic_write_roundtrip() {
        let dir = tempfile::tempdir().unwrap();
        let hosts = dir.path().join("hosts");
        std::fs::write(&hosts, "127.0.0.1 localhost\n").unwrap();
        std::env::set_var("VIGO_SWARMDNS_HOSTS_FILE", &hosts);

        // First write — block added, file changed.
        assert!(apply_block("192.168.1.2\tdanlap\n").unwrap());
        let c = std::fs::read_to_string(&hosts).unwrap();
        assert!(c.starts_with("127.0.0.1 localhost\n"));
        assert!(c.contains("192.168.1.2\tdanlap"));
        assert!(c.contains(BLOCK_BEGIN));

        // Re-apply same body — no change.
        assert!(!apply_block("192.168.1.2\tdanlap\n").unwrap());

        // Remove — block gone, original content intact.
        assert!(apply_block("").unwrap());
        assert_eq!(std::fs::read_to_string(&hosts).unwrap(), "127.0.0.1 localhost\n");
        // Re-remove — no change.
        assert!(!apply_block("").unwrap());

        std::env::remove_var("VIGO_SWARMDNS_HOSTS_FILE");
    }
}
