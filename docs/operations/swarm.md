
# Swarm Operations

How to configure, monitor, and troubleshoot peer-to-peer content distribution.

## Enabling Swarm

Add the `swarm:` section to `server.yaml`:

```yaml
swarm:
  enabled: true
  port: 1531                         # agent peer server port (default)
  max_bandwidth_percent: 50          # agent download throttle (default)
```

Restart the server. Agents automatically start their HTTPS peer server on port 1531 when they receive swarm assignments.

## Distributing a File

Distribute in one command:

```bash
# Distribute to all envoys
vigocli swarm distribute agent-0.8.4-linux-amd64.tar.gz --target '*'

# Distribute to web servers only
vigocli swarm distribute app-v2.tar.gz --target '*.web.*' --label app-v2

# Distribute by trait
vigocli swarm distribute firmware.bin --target 'dmi.product_name=PowerEdge R750'

# Custom retention (default is 7 days)
vigocli swarm distribute logs.tar.gz --target '*' --retention 30d
```

The CLI uploads the file to the server's seed directory and writes a `.swarm` manifest. The server's own agent discovers the seed on its next check-in, hashes it, and auto-distributes to matching envoys. Agents download from each other via P2P on port 1531.

## Monitoring Progress

Check per-envoy distribution status:

```bash
vigocli swarm status <sha256>
```

```
Blob: a1b2c3d4e5f6...  (app-v2)

Summary: 8 complete, 2 downloading, 1 pending, 1 skipped

HOSTNAME                 STATUS        CHUNKS     SKIP REASON
web-01.example.com       complete      47/47
web-02.example.com       complete      47/47
web-03.example.com       downloading   31/47
web-04.example.com       pending       0/47
db-01.example.com        skipped       0/47       insufficient disk space
```

List all swarm seeds:

```bash
vigocli swarm list
```

The web UI at `/swarm` shows all active and retired seeds with distribution progress. Detail pages auto-refresh every 5 seconds.

## Envoy-Seeded Distribution

Any envoy can originate a distribution — not just the server. Drop a file and a `.swarm` manifest into the seed directory:

```bash
# On any envoy:
cp firmware.bin /var/lib/vigo/swarm/seed/
cat > /var/lib/vigo/swarm/seed/firmware.bin.swarm <<'EOF'
{
  "target": "*",
  "retention": "7d"
}
EOF
```

The manifest fields:

| Field | Required | Description |
|-------|----------|-------------|
| `target` | Yes | Target spec: `*` (all), hostname glob (`*.web.*`), or trait filter (`os.distro=ubuntu`) |
| `retention` | Yes | How long to keep the blob (e.g. `"7d"`, `"168h"`) |

On the next check-in, the agent hashes the file and reports it to the server. The server auto-distributes to matching envoys, pointing them at the seeding envoy as the first peer.

`vigocli swarm distribute` uses the same mechanism — it places the file in the server's seed directory. The server is just another envoy.

To change retention or target after seeding, edit the `.swarm` file — changes take effect on the next check-in cycle.

## Network Requirements

| Port | Protocol | Direction | Purpose |
|------|----------|-----------|---------|
| 1531 | HTTPS (mTLS) | Agent ↔ Agent | Peer chunk exchange |
| 1531 | UDP | Agent → `224.0.0.42` | Multicast peer discovery (LAN only) |

Multicast discovery only works within a LAN segment. Across subnets, agents discover peers via the server's peer list and peer exchange headers.

If your network blocks multicast, swarm still works — discovery falls back to server-provided peer lists. It just takes one extra check-in cycle for agents to learn about each other.

## Agent Traits

The agent reports swarm-related traits:

| Trait | Description |
|-------|-------------|
| `swarm.blob_count` | Number of cached blobs |
| `swarm.blobs` | SHA256 hashes of cached blobs |
| `swarm.chunk_sources` | Per-blob download source breakdown |
| `swarm.chunk_details` | Per-chunk source and timing |
| `swarm.download_ms` | Per-blob total download time |

These traits are visible in the envoy detail page and can be queried with `vigocli query`.

## Agent Storage

Swarm files are stored under `/var/lib/vigo/swarm/` on Linux (`/Library/Vigo/swarm/` on macOS):

```
/var/lib/vigo/swarm/
├── .blobs/         # Content-addressed storage (SHA256 filenames, hidden)
│   └── 9faa44b9...
├── files/          # Symlinks with original filenames
│   └── firmware.bin → ../.blobs/9faa44b9...
├── seed/           # Drop files here with .swarm manifests to seed distribution
│   ├── firmware.bin
│   └── firmware.bin.swarm
└── retired/        # Expired blobs awaiting hard-delete (auto-cleaned after 7 days)
    └── a1b2c3d4...
```

The `.blobs/` directory holds the actual data, named by SHA256 hash. The `files/` directory contains symlinks using the blob's label (original filename):

```bash
ls -la /var/lib/vigo/swarm/files/
```

If a new blob is distributed with the same label, the symlink is updated to point to the new blob.

## Retention

All swarm blobs have a default retention of **7 days**.

**Receivers:** `vigocli swarm distribute` defaults to `--retention 7d`. The agent records the download completion timestamp and evicts the blob when the retention period expires. Override per distribution with `--retention 30d` or `--retention 0` to keep forever.

**Seeders:** 7 days after distribution started, the server signals the seeder to stop serving. The blob moves from `.blobs/` to `retired/`. The source file in `seed/` is never touched — the operator owns it. Retired files are hard-deleted after another 7 days.

**Re-seeding:** If a seed file remains in `seed/` after retirement, updating its `.swarm` manifest (or the file content) will trigger a fresh distribution on the next check-in.

To manually delete a specific blob:

```bash
vigocli swarm delete <sha256>
```

## Disk Space

The agent checks available disk space before each download and skips blobs that won't fit. Skipped blobs appear in the distribution status with a reason.

## Troubleshooting

### Envoy stuck on "pending"

The envoy hasn't received the assignment. Check:

- Is the envoy online? (`vigocli envoys` — look for last-seen time)
- Is the agent running? (`systemctl status vigo` on the envoy)
- Is swarm enabled on the server? (`swarm.enabled: true` in `server.yaml`)
- If the server was restarted after the distribute command, assignments are lost (in-memory only) — re-run `vigocli swarm distribute`

### Envoy stuck on "downloading"

The envoy started downloading but hasn't completed. Check:

- Network connectivity between peers (port 1531 must be reachable)
- Disk space on the envoy (`df -h`)
- Agent logs for download errors (`journalctl -u vigo`)

### "skipped — insufficient disk space"

The envoy doesn't have enough free disk for the blob. Free up space or increase the disk, then re-distribute.

### Peer downloads timing out

If agents log `timeout: connect` errors when downloading from peers, the peer address may be wrong. The server uses the IP the agent reported at enrollment. If the agent was enrolled from a machine running Docker, it may have captured the Docker bridge IP instead of the real LAN IP.

Check the stored IP:

```bash
vigocli envoys | grep <hostname>   # look at the IP column
```

If the IP is a Docker/virtual interface address, re-enroll the agent. The agent filters out virtual interfaces (docker, br-, veth, virbr) when detecting its outbound IP, and refreshes the IP on every check-in cycle.

### Peer TLS handshake failure

If agents log `certificate does not allow extended key usage for server authentication`, the envoy's enrollment certificate was issued before the CA included ServerAuth EKU. Re-enroll the affected envoys to get new certificates with both ClientAuth and ServerAuth.

### No peers discovered

If all envoys are downloading only from the seeder:

- Check that port 1531 is open between envoys
- If envoys are on different subnets, multicast won't work — this is normal, peers will be exchanged via the server's peer list after one check-in cycle
- Verify mTLS certs are valid on all envoys

## Related

- [Swarm Concepts](../concepts/swarm.md) — How swarm works
- [vigocli swarm](../reference/cli/swarm.md) — CLI reference
- [Server Configuration](../getting-started/server-config.md) — `swarm:` section
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
