
# config

Config inspection commands.

## Subcommands

| Command | Description |
|---------|-------------|
| `config edit` | Open a .vgo file in an editor with validation on save |
| `config trace` | Trace config resolution for a host, or find where a configcrate/usercrate is applied |
| `config tree` | Show the directory inheritance hierarchy |
| `config search` | Search envoy entries by match pattern, configcrate, or var |
| `config publish` | Validate and publish config from stage to live |
| `config history` | View config publish history |
| `config rollback` | Rollback to a previous config version |
| `config freeze` | Freeze config publishes until a specified time |
| `config unfreeze` | Remove the config publish freeze |
| `config status` | Show config-reload health (exit 0 healthy, 1 unhealthy) |
| `config validate` | Validate `.vgo` files for YAML syntax, cross-references, and idempotency guards |
| `config promote` | Copy a configcrate's `environment_overrides` from one tier to another |
| `config dedup` | Find redundant configcrates and variables in the config tree |
| `config stacks` | Manage the stacks directory |
| `config server.yaml` | Annotated `server.yaml` reference: list sections, show full config, or filter by section |
| `config examples list` | Show available install templates under `stacks-examples/` |
| `config examples copy <name>` | Copy an install template into `stacks/` |

## config server.yaml

Annotated `server.yaml` reference. Has subcommands: `full`, `list`.

`full` prints the whole annotated file with every option, default, and description. Redirect it to a new file, never over your live `server.yaml`:

```bash
vigocli config server.yaml full
vigocli config server.yaml full > server.yaml.example
```

`list` lists the configuration sections, or shows one:

```bash
vigocli config server.yaml list
vigocli config server.yaml list smtp
vigocli config server.yaml list ai
```

## config edit

Open a `.vgo` config file in an editor with YAML validation on save. Accepts a configcrate name, relative path, or absolute path. Configcrate names are searched recursively in `stacks/configcrates/`.

### Usage

```bash
vigocli config edit <name-or-path> [--editor <editor>]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--editor` | `$EDITOR` or `vi` | Editor to use |

### Examples

```bash
vigocli config edit nginx                    # finds stacks/configcrates/.../nginx.vgo
vigocli config edit configcrates/nginx.vgo        # relative to stacks/
vigocli config edit danlap.vgo               # finds by name anywhere in stacks/
vigocli config edit --editor nano nginx      # use nano instead of vim
```

When using vi/vim, the editor launches with Vigo-aware syntax highlighting and validates the file on every `:w`. Validation errors abort the write — use `:w!` to force. Other editors get post-save validation only.

## config trace

Two directions, one command.

**Forward (hostname-rooted).** Shows the full directory inheritance chain for a hostname with colored output: which `common.vgo` files contributed configcrates and vars at each level, which vars override parent values, and where each configcrate originated.

**Inverse (subject-rooted).** Answers "where is configcrate/usercrate X applied?" — enumerates every enrolled envoy whose resolved config includes the subject, with the carrier (file path / role / environment override) that pulled it in. With `--include-tree`, additionally lists config-tree patterns that reference the subject but match no currently-enrolled envoy.

### Usage

```bash
# Forward
vigocli config trace <hostname>
vigocli config trace <hostname> --configcrate <configcrate-name>
vigocli config trace --host <hostname>

# Inverse
vigocli config trace --configcrate <configcrate-name>
vigocli config trace --usercrate <usercrate-name>
vigocli config trace --configcrate <configcrate-name> --include-tree
```

### Flags

| Flag | Description |
|------|-------------|
| `--host` | Hostname to trace (alias for the positional argument) |
| `--configcrate` | With a hostname: filter to that configcrate's branch. Without a hostname: find where the configcrate is applied across the fleet. |
| `--usercrate` | Find where this usercrate is applied across the fleet (inverse mode only — cannot be combined with a hostname). |
| `--include-tree` | Inverse mode only: also surface config-tree patterns that reference the subject but match no enrolled envoy. |

Exactly one subject must be specified per invocation: the positional `<hostname>`, `--host`, `--configcrate` (alone), or `--usercrate`.

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

Configcrate Inheritance:
  ┌─ common.vgo ───────────────────────────┐
  │ configcrates: sshd, monitoring              │
  └────────────────────────────────────────┘
              │
              ▼
  ┌─ production/common.vgo ────────────────┐
  │ configcrates: log-shipping, auditd          │
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
    dns_server = 1.1.1.1  configcrate:sshd
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
    nginx ──▶ sshd (configcrate)
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
2. **Usercrates** — auto-applied per-user configcrates from `stacks/**/usercrates/`, listed with the scope directory where each one lives. Only rendered when any usercrates apply. See [Usercrates](../../reference/configcrate-language.md#usercrates).
3. **Configcrate Inheritance** — configcrates and roles contributed at each level (usercrates excluded — they show in section 2)
4. **Variable Inheritance** — vars at each level with override markers, plus final resolution table
5. **Password Inheritance** — all `secret:` references with masked values, secret paths, source configcrate, and provider
6. **Dependency Inheritance** — configcrate and resource dependency DAG (depends_on, before, notify, subscribes, watch_secret)
7. **Compliance Inheritance** — compliance framework tags on each configcrate (e.g., hipaa, pci-dss with control IDs)
8. **Conditional Inheritance** — `when:` expressions with server vs agent evaluation classification

### Filtering by configcrate

```
$ vigocli config trace web01.prod.example.com --configcrate log-shipping
```

When `--configcrate` is specified alongside a hostname, only inheritance levels that contribute that configcrate are shown, along with the relevant role expansion.

### Inverse mode — "where is this applied?"

```
$ vigocli config trace --configcrate nginx

configcrate: nginx

Enrolled envoys:

    HOSTNAME              MATCH               ENV         CARRIER
    plex.alex.lan         plex.alex.lan       —           default/envoys.vgo
    web01.prod.example    web*.prod.example.. production  production/common.vgo

  2 envoys.
```

`--include-tree` widens the inverse search to surface configured patterns that no enrolled envoy currently matches — useful for catching wiring you've authored but haven't deployed yet:

```
$ vigocli config trace --configcrate nginx --include-tree

configcrate: nginx

Enrolled envoys:
    HOSTNAME              MATCH               ENV         CARRIER
    plex.alex.lan         plex.alex.lan       —           default/envoys.vgo

Config-tree patterns (no enrolled envoy matches):
    MATCH                       ENV         CARRIER
    staging-only.example.com    staging     staging/common.vgo

  1 envoy, 1 config-tree pattern.
```

Usercrate inverse works the same way:

```
$ vigocli config trace --usercrate alexander4
```

### No match

```
$ vigocli config trace unknown.example.com
! No config entry matches hostname "unknown.example.com"
```

### Notes

- Secret values (prefixed with `secret:`) are always masked as `secret:****` in both CLI and web UI — the secret path is shown but never the resolved value
- The source column shows where each variable was set (configcrate defaults, inherited `common.vgo`, match-block vars, or `environments.vgo`)
- The inheritance chain shows vars contributed at each `common.vgo` level with override markers
- Conditional expressions are classified as `server` (pure variable refs) or `agent` (contains function calls like `os_family()`) based on whether they require agent-side trait evaluation

## config tree

Displays the directory inheritance hierarchy as a colored tree. Shows `common.vgo` files at each level, leaf entries with their match patterns, and accumulated configcrate totals.

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
- Inherited configcrates: dim gray
- Own configcrates: bold white
- Excluded configcrates: red strikethrough
- Total line: green

## config search

Search envoy entries by match pattern, configcrate name, or var name. Shows the full inheritance chain, own configcrates, and accumulated totals.

### Usage

```bash
vigocli config search --match <glob>
vigocli config search --configcrate <name>
vigocli config search --var <name>
```

At least one flag is required. Flags can be combined.

### Flags

| Flag | Description |
|------|-------------|
| `--match` | Glob pattern to match against envoy `match:` patterns (e.g., `*web*`) |
| `--configcrate` | Find entries that use this configcrate (inherited or direct) |
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

# Find every entry using the nginx configcrate
$ vigocli config search --configcrate nginx
  web*.prod.example.com  production/web
  ...

# Find where log_level is defined
$ vigocli config search --var log_level
  web*.prod.example.com  production/web
  Var: log_level = warn
```

## config publish

Validates `.vgo` files in `/srv/vigo/stacks/` then atomically syncs to `/srv/vigo/.live/` (the directory the server reads). Changed configcrate files are automatically run through the configcrate linter, which repairs YAML issues, normalizes formatting, validates against the schema, and adds `#~` auto-comments. See [Config Publish Pipeline](../../howto/configcrates/publish-config.md) for full details.

Publish carries forward only files some carrier — a role, role `case:` branch, role `includes:` chain, match block, `common.vgo`, or `environments.vgo` — can actually reach. Configcrates and usercrates with no inbound reference, and template files no configcrate names via `source:`, are dropped from `.live/`. Structural files (hostcrates, `common.vgo`, `roles.vgo`, `environments.vgo`, `waivers.vgo`, task / workflow definitions) are always preserved. Use `--no-prune` to fall back to a verbatim mirror.

### Usage

```bash
vigocli config publish [--dry-run] [--skip-url-check] [--url-timeout <duration>] [--no-prune]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--dry-run` | `false` | Validate only |
| `--skip-url-check` | `false` | Skip HTTP aliveness checks for URLs |
| `--url-timeout` | `10s` | Timeout per URL aliveness check |
| `--diff` | `false` | Show line-level diff for each changed file |
| `--ask-ai` | `false` | Ask AI to suggest fixes for validation errors |
| `--no-prune` | `false` | Publish every file in `stacks/` verbatim — skip the reachability prune |
| `--server-root` | `/srv/vigo` | Server root holding `stacks/`, `.live/`, and `config-history/`. Override to publish a non-default instance (the three dirs are always siblings under it). |

### Examples

```bash
# Validate without publishing
$ vigocli config publish --dry-run
Validation passed: 12 configcrates, 3 roles, 8 envoy entries
  + configcrates/nginx.vgo
  + envoys/web.vgo

# Publish
$ vigocli config publish
Published: 12 configcrates, 3 roles, 8 envoy entries (15 files synced)
  + configcrates/nginx.vgo
  + envoys/web.vgo

# Nothing changed
$ vigocli config publish
Nothing to publish — stage matches live.
```

### Configcrate Retraction

When `publish.retraction.enabled` and `ai.enabled` are both true in `server.yaml`, every `config publish` automatically generates `.retract` files for new and changed configcrates. When a configcrate is removed, the CLI also prints which envoys are affected and per-resource reversal confidence. The `.retract` file is inert until renamed to `.vgo`. See [Configcrate Retraction](../../reference/configcrate-language.md) for the full workflow.

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

Every publish snapshots the tree it is about to sync into `config-history/` before `.live/` changes; a publish whose snapshot fails is refused and nothing reaches the fleet. Version names carry nanoseconds, so two publishes in the same second never share a snapshot. Publishes and rollbacks take one lock (`<server-root>/.publish.lock`), so a rollback never swaps `.live/` out in the middle of a publish.

```bash
# List available versions
vigocli config rollback

# Rollback to a specific version
vigocli config rollback 20260313T143000.123456789Z
```

### Example

```
$ vigocli config rollback
Available versions:
  20260313T143000.123456789Z
  20260312T091500.004211932Z

$ vigocli config rollback 20260312T091500.004211932Z
Rolled back to version 20260312T091500.004211932Z
```

## config freeze

Freeze config publishes until a specified time. Accepts either an RFC3339 timestamp or a duration.

The freeze is written to `<server-root>/.publish-freeze`, so it survives a server restart and `vigocli config publish` refuses to run while it is active. `maintenance.freeze_until` in `server.yaml` is a second freeze that only an edit to the file lifts; see [server.yaml](../server-yaml.md#maintenance). A stacks push picked up by the spanner stacks watcher is held back by either freeze too.

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

## config status

Show config-reload health. Reports whether the most recent reload succeeded; useful for monitoring hooks and shell scripts.

```bash
vigocli config status
```

Output includes the current config version, the timestamp of the last reload attempt, and (when non-empty) the last error. Exit code is `0` when healthy, `1` when the last reload returned an error (e.g., `IdempotencyErrors` that left the store empty).

### Example

```bash
$ vigocli config status
Config: healthy
Version: 42
Last reload: 2026-05-24T13:14:15Z

$ vigocli config status >/dev/null && echo ok || echo broken
ok
```
