# Server Configuration

The server is configured via `server.yaml`. This page documents every section and option.

## Configuration File Location

The server looks for `server.yaml` in the current working directory. Override with:

```bash
HOBNOB_CONFIG_FILE=/path/to/server.yaml hobd
```

## Sections

### secrets

Determines how `secret:` references in config values are resolved.

```yaml
secrets:
  backend: local                          # local (default) | isopass
  key_file: "/srv/hobnob/master.key"   # 32-byte hex master key (local only)
  secrets_dir: "/srv/hobnob/secrets"    # encrypted files directory (local only)
```

| Option | Default | Description |
|--------|---------|-------------|
| `backend` | `local` | Secrets backend: `local` (AES-256-GCM encrypted files) or `isopass` (external API) |
| `key_file` | auto-generated | Path to 32-byte hex master key file (local backend, mode 0600) |
| `secrets_dir` | `/srv/hobnob/secrets` | Directory for encrypted secret files (local backend) |
| `url` | — | Isopass API base URL (isopass backend only) |
| `token_file` | — | File containing bearer token (isopass backend, required, mode 0600) |
| `tls_skip_verify` | `false` | Skip TLS verification for isopass (not recommended) |

The `local` backend auto-generates the master key on first start if `key_file` doesn't exist.

### server

Listener addresses and TLS configuration.

```yaml
server:
  grpc_listen: ":1530"
  api_listen: ":8443"
  tls:
    cert_file: "tls/cert"
    key_file: "tls/key"
    ca_file: "tls/ca"
    ca_key_file: "tls/ca_key"
  tls_sans:
    - "192.168.1.2"
    - "hobnob.example.com"
```

| Option | Default | Description |
|--------|---------|-------------|
| `grpc_listen` | `:1530` | gRPC listen address (agent communication, mTLS) |
| `api_listen` | `:8443` | REST API + web UI + metrics listen address |
| `tls.cert_file` | — | Server TLS certificate path |
| `tls.key_file` | — | Server TLS private key path |
| `tls.ca_file` | — | CA certificate for mTLS verification |
| `tls.ca_key_file` | — | CA private key — enables bootstrap CSR signing |
| `tls_sans` | `[]` | Extra Subject Alternative Names (IPs or DNS names) for auto-generated certs |

TLS certificates are auto-generated on first start when using the `local` secrets backend. The auto-generated cert includes `localhost`, `127.0.0.1`, the machine hostname, and all non-loopback interface IPs as SANs. When running in Docker, the container won't see the host's LAN IP — use `tls_sans` to add it so remote agents can verify the server certificate.

### database

```yaml
database:
  dsn: "secret:hobnob/db/dsn"
  driver: sqlite
  retention: "90d"
```

| Option | Default | Description |
|--------|---------|-------------|
| `dsn` | — | SQLite file path or Postgres connection string (supports `secret:` prefix) |
| `driver` | `sqlite` | Database driver: `sqlite` or `postgres` |
| `retention` | — | Auto-prune runs/pushes/workflows older than this (e.g., `90d`, `2160h`). Empty = no pruning |

### checkin

```yaml
checkin:
  interval: "5m"
  jitter_percent: 20
  bundle_max_age: "24h"
```

| Option | Default | Description |
|--------|---------|-------------|
| `interval` | `5m` | Agent check-in frequency. Stale threshold = 2x this value |
| `jitter_percent` | `20` | Randomize check-in timing by 0–N% to avoid thundering herd |
| `bundle_max_age` | `24h` | How long signed policy bundles remain valid on agents. `0` = forever |

### bootstrap

```yaml
bootstrap:
  cert_validity: "8760h"
  trusted_enrollment:
    - pattern: "*"
      cidrs: ["192.168.0.0/16", "10.0.0.0/8"]
```

| Option | Default | Description |
|--------|---------|-------------|
| `cert_validity` | `8760h` (1 year) | Signed agent certificate lifetime |
| `trusted_enrollment` | — | List of patterns for token-free enrollment |
| `trusted_enrollment[].pattern` | — | Hostname glob pattern |
| `trusted_enrollment[].cidrs` | — | Source CIDR allowlist |

### auth

```yaml
auth:
  method: basic
```

| Option | Default | Description |
|--------|---------|-------------|
| `method` | `basic` | Authentication method: `basic`, `oidc`, `isowebauth` |
| `session_idle_timeout` | `15m` | Session idle timeout (HIPAA compliant default) |
| `oidc.issuer` | — | OIDC provider URL (required for `oidc`) |
| `oidc.client_id` | — | OIDC client ID (required for `oidc`) |
| `oidc.client_secret` | — | OIDC client secret (supports `secret:` prefix) |
| `oidc.redirect_url` | — | OAuth callback URL (e.g., `http://hobnob:8443/auth/callback`) |
| `oidc.scopes` | `[openid, profile, email]` | OIDC scopes to request |
| `isowebauth.namespace` | `hobnob` | Namespace for SSH signature verification |

Basic auth stores user accounts in the database and bcrypt password hashes in the secrets provider at `hobnob/web/auth/<username>`. An admin account is created automatically on first startup — activate it with `hobnobcli secrets set-web-password hobnob/web/auth/admin`.

See [Authentication](../operations/authentication.md) for setup details.

### watcher

```yaml
watcher:
  enabled: true
  poll_interval: "5m"
```

| Option | Default | Description |
|--------|---------|-------------|
| `enabled` | `true` | Enable secret rotation polling |
| `poll_interval` | `5m` | How often to check for rotated secrets |

Only active when the secrets backend supports versioning (isopass). Disabled on spoke servers.

### spanner

```yaml
spanner:
  role: standalone
```

| Option | Default | Description |
|--------|---------|-------------|
| `role` | `standalone` | Server role: `standalone`, `hub`, or `spoke` |
| `spoke_id` | — | Unique spoke identifier (required for spoke mode) |
| `hub_addr` | — | Hub gRPC address (spoke→hub status reporting) |
| `hub_fallback_addrs` | — | Spoke-only: fallback hub addresses for failover |
| `spokes` | — | Hub-only: list of spoke definitions |
| `spokes[].id` | — | Unique spoke identifier |
| `spokes[].addr` | — | Spoke gRPC address (host:port) |
| `spokes[].patterns` | — | Hostname globs routed to this spoke |
| `auto_failover.enabled` | `false` | Hub-only: auto-drain unhealthy spokes |
| `auto_failover.threshold` | `3` | Consecutive failures before drain |
| `auto_failover.interval` | `30s` | Health check interval |

Spanner requires a paid license — the server will refuse to start in hub or spoke mode on the built-in free license. See [Spanner](../concepts/spanner.md) for details.

### tuning

All fields are optional with sensible defaults.

```yaml
tuning:
  signature_window: "5m"
  last_seen_flush: "10s"
  traits_prune_interval: "5m"
  metrics_interval: "30s"
  traits_scanner_ttl: "30s"
  run_store: "database"
  run_store_capacity: 20
  max_concurrent_streams: 5000
  webhook:
    timeout: "10s"
    max_retries: 3
    max_concurrent: 20
    retry_delay: "5s"
```

| Option | Default | Description |
|--------|---------|-------------|
| `signature_window` | `5m` | Max age of signed agent requests |
| `last_seen_flush` | `10s` | FleetIndex dirty-set flush interval to SQLite |
| `traits_prune_interval` | `5m` | Stale trait cleanup frequency |
| `metrics_interval` | `30s` | Prometheus metrics collection interval |
| `traits_scanner_ttl` | `30s` | Custom trait collector cache TTL |
| `run_store` | `database` | Run history backend: `database` or `memory` |
| `run_store_capacity` | `20` | Per-thrall run depth in memory mode |
| `max_concurrent_streams` | `5000` | gRPC bidirectional stream limit |

See [Tuning](../operations/tuning.md) for detailed guidance.

### smtp

```yaml
smtp:
  host: "smtp.example.com"
  port: 587
  from: "hobnob@example.com"
  recipients: ["admin@example.com"]
```

| Option | Default | Description |
|--------|---------|-------------|
| `host` | — | SMTP relay hostname. Empty = notifications disabled |
| `port` | `587` | SMTP port |
| `from` | — | Sender email address |
| `username` | — | SMTP auth username |
| `password` | — | SMTP auth password (supports `secret:` prefix) |
| `tls` | `true` | Use STARTTLS |
| `recipients` | — | Default recipient list |
| `events` | all enabled | Per-event overrides (see below) |
| `digest.interval` | — | Batch window for digest summaries (e.g., `1h`) |

Available events: `thrall.enrolled`, `run.success`, `run.failure`, `drift.detected`, `secret.rotated`, `config.reload.failure`, `thrall.stale`, `compliance.threshold`.

See [Email Notifications](../operations/smtp.md) for details.

### ai

```yaml
ai:
  enabled: true
  provider: "ollama"
  model: "qwen2.5:7b"
```

| Option | Default | Description |
|--------|---------|-------------|
| `enabled` | `false` | Enable AI assistant |
| `provider` | `ollama` | LLM provider: `ollama`, `claude`, `openai`, `openai-compat` |
| `model` | provider-specific | Model name (ollama: `qwen2.5:7b`, claude: `claude-sonnet-4-20250514`, openai: `gpt-4o`) |
| `max_tokens` | `4096` | Max tokens per response |
| `base_url` | auto for ollama | API base URL (required for `openai-compat`) |
| `api_key` | — | API key for paid providers (supports `secret:` prefix) |
| `rate_limit` | `0` (unlimited) | Requests per minute |
| `max_tool_rounds` | `10` | Max tool-use iterations per request |
| `context_mode` | `auto` for ollama, `tools` otherwise | `tools`, `prefetch`, or `auto` |
| `docs_mode` | `full` | Inject docs into AI system prompt: `full`, `condensed`, `off` |

See [AI Assistant](../ai/_index.md) for details.

### backup

```yaml
backup:
  url: "s3://my-bucket/hobnob"
  access_key_id: "secret:hobnob/backup/aws_access_key"
  secret_access_key: "secret:hobnob/backup/aws_secret_key"
```

| Option | Default | Description |
|--------|---------|-------------|
| `url` | — | Replica URL: `s3://`, `gcs://`, `sftp://`, `file:///`. Empty = disabled |
| `access_key_id` | — | S3/GCS access key (supports `secret:` prefix) |
| `secret_access_key` | — | S3/GCS secret key (supports `secret:` prefix) |
| `region` | provider default | AWS region |
| `endpoint` | — | S3-compatible endpoint (MinIO, Backblaze) |
| `force_path_style` | `false` | Path-style S3 URLs (required for MinIO) |
| `retention` | `720h` (30 days) | Snapshot retention period |
| `sync_interval` | `1s` | WAL sync frequency |

Only applies when `database.driver` is `sqlite`. See [Backup](../operations/backup.md) for details.

### rate_limit

```yaml
rate_limit:
  enabled: true
  checkin_per_thrall: 6
  checkin_global: 500
```

| Option | Default | Description |
|--------|---------|-------------|
| `enabled` | `false` | Enable rate limiting on check-in RPCs |
| `checkin_per_thrall` | `6` | Max check-ins per thrall per minute |
| `checkin_global` | `500` | Max total check-ins per second |

See [Rate Limiting](../operations/rate-limiting.md) for details.

### maintenance

```yaml
maintenance:
  freeze_until: "2026-03-14T06:00:00Z"
```

| Option | Default | Description |
|--------|---------|-------------|
| `freeze_until` | — | RFC3339 timestamp; config publishes blocked while active |

See [Maintenance Windows](../operations/maintenance-windows.md) for details.

### publish

Controls change approval workflow for `hobnobcli config publish`.

```yaml
publish:
  approval_required: true
  approval_timeout: "24h"
  compliance_threshold: 90
  rollback_window: "5m"
```

| Option | Default | Description |
|--------|---------|-------------|
| `approval_required` | `false` | When true, `config publish` creates a pending change requiring a second admin to approve |
| `approval_timeout` | `"24h"` | Pending changes expire after this duration |
| `compliance_threshold` | `0` (disabled) | Auto-rollback if fleet compliance drops below this percentage after reload (1-100) |
| `rollback_window` | `"5m"` | Duration to monitor compliance after reload |

When `approval_required` is enabled, `hobnobcli config publish` no longer applies changes immediately. Instead it creates a pending change record that must be approved by a different admin via `hobnobcli config approve` or the REST API. Pending changes that are not approved within `approval_timeout` expire automatically.

If `compliance_threshold` is set, the server monitors fleet compliance for `rollback_window` after a config reload. If compliance drops below the threshold percentage, the server automatically rolls back to the previous config snapshot.

### push

Controls push dispatch behavior.

```yaml
push:
  require_definition: true
```

| Option | Default | Description |
|--------|---------|-------------|
| `require_definition` | `false` | When true, only named push definitions (from the `push/` directory) can be dispatched. Ad-hoc commands are rejected with 403. |

When enabled, `POST /api/v1/push` requires a `pushName` field referencing a predefined push definition. Requests with only a `command` field (ad-hoc execution) are rejected. This prevents arbitrary command execution while still allowing controlled operations through pre-approved push definitions.

## Environment Variables

Only one environment variable is supported. All other configuration belongs in `server.yaml`.

| Variable | Default | Description |
|----------|---------|-------------|
| `HOBNOB_CONFIG_FILE` | `server.yaml` | Path to server config file (bootstraps everything else) |

The server version is injected at build time via `-ldflags="-X main.version=1.2.3"` and defaults to `dev`.

### paths

Directory paths for server subsystems.

```yaml
paths:
  migrations: "migrations"
  custom_traits: "custom-traits"
  docs: "docs"
  push: "push"
  workflows: "workflows"
  agent_dist: "dist/agent"
  license_dir: "/srv/hobnob/license"
```
