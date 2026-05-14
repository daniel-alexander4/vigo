
# vigocli runs

View run history for convergence runs executed by envoys.

## Subcommands

| Subcommand | Description |
|------------|-------------|
| `drift` | Show resources causing drift or errors across the fleet |
| `list` | List recent runs |
| `show` | Show details for a run |

## Usage

```bash
vigocli runs [subcommand] [flags]
```

## drift

Show changed and failed resources from each envoy's most recent run. Useful for diagnosing what's causing relapsed/diverged status or errors across the fleet.

```bash
vigocli runs drift [flags]
```

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--envoy` | | | Filter by hostname or envoy ID |

### Examples

#### Fleet-wide drift

```bash
vigocli runs drift
```

```
HOSTNAME           MODULE      TYPE     RESOURCE                     ACTION    ERROR
web01.example.com  ntp         file     /etc/ntp.conf                updated   -
web01.example.com  ntp         service  ntpd                         updated   -
db01.example.com   postgres    file     /etc/postgresql/pg_hba.conf  updated   -
app02.example.com  firewall    exec     /usr/local/bin/fw-apply      updated   -

4 drifting resources across 3 envoys
```

#### Single envoy

```bash
vigocli runs drift --envoy web01.example.com
```

```
HOSTNAME           MODULE  TYPE     RESOURCE       ACTION    ERROR
web01.example.com  ntp     file     /etc/ntp.conf  updated   -
web01.example.com  ntp     service  ntpd           updated   -

2 drifting resources across 1 envoy
```

If all envoys are converged:

```
All envoys converged — no drift detected.
```

## list

List recent convergence runs with optional filtering.

```bash
vigocli runs list [flags]
```

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--envoy` | | | Filter by envoy ID |
| `--limit` | | `20` | Maximum number of runs to display |

### Examples

#### List all recent runs

```bash
vigocli runs list
```

```
ID          ENVOY      STARTED            STATUS     CHANGED  FAILED
r1a2b3c4    a1b2c3d4    2026-03-13 12:30   success    3        0
r5e6f7g8    e5f6g7h8    2026-03-13 12:28   success    0        0
r9h0i1j2    a1b2c3d4    2026-03-13 11:30   failed     2        1
```

#### List runs for a specific envoy

```bash
vigocli runs list --envoy a1b2c3d4 --limit 5
```

## show

Show detailed results for a specific run, including per-resource outcomes.

```bash
vigocli runs show <id>
```

### Example

```bash
vigocli runs show r1a2b3c4
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
- [task](task.md) -- dispatch ad-hoc tasks
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../../legal/license.md).*
