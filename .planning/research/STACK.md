# Technology Stack

**Project:** Isogov -- Puppet-style Configuration Management System
**Researched:** 2026-03-05

## Recommended Stack

### Language Runtime

| Technology | Version | Purpose | Why | Confidence |
|------------|---------|---------|-----|------------|
| Go | 1.24.13+ | Language runtime | Latest stable with security patches (Feb 2026). Generic type aliases, range over integers, tool directive in go.mod, post-quantum TLS support. Go 1.25 is also available but 1.24 is the conservative choice with full library ecosystem compatibility. | HIGH |

**Go version rationale:** Go 1.24 introduced the `tool` directive in `go.mod` for managing executable dependencies (buf, protoc-gen-go, migrate CLI), which directly benefits this project's build tooling. Post-quantum TLS support and Encrypted Client Hello are relevant for a security-focused config management system.

### Transport & API

| Technology | Version | Purpose | Why | Confidence |
|------------|---------|---------|-----|------------|
| google.golang.org/grpc | v1.70+ | gRPC server/client | Official Go gRPC implementation. Active development with Feb 2026 releases. Built-in mTLS via `credentials.NewTLS()`. Required for the project's gRPC transport layer. | HIGH |
| google.golang.org/protobuf | v1.36+ | Protobuf runtime | Official Go protobuf runtime. Required by grpc-go. | HIGH |
| grpc-ecosystem/grpc-gateway | v2.27+ | REST API from proto definitions | Generates REST reverse-proxy from gRPC proto definitions with google.api.http annotations. The project needs REST for gvctl and web UI. This approach means one proto definition serves both gRPC (agent) and REST (CLI/UI) consumers. | HIGH |
| grpc-ecosystem/go-grpc-middleware/v2 | v2.x | gRPC interceptors | Pre-built interceptors for auth, logging, recovery, rate limiting. Provides interceptor chaining, slog integration, panic recovery. Avoids writing boilerplate interceptor code. | HIGH |

**Why grpc-gateway over Connect-Go:** The project requires both gRPC transport (agents) and REST (gvctl CLI, web UI). grpc-gateway generates REST from proto annotations, keeping a single source of truth. Connect-Go is a better gRPC overall (built on net/http, simpler API), but grpc-gateway is the right choice here because: (1) the proto definitions are already specified in the handoff doc with standard gRPC service definitions, (2) grpc-gateway's annotation-based REST generation means zero additional REST handler code, and (3) the gRPC ecosystem middleware (auth interceptors, recovery) integrates directly.

**Why NOT Connect-Go:** Connect-Go builds on net/http (advantage) and supports gRPC + gRPC-Web + Connect protocols, but it would require writing separate REST handlers or using Connect's HTTP-based protocol for the CLI/UI. grpc-gateway gives true REST (with OpenAPI spec generation) from the same proto files.

### Database

| Technology | Version | Purpose | Why | Confidence |
|------------|---------|---------|-----|------------|
| jackc/pgx | v5.8+ | PostgreSQL driver | 10-20% faster than lib/pq via database/sql, 2x faster via native interface. Binary protocol, LISTEN/NOTIFY support (useful for real-time client status), COPY for bulk operations, connection pooling via pgxpool. lib/pq is in maintenance mode -- its own maintainers recommend pgx. | HIGH |
| golang-migrate/migrate | v4.19+ | Database migrations | Project constraint specifies golang-migrate. CLI + library modes. Supports pgx via pgx5 driver tag. Actively maintained (Nov 2025 release). | HIGH |
| PostgreSQL | 16 | Database | Project constraint. Use pgx's native interface (not database/sql) for PostgreSQL-specific features like LISTEN/NOTIFY and COPY. | HIGH |

**pgx usage pattern:** Use `pgxpool.Pool` for connection pooling, not raw `pgx.Conn`. Use pgx's native query interface (not `database/sql`) to get binary protocol performance and PostgreSQL-specific features. Reserve `database/sql` compatibility only for golang-migrate integration.

**Why NOT lib/pq:** Maintenance mode, no active development, text-based protocol (slower), missing PostgreSQL-specific features. pgx is the community consensus replacement.

**Why NOT GORM/sqlx:** This project manages a well-defined schema (nodes, configs, runs, compliance). Raw SQL with pgx is faster, more transparent, and avoids ORM magic. Sysadmins reading the code should see exactly what queries run. sqlx adds convenience but masks query structure. For a system where audit and transparency matter, direct SQL is better.

### Security & Cryptography

| Technology | Version | Purpose | Why | Confidence |
|------------|---------|---------|-----|------------|
| crypto/tls (stdlib) | - | mTLS for gRPC | Built into Go stdlib. Supports TLS 1.3, certificate hot-reload via `GetCertificate` callback (project requirement). No external dependency needed. | HIGH |
| crypto/ed25519 (stdlib) | - | ED25519 key operations | Built into Go stdlib since Go 1.13. Used for key generation and signature verification. | HIGH |
| golang.org/x/crypto/ssh | latest | SSH key parsing/serialization | Parses ED25519 keys in OpenSSH format. `ParseRawPrivateKey` supports PKCS#1, PKCS#8, OpenSSH formats. Required for client enrollment with SSH keypairs. | HIGH |
| golang.org/x/crypto/bcrypt | latest | Token hashing | Hashes one-time enrollment tokens at rest. Project requirement: bcrypt-hashed tokens. Standard, battle-tested implementation. | HIGH |

**mTLS implementation:** Use Go's `crypto/tls` directly with `tls.Config{ClientAuth: tls.RequireAndVerifyClientCert}`. No external mTLS library needed -- grpc-go's `credentials.NewTLS()` accepts a standard `*tls.Config`. For certificate hot-reload, use the `GetCertificate`/`GetConfigForClient` callbacks to watch cert files and reload without server restart.

**Why NOT go-spiffe:** SPIFFE/SPIRE is enterprise PKI automation. Isogov manages its own certs (project spec shows cert generation in Makefile). Adding SPIFFE would introduce unnecessary infrastructure dependency for a self-contained config management tool.

### Configuration & Serialization

| Technology | Version | Purpose | Why | Confidence |
|------------|---------|---------|-----|------------|
| go.yaml.in/yaml/v3 | v3.x | YAML parsing | Drop-in replacement for gopkg.in/yaml.v3 (which is now archived/unmaintained). Same API, actively maintained under yaml.org umbrella, receives security updates. | MEDIUM |
| text/template (stdlib) | - | Go template rendering | Project uses Go template syntax for config rendering with node facts. Stdlib -- no dependency needed. | HIGH |
| fsnotify/fsnotify | v1.8+ | Filesystem watching | Watches config YAML directory for changes (project requirement: "server watches for changes"). Cross-platform, stable at v1. Only option in Go ecosystem. | HIGH |

**YAML library note:** If `go.yaml.in/yaml/v3` causes import friction (it is relatively new as a path), `gopkg.in/yaml.v3` is still perfectly functional and stable despite being archived. The API is identical. Start with `go.yaml.in/yaml/v3` but fall back to `gopkg.in/yaml.v3` if there are module resolution issues. Confidence is MEDIUM because the new import path is recent and less battle-tested in module tooling.

**Why NOT viper for config:** Viper is for application configuration (reading env vars, config files, flags). Isogov's config is user-defined desired-state YAML for managed nodes -- it's application data, not application config. Parsing it directly with yaml.v3 is simpler and more transparent.

### CLI

| Technology | Version | Purpose | Why | Confidence |
|------------|---------|---------|-----|------------|
| spf13/cobra | v1.8+ | CLI framework for gvctl | gvctl has 10+ subcommands (approved, clients, push, runs, tokens, webhooks, packages, env, preview, rollback). Cobra's subcommand model, auto-generated help, and flag parsing are purpose-built for this. Used by kubectl, helm, docker -- proven for admin tooling. | HIGH |

**Why NOT urfave/cli:** urfave/cli is better for simple single-purpose CLIs. gvctl has nested subcommands, per-command flags, and will grow. Cobra handles this natively.

**Why NOT stdlib flag:** Too primitive for 10+ subcommands with distinct flag sets. Would require building subcommand dispatch from scratch.

### Observability

| Technology | Version | Purpose | Why | Confidence |
|------------|---------|---------|-----|------------|
| log/slog (stdlib) | - | Structured logging | Go stdlib since 1.21. Zero dependencies, structured key-value logging, JSON output handler, integrates with go-grpc-middleware/v2 logging interceptor. | HIGH |
| prometheus/client_golang | v1.21+ | Prometheus metrics | Official Prometheus client. Project specifies Prometheus metrics for clients, check-ins, drift, run duration, compliance. No alternative -- this is the standard. | HIGH |

**Why slog over zerolog/zap:** Zerolog is faster (zero-allocation), but slog is "fast enough" and has zero external dependencies. For a config management server, logging is not the performance bottleneck -- idempotent resource application is. slog also integrates natively with go-grpc-middleware/v2's logging interceptor without adapter code. The 10-20% speed difference in logging is irrelevant here.

**Why NOT OpenTelemetry for now:** OTel is the right long-term choice for distributed tracing, but isogov is a hub-spoke architecture (server + agents), not a microservice mesh. Prometheus metrics cover the stated requirements. Add OTel later if tracing across server-agent interactions becomes needed.

### Web UI

| Technology | Version | Purpose | Why | Confidence |
|------------|---------|---------|-----|------------|
| embed (stdlib) | - | Embed SPA in binary | `//go:embed` directive bakes the built frontend into the Go binary. Single binary deployment -- critical for a tool that manages infrastructure. | HIGH |
| React + Vite | React 19, Vite 6 | SPA framework | Dashboard, clients, runs, config, plans, compliance views. React has the largest ecosystem for admin dashboards. Vite for fast builds. Embed dist/ output into Go binary. | MEDIUM |
| net/http (stdlib) | - | Serve embedded SPA | Serve the embedded SPA static files via `http.FileServer`. grpc-gateway already runs an HTTP server -- serve the SPA alongside REST endpoints. | HIGH |

**Why React over templ+htmx:** The project spec calls for an "SPA" with dashboard, client list, run history, config viewer, compliance views. This is a data-heavy admin UI with client-side routing, filtering, and real-time updates. React handles this well. templ+htmx is excellent for server-rendered pages but becomes awkward for complex client-side state (filtering run history, live compliance dashboards). The SPA compiles to static files embedded in the Go binary -- no Node.js runtime in production.

**Why NOT a separate frontend deployment:** Single binary deployment is a core value for infrastructure tools. Embedding eliminates deployment complexity and CORS issues.

**Web UI is last priority:** The spec lists CLI and REST API as primary interfaces. Build the web UI after the core system works. This means the React choice can be revisited later without affecting architecture.

### Build & Proto Tooling

| Technology | Version | Purpose | Why | Confidence |
|------------|---------|---------|-----|------------|
| buf | v1.50+ | Protobuf compilation & linting | 2x faster than protoc, recursive .proto discovery, managed mode for Go package paths, remote plugins (no local protoc-gen-go install), lint rules catch proto design issues early. Single `buf.gen.yaml` replaces complex protoc command lines. | HIGH |
| protoc-gen-go | latest | Go code from protos | Official protobuf Go code generator. Managed as tool dependency via buf or go.mod tool directive. | HIGH |
| protoc-gen-go-grpc | latest | gRPC Go code from protos | Official gRPC Go code generator. Same management as above. | HIGH |
| protoc-gen-grpc-gateway | latest | REST gateway code from protos | Generates REST reverse-proxy handlers from google.api.http annotations. Part of grpc-gateway ecosystem. | HIGH |
| protoc-gen-openapiv2 | latest | OpenAPI spec from protos | Generates OpenAPI v2 spec from proto definitions. Useful for documenting the REST API for gvctl and web UI consumers. | MEDIUM |

**Why buf over raw protoc:** buf provides linting (catches breaking changes), managed mode (eliminates `option go_package` boilerplate in .proto files), and remote plugins (no local plugin installation). For a project with 7+ RPC methods and REST gateway annotations, buf's configuration-driven approach is significantly less error-prone than protoc CLI flags.

### Testing

| Technology | Version | Purpose | Why | Confidence |
|------------|---------|---------|-----|------------|
| testing (stdlib) | - | Test framework | Go's built-in test runner. Table-driven tests, subtests, parallel execution. Foundation for all tests. | HIGH |
| stretchr/testify | v1.9+ | Assertions & mocks | `assert` package for readable assertions, `require` for fatal assertions, `mock` for interface mocking. Reduces boilerplate vs raw `if got != want { t.Errorf(...) }`. Used by the vast majority of Go projects. | HIGH |
| testcontainers-go | v0.35+ | Integration testing with PostgreSQL | Spins up real PostgreSQL containers for migration and query tests. Better than mocking database interactions -- tests run against real PostgreSQL. | MEDIUM |

**Why testify:** The project has many interfaces (Collector, SecretsProvider, module executors) that benefit from mock generation. testify/mock provides this without a separate tool. testify/assert makes test failures readable.

**Why NOT gomock:** gomock requires a separate code generation step and is more ceremony. testify/mock is simpler for the interface count in this project.

### Supporting Libraries

| Library | Version | Purpose | When to Use | Confidence |
|---------|---------|---------|-------------|------------|
| golang.org/x/sync/errgroup | latest | Concurrent operations | Agent fact collectors running in parallel, concurrent module checks. Provides structured goroutine management with error propagation. | HIGH |
| golang.org/x/sync/semaphore | latest | Concurrency limiting | Rate-limit concurrent agent check-ins on server side. | HIGH |
| crypto/sha256 (stdlib) | - | File checksums | File module SHA256 check-before-act. Agent self-update checksum verification. | HIGH |
| encoding/json (stdlib) | - | JSON serialization | REST API responses, webhook HMAC payloads. | HIGH |
| crypto/hmac (stdlib) | - | Webhook signing | HMAC-SHA256 webhook signatures. Project requirement. | HIGH |
| net/http (stdlib) | - | Webhook dispatch, REST server | HTTP client for webhook delivery. HTTP server for REST API (via grpc-gateway). | HIGH |
| os/exec (stdlib) | - | System commands | Package manager invocation (apt, dnf, etc.), service management (systemctl). | HIGH |

### DAG / Dependency Resolution

| Technology | Version | Purpose | Why | Confidence |
|------------|---------|---------|-----|------------|
| Custom implementation | - | Module dependency DAG | Topological sort with cycle detection is ~100 lines of Go. Kahn's algorithm. External DAG libraries (souleb/dag, gammazero/toposort, gonum/graph/topo) add a dependency for trivial code. The project's DAG is simple: modules declare dependencies, sort them, detect cycles. No graph analysis, shortest path, or weighted edges needed. | HIGH |

**Why NOT an external DAG library:** gonum is a massive scientific computing library -- importing it for topological sort is overkill. Smaller libraries like gammazero/toposort work but add a dependency for ~80 lines of code. Puppet/Chef/Ansible all implement their own DAG resolution. This is core domain logic that should be owned, not imported.

## Alternatives Considered

| Category | Recommended | Alternative | Why Not |
|----------|-------------|-------------|---------|
| DB driver | pgx v5 | lib/pq | Maintenance mode, slower (text protocol), maintainers recommend pgx |
| DB driver | pgx v5 | GORM | ORM hides queries, adds magic. This project values transparency. |
| DB driver | pgx v5 | sqlx | Adds convenience but masks query structure. pgx native is faster. |
| Logging | slog (stdlib) | zerolog | Faster but adds dependency for negligible benefit in this workload |
| Logging | slog (stdlib) | zap | Same -- external dependency for marginal gain |
| CLI | cobra | urfave/cli | urfave is better for simple CLIs, cobra handles 10+ subcommands |
| CLI | cobra | stdlib flag | Too primitive for nested subcommands |
| REST | grpc-gateway | Connect-Go | Connect is better gRPC but grpc-gateway gives annotation-based REST for free |
| REST | grpc-gateway | chi + hand-written handlers | Duplicates logic already in proto definitions |
| YAML | go.yaml.in/yaml/v3 | gopkg.in/yaml.v3 | Archived/unmaintained (functional fallback if import issues) |
| Config | yaml.v3 direct | viper | Viper is for app config, not user-defined desired-state data |
| mTLS | crypto/tls stdlib | go-spiffe | Unnecessary infrastructure dependency for self-managed certs |
| DAG | Custom ~100 LOC | gonum/graph | Massive dependency for trivial algorithm |
| DAG | Custom ~100 LOC | gammazero/toposort | Dependency for ~80 lines of owned code |
| Proto tooling | buf | raw protoc | Error-prone CLI flags, no linting, no managed mode |
| Web UI | React SPA (embedded) | templ + htmx | Complex admin dashboards need client-side state management |
| Testing | testify | gomock | Extra code generation step, more ceremony |

## Installation

```bash
# Core dependencies
go get google.golang.org/grpc@latest
go get google.golang.org/protobuf@latest
go get github.com/grpc-ecosystem/grpc-gateway/v2@latest
go get github.com/grpc-ecosystem/go-grpc-middleware/v2@latest
go get github.com/jackc/pgx/v5@latest
go get github.com/golang-migrate/migrate/v4@latest
go get github.com/spf13/cobra@latest
go get github.com/prometheus/client_golang@latest
go get github.com/fsnotify/fsnotify@latest
go get go.yaml.in/yaml/v3@latest

# x/ packages
go get golang.org/x/crypto@latest
go get golang.org/x/sync@latest

# Dev / test dependencies
go get github.com/stretchr/testify@latest
go get github.com/testcontainers/testcontainers-go@latest

# Build tools (Go 1.24+ tool directive or go install)
go install github.com/bufbuild/buf/cmd/buf@latest
go install github.com/golang-migrate/migrate/v4/cmd/migrate@latest
```

## Version Pinning Summary

| Package | Minimum Version | Notes |
|---------|----------------|-------|
| Go | 1.24.13 | Latest patch with security fixes |
| google.golang.org/grpc | 1.70.0 | Feb 2026 releases available |
| jackc/pgx/v5 | 5.8.0 | Latest stable |
| golang-migrate/migrate/v4 | 4.19.1 | Nov 2025 release |
| spf13/cobra | 1.8.1 | Latest stable, v2 not released |
| prometheus/client_golang | 1.21.0 | Requires Go 1.23+ |
| fsnotify/fsnotify | 1.8.0 | Latest stable |
| grpc-gateway/v2 | 2.27.3 | Oct 2025 release |
| stretchr/testify | 1.9.0 | Aug 2025 release |
| go.yaml.in/yaml/v3 | 3.0.0 | Active fork of archived gopkg.in/yaml.v3 |

## Sources

- [grpc-go releases](https://github.com/grpc/grpc-go/releases) - gRPC version info
- [grpc-go package](https://pkg.go.dev/google.golang.org/grpc) - Official documentation
- [gRPC Services in Go: Basics to Production](https://dasroot.net/posts/2026/02/grpc-services-go-basics-production/) - 2026 best practices
- [gRPC mTLS examples](https://pkg.go.dev/google.golang.org/grpc/examples/features/encryption/mTLS) - Official mTLS example
- [jackc/pgx GitHub](https://github.com/jackc/pgx) - pgx driver
- [pgx vs lib/pq](https://preslav.me/2022/05/13/pq-or-pgx-choosing-the-right-postgresql-golang-driver/) - Driver comparison
- [GitLab: Recommend pgx over lib/pq](https://gitlab.com/gitlab-org/gitlab/-/merge_requests/49135) - Industry consensus
- [golang-migrate releases](https://github.com/golang-migrate/migrate/releases) - Migration tool versions
- [grpc-gateway GitHub](https://github.com/grpc-ecosystem/grpc-gateway) - REST gateway
- [grpc-gateway REST transcoding](https://oneuptime.com/blog/post/2026-01-08-grpc-gateway-rest-transcoding/view) - 2026 guide
- [go-grpc-middleware](https://github.com/grpc-ecosystem/go-grpc-middleware) - Interceptor library
- [Go 1.24 release notes](https://go.dev/doc/go1.24) - Go version features
- [Go release history](https://go.dev/doc/devel/release) - Go patch versions
- [spf13/cobra](https://github.com/spf13/cobra) - CLI framework
- [Go CLI comparison](https://github.com/gschauer/go-cli-comparison) - Cobra vs alternatives
- [fsnotify](https://github.com/fsnotify/fsnotify) - File watcher
- [slog package](https://pkg.go.dev/log/slog) - Standard library logging
- [Go logging in 2025](https://uptrace.dev/blog/golang-logging) - Logging comparison
- [prometheus/client_golang](https://github.com/prometheus/client_golang) - Metrics client
- [go.yaml.in/yaml/v3 migration](https://github.com/go-task/task/issues/2171) - YAML library status
- [gopkg.in/yaml.v3 unmaintained](https://github.com/goharbor/terraform-provider-harbor/issues/562) - Migration discussion
- [golang.org/x/crypto/ssh](https://pkg.go.dev/golang.org/x/crypto/ssh) - SSH library
- [golang.org/x/crypto/bcrypt](https://pkg.go.dev/golang.org/x/crypto/bcrypt) - Bcrypt package
- [buf build tool](https://github.com/bufbuild/buf) - Proto tooling
- [buf code generation](https://buf.build/docs/generate/) - buf generate docs
- [Go embed SPA pattern](https://bindplane.com/blog/embed-react-in-golang) - Embedding frontend
- [Go ServeMux vs chi](https://www.calhoun.io/go-servemux-vs-chi/) - Router comparison
- [testify GitHub](https://github.com/stretchr/testify) - Testing library
- [Testing in Go 2026](https://dasroot.net/posts/2026/02/testing-in-go-frameworks-tools-standards/) - Testing best practices
- [Go ecosystem 2025](https://blog.jetbrains.com/go/2025/11/10/go-language-trends-ecosystem-2025/) - JetBrains ecosystem survey
