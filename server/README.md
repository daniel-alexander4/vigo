# server -- hobd

The `hobd` server daemon is the central control plane for Hobnob. It serves gRPC (port 1530) for agent communication and REST/UI (port 8181) for administration.

## Summary

- **Binary name:** `hobd`
- **gRPC over mTLS (port 1530)** -- agent check-ins, result reporting, traits collection
- **REST API + web UI (port 8181)** -- admin CLI (`hobnobcli`), browser dashboard
- **SQLite** backend (WAL mode) with golang-migrate migrations
- **Config hot-reload** via fsnotify on modules/, roles/, nodes/, and templates/ directories
- **Prometheus metrics** at `GET /metrics`
- **Secrets** resolved through isopass (prod) or static provider (dev/test)

## Quickstart

### Option A -- Docker Compose (recommended)

```bash
# Start server (uses embedded SQLite)
docker compose up -d

# Bootstrap a node (trusted enrollment configured in server.yaml)
curl -sSf http://hobd.example.com:8181/bootstrap | sudo sh
```

### Option B -- Direct

```bash
# 1. Build all binaries
make build

# 2. Generate TLS certificates
make certs

# 3. Configure (edit server-configs/server.yaml)
#    Set database DSN, TLS cert paths, secrets backend

# 4. Start server (runs migrations automatically)
./bin/hobd
```

### Option C -- Development (static secrets)

```bash
# Create .env with database DSN
echo 'HOBNOB_DB_DSN=hobnob.db' > .env

# Start with static secrets backend (set backend: "static" in server.yaml)
./bin/hobd
```

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `HOBNOB_CONFIG_FILE` | `server.yaml` | Server config path |
| `HOBNOB_NODE_CONFIGS_DIR` | `.` | Node configs root directory |
| `HOBNOB_MIGRATIONS_DIR` | `migrations` | Migration files path |
| `HOBNOB_CUSTOM_TRAITS_DIR` | `custom-traits` | Custom trait executables |
| `HOBNOB_AGENT_DIR` | `dist/agent` | Agent binary distribution directory |
| `HOBNOB_VERSION` | `dev` | Version string |
| `HOBNOB_DANGEROUS` | (unset) | Set to `1` to allow non-idempotent resources |

## Server Config (server.yaml)

```yaml
server:
  grpc_listen: "0.0.0.0:1530"
  api_listen: "0.0.0.0:8181"
  tls:
    cert_file: "secret:hobnob/tls/cert"
    key_file: "secret:hobnob/tls/key"
    ca_file: "secret:hobnob/tls/ca"
    ca_key_file: "secret:hobnob/tls/ca_key"  # optional, enables bootstrap CSR signing

database:
  dsn: "secret:hobnob/db/dsn"

checkin:
  base_interval: "5m"
  jitter_percent: 20

secrets:
  backend: "isopass"     # or "static"
  url: "http://localhost:8080"

watcher:
  enabled: true
  poll_interval: "5m"

bootstrap:
  cert_validity: "8760h"   # 1 year
  trusted_enrollment:
    - pattern: "*.web.example.com"
      cidrs: ["10.0.0.0/8"]
```

## SMTP Notifications

Email notifications are sent via an SMTP relay when configured. Add an `smtp` section to `server.yaml`:

```yaml
smtp:
  host: smtp.example.com
  port: 587                              # default 587
  from: hobnob@example.com
  username: hobnob
  password: "secret:hobnob/smtp/password"
  tls: true                              # STARTTLS (default true)
  recipients:
    - admin@example.com
    - ops-team@example.com
  events:                                # omit to receive all events
    - thrall.enrolled
    - run.failure
    - drift.detected
    - secret.rotated
```

Disabled when `host` is empty or omitted. Notifications are non-blocking -- delivery failures are logged but never affect server operation.

**Available events:** `thrall.enrolled`, `run.success`, `run.failure`, `drift.detected`, `secret.rotated`

## Architecture

### Subsystems

| Package | Purpose |
|---------|---------|
| `server/grpc/` | gRPC service implementations + ED25519 signature verification interceptor. Agent RPCs: Register, CheckIn, ReportResult, ReportTraits. Admin RPCs: ForcePush, DryRun, Rollback. TLS 1.3 minimum, mTLS required. |
| `server/api/` | REST gateway + web UI (HTMX SPA) + bootstrap handler for agent enrollment |
| `server/config/` | YAML config loader with secret resolution, fsnotify hot-reload, Go template rendering. See [config/README.md](config/README.md). |
| `server/db/` | SQLite query layer, NodeCache (60s TTL), LastSeenBatcher (10s flush) |
| `server/registry/` | Node enrollment: hostname pattern matching, bcrypt token validation, revocation |
| `server/secrets/` | Provider interface: `IsopassProvider` (prod) + `StaticProvider` (dev/test) |
| `server/compliance/` | Status computation: compliant / drifted / failing / stale / unknown |
| `server/webhook/` | HMAC-signed webhook dispatch (max 20 concurrent, 3 retries, 10s HTTP timeout) |
| `server/smtp/` | SMTP email notifications with Go templates (enrollment, run failure, drift, secret rotation) |
| `server/watcher/` | Secret rotation polling -- force-pushes affected nodes on secret version change |
| `server/metrics/` | Prometheus metrics collector (30s refresh interval) |
| `server/traits/` | Custom trait executable scanner with TTL cache (30s) |
| `server/ca/` | Certificate authority for bootstrap CSR signing |
| `server/bootstrap/` | Agent bootstrap handler (curl-pipe install flow) |
| `server/conditionals/` | Conditional variable parsing and evaluation. See [conditionals/README.md](conditionals/README.md). |
| `server/validate/` | Module idempotency validation |

### Startup Sequence

1. Load secrets config from `server.yaml` (first pass -- no secret resolution)
2. Initialize secrets provider (isopass or static)
3. Load full server config with secret resolution (second pass)
4. Open SQLite database, run migrations
5. Initialize ConfigStore -- load modules, roles, nodes; start fsnotify watcher
6. Start LastSeenBatcher (flushes every 10s)
7. Start secret rotation watcher (if enabled)
8. Start gRPC server with mTLS on configured port
9. Start metrics collector (30s interval)
10. Load bootstrap CA (if `ca_key_file` configured)
11. Start REST gateway + web UI on configured port
12. Wait for SIGINT/SIGTERM, graceful shutdown

### Key Constants

| Setting | Value | Notes |
|---------|-------|-------|
| TLS minimum version | 1.3 | Enforced on gRPC |
| Signature timestamp window | 5 minutes | Replay prevention |
| NodeCache TTL | 60 seconds | Caches node record + parsed pubkey |
| LastSeen flush interval | 10 seconds | Batched DB writes |
| Metrics refresh interval | 30 seconds | Prometheus gauges |
| Traits scanner TTL | 30 seconds | Custom trait file cache |
| Webhook max concurrent | 20 | Semaphore-gated |
| Webhook max retries | 3 | Exponential backoff |
| Webhook HTTP timeout | 10 seconds | Per delivery |
| Traits snapshot retention | 10 | Per node, pruned on report |
| Bcrypt cost | 10 | Token hashing |
| Compliance stale threshold | 2x base_interval | Node hasn't checked in |
| Config reload debounce | 500ms | Prevents rapid re-parsing |

## Examples

### Bootstrap a node (trusted enrollment)

```bash
# On the managed node (server has trusted_enrollment configured)
curl -sSf http://hobd.example.com:8181/bootstrap | sudo sh
```

### Bootstrap with a token (when trusted enrollment is not configured)

```bash
# On the server
./bin/hobnobcli tokens generate
# Output: Token: abc123...
./bin/hobnobcli approved add "web-01.example.com" --token abc123...

# On the managed node
sudo hobnob bootstrap --server http://hobd.example.com:8181 --token abc123...
```

### Force a node to re-converge immediately

```bash
./bin/hobnobcli push web-01.example.com
```

### Preview what would change on a node (dry run)

```bash
./bin/hobnobcli preview web-01.example.com
```

### Check compliance status

```bash
./bin/hobnobcli nodes
```
