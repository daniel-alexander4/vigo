
# cron

Manages cron jobs by writing configuration files to `/etc/cron.d/`.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `name` | Yes | -- | Cron job name (used as the filename in `/etc/cron.d/`). |
| `command` | Yes (for present) | -- | The command to execute. |
| `state` | No | `present` | Desired state: `present` or `absent`. |
| `schedule` | No | -- | Cron schedule expression (e.g., `*/5 * * * *`) or special schedule (`@daily`, `@hourly`, `@reboot`, etc.). Takes precedence over individual time fields. |
| `minute` | No | `*` | Minute field (0-59). |
| `hour` | No | `*` | Hour field (0-23). |
| `day` | No | `*` | Day of month field (1-31). |
| `month` | No | `*` | Month field (1-12). |
| `weekday` | No | `*` | Day of week field (0-7, where 0 and 7 are Sunday). |
| `user` | No | `root` | User to run the job as. |
| `environment` | No | -- | Comma-separated `KEY=VALUE` pairs written as env lines before the schedule. |

## States

- **`present`** -- Ensure the cron job exists with the specified schedule and command.
- **`absent`** -- Remove the cron job file.

## Idempotency

Reads the existing cron file in `/etc/cron.d/` and compares its content against the desired configuration. Uses SHA-256 hashing for comparison. Only writes when the content differs.

## Examples

### Basic

```yaml
resources:
  - name: cleanup-tmp
    type: cron
    name: cleanup-tmp
    command: "find /tmp -type f -mtime +7 -delete"
    schedule: "0 2 * * *"
```

### Using individual time fields

```yaml
resources:
  - name: backup
    type: cron
    name: backup
    command: "/opt/backup.sh"
    minute: "0"
    hour: "3"
    user: backup
```

### Every 5 minutes

```yaml
resources:
  - name: health-check
    type: cron
    name: health-check
    command: "/opt/check-health.sh"
    schedule: "*/5 * * * *"
```

### Run at boot

```yaml
resources:
  - name: mount-data
    type: cron
    name: mount-data
    command: "/opt/mount-data.sh"
    schedule: "@reboot"
```

### With environment variables

```yaml
resources:
  - name: etl-job
    type: cron
    name: etl-job
    command: "/opt/run-etl.sh"
    schedule: "0 4 * * *"
    environment: "RAILS_ENV=production,PATH=/usr/local/bin:/usr/bin"
```

### Remove a cron job

```yaml
resources:
  - name: old-job
    type: cron
    name: old-job
    state: absent
```

## Platform

Linux only. On Windows, the same `type: cron` maps to the [cron_windows](cron_windows.md) executor.

## Notes

- Cron files are written to `/etc/cron.d/<name>` with a comment header identifying Vigo as the manager.
- When both `schedule` and individual time fields are provided, `schedule` takes precedence.
- The cron file format includes the user field: `<schedule> <user> <command>`.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
