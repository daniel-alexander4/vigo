
# vigogo inventory

Query enrolled envoys by their collected traits with flexible filtering and column display.

## Usage

```bash
vigogo inventory [flags]
```

## Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--where` | | | Filter by trait (`key=value`), repeatable |
| `--show` | | | Trait fields to display as columns (comma-separated) |

## Examples

### Filter by a single trait

```bash
vigogo inventory --where os.distro=ubuntu
```

```json
{
  "count": 3,
  "envoys": [
    {
      "hostname": "web-01.example.com",
      "ip_address": "10.0.1.10",
      "collected_at": "2026-03-13T12:30:00Z",
      "traits": { ... }
    }
  ]
}
```

### Filter and display specific columns

```bash
vigogo inventory --where os.distro=ubuntu --show network.ip
```

```
HOSTNAME              NETWORK.IP
web-01.example.com    10.0.1.10
web-02.example.com    10.0.1.11
web-03.example.com    10.0.1.12

3 envoys
```

### Multiple filters with multiple columns

```bash
vigogo inventory --where os.family=debian --where virtual.type=kvm \
  --show os.distro,os.version
```

```
HOSTNAME              OS.DISTRO  OS.VERSION
web-01.example.com    ubuntu     24.04
web-02.example.com    ubuntu     24.04
db-01.internal.net    debian     12

3 envoys
```

### No matches

```bash
vigogo inventory --where os.distro=centos
```

```
No matching envoys found.
```

## Output Format

- Without `--show`: full JSON output including all traits
- With `--show`: tabular output with hostname and requested trait columns

## Related

- [envoys](envoys.md) -- manage enrolled envoys and view traits
- [query](query.md) -- query trait values in real time
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
