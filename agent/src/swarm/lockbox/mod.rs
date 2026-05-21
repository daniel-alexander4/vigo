// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Lockbox — per-user encrypted P2P directory.
//!
//! A user places a file into `~/lockbox/` on one envoy; lockbox
//! encrypts it to every enrolled envoy in the user's mesh and syncs
//! the ciphertext so every envoy holds the same age-encrypted bytes
//! at rest. Files are never plaintext on disk in `~/lockbox/` — the
//! whole point.
//!
//! Relationship to longdrawer: both are per-user LAN-first P2P sync
//! subsystems. Longdrawer is unencrypted plaintext sync (LAN-trusted).
//! Lockbox is always-encrypted sync where only envoys in the mesh
//! can decrypt, and decryption requires the user's lockbox
//! password.
//!
//! Phase 1 scope: Linux + macOS only. No PAM, no FUSE, no filename
//! encryption, no key escrow. See `plan_lockbox.md` for the full
//! spec.
//!
//! Phase 1 Week 1: wrapping format, `init` / `enroll` / `status`,
//! user-executor integration.
//! Phase 1 Week 2 (this code adds): multicast discovery, peer table,
//! `recipients.txt` regeneration, HTTPS routes (push/pull/announce),
//! daemon lifecycle. Session helper + remaining verbs follow in week 3.

pub mod auto_encrypt;
pub mod cli;
pub mod config;
pub mod crypt;
pub mod discovery;
pub mod puddle_cert;
pub mod gate;
pub mod identity_rewrap;
pub mod leaves;
pub mod locks;
pub mod puddle_members;
pub mod pusher;
pub mod resurrects;
pub mod server;
pub mod session;
pub mod snapshot;
pub mod state;
pub mod tombstones;
pub mod watcher;

use std::collections::HashSet;
use std::fs;
use std::io::Write;
use std::path::{Path, PathBuf};
use std::time::Duration;

use anyhow::{Context, Result};
use tokio::sync::mpsc;
use tokio_util::sync::CancellationToken;
use tracing::{debug, info, warn};

use discovery::IncomingAnnouncement;
use state::{Announcement, PeerEntry, PeerTable};
use tombstones::TombstoneTable;

use crate::update::TlsCerts;

/// How often each enrolled user's local state is re-announced.
/// Short enough to feel responsive; long enough not to chew the LAN.
const ANNOUNCE_INTERVAL: Duration = Duration::from_secs(30);

/// How often stale peers are pruned from every user's PeerTable.
const PRUNE_INTERVAL: Duration = Duration::from_secs(60);

/// How often expired tombstones are GC'd from every user's table.
/// Daily tick is fine — tombstone retention is measured in days,
/// so the worst-case lag between "tombstone expired" and "tombstone
/// removed from disk" is one tick. Bounded storage cost at personal
/// file scale makes this cheap.
const TOMBSTONE_GC_INTERVAL: Duration = Duration::from_secs(24 * 60 * 60);

/// How often the daemon checks each enrolled user for a
/// `~/.lockbox/resurrect.pending` sentinel. Tight cadence because
/// `cmd_resurrect` is interactive — the operator is waiting at a
/// prompt for the daemon to sign + fan out the envelope. The
/// alternative (announce_timer at 30s) is unacceptable UX.
/// Cost is one filesystem stat per second per enrolled user;
/// negligible at personal-fleet scale.
const RESURRECT_POLL_INTERVAL: Duration = Duration::from_secs(1);

/// The swarm peer server port — lockbox push/pull rides here.
const PEER_PORT: u16 = crate::swarm::server::DEFAULT_PORT;

/// Inbound-announcement sender, published when the lockbox daemon
/// starts. The HTTPS `/lockbox/announce` handler uses this to feed
/// unicast announcements from `cross_subnet_peers` into the same reconciler
/// path that multicast uses. `None` before spawn.
static INCOMING_ANNOUNCE: std::sync::OnceLock<mpsc::Sender<IncomingAnnouncement>> =
    std::sync::OnceLock::new();

/// TLS certs stashed by the daemon at startup so the tombstone
/// catch-up path can build its own mTLS HTTP client without having
/// them threaded through the announcement-handling stack. `None`
/// when the daemon spawned without TLS (fan-out disabled posture).
static DAEMON_TLS: std::sync::OnceLock<TlsCerts> = std::sync::OnceLock::new();

/// Deliver a unicast announcement into the lockbox reconciler.
/// Returns true on success, false if the daemon isn't running or the
/// channel is full. Used by `POST /lockbox/announce`.
pub fn submit_announcement(from: std::net::SocketAddr, announcement: Announcement) -> bool {
    match INCOMING_ANNOUNCE.get() {
        Some(tx) => tx
            .try_send(IncomingAnnouncement {
                from,
                announcement,
            })
            .is_ok(),
        None => false,
    }
}

/// Spawn the lockbox daemon. Runs until the cancellation token
/// fires. No-op if lockbox is not wanted on this envoy (no enrolled
/// users exist yet — the daemon re-discovers enrolled users every
/// cycle so brand-new enrollments don't require a daemon restart).
///
/// `tls` is the same mTLS bundle swarm uses. When absent, the
/// fan-out pusher is disabled but everything else runs (discovery,
/// peer table maintenance, `recipients.txt` regen, serving
/// `/lockbox/pull/...` GETs). Matches longdrawer's posture.
pub fn spawn(tls: Option<TlsCerts>, cancel: CancellationToken) {
    let machine = std::fs::read_to_string("/etc/hostname")
        .unwrap_or_else(|_| "unknown".to_string())
        .trim()
        .to_string();

    let (out_tx, in_tx, in_rx) = discovery::spawn(cancel.clone());
    // Publish the inbound sender so /lockbox/announce can feed it.
    let _ = INCOMING_ANNOUNCE.set(in_tx);

    // Watcher emits PushJob + DeleteJob events on two channels; the
    // pusher drains both. If TLS certs aren't available, skip the
    // watcher entirely — no point detecting events if we can't act
    // on them.
    if let Some(tls) = tls {
        // Publish TLS for the catch-up tombstone-pull code path.
        let _ = DAEMON_TLS.set(tls.clone());
        let (push_rx, del_rx) = watcher::spawn(cancel.clone());
        pusher::spawn(tls.clone(), push_rx, del_rx, cancel.clone());
        // Extra-peers unicast announcer — sends periodic announcements
        // to each user's `cross_subnet_peers:` hosts over HTTPS.
        tokio::spawn(cross_subnet_peers_announce_loop(
            machine.clone(),
            tls,
            cancel.clone(),
        ));
    } else {
        debug!("lockbox: no TLS certs, fan-out and extra-peers disabled");
    }

    tokio::spawn(run(machine, out_tx, in_rx, cancel));
}

async fn run(
    machine: String,
    out_tx: mpsc::Sender<Announcement>,
    mut in_rx: mpsc::Receiver<IncomingAnnouncement>,
    cancel: CancellationToken,
) {
    info!(machine = %machine, "lockbox daemon started");

    let mut announce_timer = tokio::time::interval(ANNOUNCE_INTERVAL);
    let mut prune_timer = tokio::time::interval(PRUNE_INTERVAL);
    let mut tombstone_gc_timer = tokio::time::interval(TOMBSTONE_GC_INTERVAL);
    let mut resurrect_poll_timer = tokio::time::interval(RESURRECT_POLL_INTERVAL);
    // Fire one announce immediately on startup rather than waiting a
    // full interval — fresh envoys should appear on the mesh promptly.
    announce_timer.tick().await;
    // Skip the first prune / gc tick — nothing to prune yet.
    prune_timer.tick().await;
    tombstone_gc_timer.tick().await;
    // Skip the first resurrect-poll tick — sentinel can only land
    // after the daemon is up and accepting them.
    resurrect_poll_timer.tick().await;

    loop {
        tokio::select! {
            _ = cancel.cancelled() => {
                info!("lockbox daemon: cancelled, exiting");
                return;
            }
            _ = announce_timer.tick() => {
                // Process any pending leaves before announcing so
                // a fresh leave lands on peers in the same cycle.
                process_pending_leaves(&machine).await;
                // Process any post-reenroll regen requests so a
                // fresh `vigo swarm lockbox {puddle init, standalone enroll}`
                // picks up live peers within one tick instead of
                // waiting for the next peer pubkey rotation.
                process_pending_regens();
                if let Err(e) = announce_all(&machine, &out_tx).await {
                    warn!(error = %e, "lockbox: announce cycle error");
                }
            }
            _ = prune_timer.tick() => {
                prune_all();
            }
            _ = tombstone_gc_timer.tick() => {
                gc_tombstones_all();
                gc_resurrects_all();
            }
            _ = resurrect_poll_timer.tick() => {
                process_pending_resurrects(&machine).await;
            }
            maybe = in_rx.recv() => {
                match maybe {
                    Some(inc) => handle_incoming(inc),
                    None => {
                        // Channel closed — discovery exited.
                        return;
                    }
                }
            }
        }
    }
}

/// Poll every enrolled user for a `~/.lockbox/leave.pending` sentinel
/// dropped by `vigo swarm lockbox leave`. For each sentinel present:
///
/// 1. Load the daemon signing key for the user.
/// 2. Build + fan out a signed LEAVE envelope to every live peer.
/// 3. Persist the LEAVE to local `leaves.json` and apply locally
///    (mostly a no-op on the leaving envoy since self isn't in its
///    own peer table; still drops `reencrypt.pending` and the
///    `leaves.json` entry for completeness / future audit).
/// 4. Scrub user-side state per the sentinel's flags.
/// 5. Remove the sentinel.
///
/// Best-effort on network push — individual failures logged, not
/// escalated. Peers that miss the live fan-out naturally drop this
/// envoy via STALE_SECS (90 s) when announcements stop.
async fn process_pending_leaves(machine: &str) {
    let enrolled = match enrolled_users() {
        Ok(s) => s,
        Err(_) => return,
    };
    for user in enrolled {
        let home = match home_for_user(&user) {
            Some(h) => h,
            None => continue,
        };
        let pending = config::leave_pending_path(&home);
        if !pending.exists() {
            continue;
        }
        match process_one_leave(machine, &user, &home, &pending).await {
            Ok(()) => {
                info!(user = %user, "lockbox: leave processed, sentinel cleared");
            }
            Err(e) => {
                warn!(user = %user, error = %e, "lockbox: leave processing failed");
            }
        }
    }
}

async fn process_one_leave(
    machine: &str,
    user: &str,
    home: &Path,
    pending: &Path,
) -> Result<()> {
    let raw = fs::read_to_string(pending)
        .with_context(|| format!("reading {}", pending.display()))?;
    let spec: super::lockbox::cli::LeavePendingSpec =
        serde_json::from_str(&raw).context("parsing leave.pending")?;

    let signing_key = load_local_signing_key(user)
        .context("cannot load daemon signing key")?;

    let envelope = leaves::build_envelope(
        &signing_key,
        machine,
        user,
        &spec.age_pubkey,
        spec.leave_time,
    );

    // Persist first, push second: if fan-out fails mid-flight we've
    // at least recorded the LEAVE locally. `apply_leave` is
    // idempotent across repeat deliveries.
    apply_leave(user, &spec.age_pubkey, &spec.hostname, spec.leave_time, home)?;

    if let Some(tls) = DAEMON_TLS.get() {
        // Spawn into the runtime so the blocking pushes don't pin
        // the announcer loop — we're already in an async context.
        let tls = tls.clone();
        let envelope_clone = envelope.clone();
        tokio::spawn(async move {
            if let Err(e) = pusher::fan_out_leave(&tls, envelope_clone).await {
                warn!(error = %e, "lockbox: LEAVE fan-out error");
            }
        });
    } else {
        warn!("lockbox: DAEMON_TLS not initialised, skipping LEAVE fan-out (peers catch up via staleness)");
    }

    // Scrub user-side state per flags.
    if spec.purge_content {
        let content = config::content_dir(home);
        if content.exists() {
            if let Err(e) = fs::remove_dir_all(&content) {
                warn!(path = %content.display(), error = %e, "lockbox: purge ~/lockbox/ failed");
            }
        }
    }
    if !spec.keep_identity {
        let dot = config::dot_dir(home);
        // Preserve leave.pending (we're about to remove it) by
        // removing items individually. Belt-and-suspenders: the
        // rm-pending step at the end still fires even if this loop
        // raced some other file.
        for name in [
            "identity.age",
            "identity.salt",
            "recipients.txt",
            "config.yaml",
        ] {
            let p = dot.join(name);
            if p.exists() {
                let _ = fs::remove_file(&p);
            }
        }
    }

    fs::remove_file(pending)
        .with_context(|| format!("removing {}", pending.display()))?;
    Ok(())
}

/// Poll every enrolled user for a `~/.lockbox/resurrect.pending`
/// sentinel dropped by `vigo swarm lockbox resurrect`. For each
/// sentinel present: load the daemon signing key, persist a local
/// resurrect record (under the resurrects modify lock), build + sign
/// a RESURRECT envelope, fan it out to live peers + cross-subnet
/// peers, and remove the sentinel.
///
/// Runs on its own 1s tick (`RESURRECT_POLL_INTERVAL`) — the announce
/// timer (30s) is too slow for an interactive verb that the operator
/// is waiting at a prompt for. CLI polls for sentinel removal up to
/// 60s before timing out.
///
/// Best-effort on network push — individual peer failures are logged,
/// not escalated. Peers that miss the live fan-out catch up via the
/// `resurrects_digest` mismatch path on their next announce-receive.
async fn process_pending_resurrects(machine: &str) {
    let enrolled = match enrolled_users() {
        Ok(s) => s,
        Err(_) => return,
    };
    for user in enrolled {
        let home = match home_for_user(&user) {
            Some(h) => h,
            None => continue,
        };
        let pending = config::resurrect_pending_path(&home);
        if !pending.exists() {
            continue;
        }
        match process_one_resurrect(machine, &user, &pending).await {
            Ok(()) => {
                info!(user = %user, "lockbox: resurrect processed, sentinel cleared");
            }
            Err(e) => {
                warn!(user = %user, error = %e, "lockbox: resurrect processing failed");
            }
        }
    }
}

async fn process_one_resurrect(
    machine: &str,
    user: &str,
    pending: &Path,
) -> Result<()> {
    let raw = fs::read_to_string(pending)
        .with_context(|| format!("reading {}", pending.display()))?;
    let spec: super::lockbox::cli::ResurrectPendingSpec =
        serde_json::from_str(&raw).context("parsing resurrect.pending")?;

    if spec.filename.is_empty() {
        anyhow::bail!("resurrect.pending: filename is empty");
    }

    let signing_key = load_local_signing_key(user)
        .context("cannot load daemon signing key")?;

    let resurrect_time = now_secs();

    // Persist local resurrect record FIRST so the encrypt-time
    // preflight (run by the CLI right after sentinel removal) sees
    // the supersede on its `effective_tombstone` check. Doing this
    // before fan-out keeps THIS envoy in a coherent state even if
    // every peer push fails.
    if let Err(e) = resurrects::ResurrectsTable::modify(user, |table| {
        table.upsert(
            &spec.filename,
            resurrects::ResurrectRecord {
                resurrect_time,
                source_hostname: machine.to_string(),
            },
        )
    }) {
        warn!(
            user = %user,
            filename = %spec.filename,
            error = %e,
            "lockbox: resurrect record save failed"
        );
    }

    let envelope = resurrects::build_envelope(
        &signing_key,
        machine,
        user,
        &spec.filename,
        resurrect_time,
    );

    if let Some(tls) = DAEMON_TLS.get() {
        // Spawn into the runtime so the blocking pushes don't pin
        // the polling tick.
        let tls = tls.clone();
        let envelope_clone = envelope.clone();
        tokio::spawn(async move {
            if let Err(e) = pusher::fan_out_resurrect(&tls, envelope_clone).await {
                warn!(error = %e, "lockbox: RESURRECT fan-out error");
            }
        });
    } else {
        warn!("lockbox: DAEMON_TLS not initialised, skipping RESURRECT fan-out (peers catch up via resurrects_digest mismatch)");
    }

    fs::remove_file(pending)
        .with_context(|| format!("removing {}", pending.display()))?;
    Ok(())
}

/// Poll every enrolled user for a `~/.lockbox/regen.pending` sentinel
/// dropped by `vigo swarm lockbox {puddle init, standalone enroll}` after a fresh enrollment. For each
/// sentinel present: regenerate `recipients.txt` from the current
/// daemon-side peer table (folding in every live peer the daemon
/// already knows about) and remove the sentinel. Idempotent; if the
/// sentinel reappears between ticks (e.g., a second enroll), the
/// next tick processes it again.
///
/// This closes the post-reenroll gap where the announce path's
/// upsert returns `false` for already-known peers (heartbeat case)
/// and so never triggers a regen, leaving recipients.txt stuck at
/// "self only" until something explicitly forces a refresh.
fn process_pending_regens() {
    let enrolled = match enrolled_users() {
        Ok(s) => s,
        Err(_) => return,
    };
    for user in enrolled {
        let home = match home_for_user(&user) {
            Some(h) => h,
            None => continue,
        };
        let pending = config::regen_pending_path(&home);
        if !pending.exists() {
            continue;
        }
        let table = PeerTable::load(&user);
        match regenerate_recipients(&user, &home, &table) {
            Ok(()) => {
                info!(user = %user, "lockbox: post-enroll recipients regen complete");
                if let Err(e) = fs::remove_file(&pending) {
                    warn!(
                        user = %user,
                        path = %pending.display(),
                        error = %e,
                        "lockbox: failed to clear regen.pending sentinel"
                    );
                }
            }
            Err(e) => {
                warn!(
                    user = %user,
                    error = %e,
                    "lockbox: post-enroll recipients regen failed; will retry next tick"
                );
            }
        }
    }
}

/// Prune expired tombstones for every enrolled user. Runs once per
/// `TOMBSTONE_GC_INTERVAL`. Default retention is 90 days; per-user
/// override via `tombstone_retention_days` in config.yaml (future —
/// for now the default applies).
fn gc_tombstones_all() {
    let enrolled = match enrolled_users() {
        Ok(s) => s,
        Err(_) => return,
    };
    let now = now_secs();
    for user in enrolled {
        let mut dropped: usize = 0;
        if let Err(e) = TombstoneTable::modify(&user, |table| {
            dropped = table.gc(now, tombstones::effective_retention_secs());
            dropped > 0
        }) {
            warn!(
                user = %user,
                error = %e,
                "lockbox: tombstone save after gc failed"
            );
            continue;
        }
        if dropped > 0 {
            info!(
                user = %user,
                dropped = dropped,
                "lockbox: tombstone gc complete"
            );
        }
    }
}

/// Prune expired resurrects for every enrolled user. Same retention
/// as tombstones (`tombstones::DEFAULT_RETENTION_SECS`) so the two
/// tables age out together — once both have aged past retention,
/// `effective_tombstone` returns None and the filename is freely
/// reusable.
fn gc_resurrects_all() {
    let enrolled = match enrolled_users() {
        Ok(s) => s,
        Err(_) => return,
    };
    let now = now_secs();
    for user in enrolled {
        let mut dropped: usize = 0;
        if let Err(e) = resurrects::ResurrectsTable::modify(&user, |table| {
            dropped = table.gc(now, tombstones::effective_retention_secs());
            dropped > 0
        }) {
            warn!(
                user = %user,
                error = %e,
                "lockbox: resurrect save after gc failed"
            );
            continue;
        }
        if dropped > 0 {
            info!(
                user = %user,
                dropped = dropped,
                "lockbox: resurrect gc complete"
            );
        }
    }
}

// ---------- announce ----------

async fn announce_all(
    machine: &str,
    out_tx: &mpsc::Sender<Announcement>,
) -> Result<()> {
    for user in enrolled_users()? {
        let home = match home_for_user(&user) {
            Some(h) => h,
            None => continue,
        };
        let pubkey = match load_local_pubkey(&home) {
            Ok(p) => p,
            Err(e) => {
                debug!(user = %user, error = %e, "lockbox: cannot read local pubkey, skipping announce");
                continue;
            }
        };
        if let Err(e) = ensure_signing_identity(&user) {
            warn!(user = %user, error = %e, "lockbox: ensure_signing_identity failed");
        }
        let sign_pubkey = load_local_sign_pubkey(&user).unwrap_or_default();
        let tombstones_digest =
            hex::encode(TombstoneTable::load(&user).digest());
        let leaves_digest =
            hex::encode(leaves::LeavesTable::load(&user).digest());
        let resurrects_digest =
            hex::encode(resurrects::ResurrectsTable::load(&user).digest());
        // Files digest: covers the announcer's ~<user>/lockbox/ ciphertext
        // set. Receivers with a mismatching local digest pull the file
        // list + any missing blobs via `maybe_catchup_files`. Closes the
        // empty-peers push race that `pusher.rs` cannot retry around.
        let files_digest =
            snapshot::digest_files(&snapshot::local_files_for_user(&user, &home));
        // ADR-016: include the user's puddle pubkey when present
        // so peers can scope recipient resolution to the same fleet.
        // Empty when puddle isn't initialized for this user (the
        // standalone mode the legacy gossip pattern serves).
        let puddle_pubkey = local_puddle_pubkey_hex(&home);
        // ADR-016 follow-up: embed the puddle membership certificate
        // when one's on disk and matches our age + puddle pubkeys.
        // Receivers verify the signature, closing the same-LAN
        // first-contact race that TOFU has on its own. None when the
        // user hasn't run `lockbox puddle renew-cert` (or when the
        // on-disk cert is stale and they need to renew).
        let puddle_cert = load_announce_puddle_cert(&home, &pubkey, &puddle_pubkey);
        let ann = Announcement {
            machine: machine.to_string(),
            user: user.clone(),
            pubkey,
            sign_pubkey,
            emitted_at: now_secs(),
            port: PEER_PORT,
            generation: 0,
            tombstones_digest,
            leaves_digest,
            resurrects_digest,
            files_digest,
            puddle_pubkey,
            puddle_cert,
        };
        if let Err(e) = out_tx.send(ann).await {
            warn!(error = %e, user = %user, "lockbox: announce channel send failed");
        }
    }
    Ok(())
}

// ---------- incoming ----------

fn handle_incoming(inc: IncomingAnnouncement) {
    let IncomingAnnouncement { from, announcement } = inc;
    debug!(
        machine = %announcement.machine,
        user = %announcement.user,
        from = %from,
        "lockbox: incoming announcement"
    );

    // Ignore announcements for users we don't have enrolled locally.
    // Different users' meshes are fully independent; we never keep
    // state for users we don't participate in.
    let enrolled = match enrolled_users() {
        Ok(s) => s,
        Err(e) => {
            warn!(error = %e, "lockbox: cannot enumerate enrolled users, dropping incoming");
            return;
        }
    };
    if !enrolled.contains(&announcement.user) {
        debug!(
            user = %announcement.user,
            "lockbox: ignoring announcement for non-local user"
        );
        return;
    }

    // Ignore self-announcements (multicast loops them back on some
    // platforms). Compare by hostname since that's the stable
    // identifier the envoy reports about itself.
    let self_host = std::fs::read_to_string("/etc/hostname")
        .unwrap_or_default()
        .trim()
        .to_string();
    if !self_host.is_empty() && announcement.machine == self_host {
        return;
    }

    // Reject announces from pubkeys we've already recorded as left.
    // Without this guard, a still-running peer that missed its own
    // LEAVE (or whose admin evict didn't fully scrub it) would be
    // re-admitted on its next announce and silently leak back into
    // recipients.txt. The check is keyed by age pubkey, so a
    // re-enrolled host under the same hostname (different pubkey) is
    // unaffected. Pre-emptive forget covers the race where a stale
    // entry survived in peers.json from before the LEAVE landed.
    let leaves = leaves::LeavesTable::load(&announcement.user);
    if leaves.leave_time_for(&announcement.pubkey).is_some() {
        let mut snapshot: Option<PeerTable> = None;
        if let Err(e) = PeerTable::modify(&announcement.user, |table| {
            let dropped = table.forget_by_pubkey(&announcement.pubkey);
            if dropped {
                snapshot = Some(table.clone());
                Some("leave-preemptive-forget")
            } else {
                None
            }
        }) {
            warn!(
                error = %e,
                user = %announcement.user,
                "lockbox: peer table save after pre-emptive forget failed"
            );
        }
        if let Some(table) = snapshot {
            if let Some(home) = home_for_user(&announcement.user) {
                if let Err(e) = regenerate_recipients(&announcement.user, &home, &table) {
                    warn!(
                        error = %e,
                        user = %announcement.user,
                        "lockbox: recipients regen after pre-emptive forget failed"
                    );
                }
            }
        }
        debug!(
            user = %announcement.user,
            machine = %announcement.machine,
            pubkey = %announcement.pubkey,
            "lockbox: rejecting announce from pubkey in leaves.json"
        );
        return;
    }

    // ADR-016 follow-up: cryptographic puddle-cert check. When the
    // announce claims a puddle_pubkey AND carries a cert, verify it
    // against the claim before we let the puddle_pubkey reach the
    // peer table. On verification failure, drop the fleet claim
    // entirely (treat as if the announce had empty puddle_pubkey) —
    // the peer is still reachable for standalone purposes, but our
    // recipient resolution won't include them in the fleet's set.
    let mut effective_puddle_pubkey = announcement.puddle_pubkey.clone();
    if !announcement.puddle_pubkey.is_empty() {
        match announcement.puddle_cert.as_ref() {
            Some(cert) => {
                let now = now_secs();
                if let Err(reason) = cert.verify_for_announce(
                    &announcement.pubkey,
                    &announcement.puddle_pubkey,
                    now,
                ) {
                    warn!(
                        user = %announcement.user,
                        host = %announcement.machine,
                        puddle_pubkey = %announcement.puddle_pubkey,
                        reason = %reason,
                        "lockbox: puddle cert rejected — dropping fleet claim from announce"
                    );
                    effective_puddle_pubkey = String::new();
                }
            }
            None => {
                // No cert present. Slice 1/slice 2 peers are valid
                // here — accept under TOFU rules but log at debug so
                // operators can spot the upgrade gap during rollout.
                tracing::debug!(
                    user = %announcement.user,
                    host = %announcement.machine,
                    "lockbox: announce carries puddle_pubkey but no cert — falling back to TOFU"
                );
            }
        }
    }

    // ADR-020: announces without a puddle_pubkey are dropped at the
    // front door. Pre-0.40.0 standalone-mode peers announced with an
    // empty puddle_pubkey; that path is gone. The cert-verification
    // branch above can also strip the puddle pubkey when the cert
    // doesn't validate — those announces fall through here and are
    // logged once at info before being dropped.
    if effective_puddle_pubkey.is_empty() {
        tracing::info!(
            user = %announcement.user,
            host = %announcement.machine,
            "lockbox: dropping announce with empty puddle_pubkey \
             (pre-ADR-020 standalone peer or cert-rejected)"
        );
        return;
    }

    let entry = PeerEntry {
        hostname: announcement.machine.clone(),
        pubkey: announcement.pubkey.clone(),
        sign_pubkey: announcement.sign_pubkey.clone(),
        last_seen: announcement.emitted_at,
        last_seen_addr: Some(from.to_string()),
        // ADR-016: capture the announcer's puddle pubkey so the
        // recipient-resolution path can filter by puddle match.
        puddle_pubkey: effective_puddle_pubkey.clone(),
        // ADR-016 follow-up: TOFU pinning. Left empty here; upsert()
        // pins on first sighting and enforces match on subsequent
        // announces — see PeerTable::upsert and the rejection log
        // emitted via the post-upsert helper below.
        pinned_puddle_pubkey: String::new(),
    };

    // Capture the pre-upsert pin so we can detect when upsert
    // rejected the announce on TOFU mismatch and warn-log with full
    // context. PeerTable::modify takes an FnOnce closure; the pre-pin
    // lookup needs an extra acquire of the load path, but it's cheap
    // (peers.json is small + cached by the OS).
    let prior_pin: Option<String> = PeerTable::load(&announcement.user)
        .peers
        .get(&announcement.machine)
        .map(|p| p.pinned_puddle_pubkey.clone());

    // Lock-protected upsert. Capture a post-update snapshot so we can
    // call regenerate_recipients outside the lock without re-reading
    // the table — only when upsert reported a real_change. Heartbeats
    // also persist (so prune_stale doesn't churn fresh peers every
    // ~120s), but they don't shift the recipient set so we skip the
    // regen.
    //
    // The TOFU comparison uses `effective_puddle_pubkey` (post-cert
    // verification), not `announcement.puddle_pubkey` — a peer whose
    // cert just lapsed becomes "standalone" from our perspective,
    // and we don't want to TOFU-reject them when their cert returns.
    let incoming_fleet = effective_puddle_pubkey.clone();
    let mut snapshot: Option<PeerTable> = None;
    let mut tofu_rejected = false;
    if let Err(e) = PeerTable::modify(&announcement.user, |table| {
        let outcome = table.upsert(entry);
        // Detect TOFU mismatch — `upsert` returns Unchanged on both
        // legitimate identical announces and TOFU rejections;
        // disambiguate by checking whether the pin we observed
        // pre-upsert disagrees with the incoming puddle_pubkey.
        if let Some(pin) = &prior_pin {
            if !pin.is_empty() && pin != &incoming_fleet {
                tofu_rejected = true;
            }
        }
        if outcome.is_real_change() {
            snapshot = Some(table.clone());
        }
        outcome.save_reason()
    }) {
        warn!(error = %e, user = %announcement.user, "lockbox: peer table save failed");
    }
    if tofu_rejected {
        // Drop the announce — peer is announcing a puddle pubkey
        // different from the one we pinned on first sighting. Either
        // a same-LAN spoof, or a legitimate puddle rotation that
        // requires admin intervention (clear with
        // `vigo swarm lockbox forget <host>` then let TOFU re-pin).
        warn!(
            user = %announcement.user,
            host = %announcement.machine,
            pinned = %prior_pin.as_deref().unwrap_or(""),
            announced = %incoming_fleet,
            "lockbox: TOFU rejected announce — puddle pubkey changed for known host. \
             Clear with `vigo swarm lockbox forget <host>` if this is a legitimate rotation."
        );
        return;
    }

    if let Some(table) = snapshot {
        if let Some(home) = home_for_user(&announcement.user) {
            if let Err(e) = regenerate_recipients(&announcement.user, &home, &table) {
                warn!(
                    error = %e,
                    user = %announcement.user,
                    "lockbox: recipients.txt regeneration failed"
                );
            }
        }
    }

    // Puddle-members table: append-only "ever-known members" set
    // backing the long-term-absence gap line in `lockbox status`.
    // Fires for every accepted announce (RealChange OR Heartbeat —
    // the table tracks last_seen too) whose effective puddle
    // pubkey matches the local user's puddle. Standalone callers
    // (no local puddle, or peer announces under a different
    // puddle) skip — they have no membership concept to track.
    //
    // First-call seed: when this is the first matching upsert
    // post-0.39.12 against an already-populated puddle, the table
    // file doesn't yet exist on disk and `load` returns empty. The
    // closure walks peers.json and adopts every entry whose pinned
    // puddle pubkey matches, so the gap line works from the first
    // tick rather than waiting on every member to re-announce.
    if !effective_puddle_pubkey.is_empty() {
        if let Some(home) = home_for_user(&announcement.user) {
            let local_pp = local_puddle_pubkey_hex(&home);
            if !local_pp.is_empty() && local_pp == effective_puddle_pubkey {
                let peers_snapshot = PeerTable::load(&announcement.user);
                let now = announcement.emitted_at;
                let host = announcement.machine.clone();
                let pubkey = announcement.pubkey.clone();
                if let Err(e) = puddle_members::PuddleMembersTable::modify(
                    &announcement.user,
                    |table| {
                        let seeded =
                            table.seed_from_peers_if_empty(&peers_snapshot, &local_pp);
                        let upserted = table.upsert(&pubkey, &host, now);
                        seeded || upserted
                    },
                ) {
                    warn!(
                        error = %e,
                        user = %announcement.user,
                        "lockbox: puddle_members save after announce failed"
                    );
                }
            }
        }
    }

    // Tombstone catch-up: if the announcer's digest differs from
    // this envoy's local digest, pull the announcer's full
    // tombstone list and merge. Common case on announce rejoin
    // after a partition or on fresh enroll. Empty announcer digest
    // skips (pre-0.27.4 peer that doesn't advertise one).
    if !announcement.tombstones_digest.is_empty() {
        maybe_catchup_tombstones(&announcement.user, &announcement.machine, from,
            &announcement.tombstones_digest);
    }

    // Leaves catch-up: identical pattern, distinct table. Lets an
    // envoy that was offline during a LEAVE observe the signed
    // history when it rejoins, instead of just seeing the leaver
    // vanish from the live announce stream. Empty announcer digest
    // skips (pre-0.30.24 peer that doesn't advertise one).
    if !announcement.leaves_digest.is_empty() {
        maybe_catchup_leaves(&announcement.user, &announcement.machine, from,
            &announcement.leaves_digest);
    }

    // Resurrects catch-up: identical pattern, distinct table. Lets
    // an envoy that was offline during a `lockbox resurrect`
    // observe the operator override when it rejoins. Without this,
    // a peer would still hold the un-superseded tombstone and 410
    // any subsequent push of the resurrected filename. Empty
    // announcer digest skips (pre-0.39.x peer that doesn't
    // advertise one).
    if !announcement.resurrects_digest.is_empty() {
        maybe_catchup_resurrects(&announcement.user, &announcement.machine, from,
            &announcement.resurrects_digest);
    }

    // Files catch-up: identical pattern, applied to the ciphertext
    // file set itself. Closes the empty-peers push race in
    // `pusher.rs` (PushJob fires before multicast has populated
    // peers.json → push silently drops → file stranded). Receiver
    // observes a digest mismatch on the next announcement cycle,
    // pulls the announcer's file list, then pulls each missing
    // blob via the existing `GET /swarm/lockbox/pull/...` path.
    // Empty announcer digest skips (pre-0.65.17 peer that doesn't
    // advertise one).
    if !announcement.files_digest.is_empty() {
        maybe_catchup_files(&announcement.user, &announcement.machine, from,
            &announcement.files_digest);
    }
}

/// If the announcer's tombstone digest differs from this envoy's
/// local digest for the same user, spawn a background task that
/// pulls the full list from the announcer and merges it into the
/// local store. Non-blocking; failures log at debug level and
/// retry on the next announce.
fn maybe_catchup_tombstones(
    user: &str,
    source_hostname: &str,
    source_addr: std::net::SocketAddr,
    remote_digest_hex: &str,
) {
    let local = TombstoneTable::load(user);
    let local_digest_hex = hex::encode(local.digest());
    if local_digest_hex == remote_digest_hex {
        return;
    }
    debug!(
        user = %user,
        source = %source_hostname,
        local = %local_digest_hex,
        remote = %remote_digest_hex,
        "lockbox: tombstone digest mismatch, triggering catch-up"
    );

    let user = user.to_string();
    let source_hostname = source_hostname.to_string();
    // Build the URL using the announcement source socket so we
    // pull from the exact peer that told us about the divergence.
    let url = format!(
        "https://{}/swarm/lockbox/tombstones/{}",
        source_addr,
        url_encode_seg(&user)
    );
    tokio::spawn(async move {
        if let Err(e) = catchup_tombstones_from(&user, &source_hostname, &url).await {
            debug!(
                user = %user,
                source = %source_hostname,
                error = %e,
                "lockbox: tombstone catch-up failed"
            );
        }
    });
}

async fn catchup_tombstones_from(user: &str, source_hostname: &str, url: &str) -> Result<()> {
    // Reuse the pusher's mTLS agent. Tiny helper crate boundary
    // crossing — the pusher already exposes `build_agent_for_mod`.
    let tls = match DAEMON_TLS.get() {
        Some(t) => t.clone(),
        None => anyhow::bail!("no TLS certs available for catch-up"),
    };
    let agent = pusher::build_agent_for_mod(&tls)?;
    let url = url.to_string();

    // Blocking HTTP call in spawn_blocking so we don't tie up the
    // tokio runtime.
    let body = tokio::task::spawn_blocking(move || -> Result<Vec<u8>> {
        let mut resp = agent
            .get(&url)
            .call()
            .with_context(|| format!("GET {}", url))?;
        let status = resp.status();
        if !status.is_success() {
            anyhow::bail!("peer returned HTTP {}", status.as_u16());
        }
        let body = resp
            .body_mut()
            .read_to_vec()
            .context("reading tombstones response body")?;
        Ok(body)
    })
    .await
    .context("catch-up join")??;

    let remote: TombstoneTable =
        serde_json::from_slice(&body).context("parsing tombstones response")?;
    let mut merged_snapshot: Option<TombstoneTable> = None;
    TombstoneTable::modify(user, |local| {
        let changed = local.merge(&remote);
        if changed {
            merged_snapshot = Some(local.clone());
        }
        changed
    })
    .context("saving merged tombstones")?;
    if let Some(snapshot) = merged_snapshot {
        // Removed any local files newly-covered by tombstones so
        // the receiver converges to the same file-set as the rest
        // of the mesh. Done outside the lock — file IO that doesn't
        // touch the table is fine concurrently with another modify.
        remove_files_for_active_tombstones(user, &snapshot);
        tracing::info!(
            user = %user,
            source = %source_hostname,
            "lockbox: tombstone catch-up merged"
        );
    }
    Ok(())
}

/// Sibling of `maybe_catchup_tombstones` for the per-user
/// `LeavesTable`. Triggered when an announcer's `leaves_digest`
/// disagrees with this envoy's local digest. Background pull +
/// merge; for each newly-merged entry (peer leaving the mesh) we
/// run `apply_leave` to drop the pubkey from `peers.json`,
/// regenerate `recipients.txt`, and drop `reencrypt.pending` —
/// keeping the catch-up path side-effect-equivalent to receiving
/// the original LEAVE envelope live.
fn maybe_catchup_leaves(
    user: &str,
    source_hostname: &str,
    source_addr: std::net::SocketAddr,
    remote_digest_hex: &str,
) {
    let local = leaves::LeavesTable::load(user);
    let local_digest_hex = hex::encode(local.digest());
    if local_digest_hex == remote_digest_hex {
        return;
    }
    debug!(
        user = %user,
        source = %source_hostname,
        local = %local_digest_hex,
        remote = %remote_digest_hex,
        "lockbox: leaves digest mismatch, triggering catch-up"
    );

    let user = user.to_string();
    let source_hostname = source_hostname.to_string();
    let url = format!(
        "https://{}/swarm/lockbox/leaves/{}",
        source_addr,
        url_encode_seg(&user)
    );
    tokio::spawn(async move {
        if let Err(e) = catchup_leaves_from(&user, &source_hostname, &url).await {
            debug!(
                user = %user,
                source = %source_hostname,
                error = %e,
                "lockbox: leaves catch-up failed"
            );
        }
    });
}

async fn catchup_leaves_from(user: &str, source_hostname: &str, url: &str) -> Result<()> {
    let tls = match DAEMON_TLS.get() {
        Some(t) => t.clone(),
        None => anyhow::bail!("no TLS certs available for catch-up"),
    };
    let agent = pusher::build_agent_for_mod(&tls)?;
    let url = url.to_string();

    let body = tokio::task::spawn_blocking(move || -> Result<Vec<u8>> {
        let mut resp = agent
            .get(&url)
            .call()
            .with_context(|| format!("GET {}", url))?;
        let status = resp.status();
        if !status.is_success() {
            anyhow::bail!("peer returned HTTP {}", status.as_u16());
        }
        let body = resp
            .body_mut()
            .read_to_vec()
            .context("reading leaves response body")?;
        Ok(body)
    })
    .await
    .context("catch-up join")??;

    let remote: leaves::LeavesTable =
        serde_json::from_slice(&body).context("parsing leaves response")?;

    // Identify entries that are new or advanced relative to local
    // BEFORE the merge so we can run apply_leave for each — the
    // signed envelope's side effects (peer table prune + recipients
    // regen + reencrypt sentinel) only fire for entries we hadn't
    // already processed locally.
    let local_pre = leaves::LeavesTable::load(user);
    let new_entries: Vec<(String, leaves::LeaveRecord)> = remote
        .leaves
        .iter()
        .filter(|(pk, r)| {
            local_pre
                .leave_time_for(pk)
                .map(|prior| r.leave_time > prior)
                .unwrap_or(true)
        })
        .map(|(pk, r)| (pk.clone(), r.clone()))
        .collect();

    if new_entries.is_empty() {
        return Ok(());
    }

    let home = match home_for_user(user) {
        Some(h) => h,
        None => {
            debug!(user = %user, "lockbox: leaves catch-up: no resolvable home for user");
            return Ok(());
        }
    };

    for (pubkey, record) in &new_entries {
        if let Err(e) = apply_leave(
            user,
            pubkey,
            &record.hostname_leaving,
            record.leave_time,
            &home,
        ) {
            debug!(
                user = %user,
                pubkey = %pubkey,
                error = %e,
                "lockbox: leaves catch-up: apply_leave failed"
            );
        }
    }

    tracing::info!(
        user = %user,
        source = %source_hostname,
        merged = new_entries.len(),
        "lockbox: leaves catch-up merged"
    );
    Ok(())
}

/// Sibling of `maybe_catchup_tombstones` for the per-user
/// `ResurrectsTable`. Triggered when an announcer's
/// `resurrects_digest` disagrees with this envoy's local digest.
/// Background pull + merge — for each newly-merged resurrect, the
/// effective_tombstone gate flips so subsequent pushes of that
/// filename land instead of 410'ing.
fn maybe_catchup_resurrects(
    user: &str,
    source_hostname: &str,
    source_addr: std::net::SocketAddr,
    remote_digest_hex: &str,
) {
    let local = resurrects::ResurrectsTable::load(user);
    let local_digest_hex = hex::encode(local.digest());
    if local_digest_hex == remote_digest_hex {
        return;
    }
    debug!(
        user = %user,
        source = %source_hostname,
        local = %local_digest_hex,
        remote = %remote_digest_hex,
        "lockbox: resurrects digest mismatch, triggering catch-up"
    );

    let user = user.to_string();
    let source_hostname = source_hostname.to_string();
    let url = format!(
        "https://{}/swarm/lockbox/resurrects/{}",
        source_addr,
        url_encode_seg(&user)
    );
    tokio::spawn(async move {
        if let Err(e) = catchup_resurrects_from(&user, &source_hostname, &url).await {
            debug!(
                user = %user,
                source = %source_hostname,
                error = %e,
                "lockbox: resurrects catch-up failed"
            );
        }
    });
}

async fn catchup_resurrects_from(user: &str, source_hostname: &str, url: &str) -> Result<()> {
    let tls = match DAEMON_TLS.get() {
        Some(t) => t.clone(),
        None => anyhow::bail!("no TLS certs available for catch-up"),
    };
    let agent = pusher::build_agent_for_mod(&tls)?;
    let url = url.to_string();

    let body = tokio::task::spawn_blocking(move || -> Result<Vec<u8>> {
        let mut resp = agent
            .get(&url)
            .call()
            .with_context(|| format!("GET {}", url))?;
        let status = resp.status();
        if !status.is_success() {
            anyhow::bail!("peer returned HTTP {}", status.as_u16());
        }
        let body = resp
            .body_mut()
            .read_to_vec()
            .context("reading resurrects response body")?;
        Ok(body)
    })
    .await
    .context("catch-up join")??;

    let remote: resurrects::ResurrectsTable =
        serde_json::from_slice(&body).context("parsing resurrects response")?;
    let mut merged_count = 0usize;
    resurrects::ResurrectsTable::modify(user, |local| {
        if local.merge(&remote) {
            // Count merged-or-advanced entries. merge() returns true
            // if anything changed; use a quick second pass to count
            // for the log line.
            merged_count = remote
                .resurrects
                .iter()
                .filter(|(name, r)| {
                    local
                        .active(name)
                        .map(|cur| cur.resurrect_time == r.resurrect_time)
                        .unwrap_or(false)
                })
                .count();
            true
        } else {
            false
        }
    })
    .context("saving merged resurrects")?;

    if merged_count > 0 {
        tracing::info!(
            user = %user,
            source = %source_hostname,
            merged = merged_count,
            "lockbox: resurrects catch-up merged"
        );
    }
    Ok(())
}

/// Sibling of `maybe_catchup_tombstones` for the per-user ciphertext
/// file set. Triggered when an announcer's `files_digest` disagrees
/// with this envoy's local digest. Background pull of the announcer's
/// file list, diff against local, then pull each missing blob via the
/// existing `GET /swarm/lockbox/pull/<user>/<filename>` endpoint —
/// the safety net for missed pushes (empty-peers race, offline-during-
/// push, network blip, receiver restart mid-push).
fn maybe_catchup_files(
    user: &str,
    source_hostname: &str,
    source_addr: std::net::SocketAddr,
    remote_digest_hex: &str,
) {
    let home = match home_for_user(user) {
        Some(h) => h,
        None => return,
    };
    let local = snapshot::local_files_for_user(user, &home);
    let local_digest_hex = snapshot::digest_files(&local);
    if local_digest_hex == remote_digest_hex {
        return;
    }
    debug!(
        user = %user,
        source = %source_hostname,
        local = %local_digest_hex,
        remote = %remote_digest_hex,
        "lockbox: files digest mismatch, triggering catch-up"
    );

    let user = user.to_string();
    let source_hostname = source_hostname.to_string();
    let list_url = format!(
        "https://{}/swarm/lockbox/files/{}",
        source_addr,
        url_encode_seg(&user)
    );
    let source_addr_owned = source_addr;
    tokio::spawn(async move {
        if let Err(e) =
            catchup_files_from(&user, &source_hostname, source_addr_owned, &list_url).await
        {
            debug!(
                user = %user,
                source = %source_hostname,
                error = %e,
                "lockbox: files catch-up failed"
            );
        }
    });
}

async fn catchup_files_from(
    user: &str,
    source_hostname: &str,
    source_addr: std::net::SocketAddr,
    list_url: &str,
) -> Result<()> {
    let tls = match DAEMON_TLS.get() {
        Some(t) => t.clone(),
        None => anyhow::bail!("no TLS certs available for catch-up"),
    };
    let agent = pusher::build_agent_for_mod(&tls)?;

    // Step 1: fetch the announcer's file list.
    let list_url_owned = list_url.to_string();
    let agent_for_list = agent.clone();
    let body = tokio::task::spawn_blocking(move || -> Result<Vec<u8>> {
        let mut resp = agent_for_list
            .get(&list_url_owned)
            .call()
            .with_context(|| format!("GET {}", list_url_owned))?;
        let status = resp.status();
        if !status.is_success() {
            anyhow::bail!("peer returned HTTP {}", status.as_u16());
        }
        let body = resp
            .body_mut()
            .read_to_vec()
            .context("reading files list response body")?;
        Ok(body)
    })
    .await
    .context("catch-up join")??;
    let remote: Vec<snapshot::FileEntry> =
        serde_json::from_slice(&body).context("parsing files list response")?;

    // Step 2: diff. Build a sha256 lookup over the current local set
    // and a tombstone/resurrect gate so a file the operator deleted
    // doesn't get resurrected by catch-up — the supersede goes via
    // `vigo swarm lockbox resurrect`, not via re-pulling.
    let home = match home_for_user(user) {
        Some(h) => h,
        None => return Ok(()),
    };
    let local = snapshot::local_files_for_user(user, &home);
    let mut local_sha: std::collections::HashMap<String, String> =
        std::collections::HashMap::with_capacity(local.len());
    for f in &local {
        local_sha.insert(f.name.clone(), f.sha256.clone());
    }
    let tombstones = TombstoneTable::load(user);
    let resurrects_table = resurrects::ResurrectsTable::load(user);

    let mut pulled = 0usize;
    let mut skipped_tombstone = 0usize;
    for rf in &remote {
        if !valid_filename_for_catchup(&rf.name) {
            debug!(user = %user, name = %rf.name, "lockbox: catch-up: skipping invalid filename");
            continue;
        }
        if tombstones::effective_tombstone(&rf.name, &tombstones, &resurrects_table).is_some() {
            skipped_tombstone += 1;
            continue;
        }
        if local_sha.get(&rf.name).map(|s| s == &rf.sha256).unwrap_or(false) {
            continue; // already have it with the same sha
        }
        if let Err(e) = pull_one_file(&agent, source_addr, user, &rf.name).await {
            debug!(
                user = %user,
                name = %rf.name,
                error = %e,
                "lockbox: catch-up: pull failed (will retry on next announcement)"
            );
            continue;
        }
        pulled += 1;
    }

    if pulled > 0 || skipped_tombstone > 0 {
        tracing::info!(
            user = %user,
            source = %source_hostname,
            pulled = pulled,
            skipped_tombstone = skipped_tombstone,
            "lockbox: files catch-up complete"
        );
    }
    Ok(())
}

/// Filename validator for catch-up — matches the server-side
/// `valid_filename` shape (no `/`, no leading `.`, non-empty,
/// bounded length). Defense in depth on the receiving end against
/// a malformed file list from a misbehaving peer.
fn valid_filename_for_catchup(name: &str) -> bool {
    if name.is_empty() || name.len() > 255 {
        return false;
    }
    if name.starts_with('.') {
        return false;
    }
    !name.chars().any(|c| c == '/' || c == '\0')
}

async fn pull_one_file(
    agent: &ureq::Agent,
    source_addr: std::net::SocketAddr,
    user: &str,
    name: &str,
) -> Result<()> {
    let pull_url = format!(
        "https://{}/swarm/lockbox/pull/{}/{}",
        source_addr,
        url_encode_seg(user),
        url_encode_seg(name)
    );
    let agent = agent.clone();
    let url_owned = pull_url.clone();
    let bytes = tokio::task::spawn_blocking(move || -> Result<Vec<u8>> {
        let mut resp = agent
            .get(&url_owned)
            .call()
            .with_context(|| format!("GET {}", url_owned))?;
        let status = resp.status();
        if !status.is_success() {
            anyhow::bail!("peer returned HTTP {}", status.as_u16());
        }
        let body = resp
            .body_mut()
            .read_to_vec()
            .context("reading file body")?;
        Ok(body)
    })
    .await
    .context("pull join")??;

    // Defense in depth: refuse anything that isn't an age envelope.
    // Mirrors the server-side push handler's AGE magic check so the
    // catch-up path can't be coerced into landing arbitrary bytes
    // in ~<user>/lockbox/.
    const AGE_MAGIC: &[u8] = b"age-encryption.org/v1\n";
    if bytes.len() < AGE_MAGIC.len() || &bytes[..AGE_MAGIC.len()] != AGE_MAGIC {
        anyhow::bail!("pulled body is not age-encrypted");
    }

    let home = home_for_user(user).context("no home for user")?;
    let content_dir = config::content_dir(&home);
    if !content_dir.is_dir() {
        // Dormant — user hasn't enrolled or rm -rf'd ~/lockbox/.
        // Same opt-in gate the push handler enforces; don't auto-
        // recreate the dir from catch-up.
        return Ok(());
    }
    let user_string = user.to_string();
    let name_string = name.to_string();
    let target = content_dir.join(&name_string);
    let tmp = content_dir.join(format!(".{}.lockbox.catchup.tmp", name_string));
    tokio::task::spawn_blocking(move || -> Result<()> {
        write_tmp_0600(&tmp, &bytes)
            .with_context(|| format!("writing temp {}", tmp.display()))?;
        if let Err(e) = finalize_user_ciphertext(&tmp, &target, &user_string) {
            let _ = fs::remove_file(&tmp);
            return Err(e).context("finalize ciphertext");
        }
        Ok(())
    })
    .await
    .context("write join")??;
    Ok(())
}

fn write_tmp_0600(tmp: &std::path::Path, data: &[u8]) -> std::io::Result<()> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        let mut f = fs::OpenOptions::new()
            .write(true)
            .create(true)
            .truncate(true)
            .mode(0o600)
            .open(tmp)?;
        f.write_all(data)?;
        f.sync_all().ok();
        Ok(())
    }
    #[cfg(not(unix))]
    {
        let mut f = fs::File::create(tmp)?;
        f.write_all(data)?;
        f.sync_all().ok();
        Ok(())
    }
}

fn remove_files_for_active_tombstones(user: &str, table: &TombstoneTable) {
    let home = match home_for_user(user) {
        Some(h) => h,
        None => return,
    };
    let content_dir = config::content_dir(&home);
    for filename in table.tombstones.keys() {
        let p = content_dir.join(filename);
        if p.exists() {
            if let Err(e) = fs::remove_file(&p) {
                debug!(
                    path = %p.display(),
                    error = %e,
                    "lockbox: catch-up file remove failed"
                );
            }
        }
    }
}

fn url_encode_seg(s: &str) -> String {
    // Minimal encoding — users are `[a-zA-Z0-9._-]+` per the
    // agent's username validator, so no escaping needed. Exists
    // as a hook if the username grammar ever loosens.
    s.to_string()
}

// ---------- prune ----------

fn prune_all() {
    let enrolled = match enrolled_users() {
        Ok(s) => s,
        Err(_) => return,
    };
    let now = now_secs();
    for user in enrolled {
        let mut snapshot: Option<PeerTable> = None;
        if let Err(e) = PeerTable::modify(&user, |table| {
            let pruned = table.prune_stale(now);
            if pruned {
                snapshot = Some(table.clone());
                Some("prune")
            } else {
                None
            }
        }) {
            warn!(error = %e, user = %user, "lockbox: peer table save after prune failed");
            continue;
        }
        if let Some(table) = snapshot {
            if let Some(home) = home_for_user(&user) {
                if let Err(e) = regenerate_recipients(&user, &home, &table) {
                    warn!(
                        error = %e,
                        user = %user,
                        "lockbox: recipients.txt regeneration after prune failed"
                    );
                }
            }
        }
    }
}

// ---------- helpers ----------

/// Public re-export for sibling modules (watcher.rs).
pub fn now_secs_pub() -> i64 {
    now_secs()
}

/// True iff the user's lockbox session helper is currently running
/// (file `<home>/.lockbox/session.unlocked` exists and the PID
/// inside is alive). Used by the watcher (ADR-012) to gate
/// auto-encrypt-on-write — encryption only fires when the user has
/// the session unlocked.
///
/// Stale sentinels (helper crashed without cleanup) read as locked,
/// matching the "no live helper" reality. The watcher's natural retry
/// on the next plaintext write event recovers once the user unlocks
/// again.
pub fn is_session_unlocked_for_home(home: &Path) -> bool {
    let path = config::session_unlocked_path(home);
    let raw = match fs::read_to_string(&path) {
        Ok(s) => s,
        Err(_) => return false,
    };
    let pid: u32 = match raw.trim().parse() {
        Ok(p) => p,
        Err(_) => return false,
    };
    pid_alive(pid)
}

/// Best-effort process-liveness check. Linux uses `/proc/<pid>`;
/// other Unixes fall back to `kill(pid, 0)` (returns 0 when the
/// process exists; ESRCH means gone). Mirrors session.rs's private
/// `pid_alive` so we don't bloat session.rs's public surface.
fn pid_alive(pid: u32) -> bool {
    if pid == 0 {
        return false;
    }
    #[cfg(target_os = "linux")]
    {
        std::path::Path::new(&format!("/proc/{}", pid)).exists()
    }
    #[cfg(not(target_os = "linux"))]
    {
        let rc = unsafe { libc::kill(pid as libc::pid_t, 0) };
        if rc == 0 {
            return true;
        }
        let err = std::io::Error::last_os_error();
        err.raw_os_error() != Some(libc::ESRCH)
    }
}

/// Walk `~<user>/lockbox/`, encrypting every plaintext file in
/// place per ADR-012's unlock-sweep. Called from `cmd_unlock` after
/// the session helper is up. Already-ciphertext files are skipped
/// naturally via the age-magic check; `.failed` artifacts are skipped
/// so a permanently-broken file doesn't loop on every unlock. Each
/// per-file outcome is logged + counted; the final summary line
/// matches ADR-012 §5.
pub fn sweep_plaintext_on_unlock(home: &Path, username: &str) {
    let content = config::content_dir(home);
    if !content.is_dir() {
        return;
    }
    let recipients = match config::load_recipients(home) {
        Ok(r) => r,
        Err(e) => {
            warn!(
                error = %e,
                "lockbox sweep: cannot load recipients; skipping plaintext drain"
            );
            return;
        }
    };

    let entries = match fs::read_dir(&content) {
        Ok(rd) => rd,
        Err(e) => {
            warn!(path = %content.display(), error = %e, "lockbox sweep: read_dir failed");
            return;
        }
    };

    let mut encrypted = 0usize;
    let mut failed = 0usize;
    for entry in entries.flatten() {
        let path = entry.path();
        if !path.is_file() {
            continue;
        }
        let name = match path.file_name().and_then(|n| n.to_str()) {
            Some(n) => n.to_string(),
            None => continue,
        };
        if name.starts_with('.') {
            continue;
        }
        if auto_encrypt::is_failed_artifact_name(&name) {
            continue;
        }
        // Skip already-ciphertext files via the same magic check the
        // watcher uses. Keep the read window small (just enough for
        // the magic line) so we don't pull a 500 MiB ciphertext into
        // memory just to decide it's already done.
        match crypt::is_age_encrypted(&path) {
            Ok(true) => continue,
            Ok(false) => {}
            Err(e) => {
                warn!(
                    path = %path.display(),
                    error = %e,
                    "lockbox sweep: cannot read magic, skipping"
                );
                continue;
            }
        }

        match auto_encrypt::encrypt_in_place(home, username, &name, &recipients) {
            Ok(size) => {
                info!(
                    user = %username,
                    filename = %name,
                    size_bytes = size,
                    "lockbox sweep: encrypted"
                );
                encrypted += 1;
            }
            Err(auto_encrypt::AutoEncryptError::Terminal(msg)) => {
                warn!(
                    user = %username,
                    filename = %name,
                    "lockbox sweep: cannot encrypt: {}; left plaintext",
                    msg
                );
                failed += 1;
            }
            Err(auto_encrypt::AutoEncryptError::Unrecoverable(msg)) => {
                let ts = now_secs();
                match auto_encrypt::rename_to_failed(home, &name, ts) {
                    Ok(dst) => {
                        warn!(
                            user = %username,
                            filename = %name,
                            renamed = %dst.display(),
                            "lockbox sweep: encrypt failed permanently: {}; renamed",
                            msg
                        );
                    }
                    Err(rename_err) => {
                        warn!(
                            user = %username,
                            filename = %name,
                            error = %rename_err,
                            "lockbox sweep: encrypt failed permanently: {}; rename also failed",
                            msg
                        );
                    }
                }
                failed += 1;
            }
            Err(auto_encrypt::AutoEncryptError::Transient(msg)) => {
                // Sweep is single-shot per file — the user just unlocked
                // and we don't want to stall the CLI for retries. Treat
                // as actionable: leave plaintext, log, move on. The next
                // watcher event picks it up and the watcher's own retry
                // schedule applies.
                warn!(
                    user = %username,
                    filename = %name,
                    "lockbox sweep: transient error: {}; left plaintext, watcher will retry",
                    msg
                );
                failed += 1;
            }
        }
    }
    if encrypted > 0 || failed > 0 {
        info!(
            user = %username,
            encrypted = encrypted,
            failed = failed,
            "lockbox sweep: complete"
        );
        println!(
            "lockbox sweep: {} file(s) encrypted, {} failed for {}",
            encrypted, failed, username
        );
    }
}

/// Public helper so the pusher can stamp tombstones with this host's
/// reported hostname without re-reading /etc/hostname everywhere.
pub fn read_hostname() -> String {
    std::fs::read_to_string("/etc/hostname")
        .unwrap_or_else(|_| "unknown".to_string())
        .trim()
        .to_string()
}

/// Public re-export for sibling modules (pusher.rs, watcher.rs).
pub fn home_for_user_pub(username: &str) -> Option<PathBuf> {
    home_for_user(username)
}

/// Public re-export for sibling subsystems that need to chown files
/// they drop into a user's home (currently `puddle`'s CLI verbs).
/// Idempotent under self-target — safe in user context as well as
/// root.
#[cfg(unix)]
pub fn chown_to_user_pub(path: &Path, username: &str) -> Result<()> {
    chown_to_user(path, username)
}

#[cfg(not(unix))]
pub fn chown_to_user_pub(_path: &Path, _username: &str) -> Result<()> {
    Ok(())
}

/// Best-effort writer for user-side state mirrors (tombstones.json,
/// leaves.json — daemon-owned tables that the CLI needs to read as
/// the unprivileged user). Looks up the user's home, writes JSON via
/// temp+rename with mode 0600, then chowns the result to the user.
/// Any failure is logged at debug and swallowed: the canonical
/// daemon-side state has already been written by the caller, so a
/// missing or stale mirror only degrades CLI visibility — never
/// correctness. No-op when the user has no resolvable home.
pub(crate) fn write_user_mirror_json(user: &str, filename: &str, json: &str) {
    let home = match home_for_user(user) {
        Some(h) => h,
        None => {
            debug!(user = %user, "lockbox: skipping user-mirror write (no home)");
            return;
        }
    };
    let dot = home.join(".lockbox");
    if let Err(e) = fs::create_dir_all(&dot) {
        debug!(path = %dot.display(), error = %e, "lockbox: mkdir for user mirror");
        return;
    }
    // Chown the dir to the user when the daemon (root) creates it.
    // Without this, a post-scrub recreate ends up root-owned and the
    // next user-mode `lockbox unlock` fails to bootstrap because
    // chowning files into a root-owned dir from user context errors
    // with EPERM. chown_to_user is idempotent — no-op when already
    // user-owned. Best-effort like the rest of this writer.
    #[cfg(unix)]
    if let Err(e) = chown_to_user(&dot, user) {
        debug!(path = %dot.display(), error = %e, "lockbox: chown user-mirror dir");
    }
    let path = dot.join(filename);
    let tmp = path.with_extension("json.tmp");
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        let mut f = match fs::OpenOptions::new()
            .write(true)
            .create(true)
            .truncate(true)
            .mode(0o600)
            .open(&tmp)
        {
            Ok(f) => f,
            Err(e) => {
                debug!(path = %tmp.display(), error = %e, "lockbox: open user-mirror tmp");
                return;
            }
        };
        if let Err(e) = f.write_all(json.as_bytes()) {
            debug!(path = %tmp.display(), error = %e, "lockbox: write user-mirror tmp");
            return;
        }
        f.sync_all().ok();
    }
    #[cfg(not(unix))]
    {
        if let Err(e) = fs::write(&tmp, json.as_bytes()) {
            debug!(path = %tmp.display(), error = %e, "lockbox: write user-mirror tmp");
            return;
        }
    }
    if let Err(e) = fs::rename(&tmp, &path) {
        debug!(path = %path.display(), error = %e, "lockbox: rename user-mirror");
        return;
    }
    #[cfg(unix)]
    if let Err(e) = chown_to_user(&path, user) {
        debug!(path = %path.display(), error = %e, "lockbox: chown user-mirror");
    }
}

/// Enumerate all users on this envoy who have completed lockbox
/// enrollment (identity.age exists under their home).
pub(crate) fn enrolled_users() -> Result<HashSet<String>> {
    let mut out = HashSet::new();
    let passwd = fs::read_to_string("/etc/passwd").context("reading /etc/passwd")?;
    for line in passwd.lines() {
        let fields: Vec<&str> = line.split(':').collect();
        if fields.len() < 7 {
            continue;
        }
        let name = fields[0];
        let home = PathBuf::from(fields[5]);
        if config::is_enrolled(&home) {
            out.insert(name.to_string());
        }
    }
    Ok(out)
}

pub(crate) fn home_for_user(username: &str) -> Option<PathBuf> {
    let passwd = fs::read_to_string("/etc/passwd").ok()?;
    for line in passwd.lines() {
        let fields: Vec<&str> = line.split(':').collect();
        if fields.len() >= 7 && fields[0] == username {
            return Some(PathBuf::from(fields[5]));
        }
    }
    None
}

/// Ensure this envoy has an Ed25519 signing identity for `user`.
/// Generates one if missing, writes both private (0600) and public
/// (0644) forms under `/var/lib/vigo/swarm/lockbox/<user>/`. Idempotent.
///
/// Daemon-owned (not user-wrapped) because DELETE envelopes need to
/// be signable autonomously — requiring the user to be unlocked for
/// deletions would break the pusher's reactive model. The tradeoff
/// is documented in `docs/engineering/subsystems/lockbox.md`:
/// a compromised signing key authorizes destructive operations on
/// this envoy's behalf until peers `vigo swarm lockbox forget <host>` it,
/// but can't decrypt content.
fn ensure_signing_identity(user: &str) -> Result<()> {
    let key_path = config::signing_key_path(user);
    if key_path.exists() {
        return Ok(());
    }
    let dir = config::agent_state_dir(user);
    fs::create_dir_all(&dir).with_context(|| format!("creating {}", dir.display()))?;

    let keypair = crypt::generate_signing_keypair();
    let pubkey_text = crypt::encode_sign_pubkey(&keypair.verifying_key());

    // Private: 32 bytes, 0600 root-owned.
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        let mut f = fs::OpenOptions::new()
            .write(true)
            .create_new(true)
            .mode(0o600)
            .open(&key_path)
            .with_context(|| format!("creating {}", key_path.display()))?;
        f.write_all(&keypair.to_bytes())?;
        f.sync_all().ok();
    }
    #[cfg(not(unix))]
    {
        fs::write(&key_path, keypair.to_bytes())
            .with_context(|| format!("writing {}", key_path.display()))?;
    }

    // Public: 0644 root-owned.
    let pub_path = config::signing_pubkey_path(user);
    let content = format!("{}\n", pubkey_text);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        let mut f = fs::OpenOptions::new()
            .write(true)
            .create_new(true)
            .mode(0o644)
            .open(&pub_path)
            .with_context(|| format!("creating {}", pub_path.display()))?;
        f.write_all(content.as_bytes())?;
        f.sync_all().ok();
    }
    #[cfg(not(unix))]
    {
        fs::write(&pub_path, &content)
            .with_context(|| format!("writing {}", pub_path.display()))?;
    }

    tracing::info!(
        user = %user,
        pubkey = %pubkey_text,
        "lockbox: generated signing identity"
    );
    Ok(())
}

/// Read this envoy's Ed25519 signing pubkey for `user` from
/// `/var/lib/vigo/swarm/lockbox/<user>/signing.pub`. Returns an error
/// when the file is missing or malformed; callers typically treat
/// error as "skip advertising a signing pubkey this cycle."
fn load_local_sign_pubkey(user: &str) -> Result<String> {
    let path = config::signing_pubkey_path(user);
    let text = fs::read_to_string(&path)
        .with_context(|| format!("reading {}", path.display()))?;
    for line in text.lines() {
        let trimmed = line.trim();
        if trimmed.is_empty() || trimmed.starts_with('#') {
            continue;
        }
        return Ok(trimmed.to_string());
    }
    anyhow::bail!("no signing pubkey found in {}", path.display())
}

/// Load this envoy's Ed25519 signing key for `user`. Daemon-local —
/// never shipped over the wire. Used by the pusher to sign DELETE
/// envelopes.
pub fn load_local_signing_key(user: &str) -> Result<ed25519_dalek::SigningKey> {
    let path = config::signing_key_path(user);
    let bytes = fs::read(&path).with_context(|| format!("reading {}", path.display()))?;
    if bytes.len() != 32 {
        anyhow::bail!(
            "{}: expected 32-byte Ed25519 private key, got {} bytes",
            path.display(),
            bytes.len()
        );
    }
    let mut arr = [0u8; 32];
    arr.copy_from_slice(&bytes);
    Ok(ed25519_dalek::SigningKey::from_bytes(&arr))
}

/// Load the puddle membership cert for an outgoing announce. Returns
/// `None` when the cert is missing on disk, doesn't match the
/// announcer's age + puddle pubkeys (stale after a rotation), or has
/// already expired — receivers would reject any of those, so it's
/// cheaper to omit than to ship and let them warn-log. The user's
/// recovery is to run `vigo swarm lockbox puddle renew-cert`.
fn load_announce_puddle_cert(
    home: &Path,
    age_pubkey: &str,
    puddle_pubkey: &str,
) -> Option<puddle_cert::PuddleCert> {
    if puddle_pubkey.is_empty() {
        return None;
    }
    let cert = puddle_cert::PuddleCert::load(home)?;
    let now = now_secs();
    if cert.envoy_age_pubkey != age_pubkey {
        tracing::debug!(
            age_in_cert = %cert.envoy_age_pubkey,
            announce_age = %age_pubkey,
            "lockbox: puddle_cert.json doesn't match local age pubkey; not embedding"
        );
        return None;
    }
    if cert.puddle_pubkey != puddle_pubkey {
        tracing::debug!(
            fleet_in_cert = %cert.puddle_pubkey,
            announce_fleet = %puddle_pubkey,
            "lockbox: puddle_cert.json doesn't match local puddle pubkey; not embedding"
        );
        return None;
    }
    if now > cert.expires_at {
        tracing::debug!(
            expires_at = cert.expires_at,
            now,
            "lockbox: puddle_cert.json expired; run `lockbox puddle renew-cert`"
        );
        return None;
    }
    Some(cert)
}

/// Read the user's puddle pubkey from `~/.vigo-puddle/identity.pub`,
/// stripped of the `ed25519:` prefix and lowercased. Returns an empty
/// string when puddle is not initialized for this user (the
/// "standalone" path of ADR-016) or when the file is unreadable.
///
/// Cheap (one filesystem read) and called every announce tick, so
/// no caching — late `puddle init` flips the user into fleet
/// mode within one tick without an agent restart.
pub(crate) fn local_puddle_pubkey_hex(home: &Path) -> String {
    match crate::swarm::puddle::local_puddle_pubkey(home) {
        Ok(Some(text)) => text
            .strip_prefix("ed25519:")
            .map(|s| s.to_string())
            .unwrap_or(text)
            .trim()
            .to_ascii_lowercase(),
        _ => String::new(),
    }
}

/// Read the local envoy's own pubkey — this is the first line in
/// `recipients.txt` that was written by `init`/`enroll`. Using the
/// recipients file instead of re-deriving from the wrapped identity
/// means we never need to prompt for the password to announce.
fn load_local_pubkey(home: &Path) -> Result<String> {
    let path = config::recipients_path(home);
    let text = fs::read_to_string(&path)
        .with_context(|| format!("reading {}", path.display()))?;
    for line in text.lines() {
        let trimmed = line.trim();
        if trimmed.is_empty() || trimmed.starts_with('#') {
            continue;
        }
        return Ok(trimmed.to_string());
    }
    anyhow::bail!("no pubkey found in {}", path.display())
}

/// Regenerate `~<user>/.lockbox/recipients.txt` from the current
/// peer table. Preserves this envoy's own pubkey (first line) and
/// appends live peers sorted by pubkey, filtered against
/// `leaves.json` so a peer that left can never leak back into
/// recipients even if a stale entry survived in `peers.json`.
/// Writes atomically via temp + rename; chowns the resulting file
/// to the user.
fn regenerate_recipients(user: &str, home: &Path, table: &PeerTable) -> Result<()> {
    let path = config::recipients_path(home);
    let self_pubkey = load_local_pubkey(home)?;
    let leaves = leaves::LeavesTable::load(user);
    let now = now_secs();

    // Recipients are per-envoy age pubkeys, not the puddle pubkey.
    // ADR-016 keeps per-envoy keys deliberately: single-envoy
    // compromise must not decrypt the whole fleet's corpus. The
    // puddle pubkey only scopes which peers we accept as recipient
    // candidates — each peer still contributes its own age x25519
    // pubkey to the resulting recipients.txt.
    //
    // ADR-018 rekey window: during the 24h graceful retirement
    // window after a puddle rekey, surviving envoys still
    // announce under the old puddle pubkey until they finish their
    // own rekey. Without widening the filter the founder's
    // mid-window writes encrypt to a smaller recipient set than
    // steady state. So the accepted list is (local fleet) ∪ (every
    // unexpired entry in `~/.vigo-puddle/retired_puddles.json`).
    let local_fleet = local_puddle_pubkey_hex(home);
    let mut accepted: Vec<String> = Vec::new();
    if !local_fleet.is_empty() {
        accepted.push(local_fleet);
        match crate::swarm::puddle::rekey::load_retired(home) {
            Ok(entries) => {
                for e in entries {
                    if e.expires_at <= now {
                        continue;
                    }
                    let bare = e
                        .old_puddle_pubkey
                        .strip_prefix("ed25519:")
                        .unwrap_or(&e.old_puddle_pubkey)
                        .trim()
                        .to_ascii_lowercase();
                    if !bare.is_empty() && !accepted.contains(&bare) {
                        accepted.push(bare);
                    }
                }
            }
            Err(e) => {
                debug!(
                    error = %e,
                    "lockbox: retired_puddles.json unreadable — proceeding without rekey-window widening"
                );
            }
        }
    }

    let mut all: Vec<String> = Vec::new();
    all.push(self_pubkey);
    for pk in table.live_pubkeys_for_puddles(now, &accepted) {
        if leaves.leave_time_for(&pk).is_some() {
            // Defense in depth: handle_incoming should already have
            // pre-emptively forgotten this peer, but skip here too in
            // case an older entry slipped through.
            continue;
        }
        if !all.contains(&pk) {
            all.push(pk);
        }
    }

    let tmp = path.with_extension("txt.tmp");
    {
        let mut f = fs::OpenOptions::new()
            .write(true)
            .create(true)
            .truncate(true)
            .open(&tmp)
            .with_context(|| format!("creating {}", tmp.display()))?;
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let perms = fs::Permissions::from_mode(0o600);
            if let Err(e) = f.set_permissions(perms) {
                debug!(path = %tmp.display(), error = %e, "lockbox: set_permissions on tmp");
            }
        }
        for line in &all {
            writeln!(f, "{}", line)?;
        }
        f.sync_all().ok();
    }
    fs::rename(&tmp, &path).with_context(|| format!("renaming to {}", path.display()))?;
    #[cfg(unix)]
    let _ = chown_to_user(&path, user);
    Ok(())
}

fn now_secs() -> i64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs() as i64)
        .unwrap_or(0)
}

/// Public helper for the `forget` CLI verb: regenerate
/// `recipients.txt` from the current peer table immediately, instead
/// of waiting for the next daemon announce cycle. Loads the table,
/// writes recipients.txt, done.
pub fn regenerate_recipients_now(user: &str, home: &Path) -> Result<()> {
    let table = PeerTable::load(user);
    regenerate_recipients(user, home, &table)
}

/// Shared local-apply for a LEAVE. Called by:
///
/// - `POST /lockbox/leaves/<user>` handler after verifying a signed
///   envelope from a peer
/// - `vigo swarm lockbox leave` after signing its own envelope
/// - `vigo swarm lockbox _apply-evict` after the admin task dispatch lands
///
/// Steps: drop the pubkey from `peers.json`, persist the table,
/// record the LEAVE in `leaves.json`, regenerate `recipients.txt`
/// (omitting the pubkey), drop the `reencrypt.pending` sentinel so
/// the next `vigo swarm lockbox unlock` (or the running session helper)
/// rewraps existing ciphertext. Idempotent — repeat calls with the
/// same args are no-ops after the first.
pub fn apply_leave(
    user: &str,
    age_pubkey_leaving: &str,
    hostname_leaving: &str,
    leave_time: i64,
    home: &Path,
) -> Result<()> {
    // 1. Peer table: drop the pubkey if still present, under the
    //    per-user peers lock so a concurrent announce-driven upsert
    //    can't undo this drop.
    let mut peer_table_after: Option<PeerTable> = None;
    PeerTable::modify(user, |table| {
        let dropped = table.forget_by_pubkey(age_pubkey_leaving);
        if dropped {
            peer_table_after = Some(table.clone());
            Some("leave")
        } else {
            None
        }
    })?;
    let peer_was_present = peer_table_after.is_some();

    // 2. Leave table: record the leave (monotonic on leave_time).
    //    Leaves are mutually-independent of peers but get the same
    //    treatment for symmetry and to handle simultaneous LEAVE
    //    envelopes for the same user from different sources.
    let mut leaves_changed = false;
    leaves::LeavesTable::modify(user, |leaves| {
        let record = leaves::LeaveRecord {
            leave_time,
            hostname_leaving: hostname_leaving.to_string(),
        };
        leaves_changed = leaves.upsert(age_pubkey_leaving, record);
        leaves_changed
    })?;

    // 3. Puddle-members table: drop the leaving age pubkey so the
    //    `lockbox status` denominator shrinks. Authoritative
    //    eviction — the membership table is otherwise append-only,
    //    so this is the only path (alongside admin `forget`) that
    //    removes an entry. Idempotent: repeat LEAVEs against an
    //    already-evicted pubkey are a no-op.
    if let Err(e) = puddle_members::PuddleMembersTable::modify(user, |table| {
        table.forget_by_pubkey(age_pubkey_leaving)
    }) {
        warn!(
            user = %user,
            error = %e,
            "lockbox: puddle_members save after leave failed"
        );
    }

    // 4. Recipients.txt — regenerate when a peer was dropped, using
    //    the post-drop snapshot we captured under the lock.
    if let Some(table) = peer_table_after.as_ref() {
        regenerate_recipients(user, home, table)?;
    }

    // 5. Reencrypt sentinel — drop only when something actually
    //    changed, else we'd churn the session helper across repeated
    //    idempotent deliveries.
    if peer_was_present || leaves_changed {
        let sentinel = config::reencrypt_pending_path(home);
        if let Some(parent) = sentinel.parent() {
            let _ = fs::create_dir_all(parent);
            #[cfg(unix)]
            let _ = chown_to_user(parent, user);
        }
        if !sentinel.exists() {
            #[cfg(unix)]
            let open_result = {
                use std::os::unix::fs::OpenOptionsExt;
                fs::OpenOptions::new()
                    .create(true)
                    .write(true)
                    .truncate(true)
                    .mode(0o600)
                    .open(&sentinel)
            };
            #[cfg(not(unix))]
            let open_result = fs::OpenOptions::new()
                .create(true)
                .write(true)
                .truncate(true)
                .open(&sentinel);
            match open_result {
                Ok(_) => {
                    #[cfg(unix)]
                    if let Err(e) = chown_to_user(&sentinel, user) {
                        warn!(
                            path = %sentinel.display(),
                            user = %user,
                            error = %e,
                            "lockbox: chown reencrypt.pending failed (file is 0600 root-owned)"
                        );
                    }
                }
                Err(e) => {
                    warn!(path = %sentinel.display(), error = %e, "lockbox: failed to drop reencrypt.pending");
                }
            }
        }
    }

    Ok(())
}

/// Periodic unicast-announce loop for each enrolled user's
/// `cross_subnet_peers:`. Same 30-second cadence as multicast. Runs in a
/// blocking task per send because the ureq agent is synchronous.
async fn cross_subnet_peers_announce_loop(
    machine: String,
    tls: TlsCerts,
    cancel: CancellationToken,
) {
    let agent = match pusher_agent(&tls) {
        Ok(a) => a,
        Err(e) => {
            warn!(error = %e, "lockbox cross_subnet_peers: agent build failed");
            return;
        }
    };
    info!("lockbox cross_subnet_peers announce loop started");

    let mut interval = tokio::time::interval(ANNOUNCE_INTERVAL);
    interval.tick().await; // fire first tick immediately, then cadence
    loop {
        tokio::select! {
            _ = cancel.cancelled() => return,
            _ = interval.tick() => {
                if let Err(e) = cross_subnet_peers_announce_once(&machine, &agent).await {
                    debug!(error = %e, "cross_subnet_peers announce pass error");
                }
            }
        }
    }
}

async fn cross_subnet_peers_announce_once(machine: &str, agent: &ureq::Agent) -> Result<()> {
    for user in enrolled_users()? {
        let home = match home_for_user(&user) {
            Some(h) => h,
            None => continue,
        };
        let pubkey = match load_local_pubkey(&home) {
            Ok(p) => p,
            Err(_) => continue,
        };
        let cfg = config::load_config(&home);
        if cfg.cross_subnet_peers.is_empty() {
            continue;
        }
        let sign_pubkey = load_local_sign_pubkey(&user).unwrap_or_default();
        let tombstones_digest =
            hex::encode(TombstoneTable::load(&user).digest());
        let leaves_digest =
            hex::encode(leaves::LeavesTable::load(&user).digest());
        let resurrects_digest =
            hex::encode(resurrects::ResurrectsTable::load(&user).digest());
        // Files digest — same shape and SoT helper the multicast
        // announce-build uses; identical state must produce identical
        // bytes here.
        let files_digest =
            snapshot::digest_files(&snapshot::local_files_for_user(&user, &home));
        // ADR-016: include the user's puddle pubkey when present
        // so peers can scope recipient resolution to the same fleet.
        // Empty when puddle isn't initialized for this user (the
        // standalone mode the legacy gossip pattern serves).
        let puddle_pubkey = local_puddle_pubkey_hex(&home);
        // ADR-016 follow-up: embed the puddle membership certificate
        // when one's on disk and matches our age + puddle pubkeys.
        // Receivers verify the signature, closing the same-LAN
        // first-contact race that TOFU has on its own. None when the
        // user hasn't run `lockbox puddle renew-cert` (or when the
        // on-disk cert is stale and they need to renew).
        let puddle_cert = load_announce_puddle_cert(&home, &pubkey, &puddle_pubkey);
        let ann = Announcement {
            machine: machine.to_string(),
            user: user.clone(),
            pubkey,
            sign_pubkey,
            emitted_at: now_secs(),
            port: PEER_PORT,
            generation: 0,
            tombstones_digest,
            leaves_digest,
            resurrects_digest,
            files_digest,
            puddle_pubkey,
            puddle_cert,
        };
        let body = serde_json::to_vec(&ann).context("serializing announcement")?;
        for host in &cfg.cross_subnet_peers {
            let url = format!("https://{}:{}/swarm/lockbox/announce", host, PEER_PORT);
            let body = body.clone();
            let agent = agent.clone();
            // Fire-and-forget in spawn_blocking — announcer shouldn't
            // block the tokio runtime on a slow peer.
            let host = host.clone();
            tokio::task::spawn_blocking(move || {
                match agent
                    .post(&url)
                    .header("Content-Type", "application/json")
                    .send(body)
                {
                    Ok(r) if r.status().is_success() => {
                        debug!(host = %host, "cross_subnet_peers announce accepted");
                    }
                    Ok(r) => debug!(host = %host, status = %r.status(), "cross_subnet_peers announce rejected"),
                    Err(e) => debug!(host = %host, error = %e, "cross_subnet_peers announce failed"),
                }
            });
        }
    }
    Ok(())
}

/// Build the same mTLS agent the pusher uses. Private re-export of
/// `pusher::build_agent` so we don't duplicate TLS setup.
fn pusher_agent(tls: &TlsCerts) -> Result<ureq::Agent> {
    // Delegate to pusher::build_agent via a private shim so the
    // crate-private function can be reused without a public API.
    pusher::build_agent_for_mod(tls)
}

/// Finalize a freshly-written ciphertext temp file: enforce mode
/// 0600 on the temp, atomically rename to `dst`, then chown `dst`
/// to `username`. Both write paths into `~<user>/lockbox/` — the
/// encrypt CLI verb (cmd_encrypt, reencrypt_one, both run as root
/// when invoked through the sudo-only `/usr/local/sbin/vigo` binary)
/// and the agent's push receiver (always root) — call this so the
/// resulting ciphertext lands as `<user>:<group> 0600` regardless of
/// which root context wrote it.
#[cfg(unix)]
pub(crate) fn finalize_user_ciphertext(
    tmp: &Path,
    dst: &Path,
    username: &str,
) -> Result<()> {
    use std::os::unix::fs::PermissionsExt;
    fs::set_permissions(tmp, fs::Permissions::from_mode(0o600))
        .with_context(|| format!("set 0600 on {}", tmp.display()))?;
    fs::rename(tmp, dst)
        .with_context(|| format!("rename {} → {}", tmp.display(), dst.display()))?;
    chown_to_user(dst, username)
        .with_context(|| format!("chown {} to {}", dst.display(), username))?;
    Ok(())
}

#[cfg(not(unix))]
pub(crate) fn finalize_user_ciphertext(
    tmp: &Path,
    dst: &Path,
    _username: &str,
) -> Result<()> {
    fs::rename(tmp, dst)
        .with_context(|| format!("rename {} → {}", tmp.display(), dst.display()))?;
    Ok(())
}

#[cfg(unix)]
pub(crate) fn chown_to_user(path: &Path, username: &str) -> Result<()> {
    use std::ffi::CString;
    use std::os::unix::fs::MetadataExt;

    // Look up uid + primary gid from /etc/passwd.
    let passwd = fs::read_to_string("/etc/passwd")
        .context("reading /etc/passwd for uid lookup")?;
    for line in passwd.lines() {
        let fields: Vec<&str> = line.split(':').collect();
        if fields.len() >= 4 && fields[0] == username {
            let uid: u32 = fields[2]
                .parse()
                .with_context(|| format!("parsing uid for {}", username))?;
            let gid: u32 = fields[3]
                .parse()
                .with_context(|| format!("parsing gid for {}", username))?;

            // Skip chown if already correct (idempotent / avoids needless
            // syscall + permission-check noise on re-convergence).
            if let Ok(meta) = fs::metadata(path) {
                if meta.uid() == uid && meta.gid() == gid {
                    return Ok(());
                }
            }

            let c_path = CString::new(path.to_string_lossy().as_bytes())
                .map_err(|_| anyhow::anyhow!("path has interior NUL"))?;
            let rc = unsafe { libc::chown(c_path.as_ptr(), uid, gid) };
            if rc != 0 {
                return Err(std::io::Error::last_os_error()).with_context(|| {
                    format!("chown {} to {}:{}", path.display(), uid, gid)
                });
            }
            return Ok(());
        }
    }
    anyhow::bail!("user {} not found in /etc/passwd", username)
}

