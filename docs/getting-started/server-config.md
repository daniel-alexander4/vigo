
# Server Configuration

The server is configured via `server.yaml`. This page documents every section and option.

To see a fully annotated reference with all options, defaults, and descriptions:

```bash
vigocli server server.yaml full          # full reference
vigocli server server.yaml list          # list all sections
vigocli server server.yaml list smtp     # show one section
```

## Configuration File Location

The server looks for `server.yaml` in the current working directory. Override with:

```bash
VIGO_CONFIG_FILE=/path/to/server.yaml vigosrv
```

## Sections

### secrets

Determines how `secret:` references in config values are resolved.

```yaml
secrets:
  backend: local                          # local (default) | isopass
  key_file: "/srv/vigo/master.key"   # 32-byte hex master key (local only)
  secrets_dir: "/srv/vigo/secrets"    # encrypted files directory (local only)
```

| Option | Default | Description |
|--------|---------|-------------|
| `backend` | `local` | Secrets backend: `local` (AES-256-GCM encrypted files) or `isopass` (external API) |
| `key_file` | auto-generated | Path to 32-byte hex master key file (local backend, mode 0600) |
| `secrets_dir` | `/srv/vigo/secrets` | Directory for encrypted secret files (local backend) |
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
  hostname: "myserver.example.com"  # server's enrolled envoy hostname
  tls:
    cert_file: "tls/cert"
    key_file: "tls/key"
    ca_file: "tls/ca"
    ca_key_file: "tls/ca_key"
  tls_sans:
    - "192.168.1.2"
    - "vigo.example.com"
```

| Option | Default | Description |
|--------|---------|-------------|
| `grpc_listen` | `:1530` | gRPC listen address (agent communication, mTLS) |
| `api_listen` | `:8443` | REST API + web UI + metrics listen address |

| `hostname` | — | Server's enrolled envoy hostname. Used by the compliance page to show the server's own standards compliance separately from the fleet aggregate. Required when running in Docker (where `os.Hostname()` returns the container ID). |
| `tls.cert_file` | — | Server TLS certificate path |
| `tls.key_file` | — | Server TLS private key path |
| `tls.ca_file` | — | CA certificate for mTLS verification |
| `tls.ca_key_file` | — | CA private key — enables bootstrap CSR signing |
| `tls_sans` | `[]` | Extra Subject Alternative Names (IPs or DNS names) for auto-generated certs |

TLS certificates are auto-generated on first start when using the `local` secrets backend. The auto-generated cert includes `localhost`, `127.0.0.1`, the machine hostname, and all non-loopback interface IPs as SANs. When running in Docker, the container won't see the host's LAN IP — use `tls_sans` to add it so remote agents can verify the server certificate.

### database

```yaml
database:
  dsn: "secret:vigo/db/dsn"
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
  interval: "5m"
  jitter_percent: 20
  bundle_max_age: "24h"
  observe_only: false
```

| Option | Default | Description |
|--------|---------|-------------|
| `interval` | `5m` | Agent check-in frequency. Stale threshold = 2x this value |
| `jitter_percent` | `20` | Randomize check-in timing by 0–N% to avoid thundering herd |
| `bundle_max_age` | `24h` | How long signed policy bundles remain valid on agents. `0` = forever |
| `observe_only` | `false` | Global observe-only mode. All envoys report drift only — no changes applied. ORed with per-entry `observe_only` in envoys.vgo match blocks |

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
| `oidc.redirect_url` | — | OAuth callback URL (e.g., `http://vigo:8443/auth/callback`) |
| `oidc.scopes` | `[openid, profile, email]` | OIDC scopes to request |
| `isowebauth.namespace` | `vigo` | Namespace for SSH signature verification |

Basic auth stores user accounts in the database and bcrypt password hashes in the secrets provider at `vigo/web/auth/<username>`. An admin account is created automatically on first startup — activate it with `vigocli secrets set web vigo/web/auth/admin`.

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
| `run_store_capacity` | `20` | Per-envoy run depth in memory mode |
| `max_concurrent_streams` | `5000` | gRPC bidirectional stream limit |

See [Tuning](../operations/tuning.md) for detailed guidance.

### smtp

```yaml
smtp:
  host: "smtp.example.com"
  port: 587
  from: "vigo@example.com"
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

Available events: `envoy.enrolled`, `run.success`, `run.failure`, `drift.detected`, `secret.rotated`, `config.reload.failure`, `envoy.stale`, `convergence.threshold`.

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

See [AI Assistant](../operations/ai-providers.md) for details.

### backup

```yaml
backup:
  url: "s3://my-bucket/vigo"
  access_key_id: "secret:vigo/backup/aws_access_key"
  secret_access_key: "secret:vigo/backup/aws_secret_key"
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
  checkin_per_envoy: 6
  checkin_global: 500
```

| Option | Default | Description |
|--------|---------|-------------|
| `enabled` | `false` | Enable rate limiting on check-in RPCs |
| `checkin_per_envoy` | `6` | Max check-ins per envoy per minute |
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

Controls change approval workflow for `vigocli config publish`.

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
| `compliance_threshold` | `0` (disabled) | Auto-rollback if fleet convergence drops below this percentage after reload (1-100) |
| `rollback_window` | `"5m"` | Duration to monitor convergence after reload |
| `retraction.enabled` | `false` | Auto-generate `.retract` modules when modules are removed during publish. Requires `ai.enabled: true`. See [Module Retraction](../concepts/module-retraction.md) |

When `approval_required` is enabled, `vigocli config publish` no longer applies changes immediately. Instead it creates a pending change record that must be approved by a different admin via `vigocli config approve` or the REST API. Pending changes that are not approved within `approval_timeout` expire automatically.

If `compliance_threshold` is set, the server monitors fleet convergence for `rollback_window` after a config reload. If convergence drops below the threshold percentage, the server automatically rolls back to the previous config snapshot.

### stream_edit

Guardrails for `stream_edit` pipelines in file resources. Scripts piped through `stream_edit` run as root on managed nodes — these settings control what scripts are allowed.

```yaml
stream_edit:
  enabled: true                            # false = strip all stream_edit globally
  allowed_paths:                           # directories where scripts must reside
    - "/srv/vigo/scripts"
  default_timeout: "10s"                   # per-script timeout
```

| Option | Default | Description |
|--------|---------|-------------|
| `enabled` | `true` | When false, all `stream_edit` attributes are stripped from policy bundles before delivery to agents |
| `allowed_paths` | `["/srv/vigo/scripts"]` | Script paths must be under one of these directories. Paths outside are stripped with a warning |
| `default_timeout` | `"10s"` | Per-script execution timeout. Scripts exceeding this are killed and the resource fails |

In addition to server-side path validation, agents enforce:
- Scripts must be owned by root (uid 0)
- Scripts must not be world-writable
- Scripts must exist and be regular files


### task

Controls task dispatch behavior.

```yaml
task:
  require_definition: true
```

| Option | Default | Description |
|--------|---------|-------------|
| `require_definition` | `false` | When true, only named task definitions (from the `stockpile/tasks/` directory) can be dispatched. Ad-hoc commands are rejected with 403. |

When enabled, `POST /api/v1/task` requires a `taskName` field referencing a predefined task definition. Requests with only a `command` field (ad-hoc execution) are rejected. This prevents arbitrary command execution while still allowing controlled operations through pre-approved task definitions.

### branding

Custom branding for the web UI. When set, overrides the default Vigo logo and displays your organization's name in the sidebar and footer.

```yaml
branding:
  org_name: "ACME Corp"                       # shown in sidebar and footer
  logo_path: "/srv/vigo/branding/logo.svg"    # sidebar logo (SVG or PNG)
  favicon_path: "/srv/vigo/branding/icon.svg" # browser tab icon
  theme: "light"                              # "light", "dark", or "auto"
```

| Field | Default | Description |
|-------|---------|-------------|
| `org_name` | (none) | Organization name shown below the sidebar logo and in the page footer |
| `logo_path` | embedded Vigo logo | Path to custom sidebar logo (SVG or PNG) |
| `favicon_path` | embedded Vigo icon | Path to custom browser tab icon |
| `theme` | `light` | Default color theme: `light` (Catppuccin Latte), `dark` (Catppuccin Mocha), or `auto` (follow OS `prefers-color-scheme`). Individual operators can override this default via the sun/moon toggle in the top navbar; their choice is persisted per-browser in `localStorage` and takes precedence over the branding default. |

Place branding assets in `/srv/vigo/branding/` (or any path accessible to the server process). When a path is not set, the embedded Vigo default is used.

## Environment Variables

Only one environment variable is supported. All other configuration belongs in `server.yaml`.

| Variable | Default | Description |
|----------|---------|-------------|
| `VIGO_CONFIG_FILE` | `server.yaml` | Path to server config file (bootstraps everything else) |

The server version is injected at build time via `-ldflags="-X main.version=1.2.3"` and defaults to `dev`.

### paths

Directory paths for server subsystems.

```yaml
paths:
  migrations: "migrations"
  custom_traits: "custom-traits"
  docs: "docs"
  tasks: "stockpile/tasks"
  workflows: "workflows"
  agent_dist: "dist/agent"
  license_dir: "/srv/vigo/license"
  snapshots: "/srv/vigo/snapshots"
```

| Option | Default | Description |
|--------|---------|-------------|
| `snapshots` | `/srv/vigo/snapshots` | Directory where the `vigo-server-backup` module writes dated snapshot directories. The web UI reads this directory to display snapshot history at `/backups`. Must match the module's `snapshot_dir` var. |

### License


Controls how nodes are counted for license enforcement.

```yaml
license:
  mode: "local"      # default — count nodes on this server only
```

In multi-site deployments using spanner, set `mode: aggregate` on the hub to count nodes across all spokes under a single license. Spokes skip enforcement entirely in this mode.

```yaml
# Hub server.yaml
license:
  mode: "aggregate"

# Spoke server.yaml — no license section needed (hub enforces)
```

### grc

Push compliance evidence to external GRC platforms (Vanta, Drata, ServiceNow GRC) on a configurable schedule.

```yaml
grc:
  integrations:
    - name: vanta
      enabled: true
      endpoint: "https://api.vanta.com/v1/evidence"
      api_key: "secret:vigo/grc/vanta-api-key"
      push_interval: "6h"
      frameworks:
        hipaa: "hipaa-2013"
        soc2: "soc2-2017"
        pci-dss: "pci-dss-4.0"
```

| Field | Default | Description |
|-------|---------|-------------|
| `name` | (required) | Integration name (for logging) |
| `enabled` | `false` | Enable this integration |
| `endpoint` | (required) | API URL to POST evidence to |
| `api_key` | (required) | Bearer token for authentication (supports `secret:` prefix) |
| `push_interval` | `6h` | How often to push evidence (minimum 1 minute) |
| `frameworks` | (required) | Map of Vigo standard keys to GRC platform framework IDs |

The pusher sends a JSON payload with fleet size, per-standard coverage percentages, covered/missing control counts, and envoy count. Evidence is pushed on startup and at each interval.

### Security Alert Events

In addition to operational events (`drift.detected`, `envoy.stale`, etc.), the following security-specific events can be configured in `smtp.events` and webhook `--events`:

| Event | Trigger |
|-------|---------|
| `security.rootkit` | chkrootkit detects infection or rkhunter detects warnings |
| `security.malware` | ClamAV infected count increases from 0 |
| `security.integrity` | AIDE clean changes from true to false |
| `security.cve_critical` | Trivy critical CVE count increases from 0 |
| `security.hardening_drop` | Lynis hardening score drops by more than 10 points |

These events fire automatically during agent trait reporting — no additional configuration beyond enabling them in `smtp.events` or webhook subscriptions.

```yaml
smtp:
  events:
    security.rootkit:
      recipients: ["soc@example.com"]
    security.cve_critical: {}    # uses default recipients
```

### swarm

Peer-to-peer content distribution. When enabled, agents download large files from each other instead of all pulling from the server.

```yaml
swarm:
  enabled: true
  seed_dir: "/var/lib/vigo/swarm/seed"
  port: 1531
  max_bandwidth_percent: 50
  seeding_envoys:
    - "build-*.ci.example.com"
    - "vault.ops.example.com"
```

| Option | Default | Description |
|--------|---------|-------------|
| `enabled` | `false` | Enable swarm P2P distribution |
| `seed_dir` | `/var/lib/vigo/swarm/seed` | Directory where `vigocli swarm distribute` writes seed files |
| `port` | `1531` | Agent peer server listen port (mTLS HTTPS) |
| `max_bandwidth_percent` | `50` | Maximum network bandwidth agents will use for swarm transfers (percentage of interface capacity) |
| `seeding_envoys` | `[]` | Hostname patterns (filepath.Match globs) of envoys permitted to originate seeds. The server's own envoy (`server.hostname`) is implicitly allowed — don't repeat it here. Patterns use `*`, `?`, `[abc]`; segments between dots are matched independently, so `*.ci.example.com` matches `a.ci.example.com` but not `a.b.ci.example.com`. An empty or omitted list means only the server's envoy can seed. A bare `"*"` permits every enrolled envoy and logs a WARN at startup — narrow unless intentional. |

Agents listen on the configured port for peer chunk requests using the same mTLS certificates as gRPC communication. Multicast discovery uses UDP on `224.0.0.42` at the same port.

Non-allowlisted seeds are rejected at check-in. The agent moves the file into `seed/admin_rejected/` with a reason string so operators can see which seeds were blocked and why.

See [Swarm Concepts](../concepts/swarm.md) and [Swarm Operations](../operations/swarm.md) for details.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
