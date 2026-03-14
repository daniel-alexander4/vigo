# FleetIndex

The FleetIndex is an in-memory index that holds the current state of every thrall. It eliminates database queries on the check-in hot path, providing O(1) lookups by ID, hostname, and public key.

Source: `server/fleet/index.go`

## Purpose

Before FleetIndex, every agent check-in required multiple SQLite queries: thrall lookup, pubkey fetch, compliance status, and last-seen updates. At scale, this created write contention on the SQLite WAL.

FleetIndex moves all real-time state into memory:

- **Zero DB operations on check-in** -- pubkey lookups, thrall state reads, and last-seen updates all happen in-memory
- **O(1) lookups** by thrall ID, hostname, and public key
- **O(k) targeting queries** by compliance status and trait values (where k is the result set size)
- **Pre-computed compliance counters** -- no need to scan the database for fleet-wide compliance stats
- **Recent runs ring buffer** -- last 1000 runs kept in memory for dashboard display

## Data Structures

The FleetIndex maintains several concurrent maps protected by a single `sync.RWMutex`:

| Map | Key | Value | Purpose |
|-----|-----|-------|---------|
| `thralls` | UUID | ThrallState | Primary thrall store |
| `byHostname` | hostname | UUID | Hostname to ID mapping |
| `pubkeys` | UUID | ed25519.PublicKey | Parsed public keys for signature verification |
| `byStatus` | compliance status | set of UUIDs | Status-based targeting |
| `byTrait` | trait_key/trait_value | set of UUIDs | Trait-based targeting |
| `dirty` | UUID | bool | Thralls with pending state changes |

## ThrallState

Each thrall's in-memory state includes:

| Field | Description |
|-------|-------------|
| `ID`, `Hostname`, `IP` | Identity |
| `PubkeyPEM` | PEM-encoded public key |
| `EnrolledAt`, `LastSeen` | Timestamps |
| `Revoked` | Whether the thrall is revoked |
| `ComplianceStatus` | Current compliance: compliant, drifted, failing, stale, unknown |
| `LastRunStatus`, `LastRunChangedCount`, `LastRunAt` | Last run info |
| `Traits` | Flattened key-value map for the trait inverted index |
| `PendingRotatedSecrets` | Secrets awaiting rotation |
| `ForcePush` | Whether a force-push is pending |

## Rehydration

On server startup, `fleet.Rehydrate()` loads the entire FleetIndex from SQLite in two bulk queries:

1. **ListThrallsWithLatestRun** -- all thralls with their most recent run status (single query)
2. **GetAllLatestTraits** -- all thralls' latest traits (single query)

For each thrall, rehydration:
- Parses the ED25519 public key
- Computes compliance status from the latest run
- Flattens traits into the inverted index
- Populates all maps

Source: `server/fleet/rehydrate.go`

## Dirty-Set Flushing

State changes (last-seen updates, force-push clears, rotated secret clears) are accumulated in the dirty set and flushed to SQLite periodically by the `StateFlusher`.

Default flush interval: 10 seconds (configurable via `tuning.last_seen_flush`).

The flusher:
1. Drains the dirty set (atomically swaps it with an empty map)
2. Batch-updates `last_seen` in chunks of 500
3. Clears `force_push` and `pending_rotated_secrets` flags as needed
4. Re-marks any thralls as dirty if the batch update failed

Source: `server/fleet/flusher.go`

## Read Methods

All read methods acquire an `RLock` (shared), allowing concurrent reads:

| Method | Complexity | Description |
|--------|-----------|-------------|
| `GetThrall(id)` | O(1) | Full deep copy of ThrallState |
| `GetThrallLite(id)` | O(1) | Shallow copy (2.5x faster, -58% memory) |
| `GetPubKey(id)` | O(1) | Parsed ED25519 public key |
| `GetByHostname(hostname)` | O(1) | Hostname to UUID lookup |
| `IsRevoked(id)` | O(1) | Revocation check |
| `ListByGlob(pattern)` | O(n) | Glob-match hostnames (pre-allocates at 10% fleet size) |
| `ListByTrait(key, value)` | O(k) | Trait-filtered targeting |
| `ListByStatus(status)` | O(k) | Status-filtered listing |
| `ComplianceSummary()` | O(1) | Pre-computed counters |
| `RecentRuns(n)` | O(n) | Ring buffer read |
| `Count()`, `ActiveCount()` | O(1) / O(n) | Fleet size |

## Write Methods

Write methods acquire a full `Lock` (exclusive):

| Method | Description |
|--------|-------------|
| `AddThrall(state, pubkey)` | Insert or update a thrall |
| `RemoveThrall(id)` | Delete from all indexes |
| `MarkSeen(id, time)` | Update last-seen, mark dirty |
| `MarkSeenInMemory(id, time)` | Update last-seen without dirtying (for streaming thralls) |
| `SetForcePush(id, bool)` | Set/clear force-push flag |
| `SetForcePushAll()` | Force-push all active thralls |
| `UpdateTraits(id, traits)` | Replace traits, update inverted index |
| `UpdateRunStatus(id, status, changed, time)` | Update run status, recompute compliance |
| `AppendRecentRun(summary)` | Add to ring buffer |

## Performance

Benchmarks (from `server/fleet/bench_test.go`):

- **GetThrall**: full deep copy per lookup
- **GetThrallLite**: 2.5x faster than GetThrall, 58% less memory, 50% fewer allocations
- **GetPubKey**: single map lookup
- **ListByGlob**: pre-allocates at 10% of fleet size to reduce allocations
- **MarkSeen**: single map write + dirty flag

The check-in hot path uses `GetThrallLite` for the initial existence/revocation check, then `GetPubKey` for signature verification -- a total of two map lookups under a shared read lock with zero database queries.
