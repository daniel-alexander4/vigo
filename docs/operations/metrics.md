# Prometheus Metrics

The Hobnob server exposes Prometheus metrics at `GET /metrics` on the REST port (default 8181).

## Available Metrics

### Fleet Gauges

| Metric | Type | Description |
|--------|------|-------------|
| `hobnob_nodes_total` | Gauge | Total number of enrolled thralls |
| `hobnob_nodes_active_total` | Gauge | Thralls that checked in within the last hour |
| `hobnob_compliance_pct` | Gauge | Fleet-wide compliance percentage (0.0 to 1.0) |
| `hobnob_database_size_bytes` | Gauge | Current SQLite database file size in bytes |

### Compliance

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `hobnob_compliance_status` | Gauge | `thrall`, `status` | Per-thrall compliance status (1 for current status). Status values: compliant, drifted, failing, stale, unknown |

### Check-ins

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `hobnob_checkins_total` | Counter | `status` | Total number of agent check-ins |
| `hobnob_checkin_age_seconds` | Histogram | | Distribution of seconds since last check-in across all thralls |

The `hobnob_checkin_age_seconds` histogram uses custom buckets: 60s, 120s, 300s, 600s, 900s, 1800s, 3600s, 7200s. This replaced per-thrall last-seen gauges to avoid cardinality explosion at scale.

### Runs

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `hobnob_run_duration_seconds` | Histogram | `thrall` | Duration of agent runs in seconds |
| `hobnob_drift_detected_total` | Counter | `thrall`, `module` | Drift detection events |
| `hobnob_module_actions_total` | Counter | `type`, `action` | Module actions taken (by resource type and action) |

### Security

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `hobnob_sig_verify_failed_total` | Counter | `reason` | Signature verification failures |

### Config

| Metric | Type | Description |
|--------|------|-------------|
| `hobnob_config_reload_duration_seconds` | Histogram | Duration of config reloads |
| `hobnob_config_reload_errors_total` | Counter | Config reload failures |

Config reload duration uses buckets: 10ms, 50ms, 100ms, 250ms, 500ms, 1s, 2.5s, 5s.

## Scrape Configuration

Add the Hobnob server to your Prometheus scrape config:

```yaml
scrape_configs:
  - job_name: hobnob
    static_configs:
      - targets: ['hobnob-server:8181']
    scrape_interval: 30s
```

## Grafana Dashboard Suggestions

### Fleet Overview Panel

- **Total Thralls**: `hobnob_nodes_total`
- **Active Thralls (1h)**: `hobnob_nodes_active_total`
- **Compliance %**: `hobnob_compliance_pct * 100`
- **Check-in Rate**: `rate(hobnob_checkins_total[5m])`

### Compliance Breakdown

- Stacked gauge or pie chart using `hobnob_compliance_status` grouped by `status`
- Time-series of `hobnob_compliance_pct` for trend analysis

### Run Performance

- **P95 run duration**: `histogram_quantile(0.95, rate(hobnob_run_duration_seconds_bucket[5m]))`
- **Failure rate**: `rate(hobnob_checkins_total{status="failure"}[5m])`
- **Drift rate**: `rate(hobnob_drift_detected_total[5m])`

### Security Alerts

- Alert on `rate(hobnob_sig_verify_failed_total[5m]) > 0` to detect signature verification failures
- Alert on `hobnob_config_reload_errors_total` increases

### Check-in Staleness

- **Median check-in age**: `histogram_quantile(0.5, rate(hobnob_checkin_age_seconds_bucket[5m]))`
- Alert when `histogram_quantile(0.95, ...)` exceeds your expected check-in interval

## Metrics Refresh Interval

Gauge metrics (node counts, compliance, database size) are refreshed on a configurable interval:

```yaml
tuning:
  metrics_interval: "30s"    # default
```

See [Tuning Parameters](tuning.md) for all tuning options.
