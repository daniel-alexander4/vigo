// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0

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

    /// Drain all pending delta events in sequence order.
    /// Returns the events and deletes them from the queue.
    pub fn drain(
        &self,
        wtxn: &mut RwTxn<'_>,
    ) -> Result<Vec<crate::proto::DeltaEvent>> {
        let mut results = Vec::new();
        let mut keys_to_delete = Vec::new();

        {
            let iter = self.db.iter(wtxn)?;
            for item in iter {
                let (k, v) = item?;
                if !k.starts_with("d:") {
                    continue;
                }
                match crate::proto::DeltaEvent::decode(v) {
                    Ok(delta) => {
                        keys_to_delete.push(k.to_string());
                        results.push(delta);
                    }
                    Err(e) => {
                        tracing::warn!(key = k, error = %e, "skipping corrupt pending entry");
                        keys_to_delete.push(k.to_string());
                    }
                }
            }
        }

        for k in keys_to_delete {
            self.db.delete(wtxn, &k)?;
        }

        Ok(results)
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
                status: "success".into(),
                total_count: 1,
                changed_count: 1,
                ..Default::default()
            })),
        }
    }

    #[test]
    fn enqueue_and_drain() {
        let dir = tempfile::tempdir().unwrap();
        let store = crate::state::StateStore::open(dir.path()).unwrap();

        let mut wtxn = store.write_txn().unwrap();
        store.pending.enqueue(&mut wtxn, &make_delta("run-1", 1)).unwrap();
        store.pending.enqueue(&mut wtxn, &make_delta("run-2", 2)).unwrap();
        store.pending.enqueue(&mut wtxn, &make_delta("run-3", 3)).unwrap();
        wtxn.commit().unwrap();

        let rtxn = store.read_txn().unwrap();
        assert_eq!(store.pending.len(&rtxn).unwrap(), 3);
        drop(rtxn);

        let mut wtxn = store.write_txn().unwrap();
        let drained = store.pending.drain(&mut wtxn).unwrap();
        wtxn.commit().unwrap();

        assert_eq!(drained.len(), 3);

        // Verify run_ids from the RunCompleted events.
        for (i, d) in drained.iter().enumerate() {
            if let Some(delta_event::Event::Run(ref run)) = d.event {
                assert_eq!(run.run_id, format!("run-{}", i + 1));
            } else {
                panic!("expected RunCompleted event");
            }
        }

        let rtxn = store.read_txn().unwrap();
        assert_eq!(store.pending.len(&rtxn).unwrap(), 0);
    }

    #[test]
    fn drain_empty() {
        let dir = tempfile::tempdir().unwrap();
        let store = crate::state::StateStore::open(dir.path()).unwrap();

        let mut wtxn = store.write_txn().unwrap();
        let drained = store.pending.drain(&mut wtxn).unwrap();
        wtxn.commit().unwrap();
        assert!(drained.is_empty());
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
            let mut wtxn = store.write_txn().unwrap();
            let drained = store.pending.drain(&mut wtxn).unwrap();
            wtxn.commit().unwrap();
            assert_eq!(drained.len(), 1);
            if let Some(delta_event::Event::Run(ref run)) = drained[0].event {
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

        let mut wtxn = store.write_txn().unwrap();
        let drained = store.pending.drain(&mut wtxn).unwrap();
        wtxn.commit().unwrap();

        assert_eq!(drained.len(), 1);
        assert!(matches!(drained[0].event, Some(delta_event::Event::Heartbeat(_))));
    }
}
