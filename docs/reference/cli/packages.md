
# vigocli packages

Query package audit information across your fleet.

## Subcommands

| Subcommand | Description |
|------------|-------------|
| `list` | List packages across envoys |
| `drift` | List packages where installed version differs from desired |

## Usage

```bash
vigocli packages [subcommand] [flags]
```

## list

List packages across envoys with installed and desired version information.

```bash
vigocli packages list [flags]
```

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--envoy` | | | Filter by envoy ID |

### Examples

#### List all packages

```bash
vigocli packages list
```

```
ENVOY                PACKAGE    INSTALLED  DESIRED
web-01.example.com    nginx      1.24.0     1.24.0
web-01.example.com    openssl    3.0.13     3.0.13
db-01.internal.net    postgres   16.2       16.2
```

#### List packages for a specific envoy

```bash
vigocli packages list --envoy a1b2c3d4
```

## drift

List packages where the installed version differs from the desired version.

```bash
vigocli packages drift
```

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*

---

### Example

```bash
vigocli packages drift
```

```
ENVOY                PACKAGE    INSTALLED  DESIRED    DETECTED
web-02.example.com    nginx      1.22.1     1.24.0     2026-03-13 12:00
db-01.internal.net    postgres   15.6       16.2       2026-03-13 11:45
```

```
No package drift detected
```

## Related

- [envoys](envoys.md) -- manage enrolled envoys
- [runs](runs.md) -- view run history
- [preview](preview.md) -- preview planned changes
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
