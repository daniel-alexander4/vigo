// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Cross-task handle for swarm wanted-labels — the only piece of the
//! legacy receiver path still in service after ADR-015 step 4. The
//! file kept the original `receiver.rs` name to preserve git-blame
//! continuity for readers tracing the gitback history; the rest of
//! the legacy machinery (the `Receiver` struct, materialization,
//! pending-wipe hysteresis, GC scheduling, etc.) was deleted in
//! 0.38.16 alongside the unused per-label state.json schema.
//!
//! `ClaimsHandle` exposes the union of LAN-multicast-announced wanted
//! labels to (a) the swarm manager (consults the wanted set on every
//! manifest evaluation to decide which blobs to fetch) and (b) the
//! discovery listener (writes new entries via
//! [`ClaimsHandle::ingest_lan_claim`] when GITBACKCLAIM lines arrive).
//! The cross-subnet direct-push handler in `swarm::server` calls the
//! same method when a remote envoy pushes a bundle directly.

use std::collections::{HashMap, HashSet};
use std::sync::{Arc, Mutex, RwLock};

/// LAN claim TTL in seconds. A GITBACKCLAIM entry lives 3× the
/// standard peer announcement cadence — long enough to ride out
/// transient multicast loss but short enough that a silently-dead
/// source is pruned within a minute. `ingest_lan_claim` resets
/// this window on every refresh.
pub const LAN_CLAIM_TTL_SECS: u64 = 90;

/// Cheap-to-clone handle exposing the union of LAN-announced wanted
/// labels. Consumers (swarm manager, discovery listener) read the
/// current set via [`ClaimsHandle::snapshot`] or the Arc-wrapped
/// [`ClaimsHandle::shared`] view.
#[derive(Clone)]
pub struct ClaimsHandle {
    inner: Arc<Mutex<ClaimsInner>>,
    /// Cached union view rebuilt on every mutation. Exposed to
    /// the swarm manager via [`ClaimsHandle::shared`] — which the
    /// manager holds as `Arc<RwLock<HashSet<String>>>` directly.
    /// Keeping this as a separate `RwLock` means reader paths (the
    /// swarm manager's hot path on every manifest evaluation) don't
    /// contend with the writer path (LAN ingest).
    union: Arc<RwLock<HashSet<String>>>,
}

struct ClaimsInner {
    /// LAN-announced claims. Key: (label, source_hostname).
    /// Value: unix seconds when this entry expires if the source
    /// stops re-announcing. Callers prune via
    /// [`ClaimsHandle::prune_lan_expired`].
    lan_claims: HashMap<(String, String), u64>,
}

impl ClaimsHandle {
    pub fn new() -> Self {
        Self {
            inner: Arc::new(Mutex::new(ClaimsInner {
                lan_claims: HashMap::new(),
            })),
            union: Arc::new(RwLock::new(HashSet::new())),
        }
    }

    /// Ingest a single LAN-announced claim decision.
    ///
    /// `label` is the full gitback content-seed label.
    /// `source` is the announcing envoy's canonical hostname.
    /// `in_claim_list` is `true` if this envoy's hostname appeared
    /// in the source's claim list for that label — meaning "the
    /// source wants me to mirror this" — and `false` if it did
    /// not (the source explicitly removed us, or we were never in
    /// the list).
    ///
    /// If `in_claim_list` is true, an entry `(label, source)` is
    /// inserted or refreshed with an expiry of
    /// `now + LAN_CLAIM_TTL_SECS`. If false, any existing entry
    /// for this exact `(label, source)` pair is removed. Entries
    /// for the same label from OTHER sources are not affected —
    /// in the transient case where two source envoys both
    /// announce claim lists for the same label, one dropping the
    /// receiver doesn't override the other's continued claim.
    pub fn ingest_lan_claim(
        &self,
        label: &str,
        source: &str,
        in_claim_list: bool,
        now: u64,
    ) {
        let mut inner = self.inner.lock().unwrap_or_else(|e| e.into_inner());
        let key = (label.to_string(), source.to_string());
        if in_claim_list {
            inner.lan_claims.insert(key, now + LAN_CLAIM_TTL_SECS);
        } else {
            inner.lan_claims.remove(&key);
        }
        let new_union = inner.recompute_union();
        drop(inner);
        let mut u = self.union.write().unwrap_or_else(|e| e.into_inner());
        *u = new_union;
    }

    /// Drop LAN claim entries whose TTL has expired. Returns the
    /// number of rows removed.
    pub fn prune_lan_expired(&self, now: u64) -> usize {
        let mut inner = self.inner.lock().unwrap_or_else(|e| e.into_inner());
        let before = inner.lan_claims.len();
        inner.lan_claims.retain(|_, exp| *exp > now);
        let removed = before - inner.lan_claims.len();
        if removed > 0 {
            let new_union = inner.recompute_union();
            drop(inner);
            let mut u = self.union.write().unwrap_or_else(|e| e.into_inner());
            *u = new_union;
        }
        removed
    }

    /// Snapshot the current wanted set (union of all live LAN
    /// claims, deduplicated by label).
    pub fn snapshot(&self) -> HashSet<String> {
        self.union.read().unwrap_or_else(|e| e.into_inner()).clone()
    }

    /// Return the underlying `Arc<RwLock>` over the union view for
    /// callers that want a long-lived borrow without going through
    /// the `ClaimsHandle` clone. Used by `agent/src/main.rs` to
    /// hand the shared lock to the swarm manager (which lives in
    /// a different crate boundary from main.rs and so needs the
    /// raw type, not the gitback wrapper). Not part of the
    /// documented gitback API.
    pub fn shared(&self) -> Arc<RwLock<HashSet<String>>> {
        self.union.clone()
    }
}

impl ClaimsInner {
    /// Recompute the wanted-labels set from (non-expired)
    /// lan_claims entries. Caller is expected to hold the `inner`
    /// lock while calling this and then write the returned set to
    /// `ClaimsHandle.union` under the union write lock.
    fn recompute_union(&self) -> HashSet<String> {
        let mut out = HashSet::new();
        for (label, _src) in self.lan_claims.keys() {
            out.insert(label.clone());
        }
        out
    }
}

impl Default for ClaimsHandle {
    fn default() -> Self {
        Self::new()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn claims_handle_clones_share_state() {
        let a = ClaimsHandle::new();
        let b = a.clone();
        a.ingest_lan_claim("x", "peer1", true, 1000);
        assert!(b.snapshot().contains("x"));
    }

    #[test]
    fn claims_handle_ingest_false_removes_specific_source_only() {
        // Two sources both claim the same label. Dropping one
        // should not affect the other — the label stays in the
        // union as long as at least one source claims it.
        let h = ClaimsHandle::new();
        h.ingest_lan_claim("shared", "peer1", true, 1000);
        h.ingest_lan_claim("shared", "peer2", true, 1000);
        assert!(h.snapshot().contains("shared"));

        h.ingest_lan_claim("shared", "peer1", false, 1000);
        // Still there because peer2 still claims it.
        assert!(h.snapshot().contains("shared"));

        h.ingest_lan_claim("shared", "peer2", false, 1000);
        // Now gone.
        assert!(!h.snapshot().contains("shared"));
    }

    #[test]
    fn claims_handle_prune_lan_expired_drops_stale_entries() {
        let h = ClaimsHandle::new();
        h.ingest_lan_claim("fresh", "peer1", true, 1000);
        h.ingest_lan_claim("stale", "peer1", true, 1);
        // Advance well past the TTL of the second (which was
        // added with now=1).
        let removed = h.prune_lan_expired(1 + LAN_CLAIM_TTL_SECS + 1);
        assert_eq!(removed, 1);
        let snap = h.snapshot();
        // "fresh" was ingested with now=1000, TTL=90, so expires
        // at 1090. The prune-now of (1 + TTL + 1) = 92 is still
        // well before 1090 — "fresh" survives.
        assert!(snap.contains("fresh"));
        assert!(!snap.contains("stale"));
    }

    #[test]
    fn claims_handle_refresh_extends_ttl() {
        let h = ClaimsHandle::new();
        h.ingest_lan_claim("label", "peer", true, 1000);
        // Simulate a later re-announcement — should refresh the
        // expiry to now=5000 + TTL, not 1000 + TTL.
        h.ingest_lan_claim("label", "peer", true, 5000);
        // Prune at 1000 + TTL + 1: would evict if the first
        // timestamp were still in force, but the refresh means
        // expiry is 5000 + TTL, so the entry survives.
        let removed = h.prune_lan_expired(1000 + LAN_CLAIM_TTL_SECS + 1);
        assert_eq!(removed, 0);
        assert!(h.snapshot().contains("label"));
    }

    #[test]
    fn claims_handle_prune_no_expired_is_cheap() {
        let h = ClaimsHandle::new();
        h.ingest_lan_claim("a", "p", true, 1000);
        h.ingest_lan_claim("b", "p", true, 1000);
        // Prune well before TTL.
        let removed = h.prune_lan_expired(1000);
        assert_eq!(removed, 0);
        assert_eq!(h.snapshot().len(), 2);
    }

    #[test]
    fn claims_handle_shared_view_reflects_lan_ingest() {
        // The swarm manager reads from `shared()` which points
        // at the union view. LAN ingest must show up there as
        // well, not just via snapshot().
        let h = ClaimsHandle::new();
        let shared = h.shared();
        h.ingest_lan_claim("lan-label", "peer", true, 1000);
        let set = shared.read().unwrap().clone();
        assert!(set.contains("lan-label"));
    }
}
