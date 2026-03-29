
# Configuration

Connwaer is configured via a single YAML file. Default location: `/etc/connwaer/connwaer.yaml` (Linux) or `C:\ProgramData\Connwaer\connwaer.yaml` (Windows). Override with `--config` or `-c`.

## Top-Level Fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `license_dir` | string | `/srv/vigo/license` | Directory containing the Connwaer license file |
| `results_dir` | string | `/var/lib/connwaer/results` | Directory where JSON result files are written |
| `trends_db` | string | `/var/lib/connwaer/trends.db` | Path to the SQLite trends database |
| `log_level` | string | `info` | Log verbosity: `trace`, `debug`, `info`, `warn`, `error` |
| `alerting` | object | (empty) | Alert dispatch configuration -- see [Alerting](alerting.md) |
| `resource_limits` | object | (defaults) | Process-level resource guardrails -- see below |
| `capabilities` | map | (empty) | Per-capability configuration entries |

## Capability Entries

Each key in the `capabilities` map is a capability machine name (e.g., `time_sync`, `backup_restore`). See the [Capability Index](capabilities/_index.md) for all 18 names.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `true` | Whether to run this capability |
| `schedule` | string | (required) | Cron expression or interval -- see [Scheduling](scheduling.md) |
| `depends_on` | list | `[]` | Capabilities that must complete before this one runs |
| `params` | map | `{}` | Capability-specific parameters (keys vary per capability) |
| `thresholds` | map | `{}` | Pass/fail thresholds (keys vary per capability) |

## Schedule Syntax

Standard 5-field cron, shortcuts, or intervals:

```yaml
schedule: "*/15 * * * *"   # every 15 minutes
schedule: "0 */6 * * *"    # every 6 hours
schedule: "0 2 * * *"      # daily at 2:00 AM
schedule: "@daily"          # midnight
schedule: "@hourly"         # top of every hour
schedule: "@every 5m"       # every 5 minutes
schedule: "@every 1h30m"    # every 90 minutes
```

See [Scheduling](scheduling.md) for the full syntax reference.

## Dependencies

Capabilities can declare dependencies to enforce execution order:

```yaml
capabilities:
  time_sync:
    enabled: true
    schedule: "@daily"
  integrity_monitoring:
    enabled: true
    schedule: "@daily"
    depends_on:
      - time_sync
```


---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*

---
Dependencies are resolved via topological sort. Circular dependencies are rejected at config load time. A `depends_on` referencing a capability not present in the config is also rejected.

## Resource Limits

Connwaer runs on production systems and must be invisible to the host. The `resource_limits` section controls process-level guardrails applied at startup.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `cpu_nice` | int | `15` | Unix nice value (0-19). Higher = lower CPU priority. |
| `io_class` | string | `idle` | Linux I/O scheduling class: `idle`, `best-effort`, or `none`. |
| `memory_max_mb` | int | `256` | Process virtual memory cap in MiB via `RLIMIT_AS`. 0 = unlimited. |
| `max_trend_records` | int | `1000` | Max trend DB records kept per capability. Pruned after each scheduler cycle. |
| `max_result_files` | int | `500` | Max JSON result files in the results directory. Oldest pruned by mtime. |

```yaml
resource_limits:
  cpu_nice: 15
  io_class: idle
  memory_max_mb: 256
  max_trend_records: 1000
  max_result_files: 500
```

- **CPU**: `nice` is applied via `setpriority(2)`. Values below 0 require root.
- **I/O**: `idle` class means connwaer only gets I/O bandwidth when the disk is otherwise idle (Linux `ioprio_set` syscall). `best-effort` uses priority 7 (lowest within class).
- **Memory**: Uses `setrlimit(RLIMIT_AS)`. The process receives `ENOMEM` if it exceeds the cap. Set to 0 to disable.
- **Trends/Results**: Pruning runs once per scheduler cycle (every 30s) and removes the oldest entries beyond the limit.

## Validation

The config is validated on load. Errors that cause a startup failure:

- Empty `schedule` on any capability entry
- `depends_on` referencing a capability not in the `capabilities` map
- Circular dependency chains
- `resource_limits.cpu_nice` outside 0-19
- `resource_limits.io_class` not one of `idle`, `best-effort`, `none`
- Malformed YAML

## Full Example

```yaml
license_dir: /srv/vigo/license
results_dir: /var/lib/connwaer/results
trends_db: /var/lib/connwaer/trends.db
log_level: info

resource_limits:
  cpu_nice: 15
  io_class: idle
  memory_max_mb: 256
  max_trend_records: 1000
  max_result_files: 500

alerting:
  recovery_alerts: true
  dedup_window_seconds: 3600
  webhooks:
    - url: "https://hooks.slack.com/services/T00/B00/xxx"
      severity_min: fail
  syslog:
    facility: daemon
    severity_min: warn

capabilities:
  time_sync:
    enabled: true
    schedule: "@every 5m"
    thresholds:
      max_offset_ms: 100

  capacity_monitor:
    enabled: true
    schedule: "@every 15m"
    thresholds:
      disk_usage_max_pct: 90
      memory_usage_max_pct: 90

  encryption_at_rest:
    enabled: true
    schedule: "@daily"

  password_policy:
    enabled: true
    schedule: "@daily"
    thresholds:
      min_length: 14

  session_timeout:
    enabled: true
    schedule: "@daily"
    thresholds:
      max_idle_seconds: 900

  network_segmentation:
    enabled: true
    schedule: "@every 30m"
    params:
      blocked_targets:
        - "db-server:5432"
        - "10.0.0.0/8:22"
      allowed_targets:
        - "web-lb:443"

  integrity_monitoring:
    enabled: true
    schedule: "@daily"
    depends_on:
      - time_sync

  privilege_escalation:
    enabled: true
    schedule: "@weekly"
```
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
