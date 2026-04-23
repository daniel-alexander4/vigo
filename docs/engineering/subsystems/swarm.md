
# Swarm — Peer-to-Peer Content Distribution

Swarm distributes large files across your fleet using peer-to-peer transfer over mTLS HTTPS. Instead of every envoy downloading from a central server, envoys share chunks with each other — reducing bandwidth and speeding up rollouts.

## When to Use Swarm

Swarm is designed for distributing large binary artifacts: agent updates, firmware images, application tarballs, disk images, database dumps. For small config files (a few KB), the normal check-in bundle is more efficient.

**Good candidates:** 50 MB+ files going to 10+ envoys — agent binaries, container images, OS images, application releases.

**Not needed:** Config files, scripts, templates — these are already delivered in the policy bundle.

## How It Works

### Content Addressing

Every blob is identified by its SHA256 hash. Blobs are split into 1 MiB chunks. There is no filename at the transport layer — only the hash matters.

### Distribution Flow

1. **Seed:** Admin runs `vigocli swarm distribute <file> --target '*.web.*'`, which streams the file to the server's seed directory (hashed in a single pass). Alternatively, drop a file + `.swarm` manifest on any envoy.
2. **Registration:** The server registers the seed in the tracker and manifest CRDT immediately on upload, then force-pushes streaming agents. For envoy-seeded blobs, the seeder's agent reports the file via the `swarm_seeds` trait on the next check-in.
3. **Manifest:** The manifest entry is gossipped to all envoys via check-in responses
4. **Self-assignment:** Each agent evaluates the manifest's target specs against its own hostname and traits. If it matches, it queues the blob for download — no server assignment needed.
5. **Peer discovery:** The agent finds peers through four layers (see below)
6. **Download:** The SwarmManager coordinates downloads with bounded concurrency and deduplication
7. **Reporting:** The agent reports completed blobs via the `swarm.blobs` trait
8. **Serving:** Envoys that have the blob serve chunks to peers that still need it
9. **Retirement:** After retention expires (default 7 days), seeders stop serving and receivers evict

Both `vigocli swarm distribute` and the envoy seed directory use the same mechanism. The CLI is a convenient way to place a file in the server's seed directory remotely. The server is just another envoy.

### Agent-Driven Peer Discovery

The agent discovers peers through four layers, tried in order:

| Layer | Mechanism | Scope | Latency |
|-------|-----------|-------|---------|
| 1. Server hints | Peer addresses from check-in response | Fleet-wide | On check-in |
| 2. Multicast | UDP on `224.0.0.42:1531`, including WANT solicitation | LAN segment | < 5s |
| 3. Peer exchange | `X-Peers` header on chunk responses | Transitive | 0 (piggybacks on data) |
| 4. Active probing | `GET /blob/{sha}/meta` to known peer addresses | Fleet-wide | < 2s |
| 5. Refresh loop | Conditional `/meta` probes every 5s, `If-None-Match` against the peer's last `ETag` | Known peers | 5s |

On a LAN, multicast discovers peers within seconds. Agents also announce which blobs they need (WANT lines), so peers with the blob can respond immediately. Multicast `HAVE` lines for partial blobs carry an inline base64 bitmap of which chunks the peer has, so listeners can populate their peer map without a follow-up probe. Across subnets, active probing queries known peer addresses from the persistent peer registry — peers discovered for previous blobs are candidates for new ones. The server's check-in response provides peer hints as a supplementary channel, but agents do not depend on it.

While downloads are in flight, a background refresh loop re-probes known peers every 5 seconds using `If-None-Match` against the peer's last `ETag`. Unchanged inventories cost a single 304 with an empty body; changed ones return the new bitmap and overwrite the stored one (authoritative replace semantics — a dropped chunk no longer lingers in the scheduler's view).

Peer state persists across agent restarts in `peers.json`, so a restarted agent immediately knows which peers to try without waiting for multicast or a server round-trip.

### Download Jitter

When envoys receive a new swarm assignment, each one delays its download start by a deterministic 0–1 second (hashed from hostname + blob SHA256). This staggers initial requests to the seeder, preventing a thundering herd when many envoys all check in on the same cycle.

### Chunk Scheduling

The agent uses a **rarest-first** scheduling strategy: it prioritizes downloading chunks that fewer peers have. This maximizes the diversity of chunks across the swarm, so peers can serve each other more effectively. Up to 8 concurrent peer connections are maintained.

**Peer rotation:** The peer list for each chunk is rotated by `chunk_index % num_peers`, so different chunks start with different peers. This distributes load across all available sources instead of every chunk hitting the same first peer.

**Host-seeded tiebreaker:** Within a rarity group (chunks that have the same number of peers), the sort order is broken by a deterministic seahash of `(hostname, chunk_index)` so different envoys attack the same group in different orders. Without this, every envoy with the same availability matrix would pile onto the peer that happens to serve chunk 0 on every tick.

If a peer fails, the scheduler tries the next available peer for that chunk. Failed chunks are retried in subsequent rounds with exponential backoff (capped at 30s). Retries continue as long as progress is being made; the download only bails when a full round completes with zero new chunks (all peers down). Partial chunks survive on disk and are resumed on the next check-in cycle.

**Endgame mode:** Once the retry loop is on its second or later attempt and only the tail of the blob remains (fewer than `max(chunk_count/20, 1)` chunks), the scheduler switches to endgame: every remaining chunk is requested from every peer that has it in parallel, up to a fleet-wide cap of 32 concurrent fetches, and the first response per chunk wins while duplicates are dropped on arrival. This kills the long tail where one slow peer holding chunk N stalls the whole blob. Endgame has a hard 15-second deadline per round — anything still outstanding falls back to the normal retry path.

**Pre-download peer discovery:** When only the seeder is known (the common cold-start case), the orchestrator waits 2 seconds for multicast HAVE announcements, then actively probes all known peer addresses for the target blob. This discovers peers that already have chunks from a previous download cycle before the scheduler commits to its chunk ordering. Cost: ~2s + one /meta probe per known peer.

**Mid-download peer refresh:** Every 100 chunks (~10 seconds), the scheduler re-reads the peer map and updates availability for remaining chunks. This picks up new peers discovered via multicast during the download, enabling progressive seeding even within a single round.

This enables **progressive seeding**: envoy A gets chunks from the seeder, envoy B starts from the seeder but discovers A mid-download and pulls from both, envoy C finds all three and distributes load across them.

### Adaptive Bandwidth

Agents monitor host network utilization by reading interface statistics from sysfs. When the host is busy, swarm throttles its download rate to avoid interfering with production traffic. Four tiers:

| Host utilization | Swarm behavior |
|-----------------|----------------|
| < 25% | Full speed |
| 25–50% | 75% throttle |
| 50–75% | 50% throttle |
| > 75% | 25% throttle (trickle) |

The `max_bandwidth_percent` setting in `server.yaml` sets the upper bound.

### Disk Space Guardrails

Before downloading, the agent runs `BlobCache::check_disk_space` which enforces two gates — either one aborts the download:

1. **Absolute reserve** — `available_bytes >= blob_size + 512 MiB` (the historical check; protects against accepting a blob that would fully consume the disk).
2. **Pressure threshold** — `free_pct > 10` on the blobs mountpoint (`/var/lib/vigo/swarm/.blobs`). Swarm is pull-based, so the fleet-wide "sender doesn't push to ≤10%-free recipients" guardrail lands here as "receiver refuses to start a download when its own disk is already pressured."

Both checks go through the shared [`swarm::peer_health`](../../../agent/src/swarm/peer_health.rs) module so the gate matches the one answered by `GET /health/storage?for=swarm` on the same mount. Skipped downloads are reported to the server as "skipped" with the triggering reason. See [Disk Space in swarm operations](../../operations/swarm.md#disk-space) for how the same guardrail shows up in the other three peer-delivery paths.

## Architecture

```
       ┌──────┐    seed     ┌──────┐
       │Seeder│────────────►│Server│
       │:1531 │             │gossip│
       └──┬───┘             └──┬───┘
          │ P2P                │ manifest
     ┌────┼────┐          ┌───┼────┐
     ▼    ▼    ▼          ▼   ▼    ▼
  ┌──────┐ ┌──────┐   ┌──────┐ ┌──────┐
  │Envoy1│◄│Envoy2│   │Envoy3│◄│Envoy4│
  │:1531 │►│:1531 │   │:1531 │►│:1531 │
  └──────┘ └──────┘   └──────┘ └──────┘
    mTLS HTTPS peer connections
```

- **Seeder** has the original file — serves chunks to peers via port 1531
- **Server** gossips manifest entries and provides peer hints — does NOT assign per-envoy
- **Agents** self-assign by evaluating manifest target specs, discover peers autonomously, and coordinate downloads via the SwarmManager
- **Peers** exchange chunks directly — distribution accelerates as more envoys complete

## Manifest — Decentralized Blob Lifecycle

Every envoy maintains a local **manifest** — a LWW-Map CRDT (Last-Writer-Wins Map) that tracks which blobs are active and which are revoked. The manifest propagates between envoys via gossip, enabling fleet-wide revocation without depending on the server.

### How It Works

Each blob has a manifest entry with a status (`active` or `revoked`), a timestamp, and an author. When two envoys merge their manifests, the entry with the higher timestamp wins. If timestamps are equal, revoked wins over active (safety property).

This means revocations are **conflict-free** — no matter what order envoys receive updates, they all converge to the same state.

### Revocation Propagation

When a blob is revoked (via `vigocli swarm revoke`, by the seeder, or by retention expiry), the revocation propagates through three channels:

| Channel | Scope | Speed |
|---------|-------|-------|
| Multicast gossip | LAN segment | 5–10 seconds |
| Peer HTTPS sync | Cross-subnet (on P2P connections) | On next chunk exchange |
| Server check-in push | Anywhere agents reach the server | On next check-in |

On a LAN, multicast announcements include the manifest version. When a peer detects a version mismatch, it fetches the full manifest over mTLS HTTPS and merges it. Revoked blobs are evicted from cache immediately and their peer map entries are flushed, so re-seeding the same SHA starts with a clean slate instead of trusting stale peer claims.

The server also ships its manifest CRDT with every check-in response. This is the bridge for operator-initiated revocations: `vigocli swarm revoke` only updates the server's in-memory store, so without the server-side push, agents on isolated subnets (no shared multicast group, no direct peer reach) would never learn. The push complements multicast — it's redundant on the LAN, essential for any envoy that can reach the server but not its peers.

### Authorization

- Envoys whose hostname matches `swarm.seeding_envoys` in `server.yaml` can originate seeds. Globs (`filepath.Match`) supported.
- The server's own envoy (hostname = `server.hostname`) is implicitly allowed — the list doesn't need to include it.
- **Only the seeder** or the **server** can revoke a blob
- Non-allowlisted seeds are moved to `seed/admin_rejected/` on the envoy with a reason string
- A compromised envoy cannot revoke another envoy's blobs

### Manifest Endpoints

Each envoy's peer server (port 1531) exposes:

- `GET /manifest/version` — current manifest version (Lamport clock)
- `GET /manifest` — full manifest as JSON

These are used by the gossip protocol for efficient sync — peers compare versions before transferring the full manifest.

## Security

All peer-to-peer traffic uses the same mTLS certificates as gRPC communication. The agent's HTTPS peer server on port 1531 verifies client certificates against the Vigo CA. No unauthenticated connections are accepted.

Blob integrity is verified by SHA256 hash after download — if the hash doesn't match, the blob is discarded and re-downloaded.

## Related

- [Swarm Operations](../../operations/swarm.md) — Configuration, monitoring, troubleshooting
- [vigocli swarm](../../reference/cli/swarm.md) — CLI reference
- [Server Configuration](../../getting-started/server-config.md) — `swarm:` section
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
