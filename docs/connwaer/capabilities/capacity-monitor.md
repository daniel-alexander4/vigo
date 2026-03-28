
# Capacity Monitoring

| | |
|---|---|
| **Machine name** | `capacity_monitor` |
| **Controls** | RTS7-Art11-1, RTS7-Art11-2 |
| **Platforms** | All |
| **Timeout** | default (5m) |

## What it verifies

Measures current system capacity -- disk usage, memory usage, CPU performance, and disk I/O throughput -- against configurable thresholds. Also performs trend analysis using historical data to project when resources will be exhausted. Designed for DORA RTS Article 11 compliance, which requires organizations to monitor and manage ICT capacity.

## Parameters

This capability has no configurable parameters.

## Thresholds

| Threshold | Type | Default | Description |
|-----------|------|---------|-------------|
| `disk_usage_max_pct` | float | `90.0` | Maximum root filesystem usage percentage before flagging. |
| `memory_usage_max_pct` | float | `90.0` | Maximum memory usage percentage before flagging. |
| `cpu_score_min` | integer | `0` (disabled) | Minimum acceptable CPU benchmark score (128x128 matrix multiply iterations/sec). Set to 0 to disable the check. |
| `disk_write_mbps_min` | float | `0.0` (disabled) | Minimum acceptable sequential disk write throughput in MB/s. Set to 0 to disable the check. |

## Verification logic

1. **Disk usage** -- Query root filesystem (`/`) usage via `statvfs` on Unix. Calculates `(1 - available/total) * 100`. If the percentage exceeds `disk_usage_max_pct`, flag a warning.

2. **Memory usage** -- Parse `/proc/meminfo` on Linux to read `MemTotal` and `MemAvailable`. Calculates `(total - available) / total * 100`. If the percentage exceeds `memory_usage_max_pct`, flag a warning.

3. **CPU benchmark** -- Perform a 128x128 floating-point matrix multiplication in a tight loop for exactly 1 second. The score is the number of complete iterations. If `cpu_score_min` is set and the score falls below it, flag a warning.

4. **Disk I/O benchmark** -- Write 64 MB sequentially (64 x 1 MB blocks) to a temporary file, call `fsync`, and measure throughput in MB/s. If `disk_write_mbps_min` is set and throughput falls below it, flag a warning.

5. **Load average** (Linux only) -- Read `/proc/loadavg` and record the 1-minute, 5-minute, and 15-minute load averages as evidence.

6. **Trend analysis** -- Query the trends database for historical `disk_usage_pct` values. If at least 5 data points exist, compute a linear regression slope (percent per day). If the trend projects disk exhaustion (reaching 100%), record the projected date and flag a warning.

7. **Record metrics** -- Store the current run's `disk_usage_pct`, `memory_usage_pct`, `cpu_score`, and `disk_write_mbps` in the trends database for future analysis.

### Result

- **Pass** -- All measurements are within thresholds and no exhaustion is projected.
- **Warn** -- One or more measurements exceed thresholds, or disk exhaustion is projected.

## Example config

```yaml
capabilities:
  capacity_monitor:
    schedule: "every 6 hours"
    thresholds:
      disk_usage_max_pct: 85
      memory_usage_max_pct: 80
```

With performance floor requirements:

```yaml
capabilities:
  capacity_monitor:
    schedule: "daily"
    thresholds:
      disk_usage_max_pct: 90
      memory_usage_max_pct: 90
      cpu_score_min: 50
      disk_write_mbps_min: 100.0
```
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
