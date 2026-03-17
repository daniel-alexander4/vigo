# Prometheus Metrics

The Hobnob server exposes Prometheus metrics at `GET /metrics` on the REST port (default 8443).

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

### gRPC

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `hobnob_grpc_requests_total` | Counter | `method`, `code` | Total gRPC requests by full method name and status code |
| `hobnob_grpc_request_duration_seconds` | Histogram | `method` | gRPC request latency in seconds |

Covers all unary RPCs (CheckIn, ReportResult, ReportTraits, Register). Bidirectional streams (AgentStream) are not included — they use the check-in and delta metrics instead.

### Runs

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `hobnob_run_duration_seconds` | Histogram | `thrall` | Duration of agent convergence runs in seconds |
| `hobnob_drift_detected_total` | Counter | `thrall`, `module` | Drift detection events |
| `hobnob_module_actions_total` | Counter | `type`, `action` | Module actions taken (by resource type and action) |

### Orchestration

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `hobnob_task_runs_total` | Counter | `status` | Completed task runs by final status (complete, failed) |
| `hobnob_workflow_runs_total` | Counter | `status` | Completed workflow runs by final status (complete, failed) |

### Webhooks

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `hobnob_webhook_deliveries_total` | Counter | `status` | Webhook delivery outcomes (success, failed, rejected, cancelled, error) |
| `hobnob_webhook_duration_seconds` | Histogram | | Webhook delivery latency including retries |

### SMTP

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `hobnob_smtp_sends_total` | Counter | `event`, `status` | Email send attempts by event type and outcome (success, error) |

### Spanner

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `hobnob_spanner_spoke_latency_seconds` | Histogram | `spoke` | Spoke RPC probe latency in seconds |
| `hobnob_spanner_spoke_up` | Gauge | `spoke` | Spoke reachability (1=up, 0=down) |

Only populated on hub servers. Updated each time `SpokeStatuses` is probed.

### License

| Metric | Type | Description |
|--------|------|-------------|
| `hobnob_license_thralls_used` | Gauge | Current number of active (non-revoked) thralls |
| `hobnob_license_thralls_max` | Gauge | Maximum thralls allowed by the license |
| `hobnob_license_stage` | Gauge | Enforcement stage: 0=compliant, 1=grace, 2=enrollment\_block, 3=service\_stop |
| `hobnob_license_days_remaining` | Gauge | Days until license expiry (negative if expired) |
| `hobnob_license_expiry_timestamp` | Gauge | Unix timestamp of license expiration date |

### FleetIndex

| Metric | Type | Description |
|--------|------|-------------|
| `hobnob_fleetindex_flush_duration_seconds` | Histogram | Time to flush dirty thrall state from the FleetIndex to SQLite |
| `hobnob_fleetindex_dirty_count` | Gauge | Number of dirty entries in the last flush |

### Server

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `hobnob_uptime_seconds` | Gauge | | Seconds since the server process started |
| `hobnob_build_info` | Gauge | `version` | Server build information (always 1, version as label) |
| `hobnob_streams_active` | Gauge | | Number of currently connected agent streams |

### Bootstrap

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `hobnob_enrollments_total` | Counter | `status` | Bootstrap enrollment attempts (success, denied, error) |

### Secrets

| Metric | Type | Description |
|--------|------|-------------|
| `hobnob_secret_rotations_total` | Counter | Secret rotation events detected by the watcher |
| `hobnob_secret_resolution_errors_total` | Counter | Secret resolution failures during config loading |

### Security

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `hobnob_sig_verify_failed_total` | Counter | `reason` | Signature verification failures |

### Config

| Metric | Type | Description |
|--------|------|-------------|
| `hobnob_config_reload_duration_seconds` | Histogram | Duration of config reloads |
| `hobnob_config_reload_errors_total` | Counter | Config reload failures |

## AI Assistant Metrics

Defined in `server/ai/metrics.go`, registered separately.

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `hobnob_ai_requests_total` | Counter | | AI assistant requests |
| `hobnob_ai_tokens_total` | Counter | | Tokens consumed by AI responses |
| `hobnob_ai_request_duration_seconds` | Histogram | | AI request latency |
| `hobnob_ai_tool_calls_total` | Counter | `tool` | AI tool invocations by tool name |
| `hobnob_ai_errors_total` | Counter | | AI request errors |

## Scrape Configuration

Add the Hobnob server to your Prometheus scrape config:

```yaml
scrape_configs:
  - job_name: hobnob
    static_configs:
      - targets: ['hobnob-server:8443']
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

### gRPC Latency

- **P99 check-in latency**: `histogram_quantile(0.99, rate(hobnob_grpc_request_duration_seconds_bucket{method="/hobnob.HobnobAgent/CheckIn"}[5m]))`
- **Error rate by method**: `rate(hobnob_grpc_requests_total{code!="OK"}[5m])`

### Orchestration

- **Task throughput**: `rate(hobnob_task_runs_total[5m])`
- **Task failure rate**: `rate(hobnob_task_runs_total{status="failed"}[5m])`
- **Workflow success rate**: `rate(hobnob_workflow_runs_total{status="complete"}[5m]) / rate(hobnob_workflow_runs_total[5m])`

### Webhooks

- **Delivery success rate**: `rate(hobnob_webhook_deliveries_total{status="success"}[5m]) / rate(hobnob_webhook_deliveries_total[5m])`
- **P95 delivery latency**: `histogram_quantile(0.95, rate(hobnob_webhook_duration_seconds_bucket[5m]))`
- Alert on `rate(hobnob_webhook_deliveries_total{status="failed"}[5m]) > 0`

### License

- **Thrall utilization**: `hobnob_license_thralls_used / hobnob_license_thralls_max * 100`
- **Days until expiry**: `hobnob_license_days_remaining`
- **Expiry date**: `timestamp(hobnob_license_expiry_timestamp)` for Grafana absolute time display
- Alert on `hobnob_license_stage > 0` (any violation)
- Alert on `hobnob_license_thralls_used / hobnob_license_thralls_max > 0.9` (approaching limit)
- Alert on `hobnob_license_days_remaining < 30` (license expiring soon)

### Spanner

- **Spoke latency**: `histogram_quantile(0.95, rate(hobnob_spanner_spoke_latency_seconds_bucket[5m]))`
- Alert on `hobnob_spanner_spoke_up == 0` (spoke unreachable)

### Server Health

- **Uptime**: `hobnob_uptime_seconds` (detects restarts in Grafana)
- **Version**: `hobnob_build_info` label `version` (track rollout across instances)
- **Active streams**: `hobnob_streams_active` (monitor agent connectivity)
- Alert on `hobnob_uptime_seconds < 300` for recent restart detection
- Alert on `hobnob_streams_active == 0` when `hobnob_nodes_active_total > 0` (agents not connecting via stream)

### Security Alerts

- Alert on `rate(hobnob_sig_verify_failed_total[5m]) > 0` to detect signature verification failures
- Alert on `hobnob_config_reload_errors_total` increases
- Alert on `rate(hobnob_secret_resolution_errors_total[5m]) > 0`

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
