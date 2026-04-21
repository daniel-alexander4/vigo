// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Vigocrypt — per-user encrypted P2P directory.
//!
//! A user places a file into `~/vigocrypt/` on one envoy; vigocrypt
//! encrypts it to every enrolled envoy in the user's mesh and syncs
//! the ciphertext so every envoy holds the same age-encrypted bytes
//! at rest. Files are never plaintext on disk in `~/vigocrypt/` — the
//! whole point.
//!
//! Relationship to vigosync: both are per-user LAN-first P2P sync
//! subsystems. Vigosync is unencrypted plaintext sync (LAN-trusted).
//! Vigocrypt is always-encrypted sync where only envoys in the mesh
//! can decrypt, and decryption requires the user's vigocrypt
//! password.
//!
//! Phase 1 scope: Linux + macOS only. No PAM, no FUSE, no filename
//! encryption, no key escrow. See `plan_vigocrypt.md` for the full
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
pub mod pusher;
pub mod server;
pub mod session;
pub mod state;
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

use crate::update::TlsCerts;

/// How often each enrolled user's local state is re-announced.
/// Short enough to feel responsive; long enough not to chew the LAN.
const ANNOUNCE_INTERVAL: Duration = Duration::from_secs(30);

/// How often stale peers are pruned from every user's PeerTable.
const PRUNE_INTERVAL: Duration = Duration::from_secs(60);

/// The swarm peer server port — vigocrypt push/pull rides here.
const PEER_PORT: u16 = crate::swarm::server::DEFAULT_PORT;

/// Inbound-announcement sender, published when the vigocrypt daemon
/// starts. The HTTPS `/vigocrypt/announce` handler uses this to feed
/// unicast announcements from `extra_peers` into the same reconciler
/// path that multicast uses. `None` before spawn.
static INCOMING_ANNOUNCE: std::sync::OnceLock<mpsc::Sender<IncomingAnnouncement>> =
    std::sync::OnceLock::new();

/// Deliver a unicast announcement into the vigocrypt reconciler.
/// Returns true on success, false if the daemon isn't running or the
/// channel is full. Used by `POST /vigocrypt/announce`.
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

/// Spawn the vigocrypt daemon. Runs until the cancellation token
/// fires. No-op if vigocrypt is not wanted on this envoy (no enrolled
/// users exist yet — the daemon re-discovers enrolled users every
/// cycle so brand-new enrollments don't require a daemon restart).
///
/// `tls` is the same mTLS bundle swarm uses. When absent, the
/// fan-out pusher is disabled but everything else runs (discovery,
/// peer table maintenance, `recipients.txt` regen, serving
/// `/vigocrypt/pull/...` GETs). Matches vigosync's posture.
pub fn spawn(tls: Option<TlsCerts>, cancel: CancellationToken) {
    let machine = std::fs::read_to_string("/etc/hostname")
        .unwrap_or_else(|_| "unknown".to_string())
        .trim()
        .to_string();

    let (out_tx, in_tx, in_rx) = discovery::spawn(cancel.clone());
    // Publish the inbound sender so /vigocrypt/announce can feed it.
    let _ = INCOMING_ANNOUNCE.set(in_tx);

    // Watcher emits PushJob events on a channel; pusher drains them.
    // If TLS certs aren't available, skip the watcher entirely — no
    // point detecting events if we can't act on them.
    if let Some(tls) = tls {
        let push_rx = watcher::spawn(cancel.clone());
        pusher::spawn(tls.clone(), push_rx, cancel.clone());
        // Extra-peers unicast announcer — sends periodic announcements
        // to each user's `extra_peers:` hosts over HTTPS.
        tokio::spawn(extra_peers_announce_loop(
            machine.clone(),
            tls,
            cancel.clone(),
        ));
    } else {
        debug!("vigocrypt: no TLS certs, fan-out and extra-peers disabled");
    }

    tokio::spawn(run(machine, out_tx, in_rx, cancel));
}

async fn run(
    machine: String,
    out_tx: mpsc::Sender<Announcement>,
    mut in_rx: mpsc::Receiver<IncomingAnnouncement>,
    cancel: CancellationToken,
) {
    info!(machine = %machine, "vigocrypt daemon started");

    let mut announce_timer = tokio::time::interval(ANNOUNCE_INTERVAL);
    let mut prune_timer = tokio::time::interval(PRUNE_INTERVAL);
    // Fire one announce immediately on startup rather than waiting a
    // full interval — fresh envoys should appear on the mesh promptly.
    announce_timer.tick().await;
    // Skip the first prune tick — nothing to prune yet.
    prune_timer.tick().await;

    loop {
        tokio::select! {
            _ = cancel.cancelled() => {
                info!("vigocrypt daemon: cancelled, exiting");
                return;
            }
            _ = announce_timer.tick() => {
                if let Err(e) = announce_all(&machine, &out_tx).await {
                    warn!(error = %e, "vigocrypt: announce cycle error");
                }
            }
            _ = prune_timer.tick() => {
                prune_all();
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
                debug!(user = %user, error = %e, "vigocrypt: cannot read local pubkey, skipping announce");
                continue;
            }
        };
        let ann = Announcement {
            machine: machine.to_string(),
            user: user.clone(),
            pubkey,
            emitted_at: now_secs(),
            port: PEER_PORT,
            generation: 0,
        };
        if let Err(e) = out_tx.send(ann).await {
            warn!(error = %e, user = %user, "vigocrypt: announce channel send failed");
        }
        // extra_peers unicast is Week 3 territory (uses peer HTTPS
        // client from crate::swarm::peer_client — depends on having
        // mTLS certs plumbed through). Left as a follow-on.
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
        "vigocrypt: incoming announcement"
    );

    // Ignore announcements for users we don't have enrolled locally.
    // Different users' meshes are fully independent; we never keep
    // state for users we don't participate in.
    let enrolled = match enrolled_users() {
        Ok(s) => s,
        Err(e) => {
            warn!(error = %e, "vigocrypt: cannot enumerate enrolled users, dropping incoming");
            return;
        }
    };
    if !enrolled.contains(&announcement.user) {
        debug!(
            user = %announcement.user,
            "vigocrypt: ignoring announcement for non-local user"
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

    let entry = PeerEntry {
        hostname: announcement.machine.clone(),
        pubkey: announcement.pubkey.clone(),
        last_seen: announcement.emitted_at,
        last_seen_addr: Some(from.to_string()),
    };

    let mut table = PeerTable::load(&announcement.user);
    let real_change = table.upsert(entry);
    if let Err(e) = table.save() {
        warn!(error = %e, user = %announcement.user, "vigocrypt: peer table save failed");
    }

    if real_change {
        if let Some(home) = home_for_user(&announcement.user) {
            if let Err(e) = regenerate_recipients(&announcement.user, &home, &table) {
                warn!(
                    error = %e,
                    user = %announcement.user,
                    "vigocrypt: recipients.txt regeneration failed"
                );
            }
        }
    }
}

// ---------- prune ----------

fn prune_all() {
    let enrolled = match enrolled_users() {
        Ok(s) => s,
        Err(_) => return,
    };
    let now = now_secs();
    for user in enrolled {
        let mut table = PeerTable::load(&user);
        if table.prune_stale(now) {
            if let Err(e) = table.save() {
                warn!(error = %e, user = %user, "vigocrypt: peer table save after prune failed");
                continue;
            }
            if let Some(home) = home_for_user(&user) {
                if let Err(e) = regenerate_recipients(&user, &home, &table) {
                    warn!(
                        error = %e,
                        user = %user,
                        "vigocrypt: recipients.txt regeneration after prune failed"
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

/// Public re-export for sibling modules (pusher.rs, watcher.rs).
pub fn home_for_user_pub(username: &str) -> Option<PathBuf> {
    home_for_user(username)
}

/// Enumerate all users on this envoy who have completed vigocrypt
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

/// Regenerate `~<user>/.vigocrypt/recipients.txt` from the current
/// peer table. Preserves this envoy's own pubkey (first line) and
/// appends live peers sorted by pubkey. Writes atomically via
/// temp + rename; chowns the resulting file to the user.
fn regenerate_recipients(user: &str, home: &Path, table: &PeerTable) -> Result<()> {
    let path = config::recipients_path(home);
    let self_pubkey = load_local_pubkey(home)?;
    let now = now_secs();
    let mut all: Vec<String> = Vec::new();
    all.push(self_pubkey);
    for pk in table.live_pubkeys(now) {
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
                debug!(path = %tmp.display(), error = %e, "vigocrypt: set_permissions on tmp");
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

/// Periodic unicast-announce loop for each enrolled user's
/// `extra_peers:`. Same 30-second cadence as multicast. Runs in a
/// blocking task per send because the ureq agent is synchronous.
async fn extra_peers_announce_loop(
    machine: String,
    tls: TlsCerts,
    cancel: CancellationToken,
) {
    let agent = match pusher_agent(&tls) {
        Ok(a) => a,
        Err(e) => {
            warn!(error = %e, "vigocrypt extra_peers: agent build failed");
            return;
        }
    };
    info!("vigocrypt extra_peers announce loop started");

    let mut interval = tokio::time::interval(ANNOUNCE_INTERVAL);
    interval.tick().await; // fire first tick immediately, then cadence
    loop {
        tokio::select! {
            _ = cancel.cancelled() => return,
            _ = interval.tick() => {
                if let Err(e) = extra_peers_announce_once(&machine, &agent).await {
                    debug!(error = %e, "extra_peers announce pass error");
                }
            }
        }
    }
}

async fn extra_peers_announce_once(machine: &str, agent: &ureq::Agent) -> Result<()> {
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
        if cfg.extra_peers.is_empty() {
            continue;
        }
        let ann = Announcement {
            machine: machine.to_string(),
            user: user.clone(),
            pubkey,
            emitted_at: now_secs(),
            port: PEER_PORT,
            generation: 0,
        };
        let body = serde_json::to_vec(&ann).context("serializing announcement")?;
        for host in &cfg.extra_peers {
            let url = format!("https://{}:{}/vigocrypt/announce", host, PEER_PORT);
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
                        debug!(host = %host, "extra_peers announce accepted");
                    }
                    Ok(r) => debug!(host = %host, status = %r.status(), "extra_peers announce rejected"),
                    Err(e) => debug!(host = %host, error = %e, "extra_peers announce failed"),
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

/// Mark a user for vigocrypt enrollment.
///
/// Called by the user executor (`agent/src/executors/user.rs`) after
/// successful user creation when the user resource has
/// `vigocrypt: true`. Drops the `~<home>/.vigocrypt/enroll.pending`
/// sentinel so the first interactive `vigo crypt *` invocation by
/// that user runs enrollment.
///
/// Idempotent:
/// - If the user is already enrolled (identity.age exists), no-op.
/// - If the sentinel already exists, no-op.
/// - Otherwise creates `~/.vigocrypt/` (0700, user-owned) and the
///   empty sentinel (0600, user-owned).
pub fn mark_for_enroll(username: &str, home: &Path) -> Result<()> {
    if config::is_enrolled(home) {
        tracing::debug!(user = %username, "vigocrypt: already enrolled, skipping marker");
        return Ok(());
    }
    if config::has_pending_enroll(home) {
        tracing::debug!(user = %username, "vigocrypt: enrollment already pending, skipping marker");
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
        "vigocrypt: marked for enrollment"
    );
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
