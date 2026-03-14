# hobnobcli preview

Dry-run to preview planned changes for thralls without applying them.

## Usage

```bash
hobnobcli preview [hostname-or-uuid] [flags]
```

## Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--all` | | `false` | Preview all thralls |
| `--env` | | | Filter thralls by environment |

## Examples

### Preview changes for a specific thrall

```bash
hobnobcli preview web-01.example.com
```

```
MODULE      TYPE       RESOURCE                  ACTION
nginx       package    nginx                     update
nginx       file       /etc/nginx/nginx.conf     update
nginx       service    nginx                     restart
```

### Preview changes for all thralls

```bash
hobnobcli preview --all
```

```
--- web-01.example.com (a1b2c3d4) ---
MODULE      TYPE       RESOURCE                  ACTION
nginx       package    nginx                     update

--- db-01.internal.net (e5f6g7h8) ---
No changes planned for e5f6g7h8
```

### Preview changes for a specific environment

```bash
hobnobcli preview --env staging
```

Output is colorized when writing to a terminal: `create` in green, `update` in yellow, `delete` in red.

## Related

- [push](push.md) -- trigger immediate check-in
- [validate](validate.md) -- validate .hnb configuration files
- [rollback](rollback.md) -- rollback to a previous state
