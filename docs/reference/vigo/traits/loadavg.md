
# Load Average

Reports the system load averages over 1, 5, and 15 minute intervals.

## Trait Path
`loadavg`

## Fields

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `loadavg.load1` | number | `0.5` | 1-minute load average |
| `loadavg.load5` | number | `1.25` | 5-minute load average |
| `loadavg.load15` | number | `2.0` | 15-minute load average |

## Collection Method
Parses the first three whitespace-separated fields of `/proc/loadavg`.

## Using in When Expressions
```yaml
- name: deploy-app
  type: exec
  command: /usr/local/bin/deploy
  when: "os_family('debian')"
```

## Using in Templates
```yaml
- name: load-report
  type: file
  target_path: /var/lib/vigo/load.txt
  content: |
    Load: {{ .Traits.loadavg.load1 }} / {{ .Traits.loadavg.load5 }} / {{ .Traits.loadavg.load15 }}
```

## Platform
Linux
