# timezone_windows

Sets the Windows timezone idempotently via `tzutil.exe`.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `timezone` | Yes | -- | Desired timezone in Windows format (e.g., `Eastern Standard Time`, `Pacific Standard Time`). |

## States

The timezone_windows executor does not use a `state` parameter. It always ensures the timezone matches the desired value.

## Idempotency

Reads the current timezone via `tzutil /g` and compares it against the desired value (exact string match). Only changes the timezone when they differ.

## Examples

### Basic

```yaml
resources:
  - name: Set timezone
    type: timezone
    timezone: "Eastern Standard Time"
```

### UTC

```yaml
resources:
  - name: Set timezone to UTC
    type: timezone
    timezone: "UTC"
```

## Platform

Windows only. On Linux, `type: timezone` maps to the [timezone](timezone.md) executor.

## Notes

- Uses Windows timezone names (e.g., `Eastern Standard Time`), not IANA names (e.g., `America/New_York`).
- To list available timezone names on Windows, run `tzutil /l`.
