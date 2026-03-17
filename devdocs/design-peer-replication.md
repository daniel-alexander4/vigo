# Design: Peer Replication

## Overview

Peer replication provides redundancy within a single server role (standalone, hub, or spoke). One server is primary for writes; peers receive synced config/secrets/TLS and can be promoted to primary on failure. Agents fail over to peers automatically.

This complements federation (spanner), which shards the fleet across geography or organization. Peer replication protects against individual server failure. Together they eliminate all single points of failure.

```
Hub ──── Hub-standby
 ├── Spoke-west ──── Spoke-west-standby
 ├── Spoke-east ──── Spoke-east-standby
 └── Spoke-eu   ──── Spoke-eu-standby
```

## Design Principles

1. **No SQLite replication.** Agents are the source of truth. A promoted peer rebuilds fleet state from agent check-ins within one check-in cycle (15-30 seconds). Run history is lost on failover, but compliance state rebuilds immediately.
2. **Config files are the replication unit.** `hnb/`, `secrets/`, `tls/`, and `server.yaml` are synced. These change infrequently and are small.
3. **One primary, N peers.** The primary accepts writes (config publish, enrollment, task dispatch). Peers are read-only (serve dashboard, accept check-ins, forward writes to primary). Promotion is explicit.
4. **Same config block everywhere.** The `peers:` section works identically on standalone servers, hubs, and spokes.
5. **Agents handle failover.** Agents get a fallback server list during bootstrap and try them in order.

## Configuration

### Server (server.yaml)

```yaml
peers:
  - addr: "standby1.example.com:1530"    # gRPC address
  - addr: "standby2.example.com:1530"
```

Optional fields per peer:

```yaml
peers:
  - addr: "standby1.example.com:1530"
    sync_interval: "10s"                  # how often to push changes (default "10s")
```

Top-level peer settings:

```yaml
peers:
  - addr: "standby1.example.com:1530"
peer_role: "primary"                      # "primary" or "peer" (default "primary" on first start)
```

When `peer_role: peer`, the server:
- Accepts check-ins and serves the dashboard (read-only FleetIndex)
- Forwards config publish, enrollment, and task dispatch to the primary
- Receives synced files from the primary

### Agent (config file)

```
server = spoke-west.example.com:1530
fallback_servers = spoke-west-standby.example.com:1530,spoke-west-standby2.example.com:1530
```

Populated automatically during bootstrap from the server's peer list. Agents try servers in order: primary first, then fallbacks. Fail back to primary when it returns.

## Architecture

### Components

```
┌─────────────┐         gRPC sync stream         ┌─────────────┐
│   Primary    │ ──────────────────────────────── │    Peer      │
│              │   hnb/, secrets/, tls/,          │              │
│  Accepts     │   server.yaml changes            │  Read-only   │
│  writes      │                                  │  dashboard   │
│              │                                  │              │
│  FleetIndex  │                                  │  FleetIndex  │
│  (full)      │                                  │  (from       │
│              │                                  │   check-ins) │
└──────┬───────┘                                  └──────┬───────┘
       │                                                 │
       │  gRPC check-in                                  │  gRPC check-in
       │                                                 │  (failover)
       │                                                 │
    Agents ──────────────────────────────────────── Agents
```

### New gRPC Service: PeerSync

Add to `proto/hobnob.proto`:

```protobuf
service HobnobPeer {
  // SyncStream is a bidirectional stream between primary and peer.
  // Primary pushes file changes; peer acknowledges.
  rpc SyncStream(stream PeerSyncMessage) returns (stream PeerSyncMessage);

  // Promote requests the peer to become primary.
  rpc Promote(PromoteRequest) returns (PromoteResponse);

  // Status returns the peer's current role and sync state.
  rpc Status(PeerStatusRequest) returns (PeerStatusResponse);
}

message PeerSyncMessage {
  oneof payload {
    FileChange file_change = 1;       // file created/modified/deleted
    SyncAck ack = 2;                  // acknowledgement
    FullSync full_sync = 3;           // initial full sync on connect
  }
}

message FileChange {
  string path = 1;                    // relative to /srv/hobnob (e.g., "hnb/proposed/modules/nginx.hnb")
  bytes content = 2;                  // file contents (empty = deletion)
  int64 mtime = 3;                    // modification time (unix nanos)
  uint32 mode = 4;                    // file permissions
}

message SyncAck {
  string path = 1;
  bool success = 2;
  string error = 3;
}

message FullSync {
  repeated FileChange files = 1;      // all files in hnb/, secrets/, tls/, server.yaml
}

message PromoteRequest {}
message PromoteResponse {
  bool success = 1;
  string message = 2;
}

message PeerStatusRequest {}
message PeerStatusResponse {
  string role = 1;                    // "primary" or "peer"
  string primary_addr = 2;           // address of current primary (if peer)
  int64 last_sync = 3;               // unix timestamp of last successful sync
  int64 files_synced = 4;            // total files in sync
}
```

### File Sync Mechanism

**Primary side (`server/peer/primary.go`):**

1. On startup, if `peers:` is configured and `peer_role: primary`:
   - Start fsnotify watcher on `hnb/`, `secrets/`, `tls/`, `server.yaml`
   - Connect to each peer via gRPC (using existing mTLS infrastructure)
   - Send `FullSync` with all current files
   - On file change: send `FileChange` to all connected peers

2. Debounce: batch changes within a 1-second window before sending (config publish touches many files at once).

3. Sync is unidirectional: primary → peer. Peers never push files to the primary.

**Peer side (`server/peer/peer.go`):**

1. On startup, if `peer_role: peer`:
   - Accept `SyncStream` connections from the primary
   - On `FullSync`: write all files to local `/srv/hobnob`, trigger config reload
   - On `FileChange`: write/delete the file, trigger config reload if in `hnb/`
   - Set file permissions and mtime to match primary

2. Forward writes to primary:
   - Config publish: return error "this is a read-only peer; publish on the primary"
   - Enrollment: forward `Register` RPC to primary, return result to agent
   - Task dispatch: forward to primary

### Agent Failover

**Agent side (`agent-rs/src/main.rs`, `agent-rs/src/checkin/client.rs`):**

1. Parse `fallback_servers` from config file (comma-separated list).

2. On check-in failure (connection refused, timeout, TLS error):
   - Increment failure counter for current server
   - After 3 consecutive failures: switch to next server in the list
   - Log the failover: "primary unreachable after 3 attempts, failing over to standby1.example.com:1530"

3. On successful check-in after failover:
   - Continue using the current server
   - Periodically probe the original primary (every 5 minutes)
   - If primary responds: fail back, log "primary recovered, reconnecting to primary.example.com:1530"

4. Bootstrap populates `fallback_servers`:
   - Server includes peer addresses in `RegisterResponse` (new field: `fallback_servers`)
   - Bootstrap script writes them to agent config

**No re-enrollment on failover.** The agent uses the same UUID, keypair, and TLS certs. The peer has the same CA cert (synced from primary), so mTLS works. The peer's FleetIndex won't have the thrall's record initially, but the check-in interceptor can fall back to the synced SQLite database (or the agent can re-register with `--force` if needed).

### Promotion

**CLI command:**

```bash
# On the peer server, or remotely:
hobnobcli server promote --target standby1.example.com:1530
```

**What promotion does:**

1. Peer sets `peer_role: primary` in its local `server.yaml`
2. Peer starts accepting writes (config publish, enrollment, task dispatch)
3. Peer starts fsnotify watcher and pushes to other peers
4. If old primary is reachable: sends demotion signal, old primary becomes peer
5. If old primary is unreachable: peer promotes unilaterally (this is the disaster scenario)

**Split-brain prevention:**

- Only one server should be primary at a time
- If both think they're primary (network partition heals), the one with the higher config version wins
- The loser demotes to peer and syncs from the winner
- Config version is a monotonic counter — higher version = more recent publish

### What's NOT Replicated

| Data | Replicated? | Why |
|------|-------------|-----|
| `hnb/` (config files) | Yes | Policy source of truth |
| `secrets/` | Yes | Needed for config resolution |
| `tls/` | Yes | Needed for mTLS (same CA) |
| `server.yaml` | Yes | Server configuration |
| `db/hobnob.db` | **No** | Rebuilt from agent check-ins on promotion |
| FleetIndex | **No** | Rebuilt from check-ins (or from synced DB on startup) |
| Run history | **No** | Lost on failover — acceptable trade-off |
| Audit log | **No** | Lost on failover — future: replicate audit entries |

### Recovery Timeline After Failover

| Event | Time |
|-------|------|
| Primary dies | T+0 |
| Agent detects failure (3 retries) | T+3-15s (depends on check-in interval) |
| Agent fails over to peer | T+15s |
| First check-in on peer succeeds | T+15-30s |
| FleetIndex begins rebuilding | T+15s (first check-in) |
| Full fleet visible on peer dashboard | T+30s to T+2min (one full check-in cycle) |
| Compliance counters accurate | T+1-2min |

At 15-second check-in intervals, the fleet is fully visible on the new primary within ~2 minutes. At 30-second intervals, ~3-4 minutes.

## Integration with Spanner

Peer replication works at any level of the spanner topology:

- **Standalone server**: `peers:` adds redundancy to a single-server deployment
- **Hub**: `peers:` on the hub gives hub redundancy. Spokes reconnect to the new hub.
- **Spoke**: `peers:` on a spoke gives spoke redundancy. Thralls assigned to that spoke fail over to the spoke's peer.

Spanner config (`spanner:` section) is part of `server.yaml` and is synced to peers automatically. A promoted hub-peer inherits the spoke connections.

**Spoke reconnection after hub failover:**
- Spokes connect to the hub via gRPC
- If hub dies, spokes retry with backoff (same as agents)
- Spoke config has `hub_addr` — if the hub has peers, the spoke should also have `hub_fallback_addrs` to try the hub's peers

```yaml
# Spoke server.yaml
spanner:
  role: spoke
  hub_addr: "hub.example.com:1530"
  hub_fallback_addrs:
    - "hub-standby.example.com:1530"
```

## Implementation Plan

### Phase 1: File Sync (1 week)
- Proto messages for PeerSync service
- `server/peer/` package: primary watcher + peer receiver
- fsnotify watcher on `hnb/`, `secrets/`, `tls/`, `server.yaml`
- FullSync on initial connection, FileChange on updates
- `peers:` config section in `server/config/types.go`
- Wire into server startup in `server/main.go`
- Tests: sync correctness, reconnection, debounce

### Phase 2: Agent Failover (3 days)
- `fallback_servers` in agent config
- Failure counter + failover logic in `checkin/client.rs`
- Periodic primary probe + fail-back
- `fallback_servers` field in `RegisterResponse` proto
- Server populates from `peers:` config during registration
- Bootstrap script writes `fallback_servers` to agent config
- Tests: failover, fail-back, multiple fallbacks

### Phase 3: Promotion (3 days)
- `Promote` and `Status` RPCs in PeerSync service
- `hobnobcli server promote` CLI command
- `hobnobcli server status` CLI command (shows role, sync state, peers)
- Peer-to-primary role switch logic
- Split-brain resolution (higher config version wins)
- Tests: promotion, demotion, split-brain resolution

### Phase 4: Spanner Integration (2 days)
- `hub_fallback_addrs` in spoke config
- Spoke failover to hub peer
- Hub promotion inherits spoke connections
- Tests: hub failover with spokes

### Phase 5: Documentation and UI (2 days)
- `docs/operations/peer-replication.md`
- `docs/concepts/peer-replication.md`
- Web UI: peer status on admin dashboard (role, sync state, last sync time)
- server.yaml.example update

**Total: ~3 weeks**

## Open Questions

1. **Should the peer serve a read-only dashboard?** It has synced config but no FleetIndex data until agents check in. The dashboard would show config/modules but no thrall status. Could show "this is a standby server — no fleet data until promoted" banner.

2. **Should audit log entries be synced?** Currently not replicated. If tamper-evident audit trail is built first (pending #9), it could be synced as an append-only stream between peers.

3. **Should the peer accept check-ins before promotion?** If agents fail over to a peer that's still in peer mode, should it accept check-ins (building FleetIndex) or reject them until promoted? Accepting would give faster recovery but means two servers have partial fleet state simultaneously.

4. **Certificate rotation:** If TLS certs are rotated on the primary, the sync pushes new certs to peers. But agents connected to the peer are using mTLS with the old certs. Need a graceful cert rotation that doesn't break existing connections.
