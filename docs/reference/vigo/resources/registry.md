
# registry (Windows)

Manages Windows Registry keys and values idempotently. Reads the current state via PowerShell and only writes when drift is detected.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `path` | Yes | -- | Registry key path using PowerShell notation (e.g., `HKLM:\SOFTWARE\Policies\...`). |
| `name` | No | -- | Value name within the key. Omit to manage the key itself (create/delete). |
| `value` | No | -- | Desired value data. Required when `name` is set and `state` is `present`. |
| `type` | No | `string` | Value type: `string`, `dword`, `qword`, `binary`, `multi_string`, `expand_string`. |
| `state` | Yes | -- | `present` to ensure the key/value exists, `absent` to remove it. |

## Registry Hives

| Prefix | Hive |
|--------|------|
| `HKLM:\` | HKEY_LOCAL_MACHINE |
| `HKCU:\` | HKEY_CURRENT_USER |
| `HKCR:\` | HKEY_CLASSES_ROOT |
| `HKU:\` | HKEY_USERS |
| `HKCC:\` | HKEY_CURRENT_CONFIG |

## States

- **`present`** -- Ensure the key exists and the value matches. Creates the key hierarchy if needed.
- **`absent`** -- Remove the value (if `name` is set) or the entire key and its children (if only `path` is set).

## Examples

### Set a DWORD value

```yaml
resources:
  - name: disable-telemetry
    type: registry
    path: "HKLM:\\SOFTWARE\\Policies\\Microsoft\\Windows\\DataCollection"
    name: AllowTelemetry
    value: "0"
    type: dword
```

### Set a string value

```yaml
resources:
  - name: set-wallpaper-path
    type: registry
    path: "HKCU:\\Control Panel\\Desktop"
    name: Wallpaper
    value: "C:\\Windows\\Web\\Wallpaper\\corp-bg.jpg"
```

### Ensure a registry key exists (no value)

```yaml
resources:
  - name: create-app-key
    type: registry
    path: "HKLM:\\SOFTWARE\\MyCompany\\MyApp"
```

### Remove a registry value

```yaml
resources:
  - name: remove-legacy-setting
    type: registry
    path: "HKLM:\\SOFTWARE\\MyCompany\\OldApp"
    name: LicenseKey
    state: absent
```

### Remove an entire key tree

```yaml
resources:
  - name: remove-old-app-config
    type: registry
    path: "HKLM:\\SOFTWARE\\MyCompany\\OldApp"
    state: absent
```

### Security hardening example

```yaml
resources:
  - name: disable-autorun
    type: registry
    path: "HKLM:\\SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\Policies\\Explorer"
    name: NoDriveTypeAutoRun
    value: "255"
    type: dword

  - name: disable-remote-assistance
    type: registry
    path: "HKLM:\\SYSTEM\\CurrentControlSet\\Control\\Remote Assistance"
    name: fAllowToGetHelp
    value: "0"
    type: dword

  - name: enable-firewall-domain
    type: registry
    path: "HKLM:\\SYSTEM\\CurrentControlSet\\Services\\SharedAccess\\Parameters\\FirewallPolicy\\DomainProfile"
    name: EnableFirewall
    value: "1"
    type: dword
```

## Platform

Windows only.

## Notes

- Registry paths use PowerShell drive notation (`HKLM:\`, `HKCU:\`), not the `HKEY_LOCAL_MACHINE` long form.
- Value comparison is string-based. For DWORD values, pass the numeric value as a string (e.g., `"0"`, `"1"`, `"255"`).
- When setting a value with `state: present`, the key hierarchy is created automatically if it doesn't exist.
- Removing a key with `state: absent` (no `name`) deletes the key and all its children recursively.
