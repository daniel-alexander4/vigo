
# Compliance

Vigo computes convergence status per envoy and fleet-wide. Statuses: converged, relapsed, diverged, degraded, failed, offline, no data.

## Status Levels

| Status | Meaning |
|--------|---------|
| **converged** | Last run succeeded with zero changes and zero failures |
| **relapsed** | 2 consecutive runs with changes (drift detected and corrected, but recurring) |
| **diverged** | 3+ consecutive runs with changes (persistent conflict with external process) |
| **degraded** | Last run was partial — some modules failed but others succeeded |
| **failed** | Last run failed entirely — all modules errored |
| **offline** | Envoy hasn't checked in within 2x the base check-in interval |
| **no data** | No runs recorded yet (newly enrolled envoy) |

## Computation

![Compliance Status Decision Tree](img/compliance-states.svg)

Compliance status is computed from the most recent convergence run and check-in timing.

## Fleet-Wide Compliance

The dashboard shows fleet-wide convergence as a percentage of compliant envoys versus total active envoys. Only non-revoked envoys are counted. The convergence gauge on the dashboard and the `/compliance` page show the breakdown by status.

## Convergence Threshold Alerts

Configure an SMTP alert when convergence drops below a threshold:

```yaml
smtp:
  events:
    convergence.threshold:
      threshold: 90  # fire when convergence drops below 90%
```

The checker runs at the check-in interval frequency and fires once per transition (not repeatedly).

## Per-Resource Compliance

Each resource in a run reports one of:
- **ok** — state already matched, no action taken
- **changed** — drift detected, state corrected
- **failed** — could not apply desired state

A envoy is compliant only when all resources report "ok" — meaning the system was already in its desired state and no changes were needed.

## Stale Detection

A envoy is stale when it hasn't checked in within a configurable threshold relative to the check-in interval:

```yaml
checkin:
  interval: "5m"
```

Stale envoys trigger `envoy.stale` SMTP notifications on the first transition to stale (not on every check).

## Compliance History

The web UI shows convergence trends over time via sparkline charts on the dashboard, and the per-envoy run history on the envoy detail page.

## CLI

```bash
# Fleet convergence summary
vigocli doctor

# Per-envoy convergence
vigocli envoys list --status errors
vigocli envoys list --status relapsed
vigocli envoys list --status diverged
```

## Related

- [Check-in Lifecycle](../engineering/architecture/check-in-lifecycle.md) — How runs are created
- [Resource DAG](../engineering/architecture/resource-dag.md) — How resource failures cascade
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
