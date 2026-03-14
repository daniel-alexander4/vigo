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
  backend: local                          # local (default) | static | isopass
  key_file: "/etc/hobnob-server/master.key"   # 32-byte hex master key (local only)
  secrets_dir: "/etc/hobnob-server/secrets"    # encrypted files directory (local only)
```

| Option | Default | Description |
|--------|---------|-------------|
| `backend` | `local` | Secrets backend: `local` (AES-256-GCM encrypted files), `static` (.env file), `isopass` (external API) |
| `key_file` | auto-generated | Path to 32-byte hex master key file (local backend, mode 0600) |
| `secrets_dir` | `/etc/hobnob-server/secrets` | Directory for encrypted secret files (local backend) |
| `env_file` | `.env` | Path to .env file (static backend only) |
| `url` | — | Isopass API base URL (isopass backend only) |
| `token_env` | `HOBNOB_SECRETS_TOKEN` | Env var containing bearer token (isopass backend only) |
| `tls_skip_verify` | `false` | Skip TLS verification for isopass (not recommended) |

The `local` backend auto-generates the master key on first start if `key_file` doesn't exist.

### server

Listener addresses and TLS configuration.

```yaml
server:
  grpc_listen: ":1530"
  api_listen: ":8181"
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
| `api_listen` | `:8181` | REST API + web UI + metrics listen address |
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
| `retention` | — | Auto-prune runs/tasks/workflows older than this (e.g., `90d`, `2160h`). Empty = no pruning |

### checkin

```yaml
checkin:
  base_interval: "5m"
  jitter_percent: 20
  bundle_max_age: "24h"
```

| Option | Default | Description |
|--------|---------|-------------|
| `base_interval` | `5m` | Agent check-in frequency. Stale threshold = 2x this value |
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

### federation

```yaml
federation:
  role: standalone
```

| Option | Default | Description |
|--------|---------|-------------|
| `role` | `standalone` | Server role: `standalone`, `hub`, or `spoke` |
| `spoke_id` | — | Unique spoke identifier (required for spoke mode) |
| `hub_addr` | — | Hub gRPC address (optional, for future spoke→hub communication) |
| `spokes` | — | Hub-only: list of spoke definitions |
| `spokes[].id` | — | Unique spoke identifier |
| `spokes[].addr` | — | Spoke gRPC address (host:port) |
| `spokes[].patterns` | — | Hostname globs routed to this spoke |

Federation requires a paid license — the server will refuse to start in hub or spoke mode on the built-in free license. See [Federation](../concepts/federation.md) for details.

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

## Environment Variables

These override config paths at startup:

| Variable | Default | Description |
|----------|---------|-------------|
| `HOBNOB_CONFIG_FILE` | `server.yaml` | Path to server config file |
| `HOBNOB_THRALL_CONFIGS_DIR` | `.` | Path to thrall-configs directory |
| `HOBNOB_MIGRATIONS_DIR` | `migrations` | Path to database migrations |
| `HOBNOB_CUSTOM_TRAITS_DIR` | `custom-traits` | Path to custom trait collectors |
| `HOBNOB_TASKS_DIR` | `tasks` | Path to task definitions |
| `HOBNOB_WORKFLOWS_DIR` | `workflows` | Path to workflow definitions |
| `HOBNOB_DOCS_DIR` | `docs` | Path to documentation files |
| `HOBNOB_AGENT_DIR` | `dist/agent` | Path to agent binaries for bootstrap |
| `HOBNOB_LICENSE_DIR` | `/etc/hobnob-server/license` | Path to license files |
| `HOBNOB_VERSION` | `dev` | Server version string |
| `HOBNOB_DANGEROUS` | — | Set to `1` to disable config safety checks |
