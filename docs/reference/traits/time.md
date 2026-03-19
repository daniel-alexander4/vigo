# Time

Reports the current local and UTC time, including formatted clock and date strings.

## Trait Path
`time`

## Fields

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `time.hour` | integer | `14` | Current local hour (0-23) |
| `time.minute` | integer | `30` | Current local minute (0-59) |
| `time.weekday` | string | `"wednesday"` | Current day of the week (lowercase) |
| `time.day` | integer | `13` | Current day of the month (1-31) |
| `time.month` | integer | `3` | Current month (1-12) |
| `time.year` | integer | `2026` | Current year |
| `time.timezone` | string | `"America/New_York"` | Timezone name from `TZ` environment variable |
| `time.utc_hour` | integer | `19` | Current UTC hour (0-23) |
| `time.clock` | string | `"14:30"` | Formatted local time (HH:MM) |
| `time.date` | string | `"2026-03-13"` | Formatted local date (YYYY-MM-DD) |

## Collection Method
On Linux, calls `localtime_r` and `gmtime_r` via libc to get local and UTC time fields. On Windows, calls `GetLocalTime` and `GetSystemTime` via the Windows API. The timezone name is read from the `TZ` environment variable, defaulting to `"Local"` if unset.

## Using in When Expressions
```yaml
- name: maintenance-mode
  type: exec
  command: /usr/local/bin/start-maintenance
  when: "local_time('02:00', '04:00')"
```

## Using in Templates
```yaml
- name: daily-report-header
  type: file
  target_path: /var/lib/reports/header.txt
  content: |
    Report generated: {{ .Traits.time.date }} {{ .Traits.time.clock }}
```

## Platform
Linux, Windows (cross-platform)
