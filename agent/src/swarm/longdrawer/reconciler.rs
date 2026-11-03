// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Pure reconciliation logic: given local state and a peer announcement,
//! compute the actions to take locally and the tombstones to merge.
//!
//! Reconciliation rules (Dan's confirmed design):
//!
//! For each **file** in the peer's state:
//! - If we don't have it and don't have a newer tombstone for it → fetch.
//! - If we have it with older mtime → fetch (peer's version is newer).
//! - Otherwise → no-op.
//!
//! For each **tombstone** in the peer's state:
//! - If we have a local file with `mtime < deletion_time` → delete it
//!   (the deletion is newer than our copy).
//! - If we have a local file with `mtime > deletion_time` → keep it
//!   (file was re-added after the peer deleted it; our next announce
//!   will propagate it back to them).
//! - Merge the tombstone into our log so the info propagates further.
//!
//! **Ties** (same mtime, same deletion_time) break in favor of the existing
//! local state — we never destroy data on a tie.

use super::state::{FileEntry, Tombstone, UserState};

/// An action to perform locally as a result of reconciling with a peer.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Action {
    /// Fetch the file from the peer and write it to our ~/longdrawer/.
    Fetch {
        name: String,
        sha256: String,
        size: u64,
        mtime: i64,
    },
    /// Delete the file from our ~/longdrawer/.
    Delete { name: String },
}

/// Result of reconciling local state with an incoming peer announcement.
pub struct ReconcileOutcome {
    pub actions: Vec<Action>,
    /// Tombstones to merge into our local state. Callers should then rescan
    /// and re-save the state.
    pub merged_tombstones: Vec<(String, Tombstone)>,
}

/// Compute actions to apply locally based on a peer's state.
///
/// This function is pure: it does not touch the filesystem or persist state.
pub fn reconcile(local: &UserState, peer: &UserState) -> ReconcileOutcome {
    let mut actions = Vec::new();
    let mut merged_tombstones = Vec::new();

    // Process peer file entries.
    for (name, entry) in &peer.files {
        // Is there a local tombstone that overrides this?
        if let Some(our_tomb) = local.tombstones.get(name) {
            if our_tomb.deletion_time > entry.mtime {
                // We deleted it after they last touched it — ignore and let
                // our tombstone propagate to them.
                continue;
            }
        }
        // Is the peer's file tombstoned in their own state? (shouldn't be —
        // an announcement shouldn't have conflicting entries, but defend.)
        if peer.tombstones.contains_key(name) {
            continue;
        }

        match local.files.get(name) {
            None => {
                // We don't have it and don't have a blocking tombstone.
                actions.push(Action::Fetch {
                    name: name.clone(),
                    sha256: entry.sha256.clone(),
                    size: entry.size,
                    mtime: entry.mtime,
                });
            }
            Some(ours) => {
                if entry.mtime > ours.mtime && entry.sha256 != ours.sha256 {
                    actions.push(Action::Fetch {
                        name: name.clone(),
                        sha256: entry.sha256.clone(),
                        size: entry.size,
                        mtime: entry.mtime,
                    });
                }
            }
        }
    }

    // Process peer tombstones.
    for (name, tomb) in &peer.tombstones {
        // Merge into our log regardless of whether we act on it.
        merged_tombstones.push((name.clone(), tomb.clone()));

        // If we have the file and the peer's deletion is newer than our mtime → delete.
        if let Some(ours) = local.files.get(name) {
            if tomb.deletion_time > ours.mtime {
                actions.push(Action::Delete { name: name.clone() });
            }
        }
    }

    ReconcileOutcome {
        actions,
        merged_tombstones,
    }
}

/// Merge a list of tombstones into a state. For conflicts, the newer
/// deletion_time wins.
pub fn merge_tombstones(state: &mut UserState, incoming: Vec<(String, Tombstone)>) {
    for (name, tomb) in incoming {
        match state.tombstones.get(&name) {
            Some(existing) if existing.deletion_time >= tomb.deletion_time => {
                // Ours is at least as new, keep it.
            }
            _ => {
                state.tombstones.insert(name, tomb);
            }
        }
    }
}

#[allow(dead_code)]
fn _type_checkers(_f: &FileEntry, _t: &Tombstone) {}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::BTreeMap;

    fn file(sha: &str, mtime: i64) -> FileEntry {
        FileEntry { sha256: sha.into(), mtime, size: 1 }
    }

    fn state_with(files: &[(&str, FileEntry)], tombs: &[(&str, i64)]) -> UserState {
        let mut s = UserState::empty("dan");
        for (n, f) in files {
            s.files.insert((*n).to_string(), f.clone());
        }
        for (n, d) in tombs {
            s.tombstones.insert(
                (*n).to_string(),
                Tombstone { deletion_time: *d },
            );
        }
        s
    }

    #[test]
    fn fetches_missing_file() {
        let local = state_with(&[], &[]);
        let peer = state_with(&[("notes.md", file("a", 100))], &[]);
        let out = reconcile(&local, &peer);
        assert_eq!(out.actions.len(), 1);
        assert!(matches!(&out.actions[0], Action::Fetch { name, .. } if name == "notes.md"));
    }

    #[test]
    fn fetches_newer_version() {
        let local = state_with(&[("notes.md", file("old", 50))], &[]);
        let peer = state_with(&[("notes.md", file("new", 100))], &[]);
        let out = reconcile(&local, &peer);
        assert_eq!(out.actions.len(), 1);
        assert!(matches!(&out.actions[0], Action::Fetch { name, .. } if name == "notes.md"));
    }

    #[test]
    fn keeps_newer_local_version() {
        let local = state_with(&[("notes.md", file("new", 200))], &[]);
        let peer = state_with(&[("notes.md", file("old", 100))], &[]);
        let out = reconcile(&local, &peer);
        assert!(out.actions.is_empty());
    }

    #[test]
    fn deletes_when_tombstone_newer_than_local_mtime() {
        let local = state_with(&[("notes.md", file("a", 50))], &[]);
        let peer = state_with(&[], &[("notes.md", 100)]);
        let out = reconcile(&local, &peer);
        assert_eq!(out.actions.len(), 1);
        assert!(matches!(&out.actions[0], Action::Delete { name } if name == "notes.md"));
    }

    #[test]
    fn keeps_file_when_local_mtime_newer_than_tombstone() {
        // File was re-added after the peer deleted it.
        let local = state_with(&[("notes.md", file("b", 200))], &[]);
        let peer = state_with(&[], &[("notes.md", 100)]);
        let out = reconcile(&local, &peer);
        assert!(!out.actions.iter().any(|a| matches!(a, Action::Delete { .. })));
        // We still record the peer's tombstone for propagation.
        assert_eq!(out.merged_tombstones.len(), 1);
    }

    #[test]
    fn local_tombstone_blocks_peer_fetch() {
        // We deleted notes.md at T=100. Peer has it with mtime=50 (older
        // than our deletion). We should NOT fetch it back.
        let local = state_with(&[], &[("notes.md", 100)]);
        let peer = state_with(&[("notes.md", file("a", 50))], &[]);
        let out = reconcile(&local, &peer);
        assert!(out.actions.is_empty());
    }

    #[test]
    fn local_tombstone_yields_to_newer_peer_file() {
        // We deleted notes.md at T=50. Peer has it with mtime=100 (newer
        // than our deletion — they re-added it). We should fetch.
        let local = state_with(&[], &[("notes.md", 50)]);
        let peer = state_with(&[("notes.md", file("a", 100))], &[]);
        let out = reconcile(&local, &peer);
        assert_eq!(out.actions.len(), 1);
        assert!(matches!(&out.actions[0], Action::Fetch { .. }));
    }

    #[test]
    fn tombstones_propagate_via_merge() {
        let mut s = state_with(&[], &[("old.txt", 10)]);
        let incoming: Vec<(String, Tombstone)> = vec![
            ("old.txt".into(), Tombstone { deletion_time: 20 }), // newer — wins
            ("other.txt".into(), Tombstone { deletion_time: 5 }), // new entry
        ];
        merge_tombstones(&mut s, incoming);
        assert_eq!(s.tombstones.get("old.txt").unwrap().deletion_time, 20);
        assert_eq!(s.tombstones.get("other.txt").unwrap().deletion_time, 5);
    }

    #[test]
    fn merge_tombstones_keeps_newer_local() {
        let mut s = state_with(&[], &[("old.txt", 50)]);
        let incoming: Vec<(String, Tombstone)> = vec![
            ("old.txt".into(), Tombstone { deletion_time: 20 }),
        ];
        merge_tombstones(&mut s, incoming);
        assert_eq!(s.tombstones.get("old.txt").unwrap().deletion_time, 50);
    }

    // Use the BTreeMap import so the file compiles standalone.
    #[test]
    fn _typecheck() {
        let _ = BTreeMap::<String, FileEntry>::new();
    }
}
