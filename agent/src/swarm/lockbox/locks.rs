// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Per-user serialization locks for lockbox's persistent tables.
//!
//! `TombstoneTable`, `LeavesTable`, `PeerTable`, `PuddleMembersTable`,
//! and `ResurrectsTable` all follow a load → modify → save pattern.
//! Two concurrent invocations against the same user race: both load
//! identical state, each upserts a different entry, both save, and
//! the last writer wins — the loser's update is silently lost. We've
//! observed this in practice when the watcher fires multiple Remove
//! events within the same tick (e.g., `vigo swarm lockbox leave --purge`
//! removes every file in `~/lockbox/`, each one becoming a DeleteJob;
//! the pusher processes them in parallel under `MAX_INFLIGHT`). The
//! .tmp collision in the rename phase produces a save error, but even
//! when both renames succeed the read-modify-write race drops one of
//! the upserts.
//!
//! Each table exposes a `modify(user, |&mut table| -> bool)`
//! helper that grabs the per-user lock for the entire load-mutate-
//! save sequence. Granularity is per-user so user A's tombstone
//! activity doesn't block user B's, matching the per-user shape of
//! the on-disk state directories.
//!
//! Separate lock maps (one per table type) so a tombstone save
//! doesn't block a peer-table save for the same user. They're
//! independent on disk; they should be independent in memory too.
//!
//! Holding the lock across I/O is intentional: the alternative is
//! a copy-then-CAS shape where we'd need to re-load on conflict,
//! which is more code for no real win in this code path (saves are
//! rare and brief — milliseconds per call on local disk).
//!
//! ## Memory bound
//!
//! The per-user entries are interned via `Weak<Mutex<()>>` rather
//! than retained `Arc<…>`. Live callers hold a strong `Arc` returned
//! by `user_lock`, which keeps the underlying Mutex alive AND keeps
//! the map's `Weak` upgradable so a sibling concurrent caller for
//! the same user gets the same Mutex (the serialization guarantee).
//! When every caller drops its `Arc`, the strong count hits zero,
//! the Mutex is freed, and the map's `Weak` becomes a dead pointer.
//! Dead pointers are evicted lazily via `retain(strong_count > 0)`
//! on the next `user_lock` insert — amortizing cleanup against
//! new-user arrivals (rare on a stable envoy) without a background
//! sweeper. LRU eviction would be incorrect here: evicting an entry
//! whose `Arc` is held mid-sequence and re-creating a fresh Mutex
//! for a sibling caller silently re-introduces the very RMW race
//! this module exists to prevent.

use std::collections::HashMap;
use std::sync::{Arc, LazyLock, Mutex, Weak};

/// Closed set of per-user lock tables. Adding a table here is the
/// only way to introduce a new lock domain — the exhaustive match
/// in `lock_map` makes it impossible to forget the wiring (the
/// previous string-keyed lookup quietly panicked on unknown names
/// and an undeclared call site in `resurrects.rs` was discovered
/// only when the panic shape was reviewed).
#[derive(Copy, Clone, Debug, PartialEq, Eq, Hash)]
pub enum LockTable {
    Tombstones,
    Leaves,
    Peers,
    PuddleMembers,
    Resurrects,
}

fn lock_map(table: LockTable) -> &'static Mutex<HashMap<String, Weak<Mutex<()>>>> {
    match table {
        LockTable::Tombstones => &TOMBSTONE_LOCKS,
        LockTable::Leaves => &LEAVES_LOCKS,
        LockTable::Peers => &PEER_LOCKS,
        LockTable::PuddleMembers => &PUDDLE_MEMBERS_LOCKS,
        LockTable::Resurrects => &RESURRECTS_LOCKS,
    }
}

static TOMBSTONE_LOCKS: LazyLock<Mutex<HashMap<String, Weak<Mutex<()>>>>> =
    LazyLock::new(|| Mutex::new(HashMap::new()));
static LEAVES_LOCKS: LazyLock<Mutex<HashMap<String, Weak<Mutex<()>>>>> =
    LazyLock::new(|| Mutex::new(HashMap::new()));
static PEER_LOCKS: LazyLock<Mutex<HashMap<String, Weak<Mutex<()>>>>> =
    LazyLock::new(|| Mutex::new(HashMap::new()));
static PUDDLE_MEMBERS_LOCKS: LazyLock<Mutex<HashMap<String, Weak<Mutex<()>>>>> =
    LazyLock::new(|| Mutex::new(HashMap::new()));
static RESURRECTS_LOCKS: LazyLock<Mutex<HashMap<String, Weak<Mutex<()>>>>> =
    LazyLock::new(|| Mutex::new(HashMap::new()));

/// Acquire the per-user lock for the named table. Returns an owned
/// `Arc<Mutex<()>>` — the caller `.lock()`s it for the duration of
/// the read-modify-write sequence and drops it to release. Two
/// concurrent calls for the same `(table, user)` return Arcs that
/// share the same underlying Mutex; once the last Arc drops, the
/// map entry becomes a dead `Weak` and gets swept the next time a
/// new user is inserted into the same table.
pub fn user_lock(table: LockTable, user: &str) -> Arc<Mutex<()>> {
    let map = lock_map(table);
    let mut guard = map.lock().unwrap_or_else(|p| p.into_inner());
    // Fast path: an existing entry whose Mutex still has live callers.
    if let Some(weak) = guard.get(user) {
        if let Some(arc) = weak.upgrade() {
            return arc;
        }
    }
    // Slow path: entry absent, or its last Arc was dropped (Weak is
    // dead). Sweep dead Weaks first — amortized cleanup keyed on
    // new-user arrival, which is rare on a stable envoy.
    guard.retain(|_, w| w.strong_count() > 0);
    let arc = Arc::new(Mutex::new(()));
    guard.insert(user.to_string(), Arc::downgrade(&arc));
    arc
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::thread;
    use std::time::Duration;

    fn map_size(table: LockTable) -> usize {
        lock_map(table)
            .lock()
            .unwrap_or_else(|p| p.into_inner())
            .len()
    }

    #[test]
    fn same_table_same_user_serializes() {
        // Two threads racing on the same (table, user) lock should
        // observe a strict happens-before — the second thread's
        // critical section starts only after the first finishes.
        let order: Arc<Mutex<Vec<&'static str>>> = Arc::new(Mutex::new(Vec::new()));
        let l1 = user_lock(LockTable::Tombstones, "racer");
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
            let l2 = user_lock(LockTable::Tombstones, "racer");
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
        let l_a = user_lock(LockTable::Tombstones, "alice");
        let _g_a = l_a.lock().unwrap();
        // If B's acquisition were serialized on A's lock, this would
        // hang forever and the test would time out. With per-user
        // locks the acquisition is immediate.
        let l_b = user_lock(LockTable::Tombstones, "bob");
        let _g_b = l_b.lock().unwrap();
    }

    #[test]
    fn different_tables_do_not_block() {
        // Per-table granularity: a tombstone modify on user dan must
        // not delay a peers modify on user dan.
        let l_t = user_lock(LockTable::Tombstones, "dan");
        let _g_t = l_t.lock().unwrap();
        let l_p = user_lock(LockTable::Peers, "dan");
        let _g_p = l_p.lock().unwrap();
    }

    #[test]
    fn concurrent_live_callers_share_underlying_mutex() {
        // The serialization guarantee: two callers active
        // simultaneously for the same (table, user) must receive Arcs
        // pointing at the same underlying Mutex. This is what would
        // silently break under LRU-style eviction.
        let table = LockTable::Peers;
        let user = "share_arc_test_user";
        let a1 = user_lock(table, user);
        let a2 = user_lock(table, user);
        assert!(
            Arc::ptr_eq(&a1, &a2),
            "two concurrent user_lock calls for same (table, user) must share the underlying Mutex"
        );
    }

    #[test]
    fn map_shrinks_when_last_arc_drops_via_sweep_on_insert() {
        // Use Resurrects — no production caller in any other test
        // touches it in the same test process, so we own the map
        // size for the assertion.
        let table = LockTable::Resurrects;
        let starting = map_size(table);

        // Live: 50 distinct users hold an Arc each. The map carries
        // 50 Weaks pointing at those Arcs.
        let arcs: Vec<_> = (0..50)
            .map(|i| user_lock(table, &format!("sweep_test_user_{}", i)))
            .collect();
        assert_eq!(map_size(table), starting + 50, "live Arcs hold map entries");

        // Drop every Arc. Dead Weaks linger until the next insert
        // — no background sweeper.
        drop(arcs);
        assert_eq!(
            map_size(table),
            starting + 50,
            "dead Weaks linger until next user_lock insert"
        );

        // A fresh user triggers sweep-on-insert: every dead Weak is
        // dropped, then the newcomer goes in.
        let _newcomer = user_lock(table, "sweep_test_newcomer");
        assert_eq!(
            map_size(table),
            1,
            "sweep-on-insert dropped all dead Weaks, leaving only the newcomer"
        );
    }

    // Note: a "resolve every variant" runtime test is intentionally
    // absent — the exhaustive match in `lock_map(table: LockTable)`
    // makes "variant maps to no entry" a compile error, so a runtime
    // assertion would add nothing over the type system, and the
    // shared touch of every table interferes with `map_shrinks_…`'s
    // count assertions on the Resurrects table under cargo's default
    // parallel test scheduling.
}
