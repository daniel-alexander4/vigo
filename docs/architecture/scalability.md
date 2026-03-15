# Theoretical Scalability Analysis

This document estimates Hobnob's capacity limits based on data structure sizes, measured benchmarks, and known Go/SQLite/gRPC performance characteristics. No production deployments exist yet — all figures are theoretical.

## Architecture Summary

One server process (`hobd`) handles the entire fleet. No compile masters, worker pools, or database clusters. The FleetIndex holds all real-time state in memory. SQLite (WAL mode) stores persistent state. Check-ins execute with zero database queries.

## Memory Footprint

### Per-Thrall Cost

| Component | Size | Notes |
|-----------|------|-------|
| ThrallState struct | ~272 bytes | UUID, hostname, IP, pubkey, timestamps, status, traits index |
| byHostname map entry | ~20 bytes | hostname → ID mapping |
| pubkeys map entry | ~64 bytes | ED25519 public key |
| byStatus set entry | ~8 bytes | status → ID set membership |
| byTrait index entries | ~24 bytes | per unique trait value (3 traits avg) |
| **Total per thrall** | **~388 bytes** | |

### Fixed Overhead

| Component | Size | Notes |
|-----------|------|-------|
| recentRuns ring buffer | 480 KB | 5,000 entries × 96 bytes (fixed capacity) |
| complianceHistory ring | 3 KB | 30 daily snapshots (fixed) |
| ComplianceCounters | 48 bytes | 6 pre-computed integers |
| Policy cache | varies | one entry per match pattern, not per thrall |

### Fleet Size Projections

| Thralls | FleetIndex RAM | Notes |
|---------|---------------|-------|
| 1,000 | ~1 MB | Trivial |
| 10,000 | ~4.5 MB | Fits in L3 cache on modern CPUs |
| 100,000 | ~40 MB | Comfortable on any server |
| 500,000 | ~195 MB | Mid-range VPS |
| 1,000,000 | ~390 MB | Requires Go runtime tuning (`gogc`, `memory_limit`) |

FleetIndex memory scales linearly. There is no per-thrall database table scan, no per-thrall cache miss, and no per-thrall allocation on the check-in path.

## Check-in Performance

### Measured Benchmarks (10,000 thralls, single core)

| Operation | Latency | Throughput |
|-----------|---------|------------|
| GetThrallLite (shallow copy) | 287 ns | 3.81M ops/sec |
| GetThrall (deep copy) | 620 ns | 1.91M ops/sec |
| GetPubKey (map lookup) | 136 ns | 8.45M ops/sec |
| MarkSeen (dirty flag) | 174 ns | 6.91M ops/sec |
| ComplianceSummary | 14 ns | 83.7M ops/sec |
| ListByTrait (inverted index) | 148 µs | 8,101 ops/sec |
| ListByGlob (full scan) | 2.94 ms | 404 ops/sec |

### Per-Check-in Cost

| Step | Cost | Notes |
|------|------|-------|
| ED25519 signature verify | ~10 µs | Crypto operation, unavoidable |
| Timestamp validation | < 1 ns | Simple comparison |
| FleetIndex lookup | ~287 ns | GetThrallLite, single RLock |
| Policy cache hit | ~1-2 µs | Pre-built bundle, shallow copy |
| Policy cache miss | 10-50 ms | Full role → module → resource resolution + template render |
| MarkSeen (update last_seen) | ~174 ns | In-memory, async flush to DB |
| **Total (cache hit)** | **~12 µs** | |
| **Total (cache miss)** | **~10-50 ms** | First check-in per pattern after config change |

### Throughput Estimates

With a 12 µs per-check-in cost on cache hit:

| Cores | Check-ins/sec | Thralls at 5-min interval |
|-------|---------------|--------------------------|
| 1 | ~83,000 | 25,000,000 |
| 4 | ~250,000 | 75,000,000 |
| 8 | ~400,000 | 120,000,000 |

The check-in RPC is CPU-bound on signature verification (10 µs). At a 5-minute interval with 20% jitter, each thrall checks in once per 240-360 seconds. A single core can theoretically serve 25 million thralls at steady state. In practice, GC pauses, network I/O, and config compilation reduce this significantly.

**Realistic estimate:** 100,000-500,000 thralls on a single 4-core server with comfortable headroom.

## Database Write Capacity

### Async Batched Writes

All database writes are deferred and batched. Nothing writes synchronously on the check-in hot path.

| Writer | Interval | Batch Size | Operation |
|--------|----------|------------|-----------|
| StateFlusher | 10s | 500 IDs/batch | UPDATE last_seen, clear flags |
| TraitsBatcher | 10s (offset) | 200 rows/batch | INSERT traits (deduplicated by SHA-256) |
| RunBatcher | 10s (offset) | per-run | INSERT run + results |

The three writers are staggered by `flush_interval/3` to avoid SQLite lock contention.

### SQLite WAL Throughput

SQLite in WAL mode handles approximately:
- 10,000-50,000 simple INSERTs/sec on SSD
- 5,000-20,000 UPDATE-by-PK/sec on SSD

At 10,000 thralls checking in every 5 minutes:
- ~33 check-ins/sec → 33 MarkSeen/sec (in-memory)
- Every 10s flush: ~330 dirty thralls batched into 1 UPDATE statement
- Traits: ~100 changed traits (30% rate) batched into 1 multi-row INSERT
- Runs: ~330 runs written individually

**SQLite bottleneck:** At ~50,000+ thralls, flush cycles exceed 1 second. At 100,000+, consider PostgreSQL or increasing the flush interval.

## Network Bandwidth

### Per-Check-in Payload

| Direction | Size | Contents |
|-----------|------|----------|
| Request | ~2-5 KB | UUID, traits JSON, fingerprint, ED25519 signature |
| Response (no change) | ~100 bytes | no_change flag |
| Response (with modules) | 5-50 KB | Policy bundle (varies by config complexity) |
| Result report | 1-10 KB | Per-resource outcomes |

### Aggregate Bandwidth

| Thralls | Check-ins/sec | Inbound | Outbound (steady state) |
|---------|---------------|---------|------------------------|
| 10,000 | 33/sec | 100 KB/s | 10 KB/s (mostly no-change) |
| 100,000 | 333/sec | 1 MB/s | 100 KB/s |
| 1,000,000 | 3,333/sec | 10 MB/s | 1 MB/s |

Network is not the bottleneck. Even at 1 million thralls, aggregate bandwidth is under 15 MB/s.

## gRPC Connection Limits

| Parameter | Default | Tunable |
|-----------|---------|---------|
| Max concurrent streams | 5,000 | `tuning.max_concurrent_streams` |
| Max connection idle | 15 min | `tuning.max_connection_idle` |
| Keepalive interval | 30s | `tuning.keepalive_time` |
| Read/write buffers | 32 KB each | `tuning.grpc_read_buffer`, `grpc_write_buffer` |

Each active gRPC connection consumes ~230 KB of memory (TLS state + buffers). Adaptive stream promotion eliminates this for idle agents — only agents with active tasks/queries/workflows hold persistent connections.

| Scenario | Concurrent connections | Memory |
|----------|----------------------|--------|
| 10,000 thralls, all polling | 0 persistent | 0 (stateless unary RPCs) |
| 10,000 thralls, 100 with tasks | 100 streams | 23 MB |
| 100,000 thralls, 500 with tasks | 500 streams | 115 MB |

## Bottleneck Order

What breaks first as fleet size grows:

| # | Bottleneck | Threshold | Mitigation |
|---|-----------|-----------|------------|
| 1 | **SQLite flush latency** | ~50,000 thralls | Switch to PostgreSQL, or increase flush interval |
| 2 | **Go GC pauses** | ~200,000 thralls | Tune `gogc` (50-100) and `memory_limit` |
| 3 | **FleetIndex memory** | ~500,000 thralls | 195 MB; ensure server has 1+ GB free |
| 4 | **gRPC concurrent streams** | 5,000 simultaneous | Increase `max_concurrent_streams`; only affects task dispatch |
| 5 | **Policy cache miss storms** | After config publish | Cache rebuilds are 10-50 ms per pattern; stagger agent check-ins with jitter |
| 6 | **ListByGlob (full scan)** | N/A at any size | Only used by admin queries, not check-in path; O(N) but infrequent |

## Tuning Recommendations by Fleet Size

### Up to 10,000 thralls
No tuning needed. Default settings work.

### 10,000 - 100,000 thralls
```yaml
tuning:
  last_seen_flush: "10s"
  max_concurrent_streams: 10000
```

### 100,000 - 500,000 thralls
```yaml
tuning:
  gogc: 50
  memory_limit: "4GiB"
  last_seen_flush: "30s"
  max_concurrent_streams: 20000
  grpc_read_buffer: 16384
  grpc_write_buffer: 16384
database:
  driver: postgres
```

### 500,000+ thralls
```yaml
tuning:
  gogc: 30
  memory_limit: "8GiB"
  last_seen_flush: "60s"
  max_concurrent_streams: 50000
database:
  driver: postgres
checkin:
  interval: "10m"
  jitter_percent: 30
```

At this scale, consider federation (hub-spoke) to distribute load across multiple servers.

## Comparison

| Metric | Hobnob | Puppet Enterprise |
|--------|--------|-------------------|
| Server processes | 1 | 3-5 (PuppetServer, PuppetDB, PostgreSQL, ActiveMQ, orchestrator) |
| Server RAM at 10K nodes | ~50 MB | ~8-16 GB (JVM heap × 3) |
| Check-in DB queries | 0 | 5-10 per catalog compile |
| Agent binary size | 3.9 MB | ~150 MB (Ruby runtime) |
| Infrastructure at 50K nodes | 1 server | 4-6 compile masters + HA PuppetDB + load balancer |
