# Federation

The Federation page shows hub-spoke topology and health. Available at `/federation`. Only visible in hub mode.

## Layout

### Stat Cards

Seven top-level stat cards:

| Card | Description |
|------|-------------|
| Spokes | Total number of configured spokes |
| Healthy | Healthy/total ratio (color-coded: green if all healthy, yellow if partial, red if none) |
| Total Thralls | Aggregate thrall count across all spokes |
| Compliant | Total compliant thralls (green) |
| Drifted | Total drifted thralls (yellow) |
| Failing | Total failing thralls (red) |
| Compliance | Fleet-wide compliance percentage |

### Spokes Table

| Column | Description |
|--------|-------------|
| Spoke ID | Clickable link to spoke detail page (`/federation/spokes/{id}`) |
| Address | gRPC address of the spoke |
| Patterns | Hostname glob patterns routed to this spoke |
| Thralls | Number of thralls on this spoke |
| Compliant | Compliant/total ratio |
| Compliance | Spoke-level compliance percentage |
| Latency | Round-trip latency to the spoke in milliseconds |
| Status | Health badge: "healthy" (green) or "unreachable" (red) with error tooltip |

## Key Actions

- **Monitor spoke health**: Red badges indicate unreachable spokes that need attention
- **Drill down into a spoke**: Click a spoke ID to view its thralls, runs, and force-push controls
- **Compare compliance across spokes**: Identify spokes with lower compliance rates

## Data Sources

- Spoke statuses: `federation.Hub.SpokeStatuses()` which queries each spoke via gRPC
- Per-spoke compliance: Aggregated from spoke responses including thrall counts, compliance breakdown, and latency measurements
- The hub periodically polls spokes for status updates
