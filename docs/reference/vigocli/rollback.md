
# vigocli envoys revert

Rollback a envoy to a previous state using either a run ID or a specific file path.

## Usage

```bash
vigocli envoys revert <hostname-or-uuid> [flags]
```

## Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--run` | | | Run UUID to rollback to |
| `--file` | | | File path to rollback |

Exactly one of `--run` or `--file` is required.

## Examples

### Rollback to a previous run

```bash
vigocli envoys revert web-01.example.com --run r1a2b3c4
```

```
Rollback successful: Restored 3 resources from run r1a2b3c4
```

### Rollback a specific file

```bash
vigocli envoys revert web-01.example.com --file /etc/nginx/nginx.conf
```

```
Rollback successful: Restored /etc/nginx/nginx.conf to previous snapshot
```

## Related

- [runs](runs.md) -- view run history to find run IDs
- [preview](preview.md) -- preview planned changes
- [task](task.md) -- dispatch ad-hoc tasks
