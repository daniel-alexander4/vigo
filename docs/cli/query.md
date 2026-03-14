# hobnobcli query

Query trait values from targeted thralls in real time, with cached fallback for offline nodes.

## Usage

```bash
hobnobcli query <trait-path> --target <spec> [flags]
```

## Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--target` | | | Target spec: hostname, glob (`*.web.*`), UUID, or trait filter (`os.distro=ubuntu`) (required) |
| `--timeout` | | `10` | Query timeout in seconds |

## Examples

### Query OS distribution across web servers

```bash
hobnobcli query os.distro --target "*.web.*"
```

```
Query:     os.distro
Responded: 3  Timed out: 0  Offline: 1

HOSTNAME              VALUE     STATUS
web-01.example.com    ubuntu    responded
web-02.example.com    ubuntu    responded
web-03.example.com    debian    responded

HOSTNAME              VALUE     STATUS
web-04.example.com    ubuntu    offline cached 2026-03-12 18:00
```

### Query memory across Debian hosts

```bash
hobnobcli query memory.total_mb --target "os.family=debian"
```

```
Query:     memory.total_mb
Responded: 5  Timed out: 0  Offline: 0

HOSTNAME              VALUE   STATUS
web-01.example.com    8192    responded
web-02.example.com    8192    responded
db-01.internal.net    32768   responded
db-02.internal.net    32768   responded
app-01.example.com    16384   responded
```

### Query with timeout handling

```bash
hobnobcli query os.version --target "*.web.*" --timeout 5
```

```
Query:     os.version
Responded: 2  Timed out: 1  Offline: 0

HOSTNAME              VALUE   STATUS
web-01.example.com    24.04   responded
web-02.example.com    24.04   responded

HOSTNAME              STATUS
web-03.example.com    timed_out
```

## Related

- [thralls](thralls.md) -- view traits for a specific thrall
- [inventory](inventory.md) -- query thralls by traits
- [task](task.md) -- dispatch ad-hoc commands
