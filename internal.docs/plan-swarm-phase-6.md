# Plan: Swarm Phase 6 — download scaling

Status: design approved 2026-04-12. Implementation pending.
Predecessors: 0.20.4 chunk_bitmap fix, 0.20.7 manifest CRDT push + self-filter, 0.20.9 live revoking/revoked status.

## Goal

Make parallel downloads scale with peer count. `N` envoys pulling the same blob should complete in ~1× wall-clock, not ~N×. Today's architecture is effectively client-server against the seeder with weak partial sharing bolted on — the fix is real agent-to-agent chunk trading.

## Non-negotiable constraint

**Swarm is a pure peer-to-peer system.** The server's only swarm duties are:
1. Inject files to be distributed (`vigocli swarm distribute` writes a seed file)
2. Inject revocations (`vigocli swarm revoke` tombstones a manifest entry)

Everything else — peer discovery, partial advertising, chunk trading, bitmap sharing — is agent-to-agent over multicast (224.0.0.42:1531, TTL=1) and mTLS HTTPS on peer port 1531. No server in the distribution path. `PeerTracker` and `ManifestStore` on the server side exist for operational status display (`vigocli swarm status`, `vigocli swarm list`, web UI) and are purely observational — they do not gate or orchestrate any agent behavior.

## Observed problem (from the 2026-04-12 home lab session)

Three envoys downloading the same Win11 ISO from the seeder (`danlap`): the first envoy finishes fastest, the second is slower, the third is slower still. Expected in a scaling P2P: the opposite — later joiners should match or beat the first because they can trade chunks.

Four root causes, diagnosed in order of impact:

1. **Uplink contention on the seeder.** Three envoys split the seeder's upload bandwidth roughly 3 ways. The `bandwidth.rs` monitor then throttles serving when NIC utilization crosses 75%, returning 503 to chunk requests, which feeds the scheduler's exponential backoff (`MAX_BACKOFF_SECS = 30`).

2. **Partial advertising is broken — the scheduler is actively misled.** The multicast listener in `discovery.rs` synthesizes `(0..have).collect()` when a peer announces partial chunks with the `HAVE:<sha>:<have>/0` sentinel. The peer actually has some arbitrary `have`-sized subset, not the first `have` chunks. The scheduler asks peers for chunks they don't have, gets 404s, blacklists them. This is a latent correctness bug, not just a performance bug.

3. **Scheduler chunk ordering is per-downloader, not globally coordinated.** Envoys 2 and 3 hit the seeder for the same chunks in the same order, so even if they could trade, they don't have complementary partials to trade with.

4. **Long-tail problem on every download.** The last few chunks drag on because of unlucky retries. Typical: 95% complete in 80% of wall-clock, last 5% takes 20% on its own.

## Three layered improvements

### A. Agent-to-agent partial bitmap advertising

**Problem statement:** peers need to know *which exact chunks* other peers have, not just "some." The current placeholder is wrong and the sentinel path never gets populated.

**Design — two parallel channels, zero server involvement:**

#### A1. Multicast bitmap inline (LAN fast path)

Extend the multicast announcement line format:

```
HAVE:<sha>:<have>/<total>:<bitmap-base64>   (fits in MTU)
HAVE:<sha>:<have>/<total>                    (oversized → defer to /meta)
```

- `bitmap-base64` is a URL-safe base64-encoded compact bitfield (`-_`, no `=` padding) of the chunk indices the peer currently has.
- URL-safe base64 chosen over hex for density (~33% smaller) without colliding with `:` / `/` in the line format.
- Density: 1000-chunk blob = 125 bytes → ~168 base64 chars. 5000-chunk blob = 625 bytes → ~834 chars. MTU ceiling at ~7400 chunks for a single inline bitmap in a 1400-byte budget.

Announcer (`agent/src/swarm/discovery.rs:build_announcement`):
- For each partial blob, compute the bitfield, base64-encode it, and emit the extended line form.
- Before adding the line, check `msg.len() + line.len() + 30 > MAX_MSG_SIZE`. If it would overflow, fall back to the sentinel form (`HAVE:<sha>:<have>/<total>` without bitmap).

Listener (`agent/src/swarm/discovery.rs:process_announcement`):
- When `bitmap-base64` is present, decode and call `peer_map.report(sha, peer_addr, chunks, PeerSource::Multicast)` with the real chunk set.
- When the sentinel form is received, **DO NOT** synthesize `(0..have)` (this is the current latent bug). Instead, record the peer as "partial, bitmap unknown" and asynchronously call `peer_client::fetch_meta(peer_addr, sha)` on a blocking thread. Update the PeerMap when /meta returns.

#### A2. Peer-server `/meta` ETag + prober refresh (HTTPS reliable path)

Peer server (`agent/src/swarm/server.rs`) — `/blob/<sha>/meta` already returns the current bitmap. Add two response headers:

- `ETag: "<manifest_version>-<partial_chunk_count>"` — cheap to compute, changes when cache state changes.
- `X-Peer-Server-Version: <int>` — bumps on any cache write so the prober can detect staleness without decoding the body.

Prober (`agent/src/swarm/prober.rs`):
- Current role: fills gaps when no peers are known for a blob the local agent wants.
- Extension: while a download is in progress, **every 5 seconds**, re-probe `/meta` for every known peer whose bitmap is older than 5 seconds. Use `If-None-Match: "<stored-etag>"` for conditional GET; on 304 skip, on 200 decode and update PeerMap.
- 5s is a fixed interval for the first cut. Revisit adaptive cadence only if `.sources` profiling shows bitmap staleness hurting endgame effectiveness in the last 10%.

#### A3. Scheduler integration

`scheduler::download_and_assemble` already calls `peer_map.peers_for(sha)` inside the refresh loop at `PEER_REFRESH_INTERVAL = 100` chunks. With A1+A2 landing, that call returns peers with *current* bitmaps rather than stale placeholders, and the `available_peers_for_chunk` helper (added 0.20.7) correctly routes each missing chunk to a peer that actually has it. **No scheduler API changes needed** — it starts making better decisions automatically once the PeerMap is populated correctly.

#### Precedence rule (when sources disagree)

If a multicast bitmap advertises peer X has chunks `{1,2,3}` but a /meta response says `{2,3,4}`, **/meta wins** — it is authoritative and fresher (chunk-write → server response on a single host, no UDP reordering). Multicast bitmaps are "hints" that get corrected on the next /meta probe. Implementation: `PeerMap.report` records the source; later writes from `PeerSource::MetaProbe` overwrite earlier `PeerSource::Multicast` entries for the same `(sha, addr)`.

#### Prerequisite fix — listener placeholder bug

The `(0..have).collect()` synthesis in the sentinel path is a standalone correctness bug that should be fixed as an independent prereq, even if the rest of A slips. Standalone PR: replace the synthesis with a "bitmap pending" marker + async /meta probe. Protects the scheduler from a wrong peer selection immediately and makes the rest of A a pure addition.

### B. Super-seeding via host-seeded chunk ordering

**Problem statement:** when multiple envoys start from a single seeder, they all see the same chunk rarity (all chunks available from the same one peer) and pick the same starting offset. They download identical chunks in identical order. Their partials are redundant, not complementary.

**Design — requester-driven, stateless, no protocol change:**

In `scheduler::download_and_assemble`, the current ordering is:

```rust
// Rarest-first, ties broken by index order (same for every downloader).
ordered.sort_by(|a, b| {
    count_a.cmp(&count_b).then(a.cmp(b))
});
```

Change the tiebreaker to a hostname-seeded hash so different downloaders interleave equal-rarity chunks differently:

```rust
let host_seed = seahash::hash(hostname.as_bytes());
ordered.sort_by(|a, b| {
    count_a.cmp(&count_b)
        .then(chunk_shuffle_key(*a, host_seed).cmp(&chunk_shuffle_key(*b, host_seed)))
});

fn chunk_shuffle_key(idx: u32, host_seed: u64) -> u64 {
    let mut h = seahash::SeaHasher::new();
    h.write_u64(host_seed);
    h.write_u32(idx);
    h.finish()
}
```

Both envoys see the same rarity grouping, but within each rarity tier their order is deterministically shuffled per-host. When the seeder is the only peer (all chunks rarity = 1), envoy-2 and envoy-3 start at different positions in chunk space and accumulate distinct partials.

Combined with A, once envoy-2 has chunks `{0, 42, 97, 153, ...}` and envoy-3 has `{15, 63, 120, 198, ...}`, they discover each other via bitmap advertising and start trading instead of re-downloading from danlap.

**No seeder-side state.** Seeder-driven super-seeding (where the seeder assigns distinct ranges to different requesters) would scale slightly better but requires per-envoy state on the seeder and violates the simplicity principle. The requester-driven approach achieves ~80% of the benefit with ~10% of the complexity.

### C. Endgame mode

**Problem statement:** the last few chunks of every download drag on because each one is attempted against a single peer at a time, and an unlucky 503 or TCP RST causes retry backoff. Typical pattern: 95% in 80% of wall-clock, last 5% takes 20%.

**Design — agent-local, no protocol change:**

In `scheduler::download_and_assemble`, trigger endgame mode when:
- `remaining.len() <= max(total_chunks / 20, 1)` (5% or at least 1), AND
- `attempt > 0` (at least one retry round has happened, so the "easy" chunks are done)

Endgame behavior:
- For each remaining chunk, launch a request to EVERY peer in the availability matrix that has that chunk.
- Use `tokio::select!` to take the first successful response per chunk; drop or ignore the losers.
- Cap total in-flight endgame requests at `MAX_ENDGAME_TASKS = 32` (prevents fanout explosion for very long tails).
- Cap endgame duration at `MAX_ENDGAME_SECS = 15`. If it exceeds that, fall back to normal retry logic — the tail is probably a genuine peer problem, not a slow chunk.

Implementation surface is a new branch in the scheduler retry loop. Keep it behind a clearly-named condition so the normal path is unchanged when endgame doesn't apply.

**Bandwidth monitor interaction:** endgame fanout can push seeders into the upper throttle tier (>75%), causing 503 cascades. Two mitigations to consider:
- Exempt endgame chunks from the throttle gate (serve at full speed even when utilization is high).
- Or: bound endgame task count by seeder count (scale endgame fanout to match expected peer availability).

The first is simpler; recommend that for v1 and revisit if throttle thrashing shows up in profiling.

## Recommended implementation order

1. **A-prereq — listener placeholder fix** (2-4h). Standalone correctness fix. Replace the `(0..have)` synthesis with a "bitmap pending" marker + async /meta probe. Shippable as its own PR.
2. **A1+A2+A3 — full partial bitmap advertising** (~1.5d). Multicast extension, ETag on /meta, prober refresh loop, tests.
3. **B — host-seeded shuffle** (~4h). Smallest change, biggest per-hour payoff once A is in.
4. **C — endgame mode** (~1d). Long-tail polish.

**Total effort:** ~3 days focused work, four independent PRs for easier review.

## Test plan

### Unit tests

- `bitmap::encode` and `bitmap::decode` round-trip, including edge cases (empty, single chunk, all chunks, sparse)
- `discovery::parse_have_line` with bitmap present vs sentinel form
- Listener behavior: sentinel form enqueues a /meta probe; bitmap form updates PeerMap directly
- `/meta` ETag cache hits (304 response path)
- Prober: re-probe fires at configured interval for peers with stale bitmaps; 304 responses don't update PeerMap; 200 responses do
- `chunk_shuffle_key` produces different orderings for different hostnames over the same chunk set
- Endgame trigger threshold: fires at ≤5% remaining AND ≥1 retry
- Endgame cancellation: first responder wins, losers become no-ops in the partial dir
- Endgame cap: `MAX_ENDGAME_TASKS` respected even when remaining count × peer count exceeds it

### Integration (4-node home lab)

Required: baseline measurement BEFORE any Phase 6 code lands so we know what we're beating.

- **Baseline**: re-distribute the Win11 ISO to `-rasp*` target. Record per-envoy `active_download_ms` and the distribution of chunk sources from each envoy's `.blobs/<sha>.sources` file. Expected: each envoy mostly downloads from `danlap`, minimal cross-envoy trading.
- **After A-prereq**: same measurement. `.sources` should show the same seeder dominance but without the latent 404+blacklist noise.
- **After A**: `.sources` diversity — target ≥30% of each envoy's chunks come from non-seeder peers.
- **After A+B**: `active_download_ms` — target later envoys within 1.5× of first envoy (vs current ~3×).
- **After A+B+C**: tail latency — percentage of wall-clock spent on the last 5% of chunks, target <20% (vs current often 40%+).

### Diagnostic helper — `vigocli swarm profile <sha>`

New CLI subcommand, ~4h to build. Dispatches a task to every envoy with a record for the blob; each envoy returns its `.sources` file contents + download timing; the CLI aggregates:

- Per-envoy breakdown: total chunks, bytes-per-source address, active_download_ms, start timestamp
- Aggregate: chunk-source diversity index (Shannon entropy), seeder load share, estimated cross-envoy trade count
- Flag if any envoy has ≥80% of chunks from the seeder (indicates partial trading isn't happening)

Builds on existing `.sources` files + tracker state. Pays for itself the first time we need to verify Phase 6 is actually working in production.

## Risks & tradeoffs

1. **Multicast MTU cliff for very large blobs.** The inline bitmap ceiling is ~7400 chunks at typical MTU. Above that, peers depend on /meta + prober re-fetch. Mitigation: keep /meta fast, keep prober aggressive during active downloads. Worst case (100 GB blob): purely dependent on /meta propagation, but that was the current situation too, so it's not a regression.
2. **Super-seeding breaks ordering determinism.** Debugging becomes harder because "which chunks did envoy-X fetch first" is hostname-dependent. Document clearly in `scheduler.rs` and add a verbose log line (`scheduled rarest-first host_seed=<seed>`).
3. **Endgame fanout amplification.** If triggered at the wrong threshold or with too many peers, wastes bandwidth and can overwhelm peers. Hard caps (`MAX_ENDGAME_TASKS`, `MAX_ENDGAME_SECS`) from day one; log every endgame entry/exit with counts.
4. **Bandwidth monitor + endgame interaction.** Endgame can push seeders into the throttle tier and cause 503 cascades. First solution: exempt endgame requests from the throttle gate. Revisit if profiling shows it hurts.
5. **Cross-subnet deployment.** Pure P2P means agents that can't reach each other via multicast or direct HTTPS cannot trade. This is a deployment invariant, not a plan risk. Vigo's current answer is "per-segment seeders for cross-subnet distribution"; Phase 6 doesn't change that and doesn't need to.

## Out of scope

- **Super-seeding from the seeder side** (seeder-driven range assignment). More scalable but requires per-envoy state on the seeder and is a significant complexity jump. Revisit only if requester-driven shuffle doesn't deliver enough.
- **Changes to the `swarm.downloading` trait format.** The server's observational role stays count-only. If `vigocli swarm status` needs per-chunk telemetry eventually, that's a separate decision about display instrumentation and doesn't block distribution mechanics.
- **Cross-subnet trading.** Requires routed peer discovery or a server-mediated fallback path. Neither is on the table for Phase 6.
- **Piece-range negotiation** (telling a peer "give me chunks 100-200" in one request). Nice in theory, but Vigo's chunks are 1 MiB each and the existing per-chunk HTTPS GET has low enough overhead that batching isn't obviously a win. Revisit only if profiling shows per-chunk RTT is dominating.
