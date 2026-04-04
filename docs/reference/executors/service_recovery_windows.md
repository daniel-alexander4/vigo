
# service_recovery_windows

Manages per-service failure recovery actions on Windows idempotently via `sc.exe`. Configures what happens when a Windows service crashes -- restart the service, run a command, reboot the machine, or take no action.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `service` | Yes | -- | Windows service name (e.g., `W3SVC`, `vigo-envoy`). |
| `first_failure` | No | `restart` | Action on first failure: `restart`, `run`, `reboot`, or `none`. |
| `second_failure` | No | Same as `first_failure` | Action on second failure. |
| `subsequent_failure` | No | Same as `first_failure` | Action on third and all subsequent failures. |
| `reset_period` | No | `86400` | Seconds before the failure counter resets (default: 24 hours). |
| `restart_delay` | No | `60000` | Milliseconds to wait before executing the restart/run/reboot action (default: 60 seconds). |
| `command` | When any action is `run` | -- | Command to execute for `run` actions. Required if any failure action is set to `run`. |

## States

This executor does not use a `state` parameter. It always ensures the service's recovery settings match the desired configuration.

## Idempotency

The executor queries current recovery settings via `sc.exe qfailure` before acting:

- Parses the reset period and all three failure action/delay pairs from the output.
- Compares each field against the desired values.
- If all settings match, no action is taken.
- If any setting differs, the full recovery configuration is applied via `sc.exe failure`.

When any action is `run`, the executor also sets the failure flag (`sc.exe failureflag`) to enable recovery actions even when the service exits with a non-zero code (not just on crash).

## Examples

### Restart on all failures with 30-second delay

```yaml
resources:
  - name: myapp-recovery
    type: service_recovery_windows
    service: MyAppService
    first_failure: restart
    restart_delay: "30000"
```

### Escalating recovery: restart, restart, then reboot

```yaml
resources:
  - name: critical-service-recovery
    type: service_recovery_windows
    service: CriticalService
    first_failure: restart
    second_failure: restart
    subsequent_failure: reboot
    restart_delay: "60000"
    reset_period: "3600"
```

### Run a diagnostic script on failure


```yaml
resources:
  - name: db-service-recovery
    type: service_recovery_windows
    service: SQLServer
    first_failure: run
    second_failure: restart
    subsequent_failure: restart
    command: "C:\\Scripts\\diagnose-db.bat"
    restart_delay: "120000"
```

### Disable recovery actions

```yaml
resources:
  - name: no-recovery
    type: service_recovery_windows
    service: TestService
    first_failure: none
    second_failure: none
    subsequent_failure: none
```

## Platform

Windows only.

## Notes

- Requires administrator privileges. The agent runs as SYSTEM when installed as a Windows Service.
- The `service` must already exist. Use a `service` resource with `depends_on` to ensure it is installed first.
- The `reset_period` controls how long (in seconds) Windows waits before resetting the failure counter back to zero. A value of `86400` (24 hours) is the Windows default.
- The `restart_delay` applies to `restart`, `run`, and `reboot` actions. For `none` actions, the delay is automatically set to `0`.
- The `reboot` action reboots the entire machine. Use with extreme caution and only as a last resort for critical services.
- The `run` action requires the `command` parameter and automatically enables the failure flag so the command runs on non-crash exits too.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
