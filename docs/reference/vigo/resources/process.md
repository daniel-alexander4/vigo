
# process

Manages running processes -- ensures processes are running or stopped, with optional count enforcement and automatic restart.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `process` | Yes | -- | Process name to match. |
| `state` | No | `running` | Desired state: `running` or `stopped`. |
| `signal` | No | `TERM` | Signal to send when stopping processes. |
| `match_mode` | No | `exact` | Match mode: `exact` (process name equals) or `pattern` (regex match via `pgrep -f`). |
| `count` | No | -- | Exact number of matching processes expected. If the count differs, triggers action. |
| `min` | No | -- | Minimum number of matching processes. Below this triggers action. |
| `max` | No | -- | Maximum number of matching processes. Above this kills excess. |
| `restart_method` | No | -- | Restart method to use if the process is not running (e.g., `systemctl`). |
| `restart_unit` | No | -- | Systemd unit to restart via `systemctl restart`. |
| `kill_timeout` | No | -- | When `true`, kill excess processes that exceed the count/max threshold. |

## States

- **`running`** -- Ensure at least one matching process is running. If none found, attempt restart.
- **`stopped`** -- Ensure no matching processes are running. Sends the specified signal to kill them.

## Idempotency

Uses `pgrep` to check if matching processes exist. For `state: running`, if processes are found (and count constraints are satisfied), no action is taken. For `state: stopped`, if no matching processes exist, no action is taken.

## Examples

### Basic

```yaml
resources:
  - name: Ensure nginx is running
    type: process
    process: nginx
    state: running
    restart_unit: nginx
```

### Stop a process

```yaml
resources:
  - name: Stop legacy worker
    type: process
    process: legacy-worker
    state: stopped
    signal: TERM
```

### Pattern matching with count

```yaml
resources:
  - name: Ensure worker pool
    type: process
    process: "myapp-worker"
    match_mode: pattern
    min: "4"
    max: "8"
```

### With when

```yaml
resources:
  - name: Ensure sshd running
    type: process
    process: sshd
    state: running
    when: "!is_container"
```

## Platform

Cross-platform. Uses `pgrep`/`pkill` which must be available on the system.

## Notes

- The `restart_method` parameter specifies how to restart the process (e.g., `systemctl`). `restart_unit` is a convenience that specifies which systemd unit to restart.
- When `state: stopped`, the executor sends the specified signal using `pkill`.
- Count constraints (`count`, `min`, `max`) provide fine-grained control over process pool sizes.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../../legal/license.md).*
