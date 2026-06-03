
# service_windows

Manages Windows services via `sc.exe`. Supports starting, stopping, restarting, and configuring startup type.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `service` | Yes | -- | Service name. Supports comma-separated names for multiple services. |
| `state` | Yes | -- | Desired state: `running`, `stopped`, or `restarted`. |
| `enabled` | No | -- | Startup type: `true` for auto-start, `false` for disabled. Empty means do not change. |

## States

- **`running`** -- Ensure the service is running. If stopped, start it.
- **`stopped`** -- Ensure the service is stopped. If running, stop it.
- **`restarted`** -- Stop (if running) then start the service. Always reports `changed: true`.

## Idempotency

Queries the service state via `sc.exe query` and startup type via `sc.exe qc`. Only acts when the current state or startup type differs from desired.

## Examples

### Basic

```yaml
resources:
  - name: Ensure W3SVC running
    type: service
    service: W3SVC
    state: running
    enabled: "true"
```

### Stop and disable

```yaml
resources:
  - name: Disable print spooler
    type: service
    service: Spooler
    state: stopped
    enabled: "false"
```

### Multiple services

```yaml
resources:
  - name: Start web services
    type: service
    service: "W3SVC,WAS"
    state: running
```

## Platform

Windows only. On Linux, `type: service` maps to the [service](service.md) executor (systemd).

## Notes

- Uses `sc.exe start`, `sc.exe stop` for state changes.
- Uses `sc.exe config <name> start=auto` / `start=disabled` for startup type.
- When restarting, a 2-second delay is inserted between stop and start to allow the service to fully stop.
- If the service is not found, an error is returned.
- Comma-separated service names are processed individually.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../../../legal/license.md).*
