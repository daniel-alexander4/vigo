
# cron_windows

Manages Windows scheduled tasks via `schtasks.exe`. Translates cron-style schedule expressions to `schtasks` equivalents.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `name` | Yes | -- | Scheduled task name. |
| `state` | Yes | -- | Desired state: `present` or `absent`. |
| `command` | Yes (for present) | -- | Command to execute. |
| `schedule` | No | `* * * * *` | Cron-style schedule expression or shortcut (`@hourly`, `@daily`, `@weekly`, `@monthly`, `@reboot`). |
| `user` | No | -- | Windows user to run the task as (passed to `/RU`). |

## Schedule Translation

Standard 5-field cron expressions are translated to `schtasks` equivalents:

| Cron Pattern | schtasks Translation |
|-------------|---------------------|
| `@hourly` | `/SC HOURLY` |
| `@daily` / `@midnight` | `/SC DAILY /ST 00:00` |
| `@weekly` | `/SC WEEKLY /ST 00:00` |
| `@monthly` | `/SC MONTHLY /D 1 /ST 00:00` |
| `@reboot` | `/SC ONSTART` |
| `*/N * * * *` | `/SC MINUTE /MO N` |
| `0 */N * * *` | `/SC HOURLY /MO N` |
| `M H * * *` | `/SC DAILY /ST HH:MM` |
| `0 H * * D` | `/SC WEEKLY /D day /ST HH:00` |
| `0 H D * *` | `/SC MONTHLY /D day /ST HH:00` |

## States

- **`present`** -- Ensure the scheduled task exists with the specified command and schedule. If it already exists, it is deleted and recreated.
- **`absent`** -- Remove the scheduled task.

## Idempotency

Checks if the task exists via `schtasks /Query /TN <name>`. For `state: present`, if the task exists, it is deleted and recreated to ensure the schedule and command match the desired configuration.

## Examples

### Daily backup

```yaml
resources:
  - name: daily-backup
    type: cron
    name: daily-backup
    command: "C:\\Scripts\\backup.bat"
    schedule: "@daily"
```

### Every 15 minutes

```yaml
resources:
  - name: health-check
    type: cron
    name: health-check
    command: "C:\\Scripts\\health-check.ps1"
    schedule: "*/15 * * * *"
```

### Weekly on Monday at 3am

```yaml
resources:
  - name: weekly-cleanup
    type: cron
    name: weekly-cleanup
    command: "C:\\Scripts\\cleanup.bat"
    schedule: "0 3 * * 1"
    user: SYSTEM
```

### Remove a task

```yaml
resources:
  - name: old-task
    type: cron
    name: old-task
    state: absent
```

## Platform

Windows only. On Linux, `type: cron` maps to the [cron](cron.md) executor.

## Notes

- Tasks are created with the `/F` (force) flag to allow overwriting.
- Unsupported or complex cron expressions that cannot be translated will produce an error.
- When a task exists and `state: present`, it is deleted and recreated to ensure accuracy.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../../../legal/license.md).*
