// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Sigil — per-user encrypted P2P directory.
//!
//! A user places a file into `~/sigil/` on one envoy; sigil
//! encrypts it to every enrolled envoy in the user's mesh and syncs
//! the ciphertext so every envoy holds the same age-encrypted bytes
//! at rest. Files are never plaintext on disk in `~/sigil/` — the
//! whole point.
//!
//! Relationship to echo: both are per-user LAN-first P2P sync
//! subsystems. Echo is unencrypted plaintext sync (LAN-trusted).
//! Sigil is always-encrypted sync where only envoys in the mesh
//! can decrypt, and decryption requires the user's sigil
//! password.
//!
//! Phase 1 scope: Linux + macOS only. No PAM, no FUSE, no filename
//! encryption, no key escrow. See `plan_sigil.md` for the full
//! spec.
//!
//! Phase 1 Week 1: wrapping format, `init` / `enroll` / `status`,
//! user-executor integration.
//! Phase 1 Week 2 (this code adds): multicast discovery, peer table,
//! `recipients.txt` regeneration, HTTPS routes (push/pull/announce),
//! daemon lifecycle. Session helper + remaining verbs follow in week 3.

pub mod cli;
pub mod config;
pub mod crypt;
pub mod discovery;
pub mod leaves;
pub mod locks;
pub mod pusher;
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

/// The swarm peer server port — sigil push/pull rides here.
const PEER_PORT: u16 = crate::swarm::server::DEFAULT_PORT;

/// Inbound-announcement sender, published when the sigil daemon
/// starts. The HTTPS `/sigil/announce` handler uses this to feed
/// unicast announcements from `cross_subnet_peers` into the same reconciler
/// path that multicast uses. `None` before spawn.
static INCOMING_ANNOUNCE: std::sync::OnceLock<mpsc::Sender<IncomingAnnouncement>> =
    std::sync::OnceLock::new();

/// TLS certs stashed by the daemon at startup so the tombstone
/// catch-up path can build its own mTLS HTTP client without having
/// them threaded through the announcement-handling stack. `None`
/// when the daemon spawned without TLS (fan-out disabled posture).
static DAEMON_TLS: std::sync::OnceLock<TlsCerts> = std::sync::OnceLock::new();

/// Deliver a unicast announcement into the sigil reconciler.
/// Returns true on success, false if the daemon isn't running or the
/// channel is full. Used by `POST /sigil/announce`.
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

/// Spawn the sigil daemon. Runs until the cancellation token
/// fires. No-op if sigil is not wanted on this envoy (no enrolled
/// users exist yet — the daemon re-discovers enrolled users every
/// cycle so brand-new enrollments don't require a daemon restart).
///
/// `tls` is the same mTLS bundle swarm uses. When absent, the
/// fan-out pusher is disabled but everything else runs (discovery,
/// peer table maintenance, `recipients.txt` regen, serving
/// `/sigil/pull/...` GETs). Matches echo's posture.
pub fn spawn(tls: Option<TlsCerts>, cancel: CancellationToken) {
    let machine = std::fs::read_to_string("/etc/hostname")
        .unwrap_or_else(|_| "unknown".to_string())
        .trim()
        .to_string();

    let (out_tx, in_tx, in_rx) = discovery::spawn(cancel.clone());
    // Publish the inbound sender so /sigil/announce can feed it.
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
        debug!("sigil: no TLS certs, fan-out and extra-peers disabled");
    }

    tokio::spawn(run(machine, out_tx, in_rx, cancel));
}

async fn run(
    machine: String,
    out_tx: mpsc::Sender<Announcement>,
    mut in_rx: mpsc::Receiver<IncomingAnnouncement>,
    cancel: CancellationToken,
) {
    info!(machine = %machine, "sigil daemon started");

    let mut announce_timer = tokio::time::interval(ANNOUNCE_INTERVAL);
    let mut prune_timer = tokio::time::interval(PRUNE_INTERVAL);
    let mut tombstone_gc_timer = tokio::time::interval(TOMBSTONE_GC_INTERVAL);
    // Fire one announce immediately on startup rather than waiting a
    // full interval — fresh envoys should appear on the mesh promptly.
    announce_timer.tick().await;
    // Skip the first prune / gc tick — nothing to prune yet.
    prune_timer.tick().await;
    tombstone_gc_timer.tick().await;

    loop {
        tokio::select! {
            _ = cancel.cancelled() => {
                info!("sigil daemon: cancelled, exiting");
                return;
            }
            _ = announce_timer.tick() => {
                // Process any pending leaves before announcing so
                // a fresh leave lands on peers in the same cycle.
                process_pending_leaves(&machine).await;
                // Process any post-reenroll regen requests so a
                // fresh `vigo sigil enroll` picks up live peers
                // within one tick instead of waiting for the next
                // peer pubkey rotation.
                process_pending_regens();
                if let Err(e) = announce_all(&machine, &out_tx).await {
                    warn!(error = %e, "sigil: announce cycle error");
                }
            }
            _ = prune_timer.tick() => {
                prune_all();
            }
            _ = tombstone_gc_timer.tick() => {
                gc_tombstones_all();
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

/// Poll every enrolled user for a `~/.sigil/leave.pending` sentinel
/// dropped by `vigo sigil leave`. For each sentinel present:
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
                info!(user = %user, "sigil: leave processed, sentinel cleared");
            }
            Err(e) => {
                warn!(user = %user, error = %e, "sigil: leave processing failed");
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
    let spec: super::sigil::cli::LeavePendingSpec =
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
                warn!(error = %e, "sigil: LEAVE fan-out error");
            }
        });
    } else {
        warn!("sigil: DAEMON_TLS not initialised, skipping LEAVE fan-out (peers catch up via staleness)");
    }

    // Scrub user-side state per flags.
    if spec.purge_content {
        let content = config::content_dir(home);
        if content.exists() {
            if let Err(e) = fs::remove_dir_all(&content) {
                warn!(path = %content.display(), error = %e, "sigil: purge ~/sigil/ failed");
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

/// Poll every enrolled user for a `~/.sigil/regen.pending` sentinel
/// dropped by `vigo sigil enroll` after a fresh enrollment. For each
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
                info!(user = %user, "sigil: post-enroll recipients regen complete");
                if let Err(e) = fs::remove_file(&pending) {
                    warn!(
                        user = %user,
                        path = %pending.display(),
                        error = %e,
                        "sigil: failed to clear regen.pending sentinel"
                    );
                }
            }
            Err(e) => {
                warn!(
                    user = %user,
                    error = %e,
                    "sigil: post-enroll recipients regen failed; will retry next tick"
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
            dropped = table.gc(now, tombstones::DEFAULT_RETENTION_SECS);
            dropped > 0
        }) {
            warn!(
                user = %user,
                error = %e,
                "sigil: tombstone save after gc failed"
            );
            continue;
        }
        if dropped > 0 {
            info!(
                user = %user,
                dropped = dropped,
                "sigil: tombstone gc complete"
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
                debug!(user = %user, error = %e, "sigil: cannot read local pubkey, skipping announce");
                continue;
            }
        };
        if let Err(e) = ensure_signing_identity(&user) {
            warn!(user = %user, error = %e, "sigil: ensure_signing_identity failed");
        }
        let sign_pubkey = load_local_sign_pubkey(&user).unwrap_or_default();
        let tombstones_digest =
            hex::encode(TombstoneTable::load(&user).digest());
        let ann = Announcement {
            machine: machine.to_string(),
            user: user.clone(),
            pubkey,
            sign_pubkey,
            emitted_at: now_secs(),
            port: PEER_PORT,
            generation: 0,
            tombstones_digest,
        };
        if let Err(e) = out_tx.send(ann).await {
            warn!(error = %e, user = %user, "sigil: announce channel send failed");
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
        "sigil: incoming announcement"
    );

    // Ignore announcements for users we don't have enrolled locally.
    // Different users' meshes are fully independent; we never keep
    // state for users we don't participate in.
    let enrolled = match enrolled_users() {
        Ok(s) => s,
        Err(e) => {
            warn!(error = %e, "sigil: cannot enumerate enrolled users, dropping incoming");
            return;
        }
    };
    if !enrolled.contains(&announcement.user) {
        debug!(
            user = %announcement.user,
            "sigil: ignoring announcement for non-local user"
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
            }
            dropped
        }) {
            warn!(
                error = %e,
                user = %announcement.user,
                "sigil: peer table save after pre-emptive forget failed"
            );
        }
        if let Some(table) = snapshot {
            if let Some(home) = home_for_user(&announcement.user) {
                if let Err(e) = regenerate_recipients(&announcement.user, &home, &table) {
                    warn!(
                        error = %e,
                        user = %announcement.user,
                        "sigil: recipients regen after pre-emptive forget failed"
                    );
                }
            }
        }
        debug!(
            user = %announcement.user,
            machine = %announcement.machine,
            pubkey = %announcement.pubkey,
            "sigil: rejecting announce from pubkey in leaves.json"
        );
        return;
    }

    let entry = PeerEntry {
        hostname: announcement.machine.clone(),
        pubkey: announcement.pubkey.clone(),
        sign_pubkey: announcement.sign_pubkey.clone(),
        last_seen: announcement.emitted_at,
        last_seen_addr: Some(from.to_string()),
    };

    // Lock-protected upsert. Capture a post-update snapshot so we can
    // call regenerate_recipients outside the lock without re-reading
    // the table — and only when upsert reported a real_change.
    let mut snapshot: Option<PeerTable> = None;
    if let Err(e) = PeerTable::modify(&announcement.user, |table| {
        let real_change = table.upsert(entry);
        if real_change {
            snapshot = Some(table.clone());
        }
        real_change
    }) {
        warn!(error = %e, user = %announcement.user, "sigil: peer table save failed");
    }

    if let Some(table) = snapshot {
        if let Some(home) = home_for_user(&announcement.user) {
            if let Err(e) = regenerate_recipients(&announcement.user, &home, &table) {
                warn!(
                    error = %e,
                    user = %announcement.user,
                    "sigil: recipients.txt regeneration failed"
                );
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
        "sigil: tombstone digest mismatch, triggering catch-up"
    );

    let user = user.to_string();
    let source_hostname = source_hostname.to_string();
    // Build the URL using the announcement source socket so we
    // pull from the exact peer that told us about the divergence.
    let url = format!(
        "https://{}/sigil/tombstones/{}",
        source_addr,
        url_encode_seg(&user)
    );
    tokio::spawn(async move {
        if let Err(e) = catchup_tombstones_from(&user, &source_hostname, &url).await {
            debug!(
                user = %user,
                source = %source_hostname,
                error = %e,
                "sigil: tombstone catch-up failed"
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
            "sigil: tombstone catch-up merged"
        );
    }
    Ok(())
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
                    "sigil: catch-up file remove failed"
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
            }
            pruned
        }) {
            warn!(error = %e, user = %user, "sigil: peer table save after prune failed");
            continue;
        }
        if let Some(table) = snapshot {
            if let Some(home) = home_for_user(&user) {
                if let Err(e) = regenerate_recipients(&user, &home, &table) {
                    warn!(
                        error = %e,
                        user = %user,
                        "sigil: recipients.txt regeneration after prune failed"
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
            debug!(user = %user, "sigil: skipping user-mirror write (no home)");
            return;
        }
    };
    let dot = home.join(".sigil");
    if let Err(e) = fs::create_dir_all(&dot) {
        debug!(path = %dot.display(), error = %e, "sigil: mkdir for user mirror");
        return;
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
                debug!(path = %tmp.display(), error = %e, "sigil: open user-mirror tmp");
                return;
            }
        };
        if let Err(e) = f.write_all(json.as_bytes()) {
            debug!(path = %tmp.display(), error = %e, "sigil: write user-mirror tmp");
            return;
        }
        f.sync_all().ok();
    }
    #[cfg(not(unix))]
    {
        if let Err(e) = fs::write(&tmp, json.as_bytes()) {
            debug!(path = %tmp.display(), error = %e, "sigil: write user-mirror tmp");
            return;
        }
    }
    if let Err(e) = fs::rename(&tmp, &path) {
        debug!(path = %path.display(), error = %e, "sigil: rename user-mirror");
        return;
    }
    #[cfg(unix)]
    if let Err(e) = chown_to_user(&path, user) {
        debug!(path = %path.display(), error = %e, "sigil: chown user-mirror");
    }
}

/// Enumerate all users on this envoy who have completed sigil
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
/// (0644) forms under `/var/lib/vigo/sigil/<user>/`. Idempotent.
///
/// Daemon-owned (not user-wrapped) because DELETE envelopes need to
/// be signable autonomously — requiring the user to be unlocked for
/// deletions would break the pusher's reactive model. The tradeoff
/// is documented in `docs/engineering/subsystems/sigil.md`:
/// a compromised signing key authorizes destructive operations on
/// this envoy's behalf until peers `vigo sigil forget <host>` it,
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
        "sigil: generated signing identity"
    );
    Ok(())
}

/// Read this envoy's Ed25519 signing pubkey for `user` from
/// `/var/lib/vigo/sigil/<user>/signing.pub`. Returns an error
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

/// Regenerate `~<user>/.sigil/recipients.txt` from the current
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
    let mut all: Vec<String> = Vec::new();
    all.push(self_pubkey);
    for pk in table.live_pubkeys(now) {
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
                debug!(path = %tmp.display(), error = %e, "sigil: set_permissions on tmp");
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
/// - `POST /sigil/leaves/<user>` handler after verifying a signed
///   envelope from a peer
/// - `vigo sigil leave` after signing its own envelope
/// - `vigo sigil _apply-evict` after the admin task dispatch lands
///
/// Steps: drop the pubkey from `peers.json`, persist the table,
/// record the LEAVE in `leaves.json`, regenerate `recipients.txt`
/// (omitting the pubkey), drop the `reencrypt.pending` sentinel so
/// the next `vigo sigil unlock` (or the running session helper)
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
        }
        dropped
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

    // 3. Recipients.txt — regenerate when a peer was dropped, using
    //    the post-drop snapshot we captured under the lock.
    if let Some(table) = peer_table_after.as_ref() {
        regenerate_recipients(user, home, table)?;
    }

    // 4. Reencrypt sentinel — drop only when something actually
    //    changed, else we'd churn the session helper across repeated
    //    idempotent deliveries.
    if peer_was_present || leaves_changed {
        let sentinel = config::reencrypt_pending_path(home);
        if let Some(parent) = sentinel.parent() {
            let _ = fs::create_dir_all(parent);
        }
        if !sentinel.exists() {
            if let Err(e) = fs::OpenOptions::new()
                .create(true)
                .write(true)
                .truncate(true)
                .open(&sentinel)
            {
                warn!(path = %sentinel.display(), error = %e, "sigil: failed to drop reencrypt.pending");
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
            warn!(error = %e, "sigil cross_subnet_peers: agent build failed");
            return;
        }
    };
    info!("sigil cross_subnet_peers announce loop started");

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
        let ann = Announcement {
            machine: machine.to_string(),
            user: user.clone(),
            pubkey,
            sign_pubkey,
            emitted_at: now_secs(),
            port: PEER_PORT,
            generation: 0,
            tombstones_digest,
        };
        let body = serde_json::to_vec(&ann).context("serializing announcement")?;
        for host in &cfg.cross_subnet_peers {
            let url = format!("https://{}:{}/sigil/announce", host, PEER_PORT);
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

/// Mark a user for sigil enrollment.
///
/// Called by the user executor (`agent/src/executors/user.rs`) after
/// successful user creation when the user resource has
/// `sigil: true`. Drops the `~<home>/.sigil/enroll.pending`
/// sentinel so the first interactive `vigo sigil *` invocation by
/// that user runs enrollment.
///
/// Idempotent:
/// - If the user is already enrolled (identity.age exists), no-op.
/// - If the sentinel already exists, no-op.
/// - Otherwise creates `~/.sigil/` (0700, user-owned) and the
///   empty sentinel (0600, user-owned).
pub fn mark_for_enroll(username: &str, home: &Path) -> Result<()> {
    if config::is_enrolled(home) {
        tracing::debug!(user = %username, "sigil: already enrolled, skipping marker");
        return Ok(());
    }
    if config::has_pending_enroll(home) {
        tracing::debug!(user = %username, "sigil: enrollment already pending, skipping marker");
        return Ok(());
    }

    let dot = config::dot_dir(home);
    fs::create_dir_all(&dot)
        .with_context(|| format!("creating {}", dot.display()))?;

    let marker = config::enroll_pending_path(home);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        fs::OpenOptions::new()
            .write(true)
            .create_new(true)
            .mode(0o600)
            .open(&marker)
            .with_context(|| format!("creating {}", marker.display()))?;
    }
    #[cfg(not(unix))]
    {
        fs::OpenOptions::new()
            .write(true)
            .create_new(true)
            .open(&marker)
            .with_context(|| format!("creating {}", marker.display()))?;
    }

    // Chown dot dir + sentinel to the user so it's owned by them,
    // not root. The user executor runs as root (agent daemon); the
    // user needs to be able to delete these files and write siblings.
    #[cfg(unix)]
    chown_to_user(&dot, username)?;
    #[cfg(unix)]
    chown_to_user(&marker, username)?;

    tracing::info!(
        user = %username,
        marker = %marker.display(),
        "sigil: marked for enrollment"
    );
    Ok(())
}

/// Finalize a freshly-written ciphertext temp file: enforce mode
/// 0600 on the temp, atomically rename to `dst`, then chown `dst`
/// to `username`. Both write paths into `~<user>/sigil/` — the
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
fn chown_to_user(path: &Path, username: &str) -> Result<()> {
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

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn mark_for_enroll_noop_when_identity_exists() {
        let dir = tempfile::tempdir().unwrap();
        fs::create_dir_all(config::dot_dir(dir.path())).unwrap();
        // Fake a present identity + salt — full content doesn't matter.
        fs::write(config::identity_path(dir.path()), b"fake").unwrap();
        fs::write(config::salt_path(dir.path()), b"fake").unwrap();

        // Call mark_for_enroll; should no-op (not create the sentinel).
        // NOTE: we skip the chown step by using a fake username that's
        // unlikely to exist; the early return on is_enrolled fires first.
        let _ = mark_for_enroll("nonexistent-test-user-xyzzy", dir.path());
        assert!(!config::has_pending_enroll(dir.path()));
    }
}
