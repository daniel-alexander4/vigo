# hobnobcli packages

Query package audit information across your fleet.

## Subcommands

| Subcommand | Description |
|------------|-------------|
| `list` | List packages across thralls |
| `drift` | List packages where installed version differs from desired |

## Usage

```bash
hobnobcli packages [subcommand] [flags]
```

## list

List packages across thralls with installed and desired version information.

```bash
hobnobcli packages list [flags]
```

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--thrall` | | | Filter by thrall ID |

### Examples

#### List all packages

```bash
hobnobcli packages list
```

```
THRALL                PACKAGE    INSTALLED  DESIRED
web-01.example.com    nginx      1.24.0     1.24.0
web-01.example.com    openssl    3.0.13     3.0.13
db-01.internal.net    postgres   16.2       16.2
```

#### List packages for a specific thrall

```bash
hobnobcli packages list --thrall a1b2c3d4
```

## drift

List packages where the installed version differs from the desired version.

```bash
hobnobcli packages drift
```

### Example

```bash
hobnobcli packages drift
```

```
THRALL                PACKAGE    INSTALLED  DESIRED    DETECTED
web-02.example.com    nginx      1.22.1     1.24.0     2026-03-13 12:00
db-01.internal.net    postgres   15.6       16.2       2026-03-13 11:45
```

```
No package drift detected
```

## Related

- [thralls](thralls.md) -- manage enrolled thralls
- [runs](runs.md) -- view run history
- [preview](preview.md) -- preview planned changes
