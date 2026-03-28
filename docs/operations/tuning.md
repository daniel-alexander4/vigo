
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
| `signature_window` | `5m` | Maximum allowed age difference between the request timestamp and server time for signature verification. Wider windows tolerate clock skew; narrower windows improve replay protection. |
| `last_seen_flush` | `10s` | How often the StateFlusher writes dirty envoy state from the FleetIndex to the database. Lower values reduce data loss on crash; higher values reduce write load. |
| `traits_prune_interval` | `5m` | How often old trait records are pruned from the database. Only the latest trait snapshot per envoy is kept. |
| `metrics_interval` | `30s` | How often Prometheus gauge metrics (node counts, compliance percentage, database size) are refreshed. Does not affect counters or histograms, which update in real time. |
| `traits_scanner_ttl` | `30s` | How long trait scan results are cached before the scanner re-evaluates. |

## Run Store

| Parameter | Default | Description |
|-----------|---------|-------------|
| `run_store` | `database` | Where to store convergence run records. Options: `database` (SQLite/Postgres) or `memory` (in-memory ring buffer per envoy, no persistence). Memory mode is useful for development or when you do not need historical run data. |
| `run_store_capacity` | `20` | When `run_store` is `memory`, the number of runs to keep per envoy in the ring buffer. Older runs are evicted when the buffer is full. |

## gRPC Transport

| Parameter | Default | Description |
|-----------|---------|-------------|
| `max_concurrent_streams` | `5000` | Maximum number of simultaneous bidirectional gRPC streams. With adaptive stream promotion, agents only hold streams during active work (task/query dispatch), so this limit applies to the peak number of agents receiving work concurrently — typically much smaller than the fleet size. |
| `grpc_read_buffer` | gRPC default | Per-connection read buffer in bytes. Reducing this saves memory per connection at the cost of more syscalls. |
| `grpc_write_buffer` | gRPC default | Per-connection write buffer in bytes. Same trade-off as read buffer. |
| `max_connection_age` | no limit | Force agents to reconnect after this duration (e.g. `"1h"`). Useful for load balancing across multiple server instances and for freeing leaked resources. |
| `max_connection_idle` | `15m` | Close connections that have no active streams after this duration. |
| `keepalive_time` | `30s` | How often the server pings idle clients to check liveness. |
| `keepalive_timeout` | `10s` | How long to wait for a keepalive ping response before closing the connection. |
| `stream_send_buffer` | `8` | Per-agent send channel capacity (messages). If the buffer is full, policy pushes are dropped (the agent picks up changes on its next check-in). Increase for agents on slow networks; keep low for memory efficiency at scale. |

## Go Runtime Tuning

These control the Go garbage collector and are only needed for large fleets or memory-constrained environments.

```yaml
tuning:
  gogc: 50
  memory_limit: "4GiB"
```

| Parameter | Default | Description |
|-----------|---------|-------------|
| `gogc` | `100` (Go default) | GC target percentage. Lower values reduce peak memory at the cost of more CPU spent on GC. Higher values reduce GC overhead but allow the heap to grow larger. |
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

## Scaling Guidance

For large fleets, consider reducing gRPC buffer sizes and adjusting the GC parameters. Example configuration for large deployments:

```yaml
tuning:
  grpc_read_buffer: 16384
  grpc_write_buffer: 16384
  stream_send_buffer: 4
  max_concurrent_streams: 60000
  gogc: 50
  memory_limit: "8GiB"
```


---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*

---
### Connection Rotation

For very long-running deployments, set `max_connection_age` to periodically force agents to reconnect. This prevents resource leaks from accumulating:

```yaml
tuning:
  max_connection_age: "4h"
```

Agents reconnect gracefully — the gRPC client library handles this transparently with exponential backoff.

### SQLite Write Bottleneck

At very large fleet sizes, database writes may become a bottleneck. Mitigations:

- Increase `last_seen_flush` to batch more writes
- Consider Postgres for write-heavy workloads
- Use `run_store: memory` if historical run data isn't needed

## Sizing Guidelines

| Fleet Size | `max_concurrent_streams` | `last_seen_flush` | `run_store` | `gogc` | `memory_limit` |
|-----------|-------------------------|-------------------|-------------|--------|----------------|
| < 1,000 | 5000 (default) | 10s (default) | database | 100 (default) | — |
| 1,000-10,000 | 15000 | 10s | database | 100 | — |
| 10,000-50,000 | 60000 | 30s | database | 50 | 8GiB |
| 50,000+ | 100000 | 60s | Postgres | 50 | 16GiB |
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
