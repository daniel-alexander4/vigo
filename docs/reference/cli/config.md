
# config

Config inspection commands.

## Subcommands

| Command | Description |
|---------|-------------|
| `config trace` | Trace config resolution for a hostname |
| `config tree` | Show the directory inheritance hierarchy |
| `config search` | Search envoy entries by match pattern, module, or var |
| `config publish` | Validate and publish config from stage to live |
| `config history` | View config publish history |
| `config rollback` | Rollback to a previous config version |
| `config pending` | List config changes awaiting approval |
| `config approve` | Approve a pending config change |
| `config reject` | Reject a pending config change |
| `config freeze` | Freeze config publishes until a specified time |
| `config unfreeze` | Remove the config publish freeze |

## config trace

Shows the full directory inheritance chain for a hostname with colored output: which `common.vgo` files contributed modules and vars at each level, which vars override parent values, and where each module originated (inheritance, role, or direct assignment).

### Usage

```bash
vigogo config trace <hostname>
vigogo config trace <hostname> --module <module-name>
```

### Flags

| Flag | Description |
|------|-------------|
| `--module` | Filter the trace to only show the inheritance branch relevant to a specific module |

### Example

```
$ vigogo config trace web01.prod.example.com

Hostname:       web01.prod.example.com
Match:          web*.prod.example.com (glob)
Environment:    production
Resources:      14 total

Inheritance:

  ├─ common.vgo
  │  modules: sshd, monitoring
  │  vars:    dns_server = 1.1.1.1
  │
  └─ production/common.vgo
     modules: log-shipping, auditd
     vars:    log_level = warn

Modules (7):
  sshd            ← common.vgo
  monitoring      ← common.vgo
  log-shipping    ← production/common.vgo
  auditd          ← production/common.vgo
  firewall        ← role:webserver
  nginx           ← role:webserver
  logrotate       ← role:webserver

Roles:
  webserver (includes: base-security)
    → firewall
    → nginx
    → logrotate

Vars:
  dns_server      = 1.1.1.1                          ← module:sshd
  log_level       = warn                              ← environment_overrides:production
  nginx_port      = 443                               ← inline
  worker_procs    = 4                                 ← vars_from:vars/web01.vgo
  db_password     = secret:****                       ← vars_from:vars/db-creds.vgo

Conditional vars: is_debian, has_enough_ram
```

The inheritance section uses a green color ramp in the terminal — lighter at the root `common.vgo` and progressively darker at deeper directory levels. Vars that override a parent value are marked with a `⚡` indicator.

### Filtering by module

```
$ vigogo config trace web01.prod.example.com --module log-shipping
```

When `--module` is specified, only inheritance levels that contribute that module are shown, along with the relevant role expansion.

### No match

```
$ vigogo config trace unknown.example.com
! No config entry matches hostname "unknown.example.com"
```

### Notes

- Secret values (prefixed with `secret:`) are masked in output
- Variable values longer than 60 characters are truncated
- The source column shows where each variable was set (module defaults, node vars, environment_overrides, or vars_from file)
- The inheritance chain shows vars contributed at each `common.vgo` level with override markers

## config tree

Displays the directory inheritance hierarchy as a colored tree. Shows `common.vgo` files at each level, leaf entries with their match patterns, and accumulated module totals.

### Usage

```bash
vigogo config tree
```

### Example

```
common.vgo → [sshd, ntp, monitoring]
├── production/
│   common.vgo → [log-shipping, auditd]
│   ├── web/
│   │   └── web.vgo → web*.prod.example.com → [nginx, certbot]
│   │       Total: sshd, ntp, monitoring, log-shipping, auditd, nginx, certbot
│   └── db/
│       └── db.vgo → db*.prod.example.com → [postgres, pg-backup]
│           Total: sshd, ntp, monitoring, log-shipping, auditd, postgres, pg-backup
└── staging/
    └── staging.vgo → *.staging.example.com → [debug-tools]
        Total: sshd, ntp, monitoring, debug-tools
        Excluded: monitoring
```

**Colors** (in terminal):
- Directories: blue
- Match patterns: yellow
- Inherited modules: dim gray
- Own modules: bold white
- Excluded modules: red strikethrough
- Total line: green

## config search

Search envoy entries by match pattern, module name, or var name. Shows the full inheritance chain, own modules, and accumulated totals.

### Usage

```bash
vigogo config search --match <glob>
vigogo config search --module <name>
vigogo config search --var <name>
```

At least one flag is required. Flags can be combined.

### Flags

| Flag | Description |
|------|-------------|
| `--match` | Glob pattern to match against envoy `match:` patterns (e.g., `*web*`) |
| `--module` | Find entries that use this module (inherited or direct) |
| `--var` | Find entries that define this var |

### Examples

```bash
# Find all entries with "web" in their pattern
$ vigogo config search --match "*web*"
  web*.prod.example.com  production/web
  Chain:
    (root)/common.vgo → sshd, ntp, monitoring
    production/common.vgo → log-shipping, auditd
  Own: nginx, certbot
  Total: sshd, ntp, monitoring, log-shipping, auditd, nginx, certbot

# Find every entry using the nginx module
$ vigogo config search --module nginx
  web*.prod.example.com  production/web
  ...

# Find where log_level is defined
$ vigogo config search --var log_level
  web*.prod.example.com  production/web
  Var: log_level = warn
```

## config publish

Validates `.vgo` files in `/srv/vigo/policy/stage/` then atomically syncs to `/srv/vigo/policy/.live/` (the hidden directory the server reads from). Changed module files are automatically run through the module linter, which repairs YAML issues, normalizes formatting, validates against the schema, and adds `#~` auto-comments. See [Config Publish Pipeline](../operations/config-publish.md) for full details.

### Usage

```bash
vigogo config publish [--dry-run] [--skip-url-check] [--url-timeout <duration>]
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
$ vigogo config publish --dry-run
Validation passed: 12 modules, 3 roles, 8 envoy entries
  + modules/nginx.vgo
  + envoys/web.vgo

# Publish
$ vigogo config publish
Published: 12 modules, 3 roles, 8 envoy entries (15 files synced)
  + modules/nginx.vgo
  + envoys/web.vgo

# Nothing changed
$ vigogo config publish
Nothing to publish — stage matches live.
```

## config history

View recent config publishes with operator and change details.

```bash
vigogo config history
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
vigogo config history show <id>
```

## config rollback

Rollback config to a previous snapshot. List available versions if no argument given.

```bash
# List available versions
vigogo config rollback

# Rollback to a specific version
vigogo config rollback 20260313T143000Z
```

### Example

```
$ vigogo config rollback
Available versions:
  20260313T143000Z
  20260312T091500Z
  20260311T160000Z

$ vigogo config rollback 20260312T091500Z
Rolled back to version 20260312T091500Z
```

## config pending

List config changes awaiting approval. Only relevant when `publish.approval_required` is enabled in `server.yaml`.

```bash
vigogo config pending
```

### Example

```
$ vigogo config pending
ID  STATUS   REQUESTED BY  REQUESTED AT       EXPIRES AT
42  pending  alice         2026-03-16 14:30   2026-03-17 14:30
```

## config approve

Approve a pending config change and trigger a config reload. The approver must be a different user than the requester (two-person rule).

```bash
vigogo config approve <id>
```

### Example

```
$ vigogo config approve 42
Change 42 approved by bob (requested by alice) — config reloaded.
```

## config reject

Reject a pending config change with an optional reason.

```bash
vigogo config reject <id> [--reason <text>]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--reason` | (none) | Reason for rejection |

### Example

```
$ vigogo config reject 42 --reason "not ready for production"
Change 42 rejected.
```

## config freeze

Freeze config publishes until a specified time. Accepts either an RFC3339 timestamp or a duration.

```bash
vigogo config freeze --until <RFC3339|duration>
```

### Examples

```bash
# Freeze for 24 hours
vigogo config freeze --until 24h

# Freeze until a specific time
vigogo config freeze --until 2026-03-14T06:00:00Z
```

## config unfreeze

Remove the config publish freeze.

```bash
vigogo config unfreeze
```
