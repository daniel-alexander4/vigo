// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Rarest-first chunk scheduler for multi-peer parallel downloads.
//!
//! Builds an availability matrix from the PeerMap, sorts missing chunks
//! by peer count ascending (rarest first), and assigns chunks to peers
//! with bounded concurrency (max 8 simultaneous downloads).

use std::collections::{HashMap, HashSet};
use std::sync::atomic::{AtomicU32, Ordering};
use std::sync::Arc;

use anyhow::Result;
use tokio::sync::{mpsc, Semaphore};
use tracing::{debug, info, warn};

use super::cache::BlobCache;
use super::manifest::Manifest;
use super::meta::BlobMeta;
use super::peer_client;
use super::peermap::{PeerMap, PeerSource};
use super::AnnounceSignal;
use crate::update::TlsCerts;

/// Sentinel error message used to signal mid-flight revocation so the
/// top-level `download_from_swarm` can wipe the partial and exit cleanly
/// without triggering the SHA256-mismatch retry path.
const REVOKED_ERR: &str = "blob revoked mid-download";

/// Maximum concurrent peer chunk downloads.
const MAX_PEER_CONNECTIONS: usize = 8;

/// Maximum backoff between retry rounds (seconds).
const MAX_BACKOFF_SECS: u64 = 30;

/// How often (in chunks) the producer refreshes peer availability from
/// the PeerMap. At ~100ms per chunk, 100 chunks ≈ 10 seconds — fast
/// enough to pick up new peers discovered via multicast (5s interval).
const PEER_REFRESH_INTERVAL: usize = 100;

/// Endgame mode triggers when the retry loop has fewer chunks left than
/// `max(chunk_count / ENDGAME_REMAINING_RATIO, 1)`. Tail-of-download only.
const ENDGAME_REMAINING_RATIO: u32 = 20;

/// Maximum concurrent fetches across the whole endgame round. Total, not
/// per-chunk — a large fleet fanning out 8 peers × 5 chunks should still
/// cap at 32 simultaneous connections to avoid stressing the local socket
/// budget or the peer fleet.
const MAX_ENDGAME_TASKS: usize = 32;

/// Time budget for a single endgame round. Anything still outstanding
/// after this window falls back to the normal retry path.
const MAX_ENDGAME_SECS: u64 = 15;

/// Result of downloading a single chunk.
struct ChunkDownloadResult {
    index: u32,
    data: Vec<u8>,
    /// Peers discovered via peer exchange.
    new_peers: Vec<String>,
    source: String,
    duration_ms: u64,
}

/// Statistics returned by a single endgame round.
struct EndgameStats {
    succeeded: HashSet<u32>,
    duplicates: u32,
    failures: u32,
    timed_out: bool,
}

/// Maximum assembly retries after SHA256 verification failure.
/// Each failure blacklists the peers that served chunks in the bad build.
const MAX_ASSEMBLY_RETRIES: usize = 2;

/// Flush the `.sources` file every N chunks so in-progress downloads expose
/// source attribution to the trait collector.
const SOURCES_FLUSH_INTERVAL: usize = 32;

/// Maximum candidate peers considered per chunk. Beyond this, extra peers
/// add selection overhead without benefit — only MAX_PEER_CONNECTIONS (8)
/// can be in flight at once.
const MAX_CANDIDATES_PER_CHUNK: usize = 16;

// -- In-flight connection tracking ------------------------------------------

/// Tracks how many downloads are currently in flight to each peer address.
/// Shared between the producer (increments on task spawn) and the blocking
/// tasks (decrements via InFlightGuard drop). All operations are lock-free
/// via AtomicU32.
struct InFlightTracker {
    counts: HashMap<String, Arc<AtomicU32>>,
}

impl InFlightTracker {
    /// Build from the set of known peer addresses. Addresses discovered
    /// mid-download that aren't in this map get a default count of 0 via
    /// `get_count`.
    fn new(addresses: &[String]) -> Self {
        let mut counts = HashMap::with_capacity(addresses.len());
        for addr in addresses {
            counts.entry(addr.clone()).or_insert_with(|| Arc::new(AtomicU32::new(0)));
        }
        Self { counts }
    }

    /// Current in-flight count for an address. Returns 0 for unknown peers.
    fn get_count(&self, addr: &str) -> u32 {
        self.counts.get(addr).map(|c| c.load(Ordering::Relaxed)).unwrap_or(0)
    }

    /// Acquire an in-flight guard for a peer. Increments the counter now,
    /// decrements on drop (including panic unwind).
    fn acquire(&self, addr: &str) -> InFlightGuard {
        let counter = self.counts.get(addr).cloned()
            .unwrap_or_else(|| Arc::new(AtomicU32::new(0)));
        counter.fetch_add(1, Ordering::Relaxed);
        InFlightGuard { counter }
    }
}

/// RAII guard that decrements a peer's in-flight count on drop.
struct InFlightGuard {
    counter: Arc<AtomicU32>,
}

impl Drop for InFlightGuard {
    fn drop(&mut self) {
        self.counter.fetch_sub(1, Ordering::Relaxed);
    }
}

/// Download all missing chunks of a blob from peers with retry.
///
/// Returns Ok(path) on success (all chunks downloaded + assembled).
pub async fn download_from_swarm(
    meta: &BlobMeta,
    cache: &BlobCache,
    peer_map: &Arc<PeerMap>,
    tls: Option<&TlsCerts>,
    manifest: &Arc<Manifest>,
    announce: &Arc<AnnounceSignal>,
) -> Result<std::path::PathBuf> {
    // If already complete, return immediately.
    if cache.has_blob(&meta.sha256) {
        return Ok(cache.dir().join(&meta.sha256));
    }

    // Assembly-only short-circuit: if every chunk is already on disk
    // in `.partial/`, skip the disk-pressure gate and call assemble
    // directly. The check at `check_disk_space` is appropriate for
    // *fresh* downloads (where blob.size of net new bytes will land
    // on disk); for assembly the net delta is zero (peak +blob.size
    // for the temp file, then -blob.size when the partial dir is
    // removed by `assemble`). Without this branch a near-full disk
    // with all chunks already received would reject every retry
    // forever and the partial dir would never assemble.
    let chunks_on_disk = cache.chunk_bitmap(&meta.sha256, meta.chunk_count);
    if chunks_on_disk.len() as u32 == meta.chunk_count {
        info!(
            sha256 = &meta.sha256[..12],
            chunks = meta.chunk_count,
            "all chunks already on disk; assembling directly (skipping fresh-download disk gate)"
        );
        return cache.assemble(&meta.sha256, meta.size, meta.chunk_count);
    }

    // Fresh-download path. Apply the disk-pressure check; on failure
    // with a non-empty partial dir, purge the partial chunks so the
    // retry loop doesn't accumulate garbage on a disk that can't fit
    // the blob. Exit with the original error so the manager surfaces
    // "insufficient_disk" rather than swallowing it.
    if let Err(e) = cache.check_disk_space(meta.size) {
        let partial_chunks = chunks_on_disk.len();
        if partial_chunks > 0 {
            warn!(
                sha256 = &meta.sha256[..12],
                partial_chunks,
                error = %e,
                "purging partial chunks: insufficient disk for fresh download (would otherwise accumulate)"
            );
            cache.delete_partial(&meta.sha256);
        }
        return Err(e);
    }

    // Mark this download as active so the announcer switches to its 1-second
    // interval while we're running. Guard decrements on any exit path.
    let _active_guard = announce.begin_download();

    // Build peer agent (blocking mTLS client) if we have certs.
    let peer_agent = tls.and_then(|t| match peer_client::build_peer_agent(t) {
        Ok(agent) => Some(agent),
        Err(e) => {
            warn!(error = %e, "failed to build peer TLS agent — chunk downloads will be skipped");
            None
        }
    });

    // Peers blacklisted due to serving corrupted chunks (assembly SHA256 mismatch).
    let mut blacklisted_peers: HashSet<String> = HashSet::new();

    for assembly_attempt in 0..=MAX_ASSEMBLY_RETRIES {
        if assembly_attempt > 0 {
            info!(
                sha256 = &meta.sha256[..12],
                attempt = assembly_attempt,
                blacklisted = blacklisted_peers.len(),
                "retrying download after assembly failure"
            );
        }

        match download_and_assemble(meta, cache, peer_map, &peer_agent, &blacklisted_peers, manifest, announce).await {
            Ok(path) => {
                // Download complete — wake the announcer so other envoys
                // learn about the transition from partial to complete
                // within ~1 multicast cycle instead of ~5.
                announce.fire();
                return Ok(path);
            }
            Err(e) => {
                let msg = e.to_string();
                if msg.contains(REVOKED_ERR) {
                    // Mid-flight revocation: wipe the partial so the
                    // trait collector stops reporting it and the server
                    // transitions the envoy to `revoked` on the next
                    // check-in. Then propagate so the manager records
                    // the failure (not a retry).
                    cache.delete_partial(&meta.sha256);
                    info!(
                        sha256 = &meta.sha256[..12],
                        "wiped partial after mid-flight revocation"
                    );
                    return Err(e);
                }
                if !msg.contains("SHA256 mismatch") {
                    return Err(e);
                }

                // Assembly failed — identify and blacklist peers that served chunks.
                warn!(
                    sha256 = &meta.sha256[..12],
                    error = %e,
                    "assembly verification failed, blacklisting source peers"
                );
                if let Some(sources) = cache.read_chunk_sources(&meta.sha256) {
                    for peer in sources.counts.keys() {
                        if peer != "server" {
                            blacklisted_peers.insert(peer.clone());
                        }
                    }
                }
                // Delete corrupted partial chunks so they're re-downloaded.
                cache.delete_partial(&meta.sha256);
            }
        }
    }

    anyhow::bail!(
        "assembly failed after {} retries with {} blacklisted peers",
        MAX_ASSEMBLY_RETRIES,
        blacklisted_peers.len()
    )
}

/// Stable hostname-seeded shuffle key for a chunk index. Used as the
/// tiebreaker in the rarest-first sort so that two envoys looking at the
/// same rarity group attack it in different orders — the fleet spreads
/// read load across chunks that would otherwise all hit the same peer
/// first. Seahash is deterministic, non-cryptographic, and ~6 GB/s, so
/// the per-chunk cost is in the single-digit nanoseconds.
fn chunk_shuffle_key(chunk_idx: u32, hostname_hash: u64) -> u64 {
    use std::hash::Hasher;
    let mut h = seahash::SeaHasher::new();
    h.write_u64(hostname_hash);
    h.write_u32(chunk_idx);
    h.finish()
}

/// Hash the agent's hostname once per download. Empty hostname hashes to
/// zero — shuffle becomes a pure function of `chunk_idx`, which collapses
/// back to a stable-but-shared ordering. That's the safe fallback when
/// hostname resolution fails.
fn hash_hostname(hostname: &str) -> u64 {
    if hostname.is_empty() {
        return 0;
    }
    seahash::hash(hostname.as_bytes())
}

/// Best-effort hostname lookup. Falls back to "" so the shuffle degrades
/// gracefully rather than panicking.
fn local_hostname() -> String {
    hostname::get()
        .ok()
        .and_then(|os| os.into_string().ok())
        .unwrap_or_default()
}

/// Interleave chunks from different rarity bands so that the 8 concurrent
/// download slots pull from multiple rarity groups instead of exhausting
/// the rarest band first. Within each band, the hostname-seeded shuffle
/// is preserved so different envoys attack each band in different orders.
///
/// Example: given chunks sorted within bands as:
///   band 2 (seeder-only): [A, B, C, D, E]
///   band 3 (seeder + 1 peer): [F, G, H]
///   band 4+ (seeder + 2+ peers): [I, J]
/// Output: [A, F, I, B, G, J, C, H, D, E]
///
/// This ensures the first 8 chunks scheduled span all 3 bands, naturally
/// spreading connections across different peer sets.
fn interleave_rarity_bands(
    chunks: &[u32],
    availability: &HashMap<u32, Vec<String>>,
    hostname_hash: u64,
) -> Vec<u32> {
    if chunks.is_empty() {
        return Vec::new();
    }

    // Group chunks by rarity (peer count).
    let mut bands: HashMap<usize, Vec<u32>> = HashMap::new();
    for &chunk in chunks {
        let rarity = availability.get(&chunk).map(|v| v.len()).unwrap_or(0);
        bands.entry(rarity).or_default().push(chunk);
    }

    // Sort within each band by hostname-seeded shuffle.
    for band in bands.values_mut() {
        band.sort_by_key(|&idx| chunk_shuffle_key(idx, hostname_hash));
    }

    // Sort band keys ascending (rarest first) so rarest bands get the
    // first slot in each interleave round.
    let mut band_keys: Vec<usize> = bands.keys().copied().collect();
    band_keys.sort_unstable();

    // Build iterators per band for round-robin.
    let mut iters: Vec<std::vec::IntoIter<u32>> = band_keys
        .iter()
        .map(|k| bands.remove(k).unwrap().into_iter())
        .collect();

    let mut result = Vec::with_capacity(chunks.len());
    let mut exhausted = 0;
    loop {
        if exhausted == iters.len() {
            break;
        }
        for iter in &mut iters {
            if let Some(chunk) = iter.next() {
                result.push(chunk);
            }
        }
        exhausted = iters.iter().filter(|i| i.len() == 0).count();
    }

    result
}

/// Build the candidate peer list for a single chunk, excluding peers that
/// don't have the chunk, are blacklisted, or are ourselves. Centralizing this
/// here lets all three caller sites in `download_and_assemble` (initial build,
/// retry refresh, mid-download spawn) share the same logic.
fn available_peers_for_chunk(
    chunk_idx: u32,
    peers: &[crate::swarm::peermap::PeerInfo],
    blacklist: &HashSet<String>,
    self_addr: Option<&str>,
) -> Vec<String> {
    peers
        .iter()
        .filter(|p| p.chunks.contains(&chunk_idx))
        .filter(|p| !blacklist.contains(&p.address))
        .filter(|p| Some(p.address.as_str()) != self_addr)
        .map(|p| p.address.clone())
        .collect()
}

/// Fan out every remaining chunk to every peer that has it, accept the
/// first successful response per chunk, and discard duplicates. Returns
/// when all chunks succeed, the global deadline fires, or every spawned
/// fetch has reported. Bounded to `MAX_ENDGAME_TASKS` concurrent fetches
/// via a semaphore.
///
/// This is BitTorrent-style endgame mode: in the tail of a download, one
/// slow peer holding chunk N can stall the whole blob. Asking every peer
/// in parallel eats duplicate bandwidth but kills the long tail.
async fn run_endgame_round(
    sha256: &str,
    cache: &BlobCache,
    peer_agent: &Option<ureq::Agent>,
    peer_map: &Arc<PeerMap>,
    remaining: &[u32],
    availability: &HashMap<u32, Vec<String>>,
    source_counts: &mut HashMap<String, u32>,
    chunk_details: &mut Vec<(u32, String, u64)>,
) -> Result<EndgameStats> {
    let mut stats = EndgameStats {
        succeeded: HashSet::new(),
        duplicates: 0,
        failures: 0,
        timed_out: false,
    };

    let agent = match peer_agent {
        Some(a) => a.clone(),
        None => return Ok(stats),
    };

    // Flatten (chunk, peer) pairs. Worst case for a fleet of 8 peers and
    // 10 remaining chunks = 80 pairs, still well under the soft MAX_ENDGAME_TASKS
    // concurrency cap enforced below by the semaphore.
    let mut tasks: Vec<(u32, String)> = Vec::new();
    for &chunk_idx in remaining {
        if let Some(peers) = availability.get(&chunk_idx) {
            for peer in peers {
                tasks.push((chunk_idx, peer.clone()));
            }
        }
    }

    if tasks.is_empty() {
        return Ok(stats);
    }

    info!(
        sha256 = &sha256[..12],
        remaining = remaining.len(),
        fan_out = tasks.len(),
        max_tasks = MAX_ENDGAME_TASKS,
        deadline_secs = MAX_ENDGAME_SECS,
        "entering endgame mode"
    );
    let round_start = std::time::Instant::now();

    let sem = Arc::new(Semaphore::new(MAX_ENDGAME_TASKS));
    let (tx, mut rx) = mpsc::channel::<ChunkDownloadResult>(MAX_ENDGAME_TASKS);

    // Shared "already claimed" set — workers short-circuit and skip the
    // HTTP call when another peer already returned the chunk. This keeps
    // endgame's duplicate bandwidth bounded by how many fetches were
    // already in flight, not by the full fan-out count.
    let claimed: Arc<std::sync::RwLock<HashSet<u32>>> =
        Arc::new(std::sync::RwLock::new(HashSet::new()));

    // Spawn the producer on its own task so this function can drain rx
    // with an independent deadline (mirrors the normal-round producer/
    // consumer split higher up).
    let producer_sha = sha256.to_string();
    let producer_agent = agent.clone();
    let producer_sem = sem.clone();
    let producer_claimed = claimed.clone();
    tokio::spawn(async move {
        for (chunk_idx, peer_addr) in tasks {
            // Early exit if this chunk already arrived from another peer.
            if producer_claimed.read().unwrap().contains(&chunk_idx) {
                continue;
            }
            let permit = match producer_sem.clone().acquire_owned().await {
                Ok(p) => p,
                Err(_) => return,
            };
            let tx = tx.clone();
            let sha = producer_sha.clone();
            let agent = producer_agent.clone();
            let claimed = producer_claimed.clone();

            tokio::task::spawn_blocking(move || {
                let _permit = permit;
                // Re-check after acquiring the permit — the chunk might
                // have landed while we were queued behind other fetches.
                if claimed.read().unwrap().contains(&chunk_idx) {
                    return;
                }
                let start = std::time::Instant::now();
                match peer_client::fetch_chunk(&agent, &peer_addr, &sha, chunk_idx) {
                    Ok(result) => {
                        let _ = tx.blocking_send(ChunkDownloadResult {
                            index: chunk_idx,
                            data: result.data,
                            new_peers: result.peers,
                            source: peer_addr.clone(),
                            duration_ms: start.elapsed().as_millis() as u64,
                        });
                    }
                    Err(e) => {
                        debug!(
                            peer = peer_addr.as_str(),
                            chunk = chunk_idx,
                            error = %e,
                            "endgame fetch failed"
                        );
                    }
                }
            });
        }
        // tx drops here once every task is spawned, signalling end-of-stream.
    });

    let deadline = tokio::time::sleep(std::time::Duration::from_secs(MAX_ENDGAME_SECS));
    tokio::pin!(deadline);

    loop {
        tokio::select! {
            biased;
            _ = &mut deadline => {
                stats.timed_out = true;
                warn!(
                    sha256 = &sha256[..12],
                    fetched = stats.succeeded.len(),
                    remaining_after = remaining.len() - stats.succeeded.len(),
                    elapsed_ms = round_start.elapsed().as_millis() as u64,
                    "endgame deadline hit, falling back to normal retry"
                );
                break;
            }
            maybe = rx.recv() => {
                let Some(result) = maybe else {
                    // Producer drained — no more responses coming.
                    break;
                };
                if stats.succeeded.contains(&result.index) {
                    // A peer already gave us this one earlier. Count the
                    // duplicate and drop the body.
                    stats.duplicates += 1;
                    continue;
                }

                if let Err(e) = cache.write_chunk(sha256, result.index, &result.data) {
                    warn!(
                        chunk = result.index,
                        error = %e,
                        "endgame chunk write failed"
                    );
                    stats.failures += 1;
                    continue;
                }

                claimed.write().unwrap().insert(result.index);
                stats.succeeded.insert(result.index);

                *source_counts.entry(result.source.clone()).or_insert(0) += 1;
                chunk_details.push((result.index, result.source.clone(), result.duration_ms));

                for peer_addr in &result.new_peers {
                    peer_map.report(
                        sha256,
                        peer_addr,
                        HashSet::new(),
                        PeerSource::PeerExchange,
                    );
                }

                if stats.succeeded.len() == remaining.len() {
                    // Every remaining chunk landed — no point waiting on
                    // the duplicates still in flight.
                    break;
                }
            }
        }
    }

    info!(
        sha256 = &sha256[..12],
        fetched = stats.succeeded.len(),
        duplicates = stats.duplicates,
        failures = stats.failures,
        timed_out = stats.timed_out,
        elapsed_ms = round_start.elapsed().as_millis() as u64,
        "exiting endgame mode"
    );

    Ok(stats)
}

/// Inner download + assemble cycle. Separated so the outer function can
/// retry with blacklisted peers on assembly failure.
async fn download_and_assemble(
    meta: &BlobMeta,
    cache: &BlobCache,
    peer_map: &Arc<PeerMap>,
    peer_agent: &Option<ureq::Agent>,
    blacklisted_peers: &HashSet<String>,
    manifest: &Arc<Manifest>,
    announce: &Arc<AnnounceSignal>,
) -> Result<std::path::PathBuf> {
    // Short-circuit if a revocation landed between queue and now.
    if manifest.is_revoked(&meta.sha256) {
        anyhow::bail!(REVOKED_ERR);
    }

    // Scale the mid-download peer refresh interval with blob size so small
    // blobs get refresh points mid-download. The old hardcoded 100 meant
    // any blob under 100 chunks never refreshed its peer map during the
    // download, so the scheduler committed to "whatever peers existed at
    // spawn time" (usually just the seeder) and never reconsidered. Scale
    // to every chunk_count/4 with a floor of 4 and a ceiling of 100 — a
    // 30-chunk blob refreshes every 8 chunks, a 1000-chunk blob stays at 100.
    let refresh_interval: usize =
        ((meta.chunk_count as usize) / 4).clamp(4, PEER_REFRESH_INTERVAL);

    let existing = cache.chunk_bitmap(&meta.sha256, meta.chunk_count);
    let missing: Vec<u32> = (0..meta.chunk_count)
        .filter(|i| !existing.contains(i))
        .collect();

    if missing.is_empty() {
        return cache.assemble(&meta.sha256, meta.size, meta.chunk_count);
    }

    let download_start = std::time::Instant::now();

    // Compute our own swarm peer address so we can filter ourselves out of
    // the candidate peer list. Without this, every chunk request goes to
    // self first, 404s, then falls through to a real peer — wasted RTT
    // per chunk and noisy logs. The seeder is its own peer in the manifest,
    // and that's where the self-entry leaks in.
    let self_addr = crate::swarm::discovery::local_ipv4()
        .map(|ip| format!("{}:{}", ip, crate::swarm::server::DEFAULT_PORT));

    info!(
        sha256 = &meta.sha256[..12],
        missing = missing.len(),
        total = meta.chunk_count,
        blacklisted = blacklisted_peers.len(),
        "scheduling chunk downloads"
    );

    // Build availability matrix, excluding blacklisted peers and self.
    let peers = peer_map.peers_for(&meta.sha256);
    let mut availability: HashMap<u32, Vec<String>> = HashMap::new();
    for chunk_idx in &missing {
        availability.insert(
            *chunk_idx,
            available_peers_for_chunk(*chunk_idx, &peers, blacklisted_peers, self_addr.as_deref()),
        );
    }

    // Interleave rarity bands so the 8 concurrent download slots pull from
    // multiple peer sets instead of exhausting the rarest band (seeder-only
    // chunks) first. Within each band, a hostname-seeded shuffle ensures
    // different envoys attack each band in different orders.
    let hostname_hash = hash_hostname(&local_hostname());
    let ordered = interleave_rarity_bands(&missing, &availability, hostname_hash);

    // Log rarity band sizes for diagnostics.
    {
        let mut band_sizes: HashMap<usize, usize> = HashMap::new();
        for chunk in &ordered {
            let rarity = availability.get(chunk).map(|v| v.len()).unwrap_or(0);
            *band_sizes.entry(rarity).or_default() += 1;
        }
        let mut bands: Vec<(usize, usize)> = band_sizes.into_iter().collect();
        bands.sort_unstable();
        let summary: Vec<String> = bands.iter().map(|(r, n)| format!("r{}={}", r, n)).collect();
        info!(
            sha256 = &meta.sha256[..12],
            bands = %summary.join(" "),
            "rarity band distribution"
        );
    }

    let mut source_counts: HashMap<String, u32> = HashMap::new();
    let mut chunk_details: Vec<(u32, String, u64)> = Vec::new();

    // Pre-fill with previously-cached chunks under the synthetic "cached" source.
    // Without this, a partial reseed leaves most of the blob's provenance blank —
    // the viz shows grey for anything not downloaded in the current run.
    if !existing.is_empty() {
        source_counts.insert("cached".to_string(), existing.len() as u32);
        let mut cached_sorted: Vec<u32> = existing.iter().copied().collect();
        cached_sorted.sort_unstable();
        for idx in cached_sorted {
            chunk_details.push((idx, "cached".to_string(), 0));
        }
    }

    // Retry loop: attempt all missing chunks, retry until no progress is made.
    let mut remaining: Vec<u32> = ordered;
    let mut attempt: u32 = 0;

    loop {
        if remaining.is_empty() {
            break;
        }

        // Bail cleanly if the blob was revoked between attempts. The outer
        // `download_from_swarm` catches this error and wipes the partial.
        if manifest.is_revoked(&meta.sha256) {
            anyhow::bail!(REVOKED_ERR);
        }

        if attempt > 0 {
            let backoff = std::time::Duration::from_secs(2u64.pow(attempt.min(5)).min(MAX_BACKOFF_SECS));
            info!(
                sha256 = &meta.sha256[..12],
                attempt,
                remaining = remaining.len(),
                backoff_secs = backoff.as_secs(),
                "retrying failed chunks"
            );
            tokio::time::sleep(backoff).await;

            // Refresh peer availability — new peers may have appeared.
            let peers = peer_map.peers_for(&meta.sha256);
            for chunk_idx in &remaining {
                availability.insert(
                    *chunk_idx,
                    available_peers_for_chunk(
                        *chunk_idx,
                        &peers,
                        blacklisted_peers,
                        self_addr.as_deref(),
                    ),
                );
            }
        }

        // Endgame mode: once we're on a retry attempt and only the tail
        // remains, fan out to every peer that has each chunk instead of
        // asking one peer per chunk. First response wins.
        let endgame_threshold =
            std::cmp::max(meta.chunk_count / ENDGAME_REMAINING_RATIO, 1) as usize;
        if attempt > 0 && remaining.len() <= endgame_threshold {
            let endgame = run_endgame_round(
                &meta.sha256,
                cache,
                peer_agent,
                peer_map,
                &remaining,
                &availability,
                &mut source_counts,
                &mut chunk_details,
            )
            .await?;

            let before = remaining.len();
            remaining.retain(|idx| !endgame.succeeded.contains(idx));

            if remaining.is_empty() {
                break;
            }

            if endgame.succeeded.is_empty() {
                let received = meta.chunk_count - remaining.len() as u32;
                anyhow::bail!(
                    "endgame made no progress: got {}/{} chunks, {} remaining",
                    received,
                    meta.chunk_count,
                    remaining.len()
                );
            }

            info!(
                sha256 = &meta.sha256[..12],
                fetched = before - remaining.len(),
                remaining = remaining.len(),
                "endgame round complete, continuing"
            );
            attempt += 1;
            continue;
        }

        let (tx, mut rx) = mpsc::channel::<ChunkDownloadResult>(MAX_PEER_CONNECTIONS * 2);
        let sem = Arc::new(Semaphore::new(MAX_PEER_CONNECTIONS));

        // Build in-flight tracker from all known peer addresses so the
        // producer can spread concurrent connections across peers.
        let all_addrs: Vec<String> = availability.values()
            .flat_map(|v| v.iter().cloned())
            .collect::<HashSet<_>>()
            .into_iter()
            .collect();
        let inflight = Arc::new(InFlightTracker::new(&all_addrs));

        // Spawn the producer in a separate task so the consumer can drain
        // results concurrently. Without this, the for loop and recv loop
        // are sequential — the channel fills up after 16 results, blocking
        // senders that hold semaphore permits, deadlocking the for loop.
        let spawn_remaining = remaining.clone();
        let mut spawn_availability = availability.clone();
        let spawn_peer_agent = peer_agent.clone();
        let spawn_sha = meta.sha256.clone();
        let spawn_sem = sem.clone();
        let spawn_peer_map = peer_map.clone();
        let spawn_blacklist = blacklisted_peers.clone();
        let spawn_self_addr = self_addr.clone();
        let spawn_manifest = manifest.clone();
        let spawn_inflight = inflight.clone();
        tokio::spawn(async move {
            let mut prev_peer_count = spawn_availability.values().next().map(|v| v.len()).unwrap_or(0);
            for (i, &chunk_idx) in spawn_remaining.iter().enumerate() {
                // Drop the producer as soon as the manifest flips to
                // revoked. `tx` will be dropped at function exit, closing
                // the channel and freeing the consumer to exit its recv
                // loop — the outer retry loop then catches `is_revoked`
                // on the next iteration and bails with REVOKED_ERR.
                if spawn_manifest.is_revoked(&spawn_sha) {
                    return;
                }

                // Refresh peer availability periodically — picks up new peers
                // discovered via multicast while we're downloading.
                if i > 0 && i % refresh_interval == 0 {
                    let fresh_peers = spawn_peer_map.peers_for(&spawn_sha);
                    if fresh_peers.len() != prev_peer_count {
                        info!(
                            sha256 = &spawn_sha[..12],
                            peers = fresh_peers.len(),
                            prev = prev_peer_count,
                            chunk = i,
                            "refreshed peer list mid-download"
                        );
                        prev_peer_count = fresh_peers.len();
                    }
                    for &remaining_chunk in &spawn_remaining[i..] {
                        spawn_availability.insert(
                            remaining_chunk,
                            available_peers_for_chunk(
                                remaining_chunk,
                                &fresh_peers,
                                &spawn_blacklist,
                                spawn_self_addr.as_deref(),
                            ),
                        );
                    }
                }

                // Least-in-flight peer selection: sort candidates by current
                // in-flight count (ascending) so chunks are routed to the
                // least-loaded peer. Tie-break with hostname-seeded shuffle
                // so different envoys spread ties differently.
                let mut peer_addrs = spawn_availability.get(&chunk_idx).cloned().unwrap_or_default();
                if peer_addrs.len() > MAX_CANDIDATES_PER_CHUNK {
                    peer_addrs.truncate(MAX_CANDIDATES_PER_CHUNK);
                }
                if peer_addrs.len() > 1 {
                    peer_addrs.sort_by(|a, b| {
                        let ca = spawn_inflight.get_count(a);
                        let cb = spawn_inflight.get_count(b);
                        ca.cmp(&cb).then_with(|| {
                            chunk_shuffle_key(chunk_idx, seahash::hash(a.as_bytes()))
                                .cmp(&chunk_shuffle_key(chunk_idx, seahash::hash(b.as_bytes())))
                        })
                    });
                }

                let permit = match spawn_sem.clone().acquire_owned().await {
                    Ok(p) => p,
                    Err(_) => return, // semaphore closed
                };
                let tx = tx.clone();
                let sha = spawn_sha.clone();
                let peer_agent = spawn_peer_agent.clone();
                // Acquire in-flight guard for the first-choice peer. The
                // guard decrements on drop (success, failure, or panic).
                let first_peer = peer_addrs.first().cloned().unwrap_or_default();
                let guard = spawn_inflight.acquire(&first_peer);

                tokio::task::spawn_blocking(move || {
                    let _permit = permit;
                    let _inflight_guard = guard;
                    let start = std::time::Instant::now();

                    if let Some(ref agent) = peer_agent {
                        for peer_addr in &peer_addrs {
                            match peer_client::fetch_chunk(agent, peer_addr, &sha, chunk_idx) {
                                Ok(result) => {
                                    let _ = tx.blocking_send(ChunkDownloadResult {
                                        index: chunk_idx,
                                        data: result.data,
                                        new_peers: result.peers,
                                        source: peer_addr.clone(),
                                        duration_ms: start.elapsed().as_millis() as u64,
                                    });
                                    return;
                                }
                                Err(e) => {
                                    warn!(
                                        peer = peer_addr.as_str(),
                                        chunk = chunk_idx,
                                        error = %e,
                                        "peer chunk download failed, trying next"
                                    );
                                }
                            }
                        }
                    }
                });
            }
            // tx is dropped here when all tasks are spawned, closing the channel.
        });

        // Track whether this retry round has produced any on-disk chunk yet.
        // The *first* chunk across the whole download is the announce-worthy
        // moment: it's the transition from "this envoy has nothing" to "this
        // envoy has partials peers can pull from." We fire the announcer so
        // the next multicast cycle carries our partial bitmap within ~100ms
        // instead of up to 5s. Subsequent chunks fall through to the
        // announcer's regular cadence (1s while a download is active).
        let mut first_chunk_this_download = existing.is_empty() && source_counts.is_empty();
        let mut succeeded: HashSet<u32> = HashSet::new();
        while let Some(result) = rx.recv().await {
            // Stop writing chunks to disk the instant a revocation is
            // visible. The producer's per-iteration check also sees this
            // and returns, closing the channel; the outer retry loop
            // catches it on the next iteration and bails with REVOKED_ERR
            // so the top-level caller can wipe the partial.
            if manifest.is_revoked(&meta.sha256) {
                break;
            }
            cache.write_chunk(&meta.sha256, result.index, &result.data)?;
            succeeded.insert(result.index);

            if first_chunk_this_download {
                announce.fire();
                first_chunk_this_download = false;
            }

            *source_counts.entry(result.source.clone()).or_insert(0) += 1;
            chunk_details.push((result.index, result.source.clone(), result.duration_ms));

            // Flush sources incrementally so in-progress downloads show source attribution in traits.
            if chunk_details.len().is_multiple_of(SOURCES_FLUSH_INTERVAL) {
                let elapsed = download_start.elapsed().as_millis() as u64;
                cache.write_chunk_sources(&meta.sha256, &source_counts, &chunk_details, elapsed);
            }

            for peer_addr in &result.new_peers {
                peer_map.report(
                    &meta.sha256,
                    peer_addr,
                    HashSet::new(),
                    PeerSource::PeerExchange,
                );
            }

            debug!(
                chunk = result.index,
                source = result.source.as_str(),
                "chunk received"
            );
        }

        let before = remaining.len();
        remaining.retain(|idx| !succeeded.contains(idx));

        if remaining.is_empty() {
            break;
        }

        if succeeded.is_empty() {
            let received = meta.chunk_count - remaining.len() as u32;
            anyhow::bail!(
                "no progress: got {}/{} chunks, {} remaining with no available peers",
                received,
                meta.chunk_count,
                remaining.len()
            );
        }

        info!(
            sha256 = &meta.sha256[..12],
            fetched = before - remaining.len(),
            remaining = remaining.len(),
            "round complete, continuing"
        );
        attempt += 1;
    }

    let peer_chunks: u32 = source_counts.values().sum();

    info!(
        sha256 = &meta.sha256[..12],
        peer_chunks,
        "all chunks received, assembling"
    );

    let total_ms = download_start.elapsed().as_millis() as u64;
    cache.write_chunk_sources(&meta.sha256, &source_counts, &chunk_details, total_ms);

    cache.assemble(&meta.sha256, meta.size, meta.chunk_count)
}

#[cfg(test)]
mod tests {
    use super::*;
    
    use crate::swarm::peermap::{PeerInfo, PeerSource};
    use std::time::Instant;

    #[tokio::test]
    async fn test_download_and_assemble_bails_on_revoked_manifest() {
        // Regression: when a revocation arrives while a download is in
        // flight, the scheduler must bail with the sentinel error so the
        // top-level `download_from_swarm` can wipe the partial dir. Without
        // this, the `.partial/` directory lingered on disk forever, the
        // swarm_cache trait collector kept reporting it as `downloading`,
        // and the server's per-envoy state stayed stuck at `revoking`.
        let tmp = tempfile::tempdir().unwrap();
        let blobs_dir = tmp.path().join(".blobs");
        std::fs::create_dir_all(&blobs_dir).unwrap();
        let cache = BlobCache::new(blobs_dir).unwrap();

        let manifest = Manifest::load(tmp.path());
        let sha = "a".repeat(64);
        assert!(manifest.revoke(&sha, "server"));
        assert!(manifest.is_revoked(&sha));

        let meta = BlobMeta::new(sha, 1024, "test".to_string());
        let peer_map = Arc::new(PeerMap::new());
        let blacklist = HashSet::new();
        let announce = AnnounceSignal::new();

        let err = download_and_assemble(&meta, &cache, &peer_map, &None, &blacklist, &manifest, &announce)
            .await
            .expect_err("revoked blob must not proceed to download");
        assert!(
            err.to_string().contains(REVOKED_ERR),
            "expected {REVOKED_ERR}, got: {err}"
        );
    }

    fn peer(address: &str, chunks: &[u32]) -> PeerInfo {
        let now = Instant::now();
        PeerInfo {
            address: address.to_string(),
            chunks: chunks.iter().copied().collect(),
            last_seen: now,
            source: PeerSource::Multicast,
            etag: None,
            bitmap_refreshed_at: now,
        }
    }

    #[test]
    fn test_available_peers_excludes_self_blacklist_and_missing_chunk() {
        // Regression: if `192.168.1.2:1531` (the seeder, which is also self
        // when downloading a blob the local node is the seeder for) leaks
        // into the candidate peer list, every chunk request 404s on self
        // before falling through to a real peer. Fixed in 0.20.7.
        let peers = vec![
            peer("192.168.1.2:1531", &[0, 1, 2]),   // self — must be excluded
            peer("192.168.1.74:1531", &[0, 1, 2]),  // good peer with all chunks
            peer("192.168.1.215:1531", &[0]),       // peer with only chunk 0
            peer("192.168.1.99:1531", &[0, 1, 2]),  // blacklisted peer
        ];
        let mut blacklist = HashSet::new();
        blacklist.insert("192.168.1.99:1531".to_string());

        let self_addr = Some("192.168.1.2:1531");

        // Chunk 0: should yield only 192.168.1.74 and 192.168.1.215
        // (self excluded, blacklist excluded).
        let mut got = available_peers_for_chunk(0, &peers, &blacklist, self_addr);
        got.sort();
        assert_eq!(got, vec!["192.168.1.215:1531", "192.168.1.74:1531"]);
        assert!(!got.contains(&"192.168.1.2:1531".to_string()), "self leaked into peer list");
        assert!(!got.contains(&"192.168.1.99:1531".to_string()), "blacklisted peer leaked");

        // Chunk 1: only 192.168.1.74 has it (215 doesn't, 99 blacklisted, 2 is self).
        let got = available_peers_for_chunk(1, &peers, &blacklist, self_addr);
        assert_eq!(got, vec!["192.168.1.74:1531"]);

        // Chunk that no usable peer has.
        let got = available_peers_for_chunk(99, &peers, &blacklist, self_addr);
        assert!(got.is_empty());
    }

    #[test]
    fn test_available_peers_no_self_addr_keeps_all_non_blacklisted() {
        // When self_addr is None (couldn't determine local IP), the filter
        // should still exclude blacklisted peers and chunk-mismatched peers
        // but should NOT silently drop everything.
        let peers = vec![
            peer("192.168.1.2:1531", &[0]),
            peer("192.168.1.74:1531", &[0]),
        ];
        let blacklist = HashSet::new();

        let mut got = available_peers_for_chunk(0, &peers, &blacklist, None);
        got.sort();
        assert_eq!(got, vec!["192.168.1.2:1531", "192.168.1.74:1531"]);
    }

    fn order_with_interleave(
        missing: &[u32],
        availability: &HashMap<u32, Vec<String>>,
        hostname: &str,
    ) -> Vec<u32> {
        let hostname_hash = hash_hostname(hostname);
        interleave_rarity_bands(missing, availability, hostname_hash)
    }

    #[test]
    fn test_interleave_preserves_all_chunks() {
        // Interleaving must be a permutation — no chunks invented, none dropped.
        let mut availability: HashMap<u32, Vec<String>> = HashMap::new();
        availability.insert(0, vec!["a".into(), "b".into(), "c".into()]);
        availability.insert(1, vec!["a".into()]);
        availability.insert(2, Vec::new());

        let ordered = order_with_interleave(&[0, 1, 2], &availability, "host-a");
        let mut sorted = ordered.clone();
        sorted.sort_unstable();
        assert_eq!(sorted, vec![0, 1, 2]);
    }

    #[test]
    fn test_interleave_mixes_rarity_bands() {
        // With 3 distinct rarity bands, the first 3 chunks in the output
        // must come from 3 different bands (one per band per round).
        let mut availability: HashMap<u32, Vec<String>> = HashMap::new();
        // Band 0 (rarest): chunks 10, 11
        availability.insert(10, Vec::new());
        availability.insert(11, Vec::new());
        // Band 1: chunks 20, 21
        availability.insert(20, vec!["a".into()]);
        availability.insert(21, vec!["a".into()]);
        // Band 3: chunks 30, 31
        availability.insert(30, vec!["a".into(), "b".into(), "c".into()]);
        availability.insert(31, vec!["a".into(), "b".into(), "c".into()]);

        let ordered = order_with_interleave(
            &[10, 11, 20, 21, 30, 31],
            &availability,
            "host-a",
        );

        // First 3 chunks must be from 3 different rarity bands.
        let first_three_rarities: HashSet<usize> = ordered[..3]
            .iter()
            .map(|c| availability.get(c).map(|v| v.len()).unwrap_or(0))
            .collect();
        assert_eq!(
            first_three_rarities.len(), 3,
            "first 3 chunks should span 3 rarity bands, got rarities {:?} from chunks {:?}",
            first_three_rarities, &ordered[..3]
        );

        // All 6 chunks present.
        assert_eq!(ordered.len(), 6);
        let mut sorted = ordered.clone();
        sorted.sort_unstable();
        assert_eq!(sorted, vec![10, 11, 20, 21, 30, 31]);
    }

    #[test]
    fn test_interleave_single_band_degrades() {
        // When all chunks have the same rarity (one band), interleaving
        // produces a simple shuffle — same as the old rarest-first behavior.
        let mut availability: HashMap<u32, Vec<String>> = HashMap::new();
        for idx in 0..8u32 {
            availability.insert(idx, vec!["p1".into(), "p2".into()]);
        }
        let missing: Vec<u32> = (0..8).collect();

        let ordered = order_with_interleave(&missing, &availability, "envoy-1.prod");
        assert_eq!(ordered.len(), 8);
        let mut sorted = ordered.clone();
        sorted.sort_unstable();
        assert_eq!(sorted, missing);
    }

    #[test]
    fn test_host_seeded_shuffle_differs_by_hostname() {
        // Two different hostnames must produce two different orderings within
        // the same rarity band, so the fleet spreads load.
        let mut availability: HashMap<u32, Vec<String>> = HashMap::new();
        for idx in 0..16u32 {
            availability.insert(idx, vec!["p1".into(), "p2".into()]);
        }
        let missing: Vec<u32> = (0..16).collect();

        let order_a = order_with_interleave(&missing, &availability, "envoy-1.prod");
        let order_b = order_with_interleave(&missing, &availability, "envoy-2.prod");

        assert_ne!(order_a, order_b, "different hosts must get different orderings");

        // Both must be permutations.
        let mut sorted_a = order_a.clone();
        sorted_a.sort_unstable();
        assert_eq!(sorted_a, missing);
        let mut sorted_b = order_b.clone();
        sorted_b.sort_unstable();
        assert_eq!(sorted_b, missing);

        // Determinism.
        let order_a_again = order_with_interleave(&missing, &availability, "envoy-1.prod");
        assert_eq!(order_a, order_a_again);
    }

    #[test]
    fn test_interleave_uneven_bands() {
        // Band sizes don't need to match. When the rarest band is exhausted,
        // remaining rounds pull from the larger bands only.
        let mut availability: HashMap<u32, Vec<String>> = HashMap::new();
        // Band 1: 1 chunk
        availability.insert(0, vec!["a".into()]);
        // Band 2: 5 chunks
        for idx in 1..6u32 {
            availability.insert(idx, vec!["a".into(), "b".into()]);
        }

        let ordered = order_with_interleave(&[0, 1, 2, 3, 4, 5], &availability, "h");
        assert_eq!(ordered.len(), 6);
        // First chunk should be from the rarer band (rarity 1).
        let first_rarity = availability.get(&ordered[0]).map(|v| v.len()).unwrap_or(0);
        assert_eq!(first_rarity, 1, "first chunk should come from the rarest band");
    }

    /// Mirrors the inline trigger in `download_and_assemble` so changes to
    /// either stay in lockstep. Parameters mirror the live site variables.
    fn endgame_should_fire(attempt: u32, remaining: usize, total_chunks: u32) -> bool {
        let threshold = std::cmp::max(total_chunks / ENDGAME_REMAINING_RATIO, 1) as usize;
        attempt > 0 && remaining <= threshold
    }

    #[test]
    fn test_endgame_trigger_blocks_first_attempt() {
        // Even with only 1 chunk left, endgame doesn't fire on the initial
        // pass — attempt must be > 0.
        assert!(!endgame_should_fire(0, 1, 100));
        assert!(!endgame_should_fire(0, 5, 100));
    }

    #[test]
    fn test_endgame_trigger_requires_tail() {
        // 100 chunks, threshold = max(5,1) = 5. On a retry:
        // 10 remaining → normal retry, 5 remaining → endgame, 1 remaining → endgame.
        assert!(!endgame_should_fire(1, 10, 100));
        assert!(endgame_should_fire(1, 5, 100));
        assert!(endgame_should_fire(1, 1, 100));
        assert!(endgame_should_fire(3, 5, 100));
    }

    #[test]
    fn test_endgame_trigger_small_blob_floor() {
        // For a blob with fewer than 20 chunks, threshold would floor to 0,
        // so max(..,1) keeps endgame reachable at exactly 1 remaining chunk
        // but never for larger tails on a small blob.
        assert!(endgame_should_fire(1, 1, 4));
        assert!(!endgame_should_fire(1, 2, 4));
        // Single-chunk blob: if we're retrying with 1 remaining, endgame
        // should fire rather than falling back to a single-peer retry.
        assert!(endgame_should_fire(1, 1, 1));
    }

    #[test]
    fn test_endgame_trigger_large_blob() {
        // 2000 chunks → threshold = 100. Endgame fires only once we're
        // below that.
        assert!(!endgame_should_fire(1, 200, 2000));
        assert!(!endgame_should_fire(1, 101, 2000));
        assert!(endgame_should_fire(1, 100, 2000));
        assert!(endgame_should_fire(1, 1, 2000));
    }

    #[test]
    fn test_empty_hostname_falls_back_to_stable_order() {
        // Empty hostname → hostname_hash=0 → shuffle becomes a deterministic
        // function of chunk_idx. Still a valid permutation; just shared
        // across every envoy that couldn't resolve its hostname.
        let mut availability: HashMap<u32, Vec<String>> = HashMap::new();
        for idx in 0..4u32 {
            availability.insert(idx, vec!["p1".into(), "p2".into()]);
        }
        let a = order_with_interleave(&[0, 1, 2, 3], &availability, "");
        let b = order_with_interleave(&[0, 1, 2, 3], &availability, "");
        assert_eq!(a, b);
    }

    // -- In-flight tracker tests -------------------------------------------

    #[test]
    fn test_inflight_tracker_increment_decrement() {
        let tracker = InFlightTracker::new(&[
            "peer-a".to_string(),
            "peer-b".to_string(),
        ]);

        assert_eq!(tracker.get_count("peer-a"), 0);
        assert_eq!(tracker.get_count("peer-b"), 0);
        assert_eq!(tracker.get_count("unknown"), 0);

        let guard_a1 = tracker.acquire("peer-a");
        assert_eq!(tracker.get_count("peer-a"), 1);

        let guard_a2 = tracker.acquire("peer-a");
        assert_eq!(tracker.get_count("peer-a"), 2);

        drop(guard_a1);
        assert_eq!(tracker.get_count("peer-a"), 1);

        drop(guard_a2);
        assert_eq!(tracker.get_count("peer-a"), 0);
    }

    #[test]
    fn test_inflight_guard_decrements_on_panic_unwind() {
        let tracker = InFlightTracker::new(&["peer-a".to_string()]);

        let result = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
            let _guard = tracker.acquire("peer-a");
            assert_eq!(tracker.get_count("peer-a"), 1);
            panic!("simulated panic");
        }));

        assert!(result.is_err());
        // Guard must have decremented despite the panic.
        assert_eq!(tracker.get_count("peer-a"), 0);
    }

    #[test]
    fn test_inflight_unknown_peer_returns_zero() {
        let tracker = InFlightTracker::new(&["peer-a".to_string()]);
        // Unknown peers get a fresh counter — acquire works, count starts at 0.
        assert_eq!(tracker.get_count("peer-x"), 0);
        let _guard = tracker.acquire("peer-x");
        // The guard works even for unknown peers (they get a transient counter).
    }

    #[test]
    fn test_interleave_empty_input() {
        let availability: HashMap<u32, Vec<String>> = HashMap::new();
        let result = interleave_rarity_bands(&[], &availability, 0);
        assert!(result.is_empty());
    }
}
