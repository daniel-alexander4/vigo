# SQLite Scaling Strategy

## The write pressure is mostly solved already

Think about what actually hits SQLite on each check-in:

| Write | Frequency at 100k nodes | Current solution |
|---|---|---|
| last_seen | 333/sec | Already batched — LastSeenBatcher coalesces to one UPDATE every 10s |
| Run record + results | 333/sec | MemRunStore exists — in-memory ring buffer, never touches SQLite |
| Traits | Rare (traits don't change often) | Only written when hash differs from previous |
| Enrollment | One-time per node | Negligible |
| Compliance history | Once per day | Negligible |

With MemRunStore enabled and last-seen batching, SQLite gets maybe a few writes per second at steady state. It can handle thousands. The migration to SQLite was the right call — the "SQLite can't scale" concern is about a workload you've already moved out of SQLite.

## The real bottleneck is CPU, not the database

333 check-ins/second, each one doing: hostname matching → role resolution → module merging → conditional evaluation against traits → Go template rendering → secret resolution. That's the expensive part. SQLite is sitting idle while the CPU grinds through config compilation.

## How to make embedded work at 100k

### Step 1: Cache compiled policies, don't recompute

Right now every check-in recomputes the node's desired state from scratch. Instead:

- When config files change (fsnotify), precompute and cache the compiled policy for every node-hostname pattern
- On check-in, look up the cached result by hostname. Evaluate trait-dependent conditionals only if the node's trait hash changed since last check-in
- Config change affects 3 modules? Invalidate only nodes that use those modules

This turns check-ins from "compute everything" to "hash lookup + serve cached bytes." The delta policy transfer (content-addressable module hashing) already does half of this — extend it to cache the full compiled response.

### Step 2: Move policy evaluation to the agent

This is the CFEngine insight. Instead of the server computing each node's desired state:

- Server distributes the raw policy bundle (modules, roles, node mappings, templates) to agents
- Agent matches its own hostname, resolves its own roles/modules, evaluates its own conditionals, renders its own templates
- Check-in becomes: "has the policy bundle hash changed?" → "no" → done. Or "yes" → download new bundle → evaluate locally

The server goes from doing O(nodes × resources) computation to O(1) "here's the bundle hash." This is how CFEngine handles 100k+ with a single hub.

The secret resolution problem: Agents can't have the full secret store. Two options:
- Server pre-resolves secrets into a per-node encrypted envelope, included in the bundle download
- Agent fetches its own secrets from isopass directly (agents already have identity — mTLS cert + ED25519 key could authenticate to isopass)

### Step 3: Spoke federation with embedded SQLite per spoke

```
         ┌──────────┐
         │   Hub    │  ← UI, API, webhooks, policy distribution
         │ (SQLite) │     low-write: compliance, enrollment, webhooks
         └─────┬────┘
               │  policy bundle push (on config change only)
     ┌─────────┼─────────┐
     │         │         │
┌────▼───┐ ┌──▼─────┐ ┌─▼──────┐
│Spoke A │ │Spoke B │ │Spoke C │  ← 30-40k agents each
│(SQLite)│ │(SQLite)│ │(SQLite)│     own last-seen, MemRunStore, traits
└────────┘ └────────┘ └────────┘
```

Each spoke is the same hobd binary with a flag (`--spoke --hub-addr=...`). Each has its own SQLite for its node partition. The hub aggregates on-demand for UI queries. Policy bundles flow hub → spokes → agents. Run results stay on the spoke (MemRunStore). Compliance summaries flow spoke → hub periodically.

The spoke's SQLite handles ~30-40k nodes. With MemRunStore and last-seen batching, that's near-zero write pressure per spoke.

### Step 4: Persistent gRPC streams

Replace poll-based check-ins with a long-lived bidirectional stream per agent. Server pushes policy changes instantly. Agent pushes run results and trait updates when they happen. No more 333 handshakes/second — just 100k idle connections (a few GB of memory across spokes, well within reach).

This also eliminates the check-in interval as a scaling factor. Instead of 100k nodes all polling independently, you have 100k open connections with traffic only when something changes.

## The sequence that keeps things embedded

| Step | What changes | Effect |
|---|---|---|
| Compiled policy cache | Cache computed desired state, invalidate on config change | 10-50x reduction in CPU per check-in |
| Agent-side evaluation | Distribute raw bundle, agents evaluate locally | Server CPU drops to near-zero per check-in |
| Persistent streams | Bidirectional gRPC streams, push-based | Eliminates TLS handshake overhead, enables instant push |
| Spoke federation | Multiple hobd instances, each with own SQLite | Horizontal scaling, no shared DB |

Steps 1 and 2 get you to ~50k nodes on a single server with SQLite. Steps 3 and 4 get you past 100k. At no point do you need PostgreSQL. Each component has its own embedded SQLite with a workload that fits SQLite's strengths — small databases, read-heavy, infrequent writes.

## Implementation status

| Step | Status |
|---|---|
| 1. Compiled policy cache | Done (5129b6e) |
| 2. Agent-side evaluation | Done (scalability plan rec #4) |
| 3. Spoke federation — Phase 1 | Done — federation config, spoke mode, spoke gateway |
| 3. Spoke federation — Phase 2 | Done — hub aggregation via spoke AdminService fan-out |
| 3. Spoke federation — Phase 3 | Done — enrollment routing (hub assigns nodes to spokes) |
| 4. Persistent gRPC streams — server | Done — proto, stream handler, stream manager, config push |
| 4. Persistent gRPC streams — agent | Done — stream client, event loop, automatic fallback to polling |

### Step 3 Phase 1: Spoke mode

- `FederationSection` in server.yaml: `role` (standalone/hub/spoke), `spoke_id`, `hub_addr`
- Spoke mode: disables webhook dispatcher, secret watcher, bootstrap handler
- Spoke mode: serves gRPC (agent + admin) + lightweight REST (API, metrics, healthz, traits)
- Hub mode: identical to standalone (Phase 2 adds spoke aggregation)
- Each instance has its own SQLite, ConfigStore, MemRunStore
- Config shared via filesystem (git sync, rsync, NFS)

### Step 3 Phase 2: Hub aggregation (no new proto)

Key insight: spokes already expose `HobnobAdmin` gRPC. Hub just dials spokes and calls existing RPCs.
- `federation.Hub` — connects to spoke gRPC servers via mTLS, fans out queries concurrently
- Hub config: `federation.spokes` list with `id` + `addr` per spoke
- `AggregateListNodes` — fans out ListNodes, merges results
- `AggregateComplianceSummary` — fans out GetComplianceSummary, sums counts, recomputes pct
- `AggregateListRuns` — fans out ListRuns, merges results
- `ForwardForcePush` — fans out ForcePush, sums notified count
- AdminService delegates to Hub when `hub != nil` (ListNodes, ListRuns, GetComplianceSummary, ForcePush)
- 5 hub tests with fake spoke gRPC servers

### Step 3 Phase 3: Enrollment routing

- `SpokeEntry.Patterns` — hostname globs that determine which spoke owns which nodes
- Hub receives `Register`, validates token + pattern, matches spoke by hostname
- Hub calls spoke's `EnrollNode` admin RPC (creates node without token re-validation)
- `RegisterResponse.spoke_addr` — hub returns spoke gRPC address to agent
- Agent saves spoke address to `{data_dir}/spoke.addr`, uses it for check-ins
- `registry.CreateNode()` — creates node directly (no token validation, for hub→spoke use)

### Step 4: Persistent gRPC streams

Server side (done previously):
- Proto: `AgentStreamMessage` (agent→server), `ServerStreamMessage` (server→agent)
- `AgentStream` bidi RPC on HobnobAgent service
- `streamManager` in `server/grpc/streams.go` — tracks connected agents, push notifications
- `buildCheckInResponse()` shared between unary CheckIn and stream handler

Agent side:
- `agent/checkin/stream.go`: `StreamConn` wraps the bidi stream, dispatches server messages to channels
- `OpenStream()` — opens stream, sends initial check-in as handshake, returns initial response
- `StreamConn.CheckIn()` — sends check-in on stream, waits for response via channel
- `StreamConn.ReportResult()` — fire-and-forget result reporting on stream
- `StreamConn.PolicyPush` / `ForcePush` channels — receive server-initiated push notifications
- `StreamConn.Done()` — closed on disconnect for clean shutdown
- `Loop()` modified: tries stream first, falls back to polling if unavailable
- `streamLoop()` — event-driven: handles policy pushes, force pushes, periodic check-ins
- `converge()` — extracted from RunOnce, shared between poll and stream modes
- `client.BuildCheckInRequest()` / `BuildReportResultRequest()` — construct signed requests for stream use
- 13 stream-specific tests (handshake, push dispatch, check-in, report, disconnect)

## Key realization

SQLite's limitation is concurrent writers, not database size or read throughput. The MemRunStore pattern — keep hot, high-write data in memory, only persist what's worth persisting — is the right architecture. Extend it to traits and last-seen, and SQLite becomes a durable config store that barely gets written to.
