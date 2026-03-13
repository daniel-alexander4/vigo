# Database Architecture Plan

## Problem

The current architecture puts SQLite on the check-in hot path (pubkey lookups, thrall state reads, synchronous run result writes) and requires the agent to be online to function. This limits the server to ~2,000 thralls before write contention becomes a concern, and any server downtime means agents stop converging.

## Goals

1. Zero database operations on the check-in hot path
2. Sub-microsecond fleet queries (compliance, inventory, status)
3. Agent autonomy — converge offline, queue deltas, stream on reconnect
4. Retain SQLite as the sole server-side persistent store (no PostgreSQL)
5. Scale a single standalone server to 10,000+ thralls

## Architecture Overview

```
Agent                              Server
─────                              ──────
Converge (local LMDB)
  │
  ├─ Delta events ──gRPC stream──▶ FleetIndex (memory) ◀── UI/API queries
  │                                    │
  │                                    │ async batch (10s)
  │                                    ▼
  └─ Local state survives           SQLite (history, audit, recovery)
     server downtime
```

Three storage tiers, each optimized for its access pattern:

| Tier | Technology | Access Pattern | Latency |
|---|---|---|---|
| Agent local state | LMDB | Read-heavy, single writer | <1us |
| Server real-time state | In-memory Go structs | Read-heavy, delta-fed | <1us |
| Server historical/audit | SQLite (WAL mode) | Append-heavy, batch writes | ~5ms |

## Phase 1: Agent Local State (LMDB)

### Overview

Add persistent local storage to the Rust agent using LMDB (via the `heed` crate). The agent becomes fully autonomous — it can converge using cached policy when the server is unreachable, and queues result deltas for later delivery.

### LMDB Stores

| Store | Key | Value | Purpose |
|---|---|---|---|
| `policy_cache` | version (u64) | Serialized policy bundle (protobuf bytes) | Converge when server is unreachable |
| `compliance` | resource ID (string) | Status enum + last check timestamp | Local drift detection, delta computation |
| `file_checksums` | file path (string) | SHA256 hash + mtime | Skip unchanged files without re-reading |
| `traits_cache` | trait key (string) | Value + collection timestamp | Avoid re-collecting expensive traits (e.g., package lists) |
| `runs` | run ID (string) | Result summary (JSON) | Offline run history, retry delivery on reconnect |
| `pending_deltas` | sequence number (u64) | Delta event (protobuf bytes) | Queue for delivery when server returns |

### Agent Behavior Changes

**Online (server reachable):**
1. Check in via gRPC stream as today
2. On policy update: cache policy bundle to `policy_cache`
3. Converge, writing results to local `compliance` and `runs` stores
4. Compute deltas (what changed since last report) from `compliance` store
5. Stream deltas to server immediately

**Offline (server unreachable):**
1. Load cached policy from `policy_cache`
2. Converge normally using cached policy
3. Write results to local stores
4. Append deltas to `pending_deltas` queue
5. On reconnect: stream all pending deltas in sequence order, then resume normal operation

**Startup:**
- If LMDB stores exist, load cached state
- If no cached policy and server unreachable, log warning and wait with backoff (same as today)

### LMDB Configuration

- **Database path:** `/var/lib/hobnob/state/` (configurable)
- **Map size:** 256MB (sufficient for thousands of resources + run history)
- **Max readers:** 8 (convergence loop + trait collectors + reporter + delta streamer)
- **No explicit compaction needed** — LMDB's copy-on-write B+ tree reclaims pages automatically

### File Changes

| File | Change |
|---|---|
| `agent-rs/Cargo.toml` | Add `heed` dependency |
| `agent-rs/src/state/mod.rs` | New module — LMDB store abstraction |
| `agent-rs/src/state/policy.rs` | Policy cache read/write |
| `agent-rs/src/state/compliance.rs` | Compliance state + delta computation |
| `agent-rs/src/state/checksums.rs` | File checksum cache |
| `agent-rs/src/state/traits.rs` | Traits cache with TTL |
| `agent-rs/src/state/deltas.rs` | Pending delta queue (sequence-ordered) |
| `agent-rs/src/checkin/stream.rs` | Use cached policy on server disconnect |
| `agent-rs/src/runner/mod.rs` | Write compliance state after each resource |
| `agent-rs/src/reporter.rs` | Compute deltas from compliance store, stream to server |

## Phase 2: Server In-Memory FleetIndex

### Overview

Replace all real-time SQLite queries with an in-memory index structure fed by agent delta streams. The FleetIndex holds current state for every thrall and pre-computed aggregates for common queries. SQLite is never touched on the check-in or query hot path.

### Data Structure

```go
type FleetIndex struct {
    mu          sync.RWMutex
    thralls     map[string]*ThrallState    // thrall ID → current state
    byHostname  map[string]string          // hostname → thrall ID
    byStatus    map[Status]map[string]bool // compliance status → set of thrall IDs
    byTrait     map[string]map[string]map[string]bool
                // trait_key → trait_value → set of thrall IDs
    compliance  ComplianceCounters         // pre-computed totals
    recentRuns  *RingBuffer[RunSummary]    // last N runs fleet-wide
    pubkeys     map[string]ed25519.PublicKey // thrall ID → parsed public key
}

type ThrallState struct {
    ID                   string
    Hostname             string
    IP                   string
    EnrolledAt           time.Time
    LastSeen             time.Time
    Revoked              bool
    ComplianceStatus     Status
    LastRunStatus        string
    LastRunAt            time.Time
    Traits               map[string]string
    PendingRotatedSecrets []string
    ForcePush            bool
}

type ComplianceCounters struct {
    Total     int
    Compliant int
    Drifted   int
    Failing   int
    Stale     int
    Unknown   int
}
```

### Index Operations

| Operation | Method | Complexity |
|---|---|---|
| Get thrall by ID | `thralls[id]` | O(1) |
| Get thrall by hostname | `byHostname[hostname]` → `thralls[id]` | O(1) |
| List thralls by status | `byStatus[status]` | O(1) lookup, O(k) iterate |
| Query by trait | `byTrait[key][value]` | O(1) lookup, O(k) iterate |
| Compliance summary | `compliance` | O(1) — pre-computed |
| Verify signature | `pubkeys[id]` | O(1) |
| Recent runs | `recentRuns.Slice()` | O(k) |

### Delta Ingestion

When a delta event arrives from an agent:

1. Acquire write lock on FleetIndex
2. Update `ThrallState` fields that changed
3. Update secondary indexes (`byStatus`, `byTrait`) if relevant fields changed
4. Update `ComplianceCounters` if compliance status changed (decrement old, increment new)
5. Append to `recentRuns` if it's a run completion delta
6. Release write lock

Write lock hold time is microseconds — single struct field updates and map insertions.

### Enrollment Path

On `Register()` RPC:
1. Write thrall record to SQLite (durable enrollment)
2. Parse pubkey PEM, insert into `FleetIndex.pubkeys`
3. Insert new `ThrallState` into FleetIndex with status `Unknown`

### What Moves Off SQLite Hot Path

| Current Hot Path | New Hot Path |
|---|---|
| `SELECT pubkey_pem FROM thralls WHERE id = ?` (every RPC) | `FleetIndex.pubkeys[id]` (memory) |
| `SELECT * FROM thralls WHERE id = ?` (every check-in) | `FleetIndex.thralls[id]` (memory) |
| `UPDATE thralls SET last_seen = ...` (batched, still DB) | `FleetIndex.thralls[id].LastSeen = now` (memory) |
| `INSERT INTO runs ...` (every run report) | Async batch to SQLite (cold path) |
| `INSERT INTO run_results ...` (every resource result) | Async batch to SQLite (cold path) |
| `SELECT COUNT(*) ... GROUP BY status` (compliance query) | `FleetIndex.compliance` (memory) |

### File Changes

| File | Change |
|---|---|
| `server/fleet/index.go` | New — FleetIndex struct, read/write methods |
| `server/fleet/delta.go` | New — delta event types, ingestion logic |
| `server/fleet/rehydrate.go` | New — load FleetIndex from SQLite on startup |
| `server/grpc/interceptor.go` | Use `FleetIndex.pubkeys` instead of `ThrallCache` |
| `server/grpc/agentstream.go` | Use FleetIndex for thrall state lookups |
| `server/grpc/checkin.go` | Remove SQLite reads from check-in path |
| `server/db/clientcache.go` | Remove — replaced by FleetIndex |
| `server/db/lastseen.go` | Remove — LastSeen tracked in FleetIndex, flushed async |
| `server/api/gateway.go` | Pass FleetIndex to UI handler |
| `server/compliance/` | Use FleetIndex counters instead of DB queries |
| `ui/handler.go` | Query FleetIndex for dashboard, thralls list, compliance |

## Phase 3: Async Cold Store Writes

### Overview

All SQLite writes become asynchronous batch operations. The existing `LastSeenBatcher` and `TraitsBatcher` patterns are extended to cover run results, compliance snapshots, and trait updates — all fed from the FleetIndex rather than directly from RPCs.

### Batch Writers

| Writer | Source | Flush Interval | Batch Size |
|---|---|---|---|
| `StateFlusher` | FleetIndex dirty flags | 10s | All dirty thralls |
| `RunBatcher` | FleetIndex run completions | 10s | Up to 500 rows |
| `TraitsBatcher` | FleetIndex trait changes (hash-deduped) | 10s | Up to 200 rows |
| `ComplianceSnapshotter` | FleetIndex compliance counters | 1 day | 1 row |

### StateFlusher

Replaces `LastSeenBatcher`. Flushes all thrall state changes (last_seen, compliance status, force_push clears) in a single batched UPDATE. Tracks dirty thralls via a set in FleetIndex.

### RunBatcher

Accumulates run completion events from delta stream. Flushes to `runs` + `run_results` tables in a single transaction. On failure, re-queues for next flush.

### Startup Rehydration

On server start:
1. Load all thralls from SQLite → populate `FleetIndex.thralls`, `pubkeys`, `byHostname`
2. Load latest traits per thrall → populate `byTrait`
3. Load latest run status per thrall → populate compliance status, `byStatus`
4. Compute `ComplianceCounters` from loaded statuses
5. Log rehydration time and thrall count

Expected rehydration time: ~1-3 seconds for 10,000 thralls.

### File Changes

| File | Change |
|---|---|
| `server/fleet/flusher.go` | New — StateFlusher (replaces LastSeenBatcher) |
| `server/fleet/runbatcher.go` | New — async run result batch writer |
| `server/db/lastseen.go` | Remove — replaced by StateFlusher |
| `server/db/traitsbatcher.go` | Modify — feed from FleetIndex instead of direct RPC |
| `server/main.go` | Wire up FleetIndex, start flusher goroutines, rehydrate on startup |

## Phase 4: Delta Streaming Protocol

### Overview

Replace the current `ReportResult` RPC (full result per run) with a delta-based streaming protocol over the existing bidirectional gRPC stream. Agents send only what changed, reducing bandwidth and enabling the FleetIndex to stay current in real time.

### Delta Event Types

```protobuf
message DeltaEvent {
  string thrall_id = 1;
  int64 sequence = 2;  // monotonic per-thrall, for ordering and dedup
  oneof event {
    ComplianceDelta compliance = 3;
    TraitsDelta traits = 4;
    RunCompleted run = 5;
    HeartbeatDelta heartbeat = 6;
  }
}

message ComplianceDelta {
  string resource_id = 1;
  string old_status = 2;
  string new_status = 3;
}

message TraitsDelta {
  map<string, string> changed = 1;
  repeated string removed = 2;
}

message RunCompleted {
  string run_id = 1;
  string status = 2;
  int32 changed_count = 3;
  int32 failed_count = 4;
  int32 skipped_count = 5;
  repeated ResourceResult results = 6;  // only changed/failed resources
}

message HeartbeatDelta {
  // Empty — just updates LastSeen
}
```

### Sequence Numbers

Each agent maintains a monotonic sequence counter (persisted in LMDB). The server tracks the last seen sequence per thrall. On reconnect, the agent replays deltas from `last_acked_sequence + 1`. This provides exactly-once delivery semantics without complex distributed state.

### Backward Compatibility

The existing `ReportResult` RPC continues to work for older agents. The server detects whether an agent supports delta streaming (capability flag in the initial stream handshake) and falls back to the current full-report path if not.

### File Changes

| File | Change |
|---|---|
| `proto/hobnob.proto` | Add DeltaEvent, ComplianceDelta, TraitsDelta, RunCompleted messages |
| `agent-rs/src/reporter.rs` | Send deltas instead of full results |
| `agent-rs/src/state/deltas.rs` | Sequence tracking, pending queue |
| `server/grpc/agentstream.go` | Handle delta events, feed FleetIndex |
| `server/grpc/streams.go` | Delta acknowledgment (ack sequence back to agent) |

## Migration Path

### Phase ordering

Phases are independent enough to ship incrementally:

1. **Phase 2 (FleetIndex)** — Ship first. Biggest impact on query performance and server scalability. No agent changes required. Rehydrates from existing SQLite data.
2. **Phase 3 (Async cold store)** — Ship with Phase 2. Moves remaining writes off the hot path.
3. **Phase 1 (Agent LMDB)** — Ship next. Enables offline convergence and local state.
4. **Phase 4 (Delta streaming)** — Ship last. Connects agent local state to server FleetIndex. Requires both sides to be ready.

### Rollback

Each phase is independently reversible:
- Phase 2: Remove FleetIndex, re-enable direct SQLite queries
- Phase 3: Restore synchronous batch writers
- Phase 1: Agent ignores LMDB if stores don't exist, falls back to stateless behavior
- Phase 4: Fall back to full ReportResult RPC (backward compat built in)

## Memory Budget (10,000 thralls)

| Component | Estimated Size |
|---|---|
| `ThrallState` (10k entries) | ~20MB |
| `pubkeys` (10k ed25519 keys) | ~1MB |
| `byHostname` index | ~2MB |
| `byStatus` indexes | <1MB |
| `byTrait` inverted index (50 traits avg) | ~30MB |
| `recentRuns` ring buffer (1000 entries) | ~5MB |
| Policy cache (existing) | ~10MB |
| **Total** | **~70MB** |

Fits comfortably in a 512MB container. Negligible compared to the current JVM-based alternatives.

## Performance Targets

| Operation | Current | Target |
|---|---|---|
| Check-in (no change) | ~100us | <50us |
| Check-in (policy delivery) | ~1ms | ~500us |
| Compliance summary query | ~5ms | <1us |
| "Which thralls are failing?" | ~10ms | <10us |
| Inventory by trait | ~20ms | <10us |
| Run result persistence | ~5ms (sync) | 0ms hot path (async batch) |
| Server startup (10k thralls) | N/A | <3s rehydration |
| Agent offline convergence | Not possible | Same as online convergence |

## What Does Not Change

- gRPC over mTLS transport
- YAML config with fsnotify hot-reload
- Policy cache (per-pattern bundles) — already in memory
- Federation hub/spoke topology
- SQLite as the sole persistent server-side store
- Config loader, secret resolution, DAG ordering
- All existing CLI and API contracts
