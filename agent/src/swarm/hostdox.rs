// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! hostdox — let each envoy resolve its swarm peers' hostnames to
//! their LAN addresses by maintaining a delimited block in `/etc/hosts`
//! mapping every announced peer hostname to its LAN IP.
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
//! **Opt-in.** Off unless the operator sets `swarm.hostdox: true`
//! in `server.yaml` (and the envoy's hostname matches `swarm.enabled`).
//! The server emits `swarm_hostdox_enabled` in
//! `PolicyBundle.vars_json` only when both hold; the agent treats
//! anything else — absent, `false`, no policy bundle yet — as off.
//! That's the *opposite* of the fail-open default the content
//! subsystems use, on purpose: editing `/etc/hosts` is an operator
//! decision, not something a fresh-enrolled envoy does on its own.
//! Flipping it back to `false` removes the managed block on the next
//! reactor tick.
//!
//! Data source: `HOST:<hostname>` lines on the swarm multicast
//! ([`crate::swarm::discovery`]). Every envoy in the swarm emits one
//! in its announcement preamble; receivers feed `(hostname, src_ip,
//! now)` into the [`HostMap`] singleton here. Stale entries age out
//! after [`HOST_STALE_SECS`], matching `gitback::project_peers`.

use std::collections::HashMap;
use std::io;
use std::net::IpAddr;
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex, OnceLock};
use std::time::Duration;

use notify::Watcher;
use tokio_util::sync::CancellationToken;

use crate::state::StateStore;

// ── HostMap: hostname → (ip, last_seen) index ─────────────────────

/// Stale threshold for host-index entries. Matches
/// `gitback::project_peers::STALE_SECS` — five minutes covers the
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
/// `discovery::spawn` and `hostdox::spawn`; idempotent.
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
    if let Some(p) = std::env::var_os("VIGO_HOSTDOX_HOSTS_FILE") {
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

/// Read the current managed-block body lines from `/etc/hosts` as
/// `(ip, hostname)` pairs (whitespace-split, first two columns). Empty
/// when there's no managed block or the file is missing. Used by
/// `vigo swarm hostdox status` — the CLI runs in a separate
/// process from the agent so it can't read the in-memory [`HostMap`];
/// the managed block is the on-disk reflection of the last reactor tick.
pub fn managed_block_entries() -> Vec<(String, String)> {
    let content = match std::fs::read_to_string(hosts_path()) {
        Ok(s) => s,
        Err(_) => return Vec::new(),
    };
    let mut out = Vec::new();
    let mut in_block = false;
    for line in content.lines() {
        let trimmed = line.trim();
        if !in_block {
            if trimmed == BLOCK_BEGIN {
                in_block = true;
            }
            continue;
        }
        if trimmed == BLOCK_END {
            break;
        }
        if trimmed.is_empty() || trimmed.starts_with('#') {
            continue;
        }
        let mut cols = trimmed.split_whitespace();
        if let (Some(ip), Some(host)) = (cols.next(), cols.next()) {
            out.push((ip.to_string(), host.to_string()));
        }
    }
    out
}

/// Whether the `/etc/hosts` managed block markers are currently
/// present. (`managed_block_entries().is_empty()` can't distinguish
/// "no block" from "block exists but holds nothing" — but the reactor
/// never leaves an empty block, so in practice they coincide.)
pub fn managed_block_present() -> bool {
    std::fs::read_to_string(hosts_path())
        .map(|c| c.contains(BLOCK_BEGIN))
        .unwrap_or(false)
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

/// Backstop interval for the hostdox reactor. The fast path is an
/// inotify/kqueue watch on `/etc/hosts` (see [`spawn`]) that re-renders
/// the managed block within [`DEBOUNCE`] of any out-of-band rewrite — so
/// a config-management run that clobbers the file is repaired in ~1 s,
/// not after a minute. This timer just guarantees the block is rebuilt
/// (and stale host-index entries pruned) even if the watch ever misses
/// an event or couldn't be created. Off the hot path.
const HOSTDOX_INTERVAL: Duration = Duration::from_secs(60);

/// Coalescing window for filesystem events on `/etc/hosts`. An external
/// rewrite is normally temp-create + rename-over (+ maybe a chmod) —
/// several events within a few milliseconds; the reactor waits this long
/// after the first before reconciling so the burst collapses into a
/// single rewrite.
const DEBOUNCE: Duration = Duration::from_millis(750);

/// Spawn the hostdox reactor. Always spawned. Reconciles the
/// managed `/etc/hosts` block on two triggers:
///
/// - **inotify/kqueue watch on `/etc/hosts`** (the fast path) — fires
///   when anything *else* rewrites the file (a config-management module,
///   a hand edit), so the managed block is restored within [`DEBOUNCE`]
///   rather than only on the timer. We watch the *directory*, not the
///   file inode, because an external rewrite is a temp-file +
///   rename-over and a file-level watch goes deaf after the first
///   replace. If the watcher can't be created the reactor degrades to
///   timer-only — still correct, just slower to repair a clobber.
/// - **[`HOSTDOX_INTERVAL`] timer** (the backstop) — guarantees
///   eventual convergence and prunes stale host-index entries.
///
/// Each reconcile reads `swarm_hostdox_enabled` from the cached
/// policy bundle (via [`crate::swarm::gates::hostdox_active`]) and
/// either rewrites the block from the host index or, when disabled,
/// removes it. The host index is populated independently by `discovery`'s
/// listener parsing `HOST:` lines. hostdox's own writes (atomic
/// temp+rename in the same dir) trip the watch too, but the follow-up
/// reconcile is a no-op (`apply_block` finds the file already in the
/// desired state and doesn't write), so there's no feedback loop.
pub fn spawn(state_store: Option<Arc<StateStore>>, cancel: CancellationToken) {
    let _ = init_host_map();
    tokio::spawn(async move {
        let hosts_file = hosts_path();
        let watch_dir = hosts_file
            .parent()
            .map(Path::to_path_buf)
            .unwrap_or_else(|| PathBuf::from("/etc"));
        let (ev_tx, mut ev_rx) = tokio::sync::mpsc::channel::<()>(8);

        // The watcher owns its callback (which holds `ev_tx`); it must
        // outlive the loop, so bind it for the duration of the task.
        // `None` ⇒ no watcher ⇒ `ev_rx` is closed ⇒ the recv arms below
        // stay disabled ⇒ timer-only operation.
        let _watcher: Option<notify::RecommendedWatcher> =
            match notify::recommended_watcher(move |res: notify::Result<notify::Event>| {
                if let Ok(ev) = res {
                    if event_touches(&ev, &hosts_file) {
                        let _ = ev_tx.try_send(());
                    }
                }
            }) {
                Ok(mut w) => match w.watch(&watch_dir, notify::RecursiveMode::NonRecursive) {
                    Ok(()) => Some(w),
                    Err(e) => {
                        tracing::warn!(
                            dir = %watch_dir.display(), error = %e,
                            "hostdox: cannot watch directory for /etc/hosts changes — timer-only"
                        );
                        None
                    }
                },
                Err(e) => {
                    tracing::warn!(
                        error = %e,
                        "hostdox: cannot create filesystem watcher — timer-only"
                    );
                    None
                }
            };

        let mut tick = tokio::time::interval(HOSTDOX_INTERVAL);
        tick.tick().await; // skip the immediate first tick
        loop {
            tokio::select! {
                _ = cancel.cancelled() => return,
                _ = tick.tick() => {
                    reconcile_once(state_store.as_deref());
                }
                Some(()) = ev_rx.recv() => {
                    // Coalesce the burst (temp-create + rename + chmod)
                    // into one pass — each fresh event resets the wait.
                    loop {
                        tokio::select! {
                            _ = tokio::time::sleep(DEBOUNCE) => break,
                            Some(()) = ev_rx.recv() => continue,
                        }
                    }
                    tracing::debug!(
                        "hostdox: /etc/hosts changed out of band — reconciling managed block"
                    );
                    reconcile_once(state_store.as_deref());
                }
            }
        }
    });
}

/// True if `ev` is a non-`Access` change to `hosts_file` — a
/// create / modify / remove of the target path. Temp-file churn in the
/// same directory (a different path) and read-only `Access` events are
/// ignored; hostdox's own rename-over of `/etc/hosts` does match
/// (harmless — the follow-up reconcile is a no-op, see [`spawn`]).
fn event_touches(ev: &notify::Event, hosts_file: &Path) -> bool {
    if matches!(ev.kind, notify::EventKind::Access(_)) {
        return false;
    }
    ev.paths.iter().any(|p| p == hosts_file)
}

/// One reconcile pass: render the desired managed-block body (from the
/// host index when hostdox is enabled, empty otherwise) and apply
/// it. Pulled out so tests can drive it without the timer.
fn reconcile_once(state_store: Option<&StateStore>) {
    let now = unix_secs();
    let enabled = crate::swarm::gates::hostdox_active(state_store);
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
            "hostdox: updated /etc/hosts managed block"
        ),
        Ok(false) => {}
        Err(e) => tracing::warn!(error = %e, "hostdox: failed to update /etc/hosts"),
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
        std::env::set_var("VIGO_HOSTDOX_HOSTS_FILE", &hosts);

        // First write — block added, file changed.
        assert!(apply_block("192.168.1.2\tdanlap\n").unwrap());
        let c = std::fs::read_to_string(&hosts).unwrap();
        assert!(c.starts_with("127.0.0.1 localhost\n"));
        assert!(c.contains("192.168.1.2\tdanlap"));
        assert!(c.contains(BLOCK_BEGIN));

        // Re-apply same body — no change.
        assert!(!apply_block("192.168.1.2\tdanlap\n").unwrap());

        // Read it back via the CLI-facing helpers.
        assert!(managed_block_present());
        let entries = managed_block_entries();
        assert_eq!(entries, vec![("192.168.1.2".to_string(), "danlap".to_string())]);

        // Remove — block gone, original content intact, helpers report empty.
        assert!(apply_block("").unwrap());
        assert_eq!(std::fs::read_to_string(&hosts).unwrap(), "127.0.0.1 localhost\n");
        assert!(!managed_block_present());
        assert!(managed_block_entries().is_empty());
        // Re-remove — no change.
        assert!(!apply_block("").unwrap());

        std::env::remove_var("VIGO_HOSTDOX_HOSTS_FILE");
    }

    #[test]
    fn event_touches_matches_target_path_only() {
        use notify::event::{AccessKind, CreateKind, ModifyKind, RemoveKind};
        use notify::{Event, EventKind};

        let hosts = Path::new("/etc/hosts");
        let mk = |kind: EventKind, path: &str| Event {
            kind,
            paths: vec![PathBuf::from(path)],
            attrs: Default::default(),
        };

        // create / modify / remove of /etc/hosts (incl. an external
        // rename-over) → reconcile.
        assert!(event_touches(&mk(EventKind::Create(CreateKind::File), "/etc/hosts"), hosts));
        assert!(event_touches(&mk(EventKind::Modify(ModifyKind::Any), "/etc/hosts"), hosts));
        assert!(event_touches(&mk(EventKind::Remove(RemoveKind::File), "/etc/hosts"), hosts));

        // Our own temp file churning in the same dir → ignored (different path).
        assert!(!event_touches(
            &mk(EventKind::Create(CreateKind::File), "/etc/.vigo-hosts.deadbeef.tmp"),
            hosts
        ));
        // An unrelated file in /etc → ignored.
        assert!(!event_touches(&mk(EventKind::Modify(ModifyKind::Any), "/etc/resolv.conf"), hosts));
        // A read-only access event on /etc/hosts → ignored (a name lookup
        // touched the file; nothing to reconcile).
        assert!(!event_touches(&mk(EventKind::Access(AccessKind::Read), "/etc/hosts"), hosts));
    }
}
