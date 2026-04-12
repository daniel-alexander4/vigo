
# Geo-Distributed Deployments

Running Vigo across geographic regions — US and Europe, multiple data centers, or any WAN-separated fleet. This guide covers single-server and hub-spoke topologies, latency characteristics, and configuration for distributed fleets up to 500 nodes.

## Single Server, Distributed Fleet

The simplest topology: one Vigo server (US or Europe), agents everywhere.

```
┌──────────────┐
│  vigosrv server │  (US-East)
│  :1530 :8443 │
└──────┬───────┘
       │ gRPC over mTLS
       ├──── US agents (10-40 ms RTT)
       └──── EU agents (80-150 ms RTT)
```

### Why This Works

**Check-ins are pull-based and asynchronous.** The agent polls on a configurable interval (default 5 minutes). An extra 80-150 ms of transatlantic round-trip latency adds negligible overhead to a process that runs every few minutes:

| Operation | US Agent | EU Agent | Difference |
|-----------|----------|----------|------------|
| mTLS handshake (once per ~15 min) | 2-4 ms | 5-10 ms | Negligible (amortized) |
| Check-in (no-change, 95% of requests) | ~55 us + 20 ms RTT | ~55 us + 120 ms RTT | 100 ms (unnoticeable) |
| Full bundle delivery | ~155 us + 20 ms RTT | ~155 us + 120 ms RTT | 100 ms (unnoticeable) |
| Trait report | ~1 ms + 20 ms RTT | ~1 ms + 120 ms RTT | 100 ms (unnoticeable) |

**FleetIndex handles 500 nodes trivially.** The in-memory index serves all lookups in microseconds. At 5-minute intervals, 500 agents produce ~1.7 check-ins/second. At 30-second intervals, ~17/second. Both are well within single-server capacity on modest hardware (see [Performance Analysis](../getting-started/performance-1m.md)).

**Offline convergence absorbs outages.** If the transatlantic link drops, agents continue enforcing cached policy from LMDB. Results queue locally and drain on reconnect. A 10-minute WAN blip is invisible to enforcement — the fleet stays converged. See [Compiled Promises](../concepts/compiled-promises.md).

### Where You Feel Latency

**Ad-hoc tasks and live queries.** These use the agent stream, which is a persistent gRPC connection. Dispatching a task to a European agent from a US server adds ~120 ms round-trip per interaction. For a task that runs a script and returns output, you'll see results in seconds rather than sub-second. This is perceptible but not painful.

**Rolling execution.** Batches that include cross-Atlantic nodes are slower per round. If you have 500 nodes in 10 batches of 50, and each batch waits for completion + health check before the next batch starts, WAN latency adds a few seconds per batch. Total rollout time increases by roughly 30-60 seconds compared to a LAN-only fleet.

**Web UI dashboard.** The dashboard refreshes via HTMX polling. All data comes from FleetIndex (in-memory, zero DB). Response time is dominated by the user's RTT to the server, not agent RTT. If the admin is in the US and the server is in the US, the dashboard feels local regardless of where agents are.

### Configuration

No special configuration needed. The standard `server.yaml` works:

```yaml
checkin:
  interval: "5m"           # or "30s" for responsive fleets
  bundle_max_age: "24h"    # increase for intermittently-connected sites
```

For sites with unreliable WAN links, increase `bundle_max_age` so agents can converge offline for longer:

```yaml
checkin:
  bundle_max_age: "168h"   # 7-day validity for remote sites
```

## Hub-Spoke for Regional Affinity


When you need local-latency check-ins or data sovereignty, deploy spokes in each region:

```
                    ┌──────────────┐
                    │   Hub (US)   │
                    │  :1530 :8443 │
                    └──┬───────┬───┘
            gRPC mTLS  │       │  gRPC mTLS
              ┌────────┘       └────────┐
     ┌────────┴───────┐      ┌─────────┴───────┐
     │  Spoke: us-east │      │  Spoke: eu-west │
     │  :1530          │      │  :1530          │
     └────────┬────────┘      └────────┬────────┘
              │                        │
     US agents (10-40 ms)     EU agents (5-30 ms)
```

### When to Use Hub-Spoke

| Scenario | Recommendation |
|----------|----------------|
| < 500 nodes, single admin team | Single server — simpler, works fine |
| Data sovereignty (EU data stays in EU) | Hub-spoke — trait and run data stays on the local spoke |
| > 500 nodes across regions | Hub-spoke — distributes check-in load |
| Sub-second task responsiveness required | Hub-spoke — tasks dispatch from the local spoke |
| Unreliable WAN between regions | Hub-spoke — agents keep checking in to local spoke even if the hub link drops |

### Hub-Spoke Configuration

**Hub (US):**

```yaml
spanner:
  role: hub
  spokes:
    - id: us-east
      addr: "spoke-us.example.com:1530"
      patterns: ["*.us.*", "*.us-east.*"]
    - id: eu-west
      addr: "spoke-eu.example.com:1530"
      patterns: ["*.eu.*", "*.eu-west.*"]
  auto_failover:
    enabled: true
    threshold: 3
    interval: "30s"
```

**Spoke (EU):**

```yaml
spanner:
  role: spoke
  spoke_id: "eu-west"
  hub_addr: "hub.example.com:1530"
```

### What the Hub Handles

- Web UI, webhooks, bootstrap endpoint

- Enrollment routing (hostname pattern → correct spoke)
- Admin query fan-out (CLI queries both spokes in parallel)
- Aggregated compliance and fleet statistics
- License enforcement
- Config publish sync (pushes `.live/` config to all spokes)

### What Each Spoke Handles

- Agent check-ins for its assigned envoys
- Local run history, traits, and results storage
- Ad-hoc task dispatch (local latency)
- Responds to admin queries from the hub

### Latency Profile with Hub-Spoke

| Operation | Single Server (US→EU) | Hub-Spoke (EU→EU Spoke) |
|-----------|-----------------------|-------------------------|
| Agent check-in | ~120 ms RTT | ~5-30 ms RTT |
| Ad-hoc task | ~120 ms RTT | ~5-30 ms RTT (local spoke) |
| Dashboard load | ~120 ms (EU admin→US server) | ~120 ms (EU admin→US hub) |
| Config publish | Instant (single server) | +120 ms (hub→spoke sync) |
| Enrollment | ~120 ms | ~120 ms (routed through hub) |

### Spoke Resilience

If the WAN link between hub and spoke drops:

- **Agents keep checking in** to their local spoke — enforcement continues uninterrupted
- **Spoke operates independently** — runs, traits, and results are stored locally
- **Admin queries from the hub** return partial results (the disconnected spoke is skipped)
- **Config sync** is queued — the spoke runs its last-synced config until the link recovers
- **Auto-failover** (if enabled) can drain envoys to a reachable spoke after the threshold is exceeded

## Bandwidth and Connection Overhead

### Per-Agent Bandwidth

| Check-in Type | Payload Size | Frequency |
|---------------|-------------|-----------|
| No-change check-in | ~200 bytes | 95%+ of check-ins |
| Full bundle delivery | 2-20 KB (depends on policy size) | After config change |
| Trait report | 1-5 KB | On trait change (hash-deduped) |
| Result report | 0.5-2 KB per resource | After convergence |

At 500 nodes with 5-minute check-ins: ~1.7 requests/second, ~340 bytes/second sustained. Bandwidth is not a constraint over any WAN link.

### Connection Model

Agents use persistent gRPC connections with automatic reconnection:

- **Default mode**: Unary polling (one request-response per check-in). No idle connection overhead.
- **Stream promotion**: The server promotes agents to persistent streams only when there's active work (tasks, queries, workflows). Idle agents never hold open streams.
- **Connection cost**: ~230 KiB per active stream. With 500 nodes and typical orchestration usage, fewer than 50 streams are active at any time.

## DNS and Hostname Patterns

For geographic routing, use hostname conventions that encode region:

```
web01.us-east.example.com
db03.eu-west.example.com
cache01.us-west.example.com
```

This maps directly to spanner spoke patterns and to config node matching:

```yaml
# envoys.vgo
nodes:
  - match: "*.eu-west.*"
    roles: [base, eu-compliance]
    vars:
      ntp_server: "ntp.eu.example.com"
      dns_servers: ["8.8.8.8", "8.8.4.4"]

  - match: "*.us-east.*"
    roles: [base, us-compliance]
    vars:
      ntp_server: "ntp.us.example.com"
      dns_servers: ["1.1.1.1", "1.0.0.1"]
```

Region-specific modules can enforce local compliance requirements (GDPR modules for EU nodes, SOC2 modules for US nodes) using the same config mechanism.

## Recommendations

### Start Simple

For fewer than 500 nodes: **use a single server.** The architecture handles transatlantic latency gracefully. Agents cache policy, converge offline, and queue results. The FleetIndex serves the entire fleet from memory. You can always add spokes later without re-enrolling agents — just configure the hub, stand up spokes, and use `vigocli spanner reassign` to move envoys.

### When to Add Spokes

Add a spoke when any of these become true:

- Regulatory requirements mandate data locality (e.g., EU trait data must stay in EU)
- WAN link reliability drops below 99.5% and you need uninterrupted local enforcement with local orchestration
- Push/query responsiveness matters and 120 ms round-trip is too slow for your workflow
- Fleet grows beyond 500 nodes per region and you want to distribute check-in load

### Bundle TTL for Remote Sites

Increase `bundle_max_age` proportional to how long sites might be disconnected:

| Site Connectivity | Recommended TTL |
|-------------------|-----------------|
| Always-connected (enterprise WAN) | `24h` (default) |
| Mostly-connected (branch offices) | `72h` |
| Intermittent (ships, remote sites) | `168h` (7 days) |
| Air-gapped with periodic sync | `720h` (30 days) |

### Monitoring

Use [Prometheus metrics](metrics.md) to track geographic health:

- `vigo_checkin_duration_seconds` — histogram by envoy; spot WAN-induced outliers
- `vigo_stale_envoys` — gauge; a spike in one region suggests WAN trouble
- `vigo_spoke_latency_seconds` — histogram per spoke (hub-spoke only)
- Per-spoke compliance breakdown visible in the [Spanner UI](../reference/ui/spanner.md) and via `vigocli spanner status`

## Related

- [Architecture](../concepts/architecture.md) — System overview
- [Spanner](../concepts/spanner.md) — Hub-spoke configuration details
- [Compiled Promises](../concepts/compiled-promises.md) — Offline convergence and bundle TTL
- [Performance Analysis](../getting-started/performance-1m.md) — Server capacity projections
- [Tuning Parameters](tuning.md) — All configurable knobs
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
