
# Scheduling

Connwaer capabilities run on configurable schedules via cron expressions or fixed intervals.

## Cron Expressions

Standard 5-field format: `minute hour day-of-month month day-of-week`

| Field | Range | Description |
|-------|-------|-------------|
| Minute | 0-59 | Minute of the hour |
| Hour | 0-23 | Hour of the day |
| Day of month | 1-31 | Day of the month |
| Month | 1-12 | Month of the year |
| Day of week | 0-6 | Day of the week (0 = Sunday) |

### Field Syntax

| Syntax | Meaning | Example |
|--------|---------|---------|
| `*` | Every value | `* * * * *` (every minute) |
| `N` | Specific value | `30 * * * *` (minute 30) |
| `N,M` | List | `0,15,30,45 * * * *` |
| `N-M` | Range | `0 9-17 * * *` (9 AM to 5 PM) |
| `*/N` | Step from start | `*/15 * * * *` (every 15 minutes) |
| `N-M/S` | Range with step | `0 0-23/6 * * *` (every 6 hours) |

### Shortcuts

| Shortcut | Equivalent | Description |
|----------|-----------|-------------|
| `@yearly` / `@annually` | `0 0 1 1 *` | Once a year at midnight Jan 1 |
| `@monthly` | `0 0 1 * *` | First of every month at midnight |
| `@weekly` | `0 0 * * 0` | Every Sunday at midnight |
| `@daily` / `@midnight` | `0 0 * * *` | Every day at midnight |
| `@hourly` | `0 * * * *` | Top of every hour |

## Intervals

Fixed intervals run capabilities at a consistent cadence regardless of wall-clock time:

```yaml
schedule: "@every 5m"      # every 5 minutes
schedule: "@every 1h"      # every hour
schedule: "@every 30s"     # every 30 seconds
schedule: "@every 2h30m"   # every 2.5 hours
schedule: "@every 1d"      # every 24 hours
```

Duration units: `s` (seconds), `m` (minutes), `h` (hours), `d` (days). Compound durations are supported.

## Dependency Ordering

Capabilities declare dependencies via `depends_on`. The scheduler resolves execution order using topological sort (Kahn's algorithm):

```yaml
capabilities:
  time_sync:
    schedule: "@daily"
  integrity_monitoring:
    schedule: "@daily"
    depends_on:
      - time_sync
  bcp_test:
    schedule: "@weekly"
    depends_on:
      - backup_restore
  backup_restore:
    schedule: "@weekly"
```

Execution order: `time_sync` -> `integrity_monitoring`, `backup_restore` -> `bcp_test`. Dependencies within the same cycle are respected; capabilities without dependencies run in registration order.

Circular dependencies are detected at config load time and cause a startup error.

## Scheduler Loop

The daemon evaluates schedules every 60 seconds:

1. For cron schedules: check if the current minute matches the expression
2. For interval schedules: check if enough time has elapsed since the last run
3. Resolve dependency order among all due capabilities
4. Execute each due capability with its configured timeout (default 300 seconds)
5. Write results to disk and record metrics in the trends database

Capabilities that exceed their timeout are killed and produce an `error` result.

The scheduler handles SIGINT/SIGTERM for graceful shutdown -- in-progress capabilities complete before the process exits.
