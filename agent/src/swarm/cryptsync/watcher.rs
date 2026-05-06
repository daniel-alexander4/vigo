// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Filesystem watcher for `~<user>/cryptsync/` directories.
//!
//! When the user's `vigo swarm cryptsync encrypt` CLI writes a ciphertext file
//! into `~<user>/cryptsync/<name>`, the agent daemon (running as root)
//! needs to notice and fan out the bytes to every peer in the user's
//! mesh. Rather than poll, we watch each enrolled user's
//! `~/cryptsync/` directory via inotify (Linux) or kqueue (macOS)
//! through the `notify` crate.
//!
//! Emits `PushJob { user, filename, bytes }` onto a channel the
//! pusher drains.
//!
//! Design notes:
//!
//! - The watcher starts with no watches. Every 30 s (same cadence as
//!   the announce cycle) we reconcile: enumerate enrolled users and
//!   add/remove watches to match.
//! - Only final-rename events fire a push. The encrypt CLI writes a
//!   `.foo.cryptsync.enc.tmp` then renames to `foo`; we watch for the
//!   rename destination, not the temp write.
//! - Files must pass the age-magic check before push — defensive
//!   against a user dropping plaintext into `~/cryptsync/` by hand.
//!
//! Scope guardrail: inotify's 8192 watches/process limit matters only
//! at ~8k enrolled users on one envoy, which no plausible deployment
//! has. Document, don't solve.

use std::collections::HashMap;
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use notify::{Event, EventKind, RecursiveMode, Watcher};
use tokio::sync::mpsc;
use tokio_util::sync::CancellationToken;
use tracing::{debug, error, info, warn};

use super::auto_encrypt::{
    self, AutoEncryptError, MAX_AUTO_ENCRYPT_BYTES, RETRY_BACKOFFS,
};
use super::config;
use super::crypt;
use super::enrolled_users;
use super::home_for_user;
use super::is_session_unlocked_for_home;

/// A ciphertext file ready to fan out.
///
/// `bytes` is an `Arc<Vec<u8>>` so the pusher's fan-out can share the
/// payload across N concurrent per-target sends without cloning the
/// Vec — a 1 GiB file fanned to 10 peers would otherwise peak at ~10
/// GiB of transient RSS (every clone allocates a fresh heap Vec, and
/// glibc malloc retains freed blocks on its free list, so cgroup
/// memory.peak captures the spike permanently).
#[derive(Debug, Clone)]
pub struct PushJob {
    pub user: String,
    pub filename: String,
    pub bytes: Arc<Vec<u8>>,
}

/// A deletion ready to propagate to peers. Emitted when the watcher
/// observes a `Remove` event in `~<user>/cryptsync/`.
///
/// `deletion_time` is captured on the source envoy at event-processing
/// time. Receivers compare this timestamp against their own file
/// mtimes during anti-resurrection checks — a push older than the
/// tombstone's deletion_time gets rejected, a push newer clears the
/// tombstone and replaces the file.
#[derive(Debug, Clone)]
pub struct DeleteJob {
    pub user: String,
    pub filename: String,
    pub deletion_time: i64,
}

/// Reconciler tick interval — how often we add/remove watches to
/// match the current enrolled-user set.
const RECONCILE_INTERVAL: Duration = Duration::from_secs(30);

/// Debounce window — squelch events firing in rapid succession for
/// the same (user, filename). Useful when the encrypt CLI renames
/// over an existing file (CREATE + MODIFY fire together).
const DEBOUNCE: Duration = Duration::from_millis(500);

/// Spawn the watcher. Returns two receivers the pusher drains —
/// one for fan-out push events, one for deletion propagation.
pub fn spawn(
    cancel: CancellationToken,
) -> (mpsc::Receiver<PushJob>, mpsc::Receiver<DeleteJob>) {
    let (push_tx, push_rx) = mpsc::channel::<PushJob>(128);
    let (del_tx, del_rx) = mpsc::channel::<DeleteJob>(128);
    tokio::spawn(run(push_tx, del_tx, cancel));
    (push_rx, del_rx)
}

/// A live watch on `~user/cryptsync/`. Tracks `(dev, inode)` so the
/// reconciler can detect a delete-and-recreate (e.g.,
/// `vigo swarm cryptsync leave --purge` followed by `vigo swarm cryptsync {puddle init, standalone enroll}`) and
/// re-arm the inotify watch on the new directory inode. Without
/// this, the kernel auto-removes the watch on dir-deletion (IN_IGNORED)
/// but the reconciler's "user is enrolled and a watch exists" check
/// happily skips re-adding, leaving the daemon blind to events on
/// the recreated dir until something else flushes the entry.
#[derive(Debug, Clone, PartialEq, Eq)]
struct WatchEntry {
    path: PathBuf,
    dev: u64,
    inode: u64,
}

async fn run(
    out_tx: mpsc::Sender<PushJob>,
    del_tx: mpsc::Sender<DeleteJob>,
    cancel: CancellationToken,
) {
    // Map of user → watched-path metadata, so the reconciler can
    // detect a stale watch (path gone or inode rotated) and re-arm.
    let watches: Arc<Mutex<HashMap<String, WatchEntry>>> = Arc::new(Mutex::new(HashMap::new()));

    let (raw_tx, mut raw_rx) = tokio::sync::mpsc::unbounded_channel::<RawEvent>();

    // Build the watcher. The callback runs on notify's own thread —
    // forward into the tokio channel without blocking.
    let raw_tx_cb = raw_tx.clone();
    let mut watcher = match notify::recommended_watcher(move |res: notify::Result<Event>| {
        if let Ok(ev) = res {
            let _ = raw_tx_cb.send(RawEvent::Notify(ev));
        }
    }) {
        Ok(w) => w,
        Err(e) => {
            warn!(error = %e, "cryptsync watcher: failed to create; daemon will not fan out");
            return;
        }
    };

    info!("cryptsync watcher started");

    // Kick off an immediate reconcile, then schedule periodic ones.
    let _ = raw_tx.send(RawEvent::Reconcile);
    let mut interval = tokio::time::interval(RECONCILE_INTERVAL);
    interval.tick().await; // consume the immediate tick

    // Event dedupe: remember the last emit time per
    // (user, filename, action). Action is part of the key so a Push
    // followed by a Delete on the same filename within DEBOUNCE
    // (typical for `vigo swarm cryptsync encrypt foo && vigo swarm cryptsync delete foo`
    // or for the §7 livetest's encrypt-then-delete probe) doesn't
    // squelch the second event. CREATE+MODIFY pairs from the
    // encrypt CLI's atomic rename still collapse correctly because
    // both resolve to action=Push and so share a key.
    //
    // Pruned on each reconcile tick so the map is bounded by the rate
    // of distinct (filename, action) touched within the reconcile
    // window, not by the envoy's lifetime.
    let mut last_emit: HashMap<(String, String, EventAction), std::time::Instant> =
        HashMap::new();

    // One-shot WARN dedupe for plaintext-not-encrypted reasons
    // (session locked or oversized). Keyed by (user, filename,
    // mtime_secs) per ADR-012 §5 — re-WARN fires when the user
    // re-writes the file (mtime changes). Pruned alongside last_emit
    // on the reconcile cadence so a steady stream of unique files
    // doesn't grow this map without bound.
    let mut plaintext_warns: HashMap<(String, String, i64), std::time::Instant> =
        HashMap::new();

    loop {
        tokio::select! {
            _ = cancel.cancelled() => {
                info!("cryptsync watcher: cancelled, exiting");
                return;
            }
            _ = interval.tick() => {
                let _ = raw_tx.send(RawEvent::Reconcile);
                // Entries older than DEBOUNCE are functionally dead — the
                // only purpose of last_emit is to squelch events within
                // the DEBOUNCE window. A generous safety factor keeps any
                // in-flight debounce decision safe while still letting
                // the map drain between bursts.
                let cutoff = DEBOUNCE * 4;
                let now = std::time::Instant::now();
                last_emit.retain(|_, prev| now.duration_since(*prev) < cutoff);
                // plaintext_warns is keyed by (user, filename, mtime),
                // so a fresh user write naturally rotates the key. We
                // still prune by age to bound memory under a stream
                // of one-shot writes that never repeat.
                let warn_cutoff = Duration::from_secs(60 * 60);
                plaintext_warns.retain(|_, prev| now.duration_since(*prev) < warn_cutoff);
            }
            maybe = raw_rx.recv() => {
                match maybe {
                    Some(RawEvent::Reconcile) => {
                        reconcile_watches(&mut watcher, &watches);
                    }
                    Some(RawEvent::Notify(ev)) => {
                        if let Some((user, filename, action)) =
                            resolve_event(&ev, &watches.lock().unwrap())
                        {
                            let key = (user.clone(), filename.clone(), action);
                            let now = std::time::Instant::now();
                            if let Some(prev) = last_emit.get(&key) {
                                if now.duration_since(*prev) < DEBOUNCE {
                                    continue;
                                }
                            }
                            last_emit.insert(key, now);

                            match action {
                                EventAction::Push => {
                                    handle_push_event(
                                        &user,
                                        &filename,
                                        &out_tx,
                                        &mut plaintext_warns,
                                    );
                                }
                                EventAction::Delete => {
                                    let deletion_time = crate::swarm::cryptsync::now_secs_pub();
                                    let job = DeleteJob {
                                        user: user.clone(),
                                        filename: filename.clone(),
                                        deletion_time,
                                    };
                                    if let Err(e) = del_tx.try_send(job) {
                                        warn!(
                                            error = %e,
                                            "cryptsync watcher: delete channel full or closed, dropping delete event"
                                        );
                                    } else {
                                        info!(
                                            user = %user,
                                            filename = %filename,
                                            deletion_time = deletion_time,
                                            "cryptsync watcher: DeleteJob enqueued"
                                        );
                                    }
                                }
                            }
                        }
                    }
                    None => return,
                }
            }
        }
    }
}

#[derive(Debug)]
enum RawEvent {
    Reconcile,
    Notify(Event),
}

/// Dispatch one resolved push-class event. Cheap age-magic peek
/// decides between the ciphertext fast path (full read + emit
/// PushJob, unchanged behavior) and the plaintext branch (ADR-012:
/// auto-encrypt-on-write or one-shot WARN).
///
/// `plaintext_warns` is the watcher loop's WARN-dedupe map keyed on
/// `(user, filename, mtime_secs)`. Re-WARN fires on each real
/// rewrite (mtime change) but rapid duplicate inotify events for the
/// same plaintext don't flood the journal.
fn handle_push_event(
    user: &str,
    filename: &str,
    out_tx: &mpsc::Sender<PushJob>,
    plaintext_warns: &mut HashMap<(String, String, i64), std::time::Instant>,
) {
    let home = match home_for_user(user) {
        Some(h) => h,
        None => {
            warn!(user = %user, "cryptsync watcher: cannot resolve home, dropping event");
            return;
        }
    };
    let path = config::content_dir(&home).join(filename);

    // Fast peek: is this already age ciphertext?
    match crypt::is_age_encrypted(&path) {
        Ok(true) => emit_push_for_ciphertext(user, filename, &path, out_tx),
        Ok(false) => handle_plaintext_event(user, filename, &home, &path, plaintext_warns),
        Err(e) => {
            // Open/read-magic failed — likely a transient race
            // (file removed between event and stat). Don't loop on
            // it; debug-level is enough.
            debug!(
                user = %user,
                filename = %filename,
                error = %e,
                "cryptsync watcher: cannot read header for classification"
            );
        }
    }
}

/// Push the unchanged-since-0.34 path: full read + magic verify +
/// emit PushJob.
fn emit_push_for_ciphertext(
    user: &str,
    filename: &str,
    path: &Path,
    out_tx: &mpsc::Sender<PushJob>,
) {
    let bytes = match std::fs::read(path) {
        Ok(b) => b,
        Err(e) => {
            warn!(
                user = %user,
                filename = %filename,
                error = %e,
                "cryptsync watcher: cannot read ciphertext for push"
            );
            return;
        }
    };
    const AGE_MAGIC: &[u8] = b"age-encryption.org/v1\n";
    if bytes.len() < AGE_MAGIC.len() || &bytes[..AGE_MAGIC.len()] != AGE_MAGIC {
        // Lost the race — peek said ciphertext, full read disagrees.
        debug!(
            user = %user,
            filename = %filename,
            "cryptsync watcher: file no longer ciphertext, skipping push"
        );
        return;
    }
    let size = bytes.len();
    let job = PushJob {
        user: user.to_string(),
        filename: filename.to_string(),
        bytes: Arc::new(bytes),
    };
    if let Err(e) = out_tx.try_send(job) {
        warn!(
            error = %e,
            "cryptsync watcher: pusher channel full or closed, dropping push event"
        );
    } else {
        info!(
            user = %user,
            filename = %filename,
            size_bytes = size,
            "cryptsync watcher: PushJob enqueued"
        );
    }
}

/// ADR-012 plaintext classifier. Three exits:
///
/// - Over-cap → one-shot WARN, leave the file alone.
/// - Session locked → one-shot WARN, sweep on next unlock will pick it up.
/// - Unlocked + ≤cap → spawn a blocking encrypt task with the 3×
///   transient retry schedule. On success the rename fires a fresh
///   inotify event that lands on the ciphertext path naturally.
fn handle_plaintext_event(
    user: &str,
    filename: &str,
    home: &Path,
    path: &Path,
    plaintext_warns: &mut HashMap<(String, String, i64), std::time::Instant>,
) {
    let (size, mtime) = match plaintext_size_and_mtime(path) {
        Some(t) => t,
        None => {
            // File gone between event and stat — common with quick
            // editors; just drop.
            debug!(
                user = %user,
                filename = %filename,
                "cryptsync watcher: plaintext vanished before classification"
            );
            return;
        }
    };

    // 1. Over-cap WARN takes precedence — the file can't be encrypted
    //    or transmitted regardless of session state.
    if size > MAX_AUTO_ENCRYPT_BYTES {
        warn_once(
            plaintext_warns,
            user,
            filename,
            mtime,
            format!(
                "cryptsync watcher: {}/{} size={} exceeds {} push cap; not auto-encrypting",
                user, filename, size, MAX_AUTO_ENCRYPT_BYTES
            ),
        );
        return;
    }

    // 2. Session-locked WARN — leave the plaintext for the unlock sweep.
    if !is_session_unlocked_for_home(home) {
        warn_once(
            plaintext_warns,
            user,
            filename,
            mtime,
            format!(
                "cryptsync watcher: {}/{} is plaintext; session locked — will encrypt on next 'vigo swarm cryptsync unlock'",
                user, filename
            ),
        );
        return;
    }

    // 3. Encrypt path. Spawn into a blocking pool so a 1 GiB encrypt
    //    doesn't pin the watcher loop.
    info!(
        user = %user,
        filename = %filename,
        size_bytes = size,
        "cryptsync watcher: auto-encrypting"
    );
    let user_owned = user.to_string();
    let filename_owned = filename.to_string();
    let home_owned = home.to_path_buf();
    tokio::task::spawn_blocking(move || {
        run_auto_encrypt_with_retry(&user_owned, &filename_owned, &home_owned);
    });
}

fn plaintext_size_and_mtime(path: &Path) -> Option<(u64, i64)> {
    let meta = std::fs::metadata(path).ok()?;
    let size = meta.len();
    let mtime = meta
        .modified()
        .ok()
        .and_then(|t| t.duration_since(std::time::UNIX_EPOCH).ok())
        .map(|d| d.as_secs() as i64)
        .unwrap_or(0);
    Some((size, mtime))
}

/// Emit a WARN once per (user, filename, mtime) tuple. A subsequent
/// rewrite of the same file changes mtime and so re-WARNs.
fn warn_once(
    map: &mut HashMap<(String, String, i64), std::time::Instant>,
    user: &str,
    filename: &str,
    mtime: i64,
    line: String,
) {
    let key = (user.to_string(), filename.to_string(), mtime);
    if map.contains_key(&key) {
        debug!(
            user = %user,
            filename = %filename,
            "cryptsync watcher: warn dedupe — already logged for this mtime"
        );
        return;
    }
    map.insert(key, std::time::Instant::now());
    warn!("{}", line);
}

/// Blocking-context wrapper that runs `auto_encrypt::encrypt_in_place`
/// with the ADR-012 retry schedule for transient errors. Resolves
/// recipients here rather than passing them in so a fresh encrypt
/// always uses the latest `recipients.txt` (the watcher loop may
/// have raced an admin evict / leave; one-tick-stale recipients are
/// not what we want).
fn run_auto_encrypt_with_retry(user: &str, filename: &str, home: &Path) {
    let recipients = match config::load_recipients(home) {
        Ok(r) => r,
        Err(e) => {
            warn!(
                user = %user,
                filename = %filename,
                "cryptsync watcher: cannot encrypt {}/{}: {}; left plaintext",
                user, filename, e
            );
            return;
        }
    };

    let mut last_err: Option<AutoEncryptError> = None;
    for attempt in 0..=RETRY_BACKOFFS.len() {
        match auto_encrypt::encrypt_in_place(home, user, filename, &recipients) {
            Ok(size) => {
                info!(
                    user = %user,
                    filename = %filename,
                    size_bytes = size,
                    "cryptsync watcher: auto-encrypt complete"
                );
                return;
            }
            Err(AutoEncryptError::Terminal(msg)) => {
                warn!(
                    "cryptsync watcher: cannot encrypt {}/{}: {}; left plaintext",
                    user, filename, msg
                );
                return;
            }
            Err(AutoEncryptError::Unrecoverable(msg)) => {
                let ts = super::now_secs_pub();
                match auto_encrypt::rename_to_failed(home, filename, ts) {
                    Ok(dst) => {
                        error!(
                            "cryptsync watcher: encrypt of {}/{} failed permanently: {}; renamed to {}",
                            user, filename, msg, dst.display()
                        );
                    }
                    Err(rename_err) => {
                        error!(
                            "cryptsync watcher: encrypt of {}/{} failed permanently: {}; rename also failed: {}",
                            user, filename, msg, rename_err
                        );
                    }
                }
                return;
            }
            Err(AutoEncryptError::Transient(msg)) => {
                if attempt < RETRY_BACKOFFS.len() {
                    warn!(
                        "cryptsync watcher: encrypt attempt {}/3 failed for {}/{}: {}",
                        attempt + 1,
                        user,
                        filename,
                        msg
                    );
                    std::thread::sleep(RETRY_BACKOFFS[attempt]);
                    last_err = Some(AutoEncryptError::Transient(msg));
                } else {
                    last_err = Some(AutoEncryptError::Transient(msg));
                }
            }
        }
    }

    // Exhausted retries — convert the final transient error into an
    // unrecoverable rename per ADR-012 §2.
    let ts = super::now_secs_pub();
    let final_msg = match last_err {
        Some(AutoEncryptError::Transient(m)) => m,
        _ => "transient encrypt error (cause lost)".to_string(),
    };
    match auto_encrypt::rename_to_failed(home, filename, ts) {
        Ok(dst) => {
            error!(
                "cryptsync watcher: encrypt of {}/{} failed permanently after retries: {}; renamed to {}",
                user, filename, final_msg, dst.display()
            );
        }
        Err(rename_err) => {
            error!(
                "cryptsync watcher: encrypt of {}/{} failed permanently after retries: {}; rename also failed: {}",
                user, filename, final_msg, rename_err
            );
        }
    }
}

/// Reconcile the watcher's active watches against the current set of
/// enrolled users. Three actions per pass:
///   1. Drop watches for users no longer enrolled.
///   2. Drop watches whose underlying directory was deleted or
///      replaced (inode rotated). The kernel already detached the
///      inotify watch at the IN_IGNORED event, but the reconciler
///      doesn't see that — without an explicit (dev, inode) check
///      the entry persists forever and we miss every event on the
///      recreated dir.
///   3. Add watches for newly-enrolled users (or for users whose
///      stale entry was just dropped in step 2).
fn reconcile_watches(
    watcher: &mut notify::RecommendedWatcher,
    watches: &Arc<Mutex<HashMap<String, WatchEntry>>>,
) {
    let desired: std::collections::HashSet<String> = match enrolled_users() {
        Ok(s) => s,
        Err(e) => {
            warn!(error = %e, "cryptsync watcher: enroll enumeration failed");
            return;
        }
    };

    let mut active = watches.lock().unwrap();

    // Step 1: drop watches for users no longer enrolled.
    let unenrolled: Vec<String> = active
        .keys()
        .filter(|u| !desired.contains(*u))
        .cloned()
        .collect();
    for user in unenrolled {
        if let Some(entry) = active.remove(&user) {
            if let Err(e) = watcher.unwatch(&entry.path) {
                debug!(user = %user, error = %e, "watcher: unwatch failed");
            } else {
                info!(user = %user, path = %entry.path.display(), "watcher: removed");
            }
        }
    }

    // Step 2: drop watches whose dir was deleted or replaced (purge +
    // re-enroll rotates the inode under the same path).
    let rotated: Vec<String> = active
        .iter()
        .filter_map(|(user, entry)| match path_inode(&entry.path) {
            Some((dev, inode)) if dev == entry.dev && inode == entry.inode => None,
            _ => Some(user.clone()),
        })
        .collect();
    for user in rotated {
        if let Some(entry) = active.remove(&user) {
            // unwatch may already have been auto-cleaned by the
            // kernel (IN_IGNORED on dir delete); ignore the result.
            let _ = watcher.unwatch(&entry.path);
            info!(
                user = %user,
                path = %entry.path.display(),
                "watcher: stale watch dropped (inode rotated or path gone)"
            );
        }
    }

    // Step 3: add watches for users with no current entry.
    for user in &desired {
        if active.contains_key(user) {
            continue;
        }
        let home = match home_for_user(user) {
            Some(h) => h,
            None => continue,
        };
        let content = config::content_dir(&home);
        let (dev, inode) = match path_inode(&content) {
            Some(t) => t,
            None => continue, // dir doesn't exist yet; try again next tick
        };
        match watcher.watch(&content, RecursiveMode::NonRecursive) {
            Ok(()) => {
                info!(user = %user, path = %content.display(), "watcher: added");
                active.insert(
                    user.clone(),
                    WatchEntry { path: content, dev, inode },
                );
            }
            Err(e) => {
                warn!(
                    user = %user,
                    path = %content.display(),
                    error = %e,
                    "watcher: watch() failed"
                );
            }
        }
    }
}

/// Stat a path and return `(dev, inode)`, or `None` if the path
/// doesn't exist. Used to detect a delete-and-recreate of a watched
/// directory across reconcile ticks.
fn path_inode(path: &Path) -> Option<(u64, u64)> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::MetadataExt;
        std::fs::metadata(path).ok().map(|m| (m.dev(), m.ino()))
    }
    #[cfg(not(unix))]
    {
        // Non-unix: fall back to "exists?" — no inode rotation
        // detection. Cryptsync ships Linux + macOS in Phase 1, so this
        // path is unreachable in practice.
        std::fs::metadata(path).ok().map(|_| (0, 0))
    }
}

/// Push or delete — what the watcher does with a resolved event.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub enum EventAction {
    Push,
    Delete,
}

/// Map a notify event to (user, filename, action). Returns None for
/// events we don't care about (directory events, metadata-only,
/// dotfiles, unrelated paths). Removes resolve to Delete; create/modify
/// resolve to Push.
fn resolve_event(
    ev: &Event,
    watches: &HashMap<String, WatchEntry>,
) -> Option<(String, String, EventAction)> {
    // Classify the event: Create/Modify → Push, Remove → Delete. Anything
    // else (metadata-only, access events) ignored.
    let action = match ev.kind {
        EventKind::Create(_) | EventKind::Modify(_) => EventAction::Push,
        EventKind::Remove(_) => EventAction::Delete,
        _ => return None,
    };

    // The path that was affected.
    let p = ev.paths.first()?;
    let name = p.file_name()?.to_str()?;
    if name.starts_with('.') {
        return None;
    }
    // ADR-012: filter the auto-encrypt-failed artifacts so a permanent
    // rename doesn't loop back into the encrypt pipeline. The shape is
    // `<orig>.<unix_ts>.failed`; bare user-named `*.failed` files
    // (without a numeric timestamp segment) still sync normally.
    if auto_encrypt::is_failed_artifact_name(name) {
        return None;
    }
    let parent = p.parent()?;

    // Which user owns this watched dir?
    for (user, entry) in watches.iter() {
        if entry.path == parent {
            return Some((user.clone(), name.to_string(), action));
        }
    }
    None
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::HashMap;

    /// Helper: build a single-entry watches map for resolve_event
    /// tests. (dev, inode) don't affect resolve_event — they're only
    /// consulted by the reconciler for stale-watch detection.
    fn one_watch(user: &str, path: &str) -> HashMap<String, WatchEntry> {
        let mut w = HashMap::new();
        w.insert(
            user.to_string(),
            WatchEntry {
                path: PathBuf::from(path),
                dev: 0,
                inode: 0,
            },
        );
        w
    }

    #[test]
    fn resolve_event_matches_watched_dir() {
        let watches = one_watch("dan", "/home/dan/cryptsync");
        let ev = Event {
            kind: EventKind::Create(notify::event::CreateKind::File),
            paths: vec![PathBuf::from("/home/dan/cryptsync/tax.pdf")],
            attrs: Default::default(),
        };
        let got = resolve_event(&ev, &watches);
        assert_eq!(
            got,
            Some(("dan".to_string(), "tax.pdf".to_string(), EventAction::Push))
        );
    }

    #[test]
    fn resolve_event_ignores_dotfiles() {
        let watches = one_watch("dan", "/home/dan/cryptsync");
        let ev = Event {
            kind: EventKind::Create(notify::event::CreateKind::File),
            paths: vec![PathBuf::from("/home/dan/cryptsync/.foo.tmp")],
            attrs: Default::default(),
        };
        assert!(resolve_event(&ev, &watches).is_none());
    }

    #[test]
    fn resolve_event_ignores_failed_artifact() {
        // ADR-012: `<orig>.<unix_ts>.failed` files (created when
        // auto-encrypt fails unrecoverably) must not re-enter the
        // pipeline. The watcher's resolve_event filter is the
        // single chokepoint.
        let watches = one_watch("dan", "/home/dan/cryptsync");
        let ev = Event {
            kind: EventKind::Create(notify::event::CreateKind::File),
            paths: vec![PathBuf::from(
                "/home/dan/cryptsync/big.tar.gz.20260429.failed",
            )],
            attrs: Default::default(),
        };
        assert!(resolve_event(&ev, &watches).is_none());
    }

    #[test]
    fn resolve_event_allows_user_named_failed_file() {
        // User-named files like `report.failed` (without a numeric
        // timestamp segment) are not the watcher's failure artifact
        // and must sync normally.
        let watches = one_watch("dan", "/home/dan/cryptsync");
        let ev = Event {
            kind: EventKind::Create(notify::event::CreateKind::File),
            paths: vec![PathBuf::from("/home/dan/cryptsync/report.failed")],
            attrs: Default::default(),
        };
        assert_eq!(
            resolve_event(&ev, &watches),
            Some((
                "dan".to_string(),
                "report.failed".to_string(),
                EventAction::Push,
            ))
        );
    }

    #[test]
    fn warn_once_dedupes_same_mtime_rewrites_on_new_mtime() {
        // ADR-012 §5: WARN keyed on (user, filename, mtime). Same
        // mtime → suppress. New mtime → fire again.
        let mut map: HashMap<(String, String, i64), std::time::Instant> =
            HashMap::new();
        warn_once(&mut map, "dan", "f.bin", 100, "first".into());
        assert_eq!(map.len(), 1);
        warn_once(&mut map, "dan", "f.bin", 100, "duplicate".into());
        assert_eq!(map.len(), 1, "same mtime should not insert again");
        warn_once(&mut map, "dan", "f.bin", 200, "new mtime".into());
        assert_eq!(map.len(), 2, "new mtime should re-fire");
    }

    #[test]
    fn resolve_event_ignores_unrelated_parent() {
        let watches = one_watch("dan", "/home/dan/cryptsync");
        let ev = Event {
            kind: EventKind::Create(notify::event::CreateKind::File),
            paths: vec![PathBuf::from("/tmp/stray.txt")],
            attrs: Default::default(),
        };
        assert!(resolve_event(&ev, &watches).is_none());
    }

    #[test]
    fn resolve_event_removes_become_delete_action() {
        let watches = one_watch("dan", "/home/dan/cryptsync");
        let ev = Event {
            kind: EventKind::Remove(notify::event::RemoveKind::File),
            paths: vec![PathBuf::from("/home/dan/cryptsync/foo.pdf")],
            attrs: Default::default(),
        };
        assert_eq!(
            resolve_event(&ev, &watches),
            Some(("dan".to_string(), "foo.pdf".to_string(), EventAction::Delete))
        );
    }

    #[test]
    fn resolve_event_ignores_remove_on_dotfile() {
        // Dotfiles are ignored regardless of action — temp files
        // during atomic renames (.foo.cryptsync.enc.tmp) get removed
        // at rename time and we don't want to propagate that as a
        // user-visible deletion.
        let watches = one_watch("dan", "/home/dan/cryptsync");
        let ev = Event {
            kind: EventKind::Remove(notify::event::RemoveKind::File),
            paths: vec![PathBuf::from("/home/dan/cryptsync/.foo.tmp")],
            attrs: Default::default(),
        };
        assert!(resolve_event(&ev, &watches).is_none());
    }

    #[test]
    fn path_inode_returns_some_for_existing_dir() {
        let tmp = std::env::temp_dir();
        let (dev, inode) = path_inode(&tmp).expect("temp dir always exists");
        // Sanity: same path queried twice produces the same answer.
        let (dev2, inode2) = path_inode(&tmp).unwrap();
        assert_eq!((dev, inode), (dev2, inode2));
    }

    #[test]
    fn path_inode_returns_none_for_missing_path() {
        let bogus = PathBuf::from("/nonexistent/cryptsync/watcher/probe");
        assert!(path_inode(&bogus).is_none());
    }

    #[test]
    fn debounce_key_separates_push_from_delete() {
        // The watcher's dedupe map is keyed by (user, filename, action)
        // so a Push followed by a Delete on the same filename within
        // DEBOUNCE doesn't squelch the second event. The §7 livetest
        // (encrypt then immediate delete) and any rapid re-encrypt
        // cycle depend on this. Two CREATEs for the same file still
        // collapse because they share action=Push.
        let push_key = ("dan".to_string(), "regress.age".to_string(), EventAction::Push);
        let delete_key = ("dan".to_string(), "regress.age".to_string(), EventAction::Delete);
        let push_key_again = ("dan".to_string(), "regress.age".to_string(), EventAction::Push);
        assert_ne!(push_key, delete_key);
        assert_eq!(push_key, push_key_again);

        // Round-trip through a HashMap to confirm Hash + Eq are wired
        // such that lookup distinguishes the actions.
        let mut m: HashMap<(String, String, EventAction), u32> = HashMap::new();
        m.insert(push_key.clone(), 1);
        assert!(m.get(&delete_key).is_none(), "Push key must not collide with Delete key");
        m.insert(delete_key.clone(), 2);
        assert_eq!(m.get(&push_key), Some(&1));
        assert_eq!(m.get(&delete_key), Some(&2));
    }

    #[test]
    fn path_inode_detects_inode_rotation_after_recreate() {
        // Create a tempdir, capture its (dev, inode), delete it, recreate
        // at the same path, and verify the new inode differs. This is
        // the exact scenario the reconciler's stale-watch detection
        // needs to catch (purge + re-enroll under the same ~/cryptsync/
        // path, but a different underlying inode).
        let dir = std::env::temp_dir().join(format!(
            "cryptsync-watcher-rotation-{}",
            std::process::id()
        ));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).unwrap();
        let before = path_inode(&dir).unwrap();
        std::fs::remove_dir_all(&dir).unwrap();
        std::fs::create_dir_all(&dir).unwrap();
        let after = path_inode(&dir).unwrap();
        let _ = std::fs::remove_dir_all(&dir);
        // Same fs (dev should match), different inode.
        assert_eq!(before.0, after.0, "dev should be unchanged");
        assert_ne!(before.1, after.1, "inode should rotate after recreate");
    }
}
