# Tuning Parameters

All tuning options live under the `tuning:` section of `server.yaml`. Every field is optional and defaults to a sensible value.

```yaml
tuning:
  signature_window: "5m"
  last_seen_flush: "10s"
  traits_prune_interval: "5m"
  metrics_interval: "30s"
  traits_scanner_ttl: "30s"
  run_store: "database"
  run_store_capacity: 20
  max_concurrent_streams: 5000
  webhook:
    timeout: "10s"
    max_retries: 3
    max_concurrent: 20
    retry_delay: "5s"
```

## General Parameters

| Parameter | Default | Description |
|-----------|---------|-------------|
| `signature_window` | `5m` | Maximum allowed age difference between the request timestamp and server time for ED25519 signature verification. Wider windows tolerate clock skew; narrower windows improve replay protection. |
| `last_seen_flush` | `10s` | How often the StateFlusher writes dirty thrall state (last_seen, force_push, rotated secrets) from the FleetIndex to SQLite. Lower values reduce data loss on crash; higher values reduce SQLite write load. |
| `traits_prune_interval` | `5m` | How often old trait records are pruned from the database. Only the latest trait snapshot per thrall is kept. |
| `metrics_interval` | `30s` | How often Prometheus gauge metrics (node counts, compliance percentage, database size) are refreshed. Does not affect counters or histograms, which update in real time. |
| `traits_scanner_ttl` | `30s` | How long trait scan results are cached before the scanner re-evaluates. |

## Run Store

| Parameter | Default | Description |
|-----------|---------|-------------|
| `run_store` | `database` | Where to store convergence run records. Options: `database` (SQLite/Postgres) or `memory` (in-memory ring buffer per thrall, no persistence). Memory mode is useful for development or when you do not need historical run data. |
| `run_store_capacity` | `20` | When `run_store` is `memory`, the number of runs to keep per thrall in the ring buffer. Older runs are evicted when the buffer is full. |

## gRPC Transport

| Parameter | Default | Description |
|-----------|---------|-------------|
| `max_concurrent_streams` | `5000` | Maximum number of simultaneous bidirectional gRPC streams. With adaptive stream promotion, agents only hold streams during active work (task/query dispatch), so this limit applies to the peak number of agents receiving work concurrently — typically much smaller than the fleet size. |
| `grpc_read_buffer` | gRPC default (32 KiB) | Per-connection read buffer in bytes. Halving this to 16384 saves ~16 KiB per connection at the cost of more syscalls. |
| `grpc_write_buffer` | gRPC default (32 KiB) | Per-connection write buffer in bytes. Same trade-off as read buffer. |
| `max_connection_age` | no limit | Force agents to reconnect after this duration (e.g. `"1h"`). Useful for load balancing across multiple server instances and for freeing leaked resources. |
| `max_connection_idle` | `15m` | Close connections that have no active streams after this duration. |
| `keepalive_time` | `30s` | How often the server pings idle clients to check liveness. |
| `keepalive_timeout` | `10s` | How long to wait for a keepalive ping response before closing the connection. |
| `stream_send_buffer` | `8` | Per-agent send channel capacity (messages). If the buffer is full, policy pushes are dropped (the agent picks up changes on its next check-in). Increase for agents on slow networks; keep low for memory efficiency at scale. |

## Go Runtime Tuning

These control the Go garbage collector and are only needed for large fleets (10k+ thralls) or memory-constrained environments.

```yaml
tuning:
  gogc: 50
  memory_limit: "4GiB"
```

| Parameter | Default | Description |
|-----------|---------|-------------|
| `gogc` | `100` (Go default) | GC target percentage. Lower values (e.g. `50`) reduce peak memory at the cost of more CPU spent on GC. Higher values (e.g. `200`) reduce GC overhead but allow the heap to grow larger. |
| `memory_limit` | no limit | Hard memory ceiling (equivalent to `GOMEMLIMIT`). When the heap approaches this limit, the GC becomes more aggressive regardless of `gogc`. Supports `B`, `KiB`, `MiB`, `GiB`, `TiB` suffixes or plain byte count. |

**Interaction between `gogc` and `memory_limit`**: Use `memory_limit` as a safety net and `gogc` for steady-state tuning. For example, `gogc: 50` + `memory_limit: "4GiB"` means the GC targets 50% heap growth between cycles but will never let the process exceed 4 GiB.

## Webhook Dispatch

| Parameter | Default | Description |
|-----------|---------|-------------|
| `webhook.timeout` | `10s` | HTTP request timeout for webhook deliveries. |
| `webhook.max_retries` | `3` | Number of retry attempts after a failed delivery. |
| `webhook.max_concurrent` | `20` | Maximum number of webhook deliveries in flight simultaneously. Excess deliveries are queued. |
| `webhook.retry_delay` | `5s` | Base delay between retries. Multiplied by the attempt number (linear backoff). |

## Other Config Sections with Tuning Implications

These are not under `tuning:` but affect operational behavior:

### Check-in Timing

```yaml
checkin:
  interval: "5m"           # how often agents check in
  jitter_percent: 20       # randomization to avoid thundering herd
  bundle_max_age: "24h"    # how long signed bundles remain valid on agents
```

### Database Retention

```yaml
database:
  retention: "90d"         # auto-prune runs/pushes/workflows older than this
```

### Secret Watcher

```yaml
watcher:
  enabled: true
  poll_interval: "30s"     # how often to poll for secret rotation
```

## Scaling Ceilings

### Per-Connection Memory

Each persistent gRPC stream costs approximately:

| Component | Size |
|-----------|------|
| TLS session state | ~10 KiB |
| gRPC read buffer | 32 KiB (configurable via `grpc_read_buffer`) |
| gRPC write buffer | 32 KiB (configurable via `grpc_write_buffer`) |
| HTTP/2 flow control window | ~64 KiB |
| Go goroutines (2 per stream) | ~8 KiB |
| Stream send channel | 8 messages (configurable via `stream_send_buffer`) |
| FleetIndex entry | ~2 KiB |

**Total: ~150-230 KiB per connection** depending on configuration.

At default settings, 10,000 persistent streams use ~2.3 GB of RAM just for connection state. At 50,000 streams, this grows to ~11.5 GB.

### Reducing Per-Connection Memory

For large fleets, halve the gRPC buffers and reduce the send channel:

```yaml
tuning:
  grpc_read_buffer: 16384
  grpc_write_buffer: 16384
  stream_send_buffer: 4
  max_concurrent_streams: 60000
  gogc: 50
  memory_limit: "8GiB"
```

This brings per-connection cost down to ~120 KiB, allowing ~60,000 streams in 8 GB.

### Connection Rotation

For very long-running deployments, set `max_connection_age` to periodically force agents to reconnect. This prevents resource leaks from accumulating:

```yaml
tuning:
  max_connection_age: "4h"
```

Agents reconnect gracefully — the gRPC client library handles this transparently with exponential backoff.

### SQLite Write Bottleneck

With the FleetIndex handling all hot-path reads in memory, the main SQLite bottleneck is the periodic flush of dirty state. At very large fleet sizes:

- Increase `last_seen_flush` to `30s` or `60s` to batch more writes
- Consider Postgres for write-heavy workloads (WAL contention at 50k+ thralls)
- Use `run_store: memory` if historical run data isn't needed

### Go GC at Large Heap Sizes

At heap sizes above 4-8 GB, Go's GC can cause noticeable pause times. Mitigations:

1. **Set `memory_limit`** — gives the GC a hard ceiling to work against, enabling the soft memory limit behavior introduced in Go 1.19
2. **Lower `gogc`** — more frequent GC cycles keep the heap smaller and pauses shorter
3. **Use Postgres** — moves large data sets (runs, traits) out of the Go heap into the database process

## Sizing Guidelines

| Fleet Size | `max_concurrent_streams` | `last_seen_flush` | `run_store` | `gogc` | `memory_limit` | `grpc_read_buffer` |
|-----------|-------------------------|-------------------|-------------|--------|----------------|---------------------|
| < 100 | 5000 (default) | 10s (default) | database | 100 (default) | — | default |
| 100-1,000 | 5000 (default) | 10s (default) | database | 100 (default) | — | default |
| 1,000-10,000 | 15000 | 10s | database | 100 | — | default |
| 10,000-50,000 | 60000 | 30s | database | 50 | 8GiB | 16384 |
| 50,000+ | 100000 | 60s | Postgres | 50 | 16GiB | 16384 |
