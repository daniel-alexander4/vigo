
# Performance Analysis (Theoretical): 30-Second Check-ins on 4 vCPU / 8 GB / SSD

These projections are based on benchmarked per-request costs from the codebase, not measured end-to-end under production load. Actual performance will vary with hardware, network conditions, policy complexity, and fleet composition.

This document analyzes Vigo server performance under 30-second check-in intervals on modest hardware. 30-second intervals are the recommended setting for fleets where responsiveness matters — drift correction within 30 seconds, stale detection within 90 seconds, and a dashboard that feels alive.

All numbers reference benchmarked values from the codebase or are derived from measured operations. See [performance-1m.md](performance-1m.md) for the per-request cost derivations.

## Hardware Assumptions

- 4 vCPU (modern x86_64, e.g., Hetzner CPX21, DigitalOcean c-4)
- 8 GB RAM
- SSD storage (NVMe or SATA SSD, ~50k random IOPS)
- 1 Gbps network

## Responsiveness Profile

| Metric | Value |
|--------|-------|
| Drift correction latency | 15-30 seconds (avg half-interval) |
| Stale detection (3x interval) | 90 seconds |
| Config publish to full fleet convergence | ~30 seconds |
| New envoy visible on dashboard | ~30 seconds |
| Force-push effect | Within next check-in (~15s avg) |
| Dashboard compliance counter freshness | Updates every few seconds |

This feels like a live system. An operator pushes config and sees envoys converge before they can switch browser tabs.

## Request Rates

With 10% jitter (default), agents spread check-ins across a 27-33 second window (30s +/- 10%):

| Fleet Size | Avg RPS | Peak RPS (burst) | Status |
|------------|---------|-------------------|--------|
| 100 | 3.3 | ~5 | Trivial |
| 500 | 17 | ~25 | Trivial |
| 1,000 | 33 | ~50 | Trivial |
| 5,000 | 167 | ~250 | Comfortable |
| 10,000 | 333 | ~500 | Comfortable |
| 15,000 | 500 | ~750 | Tuning recommended |
| 25,000 | 833 | ~1,200 | Spanner or upgrade recommended |

## CPU Usage

Realistic per-check-in CPU cost including gRPC framing, protobuf deserialization, goroutine scheduling, and TLS record layer: **200-300 us** (no-change) and **500-800 us** (full bundle).

| Fleet Size | Avg RPS | CPU (sustained no-change) | CPU (config change storm) |
|------------|---------|---------------------------|---------------------------|
| 1,000 | 33 | ~1% of 1 core | ~2% of 1 core |
| 5,000 | 167 | ~5% of 1 core | ~12% of 1 core |
| 10,000 | 333 | ~10% of 1 core | ~24% of 1 core |
| 15,000 | 500 | ~15% of 1 core | ~36% of 1 core |
| 25,000 | 833 | ~25% of 1 core | ~60% of 1 core |

With 4 cores available, CPU is not a concern up to 25,000 envoys. The theoretical single-core limit is ~3,000-5,000 no-change check-ins/sec; with 4 cores, ~12,000-20,000 RPS before saturation.

**Config change storm**: After `vigogo config publish`, all envoys receive a full bundle on their next check-in. At 10,000 envoys, this means ~333 full-bundle responses/sec sustained over ~30 seconds. CPU impact: ~24% of one core for 30 seconds. Noticeable but well within headroom.

## Memory Usage

Memory is dominated by gRPC connection buffers (64 KiB per persistent mTLS connection), not by the FleetIndex or check-in frequency. The interval does not change per-connection memory — only the number of concurrent connections matters.

| Component | 1,000 envoys | 5,000 envoys | 10,000 envoys | 25,000 envoys |
|-----------|---------------|---------------|----------------|----------------|
| Go runtime + gRPC server | ~150 MiB | ~180 MiB | ~200 MiB | ~300 MiB |
| FleetIndex (state + indexes) | 1.6 MiB | 8 MiB | 16 MiB | 39 MiB |
| Policy cache | <1 MiB | <1 MiB | <1 MiB | <1 MiB |
| gRPC connection buffers | 64 MiB | 320 MiB | 640 MiB | 1.6 GiB |
| Goroutine stacks | <1 MiB | <1 MiB | ~1.5 MiB | ~4 MiB |
| SQLite page cache | 100-300 MiB | 100-300 MiB | 100-500 MiB | 200-500 MiB |
| **Total** | **~350 MiB** | **~850 MiB** | **~1.4 GiB** | **~2.6 GiB** |

At 10,000 envoys, memory sits at ~1.4 GiB of 8 GB — comfortable with 5.6 GiB headroom. The constraint arrives around 25,000 envoys where gRPC buffers alone consume 1.6 GiB.

## Network Bandwidth

Request sizes: ~3 KiB check-in request (includes traits JSON + signature), ~100 bytes no-change response, ~30 KiB full bundle (with stub optimization).

| Fleet Size | Steady-state (bidirectional) | Config change burst (30s) |
|------------|------------------------------|---------------------------|
| 1,000 | ~100 KB/s | ~1 MB/s |
| 5,000 | ~500 KB/s | ~5 MB/s |
| 10,000 | ~1 MB/s | ~10 MB/s |
| 25,000 | ~2.5 MB/s | ~25 MB/s |

Steady-state includes both request (3 KiB) and response (100 bytes) traffic. Even at 25,000 envoys, sustained bandwidth is 2.5 MB/s — 2% of a 1 Gbps link.

## SQLite Write Load

The flusher runs every 10 seconds. At 30-second check-in intervals, every envoy checks in ~3 times per flush window, but the flusher only writes the latest state — so the number of dirty envoys per flush equals the fleet size (same as 1-minute intervals).

| Fleet Size | Dirty envoys per flush | Batch transactions | SQLite write time |
|------------|-------------------------|--------------------|-------------------|
| 1,000 | ~1,000 | 2 | ~5 ms |
| 5,000 | ~5,000 | 10 | ~25 ms |
| 10,000 | ~10,000 | 20 | ~50 ms |
| 25,000 | ~25,000 | 50 | ~125 ms |

All well within the 10-second flush window. WAL mode ensures check-in handlers are never blocked by writes.

**Traits writes**: At 30-second intervals, agents send traits twice as often as at 1-minute intervals — but the hash-dedup still filters 95%+ of check-ins (traits don't change between 30-second cycles). No meaningful increase in traits write volume.

## Comparison to 1-Minute and 5-Minute Intervals

| Metric (10k fleet) | 5-min (default) | 1-min | 30-sec |
|---------------------|-----------------|-------|--------|
| Avg RPS | 33 | 167 | 333 |
| Drift correction (avg) | 2.5 min | 30s | 15s |
| Stale detection | 15 min | 3 min | 90s |
| CPU (sustained) | ~1% | ~5% | ~10% |
| Network (steady-state) | ~100 KB/s | ~500 KB/s | ~1 MB/s |
| Memory | ~1.4 GiB | ~1.4 GiB | ~1.4 GiB |
| SQLite flush | ~50 ms | ~50 ms | ~50 ms |

Memory and SQLite load are identical across intervals because they scale by fleet size, not check-in frequency. CPU and network scale linearly with frequency.

## Capacity Recommendations

### No Tuning: Up to 10,000 Envoys

Default settings handle 10,000 envoys at 30-second check-ins:
- ~10% of one core (sustained), ~24% on config change burst
- ~1.4 GiB memory
- ~1 MB/s network
- 50 ms SQLite flush every 10 seconds

### With Tuning: 10,000-15,000 Envoys

```yaml
checkin:
  interval: "30s"
  jitter_percent: 10
tuning:
  gogc: 200
  memory_limit: "6GiB"
  grpc_read_buffer: 16384
  grpc_write_buffer: 16384
```

Reduces gRPC buffer memory by half (320 MiB at 10k instead of 640 MiB). Comfortable up to 15,000 envoys on 8 GB.

### Beyond 15,000 Envoys

Options:
1. **Upgrade to 8 vCPU / 16 GB** — handles ~30,000 envoys at 30s intervals with tuning
2. **Spanner (hub-spoke)** — shard fleet across spoke servers, each handling its own subset

### Streaming vs. Polling at 30 Seconds

At 30-second polling, the latency for ad-hoc pushes is "up to 30 seconds" (average 15 seconds). For most operational workflows, this is fast enough. Streaming is only worth enabling if you need sub-second push dispatch or if the fleet runs frequent live queries.

## Bottleneck Hierarchy (30-Second Intervals)

1. **Memory (gRPC buffers)** — 640 MiB at 10k connections. First hard limit at ~20k envoys on 8 GB (with tuned 16 KiB buffers, ~25k).
2. **CPU (ED25519 verify)** — 10% of one core at 10k envoys. Saturates all 4 cores at ~40k-60k envoys.
3. **SQLite flusher** — 125 ms at 25k envoys. Not a concern until ~80k+ envoys per flush window.
4. **Network** — 2.5 MB/s at 25k envoys. Under 3% of 1 Gbps.
