
# hostname_windows

Sets the Windows hostname idempotently using PowerShell's `Rename-Computer` cmdlet.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `hostname` | Yes | -- | Desired hostname. |

## States

The hostname_windows executor does not use a `state` parameter. It always ensures the hostname matches the desired value.

## Idempotency

Reads the current hostname via the `hostname` command and performs a case-insensitive comparison against the desired value. Only changes the hostname when they differ.

## Examples

### Basic

```yaml
resources:
  - name: Set hostname
    type: hostname
    hostname: WEB-01
```

## Platform

Windows only. On Linux, `type: hostname` maps to the [hostname](hostname.md) executor.

## Notes

- Uses `Rename-Computer -NewName '<hostname>' -Force` via PowerShell.
- A reboot may be required for the hostname change to take full effect.
- Hostname comparison is case-insensitive.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../../../legal/license.md).*
