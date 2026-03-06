---
phase: 02-convergence
verified: 2026-03-06T03:00:00Z
status: passed
score: 5/5 must-haves verified
---

# Phase 2: Convergence Verification Report

**Phase Goal:** Agents pull their config from the server, build a dependency DAG, and idempotently converge managed resources (files, packages, services, users, exec) with notify/subscribe triggers, conditional execution, fact-driven templates, and full result reporting
**Verified:** 2026-03-06T03:00:00Z
**Status:** passed
**Re-verification:** No -- initial verification

## Goal Achievement

### Observable Truths

| # | Truth | Status | Evidence |
|---|-------|--------|----------|
| 1 | Agent runs as a daemon with jittered pull loop, checks in with the server, receives its compiled catalog, and executes resources in topological order -- circular dependencies are rejected at load time | VERIFIED | `agent/checkin/loop.go` Loop() function implements jittered pull loop with facts collection, check-in, RunOnce, and jittered sleep. `agent/runner/dag.go` TopoSort() implements Kahn's algorithm; circular deps return error naming participants (line 175). `agent/checkin/backoff.go` provides exponential backoff (5s-5m, never stops). All wired through `agent/main.go` rootCmd. |
| 2 | File, package, service, user, and exec resources all check current state before acting and skip when already converged -- every action is recorded as none/create/update/delete/skip | VERIFIED | `agent/modules/file.go` checks SHA256 hash + mode before writing (ActionNone on match). `agent/modules/package.go` calls PkgMgr.IsInstalled before install/remove. `agent/modules/service.go` calls isActive/isEnabled before systemctl. `agent/modules/user.go` parses /etc/passwd via lookupUser. `agent/modules/exec.go` checks creates/onlyif/unless guards. All return ResourceResult with ActionTaken from {none, created, updated, deleted, skipped}. Compile-time interface checks in all test files. |
| 3 | Changing a file resource triggers a service restart via notify/subscribe, and resources with when: expressions that evaluate to false are skipped | VERIFIED | `agent/runner/runner.go` executeNotifyPass() collects changed resources and re-executes notify targets. `agent/runner/dag.go` BuildDAG() normalizes subscribes to notify (lines 91-100). `agent/runner/runner.go` executeResource() calls EvaluateWhen() and returns ActionSkipped on false (lines 98-108). Notify pass skipped during dry-run (line 80). |
| 4 | Agent collects OS, hardware, network, and package facts via pluggable collectors, sends them with every check-in, and facts are available as template variables in config rendering | VERIFIED | `agent/facts/collector.go` Collector interface + CollectAll + DefaultCollectors (4 collectors). `agent/facts/os.go` uses gopsutil host.Info. `agent/facts/hardware.go` uses gopsutil cpu/mem/disk. `agent/facts/network.go` uses os.Hostname + gopsutil net.Interfaces. `agent/facts/packages.go` uses dpkg-query/rpm/brew. `agent/checkin/loop.go` Loop() calls facts.CollectAll then client.ReportFacts every iteration (lines 138-149). `server/grpc/checkin.go` reads GetLatestClientFacts and passes to configToProtoModules (lines 56-74). `server/grpc/facts.go` stores JSONB and prunes to 10. |
| 5 | Agent supports --dry-run for preview, run for one-shot convergence, run --module for single-module apply, and saves file snapshots before overwrites | VERIFIED | `agent/main.go` rootCmd (daemon) has --dry-run flag (line 354), runCmd has --module and --dry-run flags (lines 356-357). runCmd calls RunOnce with dryRun/module params (line 239). `agent/snapshots/manager.go` Save/Restore/Prune with MaxPerPath=5 (line 121). `agent/modules/file.go` calls SnapshotMgr.Save before overwrite (lines 142-147). |

**Score:** 5/5 truths verified

### Required Artifacts

| Artifact | Expected | Status | Details |
|----------|----------|--------|---------|
| `agent/modules/executor.go` | Executor interface, ActionTaken, ResourceResult, CommandRunner | VERIFIED | 48 lines. All types exported. Interface with Execute(ctx, params, dryRun). 5 ActionTaken constants. |
| `agent/modules/file.go` | FileExecutor with SHA256 idempotency and atomic writes | VERIFIED | 213 lines. SHA256 check, atomic temp-rename in same dir, snapshot integration, state=present/absent. |
| `agent/modules/package.go` | Package resource executor | VERIFIED | 104 lines. Uses PackageManager interface, install/remove/upgrade with version pinning. |
| `agent/modules/service.go` | Service executor with systemctl | VERIFIED | 134 lines. isActive/isEnabled checks, 4 states (running/stopped/restarted/reloaded), enable/disable. |
| `agent/modules/user.go` | User executor with /etc/passwd parsing | VERIFIED | 226 lines. lookupUser parses passwd file, useradd/usermod/userdel, attribute comparison. |
| `agent/modules/exec.go` | Exec executor with idempotency guards | VERIFIED | 103 lines. creates/onlyif/unless guards in correct order, dry-run skips command guards. |
| `agent/modules/pkgmgr/interface.go` | PackageManager interface | VERIFIED | 39 lines. IsInstalled/Install/Remove/Upgrade + CommandRunner abstraction. |
| `agent/modules/pkgmgr/detect.go` | Auto-detection of package manager | VERIFIED | 40 lines. Maps osFamily+osDistro to correct backend. 7 distro groups. |
| `agent/modules/pkgmgr/apt.go` | Apt backend | VERIFIED | 60 lines. dpkg -s query, apt-get install/remove. |
| `agent/modules/pkgmgr/dnf.go` | Dnf backend | VERIFIED | 48 lines. rpm -q query, dnf install/remove. |
| `agent/modules/pkgmgr/yum.go` | Yum backend | VERIFIED | 48 lines. rpm -q query, yum install/remove. |
| `agent/modules/pkgmgr/apk.go` | Apk backend | VERIFIED | 50 lines. apk info -e query, apk add/del. |
| `agent/modules/pkgmgr/pacman.go` | Pacman backend | VERIFIED | 48 lines. pacman -Q query, pacman -S/-R --noconfirm. |
| `agent/modules/pkgmgr/brew.go` | Brew backend | VERIFIED | 50 lines. brew list query, brew install/uninstall. |
| `agent/modules/pkgmgr/zypper.go` | Zypper backend | VERIFIED | 58 lines. zypper se -i query, zypper install/remove -y. |
| `agent/facts/collector.go` | Collector interface and CollectAll | VERIFIED | 48 lines. Interface, CollectAll with error tolerance, DefaultCollectors. |
| `agent/facts/os.go` | OS facts collector | VERIFIED | 32 lines. gopsutil host.Info, runtime.GOARCH. Returns family/distro/version/kernel/arch. |
| `agent/facts/hardware.go` | Hardware facts collector | VERIFIED | 51 lines. gopsutil cpu.Counts/mem.VirtualMemory/disk.Partitions. Returns cpu_count/memory_mb/disk_gb. |
| `agent/facts/network.go` | Network facts collector | VERIFIED | 88 lines. os.Hostname, net.LookupAddr for FQDN, gopsutil net.Interfaces. Returns hostname/fqdn/ip_addresses/mac_addresses. |
| `agent/facts/packages.go` | Package facts collector | VERIFIED | 109 lines. dpkg-query/rpm/brew query. Returns installed package map. Non-fatal on error. |
| `agent/runner/when.go` | When conditional evaluator | VERIFIED | 42 lines. Go template with missingkey=zero, trims whitespace, only "true" is true. |
| `agent/runner/dag.go` | DAG construction and topological sort | VERIFIED | 205 lines. BuildDAG flattens modules, normalizes subscribes to notify. TopoSort via Kahn's with cycle detection. |
| `agent/runner/runner.go` | Runner with DAG execution, when, notify | VERIFIED | 198 lines. Run() does BuildDAG+TopoSort, main pass with when eval, notify pass for changed resources. Module filter, dry-run, error isolation. |
| `agent/snapshots/manager.go` | Snapshot save/restore/prune | VERIFIED | 135 lines. SHA256 path hash prefix, Save/Restore/Prune with MaxPerPath. Atomic restore via rename. |
| `agent/checkin/loop.go` | Daemon pull loop with jitter | VERIFIED | 199 lines. jitteredDuration, stateFingerprint, RunOnce, Loop with facts-first flow. |
| `agent/checkin/backoff.go` | Exponential backoff | VERIFIED | 19 lines. cenkalti/backoff configured: 5s initial, 5m max, never stop, 2x multiplier. |
| `agent/checkin/client.go` | gRPC client with ED25519 signing | VERIFIED | 155 lines. NewClient with mTLS, CheckIn/Register/ReportResult/ReportFacts all sign with ED25519. |
| `agent/main.go` | Cobra CLI with daemon/run/register | VERIFIED | 364 lines. rootCmd (daemon), runCmd (--module, --dry-run), registerCmd (--token). All executors wired. Signal context. |
| `server/grpc/report.go` | ReportResult RPC handler | VERIFIED | 99 lines. Stores per-module results, computes run status, finishes run with counts. |
| `server/grpc/facts.go` | ReportFacts RPC handler | VERIFIED | 63 lines. Stores JSONB facts, prunes to 10 snapshots. JSON validation. |
| `server/db/queries/facts.sql` | sqlc queries for facts | VERIFIED | 22 lines. InsertClientFacts, GetLatestClientFacts, PruneClientFacts with table aliases. |
| `server/db/queries/snapshots.sql` | sqlc queries for snapshots | VERIFIED | 11 lines. InsertFileSnapshot, ListFileSnapshots. |

### Key Link Verification

| From | To | Via | Status | Details |
|------|----|-----|--------|---------|
| `agent/modules/file.go` | `agent/modules/executor.go` | implements Executor interface | WIRED | Compile-time check: `var _ Executor = (*FileExecutor)(nil)` in test |
| `agent/modules/file.go` | `agent/snapshots/manager.go` | calls Manager.Save before overwrite | WIRED | `f.SnapshotMgr.Save(path, f.RunID)` on line 143 |
| `agent/runner/when.go` | `text/template` | Go template with missingkey=zero | WIRED | `template.New("when").Option("missingkey=zero").Parse(expr)` on line 27 |
| `agent/modules/package.go` | `agent/modules/pkgmgr/interface.go` | uses PackageManager interface | WIRED | `p.PkgMgr.IsInstalled`, `p.PkgMgr.Install`, etc. throughout |
| `agent/modules/pkgmgr/detect.go` | `agent/modules/pkgmgr/apt.go` | returns AptManager for debian/ubuntu | WIRED | `return &AptManager{}, nil` for ubuntu/debian/mint |
| `agent/modules/service.go` | `os/exec` | runs systemctl commands | WIRED | `s.runner().Run(ctx, "systemctl", ...)` in isActive, isEnabled, Execute |
| `agent/runner/runner.go` | `agent/modules/executor.go` | calls Executor.Execute | WIRED | `executor.Execute(ctx, node.Params, r.DryRun)` on line 119 |
| `agent/runner/runner.go` | `agent/runner/dag.go` | uses TopoSort for ordering | WIRED | `BuildDAG(protoModules)` line 49, `TopoSort(nodes)` line 55 |
| `agent/runner/runner.go` | `agent/runner/when.go` | evaluates when conditions | WIRED | `EvaluateWhen(node.When, r.Facts)` on line 99 |
| `agent/checkin/loop.go` | `agent/checkin/client.go` | calls Client methods | WIRED | `client.CheckIn`, `client.ReportResult`, `client.ReportFacts` in RunOnce/Loop |
| `agent/checkin/loop.go` | `agent/runner/runner.go` | creates Runner and calls Run | WIRED | `runner.NewRunner(executors, factsMap, dryRun, module)` then `r.Run(ctx, resp.Modules)` in RunOnce |
| `agent/checkin/loop.go` | `agent/facts/collector.go` | calls CollectAll before check-in | WIRED | `facts.CollectAll(ctx, facts.DefaultCollectors())` on line 138 |
| `agent/checkin/backoff.go` | `cenkalti/backoff/v4` | exponential backoff | WIRED | `backoff.NewExponentialBackOff()` on line 12 |
| `agent/main.go` | `agent/checkin/loop.go` | starts daemon loop | WIRED | `checkin.Loop(ctx, client, executors, snapshotMgr)` line 184, `checkin.RunOnce(...)` line 239 |
| `server/grpc/facts.go` | `server/db` | stores facts JSONB | WIRED | `s.queries.InsertClientFacts`, `s.queries.PruneClientFacts` |
| `server/grpc/checkin.go` | `server/db` | reads latest facts for templates | WIRED | `s.queries.GetLatestClientFacts(ctx, clientID)` line 56, passed to `configToProtoModules(cfg, facts, vars)` line 74 |

### Requirements Coverage

| Requirement | Source Plan | Description | Status | Evidence |
|-------------|-----------|-------------|--------|----------|
| CONV-01 | 02-01 | File resource checks SHA256 hash before writing, skips if match | SATISFIED | `file.go` fileHash + desiredHash comparison, returns ActionNone on match |
| CONV-02 | 02-01 | File resource uses atomic write-to-temp-then-rename | SATISFIED | `file.go` os.CreateTemp in filepath.Dir, os.Rename |
| CONV-03 | 02-02 | Package executor queries package manager for installed state | SATISFIED | `package.go` calls PkgMgr.IsInstalled before Install/Remove |
| CONV-04 | 02-02 | Service executor checks systemctl is-active/is-enabled | SATISFIED | `service.go` isActive/isEnabled via systemctl --quiet |
| CONV-05 | 02-02 | User executor parses /etc/passwd for existence and attributes | SATISFIED | `user.go` lookupUser parses passwd file fields |
| CONV-06 | 02-02 | Exec executor runs commands with creates/onlyif/unless guards | SATISFIED | `exec.go` guard order: creates, onlyif, unless |
| CONV-07 | 02-04 | Module dependencies form DAG with topological ordering via Kahn's | SATISFIED | `dag.go` TopoSort with in-degree computation, BFS |
| CONV-08 | 02-04 | Circular dependencies detected with clear error message | SATISFIED | `dag.go` line 175: "circular dependency detected among resources: [names]" |
| CONV-09 | 02-04 | Resources support notify list triggering re-application | SATISFIED | `runner.go` executeNotifyPass re-executes targets of changed resources |
| CONV-10 | 02-04 | Resources support subscribes (normalized to notify) | SATISFIED | `dag.go` lines 91-100 normalize subscribes to notify during BuildDAG |
| CONV-11 | 02-02 | Service resource supports running, stopped, restarted, reloaded | SATISFIED | `service.go` switch on state with all 4 cases |
| CONV-12 | 02-01 | All executors record action_taken (none/create/update/delete/skip) | SATISFIED | ActionTaken type with 5 constants, all executors return ResourceResult |
| CONF-09 | 02-01 | Resources support when: field as Go template expression | SATISFIED | `when.go` EvaluateWhen with template evaluation against Facts |
| CONF-10 | 02-01 | Resources where when evaluates to false are skipped | SATISFIED | `runner.go` line 105-107: ActionSkipped when !shouldRun |
| AGNT-01 | 02-05 | Agent runs as daemon with jittered pull loop | SATISFIED | `loop.go` Loop() with jitteredDuration from server response |
| AGNT-02 | 02-05 | Agent handles server unavailability with exponential backoff | SATISFIED | `backoff.go` NewBackoff: 5s-5m, never stop. `loop.go` uses bo.NextBackOff on error |
| AGNT-03 | 02-03 | Agent collects OS/hardware/network/package facts | SATISFIED | 4 collectors in agent/facts/ using gopsutil |
| AGNT-04 | 02-03 | Facts sent with every check-in, stored in DB, available as templates | SATISFIED | Loop sends via ReportFacts, server stores JSONB, CheckIn reads for RenderTemplate |
| AGNT-05 | 02-03 | Pluggable fact collectors via Collector interface | SATISFIED | Collector interface with Name/Collect, CollectAll aggregator, DefaultCollectors |
| AGNT-06 | 02-05 | Agent supports --dry-run flag | SATISFIED | `main.go` --dry-run on root and run commands |
| AGNT-07 | 02-05 | Agent supports isogov-agent run for one-shot convergence | SATISFIED | `main.go` runCmd calls RunOnce and exits |
| AGNT-08 | 02-05 | Agent supports run --module for single-module apply | SATISFIED | `main.go` runCmd --module flag, passed to RunOnce |
| AGNT-09 | 02-01 | File snapshots saved before overwrites (last 5 per path) | SATISFIED | `snapshots/manager.go` Save + Prune with MaxPerPath=5 |
| INFRA-06 | 02-02 | Multi-platform PackageManager interface (7 backends) | SATISFIED | apt/dnf/yum/apk/pacman/brew/zypper in pkgmgr/ |

**24/24 requirements SATISFIED. 0 orphaned.**

### Anti-Patterns Found

| File | Line | Pattern | Severity | Impact |
|------|------|---------|----------|--------|
| `agent/modules/file.go` | 33-34 | "placeholder" in comments for owner/group param docs | Info | Documentation only -- owner/group enforcement deferred as noted in CLAUDE.md (non-root limitation). Not a code stub. |

### Human Verification Required

### 1. End-to-End Agent-Server Convergence

**Test:** Start server and PostgreSQL via `docker compose up`, generate certs, register an agent, create a client YAML with a file resource, and run `isogov-agent run`.
**Expected:** Agent checks in, receives module config, creates the file with correct content and permissions, and reports results back to server. Second run should show action_taken=none.
**Why human:** Requires running infrastructure (server, DB, TLS) that cannot be verified via static analysis.

### 2. Daemon Loop Behavior Under Server Failure

**Test:** Start agent daemon, then stop the server. Observe agent logs.
**Expected:** Agent logs warning, applies exponential backoff (5s, 10s, 20s...), never crashes. When server comes back, agent resumes normal check-in cycle.
**Why human:** Requires observing real-time daemon behavior and network error handling.

### 3. Notify/Subscribe Trigger Chain

**Test:** Create a config with a file resource (nginx.conf) that notifies a service resource (nginx), deploy it, and observe that changing the file content triggers a service restart.
**Expected:** First run: file created + service started. Second run with changed content: file updated + service restarted via notify.
**Why human:** Requires running systemd services and observing service state transitions.

### Gaps Summary

No gaps found. All 5 success criteria from the ROADMAP are verified against the actual codebase:

1. **Daemon with DAG execution** -- Loop/RunOnce/Runner/TopoSort all wired and substantive
2. **Five idempotent executors** -- file/package/service/user/exec all check-before-act with ActionTaken recording
3. **Notify/subscribe and when conditions** -- BuildDAG normalizes subscribes, Runner evaluates when and fires notify pass
4. **Fact collection and template rendering** -- 4 collectors, JSONB storage, CheckIn reads facts for RenderTemplate
5. **CLI modes and snapshots** -- daemon/run/run --module/--dry-run all wired, snapshot manager prunes to 5

Build verification: `go build ./...` succeeds, `make build` produces all 3 binaries, `go test ./agent/... -short` passes all 7 test packages (103+ tests across modules, pkgmgr, runner, facts, snapshots, checkin, auth).

---

_Verified: 2026-03-06T03:00:00Z_
_Verifier: Claude (gsd-verifier)_
