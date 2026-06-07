// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Pending results queue for offline convergence.
//!
//! When the agent converges offline (server unreachable), delta events are
//! enqueued here and delivered on reconnect. Uses auto-incrementing
//! sequence numbers as keys for ordered delivery.

use anyhow::Result;
use heed::types::{Bytes, Str};
use heed::{Database, Env, RoTxn, RwTxn};
use prost::Message;

/// Key for the next sequence counter.
const SEQ_KEY: &str = "__seq";

/// Maximum pending entries before oldest are dropped.
const MAX_PENDING: u64 = 1000;

/// A pending entry: its storage key and the decoded delta. The key is carried
/// so the caller can remove exactly the entries it successfully delivered.
pub struct PendingEntry {
    pub key: String,
    pub delta: crate::proto::DeltaEvent,
}

/// LMDB-backed pending delta queue.
pub struct PendingQueue {
    db: Database<Str, Bytes>,
}

impl PendingQueue {
    /// Open (or create) the pending queue database.
    pub fn open(env: &Env, wtxn: &mut RwTxn<'_>) -> Result<Self> {
        let db = env
            .create_database(wtxn, Some("pending"))
            .map_err(|e| anyhow::anyhow!("creating pending db: {}", e))?;
        Ok(Self { db })
    }

    /// Increment and store the sequence counter, returning the new value.
    pub fn next_seq(&self, wtxn: &mut RwTxn<'_>) -> Result<u64> {
        let current = self.current_seq_inner(wtxn)?;
        let next = current + 1;
        self.db.put(wtxn, SEQ_KEY, &next.to_le_bytes())?;
        Ok(next)
    }

    /// Read the current sequence counter without incrementing.
    pub fn current_seq(&self, rtxn: &RoTxn<'_>) -> Result<u64> {
        match self.db.get(rtxn, SEQ_KEY)? {
            Some(b) if b.len() == 8 => {
                let mut arr = [0u8; 8];
                arr.copy_from_slice(b);
                Ok(u64::from_le_bytes(arr))
            }
            _ => Ok(0),
        }
    }

    /// Read the current sequence from a write txn (helper).
    fn current_seq_inner(&self, wtxn: &mut RwTxn<'_>) -> Result<u64> {
        match self.db.get(wtxn, SEQ_KEY)? {
            Some(b) if b.len() == 8 => {
                let mut arr = [0u8; 8];
                arr.copy_from_slice(b);
                Ok(u64::from_le_bytes(arr))
            }
            _ => Ok(0),
        }
    }

    /// Enqueue a DeltaEvent for later delivery.
    pub fn enqueue(
        &self,
        wtxn: &mut RwTxn<'_>,
        delta: &crate::proto::DeltaEvent,
    ) -> Result<u64> {
        let seq = self.next_seq(wtxn)?;
        let key = format!("d:{:020}", seq);
        let bytes = delta.encode_to_vec();
        self.db.put(wtxn, &key, &bytes)?;

        // Trim oldest entries if over capacity.
        if seq > MAX_PENDING {
            let trim_below = seq - MAX_PENDING;
            let trim_key = format!("d:{:020}", trim_below);
            let mut to_delete = Vec::new();
            let iter = self.db.iter(wtxn)?;
            for item in iter {
                let (k, _) = item?;
                if k.starts_with("d:") && k < trim_key.as_str() {
                    to_delete.push(k.to_string());
                } else if k >= trim_key.as_str() {
                    break;
                }
            }
            for k in to_delete {
                self.db.delete(wtxn, &k)?;
            }
        }

        Ok(seq)
    }

    /// Count the number of pending delta events.
    pub fn len(&self, rtxn: &RoTxn<'_>) -> Result<usize> {
        let mut count = 0;
        for item in self.db.iter(rtxn)? {
            let (k, _) = item?;
            if k.starts_with("d:") {
                count += 1;
            }
        }
        Ok(count)
    }

    /// Peek all pending delta events in sequence order **without removing
    /// them**. Returns the decoded entries (each with its storage key) plus the
    /// keys of any corrupt/undecodable entries the caller should purge.
    ///
    /// Delivery works against this read-only snapshot with no transaction held
    /// across the network RPCs; the caller then `remove`s only the keys it
    /// delivered successfully. A failed delivery therefore leaves its entry
    /// queued for the next reconnect (the queue's whole purpose), and because
    /// the server keys runs by its own id — re-delivering a succeeded entry
    /// would duplicate it — only delivered entries are ever removed.
    pub fn peek(&self, rtxn: &RoTxn<'_>) -> Result<(Vec<PendingEntry>, Vec<String>)> {
        let mut entries = Vec::new();
        let mut corrupt = Vec::new();
        for item in self.db.iter(rtxn)? {
            let (k, v) = item?;
            if !k.starts_with("d:") {
                continue;
            }
            match crate::proto::DeltaEvent::decode(v) {
                Ok(delta) => entries.push(PendingEntry {
                    key: k.to_string(),
                    delta,
                }),
                Err(e) => {
                    tracing::warn!(key = k, error = %e, "purging corrupt pending entry");
                    corrupt.push(k.to_string());
                }
            }
        }
        Ok((entries, corrupt))
    }

    /// Remove the given keys from the queue. Caller passes the keys it
    /// successfully delivered (plus any corrupt keys from `peek`).
    pub fn remove(&self, wtxn: &mut RwTxn<'_>, keys: &[String]) -> Result<()> {
        for k in keys {
            self.db.delete(wtxn, k)?;
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use crate::proto::{delta_event, DeltaEvent, HeartbeatDelta, RunCompleted};

    fn make_delta(run_id: &str, seq: i64) -> DeltaEvent {
        DeltaEvent {
            sequence: seq,
            event: Some(delta_event::Event::Run(RunCompleted {
                run_id: run_id.into(),
                total_count: 1,
                ..Default::default()
            })),
        }
    }

    #[test]
    fn peek_returns_all_in_order_without_removing() {
        let dir = tempfile::tempdir().unwrap();
        let store = crate::state::StateStore::open(dir.path()).unwrap();

        let mut wtxn = store.write_txn().unwrap();
        store.pending.enqueue(&mut wtxn, &make_delta("run-1", 1)).unwrap();
        store.pending.enqueue(&mut wtxn, &make_delta("run-2", 2)).unwrap();
        store.pending.enqueue(&mut wtxn, &make_delta("run-3", 3)).unwrap();
        wtxn.commit().unwrap();

        let rtxn = store.read_txn().unwrap();
        let (entries, corrupt) = store.pending.peek(&rtxn).unwrap();
        drop(rtxn);
        assert_eq!(entries.len(), 3);
        assert!(corrupt.is_empty());
        for (i, e) in entries.iter().enumerate() {
            if let Some(delta_event::Event::Run(ref run)) = e.delta.event {
                assert_eq!(run.run_id, format!("run-{}", i + 1));
            } else {
                panic!("expected RunCompleted event");
            }
        }

        // peek must NOT delete — the queue is unchanged.
        let rtxn = store.read_txn().unwrap();
        assert_eq!(store.pending.len(&rtxn).unwrap(), 3);
    }

    // Regression test for the offline run-result data-loss bug: delivery removes
    // ONLY the entries it succeeded on. A failed delivery (here: run-2 simulated
    // as undelivered) must leave its entry queued for the next reconnect, never
    // dropped. Before the fix, `drain` deleted every entry up front and committed
    // regardless of delivery outcome, silently losing undelivered results.
    #[test]
    fn remove_is_selective_failed_entries_survive() {
        let dir = tempfile::tempdir().unwrap();
        let store = crate::state::StateStore::open(dir.path()).unwrap();

        let mut wtxn = store.write_txn().unwrap();
        store.pending.enqueue(&mut wtxn, &make_delta("run-1", 1)).unwrap();
        store.pending.enqueue(&mut wtxn, &make_delta("run-2", 2)).unwrap();
        store.pending.enqueue(&mut wtxn, &make_delta("run-3", 3)).unwrap();
        wtxn.commit().unwrap();

        // Simulate a delivery where run-2 (the middle entry) failed: remove only
        // the keys for run-1 and run-3.
        let rtxn = store.read_txn().unwrap();
        let (entries, _) = store.pending.peek(&rtxn).unwrap();
        drop(rtxn);
        let delivered: Vec<String> = entries
            .iter()
            .filter(|e| match &e.delta.event {
                Some(delta_event::Event::Run(run)) => run.run_id != "run-2",
                _ => false,
            })
            .map(|e| e.key.clone())
            .collect();
        assert_eq!(delivered.len(), 2);

        let mut wtxn = store.write_txn().unwrap();
        store.pending.remove(&mut wtxn, &delivered).unwrap();
        wtxn.commit().unwrap();

        // run-2 must still be queued; run-1 and run-3 are gone.
        let rtxn = store.read_txn().unwrap();
        let (remaining, _) = store.pending.peek(&rtxn).unwrap();
        assert_eq!(remaining.len(), 1);
        if let Some(delta_event::Event::Run(ref run)) = remaining[0].delta.event {
            assert_eq!(run.run_id, "run-2");
        } else {
            panic!("expected the failed run-2 to survive");
        }
    }

    #[test]
    fn peek_empty() {
        let dir = tempfile::tempdir().unwrap();
        let store = crate::state::StateStore::open(dir.path()).unwrap();

        let rtxn = store.read_txn().unwrap();
        let (entries, corrupt) = store.pending.peek(&rtxn).unwrap();
        assert!(entries.is_empty());
        assert!(corrupt.is_empty());
    }

    #[test]
    fn survives_reopen() {
        let dir = tempfile::tempdir().unwrap();

        {
            let store = crate::state::StateStore::open(dir.path()).unwrap();
            let mut wtxn = store.write_txn().unwrap();
            store.pending.enqueue(&mut wtxn, &make_delta("run-a", 1)).unwrap();
            wtxn.commit().unwrap();
        }

        {
            let store = crate::state::StateStore::open(dir.path()).unwrap();
            let rtxn = store.read_txn().unwrap();
            let (entries, _) = store.pending.peek(&rtxn).unwrap();
            assert_eq!(entries.len(), 1);
            if let Some(delta_event::Event::Run(ref run)) = entries[0].delta.event {
                assert_eq!(run.run_id, "run-a");
            } else {
                panic!("expected RunCompleted event");
            }
        }
    }

    #[test]
    fn current_seq_without_increment() {
        let dir = tempfile::tempdir().unwrap();
        let store = crate::state::StateStore::open(dir.path()).unwrap();

        let rtxn = store.read_txn().unwrap();
        assert_eq!(store.pending.current_seq(&rtxn).unwrap(), 0);
        drop(rtxn);

        let mut wtxn = store.write_txn().unwrap();
        store.pending.enqueue(&mut wtxn, &make_delta("r1", 1)).unwrap();
        store.pending.enqueue(&mut wtxn, &make_delta("r2", 2)).unwrap();
        wtxn.commit().unwrap();

        let rtxn = store.read_txn().unwrap();
        assert_eq!(store.pending.current_seq(&rtxn).unwrap(), 2);
    }

    #[test]
    fn heartbeat_enqueue() {
        let dir = tempfile::tempdir().unwrap();
        let store = crate::state::StateStore::open(dir.path()).unwrap();

        let heartbeat = DeltaEvent {
            sequence: 1,
            event: Some(delta_event::Event::Heartbeat(HeartbeatDelta {})),
        };

        let mut wtxn = store.write_txn().unwrap();
        store.pending.enqueue(&mut wtxn, &heartbeat).unwrap();
        wtxn.commit().unwrap();

        let rtxn = store.read_txn().unwrap();
        let (entries, _) = store.pending.peek(&rtxn).unwrap();
        assert_eq!(entries.len(), 1);
        assert!(matches!(entries[0].delta.event, Some(delta_event::Event::Heartbeat(_))));
    }
}
