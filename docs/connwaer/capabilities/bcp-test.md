
# Business Continuity Testing

| | |
|---|---|
| **Machine name** | `bcp_test` |
| **Controls** | FINRA-BCP-3, MGA-RGR-5.2, UKGC-RTS-5B |
| **Platforms** | All |
| **Timeout** | 2 hours |

## What it verifies

Executes a multi-step business continuity plan (BCP) test by running a sequence of shell commands and verifying each step's exit code. Steps run in order, and by default the test aborts on the first failure. This is an active test -- it runs real commands, not just configuration checks. Designed for FINRA business continuity, MGA resilience, and UKGC remote technical standards compliance.

## Parameters

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `steps` | array | (required) | List of step objects to execute in order. The capability is skipped if this is empty or not set. Each step object has the fields described below. |

### Step object fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `name` | string | `"step_N"` | Display name for the step. Defaults to `step_1`, `step_2`, etc. |
| `command` | string | (required) | Shell command to execute. Runs via `sh -c` on Unix or `cmd.exe /C` on Windows. Steps with an empty command are skipped. |
| `expected_exit_code` | integer | `0` | The exit code that indicates success. The step passes if the actual exit code matches this value. |
| `continue_on_failure` | boolean | `false` | If `true`, subsequent steps still run even if this step fails. If `false` (default), the entire BCP test aborts on this step's failure. |

## Thresholds

| Threshold | Type | Default | Description |
|-----------|------|---------|-------------|
| `rpo_target_seconds` | integer | (optional) | Recovery Point Objective target in seconds. If set, the total test duration is compared against this target and recorded as evidence. |
| `total_timeout_seconds` | integer | `7200` | Maximum total wall-clock time for all steps combined. If exceeded, remaining steps are aborted. |

## Verification logic

1. **Check configuration** -- If `steps` is empty or not set, the capability returns `Skipped` with an explanation.

2. **Execute steps** -- For each step in order:
   - Check whether the total elapsed time exceeds `total_timeout_seconds`. If so, mark the step as `aborted` and stop.
   - Run the step's `command` via the system shell.
   - Compare the actual exit code against `expected_exit_code`. If they match, the step passes; otherwise it fails.
   - Record the step name, status (pass/fail/error/skipped/aborted), exit code, duration, last 5 lines of stdout, and first 3 lines of stderr.
   - If the step failed and `continue_on_failure` is `false`, abort the entire test.

3. **RPO check** (if `rpo_target_seconds` is set) -- Compare total test duration against the RPO target and record whether it was met.

4. **Trend recording** -- Store `total_seconds`, `steps_passed`, and `steps_failed` in the trends database.

### Result

- **Pass** -- All steps completed with matching exit codes and the test was not aborted.
- **Fail** -- One or more steps failed or the test was aborted due to failure or timeout.

## Example config

```yaml
capabilities:
  bcp_test:
    schedule: "monthly"
    params:
      steps:
        - name: "Stop primary database"
          command: "systemctl stop postgresql"
        - name: "Promote standby"
          command: "/opt/db/promote-standby.sh"
        - name: "Verify application health"
          command: "curl -sf http://localhost:8080/health"
        - name: "Restore original primary"
          command: "/opt/db/restore-primary.sh"
          continue_on_failure: true
    thresholds:
      rpo_target_seconds: 300
      total_timeout_seconds: 3600
```

Failover test with a non-zero expected exit code for a chaos step:

```yaml
capabilities:
  bcp_test:
    schedule: "quarterly"
    params:
      steps:
        - name: "Kill application process"
          command: "pkill -9 myapp"
          expected_exit_code: 0
        - name: "Wait for auto-restart"
          command: "sleep 10 && systemctl is-active myapp"
        - name: "Validate data integrity"
          command: "/opt/scripts/integrity-check.sh"
          continue_on_failure: true
        - name: "Run smoke tests"
          command: "/opt/scripts/smoke-test.sh"
```
