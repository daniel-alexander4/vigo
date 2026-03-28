
# hostname

Sets the system hostname idempotently.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `hostname` | Yes | -- | Desired hostname. |

## States

The hostname executor does not use a `state` parameter. It always ensures the hostname matches the desired value.

## Idempotency

Reads the current hostname and compares it against the desired value. Only changes the hostname when they differ.

## Examples

### Basic

```yaml
resources:
  - name: Set hostname
    type: hostname
    hostname: web-01.example.com
```

### With when

```yaml
resources:
  - name: Set hostname
    type: hostname
    hostname: web-01.example.com
    when: "!is_container"
```

## Platform

Linux only. Uses `hostnamectl set-hostname` with a fallback to writing `/etc/hostname`. On Windows, the same `type: hostname` maps to the [hostname_windows](hostname_windows.md) executor.

## Notes

- Prefers `hostnamectl` (systemd). Falls back to writing `/etc/hostname` directly if hostnamectl is not available.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
