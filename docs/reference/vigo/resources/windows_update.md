
# windows_update

Manages Windows Update and WSUS (Windows Server Update Services) configuration idempotently via registry values. Controls the update source, auto-update behavior, and scheduled install timing.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `wsus_server` | No | -- | WSUS server URL (e.g., `https://wsus.example.com:8531`). Empty = use Microsoft Update. |
| `auto_update_enabled` | No | `enabled` | Auto-update mode: `enabled`, `disabled`, `notify`, `download`, or `scheduled`. |
| `scheduled_day` | No | -- | Day for scheduled installs. `0` = every day, `1` = Sunday through `7` = Saturday. Only used when `auto_update_enabled=scheduled`. |
| `scheduled_time` | No | -- | Hour for scheduled installs (0-23). Only used when `auto_update_enabled=scheduled`. |
| `state` | Yes | -- | `present` to apply configuration, `absent` to remove WSUS policy (reverts to Microsoft Update). |

## Auto-Update Modes

| Mode | AUOptions Value | Behavior |
|------|----------------|----------|
| `enabled` | 5 | Auto-install updates (managed by admin). |
| `disabled` | NoAutoUpdate=1 | Automatic updates are turned off entirely. |
| `notify` | 2 | Notify before download. |
| `download` | 3 | Download automatically, notify before install. |
| `scheduled` | 4 | Download and install on a schedule. |

## States

- **`present`** -- Ensure the Windows Update configuration matches the desired settings.
- **`absent`** -- Remove the WindowsUpdate policy registry keys, reverting to Microsoft Update defaults.

## Idempotency

The executor reads the current registry values before acting:

1. Checks `HKLM:\SOFTWARE\Policies\Microsoft\Windows\WindowsUpdate` for WSUS server configuration.
2. Checks `HKLM:\SOFTWARE\Policies\Microsoft\Windows\WindowsUpdate\AU` for auto-update settings.
3. If all values match the desired state, no action is taken.
4. If values differ, registry keys are created/updated via `Set-ItemProperty`.
5. For `state=absent`, the entire `WindowsUpdate` policy key is removed recursively.

## Examples

### Point to a WSUS server with auto-install

```yaml
resources:
  - name: configure-wsus
    type: windows_update
    wsus_server: "https://wsus.corp.example.com:8531"
    auto_update_enabled: enabled
```

### Scheduled updates on Sundays at 3 AM

```yaml
resources:
  - name: scheduled-updates
    type: windows_update
    auto_update_enabled: scheduled
    scheduled_day: "1"
    scheduled_time: "3"
```

### Disable auto-updates (manual approval only)

```yaml
resources:
  - name: disable-auto-update
    type: windows_update
    auto_update_enabled: disabled
```

### WSUS with download-only (notify before install)

```yaml
resources:
  - name: wsus-download-only
    type: windows_update
    wsus_server: "https://wsus.corp.example.com:8531"
    auto_update_enabled: download
```

### Remove WSUS policy (revert to Microsoft Update)

```yaml
resources:
  - name: remove-wsus
    type: windows_update
    state: absent
```

## Platform

Windows only. Requires administrator privileges for registry writes under `HKLM:\SOFTWARE\Policies`.

## Notes

- The executor manages the Group Policy registry keys for Windows Update. On domain-joined machines, Active Directory Group Policy may override these settings.
- When `wsus_server` is set, both `WUServer` and `WUStatusServer` are configured to the same URL, and `UseWUServer` is enabled.
- When `wsus_server` is empty (or omitted), `UseWUServer` is set to `0`, directing the machine to use Microsoft Update.
- The `scheduled_day` and `scheduled_time` parameters are only applied when `auto_update_enabled=scheduled`. They are ignored for other modes.
- Changes take effect on the next Windows Update check cycle. No reboot is required, but you may need to restart the Windows Update service (`wuauserv`) for immediate effect.
