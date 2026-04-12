// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Vigosync — per-user LAN-local directory sync across machines.
//!
//! Each user's `~/vigosync/` directory is mirrored to the same user's
//! `~/vigosync/` on every other machine on the LAN. File additions and
//! modifications propagate; deletions propagate via tombstones.
//!
//! ### Design
//!
//! - **Pure P2P**, LAN-only. No server involvement. Discovery via multicast
//!   on `224.0.0.43:1532`, separate from swarm's 224.0.0.42:1531.
//! - **Content transport** piggybacks on the existing swarm peer HTTPS
//!   server (port 1531, mTLS). New route: `/vigosync/{user}/{filename}`.
//! - **State** per user is kept under `/var/lib/vigo/vigosync/<user>.json`:
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
pub mod discovery;
pub mod reconciler;
pub mod scanner;
pub mod server;
pub mod state;

use std::collections::BTreeMap;
use std::path::PathBuf;
use std::sync::Arc;
use std::time::Duration;

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

/// The swarm peer server port — vigosync fetches file content from this port.
const PEER_PORT: u16 = crate::swarm::server::DEFAULT_PORT;

/// Spawn the vigosync daemon. Runs until the cancellation token fires.
///
/// Requires mTLS certs so it can fetch files from peers over HTTPS. If
/// certs aren't available, vigosync silently does nothing (same pattern
/// as the swarm peer server).
pub fn spawn(tls: Option<TlsCerts>, cancel: CancellationToken) {
    let tls = match tls {
        Some(t) => t,
        None => {
            debug!("vigosync: no TLS certs, skipping (will not participate in sync)");
            return;
        }
    };

    // Get a stable machine identifier. Hostname is good enough for LAN use;
    // if two machines share a hostname, vigosync will misbehave — document
    // it, don't try to solve it.
    let machine = std::fs::read_to_string("/etc/hostname")
        .unwrap_or_else(|_| "unknown".to_string())
        .trim()
        .to_string();

    let (out_tx, in_rx) = spawn_discovery(cancel.clone());
    tokio::spawn(run(machine, tls, out_tx, in_rx, cancel));
}

async fn run(
    machine: String,
    tls: TlsCerts,
    out_tx: tokio::sync::mpsc::Sender<Announcement>,
    mut in_rx: tokio::sync::mpsc::Receiver<IncomingAnnouncement>,
    cancel: CancellationToken,
) {
    let state_dir = crate::platform::paths::default_vigosync_dir();
    if let Err(e) = std::fs::create_dir_all(&state_dir) {
        warn!(path = %state_dir.display(), error = %e, "vigosync: cannot create state dir");
        return;
    }

    let http = match build_peer_agent(&tls) {
        Ok(a) => Arc::new(a),
        Err(e) => {
            warn!(error = %e, "vigosync: cannot build peer HTTP agent");
            return;
        }
    };

    info!(machine = %machine, state_dir = %state_dir.display(), "vigosync daemon started");

    // Initial scan then loop.
    let mut tick = tokio::time::interval(CYCLE_INTERVAL);
    tick.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Delay);

    loop {
        tokio::select! {
            biased;

            _ = cancel.cancelled() => {
                info!("vigosync daemon shutting down");
                return;
            }

            _ = tick.tick() => {
                scan_and_announce(&machine, &state_dir, &out_tx).await;
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

/// Scan every user's `~/vigosync/`, update state, and emit announcements.
async fn scan_and_announce(
    machine: &str,
    state_dir: &std::path::Path,
    out_tx: &tokio::sync::mpsc::Sender<Announcement>,
) {
    let users = list_home_dirs();
    let now = now_secs();

    for (user, home) in users {
        let sync_dir = home.join("vigosync");
        if !sync_dir.is_dir() {
            // Skip users without a ~/vigosync/ directory. They don't opt in.
            continue;
        }

        let mut state = UserState::load(state_dir, &user);
        let previous_files = state.files.clone();
        // Scanner reuses cached sha256 from previous_files when a file's
        // (size, mtime) is unchanged, so stable directories cost ~nothing.
        let current_files = scan_user_dir(&sync_dir, &previous_files);
        merge_scan(&mut state, current_files, &previous_files, now);
        state.gc_tombstones(now, TOMBSTONE_MAX_AGE_SECS);

        if let Err(e) = state.save(state_dir) {
            warn!(user = %user, error = %e, "vigosync: failed to save state");
            continue;
        }

        let ann = Announcement {
            machine: machine.to_string(),
            emitted_at: now,
            port: PEER_PORT,
            state: state.clone(),
        };
        if out_tx.send(ann).await.is_err() {
            return; // channel closed → shutdown
        }
    }
}

/// Reconcile one incoming announcement against local state and apply.
async fn handle_incoming(
    state_dir: &std::path::Path,
    incoming: IncomingAnnouncement,
    http: Arc<ureq::Agent>,
) {
    let peer_ip = match incoming.from {
        std::net::SocketAddr::V4(v4) => v4.ip().to_string(),
        std::net::SocketAddr::V6(_) => {
            debug!("vigosync: ignoring IPv6 announcement");
            return;
        }
    };
    let peer_addr = format!("{}:{}", peer_ip, incoming.announcement.port);
    let user = incoming.announcement.state.user.clone();

    // Only process if we have a home dir for this user locally.
    let home = match applier::user_home(&user) {
        Some(h) => h,
        None => {
            debug!(user = %user, "vigosync: no local home, ignoring");
            return;
        }
    };
    let sync_dir = home.join("vigosync");

    let mut local = UserState::load(state_dir, &user);
    let outcome = reconcile(&local, &incoming.announcement.state);

    if outcome.actions.is_empty() && outcome.merged_tombstones.is_empty() {
        return;
    }

    debug!(
        user = %user,
        peer = %peer_addr,
        actions = outcome.actions.len(),
        tombstones = outcome.merged_tombstones.len(),
        "vigosync: reconciling incoming announcement"
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
                    "vigosync: apply failed"
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
        warn!(user = %user, error = %e, "vigosync: failed to save state after reconcile");
    }
}

// Force BTreeMap used to silence unused-import lints if code paths shift.
#[allow(dead_code)]
fn _btreemap_marker() -> BTreeMap<String, PathBuf> {
    BTreeMap::new()
}
