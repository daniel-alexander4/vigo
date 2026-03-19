---
title: Remediation Workflow
---

# Remediation Workflow

The remediation system detects non-compliant nodes (recurring drift or errors) and triggers automatic reconvergence. This implements the HIPAA requirement for corrective action when configuration drift is detected.

## How It Works

1. **Detect** — scans the fleet for nodes with `recurring drift` or `errors` status
2. **Fix** — triggers force-push on each non-compliant node, causing immediate reconvergence
3. **Verify** — monitors nodes for up to 5 minutes until they reach `compliant` status
4. **Report** — records results and audit trail entries

## CLI Commands

### Check remediation posture

```bash
vigogo remediation status
```

Returns the number of nodes needing remediation and a breakdown by status:

```json
{
  "needs_remediation": 3,
  "recurring_drift": 2,
  "errors": 1,
  "total": 25,
  "compliant": 22
}
```

### Preview remediation targets (dry run)

```bash
vigogo remediation run --dry-run
```

Shows which nodes would be remediated without taking action:

```json
{
  "status": "dry_run",
  "count": 3,
  "targets": [
    {"hostname": "web-01.prod", "envoy_id": "abc123", "initial_status": "recurring drift", "action": "would_force_push"},
    {"hostname": "db-02.prod", "envoy_id": "def456", "initial_status": "errors", "action": "would_force_push"}
  ]
}
```

### Execute remediation

```bash
vigogo remediation run
```

Triggers force-push reconvergence on all non-compliant nodes. Returns immediately with a run ID:

```json
{
  "id": "rem-1710720000000000000",
  "status": "running",
  "targets": 3
}
```

### List remediation runs

```bash
vigogo remediation list
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
    {"hostname": "web-01.prod", "initial_status": "recurring drift", "action": "force_push", "final_status": "compliant"},
    {"hostname": "web-02.prod", "initial_status": "recurring drift", "action": "force_push", "final_status": "compliant"},
    {"hostname": "db-02.prod", "initial_status": "errors", "action": "force_push", "final_status": "compliant"}
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
| `complete` | All nodes remediated successfully |
| `partial` | Some nodes remediated, others timed out or still non-compliant |
| `no_action` | No nodes needed remediation |

## Target Statuses

| Final Status | Meaning |
|-------------|---------|
| `compliant` | Node reconverged successfully |
| `recurring drift` | Node checked in but still drifting |
| `errors` | Node checked in but convergence failed |
| `timeout` | Node did not check in within 5 minutes |
| `unknown` | Node was removed from fleet during remediation |

## Integration with Compliance Reporting

The remediation system works alongside compliance reports to form a closed loop:

1. Generate a compliance report: `vigogo report compliance`
2. Review non-compliant nodes in the report
3. Run remediation: `vigogo remediation run`
4. Generate a follow-up report to verify improvement

For automated compliance workflows, chain these commands:

```bash
#!/bin/bash
# Weekly compliance cycle
vigogo report compliance --format html --output /reports/pre-remediation-$(date +%Y%m%d).html
vigogo remediation run
sleep 300  # wait for reconvergence
vigogo report compliance --format html --output /reports/post-remediation-$(date +%Y%m%d).html
```

## SLA Tracking

Track remediation effectiveness over time by comparing pre- and post-remediation reports. Key metrics:

- **Mean time to remediate** — time between `remediation.start` and `remediation.complete` audit events
- **Remediation success rate** — percentage of targets reaching `compliant` status
- **Recurring offenders** — nodes that appear in multiple remediation runs (investigate root cause)

Query these from the audit trail:

```bash
# Recent remediation events
vigogo audit list --type remediation.start --since 30d
vigogo audit list --type remediation.complete --since 30d
```
