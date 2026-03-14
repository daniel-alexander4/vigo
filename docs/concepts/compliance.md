# Compliance

Compliance status reflects whether a thrall is in its desired state. It's computed from the most recent convergence run and check-in timing.

## Status Levels

| Status | Meaning |
|--------|---------|
| **compliant** | Last run succeeded with zero changes and zero failures |
| **drifted** | Last run succeeded but made changes (state had drifted from desired) |
| **failing** | Last run had one or more resource failures |
| **stale** | Thrall hasn't checked in within 2× the check-in interval |
| **unknown** | No runs recorded yet (newly enrolled thrall) |

## Computation

```
                    ┌─────────────────┐
                    │  Last check-in  │
                    │  > 2× interval? │
                    └────────┬────────┘
                        yes  │  no
                        ▼    │
                    ┌────────┐  │
                    │ STALE  │  │
                    └────────┘  ▼
                    ┌─────────────────┐
                    │  Has run data?  │
                    └────────┬────────┘
                         no  │  yes
                         ▼   │
                    ┌────────┐  │
                    │UNKNOWN │  │
                    └────────┘  ▼
                    ┌─────────────────┐
                    │  Any failures?  │
                    └────────┬────────┘
                        yes  │  no
                        ▼    │
                    ┌────────┐  │
                    │FAILING │  │
                    └────────┘  ▼
                    ┌─────────────────┐
                    │  Any changes?   │
                    └────────┬────────┘
                        yes  │  no
                        ▼    ▼
                    ┌────────┐ ┌───────────┐
                    │DRIFTED │ │COMPLIANT  │
                    └────────┘ └───────────┘
```

## Fleet-Wide Compliance

The dashboard shows fleet-wide compliance as a percentage:

```
Compliance % = (compliant thralls / total active thralls) × 100
```

Only non-revoked thralls are counted. The compliance gauge on the dashboard and the `/compliance` page show the breakdown by status.

## Compliance Threshold Alerts

Configure an SMTP alert when compliance drops below a threshold:

```yaml
smtp:
  events:
    compliance.threshold:
      threshold: 90  # fire when compliance drops below 90%
```

The checker runs at the check-in interval frequency and fires once per transition (not repeatedly).

## Per-Resource Compliance

Each resource in a run reports one of:
- **ok** — state already matched, no action taken
- **changed** — drift detected, state corrected
- **failed** — could not apply desired state

A thrall is compliant only when all resources report "ok" — meaning the system was already in its desired state and no changes were needed.

## Stale Detection

A thrall is stale when it hasn't checked in within 2× the check-in interval:

```yaml
checkin:
  interval: "5m"  # stale threshold = 10 minutes
```

Stale thralls trigger `thrall.stale` SMTP notifications on the first transition to stale (not on every check).

## Compliance History

The web UI shows compliance trends over time via sparkline charts and the drift history timeline on the thrall detail page.

## CLI

```bash
# Fleet compliance summary
hobnobcli doctor

# Per-thrall compliance
hobnobcli thralls list --status failing
hobnobcli thralls list --status drifted
hobnobcli thralls list --status stale
```

## Related

- [Check-in Lifecycle](check-in-lifecycle.md) — How runs are created
- [Resource DAG](resource-dag.md) — How resource failures cascade
- [Email Notifications](../operations/smtp.md) — Compliance threshold alerts
