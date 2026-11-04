
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
| `hostname` | (auto) | Server's enrolled envoy hostname, and the address agents are told to dial (`GET /bootstrap` advertises it). Set this when running in Docker — the container's own hostname is its container ID, not a name agents can reach. It is automatically added to the auto-generated server cert's SANs, so you do not also need to list it under `tls_sans`. |
| `tls.cert_file` | `tls/cert` | Path to server TLS certificate |
| `tls.key_file` | `tls/key` | Path to server TLS private key |
| `tls.ca_file` | `tls/ca` | Path to CA certificate for mTLS verification |
| `tls.ca_key_file` | (optional) | CA private key — enables bootstrap enrollment and CSR signing |
| `tls_sans` | (empty) | Additional Subject Alternative Names for auto-generated certs. `localhost`, the host's interface IPs, and `server.hostname` are already included automatically — list only names/IPs beyond those. |

## database

Storage backend. SQLite only — `postgres` is rejected at startup in this release.

```yaml
database:
  dsn: "secret:vigo/db/dsn"         # SQLite file path
  driver: sqlite3                    # sqlite3 (default) — only supported driver
  max_open_conns: 0                  # read-pool size (0 = auto: CPU count, min 4)
  retention: "30d"                   # auto-prune old runs/task runs/workflows
```

| Field | Default | Description |
|-------|---------|-------------|
| `dsn` | (required) | SQLite database file path. Supports `secret:` prefix |
| `driver` | `sqlite3` | Database driver. Only `sqlite3` is supported; the server rejects `postgres` at startup (the Postgres migration set is incomplete). |
| `max_open_conns` | `0` | Maximum concurrent database connections. SQLite runs two pools: writes always serialize on a dedicated single-writer connection, and this knob sizes the **concurrent read pool** — so it caps reader concurrency and bounds the CGO worker threads the driver pins per in-flight query. An unbounded pool is what lets a write storm exhaust the process's OS threads. `0` = auto (CPU count, min 4). |
| `retention` | `30d` | Auto-prune data older than this duration. Applies to `runs` (cascades `run_results`), `task_runs`, `workflow_runs`, `file_snapshots`, `convergence_history`, `compliance_history`. `audit_entries` is never trimmed (tamper chain). |

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

| Field | Default | Description |
|-------|---------|-------------|
| `backend` | `local` | `local` or `isopass` |
| `key_file` | (auto-generated) | AES-256-GCM encryption key for local backend |
| `secrets_dir` | `/srv/vigo/secrets` | Directory for encrypted secret files |
| `url` | (required for isopass) | Isopass API base URL |
| `token_file` | (required for isopass) | Bearer token file path (0600) |
| `tls_skip_verify` | `false` | Isopass: skip TLS verification (dev only) |

## checkin

Agent polling behavior.

```yaml
checkin:
  interval: "1s"                     # agent poll frequency (default 1s; sets the fleet cadence)
  jitter_percent: 20                 # randomize timing by 0-N%
  bundle_max_age: "24h"              # signed bundle TTL on agents (0 = forever)
  max_concurrent: 0                  # cap on concurrent CheckIn handlers (0 = CPU-scaled default; always on)
  report_max_concurrent: 0           # cap on concurrent ReportResult handlers (0 = built-in 2048; always on)
  max_connections: 0                 # cap on concurrently-held agent streams (0 = auto-derive safe ceiling from host RAM; -1 = uncapped)
  stream_signature: all              # held-stream per-message verification: all (default) | handshake-only (security relaxation)
```

| Field | Default | Description |
|-------|---------|-------------|
| `interval` | `1s` | How often agents check in. The server pushes this to agents, so it sets the effective fleet poll cadence. The staleness threshold is **not** a fixed multiple of this — it's `2.5 × max(interval, observed-cycle)`, floored at 30s, so a fast cadence still tolerates a few missed beats (see `offline_threshold` to override) |
| `jitter_percent` | `20` | Random jitter to prevent thundering herd |
| `bundle_max_age` | `24h` | How long agents trust their cached policy bundle |
| `max_concurrent` | `0` | Cap on concurrent CheckIn handlers. The cap gates the policy-bundle build (CPU-heavy) inside the handler, so a fleet-wide stale-checkin wave (post-publish, mass re-enroll) sheds via `ResourceExhausted` → agent backoff instead of CPU-cascading. **Always on** — `0` = a CPU-scaled built-in (`max(128, GOMAXPROCS*16)`; `GOMAXPROCS` tracks the container's cgroup CPU limit on Go 1.25+), not disabled. Higher admits more before shedding (more CPU thrash under a wave); lower sheds sooner. Watch `vigo_checkins_total{status="shed"}` |
| `report_max_concurrent` | `0` | Cap on concurrent ReportResult handlers in front of the single SQLite writer. Overflow returns `ResourceExhausted` so the agent backs off. **Always on** — `0` = the built-in `2048`, not disabled (unlike `max_concurrent`). Higher admits more before shedding (deeper queue, higher tail latency under sustained overload); lower sheds sooner (tighter tail). Watch `vigo_reportresults_total{status="shed"}` |
| `max_connections` | `0` | Cap on concurrently-held agent streams. A connected envoy costs **~623 KB of live server heap** with a realistic ~150 KB inventory (FleetIndex + inventory cache + the held stream; see the [sizing guide](../howto/operate/sizing.md)), so connection count — not request rate — is what drives the server toward its memory ceiling. Past this cap, new streams are refused with `ResourceExhausted` (the agent backs off and retries) so the server sheds gracefully instead of climbing toward the wall. `0` (default) = auto-derive a cap from host RAM; a positive value sets an explicit cap; `-1` disables it (legacy uncapped). **Caveat: the auto-derived cap is built from a connection-buffer estimate (~220 KB) and is *optimistic* against the real ~623 KB inventory-aware cost** — on a memory-bound box it can sit *above* the actual safe ceiling (e.g. it auto-derives ~59,746 on a 32 GiB host, but the measured safe ceiling there is ~30,000). Don't treat the cap alone as your limit — size from the [sizing guide](../howto/operate/sizing.md) and set an explicit value if needed. Watch `vigo_streams_active` against the effective value, and `vigo_checkins_total{status="stream_shed"}` for rejections |
| `stream_signature` | `all` | Per-message ed25519 verification on held agent streams. `all` (default) verifies every check-in/report, exactly as a unary request. `handshake-only` verifies the handshake plus any **proxied** (foreign-envoy) message, but skips re-verifying the stream owner's own subsequent check-ins — relying on the verified handshake + mTLS session integrity (the same handshake-binding model `Delta` already uses). Per-message verify continuously proves possession of the envoy's ed25519 key on a held stream. Independently, since **0.76.35 the server pins each envoy's mTLS client cert**: the verify path rejects any connection whose TLS peer-cert SubjectPublicKeyInfo ≠ the SPKI captured at enrollment (`cert_spki_sha256`), so a TLS-terminating proxy/MITM presenting a *different* CA-signed cert — even one whose CN/SAN matches the hostname — is refused before any check-in is accepted. That closes the relay/forgery path for the realistic threat in **all** modes, which is what makes `handshake-only` safe by construction here rather than a trust-the-operator relaxation. Unary requests are always verified regardless. Saves ~0.10 ms/checkin (~13% of gated per-checkin CPU; measured 0.76.29) — the largest remaining steady-state app cost once persist is gated (ADR-033 Option C). **The gRPC path must be direct or L4 pass-through mTLS — a TLS-terminating proxy is rejected by the cert pin** (its cert isn't the enrolled one): `vigo_sig_verify_failed_total{reason="cert_pin_mismatch"}` increments and a throttled WARN names the envoy. So the relaxation is safe by construction rather than relying on the operator to avoid proxies. Grandfathered envoys enrolled before 0.76.35 carry no pin and skip enforcement until they re-enroll (a once-per-envoy WARN names them). The only residual the pin can't catch is an attacker who stole the envoy's *cert private key* but not its ed25519 key — same host filesystem, so unrealistic; closing even that would need RFC 5705 channel-binding on the signature (tracked in pending). Prefer raising `interval` or sharding via spanner first. When set, the server logs a startup WARN, records a `security.stream_signature_relaxed` audit event, and exposes `vigo_checkin_signature_mode{mode="handshake-only"}`; a detected proxy increments `vigo_checkin_proxy_detected_total` and logs a WARN once per process |

> **`all` does not close the proxy gap for `Delta` — direct end-to-end mTLS is the content-integrity boundary in every mode.** The proxy heuristic above runs in **all** modes (not just `handshake-only`): any held stream whose TLS peer-cert CN/SAN ≠ the envoy hostname increments `vigo_checkin_proxy_detected_total` and logs a WARN once per process. This matters because `Delta` run-result events (the lightweight convergence-report path) carry **no per-message signature in any mode** — they ride the same handshake-binding model, so the ed25519 layer proves *authorship of the handshake*, not the content of each `Delta`. `all` re-verifies every *check-in*, but it does **not** sign `Delta`, so a TLS-terminating proxy on the gRPC path can forge an envoy's convergence/run-status reports even under the default. The integrity guarantee for `Delta` content is **direct agent↔server mTLS end-to-end**, and since 0.76.35 the cert pin **enforces** it: a terminating proxy (an L7 LB or service mesh on `:1530`) presents a non-enrolled cert and is rejected, so it can no longer establish the stream to forge `Delta` — closing this for pinned (post-0.76.35-enrolled) envoys. Grandfathered envoys stay on the warn-only model until they re-enroll. (Cryptographic per-`Delta` content integrity that survives a terminating proxy would be a separate signed-convergence-evidence feature, not a `stream_signature` setting.)

### Sizing `max_concurrent`

The cap defends against the cascading-overload regime: when offered load exceeds the server's serving rate, queued requests pile up faster than they drain, the client ticker re-fires on top, and throughput collapses (measured: 70 req/s on a 5K-capable host at 10K offered). Bounding concurrency caps throughput at `max_concurrent / latency` ≈ the host's actual capacity, gracefully degrading rather than collapsing.

Watch `vigo_grpc_checkin_in_flight` as the pre-cascade signal — a sustained climb under burst is the leading edge of trouble. Size the cap to roughly your host's measured req/s capacity × p99 latency in seconds (e.g. a 5K req/s host with 100 ms p99 → `max_concurrent: 500`).

Default `0` resolves to a CPU-scaled built-in (`max(128, GOMAXPROCS*16)`, where `GOMAXPROCS` tracks the container's cgroup CPU limit on Go 1.25+) — the cap is **always on**. Operators tune the knob; the burstwave test (2026-05-29) showed that an unbounded read path will CPU-cascade under a fleet-wide stale-checkin wave (post-publish, mass re-enroll), so the always-on default is the safety net.

For per-host envoy capacity (how many envoys one vigosrv host can comfortably hold at a given check-in interval, and when to federate via spanner instead of scaling up), see [How-to: Size a vigosrv host](../howto/operate/sizing.md).

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
    # Optional: provision exactly one identity as admin on its first login.
    # Leave both unset (default) and all new OIDC users are viewers — grant
    # admin explicitly with `vigocli webusers set-role --role admin`.
    # Email matching is honored only when the IdP marks the address verified
    # (`email_verified: true`). Prefer bootstrap_admin_subject on IdPs that
    # allow self-set/unverified email.
    bootstrap_admin_email: "admin@example.com"

# Disable auth (development only)
auth:
  method: none
```

| Field | Default | Description |
|-------|---------|-------------|
| `method` | `basic` | `basic`, `oidc`, `isowebauth`, or `none` |
| `session_idle_timeout` | `15m` | Idle timeout before session expires |
| `oidc.bootstrap_admin_email` | (none) | Email of the OIDC identity to provision as admin on first login. Matched only when the IdP marks the email verified (`email_verified: true`); otherwise ignored. Empty disables auto-admin. |
| `oidc.bootstrap_admin_subject` | (none) | OIDC `sub` claim of the identity to provision as admin (use when email is not stable). |

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

Every event carries one incident key per condition and envoy. `envoy.stale`, `envoy.offline` and `envoy.online` share a key, so an outage is one incident and `envoy.online` resolves it. `resource.blocked` is keyed by configcrate and resource across the fleet, and each `security.*` event type is one incident. PagerDuty uses the key as `dedup_key` and Opsgenie as `alias`. ServiceNow gets it as `correlation_id` and Zendesk as `external_id`. The ticket targets (Jira, Zendesk, Freshservice, Linear, GitHub Issues, ServiceNow, ConnectWise, Autotask, NinjaRMM, Kaseya) open at most one ticket per key every 24 hours. A resolve event clears the key, so a recurrence opens a new ticket. That window is held in memory and resets when vigosrv restarts.

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

  loki:
    enabled: true
    url: "https://logs-prod-us-central1.grafana.net"
    tenant_id: "123456"               # X-Scope-OrgID for multi-tenant Loki; omit for single-tenant
    username: "123456"                # basic-auth username (Grafana Cloud: instance ID)
    password: "secret:vigo/loki/password"
    # bearer_token: "secret:vigo/loki/token"  # alternative to basic-auth
    events: []
```

Loki receives events as labeled log streams. Labels stay low-cardinality (`service=vigo`, `severity`, `event_name`) per Loki best practice — high-cardinality fields (`hostname`, `envoy_id`) ride in the JSON log line body. Audit-trail rows arrive as `audit.<eventType>` events alongside `envoy.offline` / `run.failure` / `security.*` etc., so an operator can query the audit chain in LogQL: `{service="vigo", event_name=~"audit\\..*"}`.

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

Each pushed framework carries the same numbers as that framework's compliance report: `satisfied`, `waived`, `partial` and `not_satisfied` control counts (they sum to `total_controls`), `coverage_percent`, and the active envoy count. Every control is listed with a fleet status: `satisfied` when every active envoy covers it, `waived` when every envoy has it waived, `not_satisfied` when none covers, waives or partially meets it, and `partial` otherwise. The evidence text gives the per-envoy split. Unknown framework keys are skipped with a warning.

## spanner

Peer-equal control-plane federation (ADR-026). See [Set up Spanner](../howto/operate/set-up-spanner.md) for the walkthrough.

```yaml
# A spanner bolt — a peer-equal vigosrv that owns a hostname-pattern
# partition and shares the admissions roster with every other bolt via
# CRDT gossip. Every bolt's config has the same shape: one operator
# founds the spanner with `vigocli spanner init`, the rest join with
# `vigocli spanner join`.
spanner:
  mode: spanner
  spanner_id: "alexander4-prod"
  patterns: ["*.us-west.*"]
  peers: ["10.0.0.2", "10.0.0.3"]   # optional — config-driven hands-off peering
  snapshot:
    tier1_interval: 30s
    tier2_interval: 5m
  transport:
    multicast_group: "224.0.0.45"
    multicast_port: 1534
  fallback_admin: local
```

| Field | Default | Description |
|-------|---------|-------------|
| `mode` | `standalone` | `standalone` (single vigosrv) or `spanner` (a peer-equal bolt). The pre-Phase-3 `founder`/`joiner` values are retired — the loader hard-errors on them. |
| `spanner_id` | (required when mode = spanner) | Federation identifier — operator-chosen, immutable, DNS-safe (1-64 ASCII alphanumeric + `-` `_` `.`, no leading dot). Must match across every bolt in the spanner; embedded in every signed admission row. |
| `patterns` | (required when mode = spanner) | Hostname glob patterns this bolt owns. First-match-wins across the spanner; loader refuses overlap with another bolt's patterns at publish time + boot time. |
| `peers` | (empty) | Addresses (`host` or `host:port`, HTTPS REST port, default `8443`) of other bolts, enabling **config-driven hands-off peering** (ADR-026). When set, a joining bolt auto-admits with the founder on boot — no manual `spanner bolt invite`/`join` token ceremony. Asymmetric: on the founder, `peers` is the who-may-join allowlist (a joiner's source address must resolve to an entry); on a joiner, point it at the founder. The shared fleet CA is still established out-of-band (`vigocli spanner ca export`/`import`), never over this channel. Empty = gossip/ceremony discovery only. |
| `transport.multicast_group` | `224.0.0.45` | IPv4 multicast group for Tier-1 counter gossip. |
| `transport.multicast_port` | `1534` | UDP port for Tier-1 counter gossip. |
| `snapshot.tier1_interval` | `30s` | Tier-1 counter-gossip cadence. |
| `snapshot.tier2_interval` | `5m` | Tier-2 roster-snapshot cadence. |
| `fallback_admin` | `local` | Fleet-wide admin-read source: `local` aggregates `ListEnvoys` / `GetConvergenceSummary` from the gossip-backed observability cache (every bolt's signed `/fleet-snapshot`); `fanout` queries every roster bolt's admin gRPC live and merges. Both produce identically-shaped responses. `ListRuns` is unaffected by this switch — runs are never gossiped, so a per-envoy query always routes to the owning bolt and a global runs list always fans out to every bolt. |

There are two admission paths (both founder-routed). With `peers` set, a joining bolt auto-admits with the founder on boot (config-driven peering, recommended). Without it, peer discovery is gossip-driven — there is no `bootstrap_peers` seed list — and a joining bolt learns the roster through the `vigocli spanner bolt join` admission ceremony. Either way, the CRDT-replicated roster is the peer list from then on. Bolts are identified by their Ed25519 pubkey (`server/bolt/identity.wrapped`), not a YAML-configured ID.

### Bolt identity

Each spanner-mode vigosrv generates a per-host Ed25519 keypair at `/srv/vigo/bolt/identity.wrapped` on first boot (HKDF purpose `vigo-bolt-wrap-v1`, mirrors the service-account puddle pattern). The bolt signs its admission row and every gossiped roster snapshot with this key; peers verify signatures end-to-end and pin each bolt's pubkey at admission time. `vigocli spanner status` shows the local bolt's first-16-char pubkey for operator cross-check.

> The retired hub-spoke keys (`role`, `bolt_id`, `hub_addr`, `hub_fallback_addrs`, `bolts`, `auto_failover`, `transport.bootstrap_peers`) and the retired `mode` values `founder`/`joiner` are rejected at startup with a one-line migration pointer.

## peers

Primary/peer server redundancy for HA.

The primary replicates `stacks/`, `.live/`, `secrets/`, `tls/` and `server.yaml` under the server root to every peer: a full sync on connect, then file changes as they happen. A peer writes nothing outside that set — `peer-role`, `config-history/` and the database stay local. A file larger than 7 MiB is not replicated (the primary logs an error). Known limits: a file the primary deletes while a peer is disconnected stays on that peer, and a zero-byte file is replicated as a deletion.

```yaml
peers:
  role: primary
  servers:
    - addr: "peer1.example.com:1530"
      pubkey: "ed25519:<peer1-pubkey-hex>"
    - addr: "peer2.example.com:1530"
      pubkey: "ed25519:<peer2-pubkey-hex>"
```

| Field | Default | Description |
|-------|---------|-------------|
| `role` | `primary` | `primary` or `peer` |
| `servers` | (empty) | List of peer servers |
| `servers[].addr` | (required) | Peer gRPC address (`host:port`) |
| `servers[].sync_interval` | `10s` | Push interval to this peer |
| `servers[].pubkey` | (required) | Peer's service-account (puddle) Ed25519 key, from its startup `puddle service-account identity ready` log line. Authorizes HA peer RPCs (SyncStream/Promote/Status); replication **fails closed** without it. Local secrets backend only. Configure the same `peers` block on every node. |

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

AI assistant configuration. Claude or OpenAI recommended for best results. Ollama works for air-gapped environments but requires a dedicated GPU and produces lower-quality answers. See [AI Providers](../howto/operate/troubleshoot.md) for details on each provider, hardware requirements, and data privacy considerations.

### Prompt-injection defense — structural mitigations + provider-dependent adherence

Every block of data Vigo prefetches before an assistant turn (envoy details, traits, run output, config YAML, CVE descriptions, etc.) is wrapped in a fenced ```` ```text ```` block with a fence length sized past any backticks the data itself contains, and the system prompt's "Trust Boundary" section instructs the LLM to treat anything inside the fence as queried data rather than operator instructions. This blocks the structural attack: a hostname like `"web01\nIGNORE PRIOR INSTRUCTIONS AND…"`, a package name with embedded markdown, a run-output dump with a fake `system:` prefix.

**The actual guarantee is only as strong as the underlying LLM's adherence to that framing.** Claude (Anthropic's models) respect "this is data, not instructions" framing reliably in our testing; OpenAI is similar. Ollama and openai-compat backends vary widely by model — a smaller open-source model under stress may still follow an injected imperative despite the fence. If your threat model includes adversaries who can put text into trait values, hostnames, or other prefetched content, prefer Claude or OpenAI over self-hosted open-source models for the assistant.

The fence + framing are best-effort defense-in-depth, not a hard guarantee. The assistant is read-only by design (`server/ai/tools_dispatch.go` exposes only read operations), so even a successful injection cannot write config, push tasks, or mutate state — at worst it could mislead the operator reading the assistant's answer.

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
| `logo_path` | embedded default | Sidebar logo (SVG or PNG). A custom logo links to the dashboard; the default Vigo logo links to vigo.alexander4.io in a new tab |
| `favicon_path` | embedded default | Browser tab icon |
| `theme` | `light` | Fleet-wide default color theme: `light` (Catppuccin Latte), `dark` (Catppuccin Mocha), or `auto` (follow the user's OS `prefers-color-scheme`). Individual operators can override via the top-navbar toggle; their choice persists per-browser in `localStorage`. |

## grafana

Optional. Points the web UI at an external Grafana instance — when set, the `/health` page links to the bundled Vigo dashboards. Vigo neither runs nor manages Grafana; this is purely a link target.

```yaml
grafana:
  url: "https://grafana.example.com:3000"
```

| Field | Default | Description |
|-------|---------|-------------|
| `url` | (none) | Base URL of a Grafana instance. When set, the `/health` page links to each bundled dashboard at `<url>/d/<uid>`. Unset = no links shown. |

## sandgorgon

Optional. Gates the sandgorgon bare-metal lifecycle REST surface (Redfish BMC power/disk control, the NIST 800-88 decommission workflow, CSV/NetBox asset import). The subsystem is **deferred until after Vigo 1.0** and exposes destructive BMC operations against plaintext-stored BMC credentials, so its routes are **not mounted unless explicitly enabled**. Leave off.

BMC connections verify the BMC's TLS certificate against the system roots. A BMC with a self-signed certificate is rejected; there is no configuration key to skip verification yet.

```yaml
sandgorgon:
  enabled: false
```

| Field | Default | Description |
|-------|---------|-------------|
| `enabled` | `false` | Mount the sandgorgon REST routes. Deferred post-v1; off by default. |

## i18n

Optional web UI localization. Off by default: with i18n disabled the UI renders in English with zero overhead and no language picker. When enabled, each request's locale is resolved **cookie → `Accept-Language` → `default_locale`**: the operator's saved choice (a cookie set from the top-navbar language picker, like the theme toggle) wins, then the browser's `Accept-Language` header (matched on the primary subtag, so `fr-CA` matches `fr`), then the configured default. Translations are key-based with English fallback — a missing string degrades to English rather than a blank.

Operator-authored data (envoy names, run results, audit entries) and all commands, flags, and config keys stay English regardless of locale; localization covers UI chrome (nav, labels, buttons) and the in-app docs.

```yaml
i18n:
  enabled: false
  default_locale: "en"
  supported_locales: ["en", "fr", "de", "es", "pl", "pt", "it"]
```

| Field | Default | Description |
|-------|---------|-------------|
| `enabled` | `false` | Enable non-English locales and the top-navbar language picker. When false the UI is English-only with no per-request locale work. |
| `default_locale` | `en` | Locale used when no cookie or `Accept-Language` match applies, and the fallback for any untranslated string. |
| `supported_locales` | all bundled | Locales offered in the picker and matched against `Accept-Language`. Empty = every locale with a bundled catalog. Always include `default_locale`. |

## swarm

Peer-to-peer content distribution (six content subsystems: filecast, gitback, longdrawer, lockbox, curator, poolq). Each subsystem documents the full field set in its howto — the section below covers **poolq** (ADR-029); see [`server.yaml.example`](https://github.com/alexander4/vigo/blob/main/server/config/server.yaml.example) for the full template plus the other subsystems.

```yaml
swarm:
  enabled: ["*"]                       # substrate-level gate (pattern list)
  poolq:
    enabled: ["*"]                     # which envoys may run poolq (founders + log-holders)
    retention: "7d"                    # per-topic age window; older messages prune
    max_msg_bytes: 16384               # per-message body cap (default 16 KiB; hard ceiling 64 KiB)
```

### `swarm.poolq`

| Field | Default | Description |
|-------|---------|-------------|
| `enabled` | `[]` | Hostname pattern list (first-match-wins; `-` prefix denies; empty list disables) of envoys allowed to run poolq — publishers (founders) and log-holders. |
| `retention` | `"7d"` | Per-topic message-age retention window. Messages older than this are pruned by the mesh aggregator, and stale-on-ingest messages are refused — together that's flap-stable pruning without tombstones. Accepts `Nd` / standard Go duration strings. Empty / unparseable falls back to `poolqmesh.DefaultRetention` (7d). |
| `max_msg_bytes` | `16384` | Per-message body size cap on the publish path, in bytes. `0` (or unset) = the 16 KiB compile-time default. Hard ceiling 65536 (64 KiB) — bigger payloads belong in a curator artifact that the message references by id. |

Publishing additionally requires `poolq: true` on the user's usercrate AND an unlocked puddle (a heavier grant than `gitback:`). Reading is ungated — messages are fleet-readable.

The admin moderation backstop is `vigocli swarm poolq block <topic_id>`: the server stops serving the topic's `/range` and `/log` endpoints fleet-wide. See the [poolq howto](../howto/swarm/set-up-poolq.md) for the publish + consume flow.

## tuning

Advanced performance tuning. All fields optional.

```yaml
tuning:
  signature_window: "5m"             # signature verification time window
  last_seen_flush: "10s"             # batch cadence for last_seen + trait insert/prune
  run_store: "database"              # "database" or "memory"
  run_store_capacity: 20             # per-envoy run depth in memory mode
  max_concurrent_streams: 5000       # gRPC concurrent stream limit
  grpc_read_buffer: 32768            # per-conn read buffer bytes; 0 = gRPC default (32 KiB)
  grpc_write_buffer: 32768           # per-conn write buffer bytes; 0 = gRPC default (32 KiB)
  keepalive_time: "30s"              # how often the server pings each conn to check liveness
  keepalive_timeout: "30s"           # deadline for the ping ACK before the conn is closed
  gogc: 100                          # Go GC target percentage
  # GOMEMLIMIT has no key here — auto-derived to ~80% of the cgroup-aware RAM,
  # or set the GOMEMLIMIT env var to override (GOMEMLIMIT=off for no limit).
```

### gRPC connection buffers — `grpc_read_buffer` + `grpc_write_buffer`

The held stream connection itself costs ~200 KB of the ~623 KB total per-envoy heap (the rest is the FleetIndex entry + cached inventory — see the [sizing guide](../howto/operate/sizing.md)), of which gRPC's default per-connection read+write staging buffers are ~64 KiB (32 KiB each). On large fleets that buffer footprint is the biggest *operator-tunable* slice of per-connection RAM: halving both to `16384` cuts ~32 KiB/conn (~48 MB across 1,500 connections). Both default to `0` → gRPC's 32 KiB; the option is only applied when set `> 0`. The trade-off is memory vs. syscall efficiency — smaller buffers mean more `read`/`write` syscalls per byte moved — so lower them only when connection-count RAM is the binding constraint, and re-check that large `config publish` bundle bursts still deliver promptly (full policy bundles ride the held stream as the only high-throughput payload).

There is intentionally **no** `initial_window` / HTTP/2 flow-control-window knob: gRPC floors the window at 64 KiB (a smaller value is ignored), so it cannot shrink per-connection RAM, and pinning it would only disable gRPC's BDP auto-tuning and throttle bundle delivery. The read/write buffers above are the connection-RAM dial.

### gRPC keepalive — `keepalive_time` + `keepalive_timeout`

The server sends an HTTP/2 PING to every conn every `keepalive_time` (default `30s`). If the PING isn't ACKed within `keepalive_timeout` (default `30s`), gRPC closes the conn with GOAWAY and any in-flight RPC on it returns `Unavailable` to the agent. gRPC also pushes `keepalive_timeout` to the kernel's `TCP_USER_TIMEOUT` for the conn, so kernel-level read timeouts use the same deadline.

The trade-off is dead-client detection latency vs. tolerance for transient Go scheduler queueing delays. At high concurrent connection counts the runqueue depth grows (per `docs/howto/sizing.md`'s measurements, ~16k runnable goroutines at 20k conns), and the deepest tail of waiting goroutines can take ~20s to dispatch — tight `keepalive_timeout` values kill conns under that transient pressure. The `30s` default gives ~50% margin over the observed worst-case dispatch latency at the comfortable conn-count envelope. Operators running large fleets that still see `Unavailable` errors growing should raise to `60s` before chasing other tunables; operators who need faster dead-conn reaping (small fleets where every connection matters) can drop to `20s` — gRPC's own default — without re-introducing the scheduler-queueing failure mode at sub-15k conn counts.

The pre-0.69.20 default was `10s`, which scaling work in 2026-05-29 showed was the actual mechanism behind the matrix's `Unavailable` errors at 20k+ conns regardless of check-in interval. See [How-to: Size a vigosrv host](../howto/operate/sizing.md) for the supporting evidence.

## paths

Directory paths for server subsystems.

```yaml
paths:
  custom_traits: "custom-traits"
  docs: "docs"
  tasks: "stacks/tasks"
  workflows: "stacks/workflows"
  agent_dist: "dist/agent"
```

## watcher

Polls the secrets provider on a schedule and force-pushes affected envoys when a value changes — automatic secret rotation without waiting for the next check-in. See [Secrets](../concepts/secrets.md).

```yaml
watcher:
  enabled: true
  poll_interval: "5m"
```

## rate_limit

Per-envoy and global ceilings on check-in RPCs. Requests over the limit get a retryable error so agents back off on their own. Note that *connection count*, not request rate, drives the memory ceiling — see [Size a vigosrv host](../howto/operate/sizing.md); this section only caps offered request rate.

```yaml
rate_limit:
  enabled: true
  checkin_per_envoy: 6      # max check-ins per envoy per window
  checkin_global: 500       # max check-ins fleet-wide per window
```

## maintenance

Global change freeze — `vigocli config publish` is blocked until `freeze_until` passes. For code freezes, customer maintenance windows, or incident response. Bypass requires editing the file and reloading, so the freeze is real.

```yaml
maintenance:
  freeze_until: "2026-07-01T00:00:00Z"   # RFC3339; unset = no freeze
```

## task

Ad-hoc task-dispatch safety. With `require_definition: true`, every task sent via `vigocli task dispatch` must reference a reviewed definition under `stacks/tasks/` — direct shell commands are refused. Turn this on in production to force review of anything fan-outable to the fleet.

```yaml
task:
  require_definition: true
```

## export

One-way outbound compliance feeds — SIEM audit-event ingestion, CMDB inventory sync, and OSCAL-format framework reports — each toggled independently. See [Compliance reporting](../howto/compliance/compliance-reporting.md).

```yaml
export:
  siem: true
  cmdb: false
  oscal: true
```

## compliance

Scopes which compliance frameworks this deployment reports against. An empty `standards` list activates every framework Vigo knows; an explicit list restricts dashboards, reports, and waiver tooling to that subset. See the [Compliance matrix](compliance-matrix.md) for the framework catalog.

```yaml
compliance:
  standards: ["hipaa", "soc2"]   # empty/unset = every framework
```

## risk

Per-envoy risk scoring. An optional NVD CVE API key enriches scoring with CVSS metrics and vendor-attributed advisories; without it, CVE severity comes from scanner output alone. Risk scores drive the dashboard Risk Posture column and the cyber-insurance attestation PDF.

```yaml
risk:
  nvd:
    api_key: "secret:vigo/risk/nvd_api_key"   # optional CVSS enrichment
```

## stream_edit

Guardrails on the file resource's `stream_edit:` attribute, which pipes content through agent-local scripts before it's written. Controls whether stream-edits are allowed, the permitted script paths, and the per-transform timeout. See [the configcrate language](configcrate-language.md) for the attribute itself.

```yaml
stream_edit:
  enabled: true
  allowed_paths: ["/srv/vigo/scripts"]
  default_timeout: "10s"
```

## scrier

Browser-based remote access (SSH, RDP, VNC) to any enrolled envoy, routed out through the envoy's outbound agent stream so no inbound ports are required on the target. Every session is logged in `scrier_sessions` and audited on disconnect. See [Set up Scrier](../howto/operate/set-up-scrier.md) and [Scrier](../concepts/scrier.md).

```yaml
scrier:
  enabled: true
  guacd_addr: "127.0.0.1:4822"
  allowed_ports: [22, 3389, 5900]
  max_sessions: 10
  recording_enabled: false
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
