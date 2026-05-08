// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Per-user serialization locks for cryptsync's persistent tables.
//!
//! `TombstoneTable`, `LeavesTable`, and `PeerTable` all follow a
//! load → modify → save pattern. Two concurrent invocations
//! against the same user race: both load identical state, each
//! upserts a different entry, both save, and the last writer wins
//! — the loser's update is silently lost. We've observed this in
//! practice when the watcher fires multiple Remove events within
//! the same tick (e.g., `vigo swarm cryptsync leave --purge` removes every
//! file in `~/cryptsync/`, each one becoming a DeleteJob; the pusher
//! processes them in parallel under `MAX_INFLIGHT`). The .tmp
//! collision in the rename phase produces a save error, but even
//! when both renames succeed the read-modify-write race drops one
//! of the upserts.
//!
//! Each table exposes a `modify(user, |&mut table| -> bool)`
//! helper that grabs the per-user lock for the entire load-mutate-
//! save sequence. Granularity is per-user so user A's tombstone
//! activity doesn't block user B's, matching the per-user shape of
//! the on-disk state directories.
//!
//! Three separate lock maps (one per table type) so a tombstone
//! save doesn't block a peer-table save for the same user. They're
//! independent on disk; they should be independent in memory too.
//!
//! Holding the lock across I/O is intentional: the alternative is
//! a copy-then-CAS shape where we'd need to re-load on conflict,
//! which is more code for no real win in this code path (saves are
//! rare and brief — milliseconds per call on local disk).

use std::collections::HashMap;
use std::sync::{Arc, LazyLock, Mutex};

/// One named lock map per table type. Keyed by username; the inner
/// `Arc<Mutex<()>>` is what callers actually serialize on. The outer
/// `Mutex` is held only briefly to look up or insert the per-user
/// entry, so users contending on it during normal operation is
/// negligible.
fn lock_map(name: &'static str) -> &'static Mutex<HashMap<String, Arc<Mutex<()>>>> {
    match name {
        "tombstones" => &TOMBSTONE_LOCKS,
        "leaves" => &LEAVES_LOCKS,
        "peers" => &PEER_LOCKS,
        "puddle_members" => &PUDDLE_MEMBERS_LOCKS,
        _ => panic!("unknown cryptsync lock map: {}", name),
    }
}

static TOMBSTONE_LOCKS: LazyLock<Mutex<HashMap<String, Arc<Mutex<()>>>>> =
    LazyLock::new(|| Mutex::new(HashMap::new()));
static LEAVES_LOCKS: LazyLock<Mutex<HashMap<String, Arc<Mutex<()>>>>> =
    LazyLock::new(|| Mutex::new(HashMap::new()));
static PEER_LOCKS: LazyLock<Mutex<HashMap<String, Arc<Mutex<()>>>>> =
    LazyLock::new(|| Mutex::new(HashMap::new()));
static PUDDLE_MEMBERS_LOCKS: LazyLock<Mutex<HashMap<String, Arc<Mutex<()>>>>> =
    LazyLock::new(|| Mutex::new(HashMap::new()));

/// Acquire the per-user lock for the named table. Returns an owned
/// `Arc<Mutex<()>>` — the caller `.lock()`s it for the duration of
/// the read-modify-write sequence and drops it to release.
pub fn user_lock(table: &'static str, user: &str) -> Arc<Mutex<()>> {
    let map = lock_map(table);
    let mut guard = map.lock().unwrap_or_else(|p| p.into_inner());
    guard
        .entry(user.to_string())
        .or_insert_with(|| Arc::new(Mutex::new(())))
        .clone()
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::thread;
    use std::time::Duration;

    #[test]
    fn same_table_same_user_serializes() {
        // Two threads racing on the same (table, user) lock should
        // observe a strict happens-before — the second thread's
        // critical section starts only after the first finishes.
        let order: Arc<Mutex<Vec<&'static str>>> = Arc::new(Mutex::new(Vec::new()));
        let l1 = user_lock("tombstones", "racer");
        let order_a = order.clone();
        let order_b = order.clone();
        let h_a = thread::spawn(move || {
            let _g = l1.lock().unwrap();
            order_a.lock().unwrap().push("a-enter");
            thread::sleep(Duration::from_millis(20));
            order_a.lock().unwrap().push("a-exit");
        });
        // Slight stagger so a-enter is recorded first deterministically.
        thread::sleep(Duration::from_millis(2));
        let h_b = thread::spawn(move || {
            let l2 = user_lock("tombstones", "racer");
            let _g = l2.lock().unwrap();
            order_b.lock().unwrap().push("b-enter");
            order_b.lock().unwrap().push("b-exit");
        });
        h_a.join().unwrap();
        h_b.join().unwrap();
        let observed = order.lock().unwrap().clone();
        assert_eq!(observed, vec!["a-enter", "a-exit", "b-enter", "b-exit"]);
    }

    #[test]
    fn different_users_do_not_block() {
        // Per-user granularity: holding user A's lock must not delay
        // user B's lock.
        let l_a = user_lock("tombstones", "alice");
        let _g_a = l_a.lock().unwrap();
        // If B's acquisition were serialized on A's lock, this would
        // hang forever and the test would time out. With per-user
        // locks the acquisition is immediate.
        let l_b = user_lock("tombstones", "bob");
        let _g_b = l_b.lock().unwrap();
    }

    #[test]
    fn different_tables_do_not_block() {
        // Per-table-type granularity: a tombstone modify on user dan
        // must not delay a peers modify on user dan.
        let l_t = user_lock("tombstones", "dan");
        let _g_t = l_t.lock().unwrap();
        let l_p = user_lock("peers", "dan");
        let _g_p = l_p.lock().unwrap();
    }
}
