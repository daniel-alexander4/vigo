
# Grafana Dashboards

Pre-built Grafana dashboards for monitoring Vigo via Prometheus.

## Download

| Dashboard | Panels | Download |
|-----------|--------|----------|
| **Fleet Overview** | 40 panels across 11 rows: fleet stats, compliance breakdown, check-ins & gRPC latency, convergence & drift, operations, infrastructure, webhooks & SMTP, secrets, AI assistant, security posture, risk posture | [vigo-overview.json](grafana/vigo-overview.json) |
| **Security Posture** | 14 panels across 4 rows: CVE counts by severity, hardening score gauge + trend, threat detection (rootkits, malware, integrity), scan coverage | [vigo-security.json](grafana/vigo-security.json) |
| **Convergence** | 9 panels across 3 rows: status pie chart + stat counters, compliance % + drift rate trends, check-in age heatmap | [vigo-convergence.json](grafana/vigo-convergence.json) |
| **Risk Posture** | 8 panels across 3 rows: average + max risk scores, distribution by level (low/medium/high/critical), risk score trends over time | [vigo-risk.json](grafana/vigo-risk.json) |
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

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*

---
| Drift Corrections / min | Time series | `rate(vigo_drift_detected_total[5m]) * 60` |
| Module Actions | Time series by action | `rate(vigo_module_actions_total[5m]) * 60` |

### Row 5 — Operations

| Panel | Type | Metric |
|-------|------|--------|
| Enrollments | Time series by status | `rate(vigo_enrollments_total[1h]) * 60` |
| Task Runs | Time series by status | `rate(vigo_task_runs_total[5m]) * 60` |
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

### Row 10 — Security Posture

| Panel | Type | Metric |
|-------|------|--------|
| Critical CVEs | Stat (green/red) | `vigo_security_cve_critical` |
| High CVEs | Stat (green/orange) | `vigo_security_cve_high` |
| Avg Hardening Score | Stat (red < 50, yellow < 80, green) | `vigo_security_hardening_avg` |
| Rootkit Warnings | Stat (green/red) | `vigo_security_rootkit_warnings` |

### Row 11 — Risk Posture

| Panel | Type | Metric |
|-------|------|--------|
| Avg Risk Score | Stat (green < 20, yellow < 40, orange < 70, red) | `vigo_risk_avg_score` |
| Max Risk Score | Stat (green < 20, yellow < 40, orange < 70, red) | `vigo_risk_max_score` |

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*

---
| Low Risk | Stat (green) | `vigo_risk_distribution{level="low"}` |
| Medium Risk | Stat (blue) | `vigo_risk_distribution{level="medium"}` |
| High Risk | Stat (orange) | `vigo_risk_distribution{level="high"}` |
| Critical Risk | Stat (red) | `vigo_risk_distribution{level="critical"}` |

## Security Posture Dashboard

[Download vigo-security.json](grafana/vigo-security.json)

Dedicated security monitoring dashboard with CVE tracking, hardening scores, and threat detection.

### Row 1 — CVE Overview

| Panel | Type | Metric |
|-------|------|--------|
| Critical CVEs | Stat (green/red) | `vigo_security_cve_critical` |
| High CVEs | Stat (green/orange) | `vigo_security_cve_high` |
| Medium CVEs | Stat (green/yellow) | `vigo_security_cve_medium` |
| Low CVEs | Stat (blue) | `vigo_security_cve_low` |

### Row 2 — Hardening

| Panel | Type | Metric |
|-------|------|--------|
| Average Hardening Score | Gauge (0-100, red < 50, yellow < 80, green) | `vigo_security_hardening_avg` |
| Hardening Score Over Time | Time series | `vigo_security_hardening_avg` |

### Row 3 — Threat Detection

| Panel | Type | Metric |
|-------|------|--------|
| Rootkit Warnings | Stat (green/red) | `vigo_security_rootkit_warnings` |
| Malware Detections | Stat (green/red) | `vigo_security_malware_detections` |
| Integrity Issues | Stat (green/orange) | `vigo_security_integrity_issues` |

### Row 4 — Coverage

| Panel | Type | Metric |
|-------|------|--------|
| Scanned Hosts | Stat (green) | `vigo_security_scanned_hosts` |
| Total Nodes | Stat (blue) | `vigo_nodes_total` |

## Convergence Dashboard

[Download vigo-convergence.json](grafana/vigo-convergence.json)

Per-envoy convergence status breakdown, compliance trends, and check-in health.

### Row 1 — Status Breakdown

| Panel | Type | Metric |
|-------|------|--------|
| Convergence Status | Pie chart (green/yellow/red/gray) | `vigo_convergence_converged`, `_drifted`, `_errors`, `_lost_contact`, `_no_data` |
| Converged | Stat (green) | `vigo_convergence_converged` |
| Drifted | Stat (yellow) | `vigo_convergence_drifted` |
| Errors | Stat (green/red) | `vigo_convergence_errors` |
| Lost Contact | Stat (green/orange) | `vigo_convergence_lost_contact` |
| No Data | Stat | `vigo_convergence_no_data` |

### Row 2 — Trends

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*

---

| Panel | Type | Metric |
|-------|------|--------|
| Compliance % | Time series (0-100) | `vigo_convergence_pct * 100` |
| Drift Rate | Time series | `rate(vigo_drift_detected_total[5m])` |

### Row 3 — Check-in Health

| Panel | Type | Metric |
|-------|------|--------|
| Check-in Age Distribution | Heatmap | `vigo_checkin_age_seconds_bucket` |

## Risk Posture Dashboard

[Download vigo-risk.json](grafana/vigo-risk.json)

Fleet-wide risk scoring with distribution breakdown and trend tracking.

### Row 1 — Fleet Risk

| Panel | Type | Metric |
|-------|------|--------|
| Average Risk Score | Stat (green < 20, yellow < 40, orange < 70, red) | `vigo_risk_avg_score` |
| Max Risk Score | Gauge (0-100, green < 20, yellow < 40, orange < 70, red) | `vigo_risk_max_score` |

### Row 2 — Distribution

| Panel | Type | Metric |
|-------|------|--------|
| Low Risk | Stat (green) | `vigo_risk_distribution{level="low"}` |
| Medium Risk | Stat (blue) | `vigo_risk_distribution{level="medium"}` |
| High Risk | Stat (orange) | `vigo_risk_distribution{level="high"}` |
| Critical Risk | Stat (red) | `vigo_risk_distribution{level="critical"}` |

### Row 3 — Trends

| Panel | Type | Metric |
|-------|------|--------|
| Risk Score Over Time | Time series (average + maximum) | `vigo_risk_avg_score`, `vigo_risk_max_score` |

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
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
