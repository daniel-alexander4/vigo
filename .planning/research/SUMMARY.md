# Project Research Summary

**Project:** Hobnob v1.3 — Orchestration
**Domain:** Adding imperative orchestration to a pull-based configuration management system
**Researched:** 2026-03-11
**Confidence:** HIGH

## Executive Summary

Hobnob v1.3 adds five orchestration capabilities to an already-working Go+Rust+gRPC configuration management system: ad-hoc command execution, task/script management, multi-step workflows with conditionals, rolling execution with failure thresholds, and live query. The recommended approach is fully in-process — no external queue, no external workflow engine. The existing bidirectional `AgentStream` gRPC connection is the delivery mechanism, extended via new `oneof` message variants rather than new RPCs. The only new library dependency is `golang.org/x/sync` (already a transitive dependency) for `errgroup` and `semaphore` primitives. All other orchestration logic uses stdlib goroutines, channels, and the existing `streamManager` infrastructure.

The critical design insight is that orchestration and configuration management are semantically opposite: config management is declarative and idempotent; orchestration is imperative and one-shot. This distinction must be enforced at the interface level — a dedicated `TaskExecutor` separate from convergence executors, a dedicated result schema separate from run results, and hard mutual exclusion on the agent so convergence runs and orchestration tasks never execute simultaneously. These separations cannot be retrofitted; they must be designed in before any execution code is written.

The primary risks are all architectural and front-loaded to Phase 1: stream buffer overflow silently dropping task dispatches (the existing fire-and-forget `send` channel semantics are insufficient for tasks), orphaned subprocesses when streams disconnect (requires context-bound process management from the start), convergence/orchestration collision on the agent (requires a single-worker execution queue), and authorization bypass via raw shell dispatch (the named-operation vs. raw-shell decision in the proto determines the security model permanently). Workflow state corruption on server restart is a Phase 3 risk requiring durable SQLite persistence before the first multi-step workflow executes. All other pitfalls are manageable with patterns already present in the codebase.

## Key Findings

### Recommended Stack

The base stack (Go + gRPC over mTLS + SQLite + sqlc + Cobra) is unchanged. Orchestration requires zero new external infrastructure. The sole `go get` addition is `golang.org/x/sync@v0.20.0` for `errgroup.WithContext` (concurrent per-thrall dispatch with automatic cancellation propagation) and `semaphore.NewWeighted` (rolling execution batch control). Both are already transitive dependencies — just needs a direct `require` entry.

**Core technologies:**
- `golang.org/x/sync/errgroup` — bounded concurrent dispatch with error propagation — `WithContext` provides exactly the "cancel remaining thralls if N fail" semantics rolling execution needs
- `golang.org/x/sync/semaphore` — rolling batch gate — `NewWeighted(batchSize)` controls how many thralls execute simultaneously; cleaner and more extensible than channel counting
- `net/http` stdlib SSE (`http.Flusher`) — live result streaming to CLI and web UI — unidirectional push is sufficient; no WebSocket library needed
- `sync.Map` (stdlib) — in-process pub/sub result broker — maps `task_id` to result channel; approximately 60 lines, no library warranted
- `context.Context` + cancellation (stdlib) — task timeout and subprocess lifetime binding — must be derived from stream `done` channel

Result streaming to CLI uses SSE over the existing port 8181 REST API (`GET /api/v1/tasks/{id}/stream`). SSE is the correct choice over WebSocket (overkill for unidirectional) and long-poll (latency and overhead). The `AgentStream` oneof extension is correct over a new RPC (avoids a second TLS handshake per agent, reuses existing auth, buffer already in place).

**Schema additions:** Three new migration tables — `task_runs`, `task_run_targets`, `workflow_runs` — plus `workflow_step_results`. These are distinct from the existing convergence `runs`/`run_results` tables; reusing those tables is explicitly an anti-pattern (convergence semantics do not map to task semantics).

### Expected Features

Research consulted Ansible, SaltStack, Puppet Bolt, and AWS Elastic Beanstalk rolling deployments. The five capabilities map clearly to P1/P2/P3 priorities.

**Must have for v1.3 (table stakes):**
- Ad-hoc command execution via AgentStream — proves the execution path end-to-end; every operator's first question
- Multi-thrall targeting by hostname glob and single trait filter — covers 90% of targeting needs
- Result collection per thrall (stdout, stderr, exit code, duration) — operators need per-node visibility
- Job record with per-thrall status (pending/dispatched/complete/failed/timeout/offline) — execution visibility
- Timeout enforcement: agent-side `context.WithTimeout` plus server-side deadline marking for non-responsive thralls
- Rolling execution with `batch_size` and `max_failures` parameters — primary safety control for fleet operations
- `hobnobcli task run`, `task status`, `task list` subcommands — CLI is the primary interface
- Live query for trait paths with cached fallback for disconnected thralls
- `hobnobcli query` subcommand for live fact retrieval
- Job history queryable via CLI and REST (`hobnobcli runs list`, `hobnobcli runs get <id>`)

**Should have for v1.4 (competitive differentiators):**
- Task definitions: scripts in `tasks/` directory with JSON parameter schema and pre-execution validation (Puppet Bolt model)
- Sequential workflow engine with `on_success`/`on_failure` branching — YAML files in `workflows/` directory, server-side state machine
- Health-check commands in rolling execution (per-batch automated gate before proceeding)
- Compound trait targeting with AND/OR of multiple conditions
- Disconnected-agent DB fallback for ad-hoc commands (poll on next check-in)

**Defer to v2+:**
- Parallel DAG workflow steps (sequential YAML is sufficient for most cases; DAG adds operator cognitive load)
- Webhook events for job completion/failure
- Scheduled job execution (cron-like recurring tasks)
- Workflow approval gates for human-in-the-loop governance
- Task result chaining (output of task A feeds task B as input)
- SSE streaming of live stdout to web UI during execution

The key competitive advantage: Ansible and Puppet Bolt establish per-execution SSH connections (100–500ms overhead, credential management required). Hobnob's AgentStream is already open — task delivery is sub-second. Rolling execution with health-gate abort is also a genuine open-source differentiator; Ansible's `serial:` has no built-in health check gate, and Puppet Bolt's rolling requires manual plan logic.

### Architecture Approach

The architecture is an additive layer on top of the existing system. The `streamManager` in `server/grpc/streams.go` gains two new methods: `sendTask()` and `sendQuery()`. The `AgentStream` oneof is extended with `StreamTaskDispatch`/`StreamQueryRequest` (server-to-agent) and `TaskResult`/`QueryResponse` (agent-to-server). A new server package `server/orchestration/` contains `TaskService`, `WorkflowEngine`, `RollingCoordinator`, and `QueryCollector`. New agent files add a `TaskExecutor` (run command, capture output, report result) and a `QueryExecutor` (read trait cache, no OS invocation for cached traits). Architecture research is based on direct codebase inspection and carries HIGH confidence.

**Major components:**
1. `TaskService` (`server/orchestration/tasks.go`) — accepts REST task dispatch, fans out via `streamMgr.sendTask()`, maintains `sync.Map[taskID, chan *pb.TaskResult]` for result routing, persists to `task_runs`/`task_run_targets`
2. `WorkflowEngine` (`server/orchestration/workflow.go`) — server-side state machine; processes steps sequentially, calls `TaskService` per step, evaluates `on_success`/`on_failure` conditions, persists step state to `workflow_runs` before each transition (write-ahead state)
3. `RollingCoordinator` (`server/orchestration/rolling.go`) — divides target set into batches, dispatches via `errgroup` + `semaphore`, evaluates per-batch failure threshold post-completion, runs health-check commands between batches, aborts on threshold breach
4. `QueryCollector` (`server/orchestration/query.go`) — sends `StreamQueryRequest` to connected agents via `streamMgr`, blocks on result channels with configurable timeout (default 10s), returns partial results with explicit `responded`/`timed_out`/`offline` counts
5. `TaskExecutor` (Rust, `agent-rs/src/executors/task.rs`) — raw command execution, no idempotency check, `tokio::spawn` to avoid blocking `stream_loop`, context-bound subprocess lifetime (SIGTERM then SIGKILL on cancellation)
6. REST API handlers (`server/api/tasks.go`, `server/api/workflows.go`, `server/api/query.go`) — accept POST to create, GET for status, SSE endpoint for streaming progress

**Build order constraint:** Proto changes must come first (Go and Rust generated code must match before any integration tests can run), then SQLite schema, then agent executor, then server service layer, then REST and CLI, then workflow engine. This order is non-negotiable — it follows hard dependency chains.

### Critical Pitfalls

1. **Convergence/orchestration collision on agent** — a convergence run and an ad-hoc task must never execute simultaneously on the same thrall. The agent needs a single-worker execution queue before any task dispatch code exists. This is architectural and cannot be retrofitted. Prevention: integration test triggering both simultaneously; verify only one executes at a time.

2. **Stream buffer overflow silently drops task dispatches** — the current `agentStream.send` channel buffer is 8. Task dispatch cannot reuse the fire-and-forget `pushPolicy` semantics. The `sendTask()` method must use blocking send with timeout plus a `TaskAck` receipt message from agent to server. A dropped task must be an explicit error.

3. **Orphaned subprocesses on stream disconnect** — every task subprocess must be bound to a `context.Context` derived from the stream's `done` channel. Use `exec.CommandContext` not `exec.Command`. Implement SIGTERM then grace period then SIGKILL on stream close. On agent reconnect, server marks orphaned tasks as `stream_interrupted`. Must be in the first implementation.

4. **Authorization bypass via raw shell dispatch** — the task dispatch proto must use named operations or an allowlist, not arbitrary shell strings. Every dispatch must write an immutable audit record before execution. The protocol shape determines the security model permanently — changing it later is a breaking proto change.

5. **Workflow state corruption on server restart** — workflow state must be persisted to SQLite before the first task in each step is dispatched. In-memory workflow state causes silent workflow loss on any server restart. Incomplete workflows on restart should be marked `failed` with `restart_interrupted` reason.

Additional high-priority pitfalls to address: SQLite write contention under fan-out (use batch writes and `PRAGMA busy_timeout=5000`, mirroring the existing `TraitsBatcher` pattern); live query partial results unreported to caller (always include `responded`/`timed_out`/`offline` counts, never silently omit non-responders); federation hub not propagating orchestration commands to spokes (design hub forwarding into the RPC shape from day one).

## Implications for Roadmap

Based on combined research, the 8-phase build order from ARCHITECTURE.md provides the clearest dependency chain. The phases below follow that order with explicit rationale drawn from all four research files.

### Phase 1: Proto and Schema Foundation
**Rationale:** Generated code (Go and Rust) must match before any integration tests can run. SQLite schema constrains the API contract — changing it after API handlers are written forces API rewrites. Both are zero-behavior-change phases; existing tests pass unchanged. This phase also requires the critical protocol design decision: named operations vs. raw shell in `StreamTaskDispatch`, and the `TaskAck` acknowledgement message shape. These cannot be deferred.
**Delivers:** New proto message types (`StreamTaskDispatch`, `TaskResult`, `StreamQueryRequest`, `QueryResponse`, `TaskAck`) regenerated into both Go and Rust; migration adding `task_runs`, `task_run_targets`, `workflow_runs`, `workflow_step_results` tables with sqlc-generated query code.
**Addresses:** Infrastructure for all five orchestration capabilities; authorization model decision; acknowledgement protocol.
**Avoids:** Proto/schema incompatibility between Go server and Rust agent discovered mid-implementation; post-hoc security model changes.

### Phase 2: Agent Task Executor and Stream Plumbing
**Rationale:** The fundamental question — can a task dispatch reach the agent and produce a result? — must be answered before building the full server orchestration stack. Early integration tests of the message round-trip are possible as soon as this phase completes. This phase also establishes the mandatory mutual exclusion between convergence and task execution (Pitfall 1) and context-bound subprocess management (Pitfall 2). Neither can be retrofitted.
**Delivers:** `agent-rs/src/executors/task.rs` (run command, capture stdout/stderr, return `TaskResult`); `StreamHandle` extended with `task_rx` channel; `read_loop()` dispatching `StreamTaskDispatch` messages; `stream_loop` spawning task execution as independent tokio tasks; `JoinHandle` registry per task; SIGTERM then SIGKILL on stream close; single-worker execution queue preventing convergence/task concurrency.
**Addresses:** Ad-hoc command execution (P1), timeout enforcement (P1).
**Avoids:** Convergence/orchestration collision (Pitfall 1), orphaned subprocesses (Pitfall 2), executor model mismatch (Pitfall 11).

### Phase 3: Server StreamManager Extension and Result Routing
**Rationale:** With the agent able to receive and execute tasks, the server needs to dispatch them and collect results. This phase wires the server side: `sendTask()`/`sendQuery()` methods on `streamManager` with backpressure semantics (not fire-and-forget), result routing via `sync.Map[taskID, chan *pb.TaskResult]`, and `TaskService` with SQLite persistence using the batch-write pattern.
**Delivers:** `streamManager.sendTask()` and `streamManager.sendQuery()` (blocking send with timeout, not drop-on-full); `TaskService` with `sync.Map` result broker; `AgentStream` handler extended to route `TaskResult` and `QueryResponse` to pending channels; batch write pattern for concurrent result inserts (mirrors `TraitsBatcher` in `server/db/traitsbatcher.go`); `PRAGMA busy_timeout=5000` on the SQLite connection.
**Addresses:** All server-side task infrastructure.
**Avoids:** Silent task drops (Pitfall 6), SQLite write contention under fan-out (Pitfall 4).

### Phase 4: REST API and CLI for Ad-Hoc Tasks
**Rationale:** Simple tasks before workflows — the workflow engine calls `TaskService` internally, so the task primitive must be working and validated before building workflows on top of it. This phase delivers a usable operator interface for QA and validates the end-to-end execution path from CLI to agent and back.
**Delivers:** `server/api/tasks.go` (POST `/api/v1/tasks`, GET `/api/v1/tasks/{id}`, SSE `/api/v1/tasks/{id}/stream`); `hobnobcli task run`, `hobnobcli task status`, `hobnobcli task list`; per-thrall result display with explicit `offline`/`timeout`/`complete` status; immutable audit log entry on every dispatch; output truncation at 1MB with visible `[output truncated at N bytes]` marker.
**Addresses:** CLI subcommands (P1), execution status visibility (P1), job history (P1), audit logging (Pitfall 3), output size limits (Pitfall 10).
**Avoids:** Operator confusion from implicit partial results; silent truncation; unlogged dispatches.

### Phase 5: Live Query
**Rationale:** Live query is independent of task execution — it is a read operation with no run record, using the stream but producing no side effects. Implementing it after the task infrastructure is in place reuses stream plumbing but keeps the query path cleanly separate from the execution path.
**Delivers:** `server/orchestration/query.go` (`QueryCollector` with configurable timeout, partial result handling, explicit `responded`/`timed_out`/`offline` counts in every response); `agent-rs/src/executors/query.rs` (trait cache read plus resource check-phase-only execution, no apply); `server/api/query.go`; `hobnobcli query` subcommand.
**Addresses:** Live query (P1), partial result visibility.
**Avoids:** Operator confusion from implicitly incomplete query results (Pitfall 8) — the `timed_out` and `offline` counts are non-negotiable fields in the response schema.

### Phase 6: Rolling Execution
**Rationale:** Rolling execution is a constrained form of task dispatch, not a fundamentally different mechanism. It builds directly on the task infrastructure from Phases 2–4. Implementing it after basic task execution is validated reduces debugging surface area. The failure threshold semantics must be specified and reviewed before implementation begins.
**Delivers:** `server/orchestration/rolling.go` (`RollingCoordinator` using `errgroup` plus `semaphore`; `batch_size` as count or percentage; `max_failures` as count or percentage; per-batch failure threshold evaluation post-completion, not mid-batch; optional `health_check` command between batches; offline thrall reporting as `offline` status, not counted as `failure`); REST and CLI integration; dry-run mode showing batch plan without executing.
**Addresses:** Rolling execution (P1), fleet safety.
**Avoids:** Rolling threshold logic errors (Pitfall 7) — threshold semantics (per-batch vs. cumulative, count vs. percentage, offline thrall handling) must be written into the YAML syntax documentation and verified with unit tests before any code is written.

### Phase 7: Multi-Step Workflow Engine
**Rationale:** Workflows require tasks (Phases 2–4) and rolling execution (Phase 6) working correctly. The workflow engine is the most complex component; building it last minimizes the blast radius of any design changes in earlier phases. Workflow state persistence must be designed before the first line of engine code.
**Delivers:** `server/orchestration/workflow.go` (`WorkflowEngine` state machine: sequential steps, `on_success`/`on_failure` edges, `when:` conditions reusing existing conditional syntax); `workflow_runs` and `workflow_step_results` SQLite persistence before every state transition (write-ahead state, not post-hoc); server-restart recovery (in-progress workflows marked `failed` with `restart_interrupted`); `server/api/workflows.go`; `hobnobcli workflow run`, `hobnobcli workflow status`, `hobnobcli workflow abort`; workflow YAML files in `workflows/` directory alongside other configs, hot-reloaded via fsnotify.
**Addresses:** Multi-step workflows with conditionals (v1.4 differentiator), sequential YAML philosophy.
**Avoids:** Workflow state corruption on server restart (Pitfall 5) — state must be persisted before dispatch, not after.

### Phase 8: Web UI Integration
**Rationale:** CLI is the primary interface for QA during development. Web UI after CLI means the REST API is validated before building UI on top of it. Web UI is a consumer of the same endpoints; no new server logic is required.
**Delivers:** Task history page with per-thrall result drilldown, workflow status visualization with step-level detail, live query interface, rolling execution progress view with real-time counts.
**Addresses:** UX pitfalls — rolling execution progress visibility, query completeness indicators, workflow failure drilldown showing per-thrall stderr.

### Phase Ordering Rationale

- **Proto before everything** because Go and Rust generated code must agree before any cross-language integration test is possible. A mismatch discovered mid-implementation requires regenerating code in both languages simultaneously.
- **Schema before API** because the result storage model constrains the API contract — fields not in the schema cannot appear in the API without a new migration, and migrations during active API development cause instability.
- **Agent before server** because the critical unknown (message round-trip through the existing `AgentStream`) must be proven before building the orchestration service layer on top of it. An early integration test verifying the end-to-end round-trip is worth more than any amount of unit testing.
- **Simple tasks before workflows** because the workflow engine calls `TaskService` internally. A bug in `TaskService` is far harder to diagnose inside a multi-step workflow than in isolation.
- **Rolling before workflows** because `WorkflowEngine` delegates to `RollingCoordinator` for step execution. Rolling must be stable before workflows are built on it.
- **CLI before Web UI** because the REST API is the stable interface; CLI validates it while the web UI is merely another consumer of the same endpoints.

### Research Flags

Phases likely needing deeper research during planning:
- **Phase 2 (Agent task executor):** Rust async tokio subprocess management — specifically the `JoinHandle` registry, cancellation on stream close, and the interaction between the new task execution arm and the existing `stream_loop` `tokio::select!` architecture. The codebase patterns are understood but subprocess lifecycle has known edge cases in async Rust.
- **Phase 6 (Rolling execution):** Failure threshold semantics need explicit written specification before implementation. The ambiguity between per-batch vs. cumulative evaluation, count vs. percentage, and offline-thrall counting is documented in PITFALLS.md and will produce the wrong behavior if not pinned down first.
- **Phase 7 (Workflow engine):** The YAML workflow definition format needs design work — specifically the `when:` condition syntax, `on_success`/`on_failure` edge semantics, and how target selectors interact with rolling execution parameters within a workflow step.

Phases with standard patterns (skip `/gsd:research-phase`):
- **Phase 1 (Proto + Schema):** Proto oneof extension and sqlc migration are standard, well-documented patterns already used in this codebase. No new patterns required.
- **Phase 3 (Server result routing):** Go `sync.Map` result broker and batch write patterns mirror the existing `TraitsBatcher` — established in-codebase patterns.
- **Phase 4 (REST API + CLI):** Standard Cobra subcommand and REST handler patterns already used throughout the codebase. SSE is a straightforward `http.Flusher` implementation.
- **Phase 5 (Live query):** Well-understood read path. The main requirement (explicit non-responder reporting) is fully specified in research.
- **Phase 8 (Web UI):** Go template infrastructure is already in place; adding orchestration pages follows existing handler patterns.

## Confidence Assessment

| Area | Confidence | Notes |
|------|------------|-------|
| Stack | HIGH | Only one new library (`golang.org/x/sync`), already transitive; all other decisions use stdlib or existing dependencies. API verified from pkg.go.dev docs at v0.20.0. |
| Features | HIGH | Grounded in direct comparison to Ansible, SaltStack, and Puppet Bolt with official documentation. Feature prioritization is opinionated and clear. Dependency graph fully mapped. |
| Architecture | HIGH | Based on direct codebase inspection of `proto/hobnob.proto`, `server/grpc/streams.go`, `agent-rs/src/checkin/stream.rs`, `agent-rs/src/checkin/loop_impl.rs`, `server/db/runstore.go`, `migrations/000001_initial_schema.up.sql`. No inference — all structural decisions verified against actual code. |
| Pitfalls | HIGH | 11 critical and moderate pitfalls with specific prevention strategies, phase assignments, and a "looks done but isn't" checklist. Sources include CVE history (SaltStack command injection), official gRPC flow control docs, and direct codebase inspection. |

**Overall confidence:** HIGH

### Gaps to Address

- **Task acknowledgement protocol shape:** PITFALLS.md and STACK.md both recommend a `TaskAck` message from agent to server, but neither specifies whether it is a distinct proto message or a field on `TaskResult`. This must be decided during Phase 1 proto design. Once the proto field is defined, changing it is a breaking protocol change affecting both Go and Rust.

- **Federation forwarding for orchestration:** PITFALLS.md (Pitfall 9) flags that hub federation must forward orchestration RPCs to spokes. ARCHITECTURE.md establishes the pattern (`ForwardForcePush`) but does not detail the richer orchestration forwarding (bidirectional result streaming through hub to spoke to agent). This design gap should be addressed in Phase 1 or Phase 3 before any federation deployments attempt orchestration.

- **Command model decision for v1.3:** PITFALLS.md recommends named operations over raw shell strings. FEATURES.md acknowledges that v1.3 accepts "all admins are trusted" with audit logging. The concrete protocol decision — whether `StreamTaskDispatch.command` is a raw shell string or a named task reference — must be made during Phase 1 proto design and cannot be changed without a breaking proto update.

- **Workflow resume semantics:** Research agrees that failing incomplete workflows on server restart is the safe v1.3 default (re-executing completed steps after a crash risks double-apply of non-idempotent commands). This is a known limitation. Operators should be warned in documentation and CLI error messages. `hobnobcli workflow resume <id>` is explicitly a v1.4+ feature.

## Sources

### Primary (HIGH confidence)
- Hobnob codebase (direct inspection): `proto/hobnob.proto`, `server/grpc/streams.go`, `agent-rs/src/checkin/stream.rs`, `agent-rs/src/checkin/loop_impl.rs`, `server/db/runstore.go`, `migrations/000001_initial_schema.up.sql`, `server/federation/hub.go`
- [pkg.go.dev/golang.org/x/sync](https://pkg.go.dev/golang.org/x/sync) — `errgroup` and `semaphore` API verified at v0.20.0
- [Ansible ad-hoc commands documentation](https://docs.ansible.com/projects/ansible/latest/command_guide/intro_adhoc.html) — table stakes reference
- [Puppet Bolt tasks documentation](https://www.puppet.com/docs/bolt/latest/writing_tasks.html) — task model reference
- [Puppet Bolt YAML plans](https://www.puppet.com/docs/bolt/latest/writing_yaml_plans.html) — workflow/plan model
- [SaltStack Orchestrate Runner](https://docs.saltproject.io/en/latest/topics/orchestrate/orchestrate_runner.html) — workflow model comparison
- [SaltStack Grains targeting](https://docs.saltproject.io/en/latest/topics/targeting/grains.html) — targeting model comparison
- [AWS Elastic Beanstalk rolling deployment strategies](https://docs.aws.amazon.com/elasticbeanstalk/latest/dg/using-features.rolling-version-deploy.html) — rolling execution patterns
- [gRPC flow control](https://grpc.io/docs/guides/flow-control/) — stream buffer and backpressure design
- [SQLite WAL mode](https://sqlite.org/wal.html) — write contention mitigation
- [SaltStack CVE history](https://www.cvedetails.com/vulnerability-list/vendor_id-12943/product_id-26420/Saltstack-Salt.html) — command injection patterns informing authorization design

### Secondary (MEDIUM confidence)
- [SSE vs WebSocket vs polling (2025)](https://dev.to/haraf/server-sent-events-sse-vs-websockets-vs-long-polling-whats-best-in-2025-5ep8) — SSE recommendation for unidirectional result streaming
- [gRPC fan-out patterns](https://elsyarifx.medium.com/the-grpc-streaming-implementation-that-handles-1m-concurrent-connections-0f7916088681) — fan-out via existing stream channels
- [Database-backed workflow orchestration (QCon SF 2025)](https://www.infoq.com/news/2025/11/database-backed-workflow/) — workflow state persistence patterns
- [SQLite concurrent writes gotchas](https://tenthousandmeters.com/blog/sqlite-concurrent-writes-and-database-is-locked-errors/) — task queue via WAL viability
- [gRPC bidirectional streaming reliability and reconnection](https://oneuptime.com/blog/post/2026-01-24-grpc-bidirectional-streaming/view) — stream disconnect handling patterns

### Tertiary (LOW confidence)
- [Temporal in Go (2026)](https://www.glukhov.org/post/2026/03/workflow-applications-temporal-in-go/) — consulted to confirm Temporal requires external infrastructure; serves as the "what not to use" reference

---
*Research completed: 2026-03-11*
*Ready for roadmap: yes*
