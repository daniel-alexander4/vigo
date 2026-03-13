# Stack Research

**Domain:** Orchestration additions to a Go configuration management system (v1.3)
**Researched:** 2026-03-11
**Confidence:** HIGH

This document covers only NEW stack additions for v1.3 orchestration features. The base stack
(Go + gRPC over mTLS + SQLite + grpc-gateway + cobra) is already built and validated through v1.2.

---

## What v1.3 Needs

Five orchestration capabilities drive stack decisions:

1. **Ad-hoc command execution** — dispatch a shell command to N thralls, collect stdout/stderr
2. **One-off task/script push** — push a script, execute it, collect structured results
3. **Multi-step workflows** — conditional multi-stage: "if step A succeeds on group1, do step B on group2"
4. **Rolling execution** — batch size, failure thresholds, inter-batch health checks
5. **Live query** — ask thralls for current state without waiting for next check-in

---

## Key Architecture Decision: In-Process vs External

**Verdict: In-process. No external queue, no external workflow engine.**

Rationale:

- Hobnob already has bidirectional gRPC streaming (`AgentStream` RPC) that handles server-to-agent push. The force-push mechanism already proves the end-to-end path: server signals agent via `streamManager`, agent acts, agent reports back. Orchestration is the same path with richer message types.
- External queues (Redis, NATS, Kafka) add operational overhead for what is effectively in-memory fan-out across a map of open gRPC streams. The `streamManager` already IS a task queue for connected agents.
- External workflow engines (Temporal, Cadence, Durable Task) solve distributed workflow state across multiple services. Hobnob is a single server process. The added complexity exceeds the benefit.
- SQLite + in-memory state handles the required durability: task records persisted to DB (audit trail), in-flight coordination in memory (fast, simple).

The right model: **task record in SQLite + dispatch via existing streamManager + result aggregation via in-process pub/sub.**

---

## Recommended Stack Additions

### Core Orchestration: No New Libraries Required

The existing stack already contains everything needed for dispatch and execution:

| Existing Capability | How It Serves Orchestration |
|--------------------|-----------------------------|
| `streamManager` (server/grpc/streams.go) | Delivers messages to connected agents. Already handles fan-out. |
| Bidirectional `AgentStream` RPC | Carries task dispatch (server→agent) and result reporting (agent→server). |
| `RunStore` interface (server/db/runstore.go) | Extend to store task/workflow records. Two backends already exist. |
| SQLite + sqlc | Persist tasks, workflow state, rolling execution progress. |
| `google/uuid` | Task and workflow ID generation. Already imported. |
| `context.Context` + cancellation | Task timeout and cancellation. Already the Go standard. |

**No new core libraries are needed.** All orchestration wiring uses stdlib goroutines, channels, and the existing gRPC stream infrastructure.

### New: golang.org/x/sync

| Library | Version | Purpose | Why |
|---------|---------|---------|-----|
| golang.org/x/sync/errgroup | v0.20.0 | Concurrent per-thrall dispatch with error propagation | `errgroup.WithContext` + `g.SetLimit(N)` provides bounded concurrency for rolling execution batch dispatch. One goroutine per thrall, auto-cancel on first fatal error, configurable batch cap. Already a transitive dependency — just needs an explicit import. |
| golang.org/x/sync/semaphore | v0.20.0 | Weighted concurrency gate for rolling execution | `semaphore.NewWeighted(batchSize)` gates how many thralls execute simultaneously in a rolling run. Acquire before dispatch, release on result receipt. |

`golang.org/x/sync` is already in `go.sum` as a transitive dependency. Bump to v0.20.0 (Feb 2026) and add a direct `require` entry.

**Why errgroup over manual WaitGroup + errCh:** errgroup's `WithContext` returns a derived context that is cancelled when any goroutine returns an error. For workflow steps ("if step A fails, cancel step B"), this is exactly the semantics needed. Manual WaitGroup requires implementing this yourself.

**Why semaphore over channel-based counting:** `semaphore.Weighted` allows future weight-based control (e.g., dispatch to large thralls costs more than small ones). Channel counting works too but semaphore is idiomatic for this pattern.

### New: Result Fan-Out Broker (In-Process, No Library)

For "wait for results from N thralls while streaming progress to the CLI/UI," the pattern is a task result broker: a `sync.Map` (or `sync.RWMutex`-protected map) of `task_id → []subscriber_channel`.

This is approximately 60 lines of Go. No library is appropriate here because:
- The broker lifecycle is tied to the task lifecycle (create on dispatch, close on all-results-received or timeout)
- It must integrate with the existing `streamManager` cleanup path
- Any external pub/sub library (NATS embedded, etc.) adds more complexity than the problem warrants

### New: SSE for Live Result Streaming to UI/CLI

| Library | Version | Purpose | Why |
|---------|---------|---------|-----|
| net/http stdlib | - | Server-Sent Events for task result streaming | The REST API (port 8181) already exists. SSE is HTTP streaming with `text/event-stream` content type. No library needed — write the event loop with `http.Flusher`. |

**Trade-off: SSE vs WebSocket vs Long-poll for result streaming to CLI/UI:**

| Approach | Pros | Cons | Verdict |
|----------|------|------|---------|
| SSE (HTTP `text/event-stream`) | Works through proxies/firewalls, HTTP/1.1 compatible, auto-reconnect built into browser spec, stdlib-only | Server-to-client only (fine for result streaming) | **Use this** |
| WebSocket | Bidirectional | More complex, less proxy-friendly, needs library or careful stdlib impl | Overkill for unidirectional result streaming |
| Long-poll | Universally compatible | High overhead, 1 request per poll cycle, stale data between polls | Inferior latency for live output |
| gRPC streaming | Already exists for agent↔server | hobnobcli is a CLI, not a gRPC client — would need generated client stubs and proper mTLS cert provisioning | Wrong transport for admin tooling |

SSE is the right choice for "stream task progress to hobnobcli or the web UI." The CLI opens `GET /api/v1/tasks/{id}/stream`, reads the event stream, prints output as it arrives. The web UI does the same via EventSource API.

### Supporting Libraries Summary

| Library | Version | Already Present? | Action |
|---------|---------|-----------------|--------|
| golang.org/x/sync | v0.20.0 | Transitive | Add as direct dependency |
| net/http stdlib | - | Yes | No action |
| context stdlib | - | Yes | No action |
| sync stdlib (Mutex, Map) | - | Yes | No action |
| encoding/json stdlib | - | Yes | No action |

---

## Proto Changes Required

New message types needed in `proto/hobnob.proto`. These extend the existing `AgentStreamMessage` / `ServerStreamMessage` oneofs:

### Server → Agent (dispatch)

```protobuf
message StreamTaskDispatch {
  string task_id    = 1;
  string kind       = 2;  // "exec" | "script" | "query"
  bytes  payload    = 3;  // JSON: {command, env, timeout_seconds}
  int32  timeout_s  = 4;
}
```

Add to `ServerStreamMessage.msg` oneof.

### Agent → Server (result)

```protobuf
message StreamTaskResult {
  string task_id    = 1;
  string thrall_id  = 2;
  int32  exit_code  = 3;
  bytes  stdout     = 4;
  bytes  stderr     = 5;
  string error      = 6;  // transport/execution error, not command stderr
  int64  duration_ms = 7;
}
```

Add to `AgentStreamMessage.msg` oneof.

**Why extend the existing stream rather than a new RPC:** A new bidirectional RPC would mean a second persistent connection per agent (second mTLS handshake, second auth, duplicate keepalive overhead). The existing `AgentStream` is multiplexed via the oneof pattern — this is exactly what oneof is for. etcd's watch protocol uses this same pattern (WatchCreateRequest/WatchCancelRequest/WatchProgressRequest in a single stream).

### Database Schema Additions

New tables needed (add as migration):

```sql
CREATE TABLE tasks (
    id          TEXT PRIMARY KEY DEFAULT (uuid_v4()),
    kind        TEXT NOT NULL CHECK (kind IN ('exec', 'script', 'query', 'workflow')),
    status      TEXT NOT NULL CHECK (status IN ('pending', 'running', 'success', 'partial', 'failed', 'cancelled')),
    target      TEXT NOT NULL,  -- JSON: thrall IDs or selector
    payload     TEXT NOT NULL,  -- JSON: command/script/query spec
    created_at  DATETIME NOT NULL DEFAULT (datetime('now')),
    started_at  DATETIME,
    finished_at DATETIME,
    created_by  TEXT            -- operator identity (future RBAC)
);

CREATE TABLE task_results (
    id          TEXT PRIMARY KEY DEFAULT (uuid_v4()),
    task_id     TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    thrall_id   TEXT NOT NULL REFERENCES thralls(id),
    exit_code   INTEGER,
    stdout      TEXT,
    stderr      TEXT,
    error       TEXT,
    duration_ms INTEGER NOT NULL DEFAULT 0,
    created_at  DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE workflows (
    id          TEXT PRIMARY KEY DEFAULT (uuid_v4()),
    status      TEXT NOT NULL CHECK (status IN ('pending', 'running', 'success', 'failed', 'cancelled')),
    definition  TEXT NOT NULL,  -- JSON: steps array
    state       TEXT NOT NULL DEFAULT '{}',  -- JSON: step outcomes
    created_at  DATETIME NOT NULL DEFAULT (datetime('now')),
    finished_at DATETIME
);
```

---

## What NOT to Add

| Avoid | Why | What to Use Instead |
|-------|-----|---------------------|
| Temporal / Cadence / Durable Task | External server required; adds operational complexity for a self-contained tool; total overkill for N-step linear workflows with conditionals | In-process workflow runner using `errgroup` + SQLite state |
| Redis / NATS / RabbitMQ as task queue | External infrastructure dependency; the streamManager already IS a push queue for connected agents | `streamManager.dispatch()` method + SQLite for durability |
| Gorilla WebSocket / nhooyr/websocket | Not needed when SSE suffices for unidirectional streaming; adds dependency for no functional gain | `net/http` with `http.Flusher` for SSE |
| go.temporal.io/sdk | Same as Temporal above | In-process state machine |
| machinery / asynq / river | Job queue libraries designed for background workers polling a shared queue. Wrong model — Hobnob needs push-to-connected-agent, not pull-by-worker | Direct stream dispatch |
| cel-go / expr (CEL expression language) | Workflow conditionals ("if step A succeeded") are simple boolean checks on step outcome maps. Full expression language is overkill. | Simple Go switch/if on step status fields |

---

## Workflow State Machine: In-Process Design

For multi-step workflows, the right pattern is a simple in-process state machine — NOT an external workflow engine.

**Design:**

```
WorkflowRunner:
  - Load workflow definition (steps: [{target, task_kind, condition, on_failure}])
  - For each step:
    1. Evaluate entry condition (check prior step outcomes in state map)
    2. Dispatch task to target thralls (via streamManager, bounded by errgroup)
    3. Wait for all results or timeout
    4. Record outcomes in workflow.state (JSON in SQLite)
    5. Decide: continue / skip / abort based on on_failure policy
  - Update workflow.status on completion
```

**State persistence:** After each step completes, write step outcomes to `workflows.state` JSON column. On server restart, incomplete workflows are marked failed (no resume logic needed for v1.3). Resume semantics add significant complexity without clear operator demand — add in v1.4 if needed.

**Why not resume:** Workflows involve executing commands on thralls. Re-executing a completed step after a server crash is likely dangerous (idempotency of ad-hoc exec is not guaranteed). Failing fast and requiring operator retry is safer.

---

## Rolling Execution Design

Rolling execution is a constrained form of task dispatch — not a fundamentally different mechanism.

**Pattern:**

```go
sem := semaphore.NewWeighted(int64(batchSize))
g, ctx := errgroup.WithContext(ctx)

for _, thrallID := range targets {
    if err := sem.Acquire(ctx, 1); err != nil {
        break // cancelled
    }
    g.Go(func() error {
        defer sem.Release(1)
        return dispatchAndWait(ctx, thrallID, task)
    })

    // Optional: inter-batch health check gate
    if completedSoFar % batchSize == 0 {
        if !healthCheck(ctx, lastBatch) {
            cancel()
        }
    }
}
g.Wait()
```

**Failure threshold:** Track `failCount` atomically. If `failCount > maxFailures`, cancel the context. `errgroup.WithContext` propagates cancellation to all in-flight dispatches.

**No new library needed.** `golang.org/x/sync/semaphore` + `errgroup` + a `sync/atomic` counter covers rolling execution completely.

---

## Live Query Design

Live query ("ask for current state without waiting for next check-in") is the simplest feature:

1. Server sends `StreamTaskDispatch{kind: "query", payload: {trait_path}}` to target thrall(s)
2. Agent handler reads the requested trait path from its in-memory trait cache
3. Agent sends `StreamTaskResult{stdout: json_encoded_value}` back
4. Server aggregates responses into a task record

No new protocol or library needed. The existing stream handles it.

**Trait path syntax** (e.g., `os.name`, `packages.installed`) matches the existing trait dot-path convention already used in conditional vars. Reuse the same accessor logic.

---

## Trade-off Summary

| Decision | Option A | Option B | Choice | Reason |
|----------|----------|----------|--------|--------|
| Task queue | In-process (streamManager) | External (Redis/NATS) | **In-process** | No new infrastructure; streamManager already handles agent fan-out |
| Workflow engine | In-process state machine | Temporal/Cadence | **In-process** | Single server process; Temporal requires an external server |
| Result streaming (CLI/UI) | SSE over HTTP | WebSocket | **SSE** | Unidirectional is sufficient; simpler impl; works through proxies |
| Workflow protocol | Extend AgentStream oneof | New gRPC RPC | **Extend oneof** | Avoids second connection per agent; etcd/Kubernetes use this pattern |
| Workflow conditionality | cel-go expressions | Go code (switch on status) | **Go code** | Conditions are simple (pass/fail); full expression language is overengineering |
| Rolling execution coordination | x/sync semaphore | Channel-based counting | **semaphore** | Cleaner API, weight-extensible, idiomatic for this problem |
| Workflow durability on restart | Resume from SQLite state | Fail incomplete workflows | **Fail and require retry** | Ad-hoc exec is not idempotent; re-execution after crash risks double-apply |

---

## Alternatives Considered

| Category | Recommended | Alternative | When Alternative Makes Sense |
|----------|-------------|-------------|------------------------------|
| Workflow state | In-process + SQLite | Temporal Go SDK | If workflows need to survive server crashes, span multiple servers, or involve long waits (days) |
| Task queue | streamManager direct | asynq (Redis-backed job queue) | If tasks need to execute even when the agent is offline (queue for later delivery) |
| Result streaming | SSE | gRPC streaming from admin service | If hobnobcli becomes a long-lived gRPC client (currently it makes short HTTP calls) |
| Concurrency control | x/sync/semaphore | Custom channel semaphore | Functionally equivalent; channel version is fine for simple cases |
| Workflow DSL | None (Go structs) | YAML workflow definition files | If operators need to define reusable, shareable workflows (add in v1.4) |

---

## Installation

```bash
# Add golang.org/x/sync as a direct dependency (currently transitive)
go get golang.org/x/sync@v0.20.0
```

That is the only new `go get` needed. All other orchestration components are:
- Proto changes (regenerate with `make proto`)
- New SQLite migration (`make proto` + `migrations/000002_tasks.up.sql`)
- New Go packages inside the existing server module (`server/orchestration/`, `server/tasks/`)

---

## Sources

- [golang.org/x/sync pkg.go.dev](https://pkg.go.dev/golang.org/x/sync) — v0.20.0 confirmed Feb 23 2026
- [errgroup.SetLimit documentation](https://pkg.go.dev/golang.org/x/sync/errgroup) — SetLimit + TryGo API verified
- [semaphore package](https://pkg.go.dev/golang.org/x/sync/semaphore) — Weighted semaphore API verified
- [SQLite WAL concurrent writes](https://sqlite.org/wal.html) — single-writer model confirmed; task queue via WAL is viable for this write volume
- [SQLite concurrent writes gotchas](https://tenthousandmeters.com/blog/sqlite-concurrent-writes-and-database-is-locked-errors/) — MEDIUM confidence (WebSearch): task state updates are infrequent relative to check-ins, not a bottleneck
- [gRPC fan-out patterns](https://elsyarifx.medium.com/the-grpc-streaming-implementation-that-handles-1m-concurrent-connections-0f7916088681) — MEDIUM confidence: fan-out via existing stream channels is the right approach
- [SSE vs WebSocket vs polling](https://dev.to/haraf/server-sent-events-sse-vs-websockets-vs-long-polling-whats-best-in-2025-5ep8) — SSE recommendation for unidirectional streaming confirmed
- [7 gRPC fan-out patterns](https://medium.com/@bhagyarana80/7-grpc-fan-out-patterns-no-fan-fires-23144b78b6f1) — MEDIUM confidence: topic-mux pattern on existing stream aligns with AgentStream oneof design
- [Go state machine patterns](https://medium.com/@johnsiilver/go-state-machine-patterns-3b667f345b5e) — MEDIUM confidence: confirms in-process state machine viability
- [Temporal in Go guide](https://www.glukhov.org/post/2026/03/workflow-applications-temporal-in-go/) — LOW confidence (WebSearch only): consulted to confirm Temporal requires external infrastructure
- etcd watch protocol (known pattern) — oneof-in-stream for multi-command-type dispatch is battle-tested in production

---

*Stack research for: Hobnob v1.3 Orchestration additions*
*Researched: 2026-03-11*
