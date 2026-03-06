# Phase 1: Foundation - Research

**Researched:** 2026-03-05
**Domain:** Go gRPC server, mTLS, PostgreSQL, YAML config loading, secrets abstraction, agent enrollment
**Confidence:** HIGH

## Summary

Phase 1 establishes a Go gRPC server accepting mTLS connections, an agent enrollment flow using approved hostname/IP patterns with one-time tokens, per-client YAML config loading with Go template rendering and secret resolution, and PostgreSQL persistence via golang-migrate + sqlc. The agent stub generates an ED25519 keypair and registers with the server. Infrastructure includes Docker Compose, Dockerfiles, Makefile, and cert generation.

The stack is well-established Go ecosystem tooling: grpc-go for transport, pgx v5 for PostgreSQL, golang-migrate for schema management, sqlc for type-safe queries, buf for protobuf tooling, grpc-gateway for REST generation, cobra for CLI, fsnotify for config watching, and slog (stdlib) for structured logging. All choices are locked decisions from CONTEXT.md.

**Primary recommendation:** Build in vertical slices -- proto definitions first, then DB schema + sqlc generation, then server skeleton with mTLS, then enrollment flow, then config loading, then secrets provider, then agent stub, then infrastructure. Each slice should be independently testable.

<user_constraints>
## User Constraints (from CONTEXT.md)

### Locked Decisions

**Proto & API Design:**
- Single `isogov.proto` file -- all services and messages in one file
- Buf for proto tooling -- lint, format, breaking-change detection, code generation
- grpc-gateway to generate REST API from proto annotations -- one source of truth for gRPC and REST
- Separate ports: gRPC on 9090 (agent traffic), REST on 8080 (gvctl, web UI, metrics)

**Config Loading:**
- fsnotify with debounce (~500ms) for detecting config file changes
- Validation before swap: bad config is logged with error, last-known-good stays active. Server never runs invalid config.
- In-memory map (RWMutex-guarded) keyed by hostname for compiled configs
- Go templates rendered at check-in time (not config load time) -- always uses latest facts and vars

**Secrets Bootstrapping:**
- StaticProvider reads from `.env` file (gitignored) for local dev/testing
- Both layers of enforcement: convention-based `secret:` prefix resolution + strict enforcement on known-secret fields (dsn, TLS keys, webhook secrets). Startup fails if a known-secret field lacks the prefix.
- `SecretString` wrapper type with `String()` returning `[REDACTED]` -- prevents accidental logging via fmt/slog
- Hard fail with clear error message when a `secret:` reference can't be resolved. Log which path failed, exit immediately.

**DB & Migrations:**
- sqlc for type-safe query generation -- write SQL, get Go code
- Sequential migration numbering: `000001_create_approved_patterns.up.sql`
- Dev seed data via `make seed` -- inserts test clients, approved patterns, sample data for faster iteration
- testcontainers-go for integration tests -- real PostgreSQL in Docker per test suite

### Claude's Discretion
- Exact fsnotify debounce duration (500ms is a starting point)
- sqlc configuration details (sqlc.yaml structure)
- Buf module configuration (buf.yaml, buf.gen.yaml)
- grpc-gateway annotation style
- In-memory config map concurrency implementation details
- Docker Compose networking and health checks
- Makefile target structure beyond the required ones (build, proto, certs, test, lint, seed)

### Deferred Ideas (OUT OF SCOPE)
None -- discussion stayed within phase scope
</user_constraints>

<phase_requirements>
## Phase Requirements

| ID | Description | Research Support |
|----|-------------|-----------------|
| SEC-01 | Server accepts only mTLS gRPC connections from agents | grpc-go TLS config with `tls.RequireAndVerifyClientCert`, CA-signed client certs |
| SEC-02 | Agents authenticate via ED25519 SSH keypair with signed payloads | `crypto/ed25519` for signing, signature included in gRPC metadata or request field |
| SEC-03 | Server verifies client signature against stored pubkey | Lookup pubkey by client UUID, verify signed payload + timestamp (5-min replay window) |
| SEC-04 | Self-signed CA + server cert generation via `make certs` | OpenSSL commands in Makefile target, SANs for localhost + isogov |
| SEC-05 | Production supports bring-your-own TLS certs by replacing cert files | Volume mount pattern in Docker Compose, cert paths in server.yaml |
| ENRL-01 | Admin adds approved hostname/IP wildcard patterns with one-time tokens | REST endpoint via grpc-gateway, pattern + bcrypt-hashed token stored in approved_patterns |
| ENRL-02 | Agent registers by sending hostname, IP, pubkey, and one-time token | gRPC Register RPC, agent generates ED25519 keypair on first run |
| ENRL-03 | Server validates hostname/IP against approved patterns using glob matching | `filepath.Match` or `path.Match` for glob syntax |
| ENRL-04 | One-time tokens are bcrypt-hashed at rest and marked used after enrollment | `golang.org/x/crypto/bcrypt`, update used_at + used_by on successful enrollment |
| ENRL-05 | Server returns assigned client UUID on successful registration | UUID generated server-side via `gen_random_uuid()` in PostgreSQL |
| ENRL-06 | Admin can revoke a client via gvctl, blocking all future check-ins | REST endpoint sets `revoked=true, revoked_at=now()` on clients row |
| ENRL-07 | Revoked clients cannot re-enroll with the same UUID | Check revoked status in Register handler, reject if UUID exists and is revoked |
| DB-01 | PostgreSQL 16 with golang-migrate for schema migrations | pgx v5 driver, golang-migrate/migrate with postgres driver |
| DB-02 | Tables: approved_patterns, clients, runs, run_results, client_facts, plans, plan_results, file_snapshots, secret_rotation_events, package_audit, webhooks | Phase 1 creates all tables; some will be unused until later phases |
| DB-03 | All tables use UUIDs as primary keys | `gen_random_uuid()` in PostgreSQL, `pgtype.UUID` in Go via pgx |
| DB-04 | Client facts retention: last 10 snapshots per client | Application-level cleanup after insert or DB trigger |
| SECR-01 | `secrets.Provider` interface with `Get(ctx, key)` and `GetBytes(ctx, key)` methods | Interface definition in `server/secrets/provider.go` |
| SECR-02 | `IsopassProvider` satisfies Provider interface using real isopass client | Deferred to Phase 3 per roadmap decision; define interface now |
| SECR-03 | `StaticProvider` reads from environment variables for local dev/testing | Reads `.env` file, resolves `secret:key` to env var value |
| SECR-04 | Config loader detects `secret:` prefix and resolves via Provider at startup | String prefix check in config loader, call `Provider.Get()` |
| SECR-05 | Known-secret fields without `secret:` prefix cause startup failure | Hardcoded list of known-secret field paths (dsn, tls keys, webhook secrets) |
| SECR-06 | isopass unreachable at startup = hard failure | StaticProvider for Phase 1; hard fail if `.env` missing required keys |
| SECR-07 | Secret values never appear in logs, error messages, DB rows, gRPC payloads, or run results | `SecretString` wrapper type, `String()` returns `[REDACTED]` |
| CONF-01 | Server loads per-client config from `config/clients/*.yaml` files | YAML unmarshaling with `gopkg.in/yaml.v3`, glob to find files |
| CONF-02 | Module YAML files use Go template syntax with `.Facts` and `.Vars` namespaces | `text/template` stdlib, render at check-in time with per-client data |
| CONF-03 | Server watches `config/clients/` directory and reloads config without restart | fsnotify watcher with debounce, validate-then-swap pattern |
| CONF-04 | Environment tiers allow per-environment variable overrides in `env_vars` blocks | Merge base vars with env-specific vars at render time |
| CONF-05 | Base vars merged with env-specific vars at render time (env wins on conflict) | Map merge with env override taking precedence |
| CONF-06 | Auto-classification rules in `config/classification.yaml` assign modules by hostname/IP pattern | Glob matching against classification rules, first-match-wins |
| CONF-07 | Explicit client YAML takes precedence over classification rules | Check explicit config map first, fall back to classifier |
| CONF-08 | Unconfigured clients receive empty module list and run cleanly with no changes | Return empty module list in CheckIn response |
| AGNT-11 | Agent generates ED25519 keypair on first run, stores private key locally | `crypto/ed25519.GenerateKey`, PEM encoding, write to `/etc/isogov/agent.key` |
| INFRA-01 | Docker Compose with server, postgres, isopass services | Phase 1: server + postgres only (isopass deferred to Phase 3) |
| INFRA-02 | Dockerfile.server and Dockerfile.agent | Multi-stage Go build, minimal final image (distroless or alpine) |
| INFRA-03 | Makefile: build, proto, certs, test, lint targets | Standard Go Makefile with buf for proto, golangci-lint for lint |
</phase_requirements>

## Standard Stack

### Core
| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| google.golang.org/grpc | v1.68+ | gRPC server and client | The official Go gRPC implementation |
| google.golang.org/protobuf | v1.36+ | Protobuf runtime | Official protobuf Go runtime |
| github.com/grpc-ecosystem/grpc-gateway/v2 | v2.25+ | REST API from proto annotations | Standard approach for dual gRPC+REST from single proto |
| github.com/jackc/pgx/v5 | v5.7+ | PostgreSQL driver | Modern, actively maintained; lib/pq is in maintenance mode |
| github.com/golang-migrate/migrate/v4 | v4.18+ | Database migrations | Most popular Go migration tool, file-based sequential migrations |
| github.com/sqlc-dev/sqlc | v1.27+ | Type-safe SQL code generation | Write SQL, get Go -- no ORM overhead, compile-time safety |
| github.com/fsnotify/fsnotify | v1.8+ | Filesystem event watching | De facto standard for file watching in Go |
| github.com/spf13/cobra | v1.8+ | CLI framework | Standard Go CLI framework, used by kubectl, docker, gh |
| gopkg.in/yaml.v3 | v3.0.1 | YAML parsing | Standard YAML library for Go |
| log/slog | stdlib | Structured logging | Go 1.21+ stdlib, no external dependency needed |
| golang.org/x/crypto/bcrypt | latest | Token hashing | Standard bcrypt implementation for Go |
| crypto/ed25519 | stdlib | Agent keypair + signatures | stdlib, no external dependency |

### Supporting
| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| github.com/bufbuild/buf | v1.47+ | Proto tooling (CLI, not Go dep) | Proto lint, format, codegen via buf.gen.yaml |
| github.com/testcontainers/testcontainers-go | v0.34+ | Integration test containers | Real PostgreSQL in tests, per-suite Docker containers |
| github.com/joho/godotenv | v1.5+ | Load .env files | StaticProvider reads secrets from .env |
| github.com/google/uuid | v1.6+ | UUID generation in Go code | When generating UUIDs outside PostgreSQL |
| connectrpc.com/grpcreflect | latest | gRPC reflection | Dev/debug: allows grpcurl to discover services |

### Alternatives Considered
| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| pgx v5 | lib/pq | lib/pq is maintenance-mode; pgx has better performance and pgx/pgxpool |
| slog | zerolog/zap | slog is stdlib (Go 1.21+), zero external deps, sufficient for this project |
| cobra | urfave/cli | cobra is more widely used, better subcommand support for gvctl |
| sqlc | sqlx/GORM | sqlc generates type-safe code at build time; no reflection, no runtime ORM |
| godotenv | viper | godotenv is simpler for just .env loading; viper is overkill for StaticProvider |

**Installation:**
```bash
# Go module init
go mod init github.com/isogov/isogov

# Core dependencies
go get google.golang.org/grpc
go get google.golang.org/protobuf
go get github.com/grpc-ecosystem/grpc-gateway/v2
go get github.com/jackc/pgx/v5
go get github.com/golang-migrate/migrate/v4
go get github.com/fsnotify/fsnotify
go get github.com/spf13/cobra
go get gopkg.in/yaml.v3
go get golang.org/x/crypto
go get github.com/joho/godotenv
go get github.com/google/uuid

# Test dependencies
go get github.com/testcontainers/testcontainers-go

# CLI tooling (not Go deps)
# buf: https://buf.build/docs/installation
# sqlc: go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest
# golang-migrate CLI: go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest
# golangci-lint: https://golangci-lint.run/welcome/install/
```

## Architecture Patterns

### Recommended Project Structure
```
isogov/
├── proto/
│   └── isogov.proto              # Single proto file (locked decision)
├── gen/
│   └── proto/
│       └── isogov/               # Generated Go + grpc-gateway code
├── server/
│   ├── main.go                   # Server entrypoint
│   ├── grpc/
│   │   ├── server.go             # gRPC server setup, mTLS config
│   │   ├── register.go           # Register RPC handler
│   │   └── checkin.go            # CheckIn RPC handler (stub for Phase 1)
│   ├── api/
│   │   └── gateway.go            # grpc-gateway REST proxy setup
│   ├── db/
│   │   ├── queries.sql           # sqlc query definitions
│   │   ├── sqlc.yaml             # sqlc configuration
│   │   ├── db.go                 # Generated by sqlc
│   │   ├── models.go             # Generated by sqlc
│   │   └── queries.sql.go        # Generated by sqlc
│   ├── config/
│   │   ├── loader.go             # YAML config loading + fsnotify watcher
│   │   ├── types.go              # Config struct definitions
│   │   ├── classifier.go         # Auto-classification from classification.yaml
│   │   └── template.go           # Go template rendering for modules
│   ├── registry/
│   │   ├── registry.go           # Enrollment logic: pattern matching, token validation
│   │   └── registry_test.go
│   └── secrets/
│       ├── provider.go           # Provider interface
│       ├── static.go             # StaticProvider (.env file)
│       ├── secret_string.go      # SecretString wrapper type
│       └── static_test.go
├── agent/
│   ├── main.go                   # Agent entrypoint (stub: keygen + register only)
│   └── auth/
│       ├── keypair.go            # ED25519 keypair generation + storage
│       └── signer.go             # Request signing
├── cli/
│   └── main.go                   # gvctl entrypoint (cobra root command, minimal for Phase 1)
├── migrations/
│   ├── 000001_create_approved_patterns.up.sql
│   ├── 000001_create_approved_patterns.down.sql
│   ├── 000002_create_clients.up.sql
│   ├── 000002_create_clients.down.sql
│   └── ...                       # All tables from DB-02
├── config/
│   ├── server.yaml               # Server config with secret: prefixes
│   ├── clients/                  # Per-client YAML configs
│   │   └── example.yaml
│   └── classification.yaml       # Auto-classification rules
├── certs/                        # Generated by make certs (gitignored)
├── buf.yaml                      # Buf module config
├── buf.gen.yaml                  # Buf code generation config
├── sqlc.yaml                     # sqlc config (can also be in server/db/)
├── docker-compose.yml
├── Dockerfile.server
├── Dockerfile.agent
├── Makefile
├── .env.example                  # Template for .env (gitignored)
└── .gitignore
```

### Pattern 1: mTLS gRPC Server Setup
**What:** Configure gRPC server to require mutual TLS authentication
**When to use:** Server startup -- all agent connections must be mTLS
**Example:**
```go
// Load server cert + key
cert, err := tls.LoadX509KeyPair(certFile, keyFile)

// Load CA cert for verifying client certs
caCert, err := os.ReadFile(caFile)
caPool := x509.NewCertPool()
caPool.AppendCertsFromPEM(caCert)

tlsConfig := &tls.Config{
    Certificates: []tls.Certificate{cert},
    ClientCAs:    caPool,
    ClientAuth:   tls.RequireAndVerifyClientCert,
    MinVersion:   tls.VersionTLS13,
}

grpcServer := grpc.NewServer(
    grpc.Creds(credentials.NewTLS(tlsConfig)),
)
```

### Pattern 2: ED25519 Signature Verification
**What:** Agent signs requests with private key, server verifies with stored pubkey
**When to use:** Every gRPC request after enrollment
**Example:**
```go
// Agent side: sign payload
payload := append(serializedProto, []byte(timestamp)...)
signature := ed25519.Sign(privateKey, payload)

// Server side: verify
pubKey := ed25519.PublicKey(storedPubKeyBytes)
valid := ed25519.Verify(pubKey, payload, signature)
```

### Pattern 3: Config Watcher with Validate-Then-Swap
**What:** Watch config directory, validate new config, atomically swap if valid
**When to use:** Runtime config reloading without restart
**Example:**
```go
type ConfigStore struct {
    mu      sync.RWMutex
    configs map[string]*ClientConfig // keyed by hostname
}

func (s *ConfigStore) Get(hostname string) *ClientConfig {
    s.mu.RLock()
    defer s.mu.RUnlock()
    return s.configs[hostname]
}

func (s *ConfigStore) reload(dir string) {
    newConfigs, err := loadAllConfigs(dir)
    if err != nil {
        slog.Error("config reload failed, keeping previous", "error", err)
        return
    }
    s.mu.Lock()
    s.configs = newConfigs
    s.mu.Unlock()
    slog.Info("config reloaded", "clients", len(newConfigs))
}
```

### Pattern 4: SecretString Wrapper
**What:** Type that prevents accidental secret leakage in logs
**When to use:** Any resolved secret value
**Example:**
```go
type SecretString struct {
    value string
}

func NewSecretString(v string) SecretString { return SecretString{value: v} }
func (s SecretString) String() string       { return "[REDACTED]" }
func (s SecretString) Value() string        { return s.value }
func (s SecretString) MarshalJSON() ([]byte, error) { return []byte(`"[REDACTED]"`), nil }
func (s SecretString) MarshalText() ([]byte, error) { return []byte("[REDACTED]"), nil }
// Also implement LogValuer for slog:
func (s SecretString) LogValue() slog.Value { return slog.StringValue("[REDACTED]") }
```

### Pattern 5: sqlc Query Pattern
**What:** Write SQL queries with annotations, sqlc generates type-safe Go code
**When to use:** All database access
**Example:**
```sql
-- name: GetClientByID :one
SELECT id, hostname, ip_address, pubkey_pem, enrolled_at, last_seen, revoked, revoked_at
FROM clients
WHERE id = $1;

-- name: CreateClient :one
INSERT INTO clients (hostname, ip_address, pubkey_pem)
VALUES ($1, $2, $3)
RETURNING id, enrolled_at;

-- name: GetApprovedPatternByToken :one
SELECT id, pattern, token_hash, used_at
FROM approved_patterns
WHERE used_at IS NULL;
```

### Pattern 6: grpc-gateway Dual-Serve
**What:** Serve gRPC on one port and REST on another from same proto
**When to use:** Server startup -- gRPC on 9090, REST on 8080
**Example:**
```go
// In proto file:
import "google/api/annotations.proto";

service IsgovAdmin {
    rpc AddApprovedPattern(AddPatternRequest) returns (AddPatternResponse) {
        option (google.api.http) = {
            post: "/api/v1/approved"
            body: "*"
        };
    }
}

// In Go: run grpc-gateway as reverse proxy to gRPC
mux := runtime.NewServeMux()
opts := []grpc.DialOption{grpc.WithTransportCredentials(creds)}
err := pb.RegisterIsgovAdminHandlerFromEndpoint(ctx, mux, "localhost:9090", opts)
http.ListenAndServe(":8080", mux)
```

### Anti-Patterns to Avoid
- **Global database connections:** Pass `*pgxpool.Pool` via dependency injection, never use package-level vars
- **Secrets in struct tags or fmt output:** Always use `SecretString` wrapper; never store raw secret strings in exported struct fields
- **Blocking fsnotify handler:** Process events asynchronously with debounce; never do heavy work in the event callback
- **Loading all templates at startup:** Templates should be parsed at check-in time (locked decision) so they always use latest facts/vars
- **Tight coupling to secret provider:** All code uses `secrets.Provider` interface, never imports static/isopass directly

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| Database migrations | Custom SQL runner | golang-migrate | Handles up/down, dirty state, concurrent migrations, version tracking |
| SQL query mapping | Manual `rows.Scan()` boilerplate | sqlc | Compile-time type safety, catches schema/query mismatches |
| Proto codegen | Manual protoc invocation | buf | Manages protoc plugins, linting, breaking change detection in one tool |
| REST from proto | Separate REST handlers | grpc-gateway | Single source of truth, auto-generated REST endpoints |
| Config file watching | Custom inotify/polling | fsnotify | Cross-platform, handles edge cases (rename, chmod, delete) |
| CLI arg parsing | Manual flag parsing | cobra | Subcommand support, help generation, shell completion |
| YAML parsing | Custom parser | gopkg.in/yaml.v3 | Handles anchors, aliases, multiline, all YAML edge cases |
| Bcrypt hashing | Custom hash scheme | golang.org/x/crypto/bcrypt | Proven, constant-time comparison, configurable cost |
| UUID generation | Custom ID scheme | PostgreSQL gen_random_uuid() + google/uuid | Standard, collision-resistant, no coordination needed |
| Container test infra | Custom Docker scripts | testcontainers-go | Programmatic container lifecycle, cleanup, port mapping |

**Key insight:** This phase is infrastructure-heavy -- nearly every component has a well-tested library. The risk is in integration and wiring, not in individual component implementation. Focus implementation effort on the glue code: enrollment flow, config loading pipeline, and the secret resolution chain.

## Common Pitfalls

### Pitfall 1: mTLS Certificate SANs
**What goes wrong:** Server cert generated without proper Subject Alternative Names; gRPC clients reject the cert with "certificate is not valid for any names"
**Why it happens:** OpenSSL defaults don't include SANs; Go's crypto/tls enforces SAN checking strictly
**How to avoid:** `make certs` must include `-addext "subjectAltName=DNS:localhost,DNS:isogov,DNS:server,IP:127.0.0.1"` in the server cert CSR. Include the Docker Compose service name (`server`) as a SAN.
**Warning signs:** Connection refused errors mentioning certificate validation in gRPC client logs

### Pitfall 2: pgx v5 vs lib/pq API Differences
**What goes wrong:** Using `database/sql` patterns with pgx, missing pgx-native features, or sqlc generating code for the wrong driver
**Why it happens:** Many Go PostgreSQL tutorials still use lib/pq
**How to avoid:** Configure sqlc with `sql_package: "pgx/v5"` in sqlc.yaml. Use `pgxpool.Pool` directly, not `database/sql`. Use `pgtype` for UUID and TIMESTAMPTZ types.
**Warning signs:** Import of `lib/pq` or `database/sql` anywhere in the codebase

### Pitfall 3: golang-migrate + pgx v5 Driver Registration
**What goes wrong:** golang-migrate can't connect to PostgreSQL because the pgx driver isn't registered
**Why it happens:** golang-migrate uses its own driver system separate from pgx
**How to avoid:** Import `github.com/golang-migrate/migrate/v4/database/pgx/v5` (note: the v5 subpackage) and `github.com/golang-migrate/migrate/v4/source/file` for file-based migrations. Use `pgx5.WithInstance()` if sharing a connection pool with sqlc.
**Warning signs:** "unknown driver" errors at startup

### Pitfall 4: bcrypt Timing on Token Validation
**What goes wrong:** Iterating over all approved patterns and comparing tokens leaks information about how many patterns exist via timing
**Why it happens:** bcrypt.CompareHashAndPassword is intentionally slow (~100ms per call)
**How to avoid:** First filter patterns by glob match on hostname/IP, then check token against the matching pattern's hash. Don't bcrypt-compare against all patterns. Also: the token lookup should find patterns where `used_at IS NULL` (unused tokens only).
**Warning signs:** Registration taking seconds when many approved patterns exist

### Pitfall 5: fsnotify Event Storm
**What goes wrong:** Saving a YAML file triggers multiple events (CREATE, WRITE, CHMOD), causing multiple config reloads
**Why it happens:** Editors create temp files, rename, and set permissions -- each generates an fsnotify event
**How to avoid:** Debounce events with a timer (500ms per locked decision). Reset the timer on each event. Only trigger reload when the timer fires without being reset.
**Warning signs:** "config reloaded" log messages appearing 3-5 times per file save

### Pitfall 6: Protobuf Timestamp and UUID Handling
**What goes wrong:** Mismatch between Go `time.Time`/`uuid.UUID` and protobuf `google.protobuf.Timestamp`/`string` types
**Why it happens:** Protobuf has its own timestamp type; UUIDs are typically represented as strings in proto
**How to avoid:** Use `google.protobuf.Timestamp` in proto for all time fields. Use `string` for UUIDs in proto (not bytes). Convert at the handler boundary using `timestamppb.New()` and `uuid.Parse()`.
**Warning signs:** Serialization errors or zero-value timestamps in responses

### Pitfall 7: grpc-gateway Proto Import Paths
**What goes wrong:** grpc-gateway annotations fail to compile because `google/api/annotations.proto` can't be found
**Why it happens:** The google/api protos must be available to buf/protoc but aren't bundled automatically
**How to avoid:** In `buf.yaml`, add `buf.build/googleapis/googleapis` as a dependency. Buf will handle the import resolution. Don't try to vendor the google/api protos manually.
**Warning signs:** `import "google/api/annotations.proto" was not found` during buf generate

### Pitfall 8: ED25519 PEM Encoding
**What goes wrong:** Agent generates ED25519 keypair but stores it in wrong PEM format, server can't parse it
**Why it happens:** ED25519 keys use PKCS8 encoding in PEM, not the older PKIX format. Multiple PEM block types exist.
**How to avoid:** Use `x509.MarshalPKCS8PrivateKey()` for private key, `x509.MarshalPKIXPublicKey()` for public key. PEM block types: `PRIVATE KEY` (PKCS8) and `PUBLIC KEY` (PKIX). Use `x509.ParsePKIXPublicKey()` on the server side to decode.
**Warning signs:** "asn1: structure error" when parsing stored public keys

## Code Examples

### sqlc.yaml Configuration
```yaml
version: "2"
sql:
  - engine: "postgresql"
    queries: "server/db/queries/"
    schema: "migrations/"
    gen:
      go:
        package: "db"
        out: "server/db"
        sql_package: "pgx/v5"
        emit_json_tags: true
        emit_empty_slices: true
        overrides:
          - db_type: "uuid"
            go_type: "github.com/google/uuid.UUID"
          - db_type: "timestamptz"
            go_type: "time.Time"
          - db_type: "jsonb"
            go_type: "json.RawMessage"
            import: "encoding/json"
```

### buf.yaml Configuration
```yaml
version: v2
modules:
  - path: proto
    name: buf.build/isogov/isogov
deps:
  - buf.build/googleapis/googleapis
  - buf.build/grpc-ecosystem/grpc-gateway
lint:
  use:
    - DEFAULT
breaking:
  use:
    - FILE
```

### buf.gen.yaml Configuration
```yaml
version: v2
plugins:
  - remote: buf.build/protocolbuffers/go
    out: gen/proto
    opt: paths=source_relative
  - remote: buf.build/grpc/go
    out: gen/proto
    opt: paths=source_relative
  - remote: buf.build/grpc-ecosystem/gateway
    out: gen/proto
    opt:
      - paths=source_relative
      - generate_unbound_methods=true
  - remote: buf.build/grpc-ecosystem/openapiv2
    out: gen/openapi
```

### StaticProvider Implementation
```go
package secrets

import (
    "context"
    "fmt"
    "os"
    "strings"

    "github.com/joho/godotenv"
)

type StaticProvider struct {
    vars map[string]string
}

func NewStaticProvider(envFile string) (*StaticProvider, error) {
    vars, err := godotenv.Read(envFile)
    if err != nil {
        return nil, fmt.Errorf("reading .env file: %w", err)
    }
    // Also include actual environment variables as fallback
    for _, env := range os.Environ() {
        parts := strings.SplitN(env, "=", 2)
        if len(parts) == 2 {
            if _, exists := vars[parts[0]]; !exists {
                vars[parts[0]] = parts[1]
            }
        }
    }
    return &StaticProvider{vars: vars}, nil
}

func (p *StaticProvider) Get(ctx context.Context, key string) (string, error) {
    // Convert secret path to env var name: "isogov/db/dsn" -> "ISOGOV_DB_DSN"
    envKey := strings.ToUpper(strings.ReplaceAll(key, "/", "_"))
    val, ok := p.vars[envKey]
    if !ok {
        return "", fmt.Errorf("secret %q not found (looked for env var %s)", key, envKey)
    }
    return val, nil
}

func (p *StaticProvider) GetBytes(ctx context.Context, key string) ([]byte, error) {
    val, err := p.Get(ctx, key)
    if err != nil {
        return nil, err
    }
    return []byte(val), nil
}

func (p *StaticProvider) Close() error { return nil }
```

### Makefile Skeleton
```makefile
.PHONY: build proto certs test lint seed migrate

build:
	go build -o bin/isogov-server ./server
	go build -o bin/isogov-agent ./agent
	go build -o bin/gvctl ./cli

proto:
	buf generate

certs:
	mkdir -p certs
	# Generate CA
	openssl genrsa -out certs/ca.key 4096
	openssl req -new -x509 -key certs/ca.key -out certs/ca.crt -days 3650 \
		-subj "/CN=Isogov CA"
	# Generate server cert
	openssl genrsa -out certs/server.key 4096
	openssl req -new -key certs/server.key -out certs/server.csr \
		-subj "/CN=isogov" \
		-addext "subjectAltName=DNS:localhost,DNS:isogov,DNS:server,IP:127.0.0.1"
	openssl x509 -req -in certs/server.csr -CA certs/ca.crt -CAkey certs/ca.key \
		-CAcreateserial -out certs/server.crt -days 365 \
		-copy_extensions copyall
	rm certs/server.csr

test:
	go test ./... -v -race

lint:
	golangci-lint run ./...
	buf lint

seed:
	go run ./cmd/seed/main.go

migrate:
	migrate -path migrations -database "$$DATABASE_URL" up
```

### Docker Compose with Health Checks
```yaml
services:
  postgres:
    image: postgres:16
    environment:
      POSTGRES_DB: isogov
      POSTGRES_USER: isogov
      POSTGRES_PASSWORD: secret
    ports:
      - "5432:5432"
    volumes:
      - pgdata:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U isogov"]
      interval: 5s
      timeout: 5s
      retries: 5

  server:
    build:
      context: .
      dockerfile: Dockerfile.server
    ports:
      - "9090:9090"
      - "8080:8080"
    volumes:
      - ./config:/config:ro
      - ./certs:/certs:ro
    env_file:
      - .env
    depends_on:
      postgres:
        condition: service_healthy

volumes:
  pgdata:
```

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| lib/pq PostgreSQL driver | pgx v5 | 2023 (lib/pq maintenance mode) | Use pgx/v5 exclusively, better performance, native types |
| zerolog/zap for logging | slog (stdlib) | Go 1.21 (Aug 2023) | No external logging dependency needed |
| protoc + manual plugin management | buf CLI | 2022-2023 widespread adoption | Single tool for lint, generate, breaking changes |
| database/sql with manual scanning | sqlc code generation | 2023-2024 matured | Compile-time type safety, no reflection |
| Custom test DB setup | testcontainers-go | 2023-2024 v0.20+ | Real PostgreSQL per test, no mocking |
| google.golang.org/grpc v1.x manual setup | grpc-go v1.68+ with grpc-gateway v2 | Ongoing | Better defaults, simpler TLS configuration |

**Deprecated/outdated:**
- `lib/pq`: Maintenance mode since 2023 -- use pgx v5
- `github.com/golang/protobuf`: Replaced by `google.golang.org/protobuf` -- use the new module
- Manual `protoc` invocation: Use `buf generate` instead
- `ioutil` package: Removed in Go 1.20 -- use `os` and `io` directly

## Open Questions

1. **Go module path**
   - What we know: The repo is at `/home/dan/repos/isogov`
   - What's unclear: Whether the module path should be `github.com/isogov/isogov`, `isogov`, or something else
   - Recommendation: Use a simple path like `github.com/isogov/isogov` or just `isogov` -- can be changed later

2. **grpc-gateway: single or split proto service**
   - What we know: Locked decision is single `isogov.proto` file. Agent gRPC on 9090, REST on 8080.
   - What's unclear: Whether to have one `Isogov` service with all RPCs (agent + admin) or split into `IsgovAgent` (gRPC-only) and `IsgovAdmin` (gRPC + REST annotations)
   - Recommendation: Two services in one proto file -- `IsgovAgent` for agent-facing RPCs (Register, CheckIn, ReportResult) and `IsgovAdmin` for admin RPCs with grpc-gateway annotations. This keeps agent traffic separate from REST traffic naturally.

3. **Replay attack window**
   - What we know: Handoff spec says "reject requests older than 5 minutes"
   - What's unclear: Whether to also track used nonces/timestamps to prevent exact replay within the 5-min window
   - Recommendation: Timestamp check is sufficient for Phase 1. A nonce store adds complexity without proportional security benefit for this use case.

4. **DB-02 scope: create all tables or just Phase 1 tables?**
   - What we know: Requirement says all tables listed. Some (plans, plan_results, file_snapshots, etc.) won't be used until later phases.
   - What's unclear: Whether to create empty tables now or defer migration creation
   - Recommendation: Create all table migrations now (they're defined in the handoff spec). Having the schema complete aids sqlc generation and prevents migration ordering issues later.

## Validation Architecture

### Test Framework
| Property | Value |
|----------|-------|
| Framework | Go testing + testcontainers-go v0.34+ |
| Config file | None needed -- Go's built-in test framework |
| Quick run command | `go test ./... -short -count=1` |
| Full suite command | `go test ./... -v -race -count=1` |

### Phase Requirements to Test Map
| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|-------------|
| SEC-01 | mTLS-only connections accepted | integration | `go test ./server/grpc/... -run TestMTLSRequired -v` | -- Wave 0 |
| SEC-02 | ED25519 signing on agent requests | unit | `go test ./agent/auth/... -run TestSignPayload -v` | -- Wave 0 |
| SEC-03 | Server verifies client signature | unit | `go test ./server/grpc/... -run TestSignatureVerification -v` | -- Wave 0 |
| SEC-04 | make certs generates valid CA + server cert | integration | `make certs && openssl verify -CAfile certs/ca.crt certs/server.crt` | -- Wave 0 |
| ENRL-01 | Admin adds approved patterns with tokens | integration | `go test ./server/registry/... -run TestAddApprovedPattern -v` | -- Wave 0 |
| ENRL-02 | Agent registers with hostname/IP/pubkey/token | integration | `go test ./server/registry/... -run TestRegisterAgent -v` | -- Wave 0 |
| ENRL-03 | Hostname/IP validated against glob patterns | unit | `go test ./server/registry/... -run TestGlobMatching -v` | -- Wave 0 |
| ENRL-04 | Tokens bcrypt-hashed, marked used after enrollment | integration | `go test ./server/registry/... -run TestTokenHashAndUse -v` | -- Wave 0 |
| ENRL-05 | Server returns client UUID on success | integration | `go test ./server/registry/... -run TestRegisterReturnsUUID -v` | -- Wave 0 |
| ENRL-06 | Admin revokes a client | integration | `go test ./server/registry/... -run TestRevokeClient -v` | -- Wave 0 |
| ENRL-07 | Revoked clients cannot re-enroll | integration | `go test ./server/registry/... -run TestRevokedCannotReenroll -v` | -- Wave 0 |
| DB-01 | Migrations run cleanly on fresh DB | integration | `go test ./server/db/... -run TestMigrationsUp -v` | -- Wave 0 |
| DB-03 | All tables use UUID primary keys | unit | `go test ./server/db/... -run TestSchemaUUIDs -v` | -- Wave 0 |
| SECR-01 | Provider interface has Get and GetBytes | unit | Compile-time check (interface satisfaction) | -- Wave 0 |
| SECR-03 | StaticProvider reads from .env | unit | `go test ./server/secrets/... -run TestStaticProvider -v` | -- Wave 0 |
| SECR-04 | Config loader resolves secret: prefix | unit | `go test ./server/config/... -run TestSecretResolution -v` | -- Wave 0 |
| SECR-05 | Known-secret fields without prefix fail startup | unit | `go test ./server/config/... -run TestKnownSecretEnforcement -v` | -- Wave 0 |
| SECR-07 | SecretString never leaks in String()/JSON/logs | unit | `go test ./server/secrets/... -run TestSecretStringRedaction -v` | -- Wave 0 |
| CONF-01 | Loads per-client YAML configs | unit | `go test ./server/config/... -run TestLoadClientConfig -v` | -- Wave 0 |
| CONF-03 | fsnotify triggers config reload | integration | `go test ./server/config/... -run TestConfigWatcher -v` | -- Wave 0 |
| CONF-06 | Classification rules assign modules | unit | `go test ./server/config/... -run TestClassification -v` | -- Wave 0 |
| CONF-07 | Explicit config takes precedence | unit | `go test ./server/config/... -run TestExplicitOverClassification -v` | -- Wave 0 |
| CONF-08 | Unconfigured clients get empty modules | unit | `go test ./server/config/... -run TestUnconfiguredClient -v` | -- Wave 0 |
| AGNT-11 | Agent generates ED25519 keypair | unit | `go test ./agent/auth/... -run TestKeypairGeneration -v` | -- Wave 0 |
| INFRA-03 | Makefile targets work | integration | `make build && make lint` | -- Wave 0 |

### Sampling Rate
- **Per task commit:** `go test ./... -short -count=1`
- **Per wave merge:** `go test ./... -v -race -count=1`
- **Phase gate:** Full suite green before `/gsd:verify-work`

### Wave 0 Gaps
- [ ] `server/registry/registry_test.go` -- covers ENRL-01 through ENRL-07
- [ ] `server/secrets/static_test.go` -- covers SECR-01, SECR-03, SECR-07
- [ ] `server/config/loader_test.go` -- covers CONF-01, CONF-03 through CONF-08, SECR-04, SECR-05
- [ ] `server/grpc/server_test.go` -- covers SEC-01, SEC-03
- [ ] `agent/auth/keypair_test.go` -- covers AGNT-11, SEC-02
- [ ] `server/db/migrate_test.go` -- covers DB-01, DB-03
- [ ] testcontainers-go dependency installed for integration tests
- [ ] `.env.test` file with test secrets for StaticProvider tests

## Sources

### Primary (HIGH confidence)
- `isogov-handoff.md` -- Complete project specification with proto definitions, DB schema, config format, Docker Compose
- `CLAUDE.md` -- Project conventions, architecture, non-negotiable constraints
- `01-CONTEXT.md` -- Locked user decisions for Phase 1

### Secondary (MEDIUM confidence)
- Go stdlib documentation (crypto/ed25519, crypto/tls, text/template, log/slog) -- standard library, stable APIs
- pgx v5, golang-migrate, sqlc, buf, grpc-gateway -- well-established Go ecosystem libraries with stable APIs

### Tertiary (LOW confidence)
- Exact latest versions of libraries -- versions cited are approximate based on training data; `go get` will fetch actual latest

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH -- all libraries are locked decisions from CONTEXT.md with well-known Go ecosystem status
- Architecture: HIGH -- project structure is specified in handoff doc, patterns are standard Go
- Pitfalls: HIGH -- based on well-known Go/gRPC/PostgreSQL gotchas from extensive ecosystem experience
- Config/secrets patterns: HIGH -- patterns are straightforward Go with clear requirements from handoff spec

**Research date:** 2026-03-05
**Valid until:** 2026-04-05 (stable Go ecosystem, 30-day validity)
