# server -- vigosrv

The `vigosrv` server daemon is the central control plane for Vigo. It serves gRPC (port 1530) for agent communication and REST/UI (port 8443) for administration.

## Summary

- **Binary name:** `vigosrv`
- **gRPC over mTLS (port 1530)** -- agent check-ins, result reporting, traits collection
- **REST API + web UI (port 8443)** -- admin CLI (`vigocli`), browser dashboard
- **SQLite** backend (WAL mode) with golang-migrate migrations
- **Config reload** triggered by `config publish` via REST endpoint
- **Prometheus metrics** at `GET /metrics`
- **Secrets** resolved through pluggable backend (local or isopass)

## Quickstart

### Option A -- Docker Compose (recommended)

```bash
# Start server (uses embedded SQLite)
docker compose up -d

# First post-install step: enroll the server's own host as an envoy.
# Vigo assumes every server runs a co-hosted agent — this is how the
# server participates in swarm / puddle / lockbox alongside its
# managed nodes. Run from the server host itself; loopback is covered
# by the default trusted_enrollment CIDR list (no token required).
curl -sSfk https://localhost:8443/bootstrap | sudo sh

# Then bootstrap any other managed nodes:
curl -sSfk https://vigosrv.example.com:8443/bootstrap | sudo sh
```

### Option B -- Direct

```bash
# 1. Build all binaries
make build

# 2. Generate TLS certificates
make certs

# 3. Configure (edit server-seed/server.yaml)
#    Set database DSN, TLS cert paths, secrets backend

# 4. Start server (runs migrations automatically)
./bin/vigosrv
```

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `VIGO_CONFIG_FILE` | `server.yaml` | Server config path |
| `VIGO_CUSTOM_TRAITS_DIR` | `custom-traits` | Custom trait executables |
| `VIGO_AGENT_DIR` | `dist/agent` | Agent binary distribution directory |
| `VIGO_VERSION` | `dev` | Version string |

## Server Config (server.yaml)

```yaml
server:
  grpc_listen: "0.0.0.0:1530"
  api_listen: "0.0.0.0:8443"
  tls:
    cert_file: "tls/cert"
    key_file: "tls/key"
    ca_file: "tls/ca"
    ca_key_file: "tls/ca_key"  # optional, enables bootstrap CSR signing

database:
  dsn: "secret:vigo/db/dsn"

checkin:
  interval: "5m"
  jitter_percent: 20

secrets:
  backend: "isopass"     # or "local" (default)
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
  from: vigo@example.com
  username: vigo
  password: "secret:vigo/smtp/password"
  tls: true                              # STARTTLS (default true)
  recipients:
    - admin@example.com
    - ops-team@example.com
  events:                                # omit to receive all events
    - envoy.enrolled
    - run.failure
    - drift.detected
    - secret.rotated
```

Disabled when `host` is empty or omitted. Notifications are non-blocking -- delivery failures are logged but never affect server operation.

**Available events:** `envoy.enrolled`, `run.success`, `run.failure`, `drift.detected`, `secret.rotated`

## Architecture

### Subsystems

| Package | Purpose |
|---------|---------|
| `server/grpc/` | gRPC service implementations + ED25519 signature verification interceptor. Agent RPCs: Register, CheckIn, ReportResult, ReportTraits. Admin RPCs: ForcePush, DryRun, Rollback. TLS 1.3 minimum, mTLS required. |
| `server/api/` | REST gateway + web UI (HTMX SPA) + bootstrap handler for agent enrollment |
| `server/config/` | YAML config loader with secret resolution, publish-triggered reload, Go template rendering. See [config/README.md](config/README.md). |
| `server/db/` | SQLite query layer, EnvoyCache (60s TTL), LastSeenBatcher (10s flush) |
| `server/registry/` | Envoy enrollment: hostname pattern matching, bcrypt token validation, revocation |
| `server/secrets/` | Provider interface: `LocalProvider` (default, file-based) + `IsopassProvider` (prod) |
| `server/convergence/` | Status computation: converged / relapsed / diverged / failed / offline / no data |
| `server/webhook/` | HMAC-signed webhook dispatch (max 20 concurrent, 3 retries, 10s HTTP timeout) |
| `server/smtp/` | SMTP email notifications with Go templates (enrollment, run failure, drift, secret rotation) |
| `server/watcher/` | Secret rotation polling -- force-pushes affected nodes on secret version change |
| `server/metrics/` | Prometheus metrics collector (30s refresh interval) |
| `server/traits/` | Custom trait executable scanner with TTL cache (30s) |
| `server/ca/` | Certificate authority for bootstrap CSR signing |
| `server/bootstrap/` | Agent bootstrap handler (curl-pipe install flow) |
| `server/conditionals/` | Conditional variable parsing and evaluation. See [conditionals/README.md](conditionals/README.md). |
| `server/validate/` | Module idempotency validation |
| `server/modlint/` | Module linter: YAML repair, normalization, schema validation, `#~` auto-commenting. Standalone package (no server deps) for use in vigosrv, CLI, and community website. |

### Startup Sequence

1. Load secrets config from `server.yaml` (first pass -- no secret resolution)
2. Initialize secrets provider (local or isopass)
3. Load full server config with secret resolution (second pass)
4. Open SQLite database, run migrations
5. Initialize ConfigStore -- load modules, roles, nodes
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
| EnvoyCache TTL | 60 seconds | Caches envoy record + parsed pubkey |
| LastSeen flush interval | 10 seconds | Batched DB writes |
| Metrics refresh interval | 30 seconds | Prometheus gauges |
| Traits scanner TTL | 30 seconds | Custom trait file cache |
| Webhook max concurrent | 20 | Semaphore-gated |
| Webhook max retries | 3 | Exponential backoff |
| Webhook HTTP timeout | 10 seconds | Per delivery |
| Traits snapshot retention | 10 | Per envoy, pruned on report |
| Bcrypt cost | 10 | Token hashing |
| Compliance stale threshold | 2x interval | Envoy hasn't checked in |
| Config reload debounce | 500ms | Prevents rapid re-parsing |

## Examples

### Bootstrap a envoy (trusted enrollment)

```bash
# On the managed envoy (server has trusted_enrollment configured)
curl -sSfk https://vigosrv.example.com:8443/bootstrap | sudo sh
```

### Bootstrap with a token (when trusted enrollment is not configured)

```bash
# On the server
./bin/vigocli tokens generate
# Output: Token: abc123...
./bin/vigocli approved add "web-01.example.com" --token abc123...

# On the managed envoy
sudo vigo bootstrap --server https://vigosrv.example.com:8443 --token abc123...
```

### Force a envoy to re-converge immediately

```bash
./bin/vigocli envoys push web-01.example.com
```

### Preview what would change on a envoy (dry run)

```bash
./bin/vigocli preview web-01.example.com
```

### Check compliance status

```bash
./bin/vigocli nodes
```
