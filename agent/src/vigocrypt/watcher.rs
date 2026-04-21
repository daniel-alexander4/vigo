// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Filesystem watcher for `~<user>/vigocrypt/` directories.
//!
//! When the user's `vigo crypt encrypt` CLI writes a ciphertext file
//! into `~<user>/vigocrypt/<name>`, the agent daemon (running as root)
//! needs to notice and fan out the bytes to every peer in the user's
//! mesh. Rather than poll, we watch each enrolled user's
//! `~/vigocrypt/` directory via inotify (Linux) or kqueue (macOS)
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
//!   `.foo.vigocrypt.enc.tmp` then renames to `foo`; we watch for the
//!   rename destination, not the temp write.
//! - Files must pass the age-magic check before push — defensive
//!   against a user dropping plaintext into `~/vigocrypt/` by hand.
//!
//! Scope guardrail: inotify's 8192 watches/process limit matters only
//! at ~8k enrolled users on one envoy, which no plausible deployment
//! has. Document, don't solve.

use std::collections::HashMap;
use std::path::PathBuf;
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
#[derive(Debug, Clone)]
pub struct PushJob {
    pub user: String,
    pub filename: String,
    pub bytes: Vec<u8>,
}

/// Reconciler tick interval — how often we add/remove watches to
/// match the current enrolled-user set.
const RECONCILE_INTERVAL: Duration = Duration::from_secs(30);

/// Debounce window — squelch events firing in rapid succession for
/// the same (user, filename). Useful when the encrypt CLI renames
/// over an existing file (CREATE + MODIFY fire together).
const DEBOUNCE: Duration = Duration::from_millis(500);

/// Spawn the watcher. Returns a receiver the pusher consumes.
pub fn spawn(cancel: CancellationToken) -> mpsc::Receiver<PushJob> {
    let (out_tx, out_rx) = mpsc::channel::<PushJob>(128);
    tokio::spawn(run(out_tx, cancel));
    out_rx
}

async fn run(out_tx: mpsc::Sender<PushJob>, cancel: CancellationToken) {
    // Map of user → watched path, so we know what to add/remove on
    // reconcile. Using HashMap here because we compare membership not
    // ordering; notify's watcher internally hashes paths anyway.
    let watches: Arc<Mutex<HashMap<String, PathBuf>>> = Arc::new(Mutex::new(HashMap::new()));

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
            warn!(error = %e, "vigocrypt watcher: failed to create; daemon will not fan out");
            return;
        }
    };

    info!("vigocrypt watcher started");

    // Kick off an immediate reconcile, then schedule periodic ones.
    let _ = raw_tx.send(RawEvent::Reconcile);
    let mut interval = tokio::time::interval(RECONCILE_INTERVAL);
    interval.tick().await; // consume the immediate tick

    // Event dedupe: remember the last emit time per (user, filename).
    let mut last_emit: HashMap<(String, String), std::time::Instant> = HashMap::new();

    loop {
        tokio::select! {
            _ = cancel.cancelled() => {
                info!("vigocrypt watcher: cancelled, exiting");
                return;
            }
            _ = interval.tick() => {
                let _ = raw_tx.send(RawEvent::Reconcile);
            }
            maybe = raw_rx.recv() => {
                match maybe {
                    Some(RawEvent::Reconcile) => {
                        reconcile_watches(&mut watcher, &watches);
                    }
                    Some(RawEvent::Notify(ev)) => {
                        if let Some((user, filename)) =
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

                            match load_and_verify(&user, &filename) {
                                Ok(bytes) => {
                                    let job = PushJob { user, filename, bytes };
                                    if let Err(e) = out_tx.try_send(job) {
                                        warn!(
                                            error = %e,
                                            "vigocrypt watcher: pusher channel full or closed, dropping event"
                                        );
                                    }
                                }
                                Err(e) => {
                                    debug!(
                                        user = %user,
                                        filename = %filename,
                                        error = %e,
                                        "vigocrypt watcher: skipping non-pushable event"
                                    );
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
/// enrolled users. Adds watches for newly-enrolled users, removes
/// watches for users who unenrolled.
fn reconcile_watches(
    watcher: &mut notify::RecommendedWatcher,
    watches: &Arc<Mutex<HashMap<String, PathBuf>>>,
) {
    let desired: std::collections::HashSet<String> = match enrolled_users() {
        Ok(s) => s,
        Err(e) => {
            warn!(error = %e, "vigocrypt watcher: enroll enumeration failed");
            return;
        }
    };

    let mut active = watches.lock().unwrap();
    // Drop watches for users no longer enrolled.
    let stale: Vec<String> = active
        .keys()
        .filter(|u| !desired.contains(*u))
        .cloned()
        .collect();
    for user in stale {
        if let Some(path) = active.remove(&user) {
            if let Err(e) = watcher.unwatch(&path) {
                debug!(user = %user, error = %e, "watcher: unwatch failed");
            } else {
                info!(user = %user, path = %path.display(), "watcher: removed");
            }
        }
    }

    // Add watches for newly-enrolled users.
    for user in &desired {
        if active.contains_key(user) {
            continue;
        }
        let home = match home_for_user(user) {
            Some(h) => h,
            None => continue,
        };
        let content = config::content_dir(&home);
        if !content.is_dir() {
            continue;
        }
        match watcher.watch(&content, RecursiveMode::NonRecursive) {
            Ok(()) => {
                info!(user = %user, path = %content.display(), "watcher: added");
                active.insert(user.clone(), content);
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

/// Map a notify event to (user, filename). Returns None for events
/// we don't care about (removes, directory events, metadata-only,
/// dotfiles).
fn resolve_event(ev: &Event, watches: &HashMap<String, PathBuf>) -> Option<(String, String)> {
    // Only act on create/modify — both can represent a final rename.
    match ev.kind {
        EventKind::Create(_) | EventKind::Modify(_) => {}
        _ => return None,
    }

    // The path that was affected.
    let p = ev.paths.first()?;
    let name = p.file_name()?.to_str()?;
    if name.starts_with('.') {
        return None;
    }
    let parent = p.parent()?;

    // Which user owns this watched dir?
    for (user, path) in watches.iter() {
        if path == parent {
            return Some((user.clone(), name.to_string()));
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

    #[test]
    fn resolve_event_matches_watched_dir() {
        let mut watches = HashMap::new();
        watches.insert("dan".to_string(), PathBuf::from("/home/dan/vigocrypt"));
        let ev = Event {
            kind: EventKind::Create(notify::event::CreateKind::File),
            paths: vec![PathBuf::from("/home/dan/vigocrypt/tax.pdf")],
            attrs: Default::default(),
        };
        let got = resolve_event(&ev, &watches);
        assert_eq!(got, Some(("dan".to_string(), "tax.pdf".to_string())));
    }

    #[test]
    fn resolve_event_ignores_dotfiles() {
        let mut watches = HashMap::new();
        watches.insert("dan".to_string(), PathBuf::from("/home/dan/vigocrypt"));
        let ev = Event {
            kind: EventKind::Create(notify::event::CreateKind::File),
            paths: vec![PathBuf::from("/home/dan/vigocrypt/.foo.tmp")],
            attrs: Default::default(),
        };
        assert!(resolve_event(&ev, &watches).is_none());
    }

    #[test]
    fn resolve_event_ignores_unrelated_parent() {
        let mut watches = HashMap::new();
        watches.insert("dan".to_string(), PathBuf::from("/home/dan/vigocrypt"));
        let ev = Event {
            kind: EventKind::Create(notify::event::CreateKind::File),
            paths: vec![PathBuf::from("/tmp/stray.txt")],
            attrs: Default::default(),
        };
        assert!(resolve_event(&ev, &watches).is_none());
    }

    #[test]
    fn resolve_event_ignores_removes() {
        let mut watches = HashMap::new();
        watches.insert("dan".to_string(), PathBuf::from("/home/dan/vigocrypt"));
        let ev = Event {
            kind: EventKind::Remove(notify::event::RemoveKind::File),
            paths: vec![PathBuf::from("/home/dan/vigocrypt/foo.pdf")],
            attrs: Default::default(),
        };
        assert!(resolve_event(&ev, &watches).is_none());
    }
}
