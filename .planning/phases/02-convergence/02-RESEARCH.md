# Phase 2: Convergence - Research

**Researched:** 2026-03-05
**Domain:** Agent daemon, DAG-ordered idempotent resource executors, fact collection, notify/subscribe triggers
**Confidence:** HIGH

## Summary

Phase 2 transforms Isogov from a server-side foundation into a working convergence engine. The agent must pull its compiled catalog from the server, build a dependency graph, execute resources idempotently in topological order, handle notify/subscribe triggers for service restarts, collect system facts, evaluate conditional `when:` expressions, manage file snapshots for rollback, and report all results back via gRPC.

The existing Phase 1 codebase provides solid foundations: proto definitions already include `ReportResult`, `ReportFacts`, `CheckIn` RPCs with signature verification; DB models and sqlc-generated queries cover `runs`, `run_results`, `client_facts`, and `file_snapshots` tables; the `config.ConfigStore` resolves client configs with template rendering; and `agent/auth` provides ED25519 signing. Phase 2 builds the agent-side execution engine and server-side report handlers on top of this.

**Primary recommendation:** Build bottom-up -- fact collectors and package manager interface first (no dependencies on other Phase 2 code), then resource executors (depend on pkgmgr), then DAG/runner engine (orchestrates executors), then agent daemon loop (ties everything together with gRPC client). Server-side additions (ReportResult/ReportFacts handlers, CheckIn facts integration) can be developed in parallel.

<phase_requirements>
## Phase Requirements

| ID | Description | Research Support |
|----|-------------|-----------------|
| CONV-01 | File resource executor checks SHA256 hash before writing, skips if match | File executor pattern: `crypto/sha256` + `os.ReadFile` for current hash, compare before writing |
| CONV-02 | File resource uses atomic write-to-temp-then-rename pattern | `os.CreateTemp` in same directory + `os.Rename` for atomic swap (POSIX guarantees) |
| CONV-03 | Package resource executor queries package manager for installed state before acting | PackageManager interface with `IsInstalled()` check before `Install`/`Remove` |
| CONV-04 | Service resource executor checks systemctl is-active/is-enabled before acting | `exec.Command("systemctl", "is-active", name)` exit code 0 = active |
| CONV-05 | User resource executor parses /etc/passwd for existence and attributes before acting | Go stdlib `os/user.Lookup()` + `/etc/passwd` parsing for full attribute comparison |
| CONV-06 | Exec resource executor runs arbitrary commands with idempotency guards (creates/onlyif/unless) | Check `creates` path exists, run `onlyif`/`unless` commands, skip if guard satisfied |
| CONV-07 | Module dependencies form a DAG with topological ordering via Kahn's algorithm | Kahn's algorithm: compute in-degrees, BFS from zero-in-degree nodes, O(V+E) |
| CONV-08 | Circular dependencies detected at config load time with clear error message | If Kahn's result length != node count, cycle exists -- report participating nodes |
| CONV-09 | Resources support `notify` list -- triggered resources re-apply when notifier changes | Track `changed` flag per resource; after DAG execution, re-run notified resources |
| CONV-10 | Resources support `subscribes` -- alternative syntax for notify relationships | Reverse-map subscribes to notify during DAG construction (normalize to one direction) |
| CONV-11 | Service resource supports states: running, stopped, restarted, reloaded | Map to systemctl commands: start, stop, restart, reload |
| CONV-12 | All executors record action_taken (none/create/update/delete/skip) in run results | Executor return type includes ActionTaken enum, maps to proto ModuleResult |
| CONF-09 | Resources support `when:` field as Go template expression against facts | Evaluate `when` string as Go template with `.Facts`, check if result is "true"/"false" |
| CONF-10 | Resources where `when` evaluates to false are skipped with action_taken: skipped | Return `ActionSkipped` before executor runs, include in run results |
| AGNT-01 | Agent runs as daemon with jittered pull loop (base_interval +/- jitter_percent) | `time.NewTimer` with randomized duration using `crypto/rand` or `math/rand/v2` |
| AGNT-02 | Agent handles server unavailability with exponential backoff, never crashes | `cenkalti/backoff/v4` already in go.mod (indirect); or hand-roll with capped exponential |
| AGNT-03 | Agent collects facts: os/hardware/network/packages | `gopsutil/v4` (already indirect dep) for os/hardware/network; package manager query for packages |
| AGNT-04 | Facts sent with every check-in, stored in DB, available as template variables | Agent sends `ReportFacts` RPC; server stores in `client_facts`; CheckIn reads latest facts for template rendering |
| AGNT-05 | Pluggable fact collectors via `Collector` interface (Name, Collect) | Interface defined per handoff spec; register collectors in slice, iterate to build facts map |
| AGNT-06 | Agent supports `--dry-run` flag for one-shot preview mode | Run executors in check-only mode (no mutations), report planned actions, exit |
| AGNT-07 | Agent supports `isogov-agent run` for one-shot immediate convergence | Cobra subcommand: check-in once, execute, report, exit 0/1 |
| AGNT-08 | Agent supports `run --module <name>` to apply a single module | Filter module list from CheckIn response by name before DAG execution |
| AGNT-09 | File snapshots saved to `/var/lib/isogov/snapshots/` before overwrites (last 5 per path) | Snapshot manager: copy file before overwrite, prune to 5 per path, filename format `<sha256-of-path>.<run-uuid>.bak` |
| INFRA-06 | Multi-platform package manager support via PackageManager interface | Interface with 7 backends (apt, dnf, yum, apk, pacman, brew, zypper); auto-detect from OS facts |
</phase_requirements>

## Standard Stack

### Core
| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| `github.com/shirou/gopsutil/v4` | v4.25.6 | System facts (CPU, memory, disk, network, host) | Already indirect dep; cross-platform; well-maintained |
| `github.com/spf13/cobra` | v1.10.2 | Agent CLI subcommands (run, register, daemon) | Already used for agent CLI |
| `os/exec` | stdlib | Package manager commands, systemctl, useradd | Standard for subprocess execution |
| `crypto/sha256` | stdlib | File content hashing for idempotency checks | Standard, no external dep needed |
| `os/user` | stdlib | User existence/attribute lookup | Standard; pure Go fallback parses /etc/passwd |
| `text/template` | stdlib | `when:` conditional evaluation | Already used in `server/config/template.go` |
| `encoding/json` | stdlib | Facts JSON serialization for gRPC and DB | Facts are JSONB in DB, JSON bytes in proto |

### Supporting
| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| `github.com/cenkalti/backoff/v4` | v4.3.0 | Exponential backoff for server unavailability | Already indirect dep; use for AGNT-02 |
| `math/rand/v2` | stdlib (Go 1.22+) | Jitter calculation for pull loop | Standard for non-crypto randomness |
| `google.golang.org/grpc` | v1.79.1 | Agent gRPC client to server | Already in go.mod |
| `google.golang.org/protobuf` | v1.36.11 | Proto message construction | Already in go.mod |
| `github.com/google/uuid` | v1.6.0 | Run IDs, snapshot naming | Already in go.mod |

### Alternatives Considered
| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| gopsutil for facts | Manual /proc parsing | gopsutil handles cross-platform (Linux+macOS) correctly; manual is fragile |
| cenkalti/backoff | Hand-rolled exponential backoff | cenkalti is already a dependency and handles edge cases (max elapsed, jitter) |
| os/user for user lookup | Third-party passwd parser | os/user is stdlib and sufficient; /etc/passwd parsing needed only for attribute comparison (uid, shell, home) |
| Hand-rolled DAG | `gonum/graph` | Kahn's algorithm is ~50 lines of Go; no need for a graph library for this |

## Architecture Patterns

### Recommended Project Structure
```
agent/
  checkin/
    loop.go            # Daemon pull loop with jitter (AGNT-01)
    backoff.go         # Exponential backoff wrapper (AGNT-02)
    client.go          # gRPC client: CheckIn, ReportResult, ReportFacts
  facts/
    collector.go       # Collector interface
    os.go              # OS facts (family, distro, version, kernel, arch)
    hardware.go        # Hardware facts (cpu_count, memory_mb, disk_gb)
    network.go         # Network facts (hostname, fqdn, ip_addresses, mac_addresses)
    packages.go        # Package facts (installed map)
  modules/
    executor.go        # Executor interface + registry
    file.go            # File resource executor (CONV-01, CONV-02)
    package.go         # Package resource executor (CONV-03)
    service.go         # Service resource executor (CONV-04, CONV-11)
    user.go            # User resource executor (CONV-05)
    exec.go            # Exec resource executor (CONV-06)
    result.go          # ActionTaken type + ResourceResult struct
    pkgmgr/
      interface.go     # PackageManager interface (INFRA-06)
      detect.go        # Auto-detection from facts
      apt.go           # Debian/Ubuntu
      dnf.go           # Fedora/RHEL 8+
      yum.go           # RHEL 7/CentOS 7
      apk.go           # Alpine
      pacman.go        # Arch
      brew.go          # macOS
      zypper.go        # openSUSE/SLES
  runner/
    dag.go             # DAG construction + topological sort (CONV-07, CONV-08)
    runner.go          # Execute resources in DAG order with notify/subscribe (CONV-09, CONV-10)
    when.go            # Conditional evaluation (CONF-09, CONF-10)
  snapshots/
    manager.go         # File snapshot save/prune/restore (AGNT-09)
  main.go             # Cobra CLI: root (daemon), run, register
server/
  grpc/
    report.go          # ReportResult handler (stores run results in DB)
    facts.go           # ReportFacts handler (stores facts in DB)
    checkin.go         # Updated: reads latest facts for template rendering
```

### Pattern 1: Executor Interface
**What:** All resource types implement a common interface that checks current state, applies desired state, and returns a structured result.
**When to use:** Every resource type (file, package, service, user, exec).
**Example:**
```go
// agent/modules/executor.go
type ActionTaken string

const (
    ActionNone    ActionTaken = "none"
    ActionCreated ActionTaken = "created"
    ActionUpdated ActionTaken = "updated"
    ActionDeleted ActionTaken = "deleted"
    ActionSkipped ActionTaken = "skipped"
)

type ResourceResult struct {
    ModuleName   string
    ResourceType string
    ResourceName string
    Action       ActionTaken
    Changed      bool
    Error        error
    DurationMs   int32
}

type Executor interface {
    // Execute checks current state, applies desired state if needed,
    // and returns the result. In dry-run mode, it only checks.
    Execute(ctx context.Context, params map[string]string, dryRun bool) ResourceResult
}
```

### Pattern 2: Kahn's Algorithm for DAG
**What:** Build adjacency list from `depends_on` declarations, compute in-degrees, BFS from zero-in-degree nodes. If result length != total nodes, a cycle exists.
**When to use:** Before executing any resources in a module set.
**Example:**
```go
// agent/runner/dag.go
func TopoSort(modules []Module) ([]Module, error) {
    inDegree := make(map[string]int)
    adj := make(map[string][]string)
    nodeMap := make(map[string]Module)

    for _, m := range modules {
        nodeMap[m.Name] = m
        if _, ok := inDegree[m.Name]; !ok {
            inDegree[m.Name] = 0
        }
        for _, dep := range m.DependsOn {
            adj[dep] = append(adj[dep], m.Name)
            inDegree[m.Name]++
        }
    }

    var queue []string
    for name, deg := range inDegree {
        if deg == 0 {
            queue = append(queue, name)
        }
    }

    var sorted []Module
    for len(queue) > 0 {
        name := queue[0]
        queue = queue[1:]
        sorted = append(sorted, nodeMap[name])
        for _, next := range adj[name] {
            inDegree[next]--
            if inDegree[next] == 0 {
                queue = append(queue, next)
            }
        }
    }

    if len(sorted) != len(modules) {
        return nil, fmt.Errorf("circular dependency detected among modules")
    }
    return sorted, nil
}
```

### Pattern 3: Notify/Subscribe Trigger Mechanism
**What:** After executing all resources in DAG order, collect resources that changed. Look up their `notify` lists. Re-execute notified resources (typically service restart/reload). `subscribes` is normalized to `notify` during construction.
**When to use:** After the main DAG execution pass.
**Example:**
```go
// During DAG construction, normalize subscribes -> notify
for _, res := range resources {
    for _, sub := range res.Subscribes {
        // Find the subscribed resource and add this resource to its notify list
        subscribedRes.Notify = append(subscribedRes.Notify, res.Name)
    }
}

// After execution pass
for _, result := range results {
    if result.Changed {
        for _, target := range notifyMap[result.ResourceName] {
            // Force re-execute target even if it would be a no-op
            reExecute(target, forceApply: true)
        }
    }
}
```

### Pattern 4: Agent Daemon Loop with Jitter
**What:** Agent sleeps for `base_interval +/- (base_interval * jitter_percent/100 * random[-1,1])`, then checks in with server.
**When to use:** Default agent mode (root command).
**Example:**
```go
func jitteredDuration(base time.Duration, jitterPct int) time.Duration {
    if jitterPct <= 0 {
        return base
    }
    maxJitter := float64(base) * float64(jitterPct) / 100.0
    jitter := (rand.Float64()*2 - 1) * maxJitter // [-maxJitter, +maxJitter]
    return base + time.Duration(jitter)
}
```

### Anti-Patterns to Avoid
- **Executing resources without checking state first:** Every executor MUST check current state and skip if already converged. This is the core idempotency guarantee.
- **Running notify targets in the main DAG pass:** Notify targets should only fire AFTER the main pass completes and only if their notifier changed. Running them inline causes ordering issues.
- **Using goroutines for resource execution:** Resources have explicit ordering via DAG. Parallel execution would violate `depends_on` semantics. Execute sequentially in topological order.
- **Parsing `when:` expressions with regex:** Use `text/template` (already in the codebase) to evaluate `when` expressions. The result after template execution should be compared to "true" or a non-empty/non-whitespace string.
- **Storing secret values in run results:** Per CLAUDE.md non-negotiable constraint, secrets must never appear in run results. File content that contains resolved secrets must not be logged or stored in `error_message` fields.

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| System facts collection | Custom /proc, /sys, sysctl parsers | `gopsutil/v4` | Cross-platform (Linux+macOS), handles distro detection, CPU topology, NIC enumeration |
| Exponential backoff | Custom retry loop with sleep | `cenkalti/backoff/v4` | Already in go.mod, handles max interval, max elapsed time, jitter, context cancellation |
| FQDN resolution | Manual DNS lookup + hostname parsing | `net.LookupAddr` + `os.Hostname()` | stdlib handles resolver config correctly |
| SHA256 file hashing | Custom buffered reader | `crypto/sha256` + `io.Copy` | stdlib, constant-memory streaming hash |
| Atomic file writes | Custom fsync + rename | `os.CreateTemp` + `os.Rename` + `os.Chmod` | Pattern is ~10 lines, no library needed, but must be in same filesystem |
| Topological sort | Graph library (gonum) | Hand-rolled Kahn's algorithm | ~50 lines of Go, no external dependency justified for this |
| User attribute lookup | Third-party /etc/passwd parser | `os/user.Lookup` + `os.ReadFile("/etc/passwd")` | stdlib for existence check; simple line parsing for uid/gid/shell/home comparison |

**Key insight:** The agent runs on managed nodes with minimal dependencies. Prefer stdlib where possible. The only justified external dependency for fact collection is `gopsutil` because cross-platform system introspection is genuinely complex.

## Common Pitfalls

### Pitfall 1: Atomic Rename Across Filesystems
**What goes wrong:** `os.Rename` fails with `EXDEV` (cross-device link) when temp file and target are on different mount points.
**Why it happens:** `os.CreateTemp("")` creates in `$TMPDIR` which may be a different filesystem (e.g., tmpfs) than the target path.
**How to avoid:** Always create the temp file in the SAME directory as the target: `os.CreateTemp(filepath.Dir(targetPath), ".isogov-*")`
**Warning signs:** Tests pass on dev machine but fail in containers or on hosts with separate `/tmp` filesystem.

### Pitfall 2: Package Manager Command Differences
**What goes wrong:** Assuming all package managers use the same arguments or exit codes.
**Why it happens:** `apt-get install` vs `dnf install` vs `apk add` vs `pacman -S` -- different flags, different query commands, different exit code semantics.
**How to avoid:** Each `PackageManager` implementation encapsulates the exact commands. Use the interface exclusively. Test with table-driven tests per backend.
**Warning signs:** Works on Ubuntu, fails on Alpine.

### Pitfall 3: Notify Firing Before Dependencies Complete
**What goes wrong:** A file change notifies a service restart, but the service depends on another resource that hasn't been applied yet.
**Why it happens:** Processing notify inline during the main execution pass.
**How to avoid:** Collect all notify triggers during execution. Process them AFTER the full DAG execution pass. Respect DAG ordering even for notify targets.
**Warning signs:** Service restarts before its config file is fully written.

### Pitfall 4: `when:` Template Evaluation Edge Cases
**What goes wrong:** Template evaluation returns `"<no value>"` instead of `"false"` when a fact key doesn't exist, causing the resource to not be skipped.
**Why it happens:** Go templates render missing map keys as `<no value>` by default.
**How to avoid:** Use `template.New("").Option("missingkey=zero")` to render missing keys as zero values. After rendering, trim whitespace and check for `"true"` -- anything else (including empty string, `"false"`, `"<no value>"`) is treated as false.
**Warning signs:** Resources execute on nodes that shouldn't match the `when` condition.

### Pitfall 5: Snapshot Directory Permissions
**What goes wrong:** Agent can't create snapshots in `/var/lib/isogov/snapshots/` because directory doesn't exist or permissions are wrong.
**Why it happens:** Fresh agent install doesn't have the directory structure.
**How to avoid:** `os.MkdirAll("/var/lib/isogov/snapshots", 0700)` on agent startup. Document that the agent runs as root (required for package/service/user management anyway).
**Warning signs:** First file write fails with "no such file or directory" for the snapshot.

### Pitfall 6: gRPC Client Certificate Handling
**What goes wrong:** Agent can't connect to server because mTLS requires client certificate.
**Why it happens:** Agent doesn't have a client cert signed by the server's CA. The current mTLS setup requires `tls.RequireAndVerifyClientCert`.
**How to avoid:** Agent needs the CA cert (for verifying server) and must present a client cert. For development, agent can use the same CA cert. Consider whether agent enrollment should generate a client cert or if the existing TLS setup needs adjustment for agent connections.
**Warning signs:** "certificate required" errors on gRPC connect.

### Pitfall 7: Facts Map Nesting for Templates
**What goes wrong:** Template `{{ .Facts.os.family }}` fails because facts are stored as a flat map, not nested.
**Why it happens:** Go template dot-notation traverses nested maps/structs. If facts are stored as `{"os.family": "linux"}` instead of `{"os": {"family": "linux"}}`, the template can't navigate.
**How to avoid:** Fact collectors should return nested maps: `{"os": {"family": "linux", "distro": "ubuntu"}}`. The existing `templateContext` already uses `map[string]any` which supports nesting.
**Warning signs:** Templates that reference `{{ .Facts.os.distro }}` return `<no value>`.

## Code Examples

### File Executor with SHA256 Check and Atomic Write
```go
// agent/modules/file.go
func (f *FileExecutor) Execute(ctx context.Context, params map[string]string, dryRun bool) ResourceResult {
    path := params["path"]
    desiredContent := params["content"]
    desiredMode := params["mode"]
    desiredOwner := params["owner"]
    desiredGroup := params["group"]

    // Check current state via SHA256
    currentHash, err := hashFile(path)
    desiredHash := sha256Hex([]byte(desiredContent))

    if err == nil && currentHash == desiredHash {
        // File exists with correct content -- check mode/owner
        if modeAndOwnerMatch(path, desiredMode, desiredOwner, desiredGroup) {
            return ResourceResult{Action: ActionNone, Changed: false}
        }
    }

    if dryRun {
        action := ActionCreated
        if err == nil { action = ActionUpdated }
        return ResourceResult{Action: action, Changed: true}
    }

    // Atomic write: temp file in same dir, then rename
    dir := filepath.Dir(path)
    tmp, err := os.CreateTemp(dir, ".isogov-*")
    if err != nil {
        return ResourceResult{Action: ActionNone, Error: err}
    }
    defer os.Remove(tmp.Name()) // cleanup on failure

    if _, err := tmp.Write([]byte(desiredContent)); err != nil {
        tmp.Close()
        return ResourceResult{Action: ActionNone, Error: err}
    }
    tmp.Close()

    // Set permissions before rename
    if err := os.Chmod(tmp.Name(), parseMode(desiredMode)); err != nil {
        return ResourceResult{Action: ActionNone, Error: err}
    }

    // Atomic rename
    if err := os.Rename(tmp.Name(), path); err != nil {
        return ResourceResult{Action: ActionNone, Error: err}
    }

    action := ActionCreated
    if currentHash != "" { action = ActionUpdated }
    return ResourceResult{Action: action, Changed: true}
}

func hashFile(path string) (string, error) {
    f, err := os.Open(path)
    if err != nil { return "", err }
    defer f.Close()
    h := sha256.New()
    if _, err := io.Copy(h, f); err != nil { return "", err }
    return hex.EncodeToString(h.Sum(nil)), nil
}
```

### PackageManager Interface and Auto-Detection
```go
// agent/modules/pkgmgr/interface.go
type PackageManager interface {
    IsInstalled(ctx context.Context, name string) (installed bool, version string, err error)
    Install(ctx context.Context, name, version string) error
    Remove(ctx context.Context, name string) error
    Upgrade(ctx context.Context, name, version string) error
}

// agent/modules/pkgmgr/detect.go
func Detect(osFamily, osDistro string) (PackageManager, error) {
    switch {
    case osDistro == "ubuntu" || osDistro == "debian" || osDistro == "mint":
        return &AptManager{}, nil
    case osDistro == "fedora" || osDistro == "rhel" || osDistro == "rocky" || osDistro == "alma":
        return &DnfManager{}, nil
    case osDistro == "centos":
        return &YumManager{}, nil  // CentOS 7; CentOS Stream uses dnf
    case osDistro == "alpine":
        return &ApkManager{}, nil
    case osDistro == "arch" || osDistro == "manjaro":
        return &PacmanManager{}, nil
    case osFamily == "darwin":
        return &BrewManager{}, nil
    case osDistro == "opensuse" || osDistro == "sles":
        return &ZypperManager{}, nil
    default:
        return nil, fmt.Errorf("unsupported distro %q (family: %q)", osDistro, osFamily)
    }
}
```

### Fact Collector Interface
```go
// agent/facts/collector.go
type Collector interface {
    Name() string
    Collect(ctx context.Context) (map[string]any, error)
}

func CollectAll(ctx context.Context, collectors []Collector) (map[string]any, error) {
    facts := make(map[string]any)
    for _, c := range collectors {
        result, err := c.Collect(ctx)
        if err != nil {
            slog.Warn("fact collector failed", "collector", c.Name(), "error", err)
            continue // Non-fatal: continue collecting other facts
        }
        facts[c.Name()] = result
    }
    return facts, nil
}
```

### Service Executor with systemctl
```go
// agent/modules/service.go
func (s *ServiceExecutor) Execute(ctx context.Context, params map[string]string, dryRun bool) ResourceResult {
    name := params["name"]
    desiredState := params["state"]   // running, stopped, restarted, reloaded
    enabledStr := params["enabled"]   // "true" or "false"

    isActive := s.isActive(ctx, name)
    isEnabled := s.isEnabled(ctx, name)

    switch desiredState {
    case "running":
        if isActive {
            return ResourceResult{Action: ActionNone, Changed: false}
        }
        if dryRun { return ResourceResult{Action: ActionUpdated, Changed: true} }
        return s.runSystemctl(ctx, "start", name)
    case "stopped":
        if !isActive {
            return ResourceResult{Action: ActionNone, Changed: false}
        }
        if dryRun { return ResourceResult{Action: ActionUpdated, Changed: true} }
        return s.runSystemctl(ctx, "stop", name)
    case "restarted":
        // Always restart (typically triggered by notify)
        if dryRun { return ResourceResult{Action: ActionUpdated, Changed: true} }
        return s.runSystemctl(ctx, "restart", name)
    case "reloaded":
        if dryRun { return ResourceResult{Action: ActionUpdated, Changed: true} }
        return s.runSystemctl(ctx, "reload", name)
    }
    return ResourceResult{Error: fmt.Errorf("unknown service state: %s", desiredState)}
}

func (s *ServiceExecutor) isActive(ctx context.Context, name string) bool {
    err := exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", name).Run()
    return err == nil // exit 0 = active
}
```

### When Conditional Evaluation
```go
// agent/runner/when.go
func EvaluateWhen(expr string, facts map[string]any) (bool, error) {
    if expr == "" {
        return true, nil // No condition = always run
    }

    tmpl, err := template.New("when").Option("missingkey=zero").Parse(expr)
    if err != nil {
        return false, fmt.Errorf("parsing when expression %q: %w", expr, err)
    }

    var buf bytes.Buffer
    ctx := struct{ Facts map[string]any }{Facts: facts}
    if err := tmpl.Execute(&buf, ctx); err != nil {
        return false, fmt.Errorf("evaluating when expression %q: %w", expr, err)
    }

    result := strings.TrimSpace(buf.String())
    return result == "true", nil
}
```

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| `math/rand` with `rand.Seed()` | `math/rand/v2` (auto-seeded) | Go 1.22 (Feb 2024) | No manual seeding needed for jitter |
| `os/user` with cgo requirement | `os/user` pure Go fallback | Go 1.19+ | Works in cross-compiled binaries without cgo |
| `gopsutil/v3` | `gopsutil/v4` (already in go.mod) | 2024 | v4 requires Go 1.18+, cleaner API, better Windows support |
| `ioutil.ReadFile` / `ioutil.TempFile` | `os.ReadFile` / `os.CreateTemp` | Go 1.16 (Feb 2021) | `ioutil` deprecated; use `os` package equivalents |

**Deprecated/outdated:**
- `math/rand.Seed()`: Auto-seeded in Go 1.20+, explicit seeding deprecated
- `ioutil` package: Fully deprecated; use `os` and `io` equivalents
- `gopsutil/v3`: v4 is current; v3 still works but v4 is already in the project

## Open Questions

1. **Agent mTLS client certificate**
   - What we know: Server requires client certs (`tls.RequireAndVerifyClientCert`). Agent has an ED25519 keypair for signing but this is not a TLS client cert.
   - What's unclear: How does the agent present a valid client cert for mTLS? Does it use the CA cert as both client and server cert in dev mode? Does enrollment generate a client cert?
   - Recommendation: For Phase 2, use the same approach as the REST gateway in Phase 1 -- the gateway already reuses the server TLS cert as the mTLS client cert to the gRPC backend (decision from 01-05). The agent in dev can do the same. Production client cert management can be addressed later. Alternatively, the server TLS config may need to be relaxed to `tls.RequireAnyClientCert` or `tls.VerifyClientCertIfGiven` if agent certs are self-signed, since the ED25519 signature provides the real authentication.

2. **Proto schema: resource-level vs module-level metadata**
   - What we know: Current proto `Module` message has `notify`, `subscribes`, `when` at module level. The handoff spec shows these at individual resource level within a module.
   - What's unclear: Current `configToProtoModules` copies resource-level notify/subscribes/when to module level, which loses per-resource granularity.
   - Recommendation: The proto `Resource` message needs `notify`, `subscribes`, `when`, and `depends_on` fields added. The `Module` message keeps its fields for module-level dependencies. The agent DAG operates at the resource level within each module. This is a proto schema update that should happen in Phase 2.

3. **Exec resource not in proto Resource types**
   - What we know: The handoff spec mentions `exec` type in CONV-06 but the proto `Resource.type` comments only list file/package/service/user.
   - What's unclear: Whether `exec` was intentionally omitted or is an oversight.
   - Recommendation: Add `exec` as a valid resource type. It's listed in the requirements (CONV-06) and is essential for configuration management (running arbitrary commands with idempotency guards).

## Validation Architecture

### Test Framework
| Property | Value |
|----------|-------|
| Framework | Go testing + testcontainers-go (PostgreSQL) |
| Config file | None (Go convention: `*_test.go` files) |
| Quick run command | `go test ./agent/... -v -race -count=1` |
| Full suite command | `go test ./... -v -race -count=1` |

### Phase Requirements -> Test Map
| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|-------------|
| CONV-01 | File SHA256 check before write | unit | `go test ./agent/modules/... -run TestFileExecutorSHA256 -v` | No -- Wave 0 |
| CONV-02 | Atomic write-to-temp-then-rename | unit | `go test ./agent/modules/... -run TestFileExecutorAtomicWrite -v` | No -- Wave 0 |
| CONV-03 | Package manager installed check | unit | `go test ./agent/modules/... -run TestPackageExecutor -v` | No -- Wave 0 |
| CONV-04 | Service systemctl check | unit | `go test ./agent/modules/... -run TestServiceExecutor -v` | No -- Wave 0 |
| CONV-05 | User /etc/passwd check | unit | `go test ./agent/modules/... -run TestUserExecutor -v` | No -- Wave 0 |
| CONV-06 | Exec with creates/onlyif/unless | unit | `go test ./agent/modules/... -run TestExecExecutor -v` | No -- Wave 0 |
| CONV-07 | DAG topological sort | unit | `go test ./agent/runner/... -run TestTopoSort -v` | No -- Wave 0 |
| CONV-08 | Circular dep detection | unit | `go test ./agent/runner/... -run TestCircularDep -v` | No -- Wave 0 |
| CONV-09 | Notify triggers re-apply | unit | `go test ./agent/runner/... -run TestNotify -v` | No -- Wave 0 |
| CONV-10 | Subscribes normalization | unit | `go test ./agent/runner/... -run TestSubscribes -v` | No -- Wave 0 |
| CONV-11 | Service states (running/stopped/restarted/reloaded) | unit | `go test ./agent/modules/... -run TestServiceStates -v` | No -- Wave 0 |
| CONV-12 | Action taken recording | unit | `go test ./agent/modules/... -run TestActionTaken -v` | No -- Wave 0 |
| CONF-09 | when: expression evaluation | unit | `go test ./agent/runner/... -run TestWhenEval -v` | No -- Wave 0 |
| CONF-10 | when: false = skipped | unit | `go test ./agent/runner/... -run TestWhenSkipped -v` | No -- Wave 0 |
| AGNT-01 | Jittered pull loop | unit | `go test ./agent/checkin/... -run TestJitter -v` | No -- Wave 0 |
| AGNT-02 | Exponential backoff | unit | `go test ./agent/checkin/... -run TestBackoff -v` | No -- Wave 0 |
| AGNT-03 | Fact collectors | unit | `go test ./agent/facts/... -run TestCollect -v` | No -- Wave 0 |
| AGNT-04 | Facts sent with check-in | integration | `go test ./agent/checkin/... -run TestFactsCheckIn -v` | No -- Wave 0 |
| AGNT-05 | Pluggable Collector interface | unit | `go test ./agent/facts/... -run TestCollectorInterface -v` | No -- Wave 0 |
| AGNT-06 | Dry-run mode | integration | `go test ./agent/... -run TestDryRun -v` | No -- Wave 0 |
| AGNT-07 | One-shot run | integration | `go test ./agent/... -run TestOneShotRun -v` | No -- Wave 0 |
| AGNT-08 | Single-module run | unit | `go test ./agent/runner/... -run TestSingleModule -v` | No -- Wave 0 |
| AGNT-09 | File snapshots | unit | `go test ./agent/snapshots/... -run TestSnapshot -v` | No -- Wave 0 |
| INFRA-06 | Package manager detection | unit | `go test ./agent/modules/pkgmgr/... -run TestDetect -v` | No -- Wave 0 |

### Sampling Rate
- **Per task commit:** `go test ./agent/... -v -race -count=1`
- **Per wave merge:** `go test ./... -v -race -count=1`
- **Phase gate:** Full suite green before `/gsd:verify-work`

### Wave 0 Gaps
- [ ] `agent/modules/executor_test.go` -- executor interface tests, shared test helpers
- [ ] `agent/modules/file_test.go` -- covers CONV-01, CONV-02
- [ ] `agent/modules/package_test.go` -- covers CONV-03 (with mock PackageManager)
- [ ] `agent/modules/service_test.go` -- covers CONV-04, CONV-11 (with mock systemctl)
- [ ] `agent/modules/user_test.go` -- covers CONV-05
- [ ] `agent/modules/exec_test.go` -- covers CONV-06
- [ ] `agent/runner/dag_test.go` -- covers CONV-07, CONV-08
- [ ] `agent/runner/runner_test.go` -- covers CONV-09, CONV-10, CONV-12
- [ ] `agent/runner/when_test.go` -- covers CONF-09, CONF-10
- [ ] `agent/facts/collector_test.go` -- covers AGNT-03, AGNT-05
- [ ] `agent/checkin/loop_test.go` -- covers AGNT-01, AGNT-02
- [ ] `agent/snapshots/manager_test.go` -- covers AGNT-09
- [ ] `agent/modules/pkgmgr/detect_test.go` -- covers INFRA-06

Note: Service, user, and exec executor tests should use interface-based mocking for subprocess calls (`exec.Command`) to avoid requiring root or real systemd. The file executor tests can use `os.MkdirTemp` for real filesystem operations.

## Sources

### Primary (HIGH confidence)
- Existing codebase: `proto/isogov.proto`, `server/config/types.go`, `server/grpc/checkin.go`, `server/db/models.go` -- verified Phase 1 implementation
- `isogov-handoff.md` -- full specification with exact proto definitions, DB schema, module format, agent behavior, fact collection, DAG, notify/subscribe, snapshots, dry-run

### Secondary (MEDIUM confidence)
- [gopsutil v4 documentation](https://pkg.go.dev/github.com/shirou/gopsutil/v4) -- system facts collection API
- [Atomic file writes in Go](https://michael.stapelberg.ch/posts/2017-01-28-golang_atomically_writing/) -- atomic rename pattern
- [renameio package](https://pkg.go.dev/github.com/google/renameio) -- reference for atomic file write edge cases
- [Kahn's Algorithm](https://en.wikipedia.org/wiki/Topological_sorting) -- topological sort algorithm
- [Go os/user package](https://pkg.go.dev/os/user) -- user lookup API

### Tertiary (LOW confidence)
- None -- all findings verified against codebase or official documentation

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH -- all libraries already in go.mod or stdlib; gopsutil is an indirect dep, needs to be promoted to direct
- Architecture: HIGH -- follows patterns established in Phase 1 and handoff spec; executor interface, DAG, and agent loop are well-understood patterns
- Pitfalls: HIGH -- based on real Go runtime behavior and POSIX filesystem semantics; verified against existing codebase decisions

**Research date:** 2026-03-05
**Valid until:** 2026-04-05 (stable domain; Go stdlib and gopsutil are mature)
