# Grafana Dashboards

Pre-built Grafana dashboards for monitoring Hobnob via Prometheus.

## Dashboards

| File | Description |
|------|-------------|
| `hobnob-overview.json` | Fleet overview — compliance, check-ins, gRPC, runs, license, AI, secrets, webhooks |
| `hobnob-federation.json` | Hub-spoke federation — spoke availability and latency |

## Import

1. Open Grafana (typically http://localhost:3000)
2. Go to **Dashboards → Import**
3. Upload the JSON file or paste its contents
4. Select your Prometheus datasource when prompted

## Prometheus Configuration

Add the Hobnob server as a scrape target in `prometheus.yml`:

```yaml
scrape_configs:
  - job_name: hobnob
    static_configs:
      - targets: ['hobnob-server:8181']
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
        expr: hobnob_federation_spoke_up == 0
        for: 5m
        labels: { severity: critical }
        annotations:
          summary: "Federation spoke {{ $labels.spoke_id }} is down"
```
