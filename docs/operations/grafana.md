
# Grafana Dashboards

Pre-built Grafana dashboards for monitoring Vigo via Prometheus.

## Download

| Dashboard | Panels | Download |
|-----------|--------|----------|
| **Fleet Overview** | 28 panels across 7 rows: fleet stats, compliance breakdown, check-ins & gRPC latency, convergence & drift, operations (tasks/workflows/enrollments), infrastructure (config reload, FleetIndex flush), webhooks & SMTP, secrets, AI assistant | [vigo-overview.json](grafana/vigo-overview.json) |
| **Spanner** | 2 panels: spoke availability (UP/DOWN status map) and spoke latency (p50/p95 time series) | [vigo-spanner.json](grafana/vigo-spanner.json) |

## Import

1. Download the JSON file using the link above (or from the docs browser at `/docs/operations/grafana/vigo-overview.json`)
2. Open Grafana (typically http://localhost:3000)
3. Go to **Dashboards → Import**
4. Upload the JSON file or paste its contents
5. Select your Prometheus datasource when prompted

## Fleet Overview Dashboard

[Download vigo-overview.json](grafana/vigo-overview.json)

### Row 1 — Fleet Overview

| Panel | Type | Metric |
|-------|------|--------|
| Total Nodes | Stat | `vigo_nodes_total` |
| Active Nodes | Stat | `vigo_nodes_active_total` |
| Compliance % | Gauge (0-100, red/yellow/green) | `vigo_compliance_pct` |
| Uptime | Stat | `vigo_uptime_seconds` |
| License Usage | Gauge (used vs max) | `vigo_license_envoys_used` / `vigo_license_envoys_max` |
| License Days Remaining | Stat (red < 30, yellow < 90) | `vigo_license_days_remaining` |
| Active Streams | Stat | `vigo_streams_active` |
| Database Size | Stat | `vigo_database_size_bytes` |

### Row 2 — Compliance Breakdown

| Panel | Type | Metric |
|-------|------|--------|
| Compliance Status by Type | Pie chart (green/yellow/red/gray) | `vigo_compliance_status` by `status` label |
| Compliance % Over Time | Time series | `vigo_compliance_pct` |

### Row 3 — Check-in & gRPC

| Panel | Type | Metric |
|-------|------|--------|
| Check-ins / min | Time series | `rate(vigo_checkins_total[5m]) * 60` |
| gRPC Request Rate | Time series by method | `rate(vigo_grpc_requests_total[5m])` |
| gRPC Latency (p50/p95/p99) | Time series | `histogram_quantile` on `vigo_grpc_request_duration_seconds` |

### Row 4 — Convergence & Drift

| Panel | Type | Metric |
|-------|------|--------|
| Run Duration (p50/p95) | Time series | `histogram_quantile` on `vigo_run_duration_seconds` |
| Drift Corrections / min | Time series | `rate(vigo_drift_detected_total[5m]) * 60` |
| Module Actions | Time series by action | `rate(vigo_module_actions_total[5m]) * 60` |

### Row 5 — Operations

| Panel | Type | Metric |
|-------|------|--------|
| Enrollments | Time series by status | `rate(vigo_enrollments_total[1h]) * 60` |
| Push Runs | Time series by status | `rate(vigo_push_runs_total[5m]) * 60` |
| Workflow Runs | Time series by status | `rate(vigo_workflow_runs_total[5m]) * 60` |
| Signature Verification Failures | Stat (green/red) | `vigo_sig_verify_failed_total` |

### Row 6 — Infrastructure

| Panel | Type | Metric |
|-------|------|--------|
| Config Reload Duration | Time series | `vigo_config_reload_duration_seconds` |
| Config Reload Errors | Stat (green/red) | `vigo_config_reload_errors_total` |
| FleetIndex Flush Duration (p50/p95) | Time series | `histogram_quantile` on `vigo_fleetindex_flush_duration_seconds` |
| FleetIndex Dirty Count | Time series | `vigo_fleetindex_dirty_count` |

### Row 7 — Webhooks & SMTP

| Panel | Type | Metric |
|-------|------|--------|
| Webhook Deliveries / min | Time series by status | `rate(vigo_webhook_deliveries_total[5m]) * 60` |
| Webhook Latency (p50/p95) | Time series | `histogram_quantile` on `vigo_webhook_duration_seconds` |
| SMTP Sends | Time series by status | `rate(vigo_smtp_sends_total[5m]) * 60` |

### Row 8 — Secrets

| Panel | Type | Metric |
|-------|------|--------|
| Secret Rotations | Stat | `vigo_secret_rotations_total` |
| Secret Resolution Errors | Stat (green/red) | `vigo_secret_resolution_errors_total` |

### Row 9 — AI Assistant

| Panel | Type | Metric |
|-------|------|--------|
| AI Requests / min | Time series | `rate(vigo_ai_requests_total[5m]) * 60` |
| AI Tokens Used | Stat | `vigo_ai_tokens_total` |
| AI Latency (p50/p95) | Time series | `histogram_quantile` on `vigo_ai_request_duration_seconds` |
| AI Errors | Stat (green/red) | `vigo_ai_errors_total` |

## Spanner Dashboard

[Download vigo-spanner.json](grafana/vigo-spanner.json)

Only populated on hub servers.

| Panel | Type | Metric |
|-------|------|--------|
| Spoke Availability | Stat with UP/DOWN mapping | `vigo_spanner_spoke_up` by `spoke_id` |
| Spoke Latency (p50/p95) | Time series by spoke | `histogram_quantile` on `vigo_spanner_spoke_latency_seconds` |

## Prometheus Configuration

Add the Vigo server as a scrape target in `prometheus.yml`:

```yaml
scrape_configs:
  - job_name: vigo
    static_configs:
      - targets: ['vigo-server:8443']
    metrics_path: /metrics
    scrape_interval: 30s
```

## Suggested Alerts

```yaml
groups:
  - name: vigo
    rules:
      - alert: VigoComplianceLow
        expr: vigo_compliance_pct < 90
        for: 10m
        labels: { severity: warning }
        annotations:
          summary: "Fleet compliance below 90%"

      - alert: VigoLicenseExpiring
        expr: vigo_license_days_remaining < 30 and vigo_license_days_remaining > 0
        for: 1h
        labels: { severity: warning }
        annotations:
          summary: "License expires in {{ $value }} days"

      - alert: VigoLicenseOverLimit
        expr: vigo_license_stage > 0
        for: 5m
        labels: { severity: critical }
        annotations:
          summary: "License enforcement active (stage {{ $value }})"

      - alert: VigoConfigReloadFailed
        expr: increase(vigo_config_reload_errors_total[5m]) > 0
        labels: { severity: warning }
        annotations:
          summary: "Config reload failed"

      - alert: VigoSignatureVerifyFailed
        expr: increase(vigo_sig_verify_failed_total[5m]) > 0
        labels: { severity: critical }
        annotations:
          summary: "ED25519 signature verification failures detected"

      - alert: VigoSpokeDown
        expr: vigo_spanner_spoke_up == 0
        for: 5m
        labels: { severity: critical }
        annotations:
          summary: "Spanner spoke {{ $labels.spoke_id }} is down"
```
