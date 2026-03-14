# hobnobcli push

Trigger an immediate check-in for a specific thrall or all thralls.

## Usage

```bash
hobnobcli push [hostname-or-uuid] [flags]
```

## Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--all` | | `false` | Push to all thralls |

## Examples

### Push to a specific thrall

```bash
hobnobcli push web-01.example.com
```

```
Force push sent — 1 thrall(s) notified
```

### Push to all thralls

```bash
hobnobcli push --all
```

```
Force push sent — 12 thrall(s) notified
```

## Related

- [thralls](thralls.md) -- manage enrolled thralls
- [preview](preview.md) -- preview changes before pushing
