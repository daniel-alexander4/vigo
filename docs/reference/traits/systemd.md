
# Systemd

Reports systemd availability, version, default target, and failed units.

## Trait Path
`systemd`

## Fields

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `systemd.available` | boolean | `true` | Whether systemd is available |
| `systemd.version` | string | `"249"` | systemd version number |
| `systemd.default_target` | string | `"multi-user.target"` | Default boot target |
| `systemd.failed_units` | array of strings | `["nginx.service"]` | Names of failed units |
| `systemd.failed_count` | integer | `1` | Number of failed units |

## Collection Method
First checks if `systemctl` is available via `which`. If not available, returns defaults with `available: false`. Otherwise runs:
- `systemctl --version` and extracts the version number from the first line
- `systemctl get-default` for the default boot target
- `systemctl --state=failed list-units --no-legend --plain` to enumerate failed unit names

## Using in When Expressions
```yaml
- name: enable-service
  type: service
  service: nginx
  state: enabled
  when: "os_family('debian')"
```

## Using in Templates
```yaml
- name: systemd-report
  type: file
  target_path: /var/lib/vigo/systemd.txt
  content: |
    systemd {{ .Traits.systemd.version }}
    Default target: {{ .Traits.systemd.default_target }}
    Failed units: {{ .Traits.systemd.failed_count }}
```

## Platform
Linux
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
