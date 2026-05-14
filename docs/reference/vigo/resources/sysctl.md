
# sysctl

Manages kernel parameters via `sysctl`, with optional persistence to `/etc/sysctl.d/`.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `key` | Yes | -- | Sysctl key name (e.g., `net.ipv4.ip_forward`). |
| `value` | Yes (for present) | -- | Desired value for the key. |
| `state` | No | `present` | Desired state: `present` or `absent`. |
| `persist` | No | `true` | If `true`, write the setting to `/etc/sysctl.d/99-vigo-<key>.conf` for persistence across reboots. |

## States

- **`present`** -- Ensure the kernel parameter has the specified value, both at runtime and (optionally) persisted.
- **`absent`** -- Remove the persisted configuration file. Does not change the runtime value.

## Idempotency

Reads the current runtime value via `sysctl -n <key>` and the persisted file if applicable. Only modifies when the current value differs from the desired value or the persistence file is missing/stale.

## Examples

### Basic

```yaml
resources:
  - name: Enable IP forwarding
    type: sysctl
    key: net.ipv4.ip_forward
    value: "1"
```

### Without persistence

```yaml
resources:
  - name: Increase somaxconn (runtime only)
    type: sysctl
    key: net.core.somaxconn
    value: "65535"
    persist: "false"
```

### Remove persisted setting

```yaml
resources:
  - name: Remove custom sysctl
    type: sysctl
    key: net.ipv4.tcp_tw_reuse
    state: absent
```

### With when

```yaml
resources:
  - name: Enable IP forwarding
    type: sysctl
    key: net.ipv4.ip_forward
    value: "1"
    when: "!is_container"
```

## Platform

Linux only.

## Notes

- Runtime values are set with `sysctl -w <key>=<value>`.
- Persistence files are written to `/etc/sysctl.d/99-vigo-<key>.conf`.
- When `state: absent`, only the persistence file is removed. The runtime value is not reverted.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../../legal/license.md).*
