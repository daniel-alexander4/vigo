
# Spanner

The Spanner page shows hub-spoke topology and health. Available at `/spanner`. Only visible in hub mode.

## Layout

### Stat Cards

Seven top-level stat cards:

| Card | Description |
|------|-------------|
| Spokes | Total number of configured spokes |
| Healthy | Healthy/total ratio (color-coded: green if all healthy, yellow if partial, red if none) |
| Total Envoys | Aggregate envoy count across all spokes |
| Compliant | Total compliant envoys (green) |
| Drifted | Total drifted envoys (yellow) |
| Failing | Total failing envoys (red) |
| Compliance | Fleet-wide compliance percentage |

### Spokes Table

| Column | Description |
|--------|-------------|
| Spoke ID | Clickable link to spoke detail page (`/spanner/spokes/{id}`) |
| Address | gRPC address of the spoke |
| Patterns | Hostname glob patterns routed to this spoke |
| Envoys | Number of envoys on this spoke |
| Compliant | Compliant/total ratio |
| Compliance | Spoke-level compliance percentage |
| Latency | Round-trip latency to the spoke in milliseconds |
| Status | Health badge: "healthy" (green) or "unreachable" (red) with error tooltip |

## Key Actions

- **Monitor spoke health**: Red badges indicate unreachable spokes that need attention
- **Drill down into a spoke**: Click a spoke ID to view its envoys, runs, and force-convergence controls
- **Compare compliance across spokes**: Identify spokes with lower compliance rates

## Data Sources

- Spoke statuses: `spanner.Hub.SpokeStatuses()` which queries each spoke via gRPC
- Per-spoke compliance: Aggregated from spoke responses including envoy counts, compliance breakdown, and latency measurements
- The hub periodically polls spokes for status updates
