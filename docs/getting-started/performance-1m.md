
# Performance Analysis (Theoretical): 1-Minute Check-ins on 4 vCPU / 8 GB / SSD

These projections are based on benchmarked per-request costs from the codebase, not measured end-to-end under production load. Actual performance will vary with hardware, network conditions, policy complexity, and fleet composition.

This document analyzes Vigo server performance under 1-minute check-in intervals on modest hardware. All numbers reference benchmarked values from the codebase or are derived from measured operations.

See also: [30-second intervals](performance-30s.md) | [15-second intervals](performance-15s.md) | [5-second intervals](performance-5s.md)

## Hardware Assumptions

- 4 vCPU (modern x86_64, e.g., Hetzner CPX21, DigitalOcean c-4)
- 8 GB RAM
- SSD storage (NVMe or SATA SSD, ~50k random IOPS)
- 1 Gbps network

## Architecture Recap

The check-in hot path is designed for zero synchronous database operations:

- **FleetIndex**: In-memory envoy index with O(1) lookups. Rehydrated from SQLite on startup, dirty state flushed every 10 seconds in 500-row batches.
- **Policy cache**: One cached bundle per match pattern (not per envoy). Cache hit: 17 ns. Miss triggers rebuild at ~3 us.
- **No-change short-circuit**: When policy hasn't changed and the envoy isn't force-pushed, the server returns a 100-byte "nothing changed" response in ~55 us total.
- **Async writes**: Last-seen timestamps, traits, and flag changes are batched and flushed to SQLite every 10 seconds, outside any hot-path lock.

## Check-in Cost Breakdown

### Per-Request Costs (Benchmarked)

| Operation | Cost | Notes |
|-----------|------|-------|
| ED25519 signature verification | 52.5 us | Interceptor, non-negotiable |
| FleetIndex GetEnvoyLite | 0.36 us | RLock, shallow copy |
| FleetIndex GetPubKey | 0.15 us | Pre-parsed key, O(1) map |
| Policy version compare + flags | ~0.5 us | In-memory comparisons |
| TraitsBatcher hash-dedup | ~1 us | SHA-256 of traits JSON |
| MarkSeen (dirty flag) | 0.46 us | Queued for async flush |
| **Total (no-change path)** | **~55 us** | 95%+ of check-ins |

| Operation | Cost | Notes |
|-----------|------|-------|
| Config pattern match (cached) | <1 us | Glob cache hit after first lookup |
| Policy cache hit + stub customize | 0.3 us | RWMutex read lock |
| Policy cache miss (full rebuild) | ~3 us | Protobuf serialization |
| Bundle signing (ED25519) | ~100 us | Only on policy delivery |
| **Total (full bundle path)** | **~155 us** | After config change |

### Costs NOT on the Hot Path

| Operation | Frequency | Cost |
|-----------|-----------|------|
| TLS handshake (mTLS, TLS 1.3) | Once per connection (~every 15 min) | ~1-2 ms |
| SQLite batch UPDATE (last_seen) | Every 10 seconds | ~5-20 ms per 500-row chunk |
| Traits INSERT (changed only) | Every 10 seconds, hash-deduped | ~2-10 ms per 200-row chunk |
| FleetIndex flusher lock hold | Every 10 seconds | <5 ms (snapshot + release) |
| Go GC pause | ~every 2-5 seconds under load | <1 ms (Go 1.25 concurrent GC) |

## Scaling Projections

### Request Rate at 1-Minute Intervals


With 10% jitter (default), agents spread check-ins across a 54-66 second window (60s +/- 10%). This prevents thundering herd:

| Fleet Size | Avg RPS | Peak RPS (burst) | Notes |
|------------|---------|-------------------|-------|
| 100 | 1.7 | ~3 | Trivial |
| 1,000 | 17 | ~25 | Trivial |
| 5,000 | 83 | ~120 | Comfortable |
| 10,000 | 167 | ~250 | Comfortable |
| 25,000 | 417 | ~600 | Tuning recommended |
| 50,000 | 833 | ~1,200 | Streaming or spanner recommended |

### CPU Usage

ED25519 verification dominates at 52.5 us/op. With full handler overhead (~55 us no-change, ~155 us full-bundle), the realistic per-check-in CPU cost including gRPC framing, protobuf deserialization, goroutine scheduling, and TLS record decryption is approximately **200-300 us** for no-change and **500-800 us** for full bundle delivery.

| Fleet Size | Avg RPS | CPU (no-change) | CPU (burst after config change) |
|------------|---------|------------------|---------------------------------|
| 1,000 | 17 | <1% of 1 core | ~1% of 1 core |
| 5,000 | 83 | ~2% of 1 core | ~5% of 1 core |
| 10,000 | 167 | ~5% of 1 core | ~12% of 1 core |
| 25,000 | 417 | ~12% of 1 core | ~30% of 1 core |
| 50,000 | 833 | ~25% of 1 core | ~60% of 1 core |

Go's goroutine scheduler distributes work across all 4 cores. A single core saturates at roughly 3,000-5,000 no-change check-ins/sec or 1,200-2,000 full-bundle check-ins/sec. With 4 cores, theoretical throughput is 12,000-20,000 RPS before CPU saturation.

**Config change storm**: When config is published, all envoys receive a full bundle on their next check-in. For a 10,000-envoy fleet at 1-minute intervals, this means ~167 full-bundle responses per second sustained over ~60 seconds. CPU impact: ~12% of one core. Not a concern.

### Memory Usage

| Component | 1,000 envoys | 10,000 envoys | 50,000 envoys |
|-----------|---------------|----------------|----------------|
| Go runtime + gRPC server | ~150 MiB | ~200 MiB | ~300 MiB |
| FleetIndex (envoy state) | 1.4 MiB | 14 MiB | 70 MiB |
| FleetIndex (inverted indexes) | 0.2 MiB | 1.6 MiB | 8 MiB |
| Policy cache | <1 MiB | <1 MiB | <1 MiB |
| gRPC buffers (32 KiB r+w per conn) | 64 MiB | 640 MiB | 3.2 GiB |
| Goroutine stacks (active handlers) | <1 MiB | ~1.5 MiB | ~8 MiB |
| SQLite page cache | 100-500 MiB | 100-500 MiB | 100-500 MiB |
| **Total estimate** | **~350 MiB** | **~1.4 GiB** | **~4.1 GiB** |

**The gRPC connection buffer is the largest memory consumer**, not the FleetIndex. Each persistent mTLS connection allocates 64 KiB (32 KiB read + 32 KiB write) by default. At 10,000 connections this is 640 MiB.

Mitigation options for 25,000+ envoys:
- Reduce `tuning.grpc_read_buffer` and `tuning.grpc_write_buffer` to 16 KiB (halves connection memory)
- Set `tuning.max_connection_age` to force periodic reconnects, reducing idle connection count
- Use adaptive stream promotion (default) so only active envoys hold streams
- Set `tuning.memory_limit: "6GiB"` to cap Go heap and trigger earlier GC

### Network Bandwidth

| Fleet Size | No-change traffic | Post-config-change burst |
|------------|-------------------|--------------------------|
| 1,000 | ~5 KB/s | ~500 KB/s (30s burst) |
| 5,000 | ~25 KB/s | ~2.5 MB/s (30s burst) |
| 10,000 | ~50 KB/s | ~5 MB/s (30s burst) |
| 50,000 | ~250 KB/s | ~25 MB/s (30s burst) |

Assumes 100-byte no-change response, 30 KiB average full bundle (with stub optimization reducing unchanged modules to ~100 bytes each). Even at 50,000 envoys, network is not the bottleneck on a 1 Gbps link.


### SQLite Write Load

The flusher batches all writes into 10-second windows. Three flushers are staggered at 0s, 3.3s, and 6.6s offsets to avoid lock contention.

| Fleet Size | Dirty envoys per flush | Batch transactions per flush | SQLite write time |
|------------|-------------------------|------------------------------|-------------------|
| 1,000 | ~1,000 | 2 (500/chunk) | ~5 ms |
| 10,000 | ~10,000 | 20 | ~50 ms |
| 50,000 | ~50,000 | 100 | ~250 ms |

At 1-minute intervals, every envoy checks in within each 10-second flush window, so the full fleet is dirty each cycle. WAL mode allows concurrent reads during writes, so check-in handlers are never blocked by the flusher.

**Traits writes** are hash-deduplicated: if a envoy's traits JSON hasn't changed (SHA-256 match), no write occurs. For a stable fleet, 95%+ of check-ins skip the traits write entirely. Only environment changes (new package installed, IP change, etc.) trigger a traits INSERT.

## Capacity Recommendations

### Sweet Spot: Up to 10,000 Envoys

No tuning required. Default settings handle 10,000 envoys at 1-minute check-ins with:
- ~5% CPU utilization (sustained)
- ~1.4 GiB memory
- ~50 KB/s network
- 50 ms SQLite flush every 10 seconds

### Comfortable: 10,000-25,000 Envoys

Add these tuning options to `server.yaml`:

```yaml
tuning:
  gogc: 200
  memory_limit: "6GiB"
  grpc_read_buffer: 16384
  grpc_write_buffer: 16384
```

This reduces gRPC buffer memory by half and gives Go more heap room before GC kicks in.

### Scaling Beyond: 25,000+ Envoys

Two options:

1. **Upgrade to 8 vCPU / 16 GB** and apply tuning above. Handles ~50,000 envoys.
2. **Use spanner (hub-spoke)** to shard the fleet across multiple spoke servers. Each spoke handles its own subset of envoys with independent FleetIndex and SQLite database. The hub aggregates compliance, routes enrollment, and fans out queries.

### When to Use Streaming Instead of Polling

Adaptive stream promotion is already the default: agents poll via unary `CheckIn()` and only open persistent streams when the server has dispatched work (tasks, queries, workflows). For pure state enforcement (no orchestration), polling at 1-minute intervals is optimal — it avoids the 64 KiB per-connection memory overhead of idle streams.

If the fleet runs frequent ad-hoc tasks or live queries, streaming reduces latency from "up to 1 minute" (next poll) to instant dispatch.

## Bottleneck Hierarchy

From most to least likely to saturate first on 4 vCPU / 8 GB:

1. **Memory (gRPC connection buffers)** — 640 MiB at 10k connections. First constraint hit at ~25k envoys on 8 GB.
2. **CPU (ED25519 verification)** — 52.5 us per check-in. Saturates one core at ~19k RPS. With 4 cores, theoretical limit ~76k RPS.
3. **SQLite flusher** — 250 ms per flush at 50k envoys. Still well within the 10-second window.
4. **Network** — 25 MB/s burst at 50k envoys after config change. Under 3% of 1 Gbps.
5. **Config pattern matching** — Cached after first lookup. Negligible.

## Key Design Properties

- **Zero synchronous DB ops on check-in**: FleetIndex serves all reads; writes are async.
- **Policy cache scales by pattern count, not fleet size**: 100 match patterns = 100 cache entries, regardless of whether 10 or 10,000 envoys match each one.
- **No-change short-circuit**: Dominates idle fleet behavior (95%+ of cycles), returning in ~55 us with a 100-byte response.
- **Jitter prevents thundering herd**: Default 10% jitter spreads 1-minute check-ins across a 12-second window. No synchronization between agents.
- **Flusher lock is non-blocking**: Snapshots dirty state in <5 ms, then performs DB writes outside the lock. Check-in handlers never wait on SQLite.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
