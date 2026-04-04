
# Peer Replication

Vigo supports active-passive replication with automatic failover. One server is the **primary** (accepts writes, pushes config); the others are **peers** (receive synced files, accept read-only check-ins). If the primary goes down, promote a peer to primary and agents fail over automatically.

This is file-level replication, not database replication. The SQLite database, FleetIndex, and run history are not synced -- they rebuild from agent check-ins after promotion.

## Configuration

Add a `peers:` section to `server.yaml` on every server in the replication set:

```yaml
peers:
  role: primary                    # "primary" (default) or "peer"
  servers:
    - addr: "peer1.example.com:1530"
    - addr: "peer2.example.com:1530"
```

Both primary and peers list the same `servers:` entries. The primary uses them to push files; the peers use them for split-brain resolution during promotion.

### Optional fields

```yaml
peers:
  role: primary
  servers:
    - addr: "peer1.example.com:1530"
      sync_interval: "10s"         # push interval (default "10s")
```

## How It Works

### Primary side

1. On startup, the primary starts an `fsnotify` watcher on `stockpile/`, `secrets/`, `tls/`, and `server.yaml`.
2. It opens a gRPC stream (mTLS, same certs as agent traffic) to each peer.
3. On initial connect, it sends a **FullSync** containing every file in the watched directories.
4. On subsequent file changes, it sends **FileChange** messages. Changes are debounced to batch config publishes.

### Peer side

1. The peer accepts the gRPC sync stream from the primary.
2. On **FullSync**: writes all files to disk, triggers config reload.
3. On **FileChange**: writes, updates, or deletes the file. Triggers config reload if the file is under `stockpile/`.
4. The peer also accepts agent check-ins (building its own FleetIndex), so the fleet index is warm if the peer is promoted.

### What is synced

| Directory/File | Synced | Purpose |
|---------------|--------|---------|
| `stockpile/` | Yes | Policy config (modules, roles, envoy mappings) |
| `secrets/` | Yes | Encrypted secret files |
| `tls/` | Yes | CA, server cert, server key |
| `server.yaml` | Yes | Server configuration |

### What is NOT synced

| Data | Why |
|------|-----|

| SQLite database | Rebuilt from agent check-ins after promotion |
| FleetIndex | Rebuilt from check-ins (peer already warm if accepting check-ins) |
| Run history | Lost on failover -- acceptable trade-off |
| Audit log | Lost on failover -- future: replicate audit entries |

## Agent Failover

Agents learn about peer servers during bootstrap. The server populates `fallback_servers` in the `RegisterResponse` from its `peers:` config.

### Agent config

After bootstrap, the agent's config file contains:

```
server = primary.example.com:1530
fallback_servers = peer1.example.com:1530,peer2.example.com:1530
```

### Failover behavior

1. Agent fails to reach the current server (connection refused, timeout, TLS error).
2. After consecutive failures, the agent switches to the next server in the fallback list.
3. Check-ins continue on the fallback server.

### Fail-back

While connected to a fallback, the agent periodically probes the original primary. If the primary responds, the agent switches back.

### No re-enrollment

Failover does not require re-enrollment. The agent uses the same UUID, keypair, and TLS certs. Peers have the same CA cert (synced from primary), so mTLS works without changes.

## Promotion

When the primary is down, promote a peer to become the new primary.

### CLI commands

```bash
# Promote the server you're connected to:
vigocli server promote

# Promote a specific server by address:
vigocli server promote --target peer1.example.com:8443

# Check replication status:
vigocli server status
```

### What promotion does

1. The peer sets its role to `primary`.
2. It starts accepting writes (config publish, enrollment, task dispatch).
3. It starts the fsnotify watcher and pushes file changes to other peers.
4. If the old primary is reachable, it receives a demotion signal and becomes a peer.
5. If the old primary is unreachable, promotion proceeds unilaterally (the disaster scenario).

### Status output

```
$ vigocli server status
  Role:           primary
  Last sync:      2026-03-16T10:30:00Z
  Files synced:   47
  Config version: 12
```

On a peer:

```
$ vigocli server status
  Role:           peer
  Primary:        primary.example.com:1530
  Last sync:      2026-03-16T10:30:15Z
  Files synced:   47
  Config version: 12
```

## Split-Brain Resolution

If a network partition heals and two servers both think they are primary, the server with the most recent configuration wins. The other server is demoted to peer. This ensures the most up-to-date config always takes precedence.

## Integration with Spanner

Peer replication works at any level of the spanner topology:

- **Standalone server**: `peers:` adds redundancy to a single-server deployment.
- **Hub**: `peers:` on the hub gives hub redundancy. Spokes reconnect to the hub's peer on failover.
- **Spoke**: `peers:` on a spoke gives spoke redundancy. Envoys assigned to that spoke fail over to the spoke's peer.

### Spoke-to-hub failover

If the hub has peers, spokes should list the hub's peer addresses as fallbacks:

```yaml
# Spoke server.yaml
spanner:
  role: spoke
  spoke_id: "dc1"
  hub_addr: "hub.example.com:1530"
  hub_fallback_addrs:
    - "hub-standby.example.com:1530"
```

A promoted hub-peer inherits the spoke connections from the synced `server.yaml`.

### Example: Full HA topology

```
                    +----------------------------+
                    |     Hub (primary)          |
                    |   hub.example.com:1530     |
                    +-------------+--------------+
                                  | peer sync
                    +-------------v--------------+
                    |    Hub (standby peer)      |
                    | hub-standby.example.com    |
                    +----------------------------+
                                  |
              +-------------------+-------------------+
              |                   |                   |
   +----------v---------+ +------v----------+ +------v----------+
   |  Spoke West        | |  Spoke East     | |  Spoke EU       |
   |  (primary)         | |  (primary)      | |  (primary)      |
   +----------+---------+ +--------+--------+ +--------+--------+
              | peer sync          | peer sync          | peer sync
   +----------v---------+ +-------v---------+ +-------v---------+
   |  Spoke West        | |  Spoke East     | |  Spoke EU       |
   |  (standby peer)    | |  (standby peer) | |  (standby peer) |
   +--------------------+ +-----------------+ +-----------------+
```

Each level (hub, spoke) has its own primary + peer pair. Agent failover is within a spoke pair. Spoke-to-hub failover uses `hub_fallback_addrs`. No single point of failure at any level.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
