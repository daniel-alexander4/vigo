# Rate Limiting

Vigo supports token-bucket rate limiting on check-in RPCs to protect the server from excessive traffic during fleet-wide events (mass reboots, network partitions, etc.).

## Configuration

```yaml
rate_limit:
  enabled: true
  checkin_per_envoy: 6   # requests/minute per envoy (default: 6)
  checkin_global: 500     # requests/second globally (default: 500)
```

| Option | Default | Description |
|--------|---------|-------------|
| `enabled` | `false` | Enable rate limiting |
| `checkin_per_envoy` | `6` | Per-envoy check-in rate limit (requests per minute) |
| `checkin_global` | `500` | Global check-in rate limit (requests per second) |

## Behavior

- **Per-envoy limiter**: Each envoy gets its own token bucket. Stale entries are evicted every 10 minutes.
- **Global limiter**: A single token bucket shared across all envoys.
- Rate-limited requests receive `ResourceExhausted` (gRPC code 8). Agents handle this gracefully with exponential backoff.
- Only `CheckIn` RPCs are rate-limited. Registration, trait reports, and admin RPCs are unaffected.

## Tuning

The default of 6 per minute per envoy (1 every 10s) works well for the typical 60-second check-in interval with 10% jitter. For high-frequency environments, increase `checkin_per_envoy`.

The global limit of 500/s handles fleets up to ~25,000 envoys with 60s intervals. For larger deployments, increase `checkin_global`.
