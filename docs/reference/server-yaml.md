
---
title: server.yaml Reference
---

# server.yaml Reference

Complete reference for all `server.yaml` configuration sections. The server looks for `server.yaml` in the current working directory. Override with `VIGO_CONFIG_FILE=/path/to/server.yaml`.

Fields that accept `secret:path` are resolved through the secrets backend at startup.

## server

Ports and TLS configuration.

```yaml
server:
  grpc_listen: ":1530"              # gRPC port for agent check-ins
  api_listen: ":8443"               # REST API + web UI port
  hostname: "myserver.example.com"  # server's enrolled envoy hostname
  tls:
    cert_file: "tls/cert"           # server TLS certificate
    key_file: "tls/key"             # server TLS private key
    ca_file: "tls/ca"               # certificate authority (verifies agents)
    ca_key_file: "tls/ca_key"       # CA private key (enables bootstrap CSR signing)
  tls_sans:                         # extra SANs for auto-generated certs
    - "192.168.1.2"                 # host LAN IP (needed in Docker)
    - "vigo.example.com"            # DNS name agents use
```

| Field | Default | Description |
|-------|---------|-------------|
| `grpc_listen` | `:1530` | gRPC listen address for agent communication (mTLS) |
| `api_listen` | `:8443` | REST API, web UI, and metrics listen address (TLS) |
| `hostname` | (auto) | Server's enrolled envoy hostname. The compliance page uses this to show the server's own standards compliance separately. Falls back to `VIGO_HOSTNAME` env var, then `os.Hostname()`. Set this when running in Docker. |
| `tls.cert_file` | `tls/cert` | Path to server TLS certificate |
| `tls.key_file` | `tls/key` | Path to server TLS private key |
| `tls.ca_file` | `tls/ca` | Path to CA certificate for mTLS verification |
| `tls.ca_key_file` | (optional) | CA private key — enables bootstrap enrollment and CSR signing |
| `tls_sans` | (empty) | Additional Subject Alternative Names for auto-generated certs |

## database

Storage backend. SQLite (default) or PostgreSQL.

```yaml
database:
  dsn: "secret:vigo/db/dsn"         # SQLite: file path; Postgres: connection string
  driver: sqlite3                    # sqlite3 (default) | postgres
  retention: "30d"                   # auto-prune old runs/task runs/workflows
```

| Field | Default | Description |
|-------|---------|-------------|
| `dsn` | (required) | Database connection string. Supports `secret:` prefix |
| `driver` | `sqlite3` | Database driver: `sqlite3` or `postgres` |
| `retention` | `30d` | Auto-prune data older than this duration. Applies to `runs` (cascades `run_results`), `push_runs`, `workflow_runs`, `file_snapshots`, `convergence_history`, `compliance_history`. `audit_entries` is never trimmed (tamper chain). |

## secrets


How `secret:` references in config values are resolved.

```yaml
# Local backend (default) — encrypted files on disk
secrets:
  backend: local
  key_file: "/srv/vigo/master.key"   # AES-256-GCM key (0600); omit to auto-generate
  secrets_dir: "/srv/vigo/secrets"   # encrypted secret files

# Isopass backend — external secrets API
secrets:
  backend: isopass
  url: "https://isopass.internal:8443"
  token_file: "/srv/vigo/isopass-token"
  tls_skip_verify: false
```

```yaml
# HashiCorp Vault (KV v2)
secrets:
  backend: vault
  url: "https://vault.example.com:8200"
  token_file: "/srv/vigo/vault-token"
  mount_path: "secret"

# AWS Secrets Manager
secrets:
  backend: aws
  region: "us-east-1"

# Azure Key Vault
secrets:
  backend: azure
  vault_name: "mycompany-vigo"

# GCP Secret Manager
secrets:
  backend: gcp
  project_id: "my-project-123"
```

| Field | Default | Description |
|-------|---------|-------------|
| `backend` | `local` | `local`, `isopass`, `vault`, `aws`, `azure`, or `gcp` |
| `key_file` | (auto-generated) | AES-256-GCM encryption key for local backend |
| `secrets_dir` | `/srv/vigo/secrets` | Directory for encrypted secret files |
| `url` | (required for isopass/vault) | API base URL |
| `token_file` | (optional) | Bearer/Vault token file path (0600) |
| `mount_path` | `secret` | Vault KV v2 engine mount path |
| `region` | `us-east-1` | AWS region |
| `vault_name` | (required for azure) | Azure Key Vault name |
| `project_id` | (required for gcp) | GCP project ID |

## checkin

Agent polling behavior.

```yaml
checkin:
  interval: "5m"                     # agent poll frequency (stale = 2x this)
  jitter_percent: 20                 # randomize timing by 0-N%
  bundle_max_age: "24h"              # signed bundle TTL on agents (0 = forever)
```

| Field | Default | Description |
|-------|---------|-------------|
| `interval` | `5m` | How often agents check in. Stale threshold = 2x this value |
| `jitter_percent` | `20` | Random jitter to prevent thundering herd |
| `bundle_max_age` | `24h` | How long agents trust their cached policy bundle |

## bootstrap

Agent enrollment configuration. Requires `server.tls.ca_key_file`.

```yaml
bootstrap:
  cert_validity: "8760h"             # agent cert lifetime (default: 1 year)
  trusted_enrollment:                # token-free enrollment by pattern + source IP
    - pattern: "*"
      cidrs: ["192.168.0.0/16", "10.0.0.0/8"]
```

| Field | Default | Description |
|-------|---------|-------------|
| `cert_validity` | `8760h` | Lifetime of agent TLS certificates |
| `trusted_enrollment` | (empty) | Patterns + CIDRs for token-free enrollment |

## auth

Web UI and REST API authentication.

```yaml
# Basic auth (default)
auth:
  method: basic
  session_idle_timeout: "15m"

# OIDC (OpenID Connect SSO)
auth:
  method: oidc
  oidc:
    issuer: "https://accounts.google.com"
    client_id: "your-client-id"
    client_secret: "secret:vigo/auth/oidc_client_secret"
    redirect_url: "https://localhost:8443/auth/callback"
    scopes: [openid, profile, email]

# Disable auth (development only)
auth:
  method: none
```

| Field | Default | Description |
|-------|---------|-------------|
| `method` | `basic` | `basic`, `oidc`, `isowebauth`, or `none` |
| `session_idle_timeout` | `15m` | Idle timeout before session expires |

## smtp

Email notifications. Disabled when `host` is empty.


```yaml
smtp:
  host: "smtp.example.com"
  port: 587
  from: "vigo@example.com"
  username: "vigo"
  password: "secret:vigo/smtp/password"
  tls: true
  recipients: ["admin@example.com"]
  events:
    drift.detected: {}
    run.failure:
      recipients: ["ops@example.com"]
    convergence.threshold:
      threshold: 90
    security.rootkit: {}
  digest:
    interval: "1h"
    events: ["run.success", "drift.detected"]
  evidence:
    schedule: "weekly"                # "weekly" or "monthly"
    recipients: ["compliance@example.com"]
```

| Field | Default | Description |
|-------|---------|-------------|
| `host` | (disabled) | SMTP relay hostname |
| `port` | `587` | SMTP port |
| `from` | (required) | Sender email address |
| `tls` | `true` | Use STARTTLS |
| `recipients` | (required) | Default recipient list |
| `events` | (all enabled) | Per-event toggles and recipient overrides |
| `digest.interval` | (disabled) | Batch window for digest emails |
| `evidence.schedule` | (disabled) | `weekly` or `monthly` evidence delivery |

## integrations

External platform integrations. Each forwards events asynchronously. All API keys support `secret:` prefix. Empty `events` list = forward all events.

### Alerting

```yaml
integrations:
  slack:
    enabled: true
    webhook_url: "https://hooks.slack.com/services/T.../B.../xxx"
    events: ["drift.detected", "run.failure"]

  pagerduty:
    enabled: true
    routing_key: "secret:vigo/pagerduty/routing-key"
    events: ["security.rootkit", "run.failure"]

  opsgenie:
    enabled: true
    api_key: "secret:vigo/opsgenie/api-key"
    events: ["security.rootkit", "convergence.threshold"]

  teams:
    enabled: true
    webhook_url: "https://outlook.office.com/webhook/..."
    events: ["drift.detected", "run.failure"]
```

### PSA / RMM

```yaml
integrations:
  connectwise:
    enabled: true
    url: "https://api-na.myconnectwise.net/v4_6_release/apis/3.0"
    company_id: "mycompany"
    public_key: "publickey"
    private_key: "secret:vigo/connectwise/private-key"
    board_id: 1
    events: ["drift.detected", "run.failure", "envoy.stale"]

  autotask:
    enabled: true
    url: "https://webservices.autotask.net/ATServicesRest/v1.0"
    username: "apiuser"
    password: "secret:vigo/autotask/password"
    queue_id: 8
    events: ["drift.detected", "run.failure"]
```

### SIEM

```yaml
integrations:
  splunk:
    enabled: true
    url: "https://splunk.example.com:8088"
    token: "secret:vigo/splunk/hec-token"
    index: "vigo"
    source: "vigo"
    events: []

  elastic:
    enabled: true
    url: "https://elasticsearch.example.com:9200"
    index: "vigo-events"
    api_key: "secret:vigo/elastic/api-key"
    events: []

  datadog:
    enabled: true
    api_key: "secret:vigo/datadog/api-key"
    site: "datadoghq.com"
    events: []
```

### CMDB

```yaml
integrations:
  servicenow:
    enabled: true
    instance: "mycompany.service-now.com"
    username: "vigo-integration"
    password: "secret:vigo/servicenow/password"
    events: ["drift.detected", "run.failure"]
```

## grc

Push compliance evidence to GRC platforms on a schedule.

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
```

| Field | Default | Description |
|-------|---------|-------------|
| `name` | (required) | Integration name for logging |
| `endpoint` | (required) | API URL to POST evidence to |
| `api_key` | (required) | Bearer token. Supports `secret:` prefix |
| `push_interval` | `6h` | How often to push evidence |
| `frameworks` | (required) | Vigo standard key → GRC platform framework ID mapping |

## tungol

Hub + steorras control-plane sharding. See [Set up Tungol](../howto/set-up-tungol.md) for the walkthrough.

```yaml
# Hub
tungol:
  role: hub
  tungol_id: "alexander4-prod"        # federation id (required for hub/spoke)
  steorras:
    - id: dc1
      addr: "steorra1.example.com:1530"
      patterns: ["*.dc1.*"]
  auto_failover:
    enabled: true
    threshold: 3
    interval: "30s"

# Spoke role (this vigosrv is one steorra in the tungol)
tungol:
  role: spoke
  tungol_id: "alexander4-prod"        # must match the hub's tungol_id
  steorra_id: "dc1"
  hub_addr: "hub.example.com:1530"
  hub_fallback_addrs:
    - "hub-standby.example.com:1530"
```

| Field | Default | Description |
|-------|---------|-------------|
| `role` | `standalone` | `standalone`, `hub`, or `spoke` |
| `tungol_id` | (required when role ≠ standalone) | Federation identifier — operator-chosen, immutable, DNS-safe (1-64 ASCII alphanumeric + `-` `_` `.`, no leading dot). Must match across every hub + steorra in the tungol; embedded in every signed announcement (Phase 1) and the future CRDT admissions set. |
| `steorras` | (hub only) | List of steorra connections with ID, address, hostname patterns |
| `auto_failover.enabled` | `false` | Auto-drain unhealthy steorras |
| `steorra_id` | (spoke only) | Unique steorra identifier |
| `hub_addr` | (spoke only) | Hub address for status reporting |

### Steorra identity (Phase 1)

Each tungol-mode vigosrv generates a per-host Ed25519 keypair at `/srv/vigo/steorra/identity.wrapped` on first boot (HKDF purpose `vigo-steorra-wrap-v1`, mirrors the service-account puddle pattern). The reporter signs every `SteorraStatusReport` with this key; the hub verifies the signature against the asserted pubkey and pins `(tungol_id, steorra_id) → pubkey` on first sighting. Subsequent reports with a mismatched pubkey are rejected (`PermissionDenied`) and emit a `tungol_steorra_pubkey_mismatch` audit event.

Pins are in-memory only — a hub restart clears them and the next round of announcements re-pins. Pre-Phase-1 reporters (no signature/pubkey fields) are accepted without pinning to keep mixed-version rollout safe. Legitimate key rotation (regenerate `/srv/vigo/steorra/`) requires a hub restart to clear the old pin.

`vigocli tungol status` shows the local steorra's first-16-char pubkey for operator cross-check against the hub's audit event payload.

> The old `spanner:` block name is rejected at startup with a one-line error pointing here. Operators must rename `spanner:` → `tungol:`, `spokes:` → `steorras:`, and `spoke_id:` → `steorra_id:` in one pass.

## peers

Primary/peer server redundancy for HA.

```yaml
peers:
  role: primary
  servers:
    - addr: "peer1.example.com:1530"
    - addr: "peer2.example.com:1530"
```

| Field | Default | Description |
|-------|---------|-------------|
| `role` | `primary` | `primary` or `peer` |
| `servers` | (empty) | List of peer server addresses |

## license

License enforcement behavior.

```yaml
license:
  mode: "local"                      # "local" or "aggregate"
```

| Field | Default | Description |
|-------|---------|-------------|
| `mode` | `local` | `local`: count nodes on this server. `aggregate`: hub sums across all steorras |

## publish

Blast radius protection on config publish.

```yaml
publish:
  compliance_threshold: 80           # auto-rollback if convergence drops below this %
  rollback_window: "5m"              # monitoring window after config reload
```

## backup

Litestream continuous replication (SQLite only).

```yaml
backup:
  url: "s3://my-bucket/vigo"
  access_key_id: "secret:vigo/backup/aws_access_key"
  secret_access_key: "secret:vigo/backup/aws_secret_key"
  region: "us-east-1"
  retention: "720h"
  sync_interval: "1s"
```


## ai

AI assistant configuration. Claude or OpenAI recommended for best results. Ollama works for air-gapped environments but requires a dedicated GPU and produces lower-quality answers. See [AI Providers](../howto/troubleshoot.md) for details on each provider, hardware requirements, and data privacy considerations.

```yaml
ai:
  enabled: true
  provider: "claude"                 # claude | openai | ollama | openai-compat
  model: "claude-sonnet-4-20250514"
  max_tokens: 4096
  api_key: "secret:vigo/ai/api_key"
```

| Field | Default | Description |
|-------|---------|-------------|
| `provider` | `ollama` | AI backend: `claude`, `openai`, `ollama` (local, GPU required), `openai-compat` (vLLM, llama.cpp, etc.) |
| `model` | (provider default) | Model name |
| `max_tokens` | `4096` | Maximum response tokens |
| `context_mode` | `auto` | `tools`, `prefetch`, or `auto` |
| `api_key` | | API key for external providers (supports `secret:` prefix) |
| `base_url` | | Auto for ollama; required for openai-compat |

## branding

Custom web UI branding.

```yaml
branding:
  org_name: "ACME Corp"
  logo_path: "/srv/vigo/branding/logo.svg"
  favicon_path: "/srv/vigo/branding/icon.svg"
  theme: "light"   # "light" (default), "dark", or "auto"
```

| Field | Default | Description |
|-------|---------|-------------|
| `org_name` | (none) | Organization name shown below the sidebar logo |
| `logo_path` | embedded default | Sidebar logo (SVG or PNG) |
| `favicon_path` | embedded default | Browser tab icon |
| `theme` | `light` | Fleet-wide default color theme: `light` (Catppuccin Latte), `dark` (Catppuccin Mocha), or `auto` (follow the user's OS `prefers-color-scheme`). Individual operators can override via the top-navbar toggle; their choice persists per-browser in `localStorage`. |

## tuning

Advanced performance tuning. All fields optional.

```yaml
tuning:
  signature_window: "5m"             # signature verification time window
  last_seen_flush: "10s"             # batch DB writes for last_seen timestamps
  traits_prune_interval: "5m"        # clean up stale trait data
  run_store: "database"              # "database" or "memory"
  run_store_capacity: 20             # per-envoy run depth in memory mode
  max_concurrent_streams: 5000       # gRPC concurrent stream limit
  gogc: 100                          # Go GC target percentage
  memory_limit: ""                   # GOMEMLIMIT ceiling (e.g. "4GiB")
```

## paths

Directory paths for server subsystems.

```yaml
paths:
  migrations: "migrations"
  custom_traits: "custom-traits"
  docs: "docs"
  tasks: "stockpile/tasks"
  workflows: "stockpile/workflows"
  agent_dist: "dist/agent"
  license_dir: "/srv/vigo/license"
```

## Event Types Reference

All event types available for SMTP, webhook, and integration subscriptions:

| Event | Trigger |
|-------|---------|
| `envoy.enrolled` | New envoy enrolled |
| `run.success` | Successful convergence run |
| `run.failure` | Failed convergence run |
| `drift.detected` | Configuration drift corrected |
| `convergence.drift` | Drift affecting compliance controls |
| `convergence.threshold` | Fleet convergence drops below threshold |
| `compliance.evidence` | Scheduled evidence email delivery |
| `envoy.stale` | Envoy hasn't checked in within expected interval |
| `secret.rotated` | Secret value rotated |
| `config.reload.failure` | Config publish/reload failed |
| `security.rootkit` | Rootkit detected |
| `security.malware` | Malware detected |
| `security.integrity` | File integrity breach |
| `security.cve_critical` | New critical CVE found |
| `security.hardening_drop` | Hardening score dropped significantly |
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../legal/license.md).*
