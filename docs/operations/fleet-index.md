
# FleetIndex

The FleetIndex is an in-memory cache that accelerates check-in operations. It holds the current state of every envoy, eliminating database queries on the check-in hot path.

Source: `server/fleet/index.go`

## Purpose

FleetIndex moves all real-time envoy state into memory, providing fast lookups by ID, hostname, and public key. This avoids database contention during agent check-ins.

Key capabilities:

- **Fast lookups** by envoy ID, hostname, and public key
- **Targeting queries** by compliance status and trait values
- **Pre-computed compliance counters** for fleet-wide compliance stats
- **Recent runs buffer** for dashboard display

## Rehydration

On server startup, the FleetIndex is populated from the database in bulk. After rehydration, all check-in operations are served from memory.

Source: `server/fleet/rehydrate.go`

## Dirty-Set Flushing

State changes (last-seen updates, force-push clears, rotated secret clears) are accumulated in memory and flushed to the database periodically by the StateFlusher. The flush interval is configurable via `tuning.last_seen_flush`.

Source: `server/fleet/flusher.go`

## Read Methods

| Method | Description |
|--------|-------------|
| `GetEnvoy(id)` | Full deep copy of EnvoyState |
| `GetEnvoyLite(id)` | Lightweight shallow copy |
| `GetPubKey(id)` | Parsed public key for signature verification |
| `GetByHostname(hostname)` | Hostname to UUID lookup |
| `IsRevoked(id)` | Revocation check |
| `ListByGlob(pattern)` | Glob-match hostnames |
| `ListByTrait(key, value)` | Trait-filtered targeting |
| `ListByStatus(status)` | Status-filtered listing |
| `ComplianceSummary()` | Pre-computed counters |
| `RecentRuns(n)` | Recent runs buffer read |
| `Count()`, `ActiveCount()` | Fleet size |

## Write Methods

| Method | Description |
|--------|-------------|
| `AddEnvoy(state, pubkey)` | Insert or update an envoy |
| `RemoveEnvoy(id)` | Delete from all indexes |
| `MarkSeen(id, time)` | Update last-seen, mark dirty |
| `MarkSeenInMemory(id, time)` | Update last-seen without dirtying (for streaming envoys) |
| `SetForcePush(id, bool)` | Set/clear force-push flag |
| `SetForcePushAll()` | Force-push all active envoys |
| `UpdateTraits(id, traits)` | Replace traits, update inverted index |
| `UpdateRunStatus(id, status, changed, time)` | Update run status, recompute compliance |
| `AppendRecentRun(summary)` | Add to recent runs buffer |
