
# Prometheus Metrics

The Vigo server exposes Prometheus metrics at `GET /metrics` on the REST port (default 8443).

## Available Metrics

### Fleet Gauges

| Metric | Type | Description |
|--------|------|-------------|
| `vigo_nodes_total` | Gauge | Total number of enrolled envoys |
| `vigo_nodes_active_total` | Gauge | Envoys that checked in within the last hour |
| `vigo_compliance_pct` | Gauge | Fleet-wide compliance percentage (0.0 to 1.0) |
| `vigo_database_size_bytes` | Gauge | Current SQLite database file size in bytes |

### Compliance

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `vigo_compliance_status` | Gauge | `envoy`, `status` | Per-envoy compliance status (1 for current status). Status values: compliant, drifted, failing, stale, unknown |

### Check-ins

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `vigo_checkins_total` | Counter | `status` | Total number of agent check-ins |
| `vigo_checkin_age_seconds` | Histogram | | Distribution of seconds since last check-in across all envoys |

The `vigo_checkin_age_seconds` histogram uses custom buckets: 60s, 120s, 300s, 600s, 900s, 1800s, 3600s, 7200s. This replaced per-envoy last-seen gauges to avoid cardinality explosion at scale.

### gRPC

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `vigo_grpc_requests_total` | Counter | `method`, `code` | Total gRPC requests by full method name and status code |
| `vigo_grpc_request_duration_seconds` | Histogram | `method` | gRPC request latency in seconds |

Covers all unary RPCs (CheckIn, ReportResult, ReportTraits, Register). Bidirectional streams (AgentStream) are not included — they use the check-in and delta metrics instead.

### Runs

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `vigo_run_duration_seconds` | Histogram | `envoy` | Duration of agent convergence runs in seconds |
| `vigo_drift_detected_total` | Counter | `envoy`, `module` | Drift detection events |
| `vigo_module_actions_total` | Counter | `type`, `action` | Module actions taken (by resource type and action) |

### Orchestration

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `vigo_task_runs_total` | Counter | `status` | Completed task runs by final status (complete, failed) |
| `vigo_workflow_runs_total` | Counter | `status` | Completed workflow runs by final status (complete, failed) |

### Webhooks

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `vigo_webhook_deliveries_total` | Counter | `status` | Webhook delivery outcomes (success, failed, rejected, cancelled, error) |
| `vigo_webhook_duration_seconds` | Histogram | | Webhook delivery latency including retries |

### SMTP

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `vigo_smtp_sends_total` | Counter | `event`, `status` | Email send attempts by event type and outcome (success, error) |

### Spanner

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `vigo_spanner_spoke_latency_seconds` | Histogram | `spoke` | Spoke RPC probe latency in seconds |
| `vigo_spanner_spoke_up` | Gauge | `spoke` | Spoke reachability (1=up, 0=down) |

Only populated on hub servers. Updated each time `SpokeStatuses` is probed.

### License

| Metric | Type | Description |
|--------|------|-------------|
| `vigo_license_envoys_used` | Gauge | Current number of active (non-revoked) envoys |
| `vigo_license_envoys_max` | Gauge | Maximum envoys allowed by the license |
| `vigo_license_stage` | Gauge | Enforcement stage: 0=compliant, 1=grace, 2=enrollment\_block, 3=service\_stop |
| `vigo_license_days_remaining` | Gauge | Days until license expiry (negative if expired) |
| `vigo_license_expiry_timestamp` | Gauge | Unix timestamp of license expiration date |

### FleetIndex

| Metric | Type | Description |
|--------|------|-------------|
| `vigo_fleetindex_flush_duration_seconds` | Histogram | Time to flush dirty envoy state from the FleetIndex to SQLite |
| `vigo_fleetindex_dirty_count` | Gauge | Number of dirty entries in the last flush |

### Server

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `vigo_uptime_seconds` | Gauge | | Seconds since the server process started |
| `vigo_build_info` | Gauge | `version` | Server build information (always 1, version as label) |
| `vigo_streams_active` | Gauge | | Number of currently connected agent streams |

### Bootstrap

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `vigo_enrollments_total` | Counter | `status` | Bootstrap enrollment attempts (success, denied, error) |

### Secrets

| Metric | Type | Description |
|--------|------|-------------|
| `vigo_secret_rotations_total` | Counter | Secret rotation events detected by the watcher |
| `vigo_secret_resolution_errors_total` | Counter | Secret resolution failures during config loading |

### Security

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `vigo_sig_verify_failed_total` | Counter | `reason` | Signature verification failures |
| `vigo_security_cve_critical` | Gauge | | Fleet-wide count of critical CVEs across all scanned hosts |
| `vigo_security_cve_high` | Gauge | | Fleet-wide count of high CVEs across all scanned hosts |
| `vigo_security_cve_medium` | Gauge | | Fleet-wide count of medium CVEs across all scanned hosts |
| `vigo_security_cve_low` | Gauge | | Fleet-wide count of low CVEs across all scanned hosts |
| `vigo_security_hardening_avg` | Gauge | | Average hardening score across all scanned hosts (0-100) |
| `vigo_security_rootkit_warnings` | Gauge | | Fleet-wide count of rootkit warnings |
| `vigo_security_malware_detections` | Gauge | | Fleet-wide count of malware detections |
| `vigo_security_integrity_issues` | Gauge | | Fleet-wide count of file integrity issues |
| `vigo_security_scanned_hosts` | Gauge | | Number of hosts with security scan data |

### Convergence

| Metric | Type | Description |
|--------|------|-------------|
| `vigo_convergence_converged` | Gauge | Number of envoys in converged state |
| `vigo_convergence_drifted` | Gauge | Number of envoys in drifted state |
| `vigo_convergence_errors` | Gauge | Number of envoys in error state |
| `vigo_convergence_lost_contact` | Gauge | Number of envoys that have lost contact |
| `vigo_convergence_no_data` | Gauge | Number of envoys with no convergence data |
| `vigo_convergence_pct` | Gauge | Fleet-wide convergence percentage (0.0 to 1.0) |

### Risk

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `vigo_risk_avg_score` | Gauge | | Fleet-wide average risk score (0-100) |
| `vigo_risk_max_score` | Gauge | | Highest risk score across all envoys (0-100) |
| `vigo_risk_distribution` | Gauge | `level` | Number of envoys at each risk level (low, medium, high, critical) |

### Config

| Metric | Type | Description |
|--------|------|-------------|
| `vigo_config_reload_duration_seconds` | Histogram | Duration of config reloads |
| `vigo_config_reload_errors_total` | Counter | Config reload failures |

## AI Assistant Metrics

Defined in `server/ai/metrics.go`, registered separately.

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `vigo_ai_requests_total` | Counter | | AI assistant requests |
| `vigo_ai_tokens_total` | Counter | | Tokens consumed by AI responses |
| `vigo_ai_request_duration_seconds` | Histogram | | AI request latency |
| `vigo_ai_tool_calls_total` | Counter | `tool` | AI tool invocations by tool name |
| `vigo_ai_errors_total` | Counter | | AI request errors |

## Scrape Configuration

Add the Vigo server to your Prometheus scrape config:

```yaml
scrape_configs:
  - job_name: vigo
    static_configs:
      - targets: ['vigo-server:8443']
    scrape_interval: 30s
```

## Grafana Dashboard Suggestions

### Fleet Overview Panel

- **Total Envoys**: `vigo_nodes_total`
- **Active Envoys (1h)**: `vigo_nodes_active_total`
- **Compliance %**: `vigo_compliance_pct * 100`
- **Check-in Rate**: `rate(vigo_checkins_total[5m])`

### Compliance Breakdown

- Stacked gauge or pie chart using `vigo_compliance_status` grouped by `status`
- Time-series of `vigo_compliance_pct` for trend analysis

### Run Performance

- **P95 run duration**: `histogram_quantile(0.95, rate(vigo_run_duration_seconds_bucket[5m]))`
- **Failure rate**: `rate(vigo_checkins_total{status="failure"}[5m])`
- **Drift rate**: `rate(vigo_drift_detected_total[5m])`

### gRPC Latency

- **P99 check-in latency**: `histogram_quantile(0.99, rate(vigo_grpc_request_duration_seconds_bucket{method="/vigo.VigoAgent/CheckIn"}[5m]))`
- **Error rate by method**: `rate(vigo_grpc_requests_total{code!="OK"}[5m])`

### Orchestration

- **Task throughput**: `rate(vigo_task_runs_total[5m])`
- **Task failure rate**: `rate(vigo_task_runs_total{status="failed"}[5m])`
- **Workflow success rate**: `rate(vigo_workflow_runs_total{status="complete"}[5m]) / rate(vigo_workflow_runs_total[5m])`

### Webhooks

- **Delivery success rate**: `rate(vigo_webhook_deliveries_total{status="success"}[5m]) / rate(vigo_webhook_deliveries_total[5m])`
- **P95 delivery latency**: `histogram_quantile(0.95, rate(vigo_webhook_duration_seconds_bucket[5m]))`
- Alert on `rate(vigo_webhook_deliveries_total{status="failed"}[5m]) > 0`

### License

- **Envoy utilization**: `vigo_license_envoys_used / vigo_license_envoys_max * 100`
- **Days until expiry**: `vigo_license_days_remaining`
- **Expiry date**: `timestamp(vigo_license_expiry_timestamp)` for Grafana absolute time display
- Alert on `vigo_license_stage > 0` (any violation)
- Alert on `vigo_license_envoys_used / vigo_license_envoys_max > 0.9` (approaching limit)
- Alert on `vigo_license_days_remaining < 30` (license expiring soon)

### Spanner

- **Spoke latency**: `histogram_quantile(0.95, rate(vigo_spanner_spoke_latency_seconds_bucket[5m]))`
- Alert on `vigo_spanner_spoke_up == 0` (spoke unreachable)

### Server Health

- **Uptime**: `vigo_uptime_seconds` (detects restarts in Grafana)
- **Version**: `vigo_build_info` label `version` (track rollout across instances)
- **Active streams**: `vigo_streams_active` (monitor agent connectivity)
- Alert on `vigo_uptime_seconds < 300` for recent restart detection
- Alert on `vigo_streams_active == 0` when `vigo_nodes_active_total > 0` (agents not connecting via stream)

### Security Alerts

- Alert on `rate(vigo_sig_verify_failed_total[5m]) > 0` to detect signature verification failures
- Alert on `vigo_config_reload_errors_total` increases
- Alert on `rate(vigo_secret_resolution_errors_total[5m]) > 0`

### Check-in Staleness

- **Median check-in age**: `histogram_quantile(0.5, rate(vigo_checkin_age_seconds_bucket[5m]))`
- Alert when `histogram_quantile(0.95, ...)` exceeds your expected check-in interval

## Metrics Refresh Interval

Gauge metrics (node counts, compliance, database size) are refreshed on a configurable interval:

```yaml
tuning:
  metrics_interval: "30s"    # default
```

See [Tuning Parameters](tuning.md) for all tuning options.
