
# vigocli envoys preview

Dry-run to preview planned changes for envoys without applying them.

## Usage

```bash
vigocli envoys preview [hostname-or-uuid] [flags]
```

## Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--all` | | `false` | Preview all envoys |
| `--env` | | | Filter envoys by environment |

## Examples

### Preview changes for a specific envoy

```bash
vigocli envoys preview web-01.example.com
```

```
CONFIGCRATE TYPE       RESOURCE                  ACTION
nginx       package    nginx                     update
nginx       file       /etc/nginx/nginx.conf     update
nginx       service    nginx                     restart
```

### Preview changes for all envoys

```bash
vigocli envoys preview --all
```

```
--- web-01.example.com (a1b2c3d4) ---
CONFIGCRATE TYPE       RESOURCE                  ACTION
nginx       package    nginx                     update

--- db-01.internal.net (e5f6g7h8) ---
No changes planned for e5f6g7h8
```

### Preview changes for a specific environment

```bash
vigocli envoys preview --env staging
```

Output is colorized when writing to a terminal: `create` in green, `update` in yellow, `delete` in red.

## Related

- [task](task.md) -- dispatch ad-hoc tasks
- [validate](validate.md) -- validate .vgo configuration files
- [rollback](rollback.md) -- rollback to a previous state
