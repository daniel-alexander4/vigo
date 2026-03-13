# Pitfalls Research

**Domain:** Adding orchestration to an existing pull-based configuration management system
**Researched:** 2026-03-11
**Confidence:** HIGH

## Critical Pitfalls

### Pitfall 1: Orchestration Tasks Collide With Convergence Runs

**What goes wrong:**
An operator runs `hobnobcli exec --target web01 "systemctl restart nginx"` at the same moment the agent's pull loop fires its convergence run. Both execute concurrently on the same node: the convergence run restarts nginx (via the service resource), then the ad-hoc task restarts it again a second later. More dangerously, a convergence run writes `/etc/nginx/nginx.conf` while an ad-hoc task is executing `nginx -t` against it. The ad-hoc task may see the file mid-write and report a false negative. In the worst case, both paths hold their own opinion of the node's state and their actions interfere, producing an unpredictable result.

**Why it happens:**
Convergence runs are triggered by the server's pull loop or force-push. Orchestration tasks arrive via the stream channel. There is no coordination between these two sources of agent work in the current architecture. The agent has one goroutine model for convergence and the stream-handling code simply fires commands into it.

**How to avoid:**
Implement a single-worker queue inside the agent. The agent has one execution goroutine. Both convergence triggers and orchestration tasks enter this queue. Use a priority scheme: in-flight convergence runs must complete before a task starts; tasks that arrive during a convergence run are buffered (with a bounded queue, not an unbounded one). Expose a `busy` signal from the agent that the server can query before dispatching a task via the stream. Never allow concurrent local execution of convergence + orchestration.

**Warning signs:**
- Agent receives and begins executing a task message while a convergence run is still in progress
- No mutual exclusion between convergence and task execution on the agent side
- Task results arrive interleaved with convergence run results in the RunStore
- Integration tests that always test convergence and orchestration in isolation from each other

**Phase to address:**
Phase 1 (ad-hoc command execution foundation). The execution queue model must be designed before any orchestration commands can be dispatched. This is architectural and cannot be retrofitted.

---

### Pitfall 2: Orphaned Processes When Streams Disconnect Mid-Task

**What goes wrong:**
An operator runs a long-running task (`apt-get dist-upgrade`, a custom migration script, a database backup). The agent's gRPC stream disconnects mid-execution (network hiccup, server restart, keepalive timeout). The agent's stream handler exits but the subprocess is still running under a detached goroutine or has been `os.Process.Release()`d. The task now runs with no owner: its stdout/stderr goes nowhere, its exit code is never collected, and the server marks the task as "lost" or times out waiting for results. On reconnect, the agent has no memory of the task. The operator reissues the command, and now two instances of the database backup are running simultaneously, corrupting the backup target.

**Why it happens:**
The existing stream model (`streamManager`) uses a `done` channel that is closed on disconnect. Process management (starting, waiting on, and killing child processes) is a separate concern that developers naturally implement in isolation. Connecting "stream closed = cancel subprocess" requires explicit effort that is easy to forget.

**How to avoid:**
Bind task lifetime to a Go `context.Context` derived from the stream's lifecycle. When the stream's `done` channel closes, cancel the context. All subprocess executions must use `exec.CommandContext(ctx, ...)` so the OS kills the child process on context cancellation. Implement a graceful shutdown window: on stream close, send SIGTERM to the running task, wait up to N seconds, then SIGKILL. Log "task aborted due to stream disconnect" with the task ID. On reconnect, the server must detect the task's pending state and either re-dispatch or mark it as failed with reason "stream_interrupted".

**Warning signs:**
- Task execution uses `exec.Command` instead of `exec.CommandContext`
- No handler on the stream's `done` channel that terminates running subprocesses
- Task status in DB can be "running" indefinitely with no timeout escalation
- No re-dispatch or failure-marking logic for tasks whose stream disconnected

**Phase to address:**
Phase 1 (ad-hoc command execution foundation). The context-bound subprocess model must be built into the first implementation. Retrofitting this later means existing code that uses `exec.Command` must be audited and changed.

---

### Pitfall 3: Ad-Hoc Command Execution Bypasses All Authorization Controls

**What goes wrong:**
Any admin with a hobnobcli token can run arbitrary shell commands as root on any managed thrall. There is no differentiation between "read-only query" and "destructive command." An operator meant to run `df -h` typos and runs `dd if=/dev/zero of=/dev/sda`. An intern with read access to the CLI can execute `rm -rf /`. A compromised operator credential gives an attacker root execution on the entire fleet. The existing gRPC auth (mTLS + ED25519 signatures) authenticates who is talking to the server, but does not restrict what commands they can run.

**Why it happens:**
Configuration management systems are designed around declared desired state — the "what to run" is already encoded in the YAML config, which is version-controlled and reviewed. Ad-hoc orchestration breaks this model: the "what" is now an arbitrary string passed at runtime. Developers naturally build the transport and execution path first, and treat authorization as a "we'll add RBAC later" problem. Later never comes.

**How to avoid:**
1. Define an allowlist model from day one. Orchestration commands must be validated against a server-side allowlist (a list of approved command patterns, or a list of approved named tasks). Raw shell strings are never passed directly to `/bin/sh -c` on the agent. Instead, the server defines named operations (`task: nginx-reload`, `task: check-disk-space`) and the agent executes the pre-defined command bound to that name.
2. Alternatively (if raw shell is required), implement a command sandbox: restrict the shell to a read-only chroot, or run the command as a restricted non-root user. Never run arbitrary operator commands as root.
3. Add audit logging to every task dispatch: who requested it, what command, at what time, on which thralls. This is non-negotiable for compliance.
4. For v1.3 with no RBAC: accept that all admins are trusted, but still implement audit logging and restrict the protocol to named tasks (not raw shell) as the transport.

**Warning signs:**
- Task dispatch protocol accepts arbitrary shell strings rather than named operations
- No server-side log entry recording who triggered what on which node
- Commands executed as the agent's user (root) without any sandboxing or restriction
- No allowlist or named-task model in the design

**Phase to address:**
Phase 1 (ad-hoc command execution foundation). The named-task model (vs. raw shell) must be a design decision before writing any execution code. This determines the entire protocol shape.

---

### Pitfall 4: SQLite Write Contention Spikes During Orchestration Fan-Out

**What goes wrong:**
An operator runs a workflow targeting 50 thralls simultaneously. Each thrall completes its task and calls back to the server to report results. The server writes task results to SQLite via the RunStore. SQLite has a single-writer lock: all 50 concurrent result-writers queue behind each other. Each write takes ~1ms but with 50 serialized writes the last thrall waits 50ms just for its result to commit. Worse, SQLite WAL mode helps readers but writers still serialize. If workflows fan out to hundreds of thralls, task result reporting becomes the bottleneck. SQLite returns `SQLITE_BUSY` under high write pressure; the Go mattn/go-sqlite3 driver surfaces this as a lock error. Result writes fail, task states become inconsistent, and the operator sees phantom failures.

**Why it happens:**
The existing system was designed for pull-based convergence where thralls check in at staggered intervals (jitter ensures they do not all write simultaneously). Orchestration deliberately synchronizes work across many thralls at once. This is the opposite of the pull model's design intent and breaks the assumption that SQLite write pressure is naturally spread out.

**How to avoid:**
1. Use WAL mode with `PRAGMA busy_timeout=5000` and `PRAGMA journal_mode=WAL` — already reduces contention significantly.
2. Batch task result writes: instead of writing each result immediately on arrival, buffer them for 100ms and batch-insert. The existing `BatchCreateRunResults` pattern is the right model.
3. For the orchestration-specific task tracking tables, consider using the in-memory `MemRunStore` backend for in-flight task state, with periodic checkpoints to SQLite. This keeps write pressure off SQLite during active workflows.
4. Limit orchestration fan-out batch sizes (rolling execution batches of 10-20 thralls, not all at once) to naturally stagger write pressure.
5. Set `_txlock=immediate` on the SQLite connection to avoid deferred-to-immediate lock escalation deadlocks.

**Warning signs:**
- `SQLITE_BUSY` errors appearing in server logs during orchestration runs
- Task result writes done one-at-a-time in separate transactions rather than batched
- SQLite write latency spikes when more than 20 thralls complete simultaneously
- No `busy_timeout` set on the SQLite connection (errors instead of retrying)

**Phase to address:**
Phase 1 (task result storage schema) and Phase 2 (rolling execution). The batch-write pattern must be established before building rolling execution, which deliberately creates concurrent result arrivals.

---

### Pitfall 5: Workflow State Corruption on Server Restart

**What goes wrong:**
A multi-step workflow is executing: step 1 (run on group A) has completed, step 2 (run on group B, conditional on step 1 success) is in progress. The server restarts (deploy, crash). The workflow state was held in memory (the orchestration engine's state machine). On restart, the server has no record of the workflow. The thralls in group B that received their tasks are still running them. When they complete and try to report back, the server has no context for their results. The operator sees group B's tasks as "orphaned results" with no parent workflow. The workflow is neither completed nor retryable — it is simply lost.

**Why it happens:**
Orchestration state is inherently transient: developers store it in memory because it is simpler and because they plan to "persist it later." The existing RunStore persists run results but not workflow state (the step DAG, which steps have passed, which groups are targeted at each step). Stateless in-memory orchestration feels fine in development where servers never restart.

**How to avoid:**
Persist workflow state to SQLite from the beginning. Define a `workflows` table and a `workflow_steps` table. Each state transition (step started, step completed, step failed, workflow aborted) is a DB write that happens before the transition takes effect (write-ahead logging for state machines). On server restart, scan for in-progress workflows and resume them. For tasks that were dispatched to thralls and are now orphaned (thrall completed but server has no context), provide a reconciliation path: on reconnect, the thrall reports its pending task results, the server matches them against the pending workflow step.

**Warning signs:**
- Workflow state held only in a Go struct in memory with no persistence
- Server restart during an active workflow leaves DB rows with status "running" indefinitely
- No reconciliation logic for task results that arrive after a server restart
- No workflow resume capability documented or implemented

**Phase to address:**
Phase 3 (multi-step workflows). The persistence model must be designed before workflows are built. A workflow without durable state is a feature that will cause production incidents on the first unplanned server restart.

---

### Pitfall 6: Stream Buffer Overflow Silently Drops Orchestration Messages

**What goes wrong:**
The current `agentStream.send` channel has a buffer of 8. During normal operation (policy pushes, force-push signals) this is sufficient — messages are small and infrequent. Orchestration introduces a new message type: task dispatch. If an operator triggers a workflow that dispatches tasks to multiple thralls faster than the stream goroutines can drain the send buffers (e.g., all 50 thralls are busy processing a previous task), the send buffer fills and `pushPolicy` silently drops the message with a log warning. The task is lost. The thrall never receives it. The workflow hangs waiting for a result that will never come — until a timeout fires, if one was implemented.

**Why it happens:**
The existing drop-on-full behavior was a deliberate choice for policy pushes: if the agent is slow to process, it will get the policy on its next check-in anyway. This fallback does not exist for orchestration tasks. A dropped task has no recovery path unless the server explicitly detects the drop and retries.

**How to avoid:**
1. Increase the send buffer for streams during orchestration, or use a separate dedicated channel for task messages with higher priority than policy pushes.
2. Change the task dispatch path to detect buffer-full conditions and implement retries with backoff (do not silently drop).
3. Add a task-acknowledgement protocol: the server sends a task, the agent acknowledges receipt within a timeout. If no acknowledgement arrives, the server retries or marks the task as undeliverable.
4. Alternatively, separate task dispatch from the policy push stream entirely: use a new RPC method for task dispatch that blocks until the agent acknowledges receipt, rather than fire-and-forget over a buffered channel.

**Warning signs:**
- Task dispatch reuses `pushPolicy`'s drop-on-full semantics without modification
- No task receipt acknowledgement in the protocol
- Log line "stream send buffer full, dropping policy push" appears during orchestration runs
- Workflows can hang indefinitely with no timeout or failure escalation

**Phase to address:**
Phase 1 (protocol design). The acknowledgement model must be part of the gRPC message design before any task dispatch code is written.

---

### Pitfall 7: Rolling Execution Failure Threshold Logic Is Wrong

**What goes wrong:**
An operator configures a rolling deployment: batch size 5, stop threshold 50% failures. The workflow runs 5 thralls in the first batch. 3 fail, 2 succeed. That is 60% failure — above the 50% threshold. But the check happens after the batch completes, not during it. Two more thralls received and started the task before the threshold was checked. Those two thralls are now running a task that should have been aborted. Alternatively: the operator intended "stop if any single batch exceeds 50% failures" but the implementation accumulated failure rate across all batches. After batch 1 (0/5 failures = 0%) and batch 2 (0/5 = 0%), batch 3 has 2/5 = 40% — still under 50%. But the operator expected each batch to be evaluated independently.

**Why it happens:**
Rolling execution thresholds sound simple ("stop after X% fail") but have multiple interpretation dimensions: per-batch vs. cumulative, pre-dispatch vs. post-completion, absolute count vs. percentage. Developers implement one interpretation, operators assume another.

**How to avoid:**
1. Define threshold semantics explicitly in the YAML syntax and document them. Recommend: per-batch evaluation (not cumulative), checked after all tasks in the batch report back (not mid-batch), with a configurable timeout per batch.
2. Support both `failure_count` and `failure_percent` to avoid ambiguity.
3. Implement a "pre-flight" health check: before dispatching a batch, check that all target thralls are reachable (have active streams). Batch size should count reachable thralls only; offline thralls do not count toward the failure threshold unless `count_offline_as_failure: true` is set.
4. Provide simulation/dry-run mode for rolling execution that shows which batches would be created and the threshold logic without executing anything.

**Warning signs:**
- Threshold semantics not written down (ambiguity will produce bugs)
- Tasks dispatched to next batch while current batch is still running
- Offline thralls counted in success/failure ratios (creates phantom failures)
- No dry-run mode for rolling execution plans

**Phase to address:**
Phase 2 (rolling execution). The threshold model must be specified and reviewed before implementation. A wrong threshold model will cause either false stops (operations blocked unnecessarily) or false continues (bad changes propagated past the intended stop point).

---

### Pitfall 8: Live Query Results Are Partial With No Signal to the Caller

**What goes wrong:**
An operator issues a live query: "show me current disk usage on all 200 web nodes." The server fans out the query to all connected thralls via their streams. 180 thralls respond within 2 seconds. 20 are slow (running heavy processes) or temporarily disconnected. The operator waits. The UI shows "waiting for results..." After 10 seconds the operator refreshes and sees 180 results. The remaining 20 are never surfaced as "did not respond" — they simply do not appear. The operator assumes 20 nodes do not have web roles (wrong) or that the query failed (also wrong). In reality the results are complete-but-partial with no indication of how many responses were expected vs. received.

**Why it happens:**
Fan-out queries require knowing the denominator (how many thralls were targeted) and tracking which responded vs. timed out. Developers implement the "collect responses" path but forget the "report non-responders" path because it requires tracking pending expectations rather than just accumulating arrivals.

**How to avoid:**
1. Before dispatching a live query, compute the expected response set: all thralls with active streams that match the target selector. Store this set as the query's "expected respondents."
2. Use a response timeout (e.g., configurable, default 30 seconds). After timeout, finalize the query results with explicit fields: `responded: 180`, `timed_out: 15`, `offline: 5`.
3. Return partial results immediately as they arrive (streaming response to the CLI/UI), with a final "query complete" event that includes the response summary.
4. Never silently omit non-responding thralls. Always report them as `status: timeout` or `status: offline`.

**Warning signs:**
- Query result API returns an array of responses with no "expected count" field
- No timeout on query response collection (waits indefinitely for all thralls to respond)
- CLI shows results as they arrive but never reports how many did not respond
- Thralls with no active stream are silently excluded from the query fan-out with no explanation to the operator

**Phase to address:**
Phase 4 (live query). The response-tracking model must be part of the query design. Building this correctly from the start prevents confusing operators with incomplete-but-indistinguishable-from-complete result sets.

---

### Pitfall 9: Federation Hub Does Not Propagate Orchestration Commands to Spokes

**What goes wrong:**
In a federated deployment, the hub aggregates admin queries but thralls connect to spokes, not the hub. Orchestration commands dispatched from the hub (via `hobnobcli exec`) must be forwarded to the correct spoke. The existing `ForwardForcePush` pattern calls `sc.admin.ForcePush` on each spoke — this works because force-push is a simple signal. Orchestration tasks require a richer protocol: task definition, target selector, result streaming back to the operator. If hub federation is not updated to handle the orchestration RPC, commands dispatched from the hub silently fail to reach thralls (the hub has no active agent streams — only spokes do), or partial forwarding leaves operators confused about which spoke received the command.

**Why it happens:**
The hub's `fanOut` model was built for read aggregation (list thralls, compliance summary) and simple signals (force-push). Orchestration introduces a new pattern: bidirectional streaming forwarded through the hub to a spoke and then to an agent. Developers build orchestration against the local (non-hub) code path first, and federation forwarding is treated as a follow-up task. It then never gets done.

**How to avoid:**
1. When designing orchestration RPCs, explicitly specify federation behavior in the proto comments: "hub must forward to spoke based on target thrall ID."
2. Add orchestration forwarding to the Hub's methods alongside `ForwardForcePush`. The pattern is established — it just needs to be applied to the new RPC.
3. Test with a federated setup as part of the orchestration test suite. If only tested against a single server, federation bugs are discovered by operators in production.
4. For stream-based result collection, design the hub as a transparent proxy, not a re-aggregator: the spoke streams task results directly to the caller (or through the hub with forwarding), rather than buffering everything on the spoke and returning a batch.

**Warning signs:**
- Orchestration RPC handlers exist on spoke but not on hub
- Hub has no forwarding methods for the new orchestration RPCs
- Integration tests for orchestration only run against single-server setups
- The hub's `HobnobAdmin` client (which spokes implement) lacks the orchestration endpoints

**Phase to address:**
Phase 1 and Phase 2. Design federation forwarding into the RPC shape from the start. The hub forwarding pattern is established; wiring it up for orchestration RPCs during initial implementation costs less than retrofitting.

---

### Pitfall 10: Task Output Buffering Causes Memory Pressure on Large Outputs

**What goes wrong:**
An ad-hoc command produces large output: `find / -name "*.log" -mtime -1` on a system with many logs returns megabytes of output. The server collects stdout/stderr from the agent via the stream and buffers it in memory in a `strings.Builder` or a byte slice, waiting for the task to complete before writing to the DB. With 20 concurrent tasks each producing 10 MB of output, the server holds 200 MB of buffered output in memory. With 100 concurrent tasks the server OOMs. Alternatively, the gRPC message size limit (default 4 MB) is hit and the stream errors out with "message too large."

**Why it happens:**
The simplest implementation of "collect task output" is to accumulate all output in a buffer and write it when done. This works fine for typical command output (a few KB) and fails silently at scale. The gRPC default message limit of 4 MB (`MaxRecvMsgSize` / `MaxSendMsgSize`) is rarely considered during development.

**How to avoid:**
1. Stream output in chunks: the agent sends stdout/stderr as a series of `TaskOutputChunk` messages (e.g., 64 KB each) over the stream. The server writes each chunk to the DB (or truncates after a configurable max). Never buffer the full output in memory.
2. Set a per-task output size limit (e.g., 1 MB by default, configurable up to 10 MB). Truncate with a "[output truncated at 1MB]" marker rather than failing the task.
3. For the proto definition: use a `bytes` field for output chunks, not a single `string` field for the entire output. This forces the streaming model at the protocol level.
4. Configure `grpc.MaxRecvMsgSize` and `grpc.MaxSendMsgSize` explicitly on both server and agent rather than relying on the default 4 MB limit.

**Warning signs:**
- Task result proto has a single `stdout_output string` field rather than a streaming `oneof` with chunk messages
- No output size limit defined in the task execution config
- Agent buffers the full subprocess output before sending anything back to the server
- No tests that verify behavior with outputs larger than 1 MB

**Phase to address:**
Phase 1 (protocol design and ad-hoc execution). The streaming output model must be in the proto definition before implementation begins. Changing from buffered-to-chunked after the fact requires proto changes and agent changes simultaneously.

---

### Pitfall 11: Convergence-Oriented Executors Are Not Appropriate for Ad-Hoc Tasks

**What goes wrong:**
The existing 33 resource executors (file, package, service, etc.) are designed around check-before-act idempotency. They check current state, compare to desired state, and act only if drift exists. Ad-hoc orchestration is the opposite: "run this command now, regardless of current state." If orchestration reuses the existing executor infrastructure (tempting because it avoids new code), the check phase produces unexpected results: `apt-get install nginx` checks whether nginx is installed, finds it is, and reports "no action taken." The operator expected a fresh install to be forced. Alternatively, resource executors may refuse to run without a full desired-state declaration, which ad-hoc tasks do not have.

**Why it happens:**
The convergence executor interface is well-defined and tested. Developers look at it and think "I can reuse this for task execution." The convergence model and the ad-hoc model are semantically incompatible but share surface-level similarities (both run commands on managed systems).

**How to avoid:**
Keep orchestration execution entirely separate from convergence execution. Ad-hoc tasks are raw command executions with no idempotency check. Implement a `TaskExecutor` interface that has only `Execute(ctx, command, args, timeout, stdin) (stdout, stderr, exitCode, error)` — no Check phase, no desired state, no drift detection. Do not let the convergence executor interface bleed into the orchestration path. The separation enforces the correct mental model: convergence is declarative and idempotent; orchestration is imperative and one-shot.

**Warning signs:**
- Orchestration task execution calls the same executor interface as convergence modules
- Ad-hoc task descriptions use the same YAML syntax as module resources (both have `type:`, `name:`, etc.)
- Task execution produces "no action taken" results (copied from convergence reporting)
- No dedicated `TaskExecutor` type in the agent codebase

**Phase to address:**
Phase 1 (task executor design). The separation must be enforced at the interface level before any code is written.

---

## Technical Debt Patterns

Shortcuts that seem reasonable but create long-term problems.

| Shortcut | Immediate Benefit | Long-term Cost | When Acceptable |
|----------|-------------------|----------------|-----------------|
| Using the existing `send` channel for task dispatch (fire-and-forget) | No new protocol machinery needed | Silent task drops with no operator feedback; impossible to guarantee delivery | Never for task dispatch — use acknowledged delivery |
| Storing workflow state in memory only | Simple to implement; no schema changes | Lost on server restart; operators discover this during a production deploy | Never — persist from day one |
| Running ad-hoc commands as root (agent user) without restriction | Simplest execution model; no sandboxing complexity | Any operator with CLI access can destroy any managed node | Acceptable only in closed, fully-trusted admin environments with audit logging |
| Reusing convergence RunStore for task results | No new DB tables needed | Convergence semantics (module/resource/changed) do not map to task semantics (stdout/exit_code/elapsed); forces square peg into round hole | Never — tasks need their own schema |
| Building orchestration without federation forwarding | Faster to ship single-server path | Hub deployments silently fail to dispatch tasks; discovered by operators in production | Only if federation is explicitly out of scope for the milestone |
| Streaming task output as a single message | Simpler proto definition | 4 MB gRPC message limit; server OOMs on large outputs; no progressive display in CLI | Only for guaranteed-small outputs (live query results, not exec output) |
| Not implementing task cancellation | Simpler execution model | No way to stop a runaway command; operator must SSH in to kill the process | Never — always implement cancellation from the start |

## Integration Gotchas

Common mistakes when wiring orchestration into the existing system.

| Integration | Common Mistake | Correct Approach |
|-------------|----------------|------------------|
| `streamManager` + task dispatch | Calling `pushPolicy` (drop-on-full) for task messages | Add a separate `dispatchTask` method with blocking send + timeout + acknowledgement; task messages must not be dropped silently |
| SQLite + concurrent task results | Inserting task results one-at-a-time from multiple goroutines | Batch inserts; use `PRAGMA busy_timeout`; consider in-memory buffering for in-flight tasks |
| Rust agent + async tokio | Spawning subprocesses and forgetting to track them in the task registry | Use tokio's `JoinHandle` per task; store handles in a `HashMap<TaskID, JoinHandle>`; cancel on stream close |
| Federation hub + task dispatch | Hub dispatching tasks to all spokes and all spokes attempting to execute on all thralls | Route by thrall ID — determine which spoke owns the thrall before dispatching; the hub routes, only one spoke executes |
| RunStore + task results | Using `run_results` table (designed for module/resource/changed tuples) for task stdout/stderr | Add new tables: `tasks` and `task_outputs`; keep convergence and orchestration data separate |
| gRPC keepalive + long-running tasks | Keepalive kills streams idle during a long task (no messages flowing) | Send periodic heartbeat/progress messages during long-running tasks to keep the stream alive; configure `MaxConnectionIdle` longer than the maximum expected task duration |
| Rolling execution + federation | Computing batch membership from local thrall registry (missing federation thralls) | For federated deployments, query all spokes to build the target list before partitioning into batches |

## Performance Traps

Patterns that work at small scale but fail as usage grows.

| Trap | Symptoms | Prevention | When It Breaks |
|------|----------|------------|----------------|
| Collecting live query results synchronously (blocking on all responses) | CLI hangs waiting for slow/offline thralls; operator experience degrades with fleet size | Stream results as they arrive; finalize with timeout and non-responder report | >50 thralls in query target, any with latency >2s |
| Fan-out task dispatch without backpressure | Server overwhelms itself sending to hundreds of stream channels simultaneously | Dispatch in batches with configurable concurrency limit; use a worker pool for stream writes | >100 concurrent task dispatches |
| Task audit log as append-only rows per event | Audit table grows unboundedly; compliance queries scan millions of rows | Partition audit table by month; add retention policy; index by (thrall_id, timestamp) | >500 task executions/day after 3 months |
| Workflow step evaluation in a single transaction | Long-running workflow holds a write transaction open while waiting for thrall responses | Never hold a DB transaction open across network I/O; transition states atomically but release locks immediately | Any workflow with step duration >100ms |
| In-memory task state for concurrent workflows | Memory grows with number of concurrent workflows; GC pressure increases | Set max concurrent workflow limit; use DB as the source of truth for task state, not in-memory structs | >20 concurrent workflows |
| gRPC message size limit for task output | Task fails with "received message larger than max" when output exceeds 4 MB | Configure `MaxRecvMsgSize` and stream output in chunks from the start | Any command producing >4 MB output |

## Security Mistakes

Domain-specific security issues beyond general web security.

| Mistake | Risk | Prevention |
|---------|------|------------|
| Accepting raw shell command strings in the task dispatch proto | Any operator can execute arbitrary shell as root on any managed node; a compromised CLI token owns the fleet | Use named operations or a command allowlist; never pass raw shell strings through the dispatch protocol |
| No audit log for task dispatches | Impossible to investigate post-incident; "who ran what on which node and when" is unanswerable | Every task dispatch must write an immutable audit record with: operator identity, command/task name, target thralls, timestamp, outcome |
| Task results containing secret values | An operator runs `cat /etc/myapp/db.conf` and the output (containing resolved secrets) is stored in the `task_outputs` table in plaintext | Warn operators that task stdout/stderr is stored in plaintext; never use tasks to extract config file contents that were templated from secrets; consider output masking for known secret patterns |
| Re-using agent's ED25519 key for task result signing without replay protection | Task result replay: a captured result message can be re-submitted to the server, making a failed task appear successful | Include task ID and sequence number in the signed payload; server rejects results for already-completed task IDs |
| No timeout on task execution | A task that hangs (network call to unreachable host, interactive prompt) ties up the agent execution goroutine indefinitely | Enforce a hard timeout on every task (configurable, default 5 minutes); SIGKILL after timeout; report failure with "task timed out" |
| Federation: hub authenticates task dispatches but spokes do not re-validate | A compromised spoke could forward task dispatches to agents without hub authorization | Spokes must re-verify that a task dispatch was authorized by a legitimate hub, not just any caller on the admin port |
| Task names used as keys in DB without sanitization | SQL injection via task name if task names flow into dynamic queries | Use parameterized queries everywhere; validate task names against a strict alphanumeric+hyphen pattern at ingestion |

## UX Pitfalls

Common user experience mistakes in this domain.

| Pitfall | User Impact | Better Approach |
|---------|-------------|-----------------|
| Rolling execution shows no progress until batch completes | Operator sees "running..." for minutes with no visibility into which thralls succeeded | Stream per-thrall status updates as they complete; show running count, success count, failure count in real time |
| Live query results returned as a flat list with no non-responder report | Operator cannot tell if results are complete; assumes complete when they are not | Always show: responded/expected/timed_out counts alongside results; use a progress indicator while query is open |
| Workflow step failures show "step failed" without task-level detail | Operator cannot diagnose which thrall's task failed or why | Show per-thrall task results within each workflow step; clicking a failed step reveals which thralls failed and their stderr |
| Task cancellation silently succeeds or silently fails | Operator runs `hobnobcli task cancel <id>` and gets no feedback; the task may or may not have been killed | Report per-thrall cancellation status: "cancelled on 8/10 thralls; 2 already completed; task output saved for completed thralls" |
| Ad-hoc command output truncated with no indication | Output is truncated at 1 MB but the last visible line makes it appear complete | Always append a visible "[output truncated at N bytes — use --output-limit to increase]" marker at the truncation point |
| No way to replay a failed workflow from the failed step | Operator must re-run the entire workflow from step 1 even if steps 1-3 completed successfully | Support `hobnobcli workflow resume <id>` that re-runs from the first failed or incomplete step |

## "Looks Done But Isn't" Checklist

Things that appear complete but are missing critical pieces.

- [ ] **Ad-hoc task dispatch:** Often missing task receipt acknowledgement — verify the protocol has a `TaskAck` message from agent to server before the caller's request returns success.
- [ ] **Task execution:** Often missing the convergence/orchestration mutual exclusion — verify there is a mutex or queue that prevents a convergence run and a task from executing simultaneously on the agent.
- [ ] **Stream disconnect handling:** Often missing subprocess cancellation on stream close — verify that `exec.CommandContext` is used and the context is derived from the stream's `done` channel.
- [ ] **Workflow persistence:** Often missing the initial state write — verify that a workflow record is written to SQLite before the first task is dispatched (not after).
- [ ] **Rolling execution:** Often missing the offline-thrall handling — verify that thralls with no active stream are not silently excluded; they should be reported as "offline" in the batch result.
- [ ] **Live query:** Often missing the timeout and non-responder report — verify that queries have a configurable deadline and that the final result includes `timed_out: N` for non-responding thralls.
- [ ] **Audit logging:** Often missing the "before execution" log entry — verify there is an audit record written when a task is dispatched (not just when it completes), so dispatches are recorded even if the server crashes mid-execution.
- [ ] **Federation:** Often missing the hub forwarding for the new orchestration RPCs — verify that the `Hub` type has corresponding forwarding methods for every new orchestration RPC.
- [ ] **Output limits:** Often missing the truncation marker — verify that truncated output includes a human-readable "[truncated]" marker, not a silent cut-off.
- [ ] **Task cancellation:** Often missing the SIGKILL fallback — verify that cancellation sends SIGTERM, waits a grace period, then sends SIGKILL if the process has not exited.

## Recovery Strategies

When pitfalls occur despite prevention, how to recover.

| Pitfall | Recovery Cost | Recovery Steps |
|---------|---------------|----------------|
| Convergence/orchestration collision corrupted node state | MEDIUM | Run `hobnobcli preview` to see what the server thinks the node's desired state is. Run a forced convergence (`hobnobcli push <thrall>`) to re-apply declared state. For package/service collisions, manual SSH verification may be needed. |
| Orphaned processes from stream disconnect | LOW | SSH to the thrall and kill the orphaned process by task ID (visible in process name if the agent sets it). Mark the task as failed in the DB via CLI or direct SQL update. Add stream-bound subprocess tracking to prevent recurrence. |
| Workflow lost on server restart | MEDIUM | Identify the last completed workflow step from the DB (`task_outputs` table). Manually re-run the remaining workflow steps as individual ad-hoc tasks. Implement workflow persistence before re-enabling multi-step workflows. |
| SQLite SQLITE_BUSY during orchestration fan-out | LOW | Set `PRAGMA busy_timeout=5000` immediately. Reduce concurrent task fan-out batch size. Switch to MemRunStore for in-flight task state. |
| Task output exceeded size limit and was truncated | LOW | Re-run the task with `--output-limit` increased, or SSH to the thrall and redirect output to a file for manual inspection. |
| Rolling execution proceeded past failure threshold | HIGH | Immediately issue `hobnobcli workflow abort <id>` to stop further batches. Manually revert the already-executed batches using rollback module or manual SSH. Fix the threshold logic before re-running. |
| Raw shell command caused unintended destruction | HIGH | Assess damage. Use file snapshots to roll back changed files. For package/service/user changes, manual remediation is needed. Implement command allowlist before re-enabling raw shell execution. |

## Pitfall-to-Phase Mapping

How roadmap phases should address these pitfalls.

| Pitfall | Prevention Phase | Verification |
|---------|------------------|--------------|
| Convergence/orchestration collision | Phase 1: Ad-hoc execution foundation | Unit test: trigger convergence run + task simultaneously; verify only one executes at a time |
| Orphaned processes on stream disconnect | Phase 1: Ad-hoc execution foundation | Integration test: disconnect agent mid-task; verify subprocess is killed; verify server marks task as stream_interrupted |
| Authorization bypass via raw shell | Phase 1: Protocol design | Code review: task dispatch proto uses named operations, not raw shell strings; audit log writes verified in every dispatch path |
| SQLite write contention | Phase 1 + Phase 2 | Load test: 50 thralls report task results simultaneously; verify no SQLITE_BUSY errors; measure p99 write latency |
| Workflow state corruption on restart | Phase 3: Multi-step workflows | Integration test: kill server during active workflow; restart; verify workflow resumes from correct step |
| Stream buffer overflow drops tasks | Phase 1: Protocol design | Integration test: fill agent's send buffer; attempt task dispatch; verify task is not silently dropped (blocked or rejected with error) |
| Rolling threshold logic errors | Phase 2: Rolling execution | Unit tests covering all threshold semantics: per-batch vs. cumulative, percent vs. count, offline thrall handling |
| Live query partial results unreported | Phase 4: Live query | Integration test: 10 thralls targeted, 3 offline; verify result includes `offline: 3`, not just 7 results |
| Federation does not propagate orchestration | Phase 1 + Phase 2 | Integration test with hub+spoke setup; verify task dispatched from hub reaches thrall on spoke |
| Output buffering memory pressure | Phase 1: Protocol design | Integration test: task producing 5 MB output; verify no OOM; verify output is chunked and truncated with marker |
| Executor model mismatch | Phase 1: Task executor design | Code review: task execution path does not call convergence executor interface; `TaskExecutor` is a separate type |

## Trade-Offs: Safety vs. Operational Flexibility

The core tension in adding orchestration to a pull-based CM system is that orchestration is inherently imperative (do this now, on this node, once) while the existing system is declarative (this is the desired state, converge to it). Every design decision involves a trade-off between these two models.

| Decision | Safe Choice | Flexible Choice | Recommendation |
|----------|-------------|-----------------|----------------|
| Command model | Named operations only (allowlist) | Raw shell strings | Named operations for v1.3; document that raw shell is a future option if RBAC is implemented |
| Task/convergence isolation | Hard mutual exclusion (queue, no concurrency) | Allow concurrent execution (faster, more responsive) | Hard mutual exclusion; the failure modes of concurrent execution are too unpredictable |
| Output size | Hard cap at 1 MB, truncate | No cap, stream unlimited | Hard cap with configurable limit up to 10 MB; unlimited output causes OOM without benefit |
| Workflow state | Durable from day one | In-memory until "stable enough" | Durable from day one; in-memory workflows create production incidents |
| Task cancellation | Implemented from day one | Added later | Day one; a task you cannot cancel is a liability |
| Audit logging | Every dispatch logged immutably | Optional/configurable | Always on, not configurable; audit logs are for compliance, not performance |
| Live query completeness | Always report non-responders | Return whatever arrives | Always report non-responders; operators cannot make decisions on implicitly incomplete data |

## Sources

- [SaltStack orchestration architecture and common mistakes](https://www.simplethread.com/server-orchestration-using-saltstack/) — MEDIUM confidence (community article, 2024)
- [SaltStack CVE history: command injection via task dispatch](https://www.cvedetails.com/vulnerability-list/vendor_id-12943/product_id-26420/Saltstack-Salt.html) — HIGH confidence (CVE database)
- [SQLite single-writer constraint and WAL mode](https://oldmoe.blog/2024/07/08/the-write-stuff-concurrent-write-transactions-in-sqlite/) — HIGH confidence (official SQLite docs corroborated)
- [SQLite connection pool pitfalls in server deployments](https://www.jtti.cc/supports/3154.html) — MEDIUM confidence
- [gRPC streaming reliability and reconnection patterns](https://oneuptime.com/blog/post/2026-01-24-grpc-bidirectional-streaming/view) — HIGH confidence (official gRPC patterns)
- [gRPC flow control and buffer limits](https://grpc.io/docs/guides/flow-control/) — HIGH confidence (official gRPC documentation)
- [Temporal: workflow state persistence and at-least-once execution](https://temporal.io/blog/error-handling-in-distributed-systems) — HIGH confidence (Temporal official blog)
- [Database-backed workflow orchestration (QCon SF 2025)](https://www.infoq.com/news/2025/11/database-backed-workflow/) — HIGH confidence (conference talk, 2025)
- [Cascading failure prevention in distributed systems](https://blog.mi.hdm-stuttgart.de/index.php/2022/03/03/cascading-failures-in-large-scale-distributed-systems/) — MEDIUM confidence
- [Remote access audit logging for compliance](https://logz.io/blog/audit-logs-security-compliance/) — MEDIUM confidence
- [crewAI orphaned threads and resource leaks in timeout handling](https://github.com/crewAIInc/crewAI/issues/4135) — MEDIUM confidence (real-world issue, 2025)
- [Configuration management vs. orchestration: separation of concerns](https://www.pass4sure.com/blog/a-deep-dive-into-configuration-management-and-orchestration/) — MEDIUM confidence
- Hobnob codebase review: `server/grpc/streams.go`, `server/db/runstore.go`, `server/federation/hub.go`, `server/grpc/server.go` — HIGH confidence (direct code inspection)

---
*Pitfalls research for: Adding orchestration to pull-based configuration management (Hobnob v1.3)*
*Researched: 2026-03-11*
