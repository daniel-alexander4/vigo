
# vigocli query

Query trait values from targeted envoys in real time, with cached fallback for offline nodes.

## Usage

```bash
vigocli query <trait-path> --target <spec> [flags]
```

## Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--target` | | | Target spec: hostname, glob (`*.web.*`), UUID, or trait filter (`os.distro=ubuntu`) (required) |
| `--timeout` | | `10` | Query timeout in seconds |

## Examples

### Query OS distribution across web servers

```bash
vigocli query os.distro --target "*.web.*"
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
vigocli query hardware.memory_mb --target "os.family=debian"
```

```
Query:     hardware.memory_mb
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
vigocli query os.version --target "*.web.*" --timeout 5
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

- [envoys](envoys.md) -- view traits for a specific envoy
- [inventory](inventory.md) -- query envoys by traits
- [task](task.md) -- dispatch ad-hoc commands
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
