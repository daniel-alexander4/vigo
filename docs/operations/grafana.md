# Grafana Dashboards

Pre-built Grafana dashboards for monitoring Hobnob via Prometheus.

## Download

| Dashboard | Panels | Download |
|-----------|--------|----------|
| **Fleet Overview** | 28 panels across 7 rows: fleet stats, compliance breakdown, check-ins & gRPC latency, convergence & drift, operations (tasks/workflows/enrollments), infrastructure (config reload, FleetIndex flush), webhooks & SMTP, secrets, AI assistant | [hobnob-overview.json](grafana/hobnob-overview.json) |
| **Spanner** | 2 panels: spoke availability (UP/DOWN status map) and spoke latency (p50/p95 time series) | [hobnob-spanner.json](grafana/hobnob-spanner.json) |

## Import

1. Download the JSON file using the link above (or from the docs browser at `/docs/operations/grafana/hobnob-overview.json`)
2. Open Grafana (typically http://localhost:3000)
3. Go to **Dashboards → Import**
4. Upload the JSON file or paste its contents
5. Select your Prometheus datasource when prompted

## Fleet Overview Dashboard

[Download hobnob-overview.json](grafana/hobnob-overview.json)

### Row 1 — Fleet Overview

| Panel | Type | Metric |
|-------|------|--------|
| Total Nodes | Stat | `hobnob_nodes_total` |
| Active Nodes | Stat | `hobnob_nodes_active_total` |
| Compliance % | Gauge (0-100, red/yellow/green) | `hobnob_compliance_pct` |
| Uptime | Stat | `hobnob_uptime_seconds` |
| License Usage | Gauge (used vs max) | `hobnob_license_thralls_used` / `hobnob_license_thralls_max` |
| License Days Remaining | Stat (red < 30, yellow < 90) | `hobnob_license_days_remaining` |
| Active Streams | Stat | `hobnob_streams_active` |
| Database Size | Stat | `hobnob_database_size_bytes` |

### Row 2 — Compliance Breakdown

| Panel | Type | Metric |
|-------|------|--------|
| Compliance Status by Type | Pie chart (green/yellow/red/gray) | `hobnob_compliance_status` by `status` label |
| Compliance % Over Time | Time series | `hobnob_compliance_pct` |

### Row 3 — Check-in & gRPC

| Panel | Type | Metric |
|-------|------|--------|
| Check-ins / min | Time series | `rate(hobnob_checkins_total[5m]) * 60` |
| gRPC Request Rate | Time series by method | `rate(hobnob_grpc_requests_total[5m])` |
| gRPC Latency (p50/p95/p99) | Time series | `histogram_quantile` on `hobnob_grpc_request_duration_seconds` |

### Row 4 — Convergence & Drift

| Panel | Type | Metric |
|-------|------|--------|
| Run Duration (p50/p95) | Time series | `histogram_quantile` on `hobnob_run_duration_seconds` |
| Drift Corrections / min | Time series | `rate(hobnob_drift_detected_total[5m]) * 60` |
| Module Actions | Time series by action | `rate(hobnob_module_actions_total[5m]) * 60` |

### Row 5 — Operations

| Panel | Type | Metric |
|-------|------|--------|
| Enrollments | Time series by status | `rate(hobnob_enrollments_total[1h]) * 60` |
| Task Runs | Time series by status | `rate(hobnob_task_runs_total[5m]) * 60` |
| Workflow Runs | Time series by status | `rate(hobnob_workflow_runs_total[5m]) * 60` |
| Signature Verification Failures | Stat (green/red) | `hobnob_sig_verify_failed_total` |

### Row 6 — Infrastructure

| Panel | Type | Metric |
|-------|------|--------|
| Config Reload Duration | Time series | `hobnob_config_reload_duration_seconds` |
| Config Reload Errors | Stat (green/red) | `hobnob_config_reload_errors_total` |
| FleetIndex Flush Duration (p50/p95) | Time series | `histogram_quantile` on `hobnob_fleetindex_flush_duration_seconds` |
| FleetIndex Dirty Count | Time series | `hobnob_fleetindex_dirty_count` |

### Row 7 — Webhooks & SMTP

| Panel | Type | Metric |
|-------|------|--------|
| Webhook Deliveries / min | Time series by status | `rate(hobnob_webhook_deliveries_total[5m]) * 60` |
| Webhook Latency (p50/p95) | Time series | `histogram_quantile` on `hobnob_webhook_duration_seconds` |
| SMTP Sends | Time series by status | `rate(hobnob_smtp_sends_total[5m]) * 60` |

### Row 8 — Secrets

| Panel | Type | Metric |
|-------|------|--------|
| Secret Rotations | Stat | `hobnob_secret_rotations_total` |
| Secret Resolution Errors | Stat (green/red) | `hobnob_secret_resolution_errors_total` |

### Row 9 — AI Assistant

| Panel | Type | Metric |
|-------|------|--------|
| AI Requests / min | Time series | `rate(hobnob_ai_requests_total[5m]) * 60` |
| AI Tokens Used | Stat | `hobnob_ai_tokens_total` |
| AI Latency (p50/p95) | Time series | `histogram_quantile` on `hobnob_ai_request_duration_seconds` |
| AI Errors | Stat (green/red) | `hobnob_ai_errors_total` |

## Spanner Dashboard

[Download hobnob-spanner.json](grafana/hobnob-spanner.json)

Only populated on hub servers.

| Panel | Type | Metric |
|-------|------|--------|
| Spoke Availability | Stat with UP/DOWN mapping | `hobnob_spanner_spoke_up` by `spoke_id` |
| Spoke Latency (p50/p95) | Time series by spoke | `histogram_quantile` on `hobnob_spanner_spoke_latency_seconds` |

## Prometheus Configuration

Add the Hobnob server as a scrape target in `prometheus.yml`:

```yaml
scrape_configs:
  - job_name: hobnob
    static_configs:
      - targets: ['hobnob-server:8443']
    metrics_path: /metrics
    scrape_interval: 30s
```

## Suggested Alerts

```yaml
groups:
  - name: hobnob
    rules:
      - alert: HobnobComplianceLow
        expr: hobnob_compliance_pct < 90
        for: 10m
        labels: { severity: warning }
        annotations:
          summary: "Fleet compliance below 90%"

      - alert: HobnobLicenseExpiring
        expr: hobnob_license_days_remaining < 30 and hobnob_license_days_remaining > 0
        for: 1h
        labels: { severity: warning }
        annotations:
          summary: "License expires in {{ $value }} days"

      - alert: HobnobLicenseOverLimit
        expr: hobnob_license_stage > 0
        for: 5m
        labels: { severity: critical }
        annotations:
          summary: "License enforcement active (stage {{ $value }})"

      - alert: HobnobConfigReloadFailed
        expr: increase(hobnob_config_reload_errors_total[5m]) > 0
        labels: { severity: warning }
        annotations:
          summary: "Config reload failed"

      - alert: HobnobSignatureVerifyFailed
        expr: increase(hobnob_sig_verify_failed_total[5m]) > 0
        labels: { severity: critical }
        annotations:
          summary: "ED25519 signature verification failures detected"

      - alert: HobnobSpokeDown
        expr: hobnob_spanner_spoke_up == 0
        for: 5m
        labels: { severity: critical }
        annotations:
          summary: "Spanner spoke {{ $labels.spoke_id }} is down"
```
