# Timezone (Windows)

Reports the Windows timezone name and its common abbreviation.

## Trait Path
`timezone`

## Fields

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `timezone.name` | string | `"Eastern Standard Time"` | Windows timezone display name |
| `timezone.abbreviation` | string | `"EST"` | Common timezone abbreviation |

## Collection Method
Runs `tzutil /g` to get the current Windows timezone name. Maps well-known Windows timezone names to standard abbreviations (e.g., "Eastern Standard Time" to "EST", "Pacific Standard Time" to "PST", "Central European Standard Time" to "CET"). For unrecognized timezones, falls back to extracting uppercase initials from the name.

Supported mappings include: EST, CST, MST, PST, AKST, HST, UTC, GMT, CET, WET, EET, IST, CST (China), JST, AEST, NZST.

## Using in When Expressions
```yaml
- name: ntp-config
  type: exec
  command: w32tm /config /syncfromflags:domhier /update
  when: "os_family('windows')"
```

## Using in Templates
```yaml
- name: timezone-info
  type: file
  target_path: C:\ProgramData\hobnob\timezone.txt
  content: |
    Timezone: {{ .Traits.timezone.name }} ({{ .Traits.timezone.abbreviation }})
```

## Platform
Windows
