
# timezone

Sets the system timezone idempotently.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `timezone` | Yes | -- | Desired timezone (e.g., `America/New_York`, `UTC`). |
| `revert` | No | `false` | When `true`, restore the timezone Vigo first captured on this node instead of applying. See [Reversal](#reversal). |

## States

The timezone executor does not use a `state` parameter. It always ensures the timezone matches the desired value.

## Reversal

`timezone` has no `state: absent` retraction — a host always *has* a timezone. Instead it is **reversible**: the agent snapshots the value it first finds on the node (write-once) before changing it, and `revert: true` restores that prior value.

- `revert: true` restores the snapshotted prior value, then reports settled on subsequent runs (idempotent — it never flaps).
- If no snapshot was captured on this node (the resource never applied here, or agent state was rebuilt), revert **fails loud** at apply rather than guessing a default — set the desired `timezone` explicitly instead.
- A normal (non-revert) apply **re-arms** reversal: it re-captures the current value as the new prior.

Removing the resource from config does **not** undo it — that only stops enforcement; the timezone Vigo set persists. Use `revert: true` to actively restore.

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
