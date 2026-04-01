
# P2P Spanner Architecture (Future)

This document describes a peer-to-peer spanner model that eliminates the hub as a single point of failure. This is a design document — the current implementation uses hub-spoke spanner, which is sufficient for most deployments.

## Motivation

The current hub-spoke model has a structural limitation: the hub is a single point of failure for the web UI, enrollment, admin queries, and config distribution. If the hub goes down, spokes continue serving agents (check-ins, convergence, offline policy), but no new enrollments, admin operations, or UI access is possible until the hub recovers.

For organizations requiring high availability of the control plane — not just the data plane — a peer-to-peer model removes this bottleneck.

## Architecture Overview

```
┌──────────┐     ┌──────────┐     ┌──────────┐
│  Spoke A  │◄───►│  Spoke B  │◄───►│  Spoke C  │
│  (peer)   │     │  (peer)   │     │  (peer)   │
└─────┬─────┘     └─────┬─────┘     └─────┬─────┘
      │                 │                 │
   agents            agents            agents
```

All servers are peers. There is no dedicated hub. Any peer can serve the web UI, accept enrollment, and answer admin queries. A lightweight leader election protocol coordinates the few operations that require consensus.

## Peer Responsibilities

Every peer handles:
- Agent check-ins for its owned envoys (unchanged from spoke behavior)
- Run storage, trait storage, result reporting
- Web UI serving, with fan-out to peers for aggregated views
- Bootstrap and enrollment (accepting and routing)
- Admin CLI requests

## Config Distribution

Config is the core shared state. Three options, in order of simplicity:

### Option A: Git-based (recommended)

All peers pull from the same git repository on a timer or webhook.

```yaml
config:
  source: git
  repo: "git@github.com:org/vigo-config.git"
  branch: main
  poll_interval: "60s"
```

Advantages:
- No consensus needed — git is the source of truth
- Audit trail built in (git log)
- Operators use familiar git workflows (branch, review, merge)
- `vigocli config publish` becomes `git push` with a webhook to notify peers

Disadvantages:
- Requires a git server (GitHub, GitLab, Gitea, bare repo on shared storage)
- Slight propagation delay (poll interval)

### Option B: Shared filesystem

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*

---

All peers mount the same NFS/CIFS share or S3 bucket for the `.live/` directory.

```yaml
config:
  source: s3
  bucket: "vigo-config"
  region: "us-west-2"
  poll_interval: "30s"
```

Advantages:
- Simple, no new protocol
- Works with existing infrastructure

Disadvantages:
- Shared filesystem is itself a SPOF
- No built-in audit trail

### Option C: Raft-replicated config store

Peers replicate config via embedded Raft consensus (e.g., hashicorp/raft).

Advantages:
- Fully self-contained, no external dependencies
- Strong consistency

Disadvantages:
- Significant implementation complexity
- Quorum requirements (need odd number of peers)
- Operational overhead (Raft cluster management)

## Peer Discovery

Peers need to find each other. Options:

### Static configuration

```yaml
spanner:
  role: peer
  peer_id: "dc1"
  peers:
    - id: dc2
      addr: "spoke2.example.com:1530"
    - id: dc3
      addr: "spoke3.example.com:1530"
```

Simple, explicit, works for fixed infrastructure. Adding a peer requires config change on all existing peers.

### DNS SRV records

```yaml
spanner:
  role: peer
  peer_id: "dc1"
  discovery: dns
  dns_name: "_vigo._tcp.example.com"
```

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*

---

Peers query DNS SRV records to discover each other. Adding a peer requires only a DNS update.

### Gossip protocol

Peers use a gossip protocol (e.g., Serf/memberlist) to discover and monitor each other. Only one seed peer address is needed.

```yaml
spanner:
  role: peer
  peer_id: "dc1"
  discovery: gossip
  seeds: ["spoke1.example.com:7946"]
```

Most resilient but adds protocol complexity.

## Leader Election

Most operations work without a leader. The few that need coordination:

| Operation | Why | Frequency |
|-----------|-----|-----------|
| Enrollment token validation | One-time tokens must be consumed exactly once | Rare (enrollment events) |
| Webhook deduplication | Prevent duplicate webhook delivery for the same event | Per run completion |
| Enrollment routing | Decide which peer owns a new envoy | Rare |

Leader election can use:
- **Raft** (if using Option C for config) — leader already exists
- **Simple leader election** via distributed lock (etcd, consul, or database-backed)
- **Consistent hashing** — avoid a leader entirely by deterministically mapping operations to peers based on hash

For enrollment tokens specifically, consistent hashing on the token value would route validation to a specific peer without needing a global leader.

## Envoy Ownership

Each envoy is owned by one peer. Ownership is determined by hostname pattern matching (same as current spoke patterns). The difference: every peer knows the full pattern table and can route requests.

```
Agent bootstraps → contacts any peer → peer checks patterns →
  If I own this pattern: enroll locally
  If peer X owns it: forward enrollment RPC to peer X
```

This is the same as current hub routing, except any peer can do it.

### Ownership transfer

Envoy reassignment works the same as today — one peer marks the envoy for reassignment, agent reconnects to the new peer. The difference: any peer can initiate a reassignment, not just the hub.

## Admin Query Fan-out

Any peer can answer admin queries by fanning out to all peers and aggregating results. This is identical to the current hub behavior, just not limited to a single server.

The web UI includes a peer selector or automatically queries all peers. The peer receiving the request acts as the temporary aggregator.

## Failure Handling

| Failure | Impact | Recovery |
|---------|--------|----------|

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*

---
| One peer down | Its envoys use cached policy (offline convergence). Other peers detect the failure. | Auto-reassign envoys after timeout, or wait for recovery. |
| Minority of peers down | Quorum maintained (if using Raft). Full operations continue. | Envoys auto-reassigned or wait. |
| Majority of peers down | Loss of quorum (Raft only). Read-only mode. Agents continue with cached policy. | Restore peers, quorum recovers. |
| Network partition | Each partition operates independently. Envoys converge locally. | Heal partition, peers reconcile. |

The critical insight: agents always have cached policy and offline convergence. The control plane can be entirely down and agents continue enforcing desired state. P2P spanner improves control plane availability, not data plane availability (which is already resilient).

## Migration Path

Moving from hub-spoke to P2P:

1. All spokes become peers (config change: `role: peer`)
2. Hub becomes a peer (loses special status)
3. Config source switches from filesystem to git/S3
4. Peer discovery configured (static, DNS, or gossip)
5. Agents don't change — they still connect to their assigned peer's gRPC address

The agent protocol is unchanged. P2P spanner is purely a server-side topology change.

## When to Use P2P vs Hub-Spoke

| Criterion | Hub-Spoke | P2P |
|-----------|-----------|-----|
| Deployment size | Any | Large (10+ servers, multiple datacenters) |
| HA requirement | Hub is SPOF (acceptable for most) | No SPOF |
| Operational complexity | Low (one hub, N spokes) | Higher (peer mesh, consensus) |
| Config management | Filesystem + push | Git or shared storage |
| Network topology | Star (hub at center) | Mesh (all-to-all) |

Hub-spoke is the right choice for most deployments. P2P is for organizations that need control plane HA and are willing to accept the operational complexity.

## Implementation Estimate

| Component | Effort | Dependencies |
|-----------|--------|-------------|
| Peer mesh networking | Medium | Replace hub fan-out with peer-to-peer gRPC mesh |
| Static peer discovery | Low | Config change, connect to all listed peers |
| DNS SRV discovery | Low | DNS query + periodic refresh |
| Gossip discovery | Medium | Add memberlist/serf dependency |
| Git-based config | Medium | Git polling, webhook receiver, .live/ sync |
| Leader election (Raft) | High | hashicorp/raft integration, state machine |
| Consistent hashing | Low | Token routing without leader |
| Web UI from any peer | Low | Already works — just enable UI on all peers |
| Bootstrap from any peer | Low | Enrollment routing already implemented |

Recommended implementation order: static discovery → git config → web UI on all peers → consistent hashing for tokens → gossip discovery (optional) → Raft (optional).
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
