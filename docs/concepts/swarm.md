
# Swarm — Peer-to-Peer Content Distribution

Swarm distributes large files across your fleet using peer-to-peer transfer over mTLS HTTPS. Instead of every envoy downloading from the server, envoys share chunks with each other — reducing server bandwidth and speeding up rollouts.

## When to Use Swarm

Swarm is designed for distributing large binary artifacts: agent updates, firmware images, application tarballs, disk images, database dumps. For small config files (a few KB), the normal check-in bundle is more efficient.

**Good candidates:** 50 MB+ files going to 10+ envoys — agent binaries, container images, OS images, application releases.

**Not needed:** Config files, scripts, templates — these are already delivered in the policy bundle.

## How It Works

### Content Addressing

Every blob is identified by its SHA256 hash. The server stores the original file, splits it into 1 MiB chunks, and tracks which envoys have which chunks. There is no filename — only the hash matters.

### Distribution Flow

1. **Upload:** Admin uploads a file via `vigocli swarm distribute <file> --target '*.web.*'`
2. **Assignment:** Server records which envoys should receive the blob
3. **Check-in:** On next check-in, targeted envoys receive the blob assignment with the SHA256 hash, size, and peer list
4. **Discovery:** The agent finds peers through four layers (see below)
5. **Download:** The agent downloads chunks from peers (preferred) or the server (fallback)
6. **Reporting:** The agent reports completed blobs via the `swarm.blobs` trait
7. **Serving:** Envoys that have the blob serve chunks to peers that still need it

### Four-Layer Peer Discovery

The agent discovers peers through four layers, tried in order:

| Layer | Mechanism | Scope | Latency |
|-------|-----------|-------|---------|
| 1. Multicast | UDP on `224.0.0.42:1531` | LAN segment | < 1s |
| 2. Peer exchange | `X-Peers` header on chunk responses | Transitive | 0 (piggybacks on data) |
| 3. Server peer list | Peers field in check-in response | Fleet-wide | Next check-in |
| 4. Server fallback | Direct HTTP download from server | Always available | Immediate |

On a LAN, multicast discovers peers within 2 seconds. The agent waits briefly for multicast responses before falling back to the server's peer list. Even across subnets (where multicast doesn't reach), peer exchange and server-provided peer lists ensure peers find each other.

### Chunk Scheduling

The agent uses a **rarest-first** scheduling strategy: it prioritizes downloading chunks that fewer peers have. This maximizes the diversity of chunks across the swarm, so peers can serve each other more effectively. Up to 8 concurrent peer connections are maintained.

If a peer fails or is too slow, the scheduler falls back to the next available peer, and ultimately to the server.

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

## Module Integration

File and archive resources can reference swarm blobs using the `blob:` source prefix:

```yaml
resources:
  - name: app-binary
    type: file
    path: /opt/app/bin/myapp
    source: "blob:sha256:a1b2c3d4..."
    mode: "0755"

  - name: app-release
    type: archive
    path: /opt/app/
    source: "blob:sha256:e5f6g7h8..."
```

When the server builds the policy bundle, it verifies the referenced blob exists in the blob store and includes the swarm assignment in the check-in response. The agent reads the blob from its local cache when applying the resource.

## Architecture

```
            ┌─────────────┐
            │  Vigo Server │
            │  Blob Store  │
            └──────┬───────┘
                   │ HTTP (fallback)
          ┌────────┼────────┐
          ▼        ▼        ▼
       ┌──────┐ ┌──────┐ ┌──────┐
       │Envoy1│◄│Envoy2│◄│Envoy3│
       │:1531 │►│:1531 │►│:1531 │
       └──────┘ └──────┘ └──────┘
         mTLS HTTPS peer connections
```

- **Server** stores blobs and tracks peers — never initiates connections to agents
- **Agents** listen on port 1531 for peer requests — same mTLS certs used for gRPC
- **Peers** exchange chunks directly — the server only provides the initial seed

## Security

All peer-to-peer traffic uses the same mTLS certificates as gRPC communication. The agent's HTTPS peer server on port 1531 verifies client certificates against the Vigo CA. No unauthenticated connections are accepted.

Blob integrity is verified by SHA256 hash after download — if the hash doesn't match, the blob is discarded and re-downloaded.

## Related

- [Swarm Operations](../operations/swarm.md) — Configuration, monitoring, troubleshooting
- [vigocli swarm](../reference/cli/swarm.md) — CLI reference
- [Server Configuration](../getting-started/server-config.md) — `swarm:` section
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
