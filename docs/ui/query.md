
# Query

The Query page provides a live trait query interface. Available at `/query`.

## Layout

### Query Form

Three input fields with a submit button:

| Field | Placeholder | Description |
|-------|-------------|-------------|
| Trait Path | `os.distro` | Dot-separated trait path to query |
| Target | `*.web.*` or `os.family=debian` | Hostname glob or trait filter |
| Timeout (s) | `10` | Maximum seconds to wait for responses (1-60) |

### Results

After submitting a query, three sections appear:

#### Stat Cards

| Card | Color | Description |
|------|-------|-------------|
| Responded | Green | Envoys that responded within the timeout |
| Timed Out | Yellow | Connected envoys that did not respond in time |
| Offline | Gray | Envoys that were not connected (may show cached values) |

#### Results Table

| Column | Description |
|--------|-------------|
| Hostname | Envoy hostname |
| Value | Trait value (monospaced), or "(not found)" if the trait does not exist |
| Status | Badge: responded, offline (with "cached" suffix if stale data), timed_out |

## Key Actions

- **Query any trait path**: Use dot notation like `os.distro`, `hardware.memory_mb`, `network.interfaces`
- **Target by glob**: `*.web.*` matches hostnames
- **Target by trait**: `os.family=debian` matches trait values
- **Adjust timeout**: Increase for large fleets or slow networks

## Data Sources

- Live query: Dispatched via `POST /api/v1/query` to connected agents over the bidirectional gRPC stream
- Offline envoys: Cached trait values from the database (if available), with `collectedAt` timestamp
- The query targets envoys using FleetIndex for glob/trait matching, then dispatches to connected agents
