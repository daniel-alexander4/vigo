# timezone

Sets the system timezone idempotently.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `timezone` | Yes | -- | Desired timezone (e.g., `America/New_York`, `UTC`). |

## States

The timezone executor does not use a `state` parameter. It always ensures the timezone matches the desired value.

## Idempotency

Reads the current timezone and compares it against the desired value. Only changes the timezone when they differ.

## Examples

### Basic

```yaml
resources:
  - name: Set timezone
    type: timezone
    timezone: America/New_York
```

### UTC

```yaml
resources:
  - name: Set timezone to UTC
    type: timezone
    timezone: UTC
```

## Platform

Linux only. Uses `timedatectl set-timezone` with a fallback to symlinking `/etc/localtime`. On Windows, the same `type: timezone` maps to the [timezone_windows](timezone_windows.md) executor.

## Notes

- Prefers `timedatectl` (systemd). Falls back to creating a symlink from `/etc/localtime` to `/usr/share/zoneinfo/<timezone>`.
- Timezone names follow the IANA Time Zone Database format (e.g., `America/New_York`, `Europe/London`).
