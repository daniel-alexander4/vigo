
# config

Config inspection commands.

## Subcommands

| Command | Description |
|---------|-------------|
| `config edit` | Open a .vgo file in an editor with validation on save |
| `config trace` | Trace config resolution for a hostname |
| `config tree` | Show the directory inheritance hierarchy |
| `config search` | Search envoy entries by match pattern, module, or var |
| `config publish` | Validate and publish config from stage to live |
| `config history` | View config publish history |
| `config rollback` | Rollback to a previous config version |
| `config pending` | List config changes awaiting approval |
| `config approve` | Approve a pending config change |
| `config reject` | Reject a pending config change |
| `config unused` | Find unused modules and match files |
| `config freeze` | Freeze config publishes until a specified time |
| `config unfreeze` | Remove the config publish freeze |

## config edit

Open a `.vgo` config file in an editor with YAML validation on save. Accepts a module name, relative path, or absolute path. Module names are searched recursively in `stockpile/modules/`.

### Usage

```bash
vigocli config edit <name-or-path> [--editor <editor>]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--editor` | `$EDITOR` or `vi` | Editor to use |

### Examples

```bash
vigocli config edit nginx                    # finds stockpile/modules/.../nginx.vgo
vigocli config edit modules/nginx.vgo        # relative to stockpile/
vigocli config edit danlap.vgo               # finds by name anywhere in stockpile/
vigocli config edit --editor nano nginx      # use nano instead of vim
```

When using vi/vim, the editor launches with Vigo-aware syntax highlighting and validates the file on every `:w`. Validation errors abort the write — use `:w!` to force. Other editors get post-save validation only.

See [vi](vi.md) for syntax highlighting and validation details.

## config trace

Shows the full directory inheritance chain for a hostname with colored output: which `common.vgo` files contributed modules and vars at each level, which vars override parent values, and where each module originated (inheritance, role, or direct assignment).

### Usage

```bash
vigocli config trace <hostname>
vigocli config trace <hostname> --module <module-name>
```

### Flags

| Flag | Description |
|------|-------------|
| `--module` | Filter the trace to only show the inheritance branch relevant to a specific module |

### Example

```
$ vigocli config trace web01.prod.example.com

Hostname:       web01.prod.example.com
Match:          web*.prod.example.com (glob)
Environment:    production
Resources:      14 total

Path Inheritance:

  ● common.vgo
  │
  └── production
      └── common.vgo
      │
      └── production/webservers.vgo

  ─────────────────────────────────────────

Module Inheritance:
  ┌─ common.vgo ───────────────────────────┐
  │ modules: sshd, monitoring              │
  └────────────────────────────────────────┘
              │
              ▼
  ┌─ production/common.vgo ────────────────┐
  │ modules: log-shipping, auditd          │
  └────────────────────────────────────────┘
              │
              ▼
  ┌─ production/webservers.vgo ────────────┐
  │ match: web*.prod.example.com           │
  │ roles: webserver (includes: base-security)
  │            ├─ firewall                 │
  │            ├─ nginx                    │
  │            └─ logrotate                │
  └────────────────────────────────────────┘

  ─────────────────────────────────────────

Variable Inheritance:

  ┌─ common.vgo ───────────────────────────┐
  │ dns_server = 1.1.1.1                   │
  └────────────────────────────────────────┘
              │
              ▼
  ┌─ production/common.vgo ────────────────┐
  │ log_level = warn  overrides            │
  └────────────────────────────────────────┘

  Variable Final Resolution:
    dns_server = 1.1.1.1  module:sshd
    worker_procs = 4  common.vgo:production/
    db_password = secret:****  match-block
    nginx_port = 443  environments.vgo:production
    log_level = warn  match-block

  ─────────────────────────────────────────

Password Inheritance:

  Provider: local

  nginx
    ssl_cert  secret:****  path: vigo/certs/nginx  source: var
    ssl_key   secret:****  path: vigo/keys/nginx   source: var
  postgres
    dsn       secret:****  path: vigo/db/dsn  source: resource:postgres/pg-config/dsn

  ─────────────────────────────────────────

Dependency Inheritance:

  depends_on:
    nginx ──▶ sshd (module)
    nginx/nginx-config ──▶ nginx/nginx-pkg (resource)

  notify:
    nginx/nginx-config ──▶ nginx/nginx-service (resource)

  ─────────────────────────────────────────

Compliance Inheritance:

  sshd
    hipaa:  AC-1, AC-2, SC-8
    pci-dss:  2.2.1, 8.3.1

  nginx
    pci-dss:  6.5.1, 6.5.3

  ─────────────────────────────────────────

Conditional Inheritance:

  sshd/fail2ban   {{os_family('debian')}}          agent
  nginx           {{is_webserver=true}}             server

Conditional vars: is_debian, has_enough_ram
```

The trace output has 8 sections, each using a green color ramp — lighter at the root `common.vgo` and progressively darker at deeper directory levels:

1. **Path Inheritance** — directory tree showing which config files apply
2. **Usercrates** — auto-applied per-user modules from `stockpile/**/usercrates/`, listed with the scope directory where each one lives. Only rendered when any usercrates apply. See [Usercrates](../../engineering/writing-modules/config-format.md#usercrates).
3. **Module Inheritance** — modules and roles contributed at each level (usercrates excluded — they show in section 2)
4. **Variable Inheritance** — vars at each level with override markers, plus final resolution table
5. **Password Inheritance** — all `secret:` references with masked values, secret paths, source module, and provider
6. **Dependency Inheritance** — module and resource dependency DAG (depends_on, before, notify, subscribes, watch_secret)
7. **Compliance Inheritance** — compliance framework tags on each module (e.g., hipaa, pci-dss with control IDs)
8. **Conditional Inheritance** — `when:` expressions with server vs agent evaluation classification

### Filtering by module

```
$ vigocli config trace web01.prod.example.com --module log-shipping
```

When `--module` is specified, only inheritance levels that contribute that module are shown, along with the relevant role expansion.

### No match

```
$ vigocli config trace unknown.example.com
! No config entry matches hostname "unknown.example.com"
```

### Notes

- Secret values (prefixed with `secret:`) are always masked as `secret:****` in both CLI and web UI — the secret path is shown but never the resolved value
- The source column shows where each variable was set (module defaults, inherited `common.vgo`, match-block vars, or `environments.vgo`)
- The inheritance chain shows vars contributed at each `common.vgo` level with override markers
- Conditional expressions are classified as `server` (pure variable refs) or `agent` (contains function calls like `os_family()`) based on whether they require agent-side trait evaluation

## config tree

Displays the directory inheritance hierarchy as a colored tree. Shows `common.vgo` files at each level, leaf entries with their match patterns, and accumulated module totals.

### Usage


```bash
vigocli config tree
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
vigocli config search --match <glob>
vigocli config search --module <name>
vigocli config search --var <name>
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
$ vigocli config search --match "*web*"
  web*.prod.example.com  production/web
  Chain:
    (root)/common.vgo → sshd, ntp, monitoring
    production/common.vgo → log-shipping, auditd
  Own: nginx, certbot
  Total: sshd, ntp, monitoring, log-shipping, auditd, nginx, certbot

# Find every entry using the nginx module
$ vigocli config search --module nginx
  web*.prod.example.com  production/web
  ...

# Find where log_level is defined
$ vigocli config search --var log_level
  web*.prod.example.com  production/web
  Var: log_level = warn
```

## config publish

Validates `.vgo` files in `/srv/vigo/stacks/stockpile/` then atomically syncs to `/srv/vigo/stacks/.live/` (the hidden directory inside the stockpile that the server reads from). Changed module files are automatically run through the module linter, which repairs YAML issues, normalizes formatting, validates against the schema, and adds `#~` auto-comments. See [Config Publish Pipeline](../../operations/config-publish.md) for full details.

### Usage

```bash
vigocli config publish [--dry-run] [--skip-url-check] [--url-timeout <duration>]
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
$ vigocli config publish --dry-run
Validation passed: 12 modules, 3 roles, 8 envoy entries
  + modules/nginx.vgo
  + envoys/web.vgo

# Publish
$ vigocli config publish
Published: 12 modules, 3 roles, 8 envoy entries (15 files synced)
  + modules/nginx.vgo
  + envoys/web.vgo

# Nothing changed
$ vigocli config publish
Nothing to publish — stage matches live.
```

### Module Retraction

When `publish.retraction.enabled` and `ai.enabled` are both true in `server.yaml`, every `config publish` automatically generates `.retract` files for new and changed modules. When a module is removed, the CLI also prints which envoys are affected and per-resource reversal confidence. The `.retract` file is inert until renamed to `.vgo`. See [Module Retraction](../../engineering/writing-modules/retraction.md) for the full workflow.

## config history

View recent config publishes with operator and change details.


```bash
vigocli config history
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
vigocli config history show <id>
```

## config rollback

Rollback config to a previous snapshot. List available versions if no argument given.

```bash
# List available versions
vigocli config rollback

# Rollback to a specific version
vigocli config rollback 20260313T143000Z
```

### Example

```
$ vigocli config rollback
Available versions:
  20260313T143000Z
  20260312T091500Z
  20260311T160000Z

$ vigocli config rollback 20260312T091500Z
Rolled back to version 20260312T091500Z
```

## config pending

List config changes awaiting approval. Only relevant when `publish.approval_required` is enabled in `server.yaml`.

```bash
vigocli config pending
```

### Example

```
$ vigocli config pending
ID  STATUS   REQUESTED BY  REQUESTED AT       EXPIRES AT
42  pending  alice         2026-03-16 14:30   2026-03-17 14:30
```

## config approve

Approve a pending config change and trigger a config reload. The approver must be a different user than the requester (two-person rule).

```bash
vigocli config approve <id>
```

### Example

```
$ vigocli config approve 42
Change 42 approved by bob (requested by alice) — config reloaded.
```

## config reject

Reject a pending config change with an optional reason.

```bash
vigocli config reject <id> [--reason <text>]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--reason` | (none) | Reason for rejection |

### Example

```
$ vigocli config reject 42 --reason "not ready for production"
Change 42 rejected.
```

## config unused

Find module definitions that are never referenced by any role, match entry, `common.vgo`, or `environments.vgo`, and match files whose patterns don't match any enrolled envoy.

### Usage

```bash
vigocli config unused [--json] [--delete]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--json` | `false` | Output raw JSON |
| `--delete` | `false` | Prompt to delete unused files from stockpile |

### Examples

```
$ vigocli config unused

!  2 unused module(s)

  NAME              FILE
  debug-tools       modules/system/debug-tools.vgo
  old-monitoring    modules/legacy/old-monitoring.vgo

!  1 unused match file(s)

  FILE                    PATTERNS
  staging/retired.vgo     retired-*.staging.example.com
```

With `--delete`, the command lists all files to be removed, asks for confirmation, deletes them from `/srv/vigo/stacks/stockpile/`, and reminds you to run `vigocli config publish` to sync the changes to `.live/`.

```
$ vigocli config unused --delete
...
Files to delete from /srv/vigo/stacks/stockpile:
  modules/system/debug-tools.vgo
  modules/legacy/old-monitoring.vgo
  staging/retired.vgo

Delete 3 file(s) from stockpile? [y/N] y
  ✓  modules/system/debug-tools.vgo
  ✓  modules/legacy/old-monitoring.vgo
  ✓  staging/retired.vgo

Deleted 3/3 files.
Run vigocli config publish to sync changes to .live/
```

## config freeze

Freeze config publishes until a specified time. Accepts either an RFC3339 timestamp or a duration.

```bash
vigocli config freeze --until <RFC3339|duration>
```

### Examples

```bash
# Freeze for 24 hours
vigocli config freeze --until 24h

# Freeze until a specific time
vigocli config freeze --until 2026-03-14T06:00:00Z
```

## config unfreeze

Remove the config publish freeze.

```bash
vigocli config unfreeze
```
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
