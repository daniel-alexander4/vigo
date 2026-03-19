# Theoretical Scalability Analysis

This document estimates Vigo's capacity limits based on data structure sizes, measured benchmarks, and known Go/SQLite/gRPC performance characteristics. No production deployments exist yet — all figures are theoretical.

## Architecture Summary

One server process (`vigosrv`) handles the entire fleet. No compile masters, worker pools, or database clusters. The FleetIndex holds all real-time state in memory. SQLite (WAL mode) stores persistent state. Check-ins execute with zero database queries.

## Memory Footprint

### Per-Envoy Cost

| Component | Size | Notes |
|-----------|------|-------|
| EnvoyState struct | ~272 bytes | UUID, hostname, IP, pubkey, timestamps, status, traits index |
| byHostname map entry | ~20 bytes | hostname → ID mapping |
| pubkeys map entry | ~64 bytes | ED25519 public key |
| byStatus set entry | ~8 bytes | status → ID set membership |
| byTrait index entries | ~24 bytes | per unique trait value (3 traits avg) |
| **Total per envoy** | **~388 bytes** | |

### Fixed Overhead

| Component | Size | Notes |
|-----------|------|-------|
| recentRuns ring buffer | 480 KB | 5,000 entries × 96 bytes (fixed capacity) |
| complianceHistory ring | 3 KB | 30 daily snapshots (fixed) |
| ComplianceCounters | 48 bytes | 6 pre-computed integers |
| Policy cache | varies | one entry per match pattern, not per envoy |

### Fleet Size Projections

| Envoys | FleetIndex RAM | Notes |
|---------|---------------|-------|
| 1,000 | ~1 MB | Trivial |
| 10,000 | ~4.5 MB | Fits in L3 cache on modern CPUs |
| 100,000 | ~40 MB | Comfortable on any server |
| 500,000 | ~195 MB | Mid-range VPS |
| 1,000,000 | ~390 MB | Requires Go runtime tuning (`gogc`, `memory_limit`) |

FleetIndex memory scales linearly. There is no per-envoy database table scan, no per-envoy cache miss, and no per-envoy allocation on the check-in path.

## Check-in Performance

### Measured Benchmarks (10,000 envoys, single core)

| Operation | Latency | Throughput |
|-----------|---------|------------|
| GetEnvoyLite (shallow copy) | 287 ns | 3.81M ops/sec |
| GetEnvoy (deep copy) | 620 ns | 1.91M ops/sec |
| GetPubKey (map lookup) | 136 ns | 8.45M ops/sec |
| MarkSeen (dirty flag) | 174 ns | 6.91M ops/sec |
| ComplianceSummary | 14 ns | 83.7M ops/sec |
| ListByTrait (inverted index) | 148 µs | 8,101 ops/sec |
| ListByGlob (full scan) | 2.94 ms | 404 ops/sec |

### Per-Check-in Cost

**In-process hot path only** (what the benchmarks measure):

| Step | Cost | Notes |
|------|------|-------|
| ED25519 signature verify | ~10 µs | Crypto operation, unavoidable |
| Timestamp validation | < 1 ns | Simple comparison |
| FleetIndex lookup | ~283 ns | GetEnvoyLite, single RLock |
| Policy cache hit | ~1-2 µs | Pre-built bundle, shallow copy |
| Policy cache miss | 10-50 ms | Full role → module → resource resolution + template render |
| MarkSeen (update last_seen) | ~170 ns | In-memory, async flush to DB |
| **Hot path subtotal** | **~12 µs** | |

**End-to-end cost** (what actually happens per gRPC call):

| Step | Cost | Notes |
|------|------|-------|
| TLS decrypt + gRPC framing | ~50-100 µs | rustls/Go TLS handshake amortized, per-message decrypt |
| Proto unmarshal (request) | ~5-10 µs | CheckInRequest deserialization |
| Hot path (above) | ~12 µs | FleetIndex + policy cache + signature |
| Proto marshal (response) | ~10-50 µs | PolicyBundle serialization (varies by config size) |
| TLS encrypt + socket write | ~50-100 µs | Response encryption + kernel buffer |
| Go scheduler + GC overhead | ~10-50 µs | Goroutine scheduling, GC assist (amortized) |
| **Total (cache hit)** | **~200-400 µs** | |
| **Total (cache miss)** | **~10-50 ms** | First check-in per pattern after config change |

### Throughput Estimates

With ~300 µs realistic per-check-in cost on cache hit:

| Cores | Check-ins/sec | Envoys at 5-min interval |
|-------|---------------|--------------------------|
| 1 | ~3,300 | 1,000,000 |
| 4 | ~10,000 | 3,000,000 |
| 8 | ~16,000 | 4,800,000 |

Each envoy checks in once per 240-360 seconds (5-min interval with 20% jitter). A 4-core server processing 10,000 check-ins/sec can serve ~3 million envoys at steady state. RWMutex contention under load, GC pauses, and network I/O reduce throughput further at scale.

**Conservative realistic estimate:** 50,000-200,000 envoys on a single 4-core server with comfortable headroom. Beyond 200,000, consider spanner (hub-spoke) or PostgreSQL for the write path.

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

At 10,000 envoys checking in every 5 minutes:
- ~33 check-ins/sec → 33 MarkSeen/sec (in-memory)
- Every 10s flush: ~330 dirty envoys batched into 1 UPDATE statement
- Traits: ~100 changed traits (30% rate) batched into 1 multi-row INSERT
- Runs: ~330 runs written individually

**SQLite bottleneck:** At ~50,000+ envoys, flush cycles exceed 1 second. At 100,000+, consider PostgreSQL or increasing the flush interval.

## Network Bandwidth

### Per-Check-in Payload

| Direction | Size | Contents |
|-----------|------|----------|
| Request | ~2-5 KB | UUID, traits JSON, fingerprint, ED25519 signature |
| Response (no change) | ~100 bytes | no_change flag |
| Response (with modules) | 5-50 KB | Policy bundle (varies by config complexity) |
| Result report | 1-10 KB | Per-resource outcomes |

### Aggregate Bandwidth

| Envoys | Check-ins/sec | Inbound | Outbound (steady state) |
|---------|---------------|---------|------------------------|
| 10,000 | 33/sec | 100 KB/s | 10 KB/s (mostly no-change) |
| 100,000 | 333/sec | 1 MB/s | 100 KB/s |
| 1,000,000 | 3,333/sec | 10 MB/s | 1 MB/s |

Network is not the bottleneck. Even at 1 million envoys, aggregate bandwidth is under 15 MB/s.

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
| 10,000 envoys, all polling | 0 persistent | 0 (stateless unary RPCs) |
| 10,000 envoys, 100 with tasks | 100 streams | 23 MB |
| 100,000 envoys, 500 with tasks | 500 streams | 115 MB |

## Bottleneck Order

What breaks first as fleet size grows:

| # | Bottleneck | Threshold | Mitigation |
|---|-----------|-----------|------------|
| 1 | **SQLite flush latency** | ~50,000 envoys | Switch to PostgreSQL, or increase flush interval |
| 2 | **Go GC pauses** | ~200,000 envoys | Tune `gogc` (50-100) and `memory_limit` |
| 3 | **FleetIndex memory** | ~500,000 envoys | 195 MB; ensure server has 1+ GB free |
| 4 | **gRPC concurrent streams** | 5,000 simultaneous | Increase `max_concurrent_streams`; only affects task dispatch |
| 5 | **Policy cache miss storms** | After config publish | Cache rebuilds are 10-50 ms per pattern; stagger agent check-ins with jitter |
| 6 | **ListByGlob (full scan)** | N/A at any size | Only used by admin queries, not check-in path; O(N) but infrequent |

## Tuning Recommendations by Fleet Size

### Up to 10,000 envoys
No tuning needed. Default settings work.

### 10,000 - 100,000 envoys
```yaml
tuning:
  last_seen_flush: "10s"
  max_concurrent_streams: 10000
```

### 100,000 - 500,000 envoys
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

### 500,000+ envoys
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

At this scale, consider spanner (hub-spoke) to distribute load across multiple servers.

## Comparison

| Metric | Vigo | Puppet Enterprise |
|--------|--------|-------------------|
| Server processes | 1 | 3-5 (PuppetServer, PuppetDB, PostgreSQL, ActiveMQ, orchestrator) |
| Server RAM at 10K nodes | ~50 MB | ~8-16 GB (JVM heap × 3) |
| Check-in DB queries | 0 | 5-10 per catalog compile |
| Agent binary size | 3.9 MB | ~150 MB (Ruby runtime) |
| Infrastructure at 50K nodes | 1 server | 4-6 compile masters + HA PuppetDB + load balancer |

## Methodology and Assumptions

### How these numbers were derived

**Memory estimates** are calculated from Go struct sizes in `server/fleet/index.go`. Each `EnvoyState` struct has known fields (strings, time.Time, maps, slices) whose sizes are determined by Go's memory layout. Map overhead uses Go's internal bucket structure (~8 bytes per entry for simple sets, more for string-keyed maps). The per-envoy estimate of ~388 bytes assumes:
- Hostname: ~20 bytes average (e.g., `web01.dc1.example.com`)
- UUID: 36 bytes
- IP: ~15 bytes
- PubkeyPEM: ~100 bytes (base64-encoded ED25519)
- 3 traits indexed per envoy (typical: os.family, os.distro, arch)
- Empty pending rotated secrets and tags lists

Fleet size projections multiply per-envoy cost linearly. Go map overhead grows sub-linearly due to bucket amortization, so actual memory at 1M envoys may be 10-15% less than the linear projection.

**Benchmark numbers** are from `go test -bench` on a 16-thread AMD Ryzen 7 5700U (2026-03-14). The FleetIndex benchmarks populate 10,000 envoys before measuring. Each benchmark runs until the Go test framework achieves statistical stability (minimum 1 second of iterations). Numbers represent **uncontended single-goroutine performance** — real-world throughput with concurrent access and RWMutex contention will be lower.

Specific benchmark commands:
```
go test ./server/fleet/... -bench=. -benchmem -count=1
go test ./server/grpc/... -bench=. -benchmem -count=1
```

**Per-check-in cost** splits into two parts:
- *In-process hot path* (~12 µs): sum of measured benchmark operations that execute per check-in (signature verify + FleetIndex lookup + policy cache hit + MarkSeen). This is what the code does, measured in isolation.
- *End-to-end overhead* (~200-400 µs): estimated from published gRPC/TLS performance characteristics. TLS record decrypt/encrypt is typically 30-80 µs per message on modern CPUs. Proto marshal/unmarshal scales with message size (CheckInRequest ~2 KB ≈ 5-10 µs, PolicyBundle 5-50 KB ≈ 10-50 µs). Go scheduler overhead is estimated at 10-50 µs per goroutine based on published Go runtime benchmarks.

**Throughput projections** divide 1 second by the per-check-in cost, then multiply by core count with a 0.8x scaling factor (accounting for lock contention and shared-resource overhead). The "envoys at 5-min interval" column divides throughput by the check-in rate: at 5-minute intervals with 20% jitter, each envoy checks in once per 240-360 seconds (average 300 seconds).

Example: 4 cores × 3,300 ops/sec × 0.8 = ~10,000 check-ins/sec. At 1 check-in per 300 seconds per envoy: 10,000 × 300 = 3,000,000 envoys theoretical maximum. The conservative estimate (50K-200K) applies a further 15-30x safety factor for GC pauses, cache misses, config compilation, network variability, and the reality that benchmarks never predict production.

**SQLite throughput** is based on published SQLite WAL-mode benchmarks on NVMe SSD. Actual performance varies with disk type, write amplification, and concurrent readers. The "50,000 envoy bottleneck" estimate assumes 10-second flush intervals with 500-ID batches: at 50,000 envoys, each flush writes ~1,600 dirty entries (50,000 / 300 sec × 10 sec) in 3-4 batches, which approaches 1 second of SQLite write lock time.

**Network bandwidth** uses measured proto message sizes from the gRPC wire format. "Mostly no-change" outbound assumes 90% of check-ins return a `no_change` response (~100 bytes) because the policy fingerprint hasn't changed.

**Puppet comparison** figures are from published Puppet Enterprise sizing guides (2024) and community benchmarks. JVM heap estimates assume PuppetServer (2-4 GB), PuppetDB (2-4 GB), and PostgreSQL (2-4 GB shared buffers). Compile master count at 50K nodes is based on Puppet's recommendation of ~4,000 nodes per compile master.

### What these numbers do NOT account for

- **Real network latency.** Benchmarks run in-process or on localhost. WAN latency, packet loss, and TCP congestion between agents and server are not modeled.
- **Config complexity.** Policy cache miss cost (10-50 ms) depends on how many modules, resources, templates, and `when:` expressions a envoy's config contains. A envoy with 500 resources will be slower to compile than one with 10.
- **Trait volume.** The 3-traits-per-envoy assumption is conservative. Envoys reporting 20+ traits (e.g., full package list, all network interfaces) will increase FleetIndex memory and trait flush batch sizes.
- **Concurrent admin queries.** ListByGlob is O(N) and takes 3 ms at 10K envoys. At 1M envoys, a single glob query would take ~300 ms and hold a read lock. Frequent admin queries could compete with check-in reads.
- **GC pause distribution.** Go's GC pauses are typically under 1 ms but can spike under memory pressure. At 200 MB+ FleetIndex, GC scans take longer. The `gogc` and `memory_limit` tuning knobs mitigate this but don't eliminate it.
- **Disk I/O contention.** SQLite flush, Litestream WAL replication, and run result writes all compete for disk I/O. On spinning disks or shared storage, throughput will be significantly lower than SSD benchmarks.
