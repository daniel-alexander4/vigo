
---
title: Remediation Workflow
---

# Remediation Workflow

The remediation system detects non-compliant nodes (relapsed, diverged, or errors) and triggers automatic reconvergence. This implements the HIPAA requirement for corrective action when configuration drift is detected.

## How It Works

1. **Detect** — scans the fleet for nodes with `relapsed`, `diverged`, or `errors` status
2. **Fix** — triggers force-convergence on each non-compliant node, causing immediate reconvergence
3. **Verify** — monitors nodes for up to 5 minutes until they reach `converged` status
4. **Report** — records results and audit trail entries

## CLI Commands

### Check remediation posture

```bash
vigocli remediation status
```

Returns the number of nodes needing remediation and a breakdown by status:

```json
{
  "needs_remediation": 3,
  "relapsed": 1,
  "diverged": 1,
  "errors": 1,
  "total": 25,
  "converged": 22
}
```

### Preview remediation targets (dry run)

```bash
vigocli remediation run --dry-run
```

Shows which nodes would be remediated without taking action:

```json
{
  "status": "dry_run",
  "count": 3,
  "targets": [
    {"hostname": "web-01.prod", "envoy_id": "abc123", "initial_status": "relapsed", "action": "would_force_push"},
    {"hostname": "db-02.prod", "envoy_id": "def456", "initial_status": "errors", "action": "would_force_push"}
  ]
}
```

### Execute remediation

```bash
vigocli remediation run
```

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*

---

Triggers force-convergence on all non-compliant nodes. Returns immediately with a run ID:

```json
{
  "id": "rem-1710720000000000000",
  "status": "running",
  "targets": 3
}
```

### List remediation runs

```bash
vigocli remediation list
```

### View remediation run details

The run tracks per-node results:

```json
{
  "id": "rem-1710720000000000000",
  "status": "complete",
  "operator": "admin",
  "started_at": "2026-03-18T10:00:00Z",
  "finished_at": "2026-03-18T10:02:30Z",
  "summary": "3/3 nodes remediated",
  "targets": [
    {"hostname": "web-01.prod", "initial_status": "relapsed", "action": "force_push", "final_status": "converged"},
    {"hostname": "web-02.prod", "initial_status": "diverged", "action": "force_push", "final_status": "converged"},
    {"hostname": "db-02.prod", "initial_status": "errors", "action": "force_push", "final_status": "converged"}
  ]
}
```

## REST API

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/api/v1/remediation/status` | GET | Fleet remediation posture |
| `/api/v1/remediation/run` | POST | Trigger remediation (`{"dry_run": true}` for preview) |
| `/api/v1/remediation/runs` | GET | List remediation runs |
| `/api/v1/remediation/runs/{id}` | GET | Get remediation run details |

## Audit Trail

Two audit events are recorded for each remediation cycle:

| Event | When | Details |
|-------|------|---------|
| `remediation.start` | Run begins | Actor, target count |
| `remediation.complete` | Run finishes | Summary (e.g., "3/3 nodes remediated") |

## Run Statuses

| Status | Meaning |
|--------|---------|
| `running` | Remediation in progress, waiting for nodes to reconverge |

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*

---
| `complete` | All nodes remediated successfully |
| `partial` | Some nodes remediated, others timed out or still non-compliant |
| `no_action` | No nodes needed remediation |

## Target Statuses

| Final Status | Meaning |
|-------------|---------|
| `converged` | Node reconverged successfully |
| `relapsed` | Node checked in but drifted back (2 consecutive) |
| `diverged` | Node checked in but persistently conflicting (3+ consecutive) |
| `errors` | Node checked in but convergence failed |
| `timeout` | Node did not check in within 5 minutes |
| `unknown` | Node was removed from fleet during remediation |

## Integration with Compliance Reporting

The remediation system works alongside compliance reports to form a closed loop:

1. Generate a compliance report: `vigocli report compliance`
2. Review non-compliant nodes in the report
3. Run remediation: `vigocli remediation run`
4. Generate a follow-up report to verify improvement

For automated compliance workflows, chain these commands:

```bash
#!/bin/bash
# Weekly compliance cycle
vigocli report compliance --format html --output /reports/pre-remediation-$(date +%Y%m%d).html
vigocli remediation run
sleep 300  # wait for reconvergence
vigocli report compliance --format html --output /reports/post-remediation-$(date +%Y%m%d).html
```

## SLA Tracking

Track remediation effectiveness over time by comparing pre- and post-remediation reports. Key metrics:

- **Mean time to remediate** — time between `remediation.start` and `remediation.complete` audit events
- **Remediation success rate** — percentage of targets reaching `compliant` status
- **Recurring offenders** — nodes that appear in multiple remediation runs (investigate root cause)

Query these from the audit trail:

```bash
# Recent remediation events
vigocli audit list --type remediation.start --since 30d
vigocli audit list --type remediation.complete --since 30d
```
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
