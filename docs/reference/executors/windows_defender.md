# windows_defender

Manages Windows Defender settings and exclusions idempotently via PowerShell. Supports path, process, and extension exclusions, real-time protection toggle, and scheduled scan configuration.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `exclusion_path` | No | -- | Comma-separated paths to exclude from scanning (e.g., `C:\App,D:\Data`). |
| `exclusion_process` | No | -- | Comma-separated process names to exclude from scanning (e.g., `sqlservr.exe,nginx.exe`). |
| `exclusion_extension` | No | -- | Comma-separated file extensions to exclude (e.g., `.log,.bak`). |
| `real_time_protection` | No | -- | `true` to enable real-time monitoring, `false` to disable it. |
| `scan_schedule_day` | No | -- | Day for scheduled quick scan: `0` = daily, `1` = Sunday, `2` = Monday, ..., `7` = Saturday. |
| `scan_schedule_time` | No | -- | Time for scheduled quick scan in `HH:MM` format (e.g., `02:00`). |
| `state` | No | `present` | `present` to apply settings/add exclusions, `absent` to remove exclusions. |

At least one setting parameter (`exclusion_path`, `exclusion_process`, `exclusion_extension`, `real_time_protection`, `scan_schedule_day`, or `scan_schedule_time`) is required.

## States

- **`present`** -- Add exclusions and apply settings. Missing exclusions are added; real-time protection and scan schedule are set to the desired values.
- **`absent`** -- Remove matching exclusions. Only exclusion parameters are acted on; `real_time_protection` and scan schedule settings are not affected.

## Idempotency

The executor queries current Defender preferences via `Get-MpPreference` before acting:

- **Exclusions:** Compared case-insensitively against the current list. Only missing entries are added (`Add-MpPreference`) or matching entries removed (`Remove-MpPreference`).
- **Real-time protection:** Reads `DisableRealtimeMonitoring` and only changes if the current state differs from the desired value.
- **Scan schedule:** Reads `ScanScheduleDay` and `ScanScheduleQuickScanTime` and only updates if they differ.

If all settings already match the desired state, no action is taken.

## Examples

### Exclude application directories from scanning

```yaml
resources:
  - name: defender-app-exclusions
    type: windows_defender
    exclusion_path: "C:\\App,D:\\Database\\Data"
```

### Exclude processes and extensions

```yaml
resources:
  - name: defender-db-exclusions
    type: windows_defender
    exclusion_process: "sqlservr.exe,mysqld.exe"
    exclusion_extension: ".mdf,.ldf,.bak"
```

### Enable real-time protection

```yaml
resources:
  - name: defender-realtime
    type: windows_defender
    real_time_protection: "true"
```

### Schedule a daily quick scan at 2 AM

```yaml
resources:
  - name: defender-scan-schedule
    type: windows_defender
    scan_schedule_day: "0"
    scan_schedule_time: "02:00"
```

### Remove exclusions

```yaml
resources:
  - name: remove-legacy-exclusions
    type: windows_defender
    exclusion_path: "C:\\OldApp"
    exclusion_process: "legacy.exe"
    state: absent
```

## Platform

Windows only.

## Notes

- Requires administrator privileges. The agent runs as SYSTEM when installed as a Windows Service.
- `real_time_protection: "false"` sets `DisableRealtimeMonitoring` to `$true` in PowerShell. Use with caution -- disabling real-time protection weakens the security posture of the machine.
- Scan schedule controls the daily quick scan time. For full scan scheduling, use the `powershell` executor with `Set-MpPreference -ScanScheduleDay` and `-ScanScheduleTime`.
- Exclusion paths are compared case-insensitively, matching Windows filesystem behavior.
