# Network (Windows)

Collects the system hostname, primary IP address, and MAC address on Windows.

## Trait Path
`network`

## Fields

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `network.hostname` | string | `"WIN-SERVER01"` | System hostname |
| `network.ip` | string | `"10.0.1.5"` | Primary outbound IP address |
| `network.mac` | string | `"aa:bb:cc:dd:ee:ff"` | Primary MAC address (colon-separated, lowercase) |

## Collection Method
Hostname is retrieved via the `hostname` crate (cross-platform). The primary IP is detected by connecting a UDP socket to `8.8.8.8:80` (no data is sent). The MAC address is extracted from `ipconfig /all` output by parsing the first "Physical Address" line, normalizing dashes to colons and converting to lowercase. Supports both English ("Physical Address") and German ("Physische Adresse") ipconfig output.

## Using in When Expressions
```yaml
- name: configure-dns
  type: exec
  command: netsh interface ip set dns "Ethernet" static 10.0.1.1
  when: "os_family('windows')"
```

## Using in Templates
```yaml
- name: network-info
  type: file
  target_path: C:\ProgramData\hobnob\network.txt
  content: |
    Hostname: {{ .Traits.network.hostname }}
    IP: {{ .Traits.network.ip }}
    MAC: {{ .Traits.network.mac }}
```

## Platform
Windows
