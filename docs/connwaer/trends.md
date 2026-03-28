
# Trends

Connwaer maintains a local SQLite database for historical metrics, enabling trend analysis and exhaustion prediction.

## Database

The trends database is stored at `/var/lib/connwaer/trends.db` (configurable via `trends_db` in the config). It uses WAL mode with NORMAL synchronous for performance.

### Schema

Two tables:

**runs** -- one row per capability execution:

| Column | Type | Description |
|--------|------|-------------|
| `id` | INTEGER | Auto-increment primary key |
| `capability` | TEXT | Capability machine name |
| `status` | TEXT | pass/fail/warn/error/skipped |
| `metrics` | TEXT | JSON object with capability-specific numeric values |
| `timestamp` | TEXT | ISO 8601 execution time |

**trend_cache** -- precomputed trend analysis:

| Column | Type | Description |
|--------|------|-------------|
| `capability` | TEXT | Capability name |
| `metric_key` | TEXT | Metric being tracked (e.g., `disk_usage_pct`) |
| `slope_per_day` | REAL | Rate of change per day |
| `latest_value` | REAL | Most recent metric value |
| `computed_at` | TEXT | When the trend was last calculated |

## Trend Analysis

The `compute_trend()` method performs linear regression over a metric key extracted from the JSON metrics column. It produces:

- **slope_per_day** -- rate of change (positive = increasing, negative = decreasing)
- **projected exhaustion** -- for capacity metrics, when the value will reach 100% at the current rate

## Capabilities That Record Trends

| Capability | Metric Key | What It Tracks |
|------------|-----------|----------------|
| `capacity_monitor` | `disk_usage_pct` | Disk usage percentage over time, projected full date |
| `backup_restore` | `rto_seconds` | Recovery time trending, slope detects degradation |
| `time_sync` | `offset_ms` | NTP offset stability over time |
| `rng_validation` | `pass_rate` | Percentage of NIST tests passing |

## Pruning

The `prune(keep)` method retains the most recent N records per capability and deletes older rows. This prevents unbounded growth of the trends database.

## Manual Inspection

The trends database can be queried directly with any SQLite client:

```bash
sqlite3 /var/lib/connwaer/trends.db \
  "SELECT capability, status, timestamp FROM runs ORDER BY timestamp DESC LIMIT 20"
```

```bash
sqlite3 /var/lib/connwaer/trends.db \
  "SELECT capability, metric_key, slope_per_day, latest_value FROM trend_cache"
```
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
