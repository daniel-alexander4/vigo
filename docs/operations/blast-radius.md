
# Blast Radius Limits

Vigo can automatically roll back a config publish when it causes fleet compliance to drop. This prevents a bad configuration change from propagating to the entire fleet.

## How It Works

1. An operator runs `vigogo config publish`, which syncs stage configs to `.live/` and triggers a config reload on the server.
2. The publish pipeline snapshots the current `.live/` directory before overwriting it, so the previous version is always available for rollback.
3. After a successful reload, if `publish.compliance_threshold` is set, the server starts a **compliance monitor** that polls the FleetIndex every 5 seconds.
4. If the fleet-wide compliant percentage drops below the threshold at any point during the rollback window, the server:
   - Restores the previous config snapshot from the config-history directory.
   - Reloads the config store so envoys receive the old (known-good) configuration on their next check-in.
   - Logs the rollback event via slog.
5. If the rollback window elapses without a compliance drop, the monitor stops and the new config is considered safe.

## Configuration

Add the `publish` section to `server.yaml`:

```yaml
publish:
  compliance_threshold: 80   # minimum fleet compliance % (0 = disabled)
  rollback_window: "5m"      # how long to watch after reload (default: 5m)
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `compliance_threshold` | int | `0` (disabled) | Minimum acceptable fleet-wide compliance percentage (1-100). Set to 0 to disable blast radius monitoring. |
| `rollback_window` | string | `"5m"` | Duration to watch compliance after each config reload. Uses Go duration syntax (e.g. `"5m"`, `"10m"`, `"1h"`). |

## Behavior Details

- **Only one monitor runs at a time.** If a new config publish triggers a reload while a previous monitor is still active, the old monitor is cancelled and a new one starts.
- **Compliance is measured as `(compliant / total) * 100`.** The `compliant` count comes from the FleetIndex pre-computed compliance counters.
- **Empty fleets are skipped.** If no envoys are enrolled, the monitor does not trigger a rollback.
- **Rollback restores the second-most-recent snapshot.** Each publish creates a snapshot. The most recent snapshot is the one that caused the problem, so the monitor restores the one before it.
- **Config reload happens automatically after rollback.** The config store is reloaded so envoys pick up the restored configuration on their next check-in.

## Example Scenario

1. Fleet has 100 envoys, 95 compliant (95%).
2. Operator publishes a config change that introduces a syntax error in a module used by 30 envoys.
3. Threshold is set to 80%.
4. Within the 5-minute window, those 30 envoys check in, fail to converge, and become "failing".
5. Compliance drops to 65% (65/100).
6. The monitor detects `65 < 80`, triggers rollback.
7. Previous config is restored, config store reloads.
8. On their next check-in, the 30 failing envoys receive the old (working) config and return to compliant.

## Logging

The compliance monitor logs at every stage:

```
INFO  compliance monitor started                threshold=80 window=5m0s
INFO  compliance monitor: checking fleet        compliant_pct=95 threshold=80 compliant=95 total=100
WARN  compliance monitor: fleet compliance dropped below threshold, triggering rollback  compliant_pct=65 threshold=80
INFO  compliance monitor: rolling back to previous config  version=20260316T120000Z
INFO  compliance monitor: rollback complete, reloading config
```

If the window elapses without incident:

```
INFO  compliance monitor: rollback window elapsed, fleet remains healthy
```

## Limitations

- The monitor polls every 5 seconds, so brief transient dips shorter than 5 seconds may not be caught.
- Rollback requires at least two config snapshots in the history directory. On a fresh server with only one publish, rollback is not possible.
- The monitor watches compliance percentage, not individual envoy failures. A small number of envoys failing may not cross the threshold.
