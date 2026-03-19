# Timezone

Reports the system's configured timezone name, abbreviation, and UTC offset.

## Trait Path
`timezone`

## Fields

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `timezone.name` | string | `"America/New_York"` | IANA timezone name |
| `timezone.abbreviation` | string | `"EST"` | Timezone abbreviation |
| `timezone.utc_offset` | string | `"-05:00"` | UTC offset in +HH:MM / -HH:MM format |
| `timezone.utc_offset_seconds` | integer | `-18000` | UTC offset in seconds |

## Collection Method
Uses `localtime_r` via libc to get the `tm_zone` abbreviation and `tm_gmtoff` offset. The IANA timezone name is resolved by reading `/etc/timezone` first, then falling back to parsing the `/etc/localtime` symlink target (looking for the path component after `zoneinfo/`). If neither source is available, the abbreviation is used as the name.

## Using in When Expressions
```yaml
- name: ntp-config
  type: file
  target_path: /etc/ntp.conf
  when: "os_family('debian')"
```

## Using in Templates
```yaml
- name: timezone-info
  type: file
  target_path: /var/lib/vigo/timezone.txt
  content: |
    Timezone: {{ .Traits.timezone.name }} ({{ .Traits.timezone.abbreviation }})
    UTC Offset: {{ .Traits.timezone.utc_offset }}
```

## Platform
Linux
