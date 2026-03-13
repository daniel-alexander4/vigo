# Architecture Research

**Domain:** Orchestration integration with pull-based config management (Hobnob v1.3)
**Researched:** 2026-03-11
**Confidence:** HIGH — based on direct codebase analysis, not inference

## Context

This is a **subsequent milestone** architecture document. The existing system is fully understood from source code. All decisions are constrained by what already exists:

- `AgentStream` bidirectional RPC in `proto/hobnob.proto` (lines 34, 361-389)
- `streamManager` in `server/grpc/streams.go`: `register()`, `pushPolicy()`, `forcePush()`
- `agentStream.send chan *pb.ServerStreamMessage` — buffer size 8
- `StreamHandle` in `agent-rs/src/checkin/stream.rs`: `request_tx`, `policy_push_rx`, `force_push_rx`, `check_in_resp_rx`, `done`
- `read_loop()` in agent dispatches on `server_stream_message::Msg` variants
- `RunStore` interface in `server/db/runstore.go`: `CreateRun`, `CreateRunResult`, `FinishRun`, `ListRuns`
- `SQLite` schema in `migrations/000001_initial_schema.up.sql`

---

## Standard Architecture

### System Overview

Current streaming topology (what exists today):

```
hobnobcli / Web UI
      |
      | REST POST /api/v1/push
      v
 AdminService.ForcePush()
      |
      v
 AgentService.streamMgr.forcePush(thrallID)
      |
      | ServerStreamMessage.force_push
      v
 agentStream.send chan (buffer=8)
      |
      | gRPC bidi stream (AgentStream RPC)
      v
 agent read_loop() → force_push_rx
      |
      v
 stream_loop: check_in request → convergence → ReportResult
```

Proposed orchestration topology (what this milestone adds):

```
hobnobcli / Web UI
      |
      | REST POST /api/v1/tasks
      |       POST /api/v1/workflows
      |       POST /api/v1/query
      v
 TaskService (new server/orchestration/ package)
      |
      | ServerStreamMessage.task_dispatch (new variant)
      v
 agentStream.send chan (existing, buffered)
      |
      | gRPC bidi stream (existing AgentStream RPC)
      v
 agent read_loop() → task_rx (new channel in StreamHandle)
      |
      v
 task executor → stdout/stderr capture
      |
      | AgentStreamMessage.task_result (new variant)
      v
 server TaskResultCollector → SQLite task_runs table
      |
      v
 hobnobcli / Web UI (poll or long-poll for results)
```

### Component Responsibilities

**Existing components (unchanged):**

| Component | Responsibility | Notes |
|-----------|---------------|-------|
| `streamManager` | Registry of connected agent streams | Lives in `server/grpc/streams.go` |
| `agentStream.send` | Channel for server→agent push messages | Buffer 8; non-blocking send |
| `StreamHandle` | Agent-side channel demuxer for bidi stream | `checkin/stream.rs` |
| `read_loop()` | Agent: dispatches server messages to channels | Extends naturally |
| `RunStore` | Config-run storage (CreateRun/FinishRun/ListRuns) | Reuse pattern for task runs |

**New components needed:**

| Component | Responsibility | Lives In |
|-----------|---------------|----------|
| `TaskService` | Accept task dispatch requests, fan to streamMgr | `server/orchestration/tasks.go` |
| `WorkflowEngine` | Multi-step workflow state machine | `server/orchestration/workflow.go` |
| `RollingCoordinator` | Batch execution with failure thresholds | `server/orchestration/rolling.go` |
| `TaskStore` | Persist task and workflow state | `server/db/tasks.sql` + sqlc |
| Task executor (Rust) | Execute ad-hoc commands, capture stdout/stderr | `agent-rs/src/executors/task.rs` |
| Task dispatch channel | Agent-side: receive `StreamTaskDispatch` messages | `agent-rs/src/checkin/stream.rs` |
| Query executor (Rust) | Return current state snapshot without convergence | `agent-rs/src/executors/query.rs` |

---

## Key Architectural Decisions

### Decision 1: Extend `ServerStreamMessage` vs New RPC

**The question:** Should orchestration use the existing `AgentStream` bidi RPC with new message variants, or add new RPCs (`TaskStream`, `QueryStream`)?

**Option A: Extend `ServerStreamMessage` with new `oneof` variants**

Add to proto:
```protobuf
message ServerStreamMessage {
  oneof msg {
    // ... existing variants ...
    StreamTaskDispatch task_dispatch = 6;   // NEW
    StreamQueryRequest query_request = 7;   // NEW
  }
}

message AgentStreamMessage {
  oneof msg {
    // ... existing variants ...
    TaskResult task_result = 4;             // NEW
    QueryResponse query_response = 5;       // NEW
  }
}
```

Pros:
- Zero new TLS handshakes — reuses the existing persistent connection
- Agent already has the connection open; new message types are handled in the existing `read_loop()`
- streamManager already fans to all connected agents — orchestration piggybacks directly
- No new authentication surface, no new interceptor code
- Buffer is already in place (size 8 per agentStream)

Cons:
- `oneof` proto field numbers must be globally unique and stable — every new variant is a permanent commitment
- The `send` buffer (size 8) can be overwhelmed during large rolling deployments that fan to hundreds of agents simultaneously
- Message ordering: task_dispatch and force_push share the same channel; a queued force_push could delay time-sensitive task dispatch

**Option B: Separate TaskStream RPC**

Add separate `rpc TaskStream(stream TaskAgentMessage) returns (stream TaskServerMessage)` to `HobnobAgent`.

Pros:
- Clean separation of concerns — orchestration has its own independent backpressure
- Can have larger buffers tuned for orchestration traffic without affecting policy delivery
- Proto surface is explicit about task semantics vs config delivery semantics

Cons:
- Second TLS-layer connection per agent (even though the transport reuses the same TCP via HTTP/2 multiplexing over mTLS, it requires agent to open a second `grpc.Dial` or second stream)
- Agent must manage two stream lifecycles with independent backoff/reconnect logic
- Doubles the complexity in agent's `stream_loop` — must select over two streams
- Requires new interceptor wiring for authentication
- Most agents are never given tasks — the second stream sits idle for the majority of the fleet

**Recommendation: Option A — extend `ServerStreamMessage`.**

The buffer concern is manageable: increase buffer from 8 to 64 for the `send` channel, or add a per-task send with blocking semantics for task dispatch (vs fire-and-forget for policy push). The existing infrastructure already handles the fundamental problem and adding new `oneof` variants is the standard protobuf extension point. A separate stream RPC would require every agent to establish and maintain a second connection that goes unused 99% of the time.

**Trade-off to watch:** If orchestration evolves to high-frequency streaming (e.g., live stdout streaming at 100ms intervals), the shared channel becomes a bottleneck. At that point, a dedicated stream is the right call. For the v1.3 scope (ad-hoc commands, tasks, live query), Option A is appropriate.

---

### Decision 2: Task Result Collection — Streaming vs Polling

**The question:** Should task stdout/stderr stream back incrementally, or be collected in full and reported at completion?

**Option A: Collect-and-report (batch result)**

Agent runs the command to completion, captures all stdout/stderr, sends one `AgentStreamMessage.task_result` with the full output.

Pros:
- Trivially simple — one new `AgentStreamMessage` variant, identical to `ReportResult` pattern
- No ordering concerns, no partial-state handling
- Result can be stored in a single SQLite row
- Works cleanly with existing `AgentStream` channel semantics

Cons:
- No live visibility for long-running commands (e.g., `apt upgrade`)
- CLI blocks waiting for the final result with no progress indicator
- Commands that produce large output (>10MB) need size limits

**Option B: Streaming stdout/stderr**

Agent sends incremental `TaskOutputChunk` messages as the command runs, followed by a `TaskComplete` with exit code.

Pros:
- Real-time feedback for long-running operations
- CLI can stream output to the terminal as it arrives

Cons:
- Requires ordered delivery guarantees (chunks must arrive in sequence)
- Server must buffer in-progress chunks until `TaskComplete` and either stream them to waiting CLI connections or persist them incrementally
- CLI must maintain a streaming HTTP connection (SSE or chunked transfer) to receive live output
- Significantly increases server complexity: in-progress task state management, client disconnect handling, chunk reassembly
- SQLite is not ideal for streaming writes — each chunk would be a separate INSERT

**Recommendation: Option A for v1.3, with Option B as a follow-on.**

The collect-and-report model handles the stated v1.3 requirements (ad-hoc commands, tasks, live query). A reasonable output size limit (e.g., 1MB stdout, 256KB stderr, truncated with notice) prevents abuse. Streaming output is a visible UX improvement but is a separate project-level feature with substantial complexity.

---

### Decision 3: Workflow State Machine Location

**The question:** Where does multi-step workflow execution state live?

**Option A: Server-side state machine (server/orchestration/workflow.go)**

The server orchestrates workflow execution: tracks current step, evaluates conditions, fans out to next batch of targets, waits for results, advances state.

Pros:
- Central visibility — one place to query workflow status
- Survives agent disconnects — server waits and retries
- Rolling execution coordinator naturally lives here (batch size, failure thresholds, inter-batch health checks)
- Consistent with Hobnob's design: server is the decision-maker, agents are executors

Cons:
- Server process must maintain in-memory workflow state — a crash loses in-progress workflows
- For long-running workflows (hours), server memory grows with concurrent workflows

**Option B: CLI-driven workflow execution**

The CLI (`hobnobcli workflow run`) implements the multi-step loop: dispatch step, poll results, evaluate condition, dispatch next step.

Pros:
- Zero new server complexity
- State lives in the CLI process — no crash recovery needed at server level
- Simple to implement: CLI just calls existing REST endpoints in a loop

Cons:
- CLI process death aborts the workflow — not suitable for long operations
- Cannot be triggered by the web UI (UI can't execute multi-step processes server-side)
- No audit trail of workflow execution
- Inter-step health checks require the CLI to remain running

**Recommendation: Option A — server-side state machine.**

The server already has the orchestration primitives. CLI-driven workflows are fragile (process death, network interruption). The server-side approach allows workflows to be initiated via REST API (from CLI or Web UI) and continue running after the initiator disconnects. State should be persisted in SQLite with a status field to survive server restarts.

---

### Decision 4: Rolling Execution Design

**The question:** How does rolling execution (batch N nodes, check success, proceed to next batch) coordinate?

The coordinator lives at `server/orchestration/rolling.go` and implements this state machine:

```
RollingJob
  id: uuid
  target_selector: string (glob or label)
  task: TaskSpec
  batch_size: int
  failure_threshold: float (0.0-1.0, abort if exceeded)
  inter_batch_check: optional HealthCheckSpec
  status: pending → running → success | aborted | partial

Execution:
  1. Resolve target_selector against enrolled thralls
  2. Partition into batches of batch_size
  3. For each batch:
     a. Dispatch task to batch via streamMgr
     b. Wait for all results (with timeout)
     c. Compute failure rate
     d. If failure_rate > threshold → abort remaining batches
     e. If inter_batch_check defined → run health check query
     f. If health check fails → abort remaining batches
     g. Advance to next batch
  4. Record final status
```

The coordinator uses a channel-based approach: dispatch and wait for `task_result` messages that match the current batch's task IDs. This requires correlating results back to the originating batch by `task_id` (a UUID embedded in the `StreamTaskDispatch` message).

**Integration with streamManager:** The rolling coordinator calls `streamMgr.sendTask(thrallID, taskMsg)` for each target in the batch. The streamManager returns immediately (fire-and-forget into the buffered channel). The coordinator then blocks waiting on a result channel until all results arrive or the timeout fires.

---

### Decision 5: Live Query Architecture

**The question:** How does "ask thralls for current state without waiting for check-in" work?

Live query bypasses the convergence loop entirely. It sends a `StreamQueryRequest` to connected agents, which immediately collects and returns current facts (traits) or resource state.

**Two query types:**

1. **Trait query:** Return current value of a specific trait (e.g., `os.kernel`, `network.ip`). The agent already has traits in memory from the last collection cycle. This is a read from the `CachedCollectors` cache — no OS invocation needed. Response time: <10ms.

2. **Resource state query:** Ask "what is the current state of resource X?" This runs a single executor's check phase without the apply phase. Response time: depends on resource type (file stat = 1ms; package query = 100ms).

The agent's `stream_loop` adds a new `tokio::select!` arm:

```rust
query_rx = query_rx.recv() => {
    let response = handle_query(&query, &collectors, &executors).await;
    send_query_response(&request_tx, response).await;
}
```

The server's `TaskService.LiveQuery()` handler:
1. Sends `StreamQueryRequest` to target agents via streamMgr
2. Blocks on a result channel with a configurable timeout (default 10s)
3. Returns results as they arrive (partial results ok — not all agents may be connected)

---

## System Overview (Post-Orchestration)

```
+------------------+    +------------------+    +------------------+
|   hobnobcli      |    |   Web UI         |    |   Webhooks       |
|                  |    |                  |    |  (task.complete, |
| task run         |    | Task history     |    |   workflow.done) |
| workflow run     |    | Workflow status  |    |                  |
| query get        |    | Live query       |    +------------------+
+--------+---------+    +--------+---------+
         |                       |
         | REST API (port 8181)  |
         v                       v
+--------+-----------------------------------------------+
|                    AdminService (REST)                  |
|  POST /api/v1/tasks      GET /api/v1/tasks/{id}        |
|  POST /api/v1/workflows  GET /api/v1/workflows/{id}     |
|  POST /api/v1/query      GET /api/v1/query/{id}        |
+--------+-----------------------------------------------+
         |
         v
+--------+---------------------------+
|        TaskService (new)           |
|  TaskStore (SQLite)                |
|  WorkflowEngine (state machine)    |
|  RollingCoordinator                |
|  QueryCollector                    |
+--------+---------------------------+
         |
         | streamMgr.sendTask(thrallID, msg)
         v
+--------+---------------------------+
|        streamManager (existing)    |
|  agentStream.send chan (extended)  |
+--------+---------------------------+
         |
         | ServerStreamMessage.task_dispatch (new variant)
         | gRPC AgentStream bidi (existing)
         v
+--------+---------------------------+
|        Rust Agent (existing)       |
|  read_loop() + new task_rx channel |
|  task executor (new)               |
|  query executor (new)              |
|  task_result → AgentStreamMessage  |
+------------------------------------+
```

---

## Recommended Project Structure

New server-side files:

```
server/
  orchestration/
    tasks.go           # TaskService: create task, dispatch via streamMgr, wait for results
    workflow.go        # WorkflowEngine: multi-step state machine
    rolling.go         # RollingCoordinator: batch execution with failure thresholds
    query.go           # LiveQuery: dispatch query, collect responses with timeout
  db/
    tasks.sql          # sqlc query source for task_runs, workflow_runs tables
    tasks.sql.go       # sqlc-generated (DO NOT EDIT)
    workflows.sql      # sqlc query source for workflow state
    workflows.sql.go   # sqlc-generated
  api/
    tasks.go           # REST handlers: POST /api/v1/tasks, GET /api/v1/tasks/{id}
    workflows.go       # REST handlers for workflows
    query.go           # REST handlers for live query
```

New agent-side files:

```
agent-rs/src/
  executors/
    task.rs            # TaskExecutor: run command, capture stdout/stderr, return TaskResult
    query.rs           # QueryExecutor: read current state without applying changes
  checkin/
    stream.rs          # MODIFIED: add task_rx channel, handle StreamTaskDispatch
    loop_impl.rs       # MODIFIED: add task_dispatch arm to stream_loop select!
```

Proto additions (`proto/hobnob.proto`):

```protobuf
// ServerStreamMessage — add variants 6 and 7
message StreamTaskDispatch {
  string task_id = 1;          // UUID for result correlation
  string command = 2;          // shell command to execute
  string stdin = 3;            // optional stdin
  int32 timeout_seconds = 4;   // 0 = no timeout
  string working_dir = 5;      // optional cwd override
  map<string,string> env = 6;  // additional env vars
}

message StreamQueryRequest {
  string query_id = 1;         // UUID for result correlation
  string trait_path = 2;       // dot-path for trait query, e.g. "os.kernel"
  string resource_type = 3;    // for resource state query
  map<string,string> params = 4;
}

// AgentStreamMessage — add variants 4 and 5
message TaskResult {
  string task_id = 1;
  string thrall_id = 2;
  int32 exit_code = 3;
  bytes stdout = 4;            // truncated at 1MB
  bytes stderr = 5;            // truncated at 256KB
  int32 duration_ms = 6;
  string error = 7;            // executor-level error (distinct from command exit code)
}

message QueryResponse {
  string query_id = 1;
  string thrall_id = 2;
  string value_json = 3;       // JSON-encoded result
  string error = 4;
}
```

---

## Data Flow

### Task Dispatch Flow

```
hobnobcli task run --target "web*.example.com" -- apt-get install -y nginx
        |
        | POST /api/v1/tasks
        | body: {target: "web*.example.com", command: "apt-get install -y nginx", timeout: 60}
        v
TaskService.CreateTask()
  → INSERT INTO task_runs (id, target, command, status='pending')
  → Resolve target glob against thralls table → [thrall-1, thrall-2, thrall-3]
  → For each thrall:
      streamMgr.sendTask(thrallID, StreamTaskDispatch{task_id: ..., command: ...})
  → INSERT INTO task_run_targets (task_id, thrall_id, status='dispatched')
  → Return {task_id: "uuid", status: "running"}

        | (response to CLI)
        | {task_id: "abc123"}

CLI polls: GET /api/v1/tasks/abc123
        v
TaskService.GetTask()
  → SELECT * FROM task_runs WHERE id = ?
  → SELECT * FROM task_run_targets WHERE task_id = ?
  → Return task + per-thrall results (pending/complete/failed)

        | Meanwhile, on each agent:
        v
read_loop() receives StreamTaskDispatch
  → task_rx.send(dispatch)
stream_loop select! arm for task_rx:
  → spawn tokio::task: run command, capture output
  → send AgentStreamMessage.task_result
        |
        v
Server grpc/streams.go AgentStream handler receives TaskResult
  → UPDATE task_run_targets SET status='complete', exit_code=..., stdout=..., stderr=...
  → Check if all targets complete → UPDATE task_runs SET status='success'/'partial'/'failure'
```

### Workflow State Machine Flow

```
POST /api/v1/workflows
body: {
  name: "deploy nginx",
  steps: [
    {target: "*.staging.example.com", command: "apt-get install -y nginx"},
    {condition: "all_success", target: "*.prod.example.com", command: "apt-get install -y nginx"}
  ]
}
        v
WorkflowEngine.Start(workflow)
  → INSERT INTO workflow_runs (id, status='running', step=0)
  → Execute step[0] as a task (via TaskService.CreateTask)
  → Wait for task completion (poll task_run_targets via goroutine + timer)
  → Evaluate step[1].condition against step[0] results
    → "all_success": all targets returned exit_code=0
  → If condition passes: execute step[1]
  → If condition fails: UPDATE workflow_runs SET status='aborted', abort_reason=...
  → On final step completion: UPDATE workflow_runs SET status='success'/'failed'
```

### Live Query Flow

```
GET /api/v1/query?target=web1.example.com&trait=os.kernel
        v
QueryCollector.Query(target, traitPath)
  → Resolve target → thrall-id
  → streamMgr.sendQuery(thrallID, StreamQueryRequest{query_id: ..., trait_path: "os.kernel"})
  → Block on result channel (timeout: 10s)
        |
        | On agent:
        v
read_loop() receives StreamQueryRequest
  → query_rx.send(request)
stream_loop select! arm:
  → collectors.get_cached_trait("os.kernel")  // no OS call, uses in-memory cache
  → send AgentStreamMessage.query_response{value_json: "\"5.15.0-91-generic\""}
        |
        v
QueryCollector receives response, sends to result channel
        |
        v
REST handler returns: {thrall: "web1.example.com", trait: "os.kernel", value: "5.15.0-91-generic"}
```

---

## SQLite Schema Additions

```sql
CREATE TABLE IF NOT EXISTS task_runs (
    id             TEXT PRIMARY KEY DEFAULT (uuid_v4()),
    created_at     DATETIME NOT NULL DEFAULT (datetime('now')),
    finished_at    DATETIME,
    command        TEXT NOT NULL,
    target_expr    TEXT NOT NULL,      -- original target glob/expression
    status         TEXT NOT NULL CHECK (status IN ('pending','running','success','partial','failed','timeout')),
    timeout_sec    INTEGER NOT NULL DEFAULT 60,
    initiator      TEXT               -- CLI user or "workflow:<workflow_id>"
);

CREATE TABLE IF NOT EXISTS task_run_targets (
    id             TEXT PRIMARY KEY DEFAULT (uuid_v4()),
    task_id        TEXT NOT NULL REFERENCES task_runs(id) ON DELETE CASCADE,
    thrall_id      TEXT NOT NULL REFERENCES thralls(id) ON DELETE CASCADE,
    status         TEXT NOT NULL CHECK (status IN ('pending','dispatched','complete','failed','timeout','offline')),
    exit_code      INTEGER,
    stdout         BLOB,              -- truncated at 1MB
    stderr         BLOB,              -- truncated at 256KB
    duration_ms    INTEGER,
    error          TEXT,
    completed_at   DATETIME
);

CREATE INDEX IF NOT EXISTS idx_task_run_targets_task_id ON task_run_targets(task_id);

CREATE TABLE IF NOT EXISTS workflow_runs (
    id             TEXT PRIMARY KEY DEFAULT (uuid_v4()),
    created_at     DATETIME NOT NULL DEFAULT (datetime('now')),
    finished_at    DATETIME,
    name           TEXT NOT NULL,
    definition     TEXT NOT NULL,     -- JSON-encoded workflow steps
    current_step   INTEGER NOT NULL DEFAULT 0,
    status         TEXT NOT NULL CHECK (status IN ('pending','running','success','failed','aborted')),
    abort_reason   TEXT
);

CREATE TABLE IF NOT EXISTS workflow_step_results (
    id             TEXT PRIMARY KEY DEFAULT (uuid_v4()),
    workflow_id    TEXT NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    step_index     INTEGER NOT NULL,
    task_id        TEXT REFERENCES task_runs(id),
    started_at     DATETIME NOT NULL DEFAULT (datetime('now')),
    finished_at    DATETIME,
    status         TEXT NOT NULL CHECK (status IN ('pending','running','success','failed','skipped'))
);
```

---

## Integration Points

### How `streamManager` Is Extended

`server/grpc/streams.go` adds two new methods:

```go
// sendTask delivers a task dispatch to a specific agent.
// Returns false if the agent is not connected or the buffer is full.
func (sm *streamManager) sendTask(thrallID string, task *pb.StreamTaskDispatch) bool

// sendQuery delivers a live query to a specific agent.
func (sm *streamManager) sendQuery(thrallID string, query *pb.StreamQueryRequest) bool
```

The `AgentStream` gRPC handler in `server/grpc/streams.go` is extended to handle incoming `TaskResult` and `QueryResponse` message variants. It routes them to result channels registered by the `TaskService` (via a pending-results map keyed by task_id/query_id).

### How `AgentStream` Handler Routes Task Results

The gRPC handler for `AgentStream` currently calls `buildCheckInResponse()` and fans results. It needs to additionally handle new incoming message types:

```go
case *pb.AgentStreamMessage_TaskResult:
    s.taskService.ReceiveResult(msg.TaskResult)
case *pb.AgentStreamMessage_QueryResponse:
    s.taskService.ReceiveQueryResponse(msg.QueryResponse)
```

`TaskService` maintains a `sync.Map[taskID, chan *pb.TaskResult]` — channels registered by the waiting goroutine in `CreateTask()`.

### How Agent `read_loop()` Is Extended

In `agent-rs/src/checkin/stream.rs`, the `read_loop` function adds dispatch for new server message types:

```rust
server_stream_message::Msg::TaskDispatch(dispatch) => {
    if task_tx.try_send(dispatch).is_err() {
        warn!("task dispatch dropped (buffer full)");
    }
}
server_stream_message::Msg::QueryRequest(req) => {
    if query_tx.try_send(req).is_err() {
        warn!("query request dropped");
    }
}
```

`StreamHandle` gains two new fields:
```rust
pub task_rx: mpsc::Receiver<StreamTaskDispatch>,
pub query_rx: mpsc::Receiver<StreamQueryRequest>,
```

### How `stream_loop` in Agent Is Extended

The `stream_loop` `tokio::select!` macro gains two new arms:

```rust
dispatch = task_rx.recv() => {
    // Spawn a tokio task to run the command non-blocking
    let tx = request_tx.clone();
    let thrall_id = config.thrall_id.clone();
    tokio::spawn(async move {
        let result = task::execute(dispatch).await;
        let msg = AgentStreamMessage { msg: Some(task_result(result)) };
        let _ = tx.send(msg).await;
    });
}

req = query_rx.recv() => {
    let response = query::handle(&req, &collectors, &executors).await;
    let msg = AgentStreamMessage { msg: Some(query_response(response)) };
    let _ = request_tx.send(msg).await;
}
```

The critical design choice here: task execution must be **spawned in a separate tokio task** so it does not block the `stream_loop`. A long-running command should not prevent policy pushes or force_pushes from being processed. This is analogous to how a web server handles concurrent requests.

---

## Scaling Considerations

| Concern | At 100 thralls | At 1,000 thralls | At 10,000 thralls |
|---------|---------------|-----------------|-------------------|
| Task fan-out | Single goroutine loops over connected streams, drops to buffered channels | Same; buffer overflows are logged, offline agents recorded as 'offline' status | Fan-out loop must be parallelized; consider goroutine pool |
| Rolling execution | Simple sequential batch loop is fine | Increase inter-batch timeout for health checks | Same pattern, but health check parallelism matters |
| Task result collection | sync.Map of result channels is fine | Same | High concurrency of concurrent tasks could stress the pending-results map; use sharded maps |
| SQLite under task load | Single writer; task result INSERTs are small and fast | May need WAL mode (already on SQLite, this is standard) | Consider archiving completed tasks to keep working set small |
| Live query concurrency | Single query blocks for 10s; easily parallelized | Concurrent queries multiply the pending channels | Limit concurrent live queries; queue excess |

**First bottleneck:** The `agentStream.send` channel buffer (currently size 8). Under rolling deployment to 100 agents, if the coordinator dispatches all 100 simultaneously, 92 task dispatches are dropped. **Fix:** Increase buffer to 64-128, or use blocking send in the coordinator (with per-agent goroutine so one slow agent doesn't block others).

**Second bottleneck:** SQLite write lock contention when many task results arrive simultaneously. **Fix:** Batch writes via a result collector goroutine (mirrors the existing `TraitsBatcher` pattern in `server/db/traitsbatcher.go`).

---

## Anti-Patterns

### Anti-Pattern 1: Treating Task Execution Like Policy Convergence

**What:** Reusing the convergence path (CheckIn → PolicyBundle → converge()) for task execution.

**Why it's wrong:** Policy convergence is idempotent by design — it checks state before acting. Task execution is explicitly not idempotent — "run this command once, right now." Routing tasks through the convergence path would add unnecessary overhead (trait collection, policy evaluation, DAG sort) and could trigger notify/subscribe side effects from the config management layer.

**Do this instead:** A dedicated `StreamTaskDispatch` message type and a task executor that is outside the convergence loop. The agent handles task execution as an independent concern in `stream_loop`, spawned as a separate tokio task.

### Anti-Pattern 2: Blocking `stream_loop` on Task Execution

**What:** Running the task command synchronously inside the `stream_loop` `tokio::select!` branch.

**Why it's wrong:** A 5-minute `apt upgrade` command would block the entire agent event loop. No policy pushes, no force pushes, no periodic check-ins would be processed during that time. The agent would appear stalled.

**Do this instead:** `tokio::spawn()` the task execution. The result sender is passed into the spawned task. The `stream_loop` returns immediately and continues processing other events.

### Anti-Pattern 3: Correlating Results by Thrall ID Alone

**What:** Using only `thrall_id` to match incoming `TaskResult` messages to pending task requests.

**Why it's wrong:** A thrall may have multiple concurrent tasks in flight (different workflows, concurrent CLI operators). Results would be routed to the wrong waiter.

**Do this instead:** Every `StreamTaskDispatch` includes a `task_id` UUID. The agent echoes this `task_id` in the `TaskResult`. The server routes results by `task_id`, not `thrall_id`.

### Anti-Pattern 4: Storing Large Stdout in SQLite BLOB Unconditionally

**What:** Storing full command output in the `task_run_targets.stdout` BLOB without a size limit.

**Why it's wrong:** `find / -name "*.log"` produces megabytes. Large BLOBs degrade SQLite performance, balloon the database file, and make backup/restore slow.

**Do this instead:** Truncate stdout at 1MB and stderr at 256KB. Include a `truncated: bool` field in `TaskResult`. Log a warning when truncation occurs. For use cases requiring full output, consider writing to a file on the agent and returning the file path.

---

## Build Order

Dependencies within v1.3:

```
Phase 1: Proto extension (required by everything)
  - Add StreamTaskDispatch, TaskResult, StreamQueryRequest, QueryResponse to hobnob.proto
  - Regenerate with `make proto`
  - No behavior changes; existing tests pass

Phase 2: SQLite schema + db layer
  - Add task_runs, task_run_targets, workflow_runs, workflow_step_results tables
  - Write sqlc queries, generate Go code
  - Depends on: Phase 1

Phase 3: Agent task executor + stream plumbing
  - agent-rs: task.rs executor (run command, capture output)
  - agent-rs: extend StreamHandle with task_rx
  - agent-rs: extend read_loop() to dispatch TaskDispatch messages
  - agent-rs: extend stream_loop with spawned task execution arm
  - Depends on: Phase 1

Phase 4: Agent query executor + stream plumbing
  - agent-rs: query.rs executor (trait read + resource check-phase-only)
  - agent-rs: extend StreamHandle with query_rx
  - agent-rs: extend stream_loop with query response arm
  - Depends on: Phase 1, Phase 3 (reuses task stream plumbing)

Phase 5: Server streamManager extension + result routing
  - server/grpc/streams.go: sendTask(), sendQuery() methods
  - server/grpc/checkin.go or new streams handler: route TaskResult and QueryResponse to pending channels
  - server/orchestration/tasks.go: TaskService with pending-results sync.Map
  - Depends on: Phase 1, Phase 2

Phase 6: REST API + CLI for ad-hoc tasks and live query
  - server/api/tasks.go: POST /api/v1/tasks, GET /api/v1/tasks/{id}
  - server/api/query.go: POST /api/v1/query
  - cli/tasks.go: `hobnobcli task run`, `hobnobcli task status`, `hobnobcli task list`
  - cli/query.go: `hobnobcli query`
  - Depends on: Phase 5

Phase 7: Workflow engine + rolling coordinator
  - server/orchestration/workflow.go: WorkflowEngine state machine
  - server/orchestration/rolling.go: RollingCoordinator
  - server/api/workflows.go: REST handlers
  - cli/workflows.go: `hobnobcli workflow run`, `hobnobcli workflow status`
  - Depends on: Phase 5, Phase 6 (workflows call TaskService internally)

Phase 8: Web UI integration
  - Task history page, live task output display
  - Workflow status visualization
  - Live query interface
  - Depends on: Phase 6, Phase 7
```

**Rationale for this order:**

1. Proto first because Go and Rust generated code must match before any integration tests can run.
2. Schema second because the result storage model constrains the API contract — changing it after API handlers are written forces API changes.
3. Agent work before server work because the fundamental question (can a task dispatch reach the agent and produce a result?) must be answered first. Early integration tests can verify the message round-trip before building the full orchestration stack.
4. Simple tasks and live query before workflows because workflows depend on the task primitive working correctly. Testing a task in isolation is easier than debugging a multi-step workflow where the task layer might also have bugs.
5. CLI before Web UI so there's a usable interface for QA during development.

---

## Sources

All findings are HIGH confidence — derived from direct reading of:
- `/home/dan/repos/hobnob/proto/hobnob.proto`
- `/home/dan/repos/hobnob/server/grpc/streams.go`
- `/home/dan/repos/hobnob/server/grpc/server.go`
- `/home/dan/repos/hobnob/server/grpc/services.go`
- `/home/dan/repos/hobnob/agent-rs/src/checkin/stream.rs`
- `/home/dan/repos/hobnob/agent-rs/src/checkin/loop_impl.rs`
- `/home/dan/repos/hobnob/agent-rs/src/runner/mod.rs`
- `/home/dan/repos/hobnob/agent-rs/src/executors/exec.rs`
- `/home/dan/repos/hobnob/server/db/runstore.go`
- `/home/dan/repos/hobnob/migrations/000001_initial_schema.up.sql`
- `/home/dan/repos/hobnob/server/config/types.go`
- `/home/dan/repos/hobnob/server/federation/hub.go`

---
*Architecture research for: Hobnob v1.3 Orchestration*
*Researched: 2026-03-11*
