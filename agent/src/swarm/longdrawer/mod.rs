// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Longdrawer — per-user LAN-local directory sync across machines.
//!
//! Each user's `~/longdrawer/` directory is mirrored to the same user's
//! `~/longdrawer/` on every other machine on the LAN. File additions and
//! modifications propagate; deletions propagate via tombstones.
//!
//! ### Design
//!
//! - **Pure P2P**, LAN-only. No server involvement. Discovery via multicast
//!   on `224.0.0.43:1532`, separate from swarm's 224.0.0.42:1531.
//! - **Content transport** piggybacks on the existing swarm peer HTTPS
//!   server (port 1531, mTLS). New route: `/longdrawer/{user}/{filename}`.
//! - **State** per user is kept under `/var/lib/vigo/swarm/longdrawer/<user>.json`:
//!   a file snapshot (name → sha256/mtime/size) plus tombstones
//!   (name → deletion_time).
//! - **Reconciliation** is last-write-wins by mtime, with tombstones that
//!   compare against file mtime: a newer deletion overrides a file, a file
//!   whose mtime is newer than a deletion wins (resurrection).
//! - **Tombstone GC** drops tombstones older than 30 days; after that, a
//!   long-offline machine's file will resurrect.
//!
//! ### Architecture
//!
//! - `state` — persistent snapshot + tombstone types
//! - `scanner` — scan a user's directory, compute file entries, detect deletions
//! - `discovery` — UDP multicast announcer + listener
//! - `reconciler` — pure logic: diff local state against an incoming announcement
//! - `applier` — fetch files from peers via HTTPS, write with correct ownership
//! - `server` — serve file content to other agents on the existing peer HTTPS port
//!
//! The main `spawn()` task runs a per-cycle scan/announce loop plus a listener
//! that reconciles incoming announcements.

pub mod applier;
pub mod config;
pub mod discovery;
pub mod gate;
pub mod reconciler;
pub mod scanner;
pub mod server;
pub mod state;

pub use gate::Gate;

use std::collections::BTreeMap;
use std::path::PathBuf;
use std::sync::Arc;
use std::time::Duration;

use anyhow::Context;
use tokio_util::sync::CancellationToken;
use tracing::{debug, info, warn};

use crate::swarm::peer_client::build_peer_agent;
use crate::update::TlsCerts;

use discovery::{spawn as spawn_discovery, IncomingAnnouncement};
use reconciler::{merge_tombstones, reconcile, Action};
use scanner::{list_home_dirs, merge_scan, now_secs, scan_user_dir};
use state::{Announcement, UserState};

/// How often to scan and announce. Short enough to feel responsive; long
/// enough to not chew the LAN.
const CYCLE_INTERVAL: Duration = Duration::from_secs(30);

/// Drop tombstones older than this. After this age, a long-offline machine
/// that still has the file will resurrect it on its next announce. Tuning
/// knob for accepting zombie files vs. unbounded tombstone growth.
const TOMBSTONE_MAX_AGE_SECS: i64 = 30 * 24 * 3600;

/// The swarm peer server port — longdrawer fetches file content from this port.
const PEER_PORT: u16 = crate::swarm::server::DEFAULT_PORT;

/// Spawn the longdrawer daemon. Runs until the cancellation token fires.
///
/// Requires mTLS certs so it can fetch files from peers over HTTPS. If
/// certs aren't available, longdrawer silently does nothing (same pattern
/// as the swarm peer server).
pub fn spawn(tls: Option<TlsCerts>, cancel: CancellationToken) {
    let tls = match tls {
        Some(t) => t,
        None => {
            debug!("longdrawer: no TLS certs, skipping (will not participate in sync)");
            return;
        }
    };

    // Get a stable machine identifier. Hostname is good enough for LAN use;
    // if two machines share a hostname, longdrawer will misbehave — document
    // it, don't try to solve it.
    let machine = std::fs::read_to_string("/etc/hostname")
        .unwrap_or_else(|_| "unknown".to_string())
        .trim()
        .to_string();

    let (out_tx, in_tx, in_rx) = spawn_discovery(cancel.clone());
    // Publish the inbound sender so the HTTPS `/longdrawer/announce` handler
    // can feed cross-subnet announcements into the same reconciler path.
    let _ = INCOMING_ANNOUNCE.set(in_tx);
    let (extra_tx, extra_rx) = tokio::sync::mpsc::unbounded_channel::<ExtraPeerJob>();
    // Spawn the extra-peer pusher so unicast announces run off the scan hot path.
    tokio::spawn(run_extra_peer_pusher(tls.clone(), extra_rx, cancel.clone()));
    tokio::spawn(run(machine, tls, out_tx, in_rx, extra_tx, cancel));
}

/// Inbound-announcement sender, published when the longdrawer daemon starts.
/// The HTTPS `/longdrawer/announce` handler uses this to feed unicast
/// announcements from `cross_subnet_peers` into the same reconciler path that
/// multicast discovery uses. `None` before longdrawer has spawned.
static INCOMING_ANNOUNCE: std::sync::OnceLock<tokio::sync::mpsc::Sender<IncomingAnnouncement>> =
    std::sync::OnceLock::new();

/// Deliver a unicast announcement into the longdrawer reconciler. Returns
/// true on success, false if longdrawer isn't running or the channel is
/// full. Used by the `POST /longdrawer/announce` HTTPS handler.
pub fn submit_announcement(from: std::net::SocketAddr, announcement: Announcement) -> bool {
    match INCOMING_ANNOUNCE.get() {
        Some(tx) => tx
            .try_send(IncomingAnnouncement { from, announcement })
            .is_ok(),
        None => false,
    }
}

async fn run(
    machine: String,
    tls: TlsCerts,
    out_tx: tokio::sync::mpsc::Sender<Announcement>,
    mut in_rx: tokio::sync::mpsc::Receiver<IncomingAnnouncement>,
    extra_peer_tx: tokio::sync::mpsc::UnboundedSender<ExtraPeerJob>,
    cancel: CancellationToken,
) {
    let state_dir = crate::platform::paths::default_longdrawer_dir();
    if let Err(e) = std::fs::create_dir_all(&state_dir) {
        warn!(path = %state_dir.display(), error = %e, "longdrawer: cannot create state dir");
        return;
    }

    let http = match build_peer_agent(&tls) {
        Ok(a) => Arc::new(a),
        Err(e) => {
            warn!(error = %e, "longdrawer: cannot build peer HTTP agent");
            return;
        }
    };

    info!(machine = %machine, state_dir = %state_dir.display(), "longdrawer daemon started");

    // Initial scan then loop.
    let mut tick = tokio::time::interval(CYCLE_INTERVAL);
    tick.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Delay);

    loop {
        tokio::select! {
            biased;

            _ = cancel.cancelled() => {
                info!("longdrawer daemon shutting down");
                return;
            }

            _ = tick.tick() => {
                scan_and_announce(&machine, &state_dir, &out_tx, &extra_peer_tx).await;
            }

            Some(incoming) = in_rx.recv() => {
                // Ignore our own announcements (we'll see them on the local interface).
                if incoming.announcement.machine == machine {
                    continue;
                }
                handle_incoming(&state_dir, incoming, http.clone()).await;
            }
        }
    }
}

/// Remove state JSON files for users no longer present in `/etc/passwd`.
///
/// After `userdel -r <user>` the per-user scan never iterates that user
/// again (they're missing from `list_home_dirs()`), but their state file at
/// `<state_dir>/<user>.json` would otherwise survive forever. Worse, if the
/// username gets reused later (e.g. `useradd` with the same name, different
/// UID), the stale state.json silently re-attaches to the new user — the
/// reconciler would treat the new user as carrying the old user's file list
/// and tombstones. This sweep keeps state hygiene aligned with system state.
fn sweep_orphaned_state(state_dir: &std::path::Path, users: &[(String, std::path::PathBuf)]) {
    let active: std::collections::HashSet<&str> =
        users.iter().map(|(u, _)| u.as_str()).collect();
    let entries = match std::fs::read_dir(state_dir) {
        Ok(e) => e,
        Err(_) => return,
    };
    for entry in entries.flatten() {
        let path = entry.path();
        let name = match path.file_stem().and_then(|n| n.to_str()) {
            Some(n) => n,
            None => continue,
        };
        if path.extension().and_then(|e| e.to_str()) != Some("json") {
            continue;
        }
        if active.contains(name) {
            continue;
        }
        if let Err(e) = std::fs::remove_file(&path) {
            warn!(path = %path.display(), error = %e, "longdrawer: failed to remove orphaned state");
        } else {
            info!(user = %name, "longdrawer: removed orphaned state for departed user");
        }
    }
}

/// Scan every user's `~/longdrawer/`, update state, and emit announcements.
async fn scan_and_announce(
    machine: &str,
    state_dir: &std::path::Path,
    out_tx: &tokio::sync::mpsc::Sender<Announcement>,
    extra_peer_tx: &tokio::sync::mpsc::UnboundedSender<ExtraPeerJob>,
) {
    let users = list_home_dirs();
    sweep_orphaned_state(state_dir, &users);
    let now = now_secs();

    for (user, home) in users {
        let sync_dir = home.join("longdrawer");
        if !sync_dir.is_dir() {
            // Skip users without a ~/longdrawer/ directory. They don't opt in.
            continue;
        }

        // Per-user config: cross_subnet_peers for cross-subnet unicast.
        // Missing config.yaml = LAN-only multicast. Malformed config
        // falls back to defaults with a warning (not an error).
        let cfg = config::load_config(&home);

        let mut state = UserState::load(state_dir, &user);
        let previous_files = state.files.clone();
        // Scanner reuses cached sha256 from previous_files when a file's
        // (size, mtime) is unchanged, so stable directories cost ~nothing.
        let current_files = scan_user_dir(&sync_dir, &previous_files);
        merge_scan(&mut state, current_files, &previous_files, now, &sync_dir);
        state.gc_tombstones(now, TOMBSTONE_MAX_AGE_SECS);

        if let Err(e) = state.save(state_dir) {
            warn!(user = %user, error = %e, "longdrawer: failed to save state");
            continue;
        }

        // Digest-only announcement — the multicast packet reveals only
        // that this user has some state with digest D. Receivers pull
        // the full UserState via mTLS if the digest differs from the
        // last one they observed. `generation` is the unix-second
        // emitted_at; good enough to order competing announcements
        // without introducing a new persisted counter.
        let ann = Announcement {
            machine: machine.to_string(),
            user: user.clone(),
            emitted_at: now,
            port: PEER_PORT,
            generation: now as u64,
            state_digest: state.state_digest(),
        };

        // Multicast announce.
        if out_tx.send(ann.clone()).await.is_err() {
            return; // channel closed → shutdown
        }

        // Cross-subnet unicast — one job per configured extra peer.
        for host in &cfg.cross_subnet_peers {
            let _ = extra_peer_tx.send(ExtraPeerJob {
                host: host.clone(),
                announcement: ann.clone(),
            });
        }
    }
}

/// A unicast-announce request for a cross-subnet peer. Produced by
/// `scan_and_announce`, consumed by a dedicated spawned task.
#[derive(Debug, Clone)]
struct ExtraPeerJob {
    host: String,
    announcement: Announcement,
}

/// Long-running task that POSTs unicast announcements to the `cross_subnet_peers`
/// listed in each user's `config.yaml`. Runs off the scan hot path so a
/// slow or unreachable peer doesn't stall the scan cycle.
async fn run_extra_peer_pusher(
    tls: TlsCerts,
    mut rx: tokio::sync::mpsc::UnboundedReceiver<ExtraPeerJob>,
    cancel: CancellationToken,
) {
    let http = match build_peer_agent(&tls) {
        Ok(a) => Arc::new(a),
        Err(e) => {
            warn!(error = %e, "longdrawer: cross_subnet_peers pusher could not build HTTPS agent");
            return;
        }
    };

    loop {
        tokio::select! {
            biased;
            _ = cancel.cancelled() => return,
            Some(job) = rx.recv() => {
                let http_c = http.clone();
                tokio::task::spawn_blocking(move || {
                    if let Err(e) = push_announcement(&http_c, &job.host, &job.announcement) {
                        debug!(
                            host = %job.host,
                            user = %job.announcement.user,
                            error = %e,
                            "longdrawer: extra_peer push failed (will retry next cycle)"
                        );
                    }
                });
            }
            else => return,
        }
    }
}

/// POST a longdrawer announcement to an extra peer's
/// `/longdrawer/announce` route. Non-2xx responses return an error so
/// the caller can log them.
/// Pull the full `UserState` for `user` from a peer via mTLS HTTPS.
/// Called on digest-mismatch in the multicast receiver; the peer returns
/// the JSON body served by `GET /swarm/longdrawer/state/{user}` on its swarm
/// peer server. Blocking I/O — caller wraps in `spawn_blocking`.
fn pull_user_state(
    http: &crate::swarm::tls::SwarmAgent,
    peer_addr: &str,
    user: &str,
) -> Result<UserState, anyhow::Error> {
    let url = format!("https://{}/swarm/longdrawer/state/{}", peer_addr, user);
    let resp = http.get(&url).send()?;
    let status = resp.status();
    if status != reqwest::StatusCode::OK {
        anyhow::bail!("GET {} returned {}", url, status);
    }
    let body = resp.bytes()?;
    let state: UserState = serde_json::from_slice(&body)
        .with_context(|| format!("parsing state JSON from {}", url))?;
    Ok(state)
}

fn push_announcement(
    http: &crate::swarm::tls::SwarmAgent,
    host: &str,
    ann: &Announcement,
) -> Result<(), anyhow::Error> {
    // Pre-flight disk-pressure probe. An announcement is tiny, but if
    // the peer's `~<user>/longdrawer/` filesystem is ≤10% free it can't
    // accept the files the announcement refers to either — skip the
    // round trip. Cached per (peer, purpose) for 60 s so periodic
    // re-announces don't hammer the health endpoint.
    use crate::swarm::peer_health::{probe, Purpose};
    let outcome = probe(
        http,
        &format!("{}:{}", host, PEER_PORT),
        &Purpose::Longdrawer { user: ann.user.clone() },
    );
    if !outcome.allow() {
        anyhow::bail!(
            "skip announce to {} ({})",
            host,
            outcome.reason()
        );
    }

    let url = format!("https://{}:{}/swarm/longdrawer/announce", host, PEER_PORT);
    let body = serde_json::to_vec(ann)?;
    let resp = http
        .post(&url)
        .header("content-type", "application/json")
        .body(body)
        .send()
        .map_err(|e| anyhow::anyhow!("POST {}: {}", url, e))?;
    let status = resp.status();
    if status != reqwest::StatusCode::OK {
        anyhow::bail!("POST {} returned {}", url, status);
    }
    Ok(())
}

/// Reconcile one incoming announcement against local state and apply.
///
/// 0.30.11+ model: the incoming announcement carries only a state digest,
/// not the full state. If the digest matches what we'd compute from our
/// local copy, there's nothing to do — skip silently. Otherwise, pull the
/// full `UserState` from the announcing peer over mTLS and hand the
/// result to the reconciler. The pull is the only path that reveals
/// file-level detail, and it rides the authenticated peer channel.
async fn handle_incoming(
    state_dir: &std::path::Path,
    incoming: IncomingAnnouncement,
    http: Arc<crate::swarm::tls::SwarmAgent>,
) {
    let peer_ip = match incoming.from {
        std::net::SocketAddr::V4(v4) => v4.ip().to_string(),
        std::net::SocketAddr::V6(_) => {
            debug!("longdrawer: ignoring IPv6 announcement");
            return;
        }
    };
    let peer_addr = format!("{}:{}", peer_ip, incoming.announcement.port);
    let user = incoming.announcement.user.clone();
    let remote_digest = incoming.announcement.state_digest.clone();

    // Only process if we have a home dir for this user locally.
    let home = match crate::platform::users::user_home(&user) {
        Some(h) => h,
        None => {
            debug!(user = %user, "longdrawer: no local home, ignoring");
            return;
        }
    };
    let sync_dir = home.join("longdrawer");

    // Opt-in symmetry with scan_and_announce: if the user's ~/longdrawer/ is
    // absent, this envoy is dormant for that user and must not silently
    // recreate the directory by fetching peer files into a freshly-created
    // sync_dir (applier::fetch_file calls create_dir_all). Drop the
    // announcement; on next user mkdir the receive path re-engages naturally.
    if !sync_dir.is_dir() {
        debug!(user = %user, "longdrawer: ~/longdrawer/ absent, dropping incoming announcement");
        return;
    }

    let mut local = UserState::load(state_dir, &user);
    // Cheap short-circuit: if the announcing peer's digest matches our
    // local state digest, we're already in sync with them — skip the
    // mTLS pull and the reconciler pass entirely.
    if local.state_digest() == remote_digest {
        return;
    }

    // Pull the peer's full UserState via mTLS. Blocks the reconciler
    // task on one ureq call per announcement; bounded by ureq's default
    // timeout so a slow peer doesn't wedge the handler.
    let peer_addr_pull = peer_addr.clone();
    let user_pull = user.clone();
    let http_pull = http.clone();
    let remote_state = match tokio::task::spawn_blocking(move || {
        pull_user_state(&http_pull, &peer_addr_pull, &user_pull)
    })
    .await
    {
        Ok(Ok(s)) => s,
        Ok(Err(e)) => {
            debug!(user = %user, peer = %peer_addr, error = %e, "longdrawer: state pull failed");
            return;
        }
        Err(_) => return, // spawn_blocking joined with panic
    };

    let outcome = reconcile(&local, &remote_state);

    if outcome.actions.is_empty() && outcome.merged_tombstones.is_empty() {
        return;
    }

    debug!(
        user = %user,
        peer = %peer_addr,
        actions = outcome.actions.len(),
        tombstones = outcome.merged_tombstones.len(),
        "longdrawer: reconciling incoming announcement"
    );

    // Apply actions (may block on I/O, so run in a blocking task).
    let http_clone = http.clone();
    let sync_dir_clone = sync_dir.clone();
    let user_clone = user.clone();
    let peer_addr_clone = peer_addr.clone();
    let actions = outcome.actions.clone();
    let applied: Vec<Action> = tokio::task::spawn_blocking(move || {
        let mut applied = Vec::new();
        for action in actions {
            match applier::apply(&action, &sync_dir_clone, &user_clone, &peer_addr_clone, &http_clone) {
                Ok(_) => applied.push(action),
                Err(e) => warn!(
                    user = %user_clone,
                    peer = %peer_addr_clone,
                    action = ?action,
                    error = %e,
                    "longdrawer: apply failed"
                ),
            }
        }
        applied
    })
    .await
    .unwrap_or_default();

    // Update local state to reflect what we applied + merged tombstones.
    for action in &applied {
        match action {
            Action::Fetch { name, sha256, size, mtime } => {
                local.files.insert(
                    name.clone(),
                    state::FileEntry {
                        sha256: sha256.clone(),
                        mtime: *mtime,
                        size: *size,
                    },
                );
                local.tombstones.remove(name);
            }
            Action::Delete { name } => {
                local.files.remove(name);
            }
        }
    }
    merge_tombstones(&mut local, outcome.merged_tombstones);

    if let Err(e) = local.save(state_dir) {
        warn!(user = %user, error = %e, "longdrawer: failed to save state after reconcile");
    }
}

// Force BTreeMap used to silence unused-import lints if code paths shift.
#[allow(dead_code)]
fn _btreemap_marker() -> BTreeMap<String, PathBuf> {
    BTreeMap::new()
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;

    #[test]
    fn sweep_removes_state_for_departed_users() {
        let state_dir = tempfile::tempdir().unwrap();
        fs::write(state_dir.path().join("alice.json"), b"{}").unwrap();
        fs::write(state_dir.path().join("bob.json"), b"{}").unwrap();
        fs::write(state_dir.path().join("carol.json"), b"{}").unwrap();

        // Only alice still exists on the system.
        let active = vec![("alice".to_string(), PathBuf::from("/home/alice"))];

        sweep_orphaned_state(state_dir.path(), &active);

        assert!(state_dir.path().join("alice.json").exists());
        assert!(!state_dir.path().join("bob.json").exists());
        assert!(!state_dir.path().join("carol.json").exists());
    }

    #[test]
    fn sweep_ignores_non_json_files() {
        // Operator notes, lockfiles, anything not a .json state file
        // must not be touched even if the basename matches no active user.
        let state_dir = tempfile::tempdir().unwrap();
        fs::write(state_dir.path().join("README"), b"hi").unwrap();
        fs::write(state_dir.path().join("alice.json.tmp"), b"{}").unwrap();
        fs::write(state_dir.path().join("alice.json"), b"{}").unwrap();

        // No active users at all.
        let active: Vec<(String, PathBuf)> = vec![];

        sweep_orphaned_state(state_dir.path(), &active);

        assert!(state_dir.path().join("README").exists());
        assert!(state_dir.path().join("alice.json.tmp").exists());
        assert!(!state_dir.path().join("alice.json").exists());
    }

    #[test]
    fn sweep_tolerates_missing_state_dir() {
        let parent = tempfile::tempdir().unwrap();
        let missing = parent.path().join("does-not-exist");
        let active = vec![("alice".to_string(), PathBuf::from("/home/alice"))];
        // Must not panic.
        sweep_orphaned_state(&missing, &active);
    }
}
