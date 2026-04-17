// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Peer map: tracks which peers have which chunks of which blobs.
//!
//! Thread-safe via `RwLock`. Entries expire after 5 minutes (extended from
//! the original 60s to support persistent peer knowledge across restarts).
//! Merge logic takes the union of chunks and the newest `last_seen`.

use std::collections::{HashMap, HashSet};
use std::path::Path;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::RwLock;
use std::time::{Duration, Instant};

use serde::{Deserialize, Serialize};
use tracing::{info, warn};

/// How long a peer entry is considered valid.
const PEER_TTL: Duration = Duration::from_secs(300);

/// How recent a peer's bitmap must be for a listener-triggered probe to
/// skip it. Matches the prober's STALE_THRESHOLD so the two probe paths
/// share the same freshness contract — no peer is probed more than once
/// every ~5 seconds regardless of which path initiates it.
pub const PROBE_COOLDOWN: Duration = Duration::from_secs(5);

/// How a peer was discovered.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum PeerSource {
    /// Learned from another peer's /meta or X-Peers response.
    PeerExchange,
    /// Discovered via UDP multicast on the local subnet.
    Multicast,
    /// Seeded from the manifest CRDT's seeder_address field (the envoy
    /// that originated the blob, learned via manifest gossip).
    Server,
    /// Refreshed directly via a GET /blob/<sha>/meta probe. This is the
    /// most authoritative source: it reflects the peer's current bitmap
    /// as-of the probe moment. Unlike other sources (which incrementally
    /// announce new chunks), a MetaProbe report **replaces** the peer's
    /// chunk set so dropped chunks don't leak forward.
    MetaProbe,
}

/// Information about a single peer for a specific blob.
#[derive(Debug, Clone)]
pub struct PeerInfo {
    pub address: String,
    pub chunks: HashSet<u32>,
    pub last_seen: Instant,
    pub source: PeerSource,
    /// Last ETag returned by this peer's /meta endpoint, used as
    /// If-None-Match on the next probe. `None` until the first probe
    /// succeeds.
    pub etag: Option<String>,
    /// When the stored bitmap was last refreshed from this peer via
    /// /meta. Used by the prober to prioritize stale entries.
    pub bitmap_refreshed_at: Instant,
}

/// Thread-safe map of blob SHA256 → peer address → PeerInfo.
pub struct PeerMap {
    inner: RwLock<HashMap<String, HashMap<String, PeerInfo>>>,
    /// Set to true on any mutation (report, touch, gc, report_meta_probe).
    /// Cleared by `save` after a successful write. Lets the announcer skip
    /// the JSON serialization + disk write when nothing has changed since
    /// the last persist — the announcer ticks every 1s during downloads
    /// so unconditional saves added measurable cost.
    dirty: AtomicBool,
}

impl PeerMap {
    pub fn new() -> Self {
        Self {
            inner: RwLock::new(HashMap::new()),
            dirty: AtomicBool::new(false),
        }
    }

    /// Should the listener-triggered probe path fire a /meta probe against
    /// this (sha, addr) pair? Returns true when we have no entry for the
    /// pair OR the entry's bitmap was refreshed longer ago than
    /// `PROBE_COOLDOWN`. Callers use this to skip the expensive probe
    /// (build TLS agent, HTTPS round-trip) when the peer just told us
    /// their bitmap and nothing has changed since.
    pub fn should_probe(&self, sha256: &str, address: &str) -> bool {
        let map = self.inner.read().unwrap();
        let cutoff = Instant::now() - PROBE_COOLDOWN;
        match map.get(sha256).and_then(|peers| peers.get(address)) {
            Some(info) => info.bitmap_refreshed_at < cutoff,
            None => true,
        }
    }

    /// Return the stored ETag for a (sha, addr) pair, or None. Used by
    /// listener-triggered probes to issue conditional GETs — a peer whose
    /// /meta is unchanged since our last fetch returns 304 with no body
    /// instead of re-sending the full bitmap.
    pub fn stored_etag(&self, sha256: &str, address: &str) -> Option<String> {
        let map = self.inner.read().unwrap();
        map.get(sha256)
            .and_then(|peers| peers.get(address))
            .and_then(|info| info.etag.clone())
    }

    /// Check and clear the dirty flag. Returns true exactly once per state
    /// change — callers that return true commit to actually persisting.
    pub fn take_dirty(&self) -> bool {
        self.dirty.swap(false, Ordering::AcqRel)
    }

    fn mark_dirty(&self) {
        self.dirty.store(true, Ordering::Release);
    }

    /// Record that a peer has certain chunks of a blob.
    ///
    /// If the peer already exists, merges chunks (union) for incremental
    /// sources (Multicast, PeerExchange, Server) but **replaces** the set
    /// for MetaProbe, since a /meta response is the authoritative current
    /// bitmap and may reflect chunks the peer has dropped.
    pub fn report(&self, sha256: &str, address: &str, chunks: HashSet<u32>, source: PeerSource) {
        self.report_inner(sha256, address, chunks, source, None);
    }

    /// Like `report`, but also updates the stored ETag for this peer. Used
    /// by the prober when it gets a fresh /meta response.
    pub fn report_meta_probe(
        &self,
        sha256: &str,
        address: &str,
        chunks: HashSet<u32>,
        etag: Option<String>,
    ) {
        self.report_inner(sha256, address, chunks, PeerSource::MetaProbe, etag);
    }

    fn report_inner(
        &self,
        sha256: &str,
        address: &str,
        chunks: HashSet<u32>,
        source: PeerSource,
        etag: Option<String>,
    ) {
        self.mark_dirty();
        let mut map = self.inner.write().unwrap();
        let blob_peers = map.entry(sha256.to_string()).or_default();
        let now = Instant::now();

        match blob_peers.get_mut(address) {
            Some(existing) => {
                if source == PeerSource::MetaProbe {
                    // Authoritative snapshot — replace chunks entirely.
                    existing.chunks = chunks;
                    existing.bitmap_refreshed_at = now;
                    if etag.is_some() {
                        existing.etag = etag;
                    }
                } else {
                    // Incremental announcement — union chunks.
                    existing.chunks.extend(&chunks);
                }
                if now > existing.last_seen {
                    existing.last_seen = now;
                }
                // Prefer more authoritative sources.
                if source_priority(source) > source_priority(existing.source) {
                    existing.source = source;
                }
            }
            None => {
                blob_peers.insert(
                    address.to_string(),
                    PeerInfo {
                        address: address.to_string(),
                        chunks,
                        last_seen: now,
                        source,
                        etag,
                        bitmap_refreshed_at: now,
                    },
                );
            }
        }
    }

    /// Refresh `last_seen` without changing the bitmap. Called when a peer
    /// returns 304 Not Modified on a /meta probe — we know they're still
    /// alive, but their inventory hasn't changed.
    pub fn touch_peer(&self, sha256: &str, address: &str) {
        let mut map = self.inner.write().unwrap();
        if let Some(blob_peers) = map.get_mut(sha256) {
            if let Some(info) = blob_peers.get_mut(address) {
                let now = Instant::now();
                info.last_seen = now;
                info.bitmap_refreshed_at = now;
                self.dirty.store(true, Ordering::Release);
            }
        }
    }

    /// Record that a peer has ALL chunks of a blob (complete download).
    pub fn report_complete(&self, sha256: &str, address: &str, total_chunks: u32, source: PeerSource) {
        let all: HashSet<u32> = (0..total_chunks).collect();
        self.report(sha256, address, all, source);
    }

    /// Get all live peers for a blob, filtering out expired entries.
    pub fn peers_for(&self, sha256: &str) -> Vec<PeerInfo> {
        let map = self.inner.read().unwrap();
        let cutoff = Instant::now() - PEER_TTL;

        match map.get(sha256) {
            Some(blob_peers) => blob_peers
                .values()
                .filter(|p| p.last_seen > cutoff)
                .cloned()
                .collect(),
            None => Vec::new(),
        }
    }

    /// Get all live peer addresses for a blob (convenience for X-Peers header).
    pub fn peer_addresses_for(&self, sha256: &str) -> Vec<String> {
        self.peers_for(sha256)
            .into_iter()
            .map(|p| p.address)
            .collect()
    }

    /// Get all blob hashes that have at least one live peer.
    pub fn active_blobs(&self) -> Vec<String> {
        let map = self.inner.read().unwrap();
        let cutoff = Instant::now() - PEER_TTL;

        map.iter()
            .filter(|(_, peers)| peers.values().any(|p| p.last_seen > cutoff))
            .map(|(sha, _)| sha.clone())
            .collect()
    }

    /// Remove all peer entries for a specific blob SHA. Called during
    /// blob eviction so stale entries don't cause 404 floods when the
    /// same SHA is re-seeded.
    pub fn flush_sha(&self, sha256: &str) {
        let mut map = self.inner.write().unwrap();
        if map.remove(sha256).is_some() {
            self.mark_dirty();
        }
    }

    /// Remove expired entries. Call periodically to prevent unbounded growth.
    /// Only marks the map dirty when something was actually removed —
    /// GC that finds nothing to prune is a no-op as far as save() is
    /// concerned, so we don't rewrite peers.json every announce cycle.
    pub fn gc(&self) {
        let mut map = self.inner.write().unwrap();
        let cutoff = Instant::now() - PEER_TTL;
        let before: usize = map.values().map(|m| m.len()).sum();

        map.retain(|_, peers| {
            peers.retain(|_, p| p.last_seen > cutoff);
            !peers.is_empty()
        });

        let after: usize = map.values().map(|m| m.len()).sum();
        if before != after {
            self.dirty.store(true, Ordering::Release);
        }
    }

    /// Total number of peer entries (for diagnostics).
    pub fn len(&self) -> usize {
        let map = self.inner.read().unwrap();
        map.values().map(|m| m.len()).sum()
    }

    /// Return (sha256, address, stored_etag) tuples for peer entries whose
    /// bitmap is older than `threshold`. Used by the refresh loop to pick
    /// candidates for an If-None-Match /meta probe. Entries past PEER_TTL
    /// are excluded — they are considered dead and shouldn't be refreshed.
    pub fn stale_probe_targets(
        &self,
        threshold: Duration,
    ) -> Vec<(String, String, Option<String>)> {
        let map = self.inner.read().unwrap();
        let now = Instant::now();
        let stale_cutoff = now - threshold;
        let live_cutoff = now - PEER_TTL;
        let mut out = Vec::new();
        for (sha, peers) in map.iter() {
            for info in peers.values() {
                if info.bitmap_refreshed_at < stale_cutoff && info.last_seen > live_cutoff {
                    out.push((sha.clone(), info.address.clone(), info.etag.clone()));
                }
            }
        }
        out
    }

    /// Get all unique peer addresses across all blobs (for active probing).
    pub fn all_addresses(&self) -> Vec<String> {
        let map = self.inner.read().unwrap();
        let cutoff = Instant::now() - PEER_TTL;
        let mut addrs: HashSet<String> = HashSet::new();
        for peers in map.values() {
            for (addr, info) in peers {
                if info.last_seen > cutoff {
                    addrs.insert(addr.clone());
                }
            }
        }
        addrs.into_iter().collect()
    }

    /// Save peer state to disk for persistence across restarts.
    pub fn save(&self, path: &Path) {
        let map = self.inner.read().unwrap();
        let now = Instant::now();
        let mut entries = Vec::new();

        for (sha256, peers) in map.iter() {
            for (addr, info) in peers {
                let age_secs = now.duration_since(info.last_seen).as_secs();
                // Don't persist very stale entries.
                if age_secs > PEER_TTL.as_secs() * 2 {
                    continue;
                }
                entries.push(SavedPeer {
                    sha256: sha256.clone(),
                    address: addr.clone(),
                    chunk_count: info.chunks.len() as u32,
                    source: match info.source {
                        PeerSource::Multicast => "multicast",
                        PeerSource::PeerExchange => "exchange",
                        PeerSource::Server => "server",
                        PeerSource::MetaProbe => "metaprobe",
                    }.to_string(),
                    age_secs,
                });
            }
        }

        if let Ok(json) = serde_json::to_string(&entries) {
            let tmp = path.with_extension("json.tmp");
            if std::fs::write(&tmp, &json).is_ok() {
                std::fs::rename(&tmp, path).ok();
            }
        }
    }

    /// Load peer state from disk. Reconstructs Instants from saved ages.
    pub fn load(path: &Path) -> Self {
        let pm = Self::new();

        let json = match std::fs::read_to_string(path) {
            Ok(j) => j,
            Err(_) => return pm,
        };

        let entries: Vec<SavedPeer> = match serde_json::from_str(&json) {
            Ok(e) => e,
            Err(e) => {
                warn!(error = %e, "corrupt peers.json, starting fresh");
                return pm;
            }
        };

        let now = Instant::now();
        let mut count = 0;
        {
            let mut map = pm.inner.write().unwrap();
            for entry in entries {
                let source = match entry.source.as_str() {
                    "multicast" => PeerSource::Multicast,
                    "server" => PeerSource::Server,
                    "metaprobe" => PeerSource::MetaProbe,
                    _ => PeerSource::PeerExchange,
                };
                // Reconstruct last_seen from age. If age exceeds TTL, still load
                // it — the peer might still be alive and will be verified by probing.
                let last_seen = now.checked_sub(Duration::from_secs(entry.age_secs))
                    .unwrap_or(now);
                let chunks: HashSet<u32> = (0..entry.chunk_count).collect();
                let blob_peers = map.entry(entry.sha256).or_default();
                blob_peers.insert(entry.address.clone(), PeerInfo {
                    address: entry.address,
                    chunks,
                    last_seen,
                    source,
                    etag: None,
                    bitmap_refreshed_at: last_seen,
                });
                count += 1;
            }
        }

        if count > 0 {
            info!(count, "loaded peer state from disk");
        }
        pm
    }
}

/// Serialized peer entry for disk persistence.
#[derive(Serialize, Deserialize)]
struct SavedPeer {
    sha256: String,
    address: String,
    chunk_count: u32,
    source: String,
    age_secs: u64,
}

/// Higher priority = more trusted source. MetaProbe beats everything
/// because it reflects a live, targeted query of the peer's current
/// bitmap.
fn source_priority(s: PeerSource) -> u8 {
    match s {
        PeerSource::PeerExchange => 1,
        PeerSource::Multicast => 2,
        PeerSource::Server => 3,
        PeerSource::MetaProbe => 4,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_report_and_lookup() {
        let pm = PeerMap::new();
        let sha = "a".repeat(64);

        pm.report(&sha, "10.0.0.1:1531", [0, 1, 2].into(), PeerSource::Multicast);
        pm.report(&sha, "10.0.0.2:1531", [1, 3].into(), PeerSource::Server);

        let peers = pm.peers_for(&sha);
        assert_eq!(peers.len(), 2);

        let addrs = pm.peer_addresses_for(&sha);
        assert!(addrs.contains(&"10.0.0.1:1531".to_string()));
        assert!(addrs.contains(&"10.0.0.2:1531".to_string()));
    }

    #[test]
    fn test_merge_chunks() {
        let pm = PeerMap::new();
        let sha = "b".repeat(64);

        pm.report(&sha, "10.0.0.1:1531", [0, 1].into(), PeerSource::Multicast);
        pm.report(&sha, "10.0.0.1:1531", [2, 3].into(), PeerSource::PeerExchange);

        let peers = pm.peers_for(&sha);
        assert_eq!(peers.len(), 1);
        assert_eq!(peers[0].chunks.len(), 4);
        assert!(peers[0].chunks.contains(&0));
        assert!(peers[0].chunks.contains(&3));
        // Multicast has higher priority than PeerExchange.
        assert_eq!(peers[0].source, PeerSource::Multicast);
    }

    #[test]
    fn test_report_complete() {
        let pm = PeerMap::new();
        let sha = "c".repeat(64);

        pm.report_complete(&sha, "10.0.0.1:1531", 5, PeerSource::Server);

        let peers = pm.peers_for(&sha);
        assert_eq!(peers.len(), 1);
        assert_eq!(peers[0].chunks.len(), 5);
    }

    #[test]
    fn test_active_blobs() {
        let pm = PeerMap::new();
        let sha1 = "d".repeat(64);
        let sha2 = "e".repeat(64);

        pm.report(&sha1, "10.0.0.1:1531", [0].into(), PeerSource::Multicast);
        pm.report(&sha2, "10.0.0.2:1531", [0].into(), PeerSource::Server);

        let active = pm.active_blobs();
        assert_eq!(active.len(), 2);
    }

    #[test]
    fn test_gc_and_len() {
        let pm = PeerMap::new();
        let sha = "f".repeat(64);

        pm.report(&sha, "10.0.0.1:1531", [0].into(), PeerSource::Multicast);
        assert_eq!(pm.len(), 1);

        // GC shouldn't remove fresh entries.
        pm.gc();
        assert_eq!(pm.len(), 1);
    }

    #[test]
    fn test_metaprobe_replaces_chunks() {
        let pm = PeerMap::new();
        let sha = "1".repeat(64);

        // Initial multicast: peer has chunks 0, 1, 2.
        pm.report(&sha, "10.0.0.1:1531", [0, 1, 2].into(), PeerSource::Multicast);
        assert_eq!(pm.peers_for(&sha)[0].chunks.len(), 3);

        // A later /meta probe discovers the peer now only has 0, 1, 3
        // (chunk 2 was evicted, chunk 3 newly downloaded). The replace
        // semantics must drop chunk 2 rather than unioning.
        pm.report_meta_probe(
            &sha,
            "10.0.0.1:1531",
            [0, 1, 3].into(),
            Some("etag-v1".into()),
        );
        let peers = pm.peers_for(&sha);
        assert_eq!(peers.len(), 1);
        assert_eq!(peers[0].chunks, [0, 1, 3].into());
        assert_eq!(peers[0].source, PeerSource::MetaProbe);
        assert_eq!(peers[0].etag.as_deref(), Some("etag-v1"));
    }

    #[test]
    fn test_metaprobe_priority_beats_server() {
        let pm = PeerMap::new();
        let sha = "2".repeat(64);

        // Server source set first.
        pm.report(&sha, "10.0.0.1:1531", [0].into(), PeerSource::Server);
        assert_eq!(pm.peers_for(&sha)[0].source, PeerSource::Server);

        // MetaProbe must take over.
        pm.report_meta_probe(&sha, "10.0.0.1:1531", [0].into(), None);
        assert_eq!(pm.peers_for(&sha)[0].source, PeerSource::MetaProbe);

        // A later Server report must NOT downgrade MetaProbe.
        pm.report(&sha, "10.0.0.1:1531", [0, 1].into(), PeerSource::Server);
        assert_eq!(pm.peers_for(&sha)[0].source, PeerSource::MetaProbe);
    }

    #[test]
    fn test_stale_probe_targets_respects_threshold() {
        let pm = PeerMap::new();
        let sha1 = "4".repeat(64);
        let sha2 = "5".repeat(64);

        pm.report_meta_probe(
            &sha1,
            "10.0.0.1:1531",
            [0].into(),
            Some("e1".into()),
        );
        pm.report_meta_probe(&sha2, "10.0.0.2:1531", [0].into(), None);

        // With a very large threshold, nothing is stale.
        let targets = pm.stale_probe_targets(Duration::from_secs(3600));
        assert!(targets.is_empty(), "nothing should be stale yet");

        // With a zero threshold, everything is stale.
        let targets = pm.stale_probe_targets(Duration::from_secs(0));
        assert_eq!(targets.len(), 2);

        // Verify the etag is plumbed through for sha1 but absent for sha2.
        let mut got: Vec<(String, Option<String>)> = targets
            .into_iter()
            .map(|(sha, _addr, etag)| (sha, etag))
            .collect();
        got.sort();
        assert_eq!(got[0].1.as_deref(), Some("e1"));
        assert_eq!(got[1].1, None);
    }

    #[test]
    fn test_touch_peer_refreshes_without_changing_chunks() {
        let pm = PeerMap::new();
        let sha = "3".repeat(64);

        pm.report_meta_probe(
            &sha,
            "10.0.0.1:1531",
            [0, 1, 2].into(),
            Some("etag-x".into()),
        );

        // Simulates a 304 response — peer still alive, bitmap unchanged.
        pm.touch_peer(&sha, "10.0.0.1:1531");

        let peers = pm.peers_for(&sha);
        assert_eq!(peers.len(), 1);
        assert_eq!(peers[0].chunks, [0, 1, 2].into());
        assert_eq!(peers[0].etag.as_deref(), Some("etag-x"));
    }

    #[test]
    fn test_empty_lookup() {
        let pm = PeerMap::new();
        let sha = "0".repeat(64);
        assert!(pm.peers_for(&sha).is_empty());
        assert!(pm.peer_addresses_for(&sha).is_empty());
    }

    #[test]
    fn test_flush_sha_removes_all_entries() {
        let pm = PeerMap::new();
        let sha_a = "a".repeat(64);
        let sha_b = "b".repeat(64);

        pm.report(&sha_a, "10.0.0.1:1531", [0, 1].into(), PeerSource::Multicast);
        pm.report(&sha_a, "10.0.0.2:1531", [0, 2].into(), PeerSource::Server);
        pm.report(&sha_a, "10.0.0.3:1531", [1, 2].into(), PeerSource::PeerExchange);
        pm.report(&sha_b, "10.0.0.4:1531", [0].into(), PeerSource::Multicast);
        pm.report(&sha_b, "10.0.0.5:1531", [1].into(), PeerSource::Server);
        assert_eq!(pm.len(), 5);

        pm.flush_sha(&sha_a);

        assert!(pm.peers_for(&sha_a).is_empty());
        assert_eq!(pm.peers_for(&sha_b).len(), 2);
        assert_eq!(pm.len(), 2);

        let active = pm.active_blobs();
        assert_eq!(active.len(), 1);
        assert!(active.contains(&sha_b));
    }

    #[test]
    fn test_flush_sha_marks_dirty() {
        let pm = PeerMap::new();
        let sha = "c".repeat(64);

        pm.report(&sha, "10.0.0.1:1531", [0].into(), PeerSource::Multicast);
        assert!(pm.take_dirty()); // consume

        pm.flush_sha(&sha);
        assert!(pm.take_dirty(), "flush_sha should mark dirty");
    }

    #[test]
    fn test_flush_sha_noop_for_unknown() {
        let pm = PeerMap::new();
        let sha_a = "a".repeat(64);
        let sha_unknown = "f".repeat(64);

        pm.report(&sha_a, "10.0.0.1:1531", [0].into(), PeerSource::Multicast);
        assert!(pm.take_dirty()); // consume

        pm.flush_sha(&sha_unknown);
        assert!(!pm.take_dirty(), "flushing unknown SHA should not mark dirty");
        assert_eq!(pm.peers_for(&sha_a).len(), 1);
    }

    #[test]
    fn test_should_probe_skips_fresh_entries() {
        // Regression for the probe-storm bug fixed in 0.21.16: listener-
        // triggered /meta probes used to fire unconditionally on every
        // sentinel-form HAVE line, even when the peer had just reported
        // a fresh bitmap. With the 1-second active announce cadence this
        // ran ~5x too often and rebuilt the TLS stack per probe. The fix
        // gates listener probes on this cooldown check.
        let pm = PeerMap::new();
        let sha = "a".repeat(64);
        let addr = "10.0.0.1:1531";

        // Empty peer map → probe is needed.
        assert!(pm.should_probe(&sha, addr), "empty map should allow probe");

        // After a fresh report, the entry's bitmap_refreshed_at == now,
        // which is well inside PROBE_COOLDOWN → probe should be skipped.
        pm.report(&sha, addr, [0, 1, 2].into(), PeerSource::Multicast);
        assert!(!pm.should_probe(&sha, addr), "fresh report should suppress probe");

        // A different peer address still needs probing.
        assert!(pm.should_probe(&sha, "10.0.0.2:1531"), "unrelated peer should allow probe");

        // A different sha on the same peer still needs probing.
        let sha2 = "b".repeat(64);
        assert!(pm.should_probe(&sha2, addr), "different sha should allow probe");
    }

    #[test]
    fn test_take_dirty_flips_once() {
        let pm = PeerMap::new();
        let sha = "c".repeat(64);

        // Fresh map with no mutations: not dirty.
        assert!(!pm.take_dirty(), "new map should not be dirty");

        // Report mutates → dirty.
        pm.report(&sha, "10.0.0.1:1531", [0].into(), PeerSource::Multicast);
        assert!(pm.take_dirty(), "report should mark dirty");

        // take_dirty already consumed the flag — next call sees clean.
        assert!(!pm.take_dirty(), "take_dirty should clear the flag");

        // touch_peer also marks dirty (the 304 refresh path).
        pm.touch_peer(&sha, "10.0.0.1:1531");
        assert!(pm.take_dirty(), "touch_peer should mark dirty");
    }

    #[test]
    fn test_gc_noop_does_not_mark_dirty() {
        // GC that finds nothing to prune should be a true no-op — not
        // marking the map dirty means the announcer can skip the
        // peers.json save on idle cycles where the only "event" was a
        // GC tick with no expired entries. Keeps save rate proportional
        // to actual state changes instead of announce cadence.
        let pm = PeerMap::new();
        let sha = "d".repeat(64);
        pm.report(&sha, "10.0.0.1:1531", [0].into(), PeerSource::Multicast);
        assert!(pm.take_dirty()); // consume the report flag

        pm.gc(); // nothing to prune
        assert!(!pm.take_dirty(), "no-op GC should not mark dirty");
    }
}
