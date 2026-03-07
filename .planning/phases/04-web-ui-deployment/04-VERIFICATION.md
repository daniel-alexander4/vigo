---
phase: 04-web-ui-deployment
verified: 2026-03-06T22:02:05Z
status: passed
score: 4/4 must-haves verified
re_verification:
  previous_status: gaps_found
  previous_score: 3/4
  gaps_closed:
    - "Bootstrap handler wired into HTTP gateway (commit f7061dd)"
    - "Loop test arity fixed to match 5-arg signature (commit 9263ce1)"
  gaps_remaining: []
  regressions: []
---

# Phase 4: Web UI & Deployment Verification Report

**Phase Goal:** Operators can monitor the fleet through a web dashboard, and new nodes can be bootstrapped with a single curl command and kept up to date via agent self-update
**Verified:** 2026-03-06T22:02:05Z
**Status:** passed
**Re-verification:** Yes -- after gap closure (Plan 04-04)

## Goal Achievement

### Observable Truths

| # | Truth | Status | Evidence |
|---|-------|--------|----------|
| 1 | Web UI dashboard shows total clients, active count, recent failures, and drift rate -- with drill-down to individual client runs, module results, and drift history | VERIFIED | Dashboard handler fetches real DB data, renders 4 stat cards with HTMX 30s auto-refresh. Client detail page shows runs with expandable per-module results and drift history timeline. All 9 page templates are substantive HTML with data bindings. |
| 2 | Web UI provides filterable run log, read-only config viewer, dry-run plan viewer with color-coded actions, and compliance gauge with 30-day trend chart | VERIFIED | Runs page has status filter bar (All/Success/Failure/Partial). Config page reads raw YAML from disk via ConfigStore.Dir() and applies chroma syntax highlighting. Plans page color-codes actions (create=success/green, update=info/blue, delete=danger/red, skip=muted/gray). Compliance page has threshold-colored gauge, breakdown by status, and SVG sparkline from 30-day history. |
| 3 | `curl <server>/bootstrap?token=TOKEN \| bash` installs the agent, generates keypair, and enrolls the node | VERIFIED | server/bootstrap/bootstrap.go implements BootstrapScript, AgentLatest, AgentDownload (231 lines). server/api/gateway.go line 17 imports `server/bootstrap`, line 54 creates `bootstrap.NewHandler(serverAddr, caFile, agentDir, version)`, line 55 calls `bh.RegisterRoutes(mux)` BEFORE the `/api/` catch-all on line 58. All 8 bootstrap tests pass. Routes are reachable at runtime. Previously FAILED -- now VERIFIED after commit f7061dd. |
| 4 | Agent checks for newer binary versions on each check-in and self-updates after checksum verification | VERIFIED | agent/update/updater.go implements CheckForUpdate (HTTP GET to /api/v1/agent/latest) and Apply (download + SHA256 verify + atomic rename + exec restart). agent/checkin/loop.go line 205 calls checkSelfUpdate() after each successful convergence run. All 7 updater tests pass. All 11 agent/checkin tests pass including TestLoop_BackoffOnError_DoesNotCrash (line 277 now passes 5 args correctly). Previously had test regression -- now VERIFIED after commit 9263ce1. |

**Score:** 4/4 truths verified

### Required Artifacts

| Artifact | Expected | Status | Details |
|----------|----------|--------|---------|
| `ui/embed.go` | go:embed FS for templates and static assets | VERIFIED | 166 bytes, `//go:embed templates/* static/*` and `var Assets embed.FS` |
| `ui/render.go` | Template renderer with FuncMap | VERIFIED | 5281 bytes. NewRenderer with FuncMap (statusColor, sparklineSVG, formatTime, highlightYAML, truncUUID, dict, pct) |
| `ui/handler.go` | HTTP handlers for all UI pages | VERIFIED | 22706 bytes. DataSource interface, 10+ handler methods, full dbDataSource implementation |
| `ui/sparkline.go` | Server-side inline SVG sparkline generation | VERIFIED | 970 bytes, SparklineSVG function returns template.HTML |
| `ui/static/css/style.css` | Complete CSS with custom properties | VERIFIED | 14422 bytes |
| `ui/static/js/htmx.min.js` | Vendored HTMX 2.0.4 | VERIFIED | 50917 bytes |
| `ui/templates/base.html` | HTML5 layout with hx-boost | VERIFIED | 532 bytes |
| `ui/templates/pages/dashboard.html` | Dashboard with stat cards and auto-refresh | VERIFIED | 1668 bytes, hx-get="/ui/fragments/dashboard-stats" hx-trigger="every 30s" |
| `ui/templates/pages/compliance.html` | Compliance gauge with sparkline | VERIFIED | 2634 bytes |
| `ui/templates/pages/clients.html` | Client list with status filter | VERIFIED | 2124 bytes |
| `ui/templates/pages/client-detail.html` | Client detail with runs and facts | VERIFIED | 3860 bytes |
| `ui/templates/pages/runs.html` | Filterable run log | VERIFIED | 1760 bytes |
| `ui/templates/pages/run-detail.html` | Run detail with color-coded actions | VERIFIED | 2427 bytes |
| `ui/templates/pages/config.html` | Syntax-highlighted YAML viewer | VERIFIED | 448 bytes |
| `ui/templates/pages/plans.html` | Color-coded plan actions | VERIFIED | 2051 bytes |
| `server/api/gateway.go` | UI + bootstrap routes mounted on HTTP mux | VERIFIED | Imports ui and bootstrap packages, creates both handlers, registers routes before /api/ catch-all |
| `server/bootstrap/bootstrap.go` | Bootstrap script and agent distribution handlers | VERIFIED | 231 lines, BootstrapScript + AgentLatest + AgentDownload, RegisterRoutes on mux. Previously ORPHANED -- now WIRED via gateway.go (commit f7061dd). |
| `server/bootstrap/agent.go` | SHA256 checksum computation and caching | VERIFIED | checksumCache with modtime invalidation, used by bootstrap.go which is now wired |
| `agent/update/updater.go` | Agent self-update with checksum verification | VERIFIED | 4289 bytes. CheckForUpdate + Apply with SHA256 verify + atomic rename |
| `agent/update/updater_test.go` | Tests for self-update | VERIFIED | 5620 bytes, 7 tests, all passing |
| `agent/checkin/loop_test.go` | Loop test with correct arity | VERIFIED | Line 277: `Loop(ctx, client, executors, nil, nil)` -- 5 args matching signature. 11 tests pass. Previously BROKEN -- now fixed (commit 9263ce1). |
| `Makefile` | dist target for cross-compiling agent binaries | VERIFIED | VERSION variable, dist target for linux/darwin x amd64/arm64 |
| `server/main.go` | Full server wiring | VERIFIED | Passes queries, configStore, baseInterval, version, agentDir to api.NewGateway which handles bootstrap wiring internally |
| `server/config/loader.go` | ConfigStore.Dir() method | VERIFIED | `func (cs *ConfigStore) Dir() string` at line 65 |

### Key Link Verification

| From | To | Via | Status | Details |
|------|----|-----|--------|---------|
| server/api/gateway.go | bootstrap.Handler | import + RegisterRoutes call | WIRED | Line 17 imports `server/bootstrap`, line 54 creates NewHandler, line 55 calls RegisterRoutes(mux). Previously NOT_WIRED -- now WIRED (commit f7061dd). |
| server/api/gateway.go | ui.Handler | import + RegisterRoutes call | WIRED | Line 20 imports `ui`, line 47 creates NewHandler, line 51 calls RegisterRoutes(mux) |
| ui/handler.go | server/db.Queries | DataSource -> dbDataSource | WIRED | dbDataSource wraps db.Queries, calls ListClients, ListRunsByClient, GetClientByID, etc. |
| ui/handler.go | compliance.ComputeStatus | clientComplianceInfo helper | WIRED | Calls ComputeStatus at lines 375 and 385, Summary at line 497 |
| ui/templates/pages/dashboard.html | HTMX fragment | hx-get + hx-trigger | WIRED | hx-get="/ui/fragments/dashboard-stats" hx-trigger="every 30s" |
| agent/checkin/loop.go | agent/update.CheckForUpdate | checkSelfUpdate function | WIRED | Line 205 calls checkSelfUpdate, line 224 calls update.CheckForUpdate |
| agent/update/updater.go | /api/v1/agent/latest | HTTP GET for version info | WIRED | Constructs URL with serverAddr + "/api/v1/agent/latest?os=...&arch=..." |
| server/bootstrap/bootstrap.go | agent binary files on disk | http.ServeFile from agentDir | WIRED | AgentDownload uses BinaryPath and http.ServeFile |
| agent/checkin/loop_test.go | agent/checkin.Loop | function call with 5 args | WIRED | Line 277: `Loop(ctx, client, executors, nil, nil)` matches 5-param signature. Previously broken -- now WIRED (commit 9263ce1). |
| ui/handler.go ConfigData | raw YAML file on disk | os.ReadFile from configStore.Dir() | WIRED | filepath.Join(ds.configStore.Dir(), hostname+".yaml") at line 773 |

### Requirements Coverage

| Requirement | Source Plan | Description | Status | Evidence |
|-------------|------------|-------------|--------|----------|
| UI-01 | 04-01 | Dashboard: total clients, active in last hour, recent failures, drift rate | SATISFIED | Dashboard handler + template with 4 stat cards, HTMX auto-refresh |
| UI-02 | 04-03 | Clients view: table with hostname, IP, enrolled date, last seen, status badge | SATISFIED | Clients handler with status filter, template with full table |
| UI-03 | 04-03 | Client detail: last N runs, expandable per-module results, drift history chart | SATISFIED | ClientDetail handler fetches runs+results+facts, two-column template layout |
| UI-04 | 04-03 | Runs view: filterable run log across all clients | SATISFIED | Runs handler with status filter, cross-client run table |
| UI-05 | 04-03 | Config view: read-only display of client YAML config | SATISFIED | Config handler reads raw YAML via configStore.Dir(), syntax-highlighted |
| UI-06 | 04-03 | Plans view: dry-run plans with per-resource actions, color-coded by severity | SATISFIED | Plans handler with planActionColor mapping, color-coded badges |
| UI-07 | 04-01 | Compliance view: percentage gauge, breakdown donut, non-compliant table, 30-day trend | SATISFIED | Compliance handler with gauge, breakdown, sparkline from ListComplianceHistory |
| UI-08 | 04-01 | SPA served from server binary via go:embed | SATISFIED | ui/embed.go with //go:embed directive |
| AGNT-10 | 04-02, 04-04 | Agent checks for newer binary version on each check-in, self-updates after checksum verification | SATISFIED | update.CheckForUpdate + Apply with SHA256 verification, wired into daemon loop, all tests pass |
| INFRA-04 | 04-02, 04-04 | Bootstrap script at GET /bootstrap with server address + CA cert baked in | SATISFIED | BootstrapScript handler serves shell script with baked-in server addr, CA cert, and token; wired into HTTP mux via gateway.go |
| INFRA-05 | 04-02, 04-04 | Agent binary download endpoint: GET /api/v1/agent/download/{os}/{arch} | SATISFIED | AgentDownload handler serves pre-built binaries with OS/arch validation; wired into HTTP mux via gateway.go |

### Anti-Patterns Found

| File | Line | Pattern | Severity | Impact |
|------|------|---------|----------|--------|
| ui/templates/pages/coming-soon.html | 4 | "coming soon" placeholder template | Info | Dead code -- not referenced by any handler, not a blocker |
| server/bootstrap/bootstrap.go | 115 | SERVER_PLACEHOLDER in systemd unit | Info | Not a code placeholder -- sed replaces it at runtime in the generated bootstrap script |

### Human Verification Required

### 1. Visual Layout and Styling

**Test:** Open http://localhost:8080/ in a browser, navigate through all pages
**Expected:** Dark sidebar with working navigation, light content area, responsive layout, properly styled stat cards/badges/tables/gauges
**Why human:** Visual appearance cannot be verified programmatically

### 2. HTMX Auto-Refresh Behavior

**Test:** Open dashboard, wait 30+ seconds
**Expected:** Stat cards refresh in-place without full page reload; sidebar stays active
**Why human:** Real-time polling behavior requires a running server and browser

### 3. Bootstrap Script Execution

**Test:** Run `curl http://server:8080/bootstrap?token=TOKEN | bash` on a fresh node
**Expected:** Script detects OS/arch, downloads binary, installs CA cert, registers agent, optionally installs systemd service
**Why human:** End-to-end bootstrap requires actual server, network, and target node

### 4. Agent Self-Update Process Restart

**Test:** Run agent with old version, set server to report newer version
**Expected:** Agent downloads new binary, verifies checksum, atomically replaces binary, exec restarts with new version
**Why human:** Process exec/restart behavior requires running processes

### Gap Closure Summary

Both gaps from the initial verification have been successfully closed:

**Gap 1 (Bootstrap handler not wired) -- CLOSED:** Commit `f7061dd` added `import "github.com/isogov/isogov/server/bootstrap"` to gateway.go (line 17) and wired `bootstrap.NewHandler(serverAddr, caFile, agentDir, version)` (line 54) with `bh.RegisterRoutes(mux)` (line 55) before the `/api/` catch-all (line 58). All 8 bootstrap tests pass. GET /bootstrap, GET /api/v1/agent/latest, and GET /api/v1/agent/download/{os}/{arch} are now reachable at runtime.

**Gap 2 (Loop test regression) -- CLOSED:** Commit `9263ce1` updated line 277 of loop_test.go from `Loop(ctx, client, executors, nil)` to `Loop(ctx, client, executors, nil, nil)`, matching the 5-argument signature. All 11 agent/checkin tests pass including TestLoop_BackoffOnError_DoesNotCrash.

**Regression check:** No regressions detected in previously-passed items. All 24 artifacts remain present and substantive, all 10 key links are wired, `go build ./...` succeeds cleanly, and all related test suites pass (bootstrap: 8/8, update: 7/7, checkin: 11/11).

---

_Verified: 2026-03-06T22:02:05Z_
_Verifier: Claude (gsd-verifier)_
