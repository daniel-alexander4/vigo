# Phase 4: Web UI & Deployment - Research

**Researched:** 2026-03-06
**Domain:** HTMX+Go templates SPA, bootstrap scripting, agent self-update
**Confidence:** HIGH

## Summary

Phase 4 has three distinct sub-domains: (1) a server-rendered web dashboard using HTMX + Go templates with `go:embed`, (2) a bootstrap script endpoint for zero-touch node enrollment, and (3) agent binary self-update with checksum verification. All three integrate into the existing server binary with no new Go dependencies required (HTMX is a vendored JS file, `go:embed` and `html/template` are stdlib).

The web UI consumes the same REST API endpoints that gvctl already uses (via grpc-gateway on port 8080). The dashboard, clients, runs, config, plans, and compliance views are all backed by existing `/api/v1/*` endpoints. The only new server-side endpoints are `GET /bootstrap` (dynamic shell script), `GET /api/v1/agent/latest` (version/checksum JSON), and `GET /api/v1/agent/download/{os}/{arch}` (binary stream). The YAML config viewer additionally needs read access to client YAML files via the existing `ConfigStore`.

**Primary recommendation:** Build the UI as pure Go templates with HTMX for dynamic updates. Use `hx-boost` on the body for SPA-like navigation. Serve static assets (htmx.min.js, CSS) and templates via `go:embed` in a `ui` package. For YAML syntax highlighting, use `alecthomas/chroma` (pure Go, the only new dependency). For the 30-day compliance sparkline, generate inline SVG server-side in Go template functions. Agent self-update uses the atomic write-then-rename pattern with `syscall.Exec` for restart.

<user_constraints>
## User Constraints (from CONTEXT.md)

### Locked Decisions
- HTMX + Go templates -- no JS framework, no build step, no node_modules
- hx-boost for navigation -- regular links upgraded to AJAX, bookmarkable URLs, browser back works natively
- Auto-poll via hx-trigger="every 30s" for dashboard stats and client table refresh
- Assets live in `ui/` directory at repo root: `ui/templates/` for Go templates, `ui/static/` for CSS/JS/images
- go:embed in a `ui` package, served by the existing `http.ServeMux` in `server/api/gateway.go`
- Fleet health at a glance: big stat cards at top (total clients, active in last hour, recent failures, drift rate)
- Compliance percentage bar below stat cards
- Recent activity table at bottom
- Client list: sortable table with hostname, IP, enrolled date, last seen, compliance status badge (color-coded). Filter by status. Click row to drill into detail.
- Client detail: last N runs in a table with expandable rows showing per-module results (action taken, status). Drift history timeline. Facts panel on the side.
- Runs view: filterable run log across all clients
- Config viewer: syntax-highlighted read-only display of client YAML with resolved template values
- Plans view: dry-run plans with per-resource actions, color-coded by action type (create/update/delete/skip)
- Clean ops style: dark sidebar navigation, light content area (Grafana/Datadog inspired)
- Single hand-written CSS file with CSS custom properties for colors/spacing -- no framework, no build step
- Status badges use semantic colors: green (compliant), yellow (drifted/stale), red (failing), gray (unknown)
- Compliance gauge: large percentage number + horizontal colored progress bar (green/yellow/red by threshold)
- 30-day compliance trend: inline SVG sparkline rendered server-side in Go template -- no JS chart library
- `GET /bootstrap?token=<token>` returns a shell script with token and server address baked in
- Script detects OS + arch, downloads correct agent binary, installs to `/usr/local/bin/isogov-agent`, copies embedded CA cert, generates keypair, registers with server using the baked-in token
- One-shot, minimal output -- designed for `curl <url> | bash` experience
- `gvctl tokens generate` prints the full bootstrap curl command
- Agent checks version with server during regular check-in
- If newer version available: downloads after completing current convergence run, verifies SHA256 checksum, replaces binary, restarts
- Update failure (bad checksum, write error): log error, keep running old version, retry on next check-in cycle
- Never crashes or leaves a broken binary -- conservative and safe

### Claude's Discretion
- Go template organization (base layout, partials, page-specific templates)
- HTMX attribute patterns and swap strategies
- SVG sparkline rendering approach
- YAML syntax highlighting implementation (server-side or lightweight JS)
- Bootstrap script error messages and OS detection details
- Agent restart mechanism after self-update (exec syscall vs systemd restart)
- Exact color values and CSS custom property naming

### Deferred Ideas (OUT OF SCOPE)
None -- discussion stayed within phase scope
</user_constraints>

<phase_requirements>
## Phase Requirements

| ID | Description | Research Support |
|----|-------------|-----------------|
| UI-01 | Dashboard: total clients, active in last hour, recent failures, drift rate | Existing `/api/v1/compliance` + `/api/v1/clients` + `/api/v1/runs/all` endpoints; HTMX auto-poll pattern |
| UI-02 | Clients view: table with hostname, IP, enrolled date, last seen, status badge | Existing `/api/v1/clients` endpoint; compliance status from `/api/v1/clients/{id}/compliance` |
| UI-03 | Client detail: last N runs, expandable per-module results, drift history chart | Existing `/api/v1/runs?client_id=X` + `/api/v1/runs/{id}` + `/api/v1/clients/{id}/compliance` endpoints |
| UI-04 | Runs view: filterable run log across all clients | Existing `/api/v1/runs/all` endpoint |
| UI-05 | Config view: read-only display of client YAML config | New server-side handler reading from ConfigStore or raw YAML files on disk |
| UI-06 | Plans view: dry-run plans with per-resource actions, color-coded | Existing `/api/v1/clients/{id}/dry-run` endpoint + plan display logic |
| UI-07 | Compliance view: percentage gauge, breakdown, non-compliant table, 30-day trend | Existing `/api/v1/compliance` + compliance history endpoints; SVG sparkline rendering |
| UI-08 | SPA served from server binary via go:embed | go:embed FS in `ui` package, mounted on gateway mux |
| AGNT-10 | Agent checks for newer binary version on each check-in, self-updates after checksum verification | New `/api/v1/agent/latest` endpoint; agent-side download + verify + exec pattern |
| INFRA-04 | Bootstrap script at `GET /bootstrap` with server address + CA cert baked in | Dynamic shell script generation from Go template; no auth required on endpoint |
| INFRA-05 | Agent binary download endpoint: `GET /api/v1/agent/download/{os}/{arch}` | Static file serving from a configured binaries directory |
</phase_requirements>

## Standard Stack

### Core
| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| `html/template` | stdlib | Server-side HTML rendering | Stdlib, auto-escaping, Go template syntax already used in client configs |
| `embed` | stdlib | Embed templates + static files in binary | Stdlib since Go 1.16, zero-dependency SPA serving |
| `net/http` | stdlib | HTTP routing for UI pages | Already used in gateway.go mux |
| HTMX | 2.0.x | AJAX upgrades for links/forms, auto-polling | User decision: no JS framework, ~14KB gzipped, vendored as static file |
| `alecthomas/chroma/v2` | v2.x | YAML syntax highlighting | Pure Go, 250+ languages, used by Hugo; generates HTML with CSS classes |

### Supporting
| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| `crypto/sha256` | stdlib | Checksum verification for agent self-update | Agent binary download verification |
| `syscall` | stdlib | Process replacement via `Exec` for agent restart | Agent self-update binary swap |
| `io/fs` | stdlib | Filesystem interface for embedded assets | Template parsing with `template.ParseFS` |

### Alternatives Considered
| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| chroma | CSS-only highlighting | Chroma handles YAML edge cases (anchors, multiline strings) that regex-based CSS cannot; small dependency (~2MB) |
| chroma | Prism.js (client-side) | Would work but adds JS dependency; server-side is simpler for this use case |
| syscall.Exec | systemd restart | Exec is simpler and works everywhere; systemd restart requires the agent to know its service name |

**Installation:**
```bash
go get github.com/alecthomas/chroma/v2
```

No npm install needed. HTMX is a single JS file downloaded and placed in `ui/static/js/htmx.min.js`.

## Architecture Patterns

### Recommended Project Structure
```
ui/
  templates/
    base.html          # Base layout: <html>, <head>, sidebar nav, content area
    partials/
      nav.html         # Sidebar navigation partial
      stat-card.html   # Reusable stat card partial
      status-badge.html # Compliance status badge partial
      pagination.html  # Pagination controls
    pages/
      dashboard.html   # Dashboard page (extends base)
      clients.html     # Client list page
      client-detail.html # Client detail page
      runs.html        # Runs list page
      run-detail.html  # Run detail page
      config.html      # Config viewer page
      plans.html       # Plans viewer page
      compliance.html  # Compliance dashboard page
  static/
    js/
      htmx.min.js      # Vendored HTMX 2.0.x (~14KB gzipped)
    css/
      style.css         # Single CSS file with custom properties
    images/
      logo.svg          # Optional branding
  embed.go              # go:embed directives + FS variables
  handler.go            # HTTP handlers for UI pages
  render.go             # Template rendering helpers + funcmap
  sparkline.go          # SVG sparkline generation
```

### Pattern 1: go:embed + Template Rendering

**What:** Embed all UI assets at compile time, parse templates once at startup, render with page-specific data structs.

**When to use:** Every UI page render.

**Example:**
```go
// ui/embed.go
package ui

import "embed"

//go:embed templates/* static/*
var Assets embed.FS

// ui/render.go
package ui

import (
    "html/template"
    "io/fs"
    "net/http"
)

type Renderer struct {
    templates *template.Template
}

func NewRenderer() (*Renderer, error) {
    funcMap := template.FuncMap{
        "statusColor": statusColor,
        "sparklineSVG": sparklineSVG,
        "formatTime": formatTime,
        "highlightYAML": highlightYAML,
    }

    tmpl, err := template.New("").Funcs(funcMap).ParseFS(Assets,
        "templates/base.html",
        "templates/partials/*.html",
        "templates/pages/*.html",
    )
    if err != nil {
        return nil, err
    }
    return &Renderer{templates: tmpl}, nil
}

func (r *Renderer) Render(w http.ResponseWriter, name string, data any) {
    w.Header().Set("Content-Type", "text/html; charset=utf-8")
    if err := r.templates.ExecuteTemplate(w, name, data); err != nil {
        http.Error(w, "template error", http.StatusInternalServerError)
    }
}
```

### Pattern 2: hx-boost SPA Navigation

**What:** Put `hx-boost="true"` on the `<body>` tag. All `<a>` tags become AJAX-powered, swapping only the `<body>` innerHTML. Browser history and URLs work normally.

**When to use:** Every page in the UI -- this gives SPA-like feel with zero client-side routing code.

**Key detail:** When `hx-boost` is active, the server can detect boosted requests via the `HX-Boosted: true` header. However, for simplicity, always return full HTML pages -- HTMX is smart enough to extract only the `<body>` content. This means every page works both as a direct load and as a boosted navigation.

**Example:**
```html
<!-- templates/base.html -->
{{define "base"}}
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="utf-8">
    <title>Isogov - {{.Title}}</title>
    <link rel="stylesheet" href="/static/css/style.css">
    <script src="/static/js/htmx.min.js"></script>
</head>
<body hx-boost="true">
    <div class="layout">
        {{template "nav" .}}
        <main class="content">
            {{template "content" .}}
        </main>
    </div>
</body>
</html>
{{end}}
```

### Pattern 3: HTMX Auto-Poll for Live Data

**What:** Use `hx-trigger="every 30s"` to auto-refresh dashboard stats and client tables without full page reload.

**When to use:** Dashboard stat cards, client list table, compliance gauge.

**Key detail:** The polled endpoint returns an HTML fragment (not the full page). The handler checks for the `HX-Request` header to decide whether to return a full page or just the fragment.

**Example:**
```html
<!-- Dashboard stat cards with auto-refresh -->
<div id="stats"
     hx-get="/ui/fragments/dashboard-stats"
     hx-trigger="every 30s"
     hx-swap="innerHTML">
    {{template "dashboard-stats" .Stats}}
</div>
```

```go
// handler.go
func (h *Handler) DashboardStats(w http.ResponseWriter, r *http.Request) {
    stats := h.fetchDashboardStats(r.Context())
    // Always render just the fragment -- used by both initial page and HTMX poll
    h.renderer.Render(w, "dashboard-stats", stats)
}
```

### Pattern 4: Gateway Integration

**What:** Mount UI handlers on the existing `http.ServeMux` in `gateway.go`.

**When to use:** Server startup -- the UI is just more routes on the same HTTP server.

**Example:**
```go
// server/api/gateway.go (modified)
func NewGateway(ctx context.Context, grpcAddr, certFile, keyFile, caFile string) (*http.ServeMux, error) {
    // ... existing grpc-gateway setup ...

    mux := http.NewServeMux()
    mux.Handle("/metrics", promhttp.Handler())

    // UI routes
    uiHandler, err := ui.NewHandler(/* deps */)
    if err != nil {
        return nil, err
    }

    // Static assets (CSS, JS, images)
    staticFS, _ := fs.Sub(ui.Assets, "static")
    mux.Handle("/static/", http.StripPrefix("/static/", http.FileServerFS(staticFS)))

    // UI page routes
    mux.HandleFunc("/", uiHandler.Dashboard)
    mux.HandleFunc("/clients", uiHandler.Clients)
    mux.HandleFunc("/clients/", uiHandler.ClientDetail)
    mux.HandleFunc("/runs", uiHandler.Runs)
    mux.HandleFunc("/runs/", uiHandler.RunDetail)
    mux.HandleFunc("/config/", uiHandler.Config)
    mux.HandleFunc("/plans/", uiHandler.Plans)
    mux.HandleFunc("/compliance", uiHandler.Compliance)

    // HTMX fragment endpoints
    mux.HandleFunc("/ui/fragments/dashboard-stats", uiHandler.DashboardStats)
    mux.HandleFunc("/ui/fragments/clients-table", uiHandler.ClientsTable)

    // Bootstrap + agent download
    mux.HandleFunc("/bootstrap", uiHandler.Bootstrap)
    mux.HandleFunc("/api/v1/agent/latest", uiHandler.AgentLatest)
    mux.HandleFunc("/api/v1/agent/download/", uiHandler.AgentDownload)

    // grpc-gateway catches remaining /api/v1/* routes
    mux.Handle("/api/", gwMux)

    return mux, nil
}
```

**Important:** The `/` route must be registered last or with careful path matching to not shadow `/api/v1/*` routes. Go 1.22+ `http.ServeMux` supports path patterns with `{param}` syntax and exact matching with trailing `/` vs no trailing `/`.

### Pattern 5: UI Data Fetching -- Direct DB vs REST API

**What:** The UI handlers fetch data directly from the `db.Queries` instance rather than making HTTP calls to the grpc-gateway REST API.

**Why:** The UI runs in the same process as the server. Making an HTTP call to localhost would be unnecessary overhead and would add TLS/auth complexity. The grpc-gateway REST API is for external consumers (gvctl, scripts). The UI handlers should use the same `db.Queries` and `config.ConfigStore` that the gRPC handlers use.

**When to use:** All UI data access.

**Example:**
```go
type Handler struct {
    renderer    *Renderer
    queries     *db.Queries
    configStore *config.ConfigStore
    baseInterval time.Duration
    serverAddr  string    // for bootstrap script
    caFile      string    // for bootstrap script
    agentDir    string    // directory containing agent binaries
}
```

### Anti-Patterns to Avoid
- **Don't make HTTP calls to self:** UI handlers run in the same process. Use `db.Queries` directly, not `http.Get("http://localhost:8080/api/v1/clients")`.
- **Don't parse templates on every request:** Parse once at startup with `template.ParseFS`. Template execution is already concurrent-safe.
- **Don't use client-side JavaScript for data fetching:** HTMX handles all dynamic loading. The only JS file should be `htmx.min.js`.
- **Don't use a CSS framework:** User explicitly chose hand-written CSS with custom properties. No Tailwind, Bootstrap, etc.
- **Don't put secrets in HTML responses:** The config viewer shows resolved YAML, but secret values must be redacted. Use `[REDACTED]` for any value that came from a `secret:` prefix.

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| YAML syntax highlighting | Regex-based tokenizer | `alecthomas/chroma/v2` | YAML has complex edge cases (anchors, multiline, quoted strings); chroma handles all of them |
| SPA client-side routing | Custom JS router | HTMX `hx-boost` | Zero JS, browser history works natively, bookmarkable URLs |
| Template inheritance | Custom include system | Go `{{define}}/{{template}}/{{block}}` | Stdlib, well-documented, sufficient for this use case |
| Auto-refresh polling | Custom JS setInterval | HTMX `hx-trigger="every 30s"` | Declarative, handles errors gracefully, no custom JS |
| Agent binary checksumming | Custom hash verification | `crypto/sha256` stdlib | Standard, well-tested, matches server-computed checksums |
| Agent process replacement | Custom restart logic | `syscall.Exec` | Standard Unix pattern, atomically replaces process, preserves PID |

**Key insight:** The entire UI requires zero custom JavaScript. HTMX provides all the dynamic behavior (navigation, polling, fragment loading). The only JS in the project is the vendored `htmx.min.js`.

## Common Pitfalls

### Pitfall 1: Template Name Collisions
**What goes wrong:** Multiple templates defining the same name (e.g., two pages both `{{define "content"}}`) causes unpredictable behavior -- the last parsed definition wins.
**Why it happens:** Go's `template.ParseFS` loads all files into a single namespace.
**How to avoid:** Use unique template names like `{{define "dashboard-content"}}`, `{{define "clients-content"}}`. The base layout calls `{{template .ContentTemplate .}}` where `.ContentTemplate` is a string set by the handler.
**Warning signs:** Pages rendering the wrong content, or changes to one page affecting another.

### Pitfall 2: go:embed Path Must Be Relative
**What goes wrong:** `//go:embed ../templates/*` fails -- go:embed paths cannot use `..` or be absolute.
**Why it happens:** The `go:embed` directive requires paths relative to the Go source file containing it.
**How to avoid:** Place `embed.go` in the `ui/` package at the root of the embedded tree. Use `//go:embed templates/* static/*`.
**Warning signs:** Build errors like "pattern ../templates: invalid pattern syntax".

### Pitfall 3: Static File Caching with go:embed
**What goes wrong:** CSS/JS changes don't appear after redeployment because browsers cache static files.
**Why it happens:** Embedded files have no mtime-based cache invalidation since they're compiled into the binary.
**How to avoid:** Add a version query parameter to static file URLs: `/static/css/style.css?v={{.Version}}`. Set the version from build-time `-ldflags`.
**Warning signs:** Users seeing old styles after server upgrade.

### Pitfall 4: Secret Leakage in Config Viewer
**What goes wrong:** The config viewer shows resolved secret values (database passwords, webhook secrets) in the browser.
**Why it happens:** The `ConfigStore` resolves `secret:` prefixes at load time, so the in-memory config has plaintext values.
**How to avoid:** For the config viewer, read the raw YAML file from disk (not the resolved config). Secrets appear as `secret:isogov/db/dsn` which is safe to display. Alternatively, render the resolved config but replace values of known-secret fields with `[REDACTED]`.
**Warning signs:** Database passwords visible in the web UI.

### Pitfall 5: Bootstrap Script Injection
**What goes wrong:** If the bootstrap script template interpolates user-provided values (token, server address) without escaping, shell injection is possible.
**Why it happens:** The bootstrap script is a shell script generated from a Go template. Shell metacharacters in the token could execute arbitrary commands.
**How to avoid:** Tokens are hex-encoded random bytes (generated by `gvctl tokens generate`), so they are safe. But still single-quote all interpolated values in the shell script template.
**Warning signs:** Token values containing `$`, backticks, or semicolons breaking the script.

### Pitfall 6: Agent Self-Update Race Condition
**What goes wrong:** Agent downloads new binary but crashes before completing the swap, leaving a corrupt binary.
**Why it happens:** Writing directly to the running binary path while the process is using it.
**How to avoid:** Write to a temporary file in the same directory, verify checksum, then atomically rename. On Linux, renaming over a running binary is safe -- the old inode stays open until the process exits. Then `syscall.Exec` replaces the process with the new binary.
**Warning signs:** Agent failing to start after an update, "text file busy" errors.

### Pitfall 7: Route Shadowing Between UI and API
**What goes wrong:** UI routes like `/clients` shadow or conflict with API routes like `/api/v1/clients`.
**Why it happens:** Incorrect route registration order on `http.ServeMux`.
**How to avoid:** Register specific API paths before the catch-all. In Go 1.22+ ServeMux, more specific patterns take priority. Register `/api/` to the grpc-gateway mux, and UI routes like `/`, `/clients`, `/runs` etc. separately.
**Warning signs:** API calls returning HTML instead of JSON, or UI pages showing JSON.

## Code Examples

### SVG Sparkline Generation (Server-Side)

Generate an inline SVG sparkline from compliance history data points:

```go
// ui/sparkline.go
package ui

import (
    "fmt"
    "html/template"
    "strings"
)

// SparklineSVG generates an inline SVG sparkline from data points.
// width and height are in pixels. values should be percentages (0-100).
func SparklineSVG(values []float64, width, height int) template.HTML {
    if len(values) == 0 {
        return ""
    }

    n := len(values)
    stepX := float64(width) / float64(n-1)

    var points []string
    for i, v := range values {
        x := float64(i) * stepX
        // Invert Y because SVG Y-axis goes downward
        y := float64(height) - (v / 100.0 * float64(height))
        points = append(points, fmt.Sprintf("%.1f,%.1f", x, y))
    }

    path := fmt.Sprintf("M%s", strings.Join(points, " L"))

    svg := fmt.Sprintf(
        `<svg viewBox="0 0 %d %d" class="sparkline" preserveAspectRatio="none">
            <path d="%s" fill="none" stroke="var(--color-success)" stroke-width="1.5"/>
        </svg>`,
        width, height, path,
    )

    return template.HTML(svg)
}
```

### YAML Syntax Highlighting with Chroma

```go
// ui/render.go (highlightYAML function for template FuncMap)
package ui

import (
    "bytes"
    "html/template"

    "github.com/alecthomas/chroma/v2"
    "github.com/alecthomas/chroma/v2/formatters/html"
    "github.com/alecthomas/chroma/v2/lexers"
    "github.com/alecthomas/chroma/v2/styles"
)

func highlightYAML(source string) template.HTML {
    lexer := lexers.Get("yaml")
    if lexer == nil {
        lexer = lexers.Fallback
    }
    lexer = chroma.Coalesce(lexer)

    formatter := html.New(html.WithClasses(true))
    style := styles.Get("monokai")

    iterator, err := lexer.Tokenise(nil, source)
    if err != nil {
        return template.HTML("<pre>" + template.HTMLEscapeString(source) + "</pre>")
    }

    var buf bytes.Buffer
    if err := formatter.Format(&buf, style, iterator); err != nil {
        return template.HTML("<pre>" + template.HTMLEscapeString(source) + "</pre>")
    }

    return template.HTML(buf.String())
}
```

### Bootstrap Script Template

```go
// The bootstrap script is a Go text/template rendered to shell script
const bootstrapScriptTmpl = `#!/bin/sh
set -euo pipefail

# Isogov Agent Bootstrap Script
# Generated by isogov server

SERVER_ADDR='{{.ServerAddr}}'
TOKEN='{{.Token}}'

# Detect OS and architecture
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
case "$ARCH" in
    x86_64|amd64) ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *) echo "ERROR: unsupported architecture: $ARCH" >&2; exit 1 ;;
esac

DOWNLOAD_URL="https://${SERVER_ADDR}/api/v1/agent/download/${OS}/${ARCH}"
INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/etc/isogov"
BINARY="${INSTALL_DIR}/isogov-agent"

echo "Installing isogov-agent for ${OS}/${ARCH}..."

# Download agent binary
curl -fsSL -o /tmp/isogov-agent "$DOWNLOAD_URL"
chmod +x /tmp/isogov-agent
mv /tmp/isogov-agent "$BINARY"

# Write CA certificate
mkdir -p "$CONFIG_DIR"
cat > "${CONFIG_DIR}/ca.crt" << 'CACERT'
{{.CACert}}
CACERT

# Generate keypair and register
"$BINARY" --data-dir "$CONFIG_DIR" --server "$SERVER_ADDR" --ca-file "${CONFIG_DIR}/ca.crt" register --token "$TOKEN"

# Install systemd service
if command -v systemctl >/dev/null 2>&1; then
    cat > /etc/systemd/system/isogov-agent.service << 'UNIT'
[Unit]
Description=Isogov Configuration Management Agent
After=network.target

[Service]
ExecStart=/usr/local/bin/isogov-agent --data-dir /etc/isogov --server {{.ServerAddr}} --ca-file /etc/isogov/ca.crt
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
UNIT
    systemctl daemon-reload
    systemctl enable isogov-agent
    systemctl start isogov-agent
    echo "isogov-agent installed and started"
else
    echo "systemd not found -- start the agent manually"
fi
`
```

### Agent Self-Update Pattern

```go
// agent/update/updater.go
package update

import (
    "crypto/sha256"
    "encoding/hex"
    "fmt"
    "io"
    "net/http"
    "os"
    "path/filepath"
    "runtime"
    "syscall"
)

type VersionInfo struct {
    Version     string `json:"version"`
    SHA256      string `json:"sha256"`
    DownloadURL string `json:"download_url"`
}

// Apply downloads, verifies, and replaces the current binary.
// Returns nil only if exec succeeds (which means the process is replaced).
func Apply(info VersionInfo, currentBinary string) error {
    // 1. Download to temp file in same directory (for atomic rename)
    dir := filepath.Dir(currentBinary)
    tmp, err := os.CreateTemp(dir, "isogov-agent-update-*")
    if err != nil {
        return fmt.Errorf("create temp file: %w", err)
    }
    tmpPath := tmp.Name()
    defer os.Remove(tmpPath) // cleanup on error

    resp, err := http.Get(info.DownloadURL)
    if err != nil {
        tmp.Close()
        return fmt.Errorf("download: %w", err)
    }
    defer resp.Body.Close()

    // 2. Write and compute checksum simultaneously
    h := sha256.New()
    if _, err := io.Copy(io.MultiWriter(tmp, h), resp.Body); err != nil {
        tmp.Close()
        return fmt.Errorf("write temp file: %w", err)
    }
    tmp.Close()

    // 3. Verify checksum
    gotHash := hex.EncodeToString(h.Sum(nil))
    if gotHash != info.SHA256 {
        return fmt.Errorf("checksum mismatch: got %s, want %s", gotHash, info.SHA256)
    }

    // 4. Make executable
    if err := os.Chmod(tmpPath, 0755); err != nil {
        return fmt.Errorf("chmod: %w", err)
    }

    // 5. Atomic rename (same filesystem, safe on Linux)
    if err := os.Rename(tmpPath, currentBinary); err != nil {
        return fmt.Errorf("rename: %w", err)
    }

    // 6. Exec to replace process with new binary
    return syscall.Exec(currentBinary, os.Args, os.Environ())
}
```

### HTMX Expandable Run Results

```html
<!-- Expandable row pattern for run results -->
<tr>
    <td>{{.Run.ID | truncUUID}}</td>
    <td>{{.Run.StartedAt | formatTime}}</td>
    <td><span class="badge badge--{{.Run.Status}}">{{.Run.Status}}</span></td>
    <td>{{.Run.ModuleCount}}</td>
    <td>{{.Run.ChangedCount}}</td>
    <td>
        <button hx-get="/ui/fragments/run-results/{{.Run.ID}}"
                hx-target="next .run-results"
                hx-swap="innerHTML"
                class="btn btn--sm">
            Details
        </button>
    </td>
</tr>
<tr>
    <td colspan="6" class="run-results"></td>
</tr>
```

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| HTMX 1.x | HTMX 2.0.x | June 2024 | Dropped IE support, cleaned up attributes, smaller bundle |
| `ioutil.ReadFile` | `os.ReadFile` | Go 1.16 | ioutil deprecated; project already uses os.ReadFile |
| `http.HandleFunc` patterns | Go 1.22+ ServeMux path patterns | Go 1.22 (Feb 2024) | Supports `{param}` in routes, method matching; project uses Go 1.25 |
| Custom embed hacks | `embed.FS` + `template.ParseFS` | Go 1.16 | Stdlib support for embedded filesystems |

**Deprecated/outdated:**
- HTMX 1.x: Still works but 2.0 is current. Since we vendor the file, use 2.0.x.
- `http.FileServer(http.FS(...))`: Use `http.FileServerFS()` instead (Go 1.22+).

## Open Questions

1. **Agent binary distribution directory**
   - What we know: Server needs to serve pre-built agent binaries for multiple OS/arch combinations
   - What's unclear: Where do compiled binaries live? A `dist/` directory? Built by Makefile?
   - Recommendation: Add a `make dist` target that cross-compiles agent binaries. Store in `dist/agent/{os}/{arch}/isogov-agent`. Server config gets an `agent_dir` setting pointing to this directory. For v1, support `linux/amd64` and `linux/arm64`.

2. **Agent version string**
   - What we know: Agent needs to report its version, server needs to compare versions
   - What's unclear: Build-time version injection via ldflags? Semver?
   - Recommendation: Use `-ldflags "-X main.version=..."` at build time. Version is a simple semver string (e.g., `1.0.0`). The `/api/v1/agent/latest` endpoint returns the version of binaries in the `agent_dir`. Agent sends its version in CheckIn metadata or a dedicated call.

3. **Config viewer: raw YAML vs resolved**
   - What we know: User wants "resolved template values" displayed, but secrets must not leak
   - What's unclear: Should it show the raw YAML file, the resolved config (with secrets redacted), or the rendered template output?
   - Recommendation: Read raw YAML from disk, display with chroma syntax highlighting. Add a note showing "secret:" values are not resolved for security. This is the simplest and safest approach. The resolved view would require a redaction pass and is error-prone.

## Validation Architecture

### Test Framework
| Property | Value |
|----------|-------|
| Framework | Go testing (stdlib) |
| Config file | none (stdlib) |
| Quick run command | `go test ./ui/... ./agent/update/... -v -count=1` |
| Full suite command | `go test ./... -v -race -count=1` |

### Phase Requirements -> Test Map
| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|-------------|
| UI-08 | go:embed FS contains all expected files | unit | `go test ./ui/... -run TestEmbedFS -v` | No -- Wave 0 |
| UI-01 | Dashboard handler returns stat cards HTML | unit | `go test ./ui/... -run TestDashboardHandler -v` | No -- Wave 0 |
| UI-02 | Clients handler returns table with expected columns | unit | `go test ./ui/... -run TestClientsHandler -v` | No -- Wave 0 |
| UI-03 | Client detail handler returns run history | unit | `go test ./ui/... -run TestClientDetailHandler -v` | No -- Wave 0 |
| UI-04 | Runs handler returns filterable run list | unit | `go test ./ui/... -run TestRunsHandler -v` | No -- Wave 0 |
| UI-05 | Config handler returns syntax-highlighted YAML | unit | `go test ./ui/... -run TestConfigHandler -v` | No -- Wave 0 |
| UI-06 | Plans handler returns color-coded actions | unit | `go test ./ui/... -run TestPlansHandler -v` | No -- Wave 0 |
| UI-07 | Compliance handler returns gauge + sparkline SVG | unit | `go test ./ui/... -run TestComplianceHandler -v` | No -- Wave 0 |
| INFRA-04 | Bootstrap endpoint returns valid shell script | unit | `go test ./ui/... -run TestBootstrapHandler -v` | No -- Wave 0 |
| INFRA-05 | Agent download endpoint serves binary | unit | `go test ./ui/... -run TestAgentDownload -v` | No -- Wave 0 |
| AGNT-10 | Agent self-update: download, verify checksum, replace | unit | `go test ./agent/update/... -run TestSelfUpdate -v` | No -- Wave 0 |

### Sampling Rate
- **Per task commit:** `go test ./ui/... ./agent/update/... -v -count=1`
- **Per wave merge:** `go test ./... -v -race -count=1`
- **Phase gate:** Full suite green before `/gsd:verify-work`

### Wave 0 Gaps
- [ ] `ui/handler_test.go` -- covers UI-01 through UI-08, INFRA-04, INFRA-05
- [ ] `ui/render_test.go` -- covers template rendering, sparkline SVG, YAML highlighting
- [ ] `agent/update/updater_test.go` -- covers AGNT-10 (checksum verify, atomic rename)
- [ ] Framework install: `go get github.com/alecthomas/chroma/v2` -- if chroma used for highlighting

## Sources

### Primary (HIGH confidence)
- Go embed package docs (https://pkg.go.dev/embed) -- embed directive syntax, FS interface
- Go html/template docs (https://pkg.go.dev/html/template) -- template parsing, FuncMap, auto-escaping
- HTMX official docs (https://htmx.org/docs/) -- hx-boost, hx-trigger, hx-swap attributes
- HTMX hx-boost attribute docs (https://htmx.org/attributes/hx-boost/) -- navigation behavior, history management
- Existing codebase: `server/api/gateway.go`, `server/main.go`, `server/db/`, `server/config/loader.go`
- Existing codebase: `agent/main.go`, `agent/checkin/client.go`, `agent/checkin/loop.go`
- Project handoff spec: `isogov-handoff.md` -- bootstrap API, self-update endpoint, UI views

### Secondary (MEDIUM confidence)
- alecthomas/chroma GitHub (https://github.com/alecthomas/chroma) -- YAML lexer, HTML formatter with CSS classes
- Go self-update gist (https://gist.github.com/fenollp/7e31e6462b10c96aef443351bce6aea7) -- syscall.Exec pattern
- HTMX + Go template patterns (https://dev.to/calvinmclean/how-to-build-a-web-application-with-htmx-and-go-3183) -- hx-boost with Go templates

### Tertiary (LOW confidence)
- None -- all critical findings verified against official docs or codebase

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH -- all stdlib except HTMX (vendored JS) and chroma (well-established Go library); patterns verified against official Go and HTMX docs
- Architecture: HIGH -- go:embed + html/template + http.ServeMux are standard Go patterns; hx-boost SPA navigation is the documented HTMX approach; gateway integration verified against existing codebase
- Pitfalls: HIGH -- template naming, embed paths, route shadowing are well-documented Go gotchas; secret leakage and bootstrap injection are security fundamentals
- Bootstrap/Self-update: MEDIUM -- patterns are standard but exact implementation details (agent version injection, binary distribution directory) need design decisions during planning

**Research date:** 2026-03-06
**Valid until:** 2026-04-06 (stable domain -- Go stdlib and HTMX 2.x are mature)
