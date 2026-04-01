
# Drift Alerting

The convergence monitor checks fleet health every 60 seconds and fires alerts when convergence drops below a configurable threshold.

## Configuration

Set the threshold in `server.yaml` under the SMTP events section:

```yaml
smtp:
  host: smtp.example.com
  from: vigo@example.com
  recipients: [ops@example.com]
  events:
    convergence.threshold:
      threshold: 95  # alert when convergence drops below 95%
```

The threshold is the percentage of non-revoked envoys that must be compliant. If not configured, no convergence threshold alerts are sent.

## Behavior

1. Every 60 seconds, the monitor calculates `compliant_envoys / total_envoys * 100`
2. If the percentage drops below the threshold, an alert fires via:
   - Webhook (`convergence.threshold` event)
   - SMTP email notification
3. The alert fires **once** per breach. It does not repeat until convergence recovers and then drops again.
4. When convergence recovers above the threshold, the alert state resets.

## Investigating Drift

When an alert fires, use the CLI to see which resources are causing drift:

```bash
vigocli runs drift
```

This shows changed and failed resources from each envoy's most recent run. Filter to a single envoy with `--envoy <hostname>`.

## Webhook Payload

```json
{
  "event": "convergence.threshold",
  "timestamp": "2026-03-13T14:30:00Z",
  "details": {
    "percentage": 82.5,
    "threshold": 95,
    "total": 40,
    "compliant": 33,
    "drifted": 3,
    "failing": 2,
    "stale": 1,
    "unknown": 1
  }
}
```
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
