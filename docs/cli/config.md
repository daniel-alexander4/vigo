# config

Config inspection commands.

## Subcommands

| Command | Description |
|---------|-------------|
| `config trace` | Trace config resolution for a hostname |
| `config publish` | Validate and publish config from proposed to live |
| `config history` | View config publish history |
| `config rollback` | Rollback to a previous config version |
| `config pending` | List config changes awaiting approval |
| `config approve` | Approve a pending config change |
| `config reject` | Reject a pending config change |
| `config freeze` | Freeze config publishes until a specified time |
| `config unfreeze` | Remove the config publish freeze |

## config trace

Shows which pattern matched a hostname, how roles expanded, where each variable came from, and the final module list. Useful for debugging why a thrall gets a particular configuration.

### Usage

```bash
hobnobcli config trace <hostname>
```

### Example

```
$ hobnobcli config trace web01.prod.example.com

Hostname:      web01.prod.example.com
Match pattern: web*.prod.example.com (glob)
Environment:   production
Resources:     14 total

Roles:
  webserver (includes: base-security)
    → firewall
    → sshd
    → nginx
    → logrotate

Modules (4):
  firewall
  sshd
  nginx
  logrotate

Vars:
  nginx_port      = 443                             ← environment_overrides[production]
  worker_procs    = 4                               ← vars_from[vars/web01.hnb]
  log_level       = info                            ← node vars
  ssl_protocols   = TLSv1.2 TLSv1.3                ← vars_from[vars/web-common.hnb]
  db_password     = secret:****                     ← vars_from[vars/db-creds.hnb]

Conditional vars: is_debian, has_enough_ram
```

If no config entry matches the hostname:

```
$ hobnobcli config trace unknown.example.com
No config entry matches hostname "unknown.example.com"
```

### Notes

- Secret values (prefixed with `secret:`) are masked in output
- Variable values longer than 60 characters are truncated
- The source column shows where each variable was set (module defaults, node vars, environment_overrides, or vars_from file)

## config publish

Validates `.hnb` files in `/srv/hobnob/hnb/proposed/` then atomically syncs to `/srv/hobnob/hnb/.live/` (the hidden directory the server reads from). See [Config Publish Pipeline](../operations/config-publish.md) for full details.

### Usage

```bash
hobnobcli config publish [--dry-run] [--skip-url-check] [--url-timeout <duration>]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--dry-run` | `false` | Validate only |
| `--skip-url-check` | `false` | Skip HTTP aliveness checks for URLs |
| `--url-timeout` | `10s` | Timeout per URL aliveness check |
| `--operator` | `$USER` | Operator name for audit trail |
| `--diff` | `false` | Show line-level diff for each changed file |
| `--ask-ai` | `false` | Ask AI to suggest fixes for validation errors |

### Examples

```bash
# Validate without publishing
$ hobnobcli config publish --dry-run
Validation passed: 12 modules, 3 roles, 8 thrall entries
  + modules/nginx.hnb
  + thralls/web.hnb

# Publish
$ hobnobcli config publish
Published: 12 modules, 3 roles, 8 thrall entries (15 files synced)
  + modules/nginx.hnb
  + thralls/web.hnb

# Nothing changed
$ hobnobcli config publish
Nothing to publish — proposed matches live.
```

## config history

View recent config publishes with operator and change details.

```bash
hobnobcli config history
```

### Example

```
ID          OPERATOR  PUBLISHED AT       CHANGED  REMOVED
a1b2c3d4    admin     2026-03-13 14:30   3        0
e5f6g7h8    deploy    2026-03-12 09:15   1        1
```

### config history show

View details for a specific publish:

```bash
hobnobcli config history show <id>
```

## config rollback

Rollback config to a previous snapshot. List available versions if no argument given.

```bash
# List available versions
hobnobcli config rollback

# Rollback to a specific version
hobnobcli config rollback 20260313T143000Z
```

### Example

```
$ hobnobcli config rollback
Available versions:
  20260313T143000Z
  20260312T091500Z
  20260311T160000Z

$ hobnobcli config rollback 20260312T091500Z
Rolled back to version 20260312T091500Z
```

## config pending

List config changes awaiting approval. Only relevant when `publish.approval_required` is enabled in `server.yaml`.

```bash
hobnobcli config pending
```

### Example

```
$ hobnobcli config pending
ID  STATUS   REQUESTED BY  REQUESTED AT       EXPIRES AT
42  pending  alice         2026-03-16 14:30   2026-03-17 14:30
```

## config approve

Approve a pending config change and trigger a config reload. The approver must be a different user than the requester (two-person rule).

```bash
hobnobcli config approve <id>
```

### Example

```
$ hobnobcli config approve 42
Change 42 approved by bob (requested by alice) — config reloaded.
```

## config reject

Reject a pending config change with an optional reason.

```bash
hobnobcli config reject <id> [--reason <text>]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--reason` | (none) | Reason for rejection |

### Example

```
$ hobnobcli config reject 42 --reason "not ready for production"
Change 42 rejected.
```

## config freeze

Freeze config publishes until a specified time. Accepts either an RFC3339 timestamp or a duration.

```bash
hobnobcli config freeze --until <RFC3339|duration>
```

### Examples

```bash
# Freeze for 24 hours
hobnobcli config freeze --until 24h

# Freeze until a specific time
hobnobcli config freeze --until 2026-03-14T06:00:00Z
```

## config unfreeze

Remove the config publish freeze.

```bash
hobnobcli config unfreeze
```
