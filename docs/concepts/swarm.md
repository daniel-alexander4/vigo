
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

1. **Seed:** Admin runs `vigocli swarm distribute <file> --target '*.web.*'`, which places the file in the server's seed directory. Alternatively, drop a file + `.swarm` manifest on any envoy.
2. **Discovery:** The seeder's agent reports the file to the server via the `swarm_seeds` trait
3. **Assignment:** Server auto-distributes to envoys matching the target spec
4. **Check-in:** On next check-in, targeted envoys receive the assignment with SHA256, size, and peer list
5. **Peer discovery:** The agent finds peers through four layers (see below)
6. **Download:** The agent downloads chunks from peers, with retry and progressive seeding
7. **Reporting:** The agent reports completed blobs via the `swarm.blobs` trait
8. **Serving:** Envoys that have the blob serve chunks to peers that still need it
9. **Retirement:** After retention expires (default 7 days), seeders stop serving and receivers evict

Both `vigocli swarm distribute` and the envoy seed directory use the same mechanism. The CLI is a convenient way to place a file in the server's seed directory remotely. The server is just another envoy.

### Four-Layer Peer Discovery

The agent discovers peers through four layers, tried in order:

| Layer | Mechanism | Scope | Latency |
|-------|-----------|-------|---------|
| 1. Multicast | UDP on `224.0.0.42:1531` | LAN segment | < 1s |
| 2. Peer exchange | `X-Peers` header on chunk responses | Transitive | 0 (piggybacks on data) |
| 3. Server peer list | Peers field in check-in response | Fleet-wide | Next check-in |
| 4. Seeder | The originating envoy, always has all chunks | Always available | Immediate |

On a LAN, multicast discovers peers within 2 seconds. The agent waits briefly for multicast responses before falling back to the server's peer list. Even across subnets (where multicast doesn't reach), peer exchange and server-provided peer lists ensure peers find each other.

### Chunk Scheduling

The agent uses a **rarest-first** scheduling strategy: it prioritizes downloading chunks that fewer peers have. This maximizes the diversity of chunks across the swarm, so peers can serve each other more effectively. Up to 8 concurrent peer connections are maintained.

If a peer fails, the scheduler tries the next available peer for that chunk. Failed chunks are retried in subsequent rounds with exponential backoff (capped at 30s). Between rounds, the peer map is refreshed — as other envoys accumulate chunks and announce them via multicast, they become additional sources. Retries continue as long as progress is being made; the download only bails when a full round completes with zero new chunks (all peers down). Partial chunks survive on disk and are resumed on the next check-in cycle.

This enables **progressive seeding**: envoy A gets chunks from the seeder, envoy B pulls from both A and the seeder, envoy C pulls from all three.

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
       │:1531 │             │tracks│
       └──┬───┘             └──┬───┘
          │ P2P                │ assign
     ┌────┼────┐          ┌───┼────┐
     ▼    ▼    ▼          ▼   ▼    ▼
  ┌──────┐ ┌──────┐   ┌──────┐ ┌──────┐
  │Envoy1│◄│Envoy2│   │Envoy3│◄│Envoy4│
  │:1531 │►│:1531 │   │:1531 │►│:1531 │
  └──────┘ └──────┘   └──────┘ └──────┘
    mTLS HTTPS peer connections
```

- **Seeder** has the original file — serves chunks to peers via port 1531
- **Server** tracks peers and assigns targets — is also a seeder when CLI-distributed
- **Agents** listen on port 1531 for peer requests — same mTLS certs used for gRPC
- **Peers** exchange chunks directly — distribution accelerates as more envoys complete

## Manifest — Decentralized Blob Lifecycle

Every envoy maintains a local **manifest** — a LWW-Map CRDT (Last-Writer-Wins Map) that tracks which blobs are active and which are revoked. The manifest propagates between envoys via gossip, enabling fleet-wide revocation without depending on the server.

### How It Works

Each blob has a manifest entry with a status (`active` or `revoked`), a timestamp, and an author. When two envoys merge their manifests, the entry with the higher timestamp wins. If timestamps are equal, revoked wins over active (safety property).

This means revocations are **conflict-free** — no matter what order envoys receive updates, they all converge to the same state.

### Revocation Propagation

When a blob is revoked (via `vigocli swarm delete` or by the seeder), the revocation propagates through three channels:

| Channel | Scope | Speed |
|---------|-------|-------|
| Multicast gossip | LAN segment | 5–10 seconds |
| Peer HTTPS sync | Cross-subnet (on P2P connections) | On next chunk exchange |
| Check-in response | Fleet-wide (cross-subnet fallback) | Next check-in cycle |

On a LAN, multicast announcements include the manifest version. When a peer detects a version mismatch, it fetches the full manifest over mTLS HTTPS and merges it. Revoked blobs are evicted from cache immediately.

For cross-subnet envoys that can't reach each other via multicast, the server includes manifest entries in check-in responses as a fallback gossip channel.

### Authorization

- **Any envoy** can seed a new blob (create an active manifest entry)
- **Only the seeder** or the **server** can revoke a blob
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
