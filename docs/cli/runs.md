# vigogo runs

View run history for convergence runs executed by envoys.

## Subcommands

| Subcommand | Description |
|------------|-------------|
| `list` | List recent runs |
| `show` | Show details for a run |

## Usage

```bash
vigogo runs [subcommand] [flags]
```

## list

List recent convergence runs with optional filtering.

```bash
vigogo runs list [flags]
```

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--envoy` | | | Filter by envoy ID |
| `--limit` | | `20` | Maximum number of runs to display |

### Examples

#### List all recent runs

```bash
vigogo runs list
```

```
ID          ENVOY      STARTED            STATUS     CHANGED  FAILED
r1a2b3c4    a1b2c3d4    2026-03-13 12:30   success    3        0
r5e6f7g8    e5f6g7h8    2026-03-13 12:28   success    0        0
r9h0i1j2    a1b2c3d4    2026-03-13 11:30   failed     2        1
```

#### List runs for a specific envoy

```bash
vigogo runs list --envoy a1b2c3d4 --limit 5
```

## show

Show detailed results for a specific run, including per-resource outcomes.

```bash
vigogo runs show <id>
```

### Example

```bash
vigogo runs show r1a2b3c4
```

```
Run:      r1a2b3c4
Envoy:     a1b2c3d4
Started:  2026-03-13 12:30
Finished: 2026-03-13 12:31
Status:   success
Modules:  5 total, 3 changed, 0 failed, 0 skipped

MODULE      TYPE       RESOURCE           ACTION    CHANGED  ERROR
nginx       package    nginx              install   true     -
nginx       file       /etc/nginx/nginx   update    true     -
nginx       service    nginx              restart   true     -
```

## Related

- [envoys](envoys.md) -- manage enrolled envoys
- [push](push.md) -- dispatch ad-hoc pushes
