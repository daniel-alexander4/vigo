# Phase 3: Operations - Research

**Researched:** 2026-03-06
**Domain:** CLI tooling, REST API, compliance computation, Prometheus metrics, webhook dispatch, secret rotation, TLS hot reload
**Confidence:** HIGH

## Summary

Phase 3 builds the operational layer on top of the existing server and agent foundation. The codebase already has: gRPC proto definitions with grpc-gateway annotations for all admin RPCs, sqlc query layer, DB tables for webhooks/package_audit/secret_rotation_events/plans/plan_results/file_snapshots, gvctl CLI stub with Cobra subcommands (all printing "not yet implemented"), and a REST gateway proxying to the gRPC backend via mTLS.

The work decomposes into five streams: (1) implementing the IsgovAdmin gRPC handlers that the gateway proxies to, plus new sqlc queries for webhooks/packages/compliance; (2) wiring the gvctl CLI stubs to make HTTP calls to the REST gateway; (3) adding Prometheus metrics via client_golang; (4) building the webhook dispatcher with HMAC-SHA256 signing; and (5) implementing the isopass IsopassProvider and secret rotation watcher with TLS hot reload.

**Primary recommendation:** Implement admin gRPC handlers first (they feed both gvctl and the web UI), then gvctl CLI, then compliance/metrics/webhooks in parallel, then isopass integration last (most self-contained).

<phase_requirements>
## Phase Requirements

| ID | Description | Research Support |
|----|-------------|-----------------|
| CLI-01 | `gvctl approved add/list/revoke` -- manage approved patterns | Admin gRPC handlers already defined in proto; sqlc queries for CRUD exist; CLI stubs exist in cli/main.go |
| CLI-02 | `gvctl clients list/show/revoke` -- manage enrolled clients | ListClients/GetClient/RevokeClient RPCs in proto; sqlc queries exist; CLI stubs exist |
| CLI-03 | `gvctl push <host> / --all` -- force immediate check-in | ForcePush RPC in proto; need DB flag table or column for force-push queueing |
| CLI-04 | `gvctl runs list/show` -- view run history and detail | ListRuns/GetRun RPCs in proto; sqlc queries exist (ListRunsByClient, GetRunByID, ListRunResults) |
| CLI-05 | `gvctl tokens generate` -- generate token and print bootstrap curl command | GenerateToken RPC in proto; CLI stub exists |
| CLI-06 | `gvctl webhooks add/list/delete` -- manage webhook subscriptions | Need new proto RPCs + grpc-gateway annotations; webhooks table exists; need sqlc queries |
| CLI-07 | `gvctl preview <host> / --all / --env` -- trigger dry-run | DryRun RPC in proto; plans/plan_results tables exist; need server-side planner |
| CLI-08 | `gvctl rollback <host> --run/--file` -- restore file snapshots | Need Rollback RPC in proto; file_snapshots table exists with sqlc queries |
| CLI-09 | `gvctl packages list/drift` -- package version audit | Need new proto RPCs; package_audit table exists; need sqlc queries |
| CLI-10 | `gvctl env promote` -- promote env_var overrides between tiers | Need new migration for env_promotions table; new proto RPCs |
| CLI-11 | gvctl talks to server REST API, not gRPC directly | grpc-gateway already set up; gvctl uses net/http to hit REST endpoints |
| OBS-01 | Compliance status per client: compliant/drifted/failing/stale/unknown | Computed from runs table + last_seen on clients; new server/compliance/ package |
| OBS-02 | Compliance API: summary, per-client status, 30-day history | New proto RPCs with grpc-gateway annotations for /api/v1/compliance/* |
| OBS-03 | Prometheus metrics: clients_total, clients_active, checkins_total, run_duration_seconds, drift_detected_total, module_actions_total, compliance_status, compliance_pct, time_since_checkin_seconds | prometheus/client_golang; register on HTTP mux at /metrics |
| OBS-04 | Webhook events: run.success/failure/partial, drift.detected, client.enrolled/revoked, plan.created/changes_detected, rollback.success/failure, secret.rotated, compliance.degraded, package.version_drift, node.stale | server/webhook/ package with dispatcher; webhooks table for subscriptions |
| OBS-05 | Webhook payloads signed with `X-Isogov-Signature: sha256=<hmac>` | HMAC-SHA256 using crypto/hmac; secret stored per-webhook |
| SECR-08 | Server polls isopass for version changes on watched secret paths at configurable interval | IsopassProvider using HTTP client to isopass REST API; watcher polling loop |
| SECR-09 | Secret rotation triggers immediate force-run for all affected clients | Watcher identifies which client configs reference rotated secret paths; queues force-push |
| SECR-10 | Server TLS cert reloads without restart via crypto/tls GetCertificate callback | tls.Config.GetCertificate callback that calls isopass on each handshake (with cache) |
</phase_requirements>

## Standard Stack

### Core
| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| github.com/prometheus/client_golang | v1.20+ | Prometheus metrics exposition | Official Go client; used by all Go services exposing Prometheus metrics |
| github.com/spf13/cobra | v1.10+ (already in go.mod) | CLI framework for gvctl | Already used; standard Go CLI framework |
| net/http (stdlib) | Go 1.25 | gvctl HTTP client for REST API calls | No external dependency needed; encoding/json for request/response marshaling |
| crypto/hmac + crypto/sha256 (stdlib) | Go 1.25 | Webhook HMAC-SHA256 signing | Standard library; no external dependency |
| crypto/tls (stdlib) | Go 1.25 | TLS hot reload via GetCertificate | Standard library; supports dynamic cert loading |

### Supporting
| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| github.com/jackc/pgx/v5 (already in go.mod) | v5.8 | DB queries for webhooks/compliance/packages | Already used for all DB access |
| encoding/json (stdlib) | Go 1.25 | Webhook payload marshaling | Standard library |
| time (stdlib) | Go 1.25 | Ticker-based polling for secret watcher, stale detection | Standard library |

### Alternatives Considered
| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| net/http for gvctl | go-resty | Unnecessary dependency for simple REST calls; net/http + json is sufficient |
| Custom webhook retry | Watermill/NATS | Overkill for simple HTTP POST + retry; in-memory queue with goroutines is fine |

**Installation:**
```bash
go get github.com/prometheus/client_golang@latest
```

## Architecture Patterns

### Recommended Project Structure
```
server/
  compliance/       # Compliance status computation (OBS-01, OBS-02)
    compliance.go   # ComputeStatus(), Summary(), History()
  planner/          # Dry-run plan computation (CLI-07)
    planner.go      # ComputePlan() -- compares desired vs current state
  watcher/          # Secret rotation watcher (SECR-08, SECR-09)
    watcher.go      # Poll loop, version tracking, force-push queueing
  webhook/          # Webhook dispatcher (OBS-04, OBS-05)
    dispatcher.go   # Dispatch(), sign(), retry logic
  metrics/          # Prometheus metrics registration (OBS-03)
    metrics.go      # All metric definitions + collector
  secrets/
    isopass.go      # IsopassProvider (SECR-02)
  grpc/
    admin.go        # All IsgovAdmin handler implementations
cli/
  main.go           # Root command (exists, needs subcommand wiring)
  client.go         # Shared HTTP client helper for REST API calls
  approved.go       # gvctl approved add/list/revoke
  clients.go        # gvctl clients list/show/revoke
  push.go           # gvctl push
  runs.go           # gvctl runs list/show
  tokens.go         # gvctl tokens generate
  webhooks.go       # gvctl webhooks add/list/delete
  preview.go        # gvctl preview
  rollback.go       # gvctl rollback
  packages.go       # gvctl packages list/drift
  env.go            # gvctl env promote
```

### Pattern 1: Admin gRPC Handler Implementation
**What:** Each IsgovAdmin RPC gets a concrete implementation on the AdminService struct, replacing the UnimplementedIsgovAdminServer defaults.
**When to use:** For all CLI-01 through CLI-10 server-side handlers.
**Example:**
```go
// server/grpc/admin.go
func (s *AdminService) AddApprovedPattern(ctx context.Context, req *pb.AddApprovedPatternRequest) (*pb.AddApprovedPatternResponse, error) {
    tokenHash, err := bcrypt.GenerateFromPassword([]byte(req.Token), bcrypt.DefaultCost)
    if err != nil {
        return nil, status.Errorf(codes.Internal, "hashing token: %v", err)
    }
    pattern, err := s.queries.CreateApprovedPattern(ctx, db.CreateApprovedPatternParams{
        Pattern:   req.Pattern,
        TokenHash: string(tokenHash),
    })
    if err != nil {
        return nil, status.Errorf(codes.Internal, "creating pattern: %v", err)
    }
    return &pb.AddApprovedPatternResponse{
        Id:        pattern.ID.String(),
        Pattern:   pattern.Pattern,
        CreatedAt: timestamppb.New(pattern.CreatedAt),
    }, nil
}
```

### Pattern 2: gvctl CLI to REST API
**What:** Each gvctl subcommand makes HTTP requests to the grpc-gateway REST endpoints.
**When to use:** All CLI commands (CLI-11 mandates REST, not direct gRPC).
**Example:**
```go
// cli/client.go
type apiClient struct {
    baseURL    string
    httpClient *http.Client
}

func (c *apiClient) get(path string, result any) error {
    resp, err := c.httpClient.Get(c.baseURL + path)
    if err != nil {
        return fmt.Errorf("request failed: %w", err)
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        body, _ := io.ReadAll(resp.Body)
        return fmt.Errorf("server returned %d: %s", resp.StatusCode, string(body))
    }
    return json.NewDecoder(resp.Body).Decode(result)
}

func (c *apiClient) post(path string, body, result any) error {
    data, err := json.Marshal(body)
    if err != nil {
        return err
    }
    resp, err := c.httpClient.Post(c.baseURL+path, "application/json", bytes.NewReader(data))
    // ... similar error handling
}
```

### Pattern 3: Compliance Status Computation
**What:** Query-based compliance computation from existing run data.
**When to use:** For OBS-01, OBS-02.
**Example:**
```go
// server/compliance/compliance.go
func ComputeStatus(lastRun *db.Run, lastSeen time.Time, baseInterval time.Duration) string {
    staleThreshold := 2 * baseInterval
    if time.Since(lastSeen) > staleThreshold {
        return "stale"
    }
    if lastRun == nil {
        return "unknown"
    }
    switch lastRun.Status {
    case "success":
        if lastRun.ChangedCount > 0 {
            return "drifted"  // auto-remediated
        }
        return "compliant"
    case "failure":
        return "failing"
    case "partial":
        return "failing"
    default:
        return "unknown"
    }
}
```

### Pattern 4: Webhook Dispatcher with HMAC
**What:** Fire-and-forget HTTP POST with HMAC signing and retry.
**When to use:** For OBS-04, OBS-05.
**Example:**
```go
// server/webhook/dispatcher.go
func (d *Dispatcher) Dispatch(event string, payload any) {
    webhooks, _ := d.queries.ListActiveWebhooksByEvent(ctx, event)
    for _, wh := range webhooks {
        go d.deliver(wh, event, payload)
    }
}

func (d *Dispatcher) deliver(wh db.Webhook, event string, payload any) {
    body, _ := json.Marshal(payload)
    sig := sign(wh.Secret, body)
    for attempt := 0; attempt <= d.maxRetries; attempt++ {
        req, _ := http.NewRequest("POST", wh.Url, bytes.NewReader(body))
        req.Header.Set("Content-Type", "application/json")
        req.Header.Set("X-Isogov-Signature", "sha256="+sig)
        req.Header.Set("X-Isogov-Event", event)
        resp, err := d.httpClient.Do(req)
        if err == nil && resp.StatusCode < 300 {
            return
        }
        time.Sleep(time.Duration(attempt+1) * 5 * time.Second) // linear backoff
    }
}

func sign(secret string, payload []byte) string {
    mac := hmac.New(sha256.New, []byte(secret))
    mac.Write(payload)
    return hex.EncodeToString(mac.Sum(nil))
}
```

### Pattern 5: IsopassProvider
**What:** HTTP client that calls isopass REST API to retrieve secrets.
**When to use:** SECR-02, SECR-08, SECR-09.
**Key findings from isopass source:**

isopass is an HTTPS REST API (listening on port 8443) using SQLite. Authentication uses:
1. **Service accounts** with bearer tokens -- best fit for Isogov server
2. **Client API** with bearer tokens -- alternative approach
3. Bearer token -> token exchange -> short-lived tokens for API calls

API endpoints used by IsopassProvider:
- `POST /api/auth/token` -- exchange bearer token for short-lived token
- `POST /api/auth/token/renew` -- renew short-lived token before expiry
- `POST /api/sa/search` (service account) or `POST /api/client/search` (client) -- search secrets by query/tags
- `GET /api/secrets/{id}` (user auth) -- get secret by ID with value
- `GET /api/secrets/{id}/versions` -- list secret versions (for rotation detection)

**Critical:** isopass uses client-scoped or service-account-scoped access. Secrets are organized by organizations and filtered by scopes. The search endpoint returns decrypted values directly.

```go
// server/secrets/isopass.go
type IsopassProvider struct {
    baseURL     string
    bearerToken string
    token       string // short-lived token from exchange
    tokenExpiry time.Time
    httpClient  *http.Client
    mu          sync.Mutex
}

func (p *IsopassProvider) Get(ctx context.Context, key string) (string, error) {
    if err := p.ensureToken(ctx); err != nil {
        return "", fmt.Errorf("isopass auth: %w", err)
    }
    // Search by secret name matching the key
    results, err := p.search(ctx, key)
    if err != nil {
        return "", err
    }
    if len(results) == 0 {
        return "", fmt.Errorf("secret %q not found in isopass", key)
    }
    return results[0].Value, nil
}
```

### Pattern 6: TLS Hot Reload
**What:** Use crypto/tls GetCertificate callback to reload certs without restart.
**When to use:** SECR-10.
**Example:**
```go
// Replace static cert loading with dynamic callback
tlsCfg := &tls.Config{
    GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
        certPEM, err := provider.GetBytes(ctx, "isogov/tls/cert")
        if err != nil {
            return cachedCert, nil // fall back to cached on error
        }
        keyPEM, err := provider.GetBytes(ctx, "isogov/tls/key")
        if err != nil {
            return cachedCert, nil
        }
        cert, err := tls.X509KeyPair(certPEM, keyPEM)
        if err != nil {
            return cachedCert, nil
        }
        cachedCert = &cert
        return &cert, nil
    },
    ClientAuth: tls.RequireAndVerifyClientCert,
    ClientCAs:  caPool,
    MinVersion: tls.VersionTLS13,
}
```

**Important optimization:** Do NOT call isopass on every TLS handshake. Cache the cert and only refresh when the watcher detects a version change, or on a timer (e.g., every 5 minutes). The GetCertificate callback should serve from an atomic cache that the watcher updates.

### Anti-Patterns to Avoid
- **Direct gRPC from gvctl:** CLI-11 mandates REST API only. Use net/http to hit grpc-gateway endpoints.
- **Polling isopass on every TLS handshake:** Cache certs; update cache only on rotation detection or timer.
- **Synchronous webhook delivery:** Fire webhooks in goroutines; never block the main request path.
- **Computing compliance on every API call:** Pre-compute and cache compliance status; update on run completion and periodic staleness checks.
- **Storing webhook secrets in plaintext config:** Webhook secrets should use the `secret:` prefix convention.

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| Prometheus metrics | Custom metrics HTTP endpoint | prometheus/client_golang + promhttp | Standard format, scraping compatibility, metric types (Counter, Gauge, Histogram) |
| HMAC signing | Custom signing scheme | crypto/hmac + crypto/sha256 (stdlib) | Standard, audited implementation |
| CLI framework | Custom arg parser | spf13/cobra (already used) | Already in project; handles subcommands, flags, help text |
| JSON HTTP client | Custom HTTP wrapper | net/http + encoding/json (stdlib) | Simple REST calls don't need resty/go-retryablehttp |
| Secret rotation polling | Filesystem watcher for secrets | HTTP polling of isopass versions API | isopass is an HTTP service, not filesystem-based |

**Key insight:** The heaviest implementation work is in the gRPC admin handlers and gvctl CLI wiring -- these are plumbing, not complex algorithms. The compliance computation, webhook dispatcher, and metrics are all straightforward once the data layer is in place.

## Common Pitfalls

### Pitfall 1: Proto Changes Require Regeneration
**What goes wrong:** Adding new RPCs to the proto (webhooks, compliance, packages, rollback, env promote) without running `make proto` leaves generated code out of sync.
**Why it happens:** The proto file needs new message types and RPC definitions for webhook/compliance/package/rollback/env operations that don't exist yet.
**How to avoid:** Add ALL needed proto RPCs in one batch, then `make proto`, then implement handlers.
**Warning signs:** Compile errors in grpc handler files, missing gateway registrations.

### Pitfall 2: Missing sqlc Queries
**What goes wrong:** The DB tables for webhooks, package_audit, secret_rotation_events exist (migrations are done) but have NO sqlc query files. Trying to use `db.Queries` methods for these tables will fail.
**Why it happens:** Phase 1 created migrations but deferred sqlc queries to Phase 3.
**How to avoid:** Create `server/db/queries/webhooks.sql`, `server/db/queries/package_audit.sql`, `server/db/queries/secret_rotation_events.sql`, then `make proto` (which should also regenerate sqlc).
**Warning signs:** No `*_sql.go` files for webhooks/package_audit/secret_rotation tables.

### Pitfall 3: ForcePush is Pull-Based
**What goes wrong:** Attempting to implement ForcePush as an outbound connection to the agent.
**Why it happens:** "push" implies server-initiated, but agents are behind firewalls/NAT.
**How to avoid:** ForcePush sets a DB flag (`force_push = true` on the clients table or a separate queue table). Agent picks it up on next check-in (which should be soon given jitter). The CheckIn handler checks the flag and returns immediately with the full module list.
**Warning signs:** Server trying to connect to agent IPs.

### Pitfall 4: gvctl Token Generation vs Pattern Addition
**What goes wrong:** Confusing `gvctl tokens generate` (which just generates a random token) with `gvctl approved add` (which creates a pattern with that token).
**Why it happens:** The handoff spec shows `gvctl tokens generate --pattern` but REQUIREMENTS.md separates them.
**How to avoid:** `gvctl tokens generate` outputs a random token. `gvctl approved add <pattern> --token <token>` associates it with a pattern. These are separate commands per CLI-01 and CLI-05.
**Warning signs:** Token generation trying to create patterns, or patterns without tokens.

### Pitfall 5: Compliance Staleness Threshold
**What goes wrong:** Hardcoding the stale threshold instead of deriving from server config.
**Why it happens:** The threshold is `2 * base_interval` which is configurable.
**How to avoid:** Read `checkin.base_interval` from server config and compute `2x` dynamically.
**Warning signs:** Stale detection not matching configured check-in intervals.

### Pitfall 6: isopass Authentication Flow
**What goes wrong:** Trying to use user authentication (SSH challenge-response) from the isogov server.
**Why it happens:** isopass has multiple auth methods -- users, clients, service accounts.
**How to avoid:** Create a **service account** in isopass for the isogov server. Use bearer token auth: exchange the static bearer token for a short-lived token via `POST /api/auth/token`, then use that token for `POST /api/sa/search` to fetch secrets. Renew tokens via `POST /api/auth/token/renew` before expiry.
**Warning signs:** Trying to do SSH challenge auth from a server process.

### Pitfall 7: Webhook Event Filtering
**What goes wrong:** Sending all events to all webhooks regardless of subscription.
**Why it happens:** The webhooks table has an `events TEXT[]` column for subscription filtering, but it's easy to skip the filter.
**How to avoid:** Query `ListActiveWebhooksByEvent` which filters by the event array using `$1 = ANY(events)`.
**Warning signs:** Webhook receivers getting events they didn't subscribe to.

## Code Examples

### Prometheus Metrics Registration
```go
// server/metrics/metrics.go
package metrics

import (
    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promauto"
)

var (
    ClientsTotal = promauto.NewGauge(prometheus.GaugeOpts{
        Name: "isogov_clients_total",
        Help: "Total number of enrolled clients",
    })
    ClientsActive = promauto.NewGauge(prometheus.GaugeOpts{
        Name: "isogov_clients_active_total",
        Help: "Number of clients seen in the last hour",
    })
    CheckinsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
        Name: "isogov_checkins_total",
        Help: "Total check-ins by status",
    }, []string{"status"})
    RunDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
        Name:    "isogov_run_duration_seconds",
        Help:    "Duration of convergence runs",
        Buckets: prometheus.DefBuckets,
    }, []string{"client"})
    DriftDetected = promauto.NewCounterVec(prometheus.CounterOpts{
        Name: "isogov_drift_detected_total",
        Help: "Number of drift detection events",
    }, []string{"client", "module"})
    ModuleActions = promauto.NewCounterVec(prometheus.CounterOpts{
        Name: "isogov_module_actions_total",
        Help: "Module actions by type and action",
    }, []string{"type", "action"})
    ComplianceStatus = promauto.NewGaugeVec(prometheus.GaugeOpts{
        Name: "isogov_compliance_status",
        Help: "Compliance status per client (1=active)",
    }, []string{"client", "status"})
    CompliancePct = promauto.NewGauge(prometheus.GaugeOpts{
        Name: "isogov_compliance_pct",
        Help: "Fleet-wide compliance percentage",
    })
    TimeSinceCheckin = promauto.NewGaugeVec(prometheus.GaugeOpts{
        Name: "isogov_time_since_checkin_seconds",
        Help: "Seconds since last check-in per client",
    }, []string{"client"})
)
```

### Exposing /metrics Endpoint
```go
// In server/main.go, add to the HTTP mux:
import "github.com/prometheus/client_golang/prometheus/promhttp"

mux.Handle("/metrics", promhttp.Handler())
```

### Secret Rotation Watcher
```go
// server/watcher/watcher.go
type Watcher struct {
    provider     secrets.Provider
    queries      *db.Queries
    configStore  *config.ConfigStore
    pollInterval time.Duration
    versions     map[string]int // path -> last known version
}

func (w *Watcher) Run(ctx context.Context) error {
    ticker := time.NewTicker(w.pollInterval)
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        case <-ticker.C:
            w.checkRotations(ctx)
        }
    }
}

func (w *Watcher) checkRotations(ctx context.Context) {
    for path, lastVersion := range w.versions {
        currentVersion, err := w.getSecretVersion(ctx, path)
        if err != nil {
            slog.Error("checking secret version", "path", path, "error", err)
            continue
        }
        if currentVersion > lastVersion {
            slog.Info("secret rotated", "path", path, "old", lastVersion, "new", currentVersion)
            w.versions[path] = currentVersion
            affectedClients := w.configStore.ClientsReferencingSecret(path)
            for _, clientID := range affectedClients {
                w.queries.SetForcePush(ctx, clientID)
            }
            w.queries.CreateSecretRotationEvent(ctx, db.CreateSecretRotationEventParams{
                SecretPath:      path,
                OldVersion:      strconv.Itoa(lastVersion),
                NewVersion:      strconv.Itoa(currentVersion),
                AffectedClients: int32(len(affectedClients)),
            })
        }
    }
}
```

### gvctl HTTP Client Pattern
```go
// cli/client.go
func newAPIClient() *apiClient {
    return &apiClient{
        baseURL:    serverURL, // from --server flag
        httpClient: &http.Client{Timeout: 30 * time.Second},
    }
}

// Usage in a command:
// cli/approved.go
var approvedAddCmd = &cobra.Command{
    Use:   "add <pattern>",
    Short: "Add an approved pattern with a one-time token",
    Args:  cobra.ExactArgs(1),
    RunE: func(cmd *cobra.Command, args []string) error {
        token, _ := cmd.Flags().GetString("token")
        client := newAPIClient()
        var resp pb.AddApprovedPatternResponse
        err := client.post("/api/v1/approved", map[string]string{
            "pattern": args[0],
            "token":   token,
        }, &resp)
        if err != nil {
            return err
        }
        fmt.Printf("Created pattern %s (id: %s)\n", resp.Pattern, resp.Id)
        return nil
    },
}
```

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| prometheus.MustRegister() globals | promauto.NewXxx() or custom registry | client_golang v1.12+ | Cleaner metric definition, less boilerplate |
| cobra.Command in init() | Structured command trees | cobra v1.6+ | Already used in this project; CLI stubs ready |
| Static TLS cert loading | GetCertificate callback | Go 1.12+ | Enables cert rotation without restart |

**Deprecated/outdated:**
- None relevant -- all stdlib APIs being used are current and stable.

## Open Questions

1. **ForcePush DB mechanism**
   - What we know: The handoff says "queue a force-run flag in the DB that the next check-in picks up"
   - What's unclear: Whether to add a `force_push BOOLEAN` column to clients table or create a separate `force_push_queue` table
   - Recommendation: Add `force_push BOOLEAN DEFAULT false` column to clients table (simplest). CheckIn handler checks it, resets to false after processing. Needs a new migration.

2. **Compliance history storage**
   - What we know: Need 30-day daily compliance history for charting
   - What's unclear: Whether to compute on-the-fly from runs or store daily snapshots
   - Recommendation: Create a `compliance_history` table with daily snapshots computed by a periodic goroutine. Avoids expensive run table scans on every API call.

3. **Env promotion mechanism**
   - What we know: `gvctl env promote` copies staging env_vars to production
   - What's unclear: Since config is filesystem-only (no API writes), this must be a read-only operation that shows a diff or writes to a promotions audit table without modifying files
   - Recommendation: The promote command should compute the diff and store it in an `env_promotions` table as an audit record. The actual file change is done by the operator. This aligns with the "single config path" principle.

4. **Proto additions needed**
   - What we know: Current proto has RPCs for approved/clients/push/dryrun/runs/tokens
   - What's missing: Webhook CRUD RPCs, Compliance RPCs, Package audit RPCs, Rollback RPC, Env promote RPC
   - Recommendation: Add all missing RPCs with grpc-gateway annotations in one proto update. The proto is the contract between gvctl and the server.

5. **isopass secret name mapping**
   - What we know: Isogov uses paths like `isogov/db/dsn`; isopass has secrets with `name` field, organized by organizations
   - What's unclear: How the isogov key path maps to isopass secret name/tags
   - Recommendation: Use the key path as the search query in `POST /api/sa/search` or `POST /api/client/search`. Tag secrets in isopass with the key path. The IsopassProvider searches by name matching the key path.

## Validation Architecture

### Test Framework
| Property | Value |
|----------|-------|
| Framework | Go testing + testify (already in go.mod) |
| Config file | Makefile `test` target |
| Quick run command | `go test ./server/... -count=1 -short` |
| Full suite command | `make test` |

### Phase Requirements -> Test Map
| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|-------------|
| CLI-01 | approved add/list/revoke via REST | integration | `go test ./server/grpc/... -run TestAdminApproved -v` | No -- Wave 0 |
| CLI-02 | clients list/show/revoke via REST | integration | `go test ./server/grpc/... -run TestAdminClients -v` | No -- Wave 0 |
| CLI-03 | force push sets DB flag | unit | `go test ./server/grpc/... -run TestForcePush -v` | No -- Wave 0 |
| CLI-04 | runs list/show via REST | integration | `go test ./server/grpc/... -run TestAdminRuns -v` | No -- Wave 0 |
| CLI-05 | token generation | unit | `go test ./server/grpc/... -run TestGenerateToken -v` | No -- Wave 0 |
| CLI-06 | webhook CRUD | unit | `go test ./server/webhook/... -run TestWebhookCRUD -v` | No -- Wave 0 |
| CLI-07 | dry-run preview | integration | `go test ./server/planner/... -run TestDryRun -v` | No -- Wave 0 |
| CLI-08 | rollback file snapshots | integration | `go test ./server/grpc/... -run TestRollback -v` | No -- Wave 0 |
| CLI-09 | package audit list/drift | unit | `go test ./server/grpc/... -run TestPackageAudit -v` | No -- Wave 0 |
| CLI-10 | env promote | unit | `go test ./server/grpc/... -run TestEnvPromote -v` | No -- Wave 0 |
| CLI-11 | gvctl uses REST not gRPC | integration | `go test ./cli/... -run TestCLI -v` | No -- Wave 0 |
| OBS-01 | compliance status computation | unit | `go test ./server/compliance/... -run TestComputeStatus -v` | No -- Wave 0 |
| OBS-02 | compliance API endpoints | integration | `go test ./server/grpc/... -run TestComplianceAPI -v` | No -- Wave 0 |
| OBS-03 | Prometheus metrics exposed | integration | `go test ./server/metrics/... -run TestMetrics -v` | No -- Wave 0 |
| OBS-04 | webhook event dispatch | unit | `go test ./server/webhook/... -run TestDispatch -v` | No -- Wave 0 |
| OBS-05 | webhook HMAC signing | unit | `go test ./server/webhook/... -run TestHMACSigning -v` | No -- Wave 0 |
| SECR-08 | secret version polling | unit | `go test ./server/watcher/... -run TestWatcher -v` | No -- Wave 0 |
| SECR-09 | rotation triggers force-run | unit | `go test ./server/watcher/... -run TestRotationTrigger -v` | No -- Wave 0 |
| SECR-10 | TLS hot reload | unit | `go test ./server/grpc/... -run TestTLSHotReload -v` | No -- Wave 0 |

### Sampling Rate
- **Per task commit:** `go test ./server/... ./cli/... -count=1 -short`
- **Per wave merge:** `make test`
- **Phase gate:** Full suite green before `/gsd:verify-work`

### Wave 0 Gaps
- [ ] `server/db/queries/webhooks.sql` -- sqlc queries for webhooks table
- [ ] `server/db/queries/package_audit.sql` -- sqlc queries for package_audit table
- [ ] `server/db/queries/secret_rotation_events.sql` -- sqlc queries for secret_rotation_events table
- [ ] `server/db/queries/plans.sql` -- sqlc queries for plans and plan_results tables
- [ ] Proto additions for Rollback, Webhook CRUD, Compliance, Package audit, Env promote RPCs
- [ ] New migration for `force_push` column on clients table
- [ ] New migration for `compliance_history` table
- [ ] New migration for `env_promotions` table
- [ ] `go get github.com/prometheus/client_golang@latest`

## Sources

### Primary (HIGH confidence)
- isopass source code at `~/repos/isopass` -- read handlers.go, types.go, server.go, auth.go, config.go, db.go, main.go
- isogov-handoff.md -- full specification for CLI, webhooks, compliance, metrics, secret rotation, rollback
- Existing isogov codebase -- proto definitions, sqlc queries, server main, CLI stubs, config types, DB migrations

### Secondary (MEDIUM confidence)
- [prometheus/client_golang documentation](https://pkg.go.dev/github.com/prometheus/client_golang/prometheus) -- metrics API, promauto, promhttp
- [Prometheus Go instrumentation guide](https://prometheus.io/docs/guides/go-application/) -- best practices
- [spf13/cobra documentation](https://pkg.go.dev/github.com/spf13/cobra) -- CLI framework patterns

### Tertiary (LOW confidence)
- None -- all findings verified against source code or official docs.

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH -- all libraries are already in go.mod or well-established (prometheus/client_golang)
- Architecture: HIGH -- patterns derived from existing codebase structure and handoff spec
- Pitfalls: HIGH -- identified from reading actual code (missing sqlc queries, isopass auth flow, force-push semantics)
- isopass integration: HIGH -- read full source code, understand auth flow, API shape, and data model

**Research date:** 2026-03-06
**Valid until:** 2026-04-06 (stable APIs, Go stdlib)
