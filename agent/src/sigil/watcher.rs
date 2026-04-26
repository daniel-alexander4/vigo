// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Filesystem watcher for `~<user>/sigil/` directories.
//!
//! When the user's `vigo sigil encrypt` CLI writes a ciphertext file
//! into `~<user>/sigil/<name>`, the agent daemon (running as root)
//! needs to notice and fan out the bytes to every peer in the user's
//! mesh. Rather than poll, we watch each enrolled user's
//! `~/sigil/` directory via inotify (Linux) or kqueue (macOS)
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
//!   `.foo.sigil.enc.tmp` then renames to `foo`; we watch for the
//!   rename destination, not the temp write.
//! - Files must pass the age-magic check before push — defensive
//!   against a user dropping plaintext into `~/sigil/` by hand.
//!
//! Scope guardrail: inotify's 8192 watches/process limit matters only
//! at ~8k enrolled users on one envoy, which no plausible deployment
//! has. Document, don't solve.

use std::collections::HashMap;
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use anyhow::{Context, Result};
use notify::{Event, EventKind, RecursiveMode, Watcher};
use tokio::sync::mpsc;
use tokio_util::sync::CancellationToken;
use tracing::{debug, info, warn};

use super::config;
use super::enrolled_users;
use super::home_for_user;

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
/// observes a `Remove` event in `~<user>/sigil/`.
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

/// A live watch on `~user/sigil/`. Tracks `(dev, inode)` so the
/// reconciler can detect a delete-and-recreate (e.g.,
/// `vigo sigil leave --purge` followed by `vigo sigil enroll`) and
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
            warn!(error = %e, "sigil watcher: failed to create; daemon will not fan out");
            return;
        }
    };

    info!("sigil watcher started");

    // Kick off an immediate reconcile, then schedule periodic ones.
    let _ = raw_tx.send(RawEvent::Reconcile);
    let mut interval = tokio::time::interval(RECONCILE_INTERVAL);
    interval.tick().await; // consume the immediate tick

    // Event dedupe: remember the last emit time per (user, filename).
    // Pruned on each reconcile tick so the map is bounded by the rate
    // of distinct filenames touched within the reconcile window, not
    // by the envoy's lifetime.
    let mut last_emit: HashMap<(String, String), std::time::Instant> = HashMap::new();

    loop {
        tokio::select! {
            _ = cancel.cancelled() => {
                info!("sigil watcher: cancelled, exiting");
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
                            let key = (user.clone(), filename.clone());
                            let now = std::time::Instant::now();
                            if let Some(prev) = last_emit.get(&key) {
                                if now.duration_since(*prev) < DEBOUNCE {
                                    continue;
                                }
                            }
                            last_emit.insert(key, now);

                            match action {
                                EventAction::Push => {
                                    match load_and_verify(&user, &filename) {
                                        Ok(bytes) => {
                                            let size = bytes.len();
                                            let job = PushJob {
                                                user: user.clone(),
                                                filename: filename.clone(),
                                                bytes: Arc::new(bytes),
                                            };
                                            if let Err(e) = out_tx.try_send(job) {
                                                warn!(
                                                    error = %e,
                                                    "sigil watcher: pusher channel full or closed, dropping push event"
                                                );
                                            } else {
                                                info!(
                                                    user = %user,
                                                    filename = %filename,
                                                    size_bytes = size,
                                                    "sigil watcher: PushJob enqueued"
                                                );
                                            }
                                        }
                                        Err(e) => {
                                            warn!(
                                                user = %user,
                                                filename = %filename,
                                                error = %e,
                                                "sigil watcher: skipping non-pushable event"
                                            );
                                        }
                                    }
                                }
                                EventAction::Delete => {
                                    let deletion_time = crate::sigil::now_secs_pub();
                                    let job = DeleteJob {
                                        user: user.clone(),
                                        filename: filename.clone(),
                                        deletion_time,
                                    };
                                    if let Err(e) = del_tx.try_send(job) {
                                        warn!(
                                            error = %e,
                                            "sigil watcher: delete channel full or closed, dropping delete event"
                                        );
                                    } else {
                                        info!(
                                            user = %user,
                                            filename = %filename,
                                            deletion_time = deletion_time,
                                            "sigil watcher: DeleteJob enqueued"
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
            warn!(error = %e, "sigil watcher: enroll enumeration failed");
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
        // detection. Sigil ships Linux + macOS in Phase 1, so this
        // path is unreachable in practice.
        std::fs::metadata(path).ok().map(|_| (0, 0))
    }
}

/// Push or delete — what the watcher does with a resolved event.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
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
    let parent = p.parent()?;

    // Which user owns this watched dir?
    for (user, entry) in watches.iter() {
        if entry.path == parent {
            return Some((user.clone(), name.to_string(), action));
        }
    }
    None
}

/// Read the file and verify age magic. Emits `Err` if the file
/// isn't age ciphertext (someone dropped plaintext by hand — log
/// and skip; don't push plaintext anywhere).
fn load_and_verify(user: &str, filename: &str) -> Result<Vec<u8>> {
    let home = home_for_user(user).context("home for user")?;
    let path = config::content_dir(&home).join(filename);
    let bytes = std::fs::read(&path)
        .with_context(|| format!("reading {}", path.display()))?;
    const AGE_MAGIC: &[u8] = b"age-encryption.org/v1\n";
    if bytes.len() < AGE_MAGIC.len() || &bytes[..AGE_MAGIC.len()] != AGE_MAGIC {
        anyhow::bail!("not age ciphertext (user-dropped plaintext?)");
    }
    Ok(bytes)
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
        let watches = one_watch("dan", "/home/dan/sigil");
        let ev = Event {
            kind: EventKind::Create(notify::event::CreateKind::File),
            paths: vec![PathBuf::from("/home/dan/sigil/tax.pdf")],
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
        let watches = one_watch("dan", "/home/dan/sigil");
        let ev = Event {
            kind: EventKind::Create(notify::event::CreateKind::File),
            paths: vec![PathBuf::from("/home/dan/sigil/.foo.tmp")],
            attrs: Default::default(),
        };
        assert!(resolve_event(&ev, &watches).is_none());
    }

    #[test]
    fn resolve_event_ignores_unrelated_parent() {
        let watches = one_watch("dan", "/home/dan/sigil");
        let ev = Event {
            kind: EventKind::Create(notify::event::CreateKind::File),
            paths: vec![PathBuf::from("/tmp/stray.txt")],
            attrs: Default::default(),
        };
        assert!(resolve_event(&ev, &watches).is_none());
    }

    #[test]
    fn resolve_event_removes_become_delete_action() {
        let watches = one_watch("dan", "/home/dan/sigil");
        let ev = Event {
            kind: EventKind::Remove(notify::event::RemoveKind::File),
            paths: vec![PathBuf::from("/home/dan/sigil/foo.pdf")],
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
        // during atomic renames (.foo.sigil.enc.tmp) get removed
        // at rename time and we don't want to propagate that as a
        // user-visible deletion.
        let watches = one_watch("dan", "/home/dan/sigil");
        let ev = Event {
            kind: EventKind::Remove(notify::event::RemoveKind::File),
            paths: vec![PathBuf::from("/home/dan/sigil/.foo.tmp")],
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
        let bogus = PathBuf::from("/nonexistent/sigil/watcher/probe");
        assert!(path_inode(&bogus).is_none());
    }

    #[test]
    fn path_inode_detects_inode_rotation_after_recreate() {
        // Create a tempdir, capture its (dev, inode), delete it, recreate
        // at the same path, and verify the new inode differs. This is
        // the exact scenario the reconciler's stale-watch detection
        // needs to catch (purge + re-enroll under the same ~/sigil/
        // path, but a different underlying inode).
        let dir = std::env::temp_dir().join(format!(
            "sigil-watcher-rotation-{}",
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
