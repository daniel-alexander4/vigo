
# hostname

Sets the system hostname idempotently.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `hostname` | Yes | -- | Desired hostname. |
| `revert` | No | `false` | When `true`, restore the hostname Vigo first captured on this node instead of applying. See [Reversal](#reversal). |

## States

The hostname executor does not use a `state` parameter. It always ensures the hostname matches the desired value.

## Reversal

`hostname` has no `state: absent` retraction — a host always *has* a hostname. Instead it is **reversible**: the agent snapshots the value it first finds on the node (write-once) before changing it, and `revert: true` restores that prior value.

- `revert: true` restores the snapshotted prior value, then reports settled on subsequent runs (idempotent — it never flaps).
- If no snapshot was captured on this node (the resource never applied here, or agent state was rebuilt), revert **fails loud** at apply rather than guessing a default — set the desired `hostname` explicitly instead.
- A normal (non-revert) apply **re-arms** reversal: it re-captures the current value as the new prior.

Removing the resource from config does **not** undo it — that only stops enforcement; the hostname Vigo set persists. Use `revert: true` to actively restore.

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
