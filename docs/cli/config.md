# config

Config inspection commands.

## Subcommands

| Command | Description |
|---------|-------------|
| `config trace` | Trace config resolution for a hostname |
| `config publish` | Validate and publish config from proposed to live |

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
  nginx_port      = 443                             ← env_vars[production]
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
- The source column shows where each variable was set (module defaults, node vars, env_vars, or vars_from file)

## config publish

Validates `.hnb` files in `/etc/hobnob-server/hnb/proposed/` then atomically syncs to `/etc/hobnob-server/hnb/.live/` (the hidden directory the server reads from). See [Config Publish Pipeline](../operations/config-publish.md) for full details.

### Usage

```bash
hobnobcli config publish [--dry-run] [--dangerous] [--skip-url-check] [--url-timeout <duration>]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--dry-run` | `false` | Validate only |
| `--dangerous` | `false` | Allow non-idempotent resources |
| `--skip-url-check` | `false` | Skip HTTP aliveness checks for URLs |
| `--url-timeout` | `10s` | Timeout per URL aliveness check |

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
