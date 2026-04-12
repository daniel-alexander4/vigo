
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

On a LAN, multicast discovers peers within seconds. Agents also announce which blobs they need (WANT lines), so peers with the blob can respond immediately. Across subnets, active probing queries known peer addresses from the persistent peer registry — peers discovered for previous blobs are candidates for new ones. The server's check-in response provides peer hints as a supplementary channel, but agents do not depend on it.

Peer state persists across agent restarts in `peers.json`, so a restarted agent immediately knows which peers to try without waiting for multicast or a server round-trip.

### Download Jitter

When envoys receive a new swarm assignment, each one delays its download start by a deterministic 0–1 second (hashed from hostname + blob SHA256). This staggers initial requests to the seeder, preventing a thundering herd when many envoys all check in on the same cycle.

### Chunk Scheduling

The agent uses a **rarest-first** scheduling strategy: it prioritizes downloading chunks that fewer peers have. This maximizes the diversity of chunks across the swarm, so peers can serve each other more effectively. Up to 8 concurrent peer connections are maintained.

**Peer rotation:** The peer list for each chunk is rotated by `chunk_index % num_peers`, so different chunks start with different peers. This distributes load across all available sources instead of every chunk hitting the same first peer.

If a peer fails, the scheduler tries the next available peer for that chunk. Failed chunks are retried in subsequent rounds with exponential backoff (capped at 30s). Retries continue as long as progress is being made; the download only bails when a full round completes with zero new chunks (all peers down). Partial chunks survive on disk and are resumed on the next check-in cycle.

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

Before downloading, the agent checks available disk space via `statvfs`. If there isn't enough room for the blob plus a safety margin, the download is skipped and the reason is reported to the server. The server tracks these as "skipped" in the distribution status.

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

When a blob is revoked (via `vigocli swarm revoke`, by the seeder, or by retention expiry), the revocation propagates through these channels:

| Channel | Scope | Speed |
|---------|-------|-------|
| Multicast gossip | LAN segment | 5–10 seconds |
| Peer HTTPS sync | Cross-subnet (on P2P connections) | On next chunk exchange |

On a LAN, multicast announcements include the manifest version. When a peer detects a version mismatch, it fetches the full manifest over mTLS HTTPS and merges it. Revoked blobs are evicted from cache immediately.

Manifest propagation is entirely P2P — the server is not in the gossip path. Cross-subnet distribution requires seeding the blob separately on each subnet.

### Authorization

- Envoys authorized by the seed allowlist (`vigocli swarm allow-seed`) can seed new blobs
- **Only the seeder** or the **server** can revoke a blob
- Seeding is controlled by `swarm.envoy_seeding` in server.yaml (default: disabled) with a `min_envoy_age` gate (default: 30 days)
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

- [Swarm Operations](../operations/swarm.md) — Configuration, monitoring, troubleshooting
- [vigocli swarm](../reference/cli/swarm.md) — CLI reference
- [Server Configuration](../getting-started/server-config.md) — `swarm:` section
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
