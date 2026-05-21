
# Set up Spanner (peer-equal federation)

> **2A.1b YAML shape change (2026-05-19):** The pre-Phase-2 `role: hub|spoke` field and the per-bolt `bolts:` list are retired per [ADR-026](../adr/026-spanner-peer-equal-admissions.md). The loader hard-errors on `spanner.role`, `spanner.hub_addr`, `spanner.hub_fallback_addrs`, `spanner.bolts`, `spanner.auto_failover`, and `spanner.bolt_id`. The new shape uses `spanner.mode: founder|joiner|standalone`, per-vigosrv `spanner.patterns:`, and `spanner.transport.bootstrap_peers:` for first-contact discovery. The conceptual sections below ("Enrollment Routing", "Admin Query Fan-out", "Bolt Health") still describe how the founder behaves as a bootstrap-discovery rendezvous in Phase 2A — Phase 3 retires the founder/joiner distinction entirely and admin-routing becomes pure CRDT-driven.

You'll finish this page with a founder server acting as a bootstrap-discovery rendezvous for one or more joiner servers — each peer owning a hostname-pattern partition of the fleet, peers sharing an admissions roster via CRDT gossip. Envoys keep checking in on whatever address they originally enrolled against.

**When you'd use this:** when one server can no longer comfortably hold the fleet's check-in cadence, query load, or convergence-history retention. Practical inflection points are usually north of ~5,000 envoys per server, or when you want geographic partitioning so a regional outage doesn't take the whole control plane down.

**When you'd skip this:** smaller fleets (a single-server `standalone` deployment is the default and the right answer for almost every operator); fleets that need HA but not horizontal sharding (use `server/peer/` primary/secondary replication instead — see the `peer:` section in `server.yaml`).

> **License requirement:** Spanner needs a paid license. The built-in free license (100 envoys) does not support founder or joiner mode; the server refuses to start with `spanner.mode` set to `founder` or `joiner` on the free license. Paid licenses are bound to a specific server via [machine fingerprint](troubleshoot.md#machine-fingerprint-binding) — migrating the founder to a new machine requires a new license.

## Enable it

Three modes: `standalone` (default — no spanner config), `founder` (bootstrap-discovery rendezvous; also owns a pattern partition like any peer), and `joiner` (a peer that joined an existing spanner). Pick the mode per server in `server.yaml`:

```yaml
# Founder server.yaml:
spanner:
  mode: founder
  spanner_id: alexander4-prod
  patterns: ["*.east.example.com"]

# Joiner server.yaml:
spanner:
  mode: joiner
  spanner_id: alexander4-prod        # must match the founder's spanner_id
  patterns: ["*.west.example.com"]
  transport:
    bootstrap_peers: ["founder.lab:1530"]
```

Restart each server (founder first, joiners second). Each joiner runs the admission ceremony with the founder via `JoinSpanner` (Slice 2A.2+) and starts participating in the gossiped admissions CRDT.

## Use it

`vigocli spanner status` shows the spanner topology, per-bolt health, aggregated convergence + compliance + license usage. Cross-bolt admin queries (force-push, ad-hoc tasks, live queries) work through the hub: you talk to one place, the hub fans out to the right bolt(s).

If a bolt health-checks N times in a row, the hub drains it by reassigning its envoys to the hub itself (envoys keep checking in; no data loss). Recovery is automatic when the bolt returns.

## Architecture

![Spanner Architecture](../img/spanner.svg)

## Roles

### standalone (default)

Single server handles everything. No spanner config needed.

### hub

The central server that:
- Serves the web UI, webhooks, bootstrap
- Routes enrollment to the correct bolt based on hostname patterns
- Fans out admin queries to all bolts and aggregates results
- Aggregates compliance and fleet statistics
- Handles license enforcement

```yaml
spanner:
  role: hub
  bolts:
    - id: dc1
      addr: "bolt1.example.com:1530"
      patterns: ["*.dc1.*"]
    - id: dc2
      addr: "bolt2.example.com:1530"
      patterns: ["*.dc2.*"]
```

### spoke (bolt)

A partition server that:
- Handles agent check-ins for its assigned envoys
- Stores runs, traits, and results locally
- Responds to admin queries from the hub
- Does not serve the web UI, webhooks, or bootstrap

```yaml
spanner:
  role: spoke
  bolt_id: "dc1"
```

## Enrollment Routing

When an envoy bootstraps through the hub, the hub routes the enrollment to the correct bolt based on hostname pattern matching:

```
Envoy: web3.dc1.example.com
    ↓
Hub: match "*.dc1.*" → route to bolt dc1
    ↓
Bolt dc1: complete enrollment, store pubkey
```

## Admin Query Fan-out

CLI and API queries go through the hub, which fans out to all bolts:

```bash
vigocli envoys list
```

```
Hub receives request
    ↓
Fan out to bolt dc1 + bolt dc2 (parallel)
    ↓
Merge results
    ↓
Return to CLI
```

## Config Sync

When `vigocli config publish` runs on the hub, the config is automatically pushed to all bolts. The hub creates a tar.gz of the `.live/` directory and sends it via the `SyncConfig` RPC. Bolts extract the archive, overwrite their local `.live/` directory, and reload. This is best-effort — if a bolt is unreachable, it's skipped and logged.

> The config-sync RPC path is being retired in the broader spanner redesign (ADR-025) in favor of stockpile-in-gitback distribution. The migration ships in Phase 0's later slices.

## Bolt Health

The hub monitors bolt health via periodic gRPC probes. Bolts can also push their status to the hub if `hub_addr` is configured. The web UI shows:
- Bolt status (online/offline)
- Per-bolt compliance breakdown
- Per-bolt latency metrics
- Envoy count per bolt

### Bolt Status Reporting

Bolts can proactively report their status to the hub:

```yaml
spanner:
  role: spoke
  bolt_id: "dc1"
  hub_addr: "hub.example.com:1530"  # enables bolt→hub reporting
```

The bolt sends compliance metrics to the hub every 30 seconds. If the hub is unreachable, the bolt continues operating normally.

### Hub Failover for Bolts

When the hub has peer replication configured (see the `peer:` section in `server.yaml`), each bolt should list the hub's peer addresses as fallbacks. After 3 consecutive failures on the current hub address, the bolt automatically tries the next fallback:

```yaml
spanner:
  role: spoke
  bolt_id: "dc1"
  hub_addr: "hub.example.com:1530"
  hub_fallback_addrs:
    - "hub-standby.example.com:1530"
```


The bolt cycles through all addresses (primary, then fallbacks) until one responds. This ensures bolts reconnect to a promoted hub peer without manual intervention.

### Auto-Failover

The hub can automatically drain unhealthy bolts:

```yaml
spanner:
  role: hub
  auto_failover:
    enabled: true
    threshold: 3      # consecutive failures before drain (default 3)
    interval: "30s"   # health check interval (default 30s)
```

When a bolt fails `threshold` consecutive health checks (default: 3 failures = 90 seconds), the hub automatically drains all its envoys to other bolts based on hostname pattern matching. When the bolt recovers, it's marked healthy again and can accept new envoys.

## Envoy Reassignment

Move envoys between bolts:

```bash
vigocli spanner bolt reassign <envoy-id> --from dc1 --to dc2
```

Drain all envoys from a bolt:

```bash
vigocli spanner bolt drain dc1 --to dc2
```

The agent handles reconnection automatically — on next check-in, it's redirected to the new bolt.

## Bolt Detail

View detailed bolt information:

```bash
vigocli spanner bolt show dc1
```

The web UI has a bolt detail page at `/spanner/bolts/{id}` showing per-bolt compliance, recent runs, and envoy list.

## Related

- [Architecture](../reference/_index.md) — System overview
- [Server Configuration](../reference/server-yaml.md) — `spanner:` section
- [ADR-025: stockpile-in-gitback](../adr/025-stockpile-in-gitback.md) — the broader spanner redesign that will retire the hub-mediated config-sync RPC path

## What's next

- **HA for the hub itself** → `server/peer/` primary/secondary replication. Pair it with `hub_fallback_addrs:` on every bolt so failover is transparent.
- **A bolt is wedged** → `vigocli spanner status` shows per-bolt health; the hub auto-drains after `threshold` consecutive misses. Manually drain with `vigocli spanner bolt drain <id> --to <other>`.
- **License is too small** → spanner needs a paid license; see [license](../reference/server-yaml.md) and [troubleshoot.md#machine-fingerprint-binding](troubleshoot.md#machine-fingerprint-binding) for fingerprint rebinds.
- **Cross-bolt admin queries are slow** → Tier-1 counter-gossip cadence is `30s` by default; reduce on the founder via `spanner.snapshot.tier1_interval` for tighter cross-bolt convergence on flapping peers.

---
*Verified on Vigo 0.65.0 · 2026-05-17.*

*Confidential — Alexander4, LLC. Not for redistribution. See [../legal/license.md](../legal/license.md).*
