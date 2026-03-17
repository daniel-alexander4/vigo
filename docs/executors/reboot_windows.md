# reboot_windows

Manages Windows reboots idempotently by checking whether a reboot is pending before triggering one. Useful after feature installs, Windows Updates, or configuration changes that require a restart.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `when_pending` | No | `true` | Only reboot if a reboot is pending. Set to `false` to always reboot. |
| `message` | No | `Hobnob: scheduled reboot` | Message displayed to logged-in users before the reboot. |
| `delay_seconds` | No | `60` | Seconds to wait before the reboot begins. Must be a positive integer. |

## Idempotency

When `when_pending` is `true` (the default), the executor checks three well-known registry locations for pending reboot state:

- `HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Component Based Servicing\RebootPending`
- `HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\WindowsUpdate\Auto Update\RebootRequired`
- `HKLM:\SYSTEM\CurrentControlSet\Control\Session Manager` -- `PendingFileRenameOperations` value

If none of these indicators are present, the resource is skipped with no changes. If any are present, the reboot is triggered via `shutdown.exe /r /t <delay_seconds> /c "<message>" /f`.

When `when_pending` is `false`, the reboot is always triggered (useful for forced maintenance windows).

## Examples

### Reboot only if pending

```yaml
resources:
  - name: pending-reboot
    type: reboot_windows
```

### Reboot after feature install

```yaml
resources:
  - name: install-hyperv
    type: windows_feature
    feature: Microsoft-Hyper-V-All

  - name: reboot-after-hyperv
    type: reboot_windows
    message: "Rebooting to complete Hyper-V installation"
    delay_seconds: "120"
    depends_on: [install-hyperv]
```

### Forced reboot with short timeout

```yaml
resources:
  - name: maintenance-reboot
    type: reboot_windows
    when_pending: "false"
    message: "Scheduled maintenance reboot"
    delay_seconds: "30"
```

### Reboot after Windows Update

```yaml
resources:
  - name: post-update-reboot
    type: reboot_windows
    message: "Rebooting to apply Windows Updates"
    delay_seconds: "300"
```

## Platform

Windows only.

## Notes

- The `/f` flag is always passed to `shutdown.exe`, which forcefully closes running applications. Warn users via the `message` parameter.
- Place this resource last in your dependency chain (via `depends_on`) to ensure all other changes are applied before the reboot.
- After a reboot, the agent service starts automatically and resumes convergence. The reboot resource will be skipped on the next run if no new reboot is pending.
- The `delay_seconds` parameter gives logged-in users time to save work. Set to `0` for an immediate reboot.
