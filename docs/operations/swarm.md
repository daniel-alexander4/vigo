
# Swarm Operations

How to configure, monitor, and troubleshoot peer-to-peer content distribution.

## Enabling Swarm

Add the `swarm:` section to `server.yaml`:

```yaml
swarm:
  enabled: true
  blob_dir: "/srv/vigo/blobs"       # default
  port: 1531                         # agent peer server port (default)
  max_bandwidth_percent: 50          # agent download throttle (default)
```

Restart the server. When enabled, the server creates the blob directory and starts the peer tracker. Agents automatically start their HTTPS peer server on port 1531 when they receive swarm assignments.

## Distributing a File

Upload and distribute in one command:

```bash
# Distribute to all envoys
vigocli swarm distribute agent-0.8.4-linux-amd64.tar.gz --target '*'

# Distribute to web servers only
vigocli swarm distribute app-v2.tar.gz --target '*.web.*' --label app-v2

# Distribute by trait
vigocli swarm distribute firmware.bin --target 'dmi.product_name=PowerEdge R750'
```

The CLI uploads the file to the server's blob store, computes the SHA256 hash, and assigns it to matching envoys. Envoys pick up the assignment on their next check-in.

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

List all blobs in the store:

```bash
vigocli swarm list
```

The web UI at `/swarm` provides the same information with auto-refreshing progress.

## Using Blobs in Modules

Reference distributed blobs in file or archive resources:

```yaml
modules:
  app-deploy:
    resources:
      - name: app-binary
        type: file
        path: /opt/app/bin/myapp
        source: "blob:sha256:a1b2c3d4e5f6..."
        mode: "0755"
```

The server validates that the referenced blob exists during config publish. The agent reads the blob from its local cache when applying the resource — no additional download needed if the envoy already has it.

## Network Requirements

| Port | Protocol | Direction | Purpose |
|------|----------|-----------|---------|
| 1531 | HTTPS (mTLS) | Agent ↔ Agent | Peer chunk exchange |
| 1531 | UDP | Agent → `224.0.0.42` | Multicast peer discovery (LAN only) |
| 8443 | HTTPS | Agent → Server | Blob download (fallback) |

Multicast discovery only works within a LAN segment. Across subnets, agents discover peers via the server's peer list and peer exchange headers.

If your network blocks multicast, swarm still works — discovery falls back to server-provided peer lists. It just takes one extra check-in cycle for agents to learn about each other.

## Agent Traits

The agent reports two swarm-related traits:

| Trait | Description |
|-------|-------------|
| `swarm.blob_count` | Number of cached blobs |
| `swarm.blobs` | Comma-separated SHA256 hashes of cached blobs |

These traits are visible in the envoy detail page and can be queried with `vigocli query`.

## Disk Space

Blobs are cached in the agent's data directory (`/var/lib/vigo/swarm/` on Linux). The agent checks available disk space before each download and skips blobs that won't fit. Skipped blobs appear in the distribution status with a reason.

To clean up old blobs, delete them from the server:

```bash
vigocli swarm delete <sha256>
```

Agents will eventually garbage-collect blobs that are no longer referenced by any assigned module.

## Troubleshooting

### Envoy stuck on "pending"

The envoy hasn't checked in since the assignment was created. Check:

- Is the envoy online? (`vigocli envoys` — look for last-seen time)
- Is the agent running? (`systemctl status vigo` on the envoy)
- Is swarm enabled on the server? (`swarm.enabled: true` in `server.yaml`)

### Envoy stuck on "downloading"

The envoy started downloading but hasn't completed. Check:

- Network connectivity between peers (port 1531 must be reachable)
- Disk space on the envoy (`df -h`)
- Agent logs for download errors (`journalctl -u vigo`)

### "skipped — insufficient disk space"

The envoy doesn't have enough free disk for the blob. Free up space or increase the disk, then re-distribute.

### No peers discovered

If all envoys are downloading from the server instead of each other:

- Check that port 1531 is open between envoys
- If envoys are on different subnets, multicast won't work — this is normal, peers will be exchanged via the server's peer list after one check-in cycle
- Verify mTLS certs are valid on all envoys

## Related

- [Swarm Concepts](../concepts/swarm.md) — How swarm works
- [vigocli swarm](../reference/cli/swarm.md) — CLI reference
- [Server Configuration](../getting-started/server-config.md) — `swarm:` section
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
