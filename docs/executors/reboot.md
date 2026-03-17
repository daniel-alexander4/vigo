# reboot

Triggers or cancels system reboots. Supports delayed reboots and conditional reboots based on a "reboot required" sentinel file.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `state` | No | `present` | `present` to trigger a reboot, `absent` to cancel a pending reboot. |
| `delay_secs` | No | `0` | Delay in minutes before the reboot (passed to `shutdown -r +N`). |
| `message` | No | `System reboot triggered by Hobnob` | Message displayed to logged-in users. |
| `reboot_required_file` | No | -- | Path to a sentinel file. If set, the reboot only triggers when this file exists. Commonly `/var/run/reboot-required` on Debian/Ubuntu. |

## States

- **`present`** -- Trigger a system reboot. If `reboot_required_file` is set and the file does not exist, skip the reboot.
- **`absent`** -- Cancel a pending reboot via `shutdown -c`. If no reboot is pending, this is a no-op.

## Idempotency

When `reboot_required_file` is set, the executor checks for the file's existence before triggering a reboot. If the file is absent, the reboot is skipped (`Action::None`). Without the sentinel file, reboots always trigger.

## Examples

### Basic

```yaml
resources:
  - name: Reboot after kernel update
    type: reboot
    reboot_required_file: /var/run/reboot-required
```

### Delayed reboot with message

```yaml
resources:
  - name: Scheduled maintenance reboot
    type: reboot
    delay_secs: "5"
    message: "System rebooting for maintenance in 5 minutes"
```

### Cancel a pending reboot

```yaml
resources:
  - name: Cancel reboot
    type: reboot
    state: absent
```

### With depends_on

```yaml
resources:
  - name: Kernel update
    type: package
    package: linux-image-generic
    state: latest

  - name: Reboot if needed
    type: reboot
    reboot_required_file: /var/run/reboot-required
    depends_on: Kernel update
```

## Platform

Cross-platform. Uses `shutdown -r` on Linux/macOS.

## Notes

- The reboot command is `shutdown -r +<delay_secs> <message>`.
- Cancel uses `shutdown -c`. If no reboot is pending, the command may fail, but this is treated as a successful no-op.
- Use `reboot_required_file` to make reboots conditional -- only reboot when the OS indicates it is needed (e.g., after kernel or library updates).
