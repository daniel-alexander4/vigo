# Performance Analysis (Theoretical): 15-Second Check-ins on 4 vCPU / 8 GB / SSD

These projections are based on benchmarked per-request costs from the codebase, not measured end-to-end under production load. Actual performance will vary with hardware, network conditions, policy complexity, and fleet composition.

This document analyzes Hobnob server performance under 15-second check-in intervals on modest hardware. 15-second intervals provide near-real-time responsiveness — drift corrected in under 15 seconds, stale machines flagged within 45 seconds. This is the fastest practical polling interval before streaming becomes a better choice.

All numbers reference benchmarked values from the codebase or are derived from measured operations. See [performance-1m.md](performance-1m.md) for the per-request cost derivations.

## Hardware Assumptions

- 4 vCPU (modern x86_64, e.g., Hetzner CPX21, DigitalOcean c-4)
- 8 GB RAM
- SSD storage (NVMe or SATA SSD, ~50k random IOPS)
- 1 Gbps network

## Responsiveness Profile

| Metric | Value |
|--------|-------|
| Drift correction latency | 7-15 seconds (avg half-interval) |
| Stale detection (3x interval) | 45 seconds |
| Config publish to full fleet convergence | ~15 seconds |
| New thrall visible on dashboard | ~15 seconds |
| Force-push effect | Within next check-in (~7s avg) |
| Dashboard compliance counter freshness | Near real-time |

This feels like watching a live terminal. Configuration changes propagate before you can alt-tab. A crashed machine is flagged stale in under a minute.

## Request Rates

With 10% jitter (default), agents spread check-ins across a 13.5-16.5 second window (15s +/- 10%):

| Fleet Size | Avg RPS | Peak RPS (burst) | Status |
|------------|---------|-------------------|--------|
| 100 | 6.7 | ~10 | Trivial |
| 500 | 33 | ~50 | Trivial |
| 1,000 | 67 | ~100 | Trivial |
| 2,000 | 133 | ~200 | Comfortable |
| 5,000 | 333 | ~500 | Comfortable |
| 7,500 | 500 | ~750 | Comfortable with tuning |
| 10,000 | 667 | ~1,000 | Tuning required |
| 15,000 | 1,000 | ~1,500 | Upgrade or spanner recommended |

## CPU Usage

Realistic per-check-in CPU cost including gRPC framing, protobuf deserialization, goroutine scheduling, and TLS record layer: **200-300 us** (no-change) and **500-800 us** (full bundle).

| Fleet Size | Avg RPS | CPU (sustained no-change) | CPU (config change storm) |
|------------|---------|---------------------------|---------------------------|
| 1,000 | 67 | ~2% of 1 core | ~4% of 1 core |
| 2,000 | 133 | ~4% of 1 core | ~9% of 1 core |
| 5,000 | 333 | ~10% of 1 core | ~24% of 1 core |
| 7,500 | 500 | ~15% of 1 core | ~36% of 1 core |
| 10,000 | 667 | ~20% of 1 core | ~48% of 1 core |
| 15,000 | 1,000 | ~30% of 1 core | ~72% of 1 core |

With 4 cores, CPU is not the binding constraint. Even at 10,000 thralls, sustained load is 20% of one core (~5% total CPU). Config change storms are more visible — 48% of one core for ~15 seconds at 10k — but transient and well within headroom.

**Config change storm**: After publish, all 10,000 thralls converge within one 15-second window. That's 667 full-bundle responses/sec for ~15 seconds. CPU spikes to ~48% of one core, then drops back to baseline. The burst is shorter and sharper than at longer intervals, but the total work is identical (same number of bundles, just compressed into a shorter window).

## Memory Usage

Memory is unchanged from longer intervals — it scales by fleet size (connection count), not check-in frequency.

| Component | 1,000 thralls | 5,000 thralls | 10,000 thralls |
|-----------|---------------|---------------|----------------|
| Go runtime + gRPC server | ~150 MiB | ~180 MiB | ~200 MiB |
| FleetIndex (state + indexes) | 1.6 MiB | 8 MiB | 16 MiB |
| Policy cache | <1 MiB | <1 MiB | <1 MiB |
| gRPC connection buffers | 64 MiB | 320 MiB | 640 MiB |
| Goroutine stacks | <1 MiB | <1 MiB | ~2 MiB |
| SQLite page cache | 100-300 MiB | 100-300 MiB | 100-500 MiB |
| **Total** | **~350 MiB** | **~850 MiB** | **~1.4 GiB** |

The story is the same: gRPC connection buffers dominate, and 10,000 thralls fits comfortably in 8 GB.

## Network Bandwidth

Request sizes: ~3 KiB check-in request, ~100 bytes no-change response, ~30 KiB full bundle.

| Fleet Size | Steady-state (bidirectional) | Config change burst (15s) |
|------------|------------------------------|---------------------------|
| 1,000 | ~200 KB/s | ~2 MB/s |
| 5,000 | ~1 MB/s | ~10 MB/s |
| 10,000 | ~2 MB/s | ~20 MB/s |
| 15,000 | ~3 MB/s | ~30 MB/s |

At 15-second intervals, steady-state bandwidth doubles relative to 30-second intervals. At 10,000 thralls, 2 MB/s sustained is under 2% of a 1 Gbps link. The config change burst at 10k (20 MB/s for 15 seconds) is noticeable but well within limits.

**Consideration for metered or satellite links**: At 2 MB/s sustained, 15-second intervals consume ~5 GiB/month for 10k thralls at idle. On a metered VPS or satellite uplink, 30-second or 1-minute intervals may be more cost-effective.

## SQLite Write Load

The flusher runs every 10 seconds. At 15-second intervals, each thrall may check in once or twice per flush window, but the flusher only writes the latest state per thrall.

| Fleet Size | Dirty thralls per flush | Batch transactions | SQLite write time |
|------------|-------------------------|--------------------|-------------------|
| 1,000 | ~1,000 | 2 | ~5 ms |
| 5,000 | ~5,000 | 10 | ~25 ms |
| 10,000 | ~10,000 | 20 | ~50 ms |
| 15,000 | ~15,000 | 30 | ~75 ms |

Identical to longer intervals. The flusher coalesces multiple check-ins from the same thrall into a single write. SQLite load is a function of fleet size, not frequency.

**Traits writes**: At 15-second intervals, agents send traits 4x more often than at 1-minute intervals. The hash-dedup still catches 95%+ (traits don't change every 15 seconds), so the actual write volume increase is negligible. The only scenario where this matters is a fleet with highly volatile traits (e.g., rapidly changing network interfaces or process lists), which would generate more dedup misses.

## Comparison Across All Intervals

| Metric (10k fleet) | 5-min | 1-min | 30-sec | 15-sec |
|---------------------|-------|-------|--------|--------|
| Avg RPS | 33 | 167 | 333 | 667 |
| Drift correction (avg) | 2.5 min | 30s | 15s | 7s |
| Stale detection | 15 min | 3 min | 90s | 45s |
| CPU (sustained) | ~1% | ~5% | ~10% | ~20% |
| CPU (config storm) | ~2% | ~12% | ~24% | ~48% |
| Network (steady) | ~100 KB/s | ~500 KB/s | ~1 MB/s | ~2 MB/s |
| Memory | ~1.4 GiB | ~1.4 GiB | ~1.4 GiB | ~1.4 GiB |
| SQLite flush | ~50 ms | ~50 ms | ~50 ms | ~50 ms |
| Config storm duration | ~5 min | ~60s | ~30s | ~15s |

Memory and SQLite are constant — they don't care about frequency. CPU and network scale linearly. The config storm is the same total work but compressed into a shorter window at faster intervals.

## Capacity Recommendations

### No Tuning: Up to 5,000 Thralls

Default settings handle 5,000 thralls at 15-second check-ins without any configuration changes:
- ~10% of one core sustained
- ~850 MiB memory
- ~1 MB/s network
- 25 ms SQLite flush every 10 seconds

### With Tuning: 5,000-10,000 Thralls

```yaml
checkin:
  interval: "15s"
  jitter_percent: 10
tuning:
  gogc: 200
  memory_limit: "6GiB"
  grpc_read_buffer: 16384
  grpc_write_buffer: 16384
```

Reduces gRPC buffer memory by half. At 10,000 thralls with tuned 16 KiB buffers, connection memory drops from 640 MiB to 320 MiB. Total memory: ~1.1 GiB of 8 GB.

### Beyond 10,000 Thralls

At 15-second intervals, 10,000+ thralls generates 667+ RPS sustained. Options:

1. **Upgrade to 8 vCPU / 16 GB** — handles ~20,000 thralls at 15s with tuning
2. **Spanner (hub-spoke)** — each spoke handles its own subset
3. **Use 30-second intervals instead** — doubles the fleet capacity for the same hardware with only a modest responsiveness tradeoff (15s drift correction vs 7s)

### When Streaming Beats 15-Second Polling

At 15-second polling, the average latency for an ad-hoc task dispatch is 7.5 seconds. If that's too slow — for incident response automation, rolling deployments, or live queries where sub-second matters — switch affected thralls to streaming. Adaptive stream promotion already handles this: the server sets `stream_requested=true` when it has work, and the thrall opens a stream on its next poll. The 15-second interval then becomes the worst-case promotion latency.

For fleets where every thrall needs sub-second dispatch at all times, persistent streaming (not polling) is the right model — but it costs 64 KiB per connection in gRPC buffers for idle streams.

## Bottleneck Hierarchy (15-Second Intervals)

1. **Memory (gRPC buffers)** — 640 MiB at 10k connections. Hard limit at ~20k on 8 GB (tuned), ~12k untuned.
2. **CPU (ED25519 verify + gRPC overhead)** — 20% of one core at 10k. Saturates 4 cores at ~20k-30k thralls.
3. **Config storm CPU spike** — 48% of one core for 15 seconds at 10k. Tolerable, but at 20k+ this becomes 96% of one core.
4. **SQLite flusher** — 50 ms at 10k. Not a concern.
5. **Network** — 2 MB/s at 10k. Not a concern on 1 Gbps.

## When to Choose 15 Seconds vs 30 Seconds

Choose **15 seconds** when:
- Compliance SLAs require drift correction under 15 seconds
- Stale detection under 1 minute matters for alerting
- The fleet is under 10,000 thralls on this hardware
- You want the dashboard to feel like a live terminal

Choose **30 seconds** when:
- 15-30 second drift correction is acceptable
- You want to double the fleet capacity on the same hardware
- Network is metered or bandwidth-constrained
- You're running 10,000+ thralls and don't want to tune
