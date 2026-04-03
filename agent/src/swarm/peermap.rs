// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Peer map: tracks which peers have which chunks of which blobs.
//!
//! Thread-safe via `RwLock`. Entries expire after 60 seconds (peer state
//! changes fast during active distribution). Merge logic takes the union
//! of chunks and the newest `last_seen`.

use std::collections::{HashMap, HashSet};
use std::sync::RwLock;
use std::time::{Duration, Instant};

/// How long a peer entry is considered valid.
const PEER_TTL: Duration = Duration::from_secs(60);

/// How a peer was discovered.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum PeerSource {
    /// Discovered via UDP multicast on the local subnet.
    Multicast,
    /// Learned from another peer's /meta or X-Peers response.
    PeerExchange,
    /// Provided by the server in CheckInResponse.swarm_assignments.
    Server,
}

/// Information about a single peer for a specific blob.
#[derive(Debug, Clone)]
pub struct PeerInfo {
    pub address: String,
    pub chunks: HashSet<u32>,
    pub last_seen: Instant,
    pub source: PeerSource,
}

/// Thread-safe map of blob SHA256 → peer address → PeerInfo.
pub struct PeerMap {
    inner: RwLock<HashMap<String, HashMap<String, PeerInfo>>>,
}

impl PeerMap {
    pub fn new() -> Self {
        Self {
            inner: RwLock::new(HashMap::new()),
        }
    }

    /// Record that a peer has certain chunks of a blob.
    ///
    /// If the peer already exists, merges chunks (union) and updates
    /// `last_seen` if the new timestamp is newer.
    pub fn report(&self, sha256: &str, address: &str, chunks: HashSet<u32>, source: PeerSource) {
        let mut map = self.inner.write().unwrap();
        let blob_peers = map.entry(sha256.to_string()).or_default();
        let now = Instant::now();

        match blob_peers.get_mut(address) {
            Some(existing) => {
                // Merge: union of chunks, newest last_seen wins.
                existing.chunks.extend(&chunks);
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
                    },
                );
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

    /// Remove expired entries. Call periodically to prevent unbounded growth.
    pub fn gc(&self) {
        let mut map = self.inner.write().unwrap();
        let cutoff = Instant::now() - PEER_TTL;

        map.retain(|_, peers| {
            peers.retain(|_, p| p.last_seen > cutoff);
            !peers.is_empty()
        });
    }

    /// Total number of peer entries (for diagnostics).
    pub fn len(&self) -> usize {
        let map = self.inner.read().unwrap();
        map.values().map(|m| m.len()).sum()
    }
}

/// Higher priority = more trusted source.
fn source_priority(s: PeerSource) -> u8 {
    match s {
        PeerSource::Multicast => 2,
        PeerSource::PeerExchange => 1,
        PeerSource::Server => 3,
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
    fn test_empty_lookup() {
        let pm = PeerMap::new();
        let sha = "0".repeat(64);
        assert!(pm.peers_for(&sha).is_empty());
        assert!(pm.peer_addresses_for(&sha).is_empty());
    }
}
