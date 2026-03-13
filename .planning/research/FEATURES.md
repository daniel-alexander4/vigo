# Feature Research

**Domain:** Orchestration capabilities for pull-based configuration management systems
**Researched:** 2026-03-11
**Confidence:** HIGH

## Context

This document replaces the v1.0 FEATURES.md (config management domain) with orchestration-specific feature research for milestone v1.3. It focuses on the five target capabilities: ad-hoc command execution, task/script management, workflow/plan definitions, rolling execution strategies, and live query. For each capability, different implementation approaches are analyzed with explicit trade-offs, as required by the downstream roadmap consumer.

The existing infrastructure available to build on:
- Bidirectional gRPC streaming (AgentStream) with `send chan *pb.ServerStreamMessage`
- Force-push delivery already works through this channel (proves the push path)
- Run result tracking: per-resource action, changed flag, error, duration stored in SQLite
- Trait collection: 25 collectors, traits stored as JSON blobs, accessible for targeting
- CLI (hobnobcli): Cobra-based with push, runs, preview subcommands
- Stream manager: `pushPolicy(thrallID)` and `pushPolicyAll()` — non-blocking, buffered

---

## Feature Landscape

### Table Stakes (Users Expect These)

Features users assume exist in any tool marketing itself as having "orchestration." Missing these = the feature isn't orchestration, it's just scripting.

| Feature | Why Expected | Complexity | Notes |
|---------|--------------|------------|-------|
| Ad-hoc command execution on targeted thralls | Ansible `ansible -m shell`, Salt `salt '*' cmd.run`, Puppet Bolt `bolt command run`. Every ops team's first question is "can I run a command on these boxes right now?" | MEDIUM | Uses existing bidirectional stream. Server sends command via stream; agent runs it and streams back stdout/stderr. Targeting by hostname glob or trait expression reuses existing trait data. |
| Multi-thrall targeting (glob, label, trait filter) | Ansible's inventory patterns, Salt's compound matching, Puppet Bolt's `--targets`. Running a command on "all web servers" is the primary use case. | LOW | Trait data already in DB (`thrall_traits` table). Targeting is a query against stored traits plus hostname glob matching. No new infrastructure required, just a query layer. |
| Result collection per thrall (stdout, stderr, exit code) | Ansible shows per-host results inline. Salt returns a dict of minion → result. Bolt shows per-target results. Operators need to see what happened on each thrall. | LOW | Run result infrastructure already exists. Need a new result type for command/task outputs (stdout/stderr/exit_code vs the current resource-oriented result schema). |
| Execution status visibility (running, succeeded, failed, timed out) | Tower's job detail page. Salt's job cache. Puppet Enterprise's task log. Seeing which thralls are still running vs done is table stakes. | MEDIUM | Run store with per-thrall status tracking. Server receives results asynchronously as thralls report back; needs per-thrall status within a job. |
| CLI subcommand for ad-hoc execution | `hobnobcli run command --target '*.web' -- nginx -t`. Ansible users expect `ansible -m command -a`. Salt users expect `salt`. | LOW | New Cobra subcommand. Calls POST /api/v1/jobs, polls for completion. Pattern established by existing `push`, `runs` subcommands. |
| Timeout enforcement | Ansible has `--timeout`. Salt has timeout in master config. Commands that hang block operators. | LOW | Server sets deadline in command payload; agent enforces it via `context.WithTimeout`. Server marks timed-out thralls when deadline passes without result. |

---

### Differentiators (Competitive Advantage)

Features that distinguish Hobnob's orchestration from bolted-on execution tools.

| Feature | Value Proposition | Complexity | Notes |
|---------|-------------------|------------|-------|
| Trait-based targeting with live trait data | Ansible targets inventory groups (static). Salt uses grains (cached at startup — static by default). Hobnob has 25 real-time trait collectors and can target based on any trait value at query time. Example: `--target 'traits.kernel.version >= 5.15'`. | MEDIUM | Trait data is already in DB, queried per check-in. Ad-hoc targeting queries the same store. Real trait freshness is bounded by last check-in interval. |
| Tasks with typed parameter schemas | Puppet Bolt's task model: scripts + JSON metadata describing parameters and types. Inputs validated before execution. Results are structured JSON, not raw stdout. Bolt plans can consume structured task outputs. Salt execution modules with structured return values are similar. | HIGH | New concept for Hobnob. Tasks are named, versioned scripts stored server-side with parameter schemas. Agent receives task payload with validated params; returns structured JSON. Enables workflow chaining (task A's output feeds task B's input). |
| Workflows with inter-step conditionals | Puppet Bolt plans: sequential steps with Puppet DSL conditionals. Ansible Tower workflows: a DAG of job templates with on-success/on-failure edges. Salt orchestration: `orch.sls` with requisites between states across minions. Hobnob can use its existing conditional infrastructure (structured conditionals, trait-based conditions). | HIGH | New server-side workflow engine. Workflows are YAML files (consistent with Hobnob's config philosophy). Steps reference tasks or commands, with `on_success:`, `on_failure:`, `when:` conditions using the same syntax as module conditionals. |
| Rolling execution with health-gate abort | Salt's orchestrate runner supports sequential minion batches. Ansible Tower workflows support batch sizes. Puppet Bolt implements rolling via plan logic (manual loop). Hobnob can make this a first-class parameter: `batch_size: 10%`, `max_failures: 2`, `health_check_command: 'systemctl is-active nginx'`. | HIGH | Built on rolling job scheduling in server. After each batch, server waits for health check results before proceeding. Failure threshold triggers workflow abort. No equivalent first-class UX in open-source Ansible or Puppet Bolt. |
| Live query over gRPC stream | Salt's `grains.item` returns current values. Ansible's `setup` module gathers fresh facts. Hobnob's bidirectional stream can deliver query requests to connected agents without waiting for check-in. For connected agents, response latency can be sub-second vs waiting up to 5 minutes for check-in. | MEDIUM | New stream message type: `QueryRequest`. Agent handles it inline, responds immediately with requested trait paths or command output. Only works for agents currently connected via AgentStream. Disconnected agents fall back to cached trait data. |
| Job history with full result archive | Ansible has stdout logs. Salt's job cache expires by default. Bolt doesn't persist results. Storing full stdout/stderr of every command execution in SQLite gives operators an audit trail for compliance. | LOW | Extends existing run_results table or adds a parallel job_results table. Minor schema change; all infrastructure exists. |

---

### Anti-Features (Commonly Requested, Often Problematic)

| Feature | Why Requested | Why Problematic | Alternative |
|---------|---------------|-----------------|-------------|
| Push-based agent connections (server initiates TCP) | "I want to push commands without waiting for the agent to connect." Ansible's SSH model does this. | Requires server to reach all agents (breaks firewall/NAT model that justified pull). Creates outbound connection management complexity. The existing AgentStream already keeps agents connected — use it. | Use AgentStream: agents maintain persistent bidirectional connection. Server sends commands over the existing stream. Any connected agent is reachable within milliseconds. |
| Arbitrary Python/shell/Ruby in workflow definitions | "Let me write loops and conditionals in a real language." Chef's orchestration lets you write Ruby. | Turns YAML configs into programs. Makes workflows unreadable to sysadmins who aren't developers. Creates security concerns (arbitrary code execution in server context for workflow evaluation). Puppet Bolt's Puppet-language plans have this problem — they require learning a DSL. | Structured YAML with `when:` conditions using the same trait-based conditional syntax as module configs. Loops via `targets:` expansion. Complex logic lives in tasks (scripts), not workflow definitions. |
| Global fan-out commands to all thralls simultaneously | "I want to run this on everything right now." Salt's `salt '*' cmd.run` does this. | Thundering herd: all thralls execute simultaneously, saturate network/server, results flood back. At 500+ thralls, this causes result processing backlog and server memory spikes. Already documented as a pitfall for force-push. | Rolling execution with configurable batch size is the right default. `--concurrency all` is an opt-in escape hatch for small fleets, not the default. |
| Blocking CLI output streaming all results live | "I want to watch all 200 thralls' stdout in my terminal." Ansible does this — prints as results arrive. | Unreadable at any meaningful scale. 200 thralls printing stdout simultaneously produces noise, not signal. Terminal output is not auditable. | CLI shows per-thrall status (running/done/failed) in a progress view. Full stdout/stderr accessible via `hobnobcli runs get <job-id> --thrall web01` after completion. SSE stream for real-time status from web UI. |
| Workflow GUI builder (drag-and-drop DAG editor) | "I want to build workflows visually." Ansible Tower has this. | Significant frontend complexity. DAGs built in a GUI still need to be serialized somewhere. Creates a second source of truth (GUI definition vs YAML file). The target audience (sysadmins) prefers YAML files. | Workflow definitions are YAML files in a `workflows/` directory alongside other Hobnob configs. Filesystem is the single source of truth. GitOps-native. |
| Stateful long-running jobs (hours/days) | "Run this weekly maintenance script and let it run overnight." | Long-running gRPC streams are fragile — network interruptions, agent restarts, OS-level timeouts. Server-side job state becomes complex (resume? restart? checkpoint?). | Tasks execute as background processes on the agent. The agent reports progress and final result when done, decoupled from the stream lifetime. The agent can reconnect and report pending results after reconnect. |
| Agent-side workflow evaluation | "Let the agent decide which steps to run." | Distributes workflow logic to agents, making it impossible to trace or audit from the server. Changes on one agent affect what another agent does. All workflow control belongs in the server. | Server-side workflow engine with full visibility. Agents receive individual task/command payloads; server orchestrates the sequence. |

---

## Approaches and Trade-offs by Capability

This section analyzes the design space for each of the five target capabilities, presenting alternative approaches with explicit trade-offs.

### Capability 1: Ad-hoc Command Execution

**The question:** How does the server deliver a command to an agent and collect the result?

**Approach A: Via AgentStream (push over existing bidirectional stream)**

Server sends a new `ServerStreamMessage` variant (e.g., `ExecuteCommand`) to the connected agent's stream. Agent executes, streams back a `ClientStreamMessage` with stdout/stderr/exit_code chunks or a final result message.

Trade-offs:
- Requires agent to be connected (AgentStream must be live). Disconnected agents cannot receive commands.
- Latency: sub-second for connected agents (stream already open).
- Result delivery: agent must send result back over the same stream while the stream is open.
- **This is the right approach for Hobnob.** Force-push already uses this channel. The infrastructure exists. Most production agents are connected continuously.

**Approach B: Via DB flag (poll-based, like force_push)**

Server writes a command record to DB with `pending` status. Agent picks it up on next check-in. Agent executes and reports result via a new RPC.

Trade-offs:
- Works for disconnected agents (checks DB on every check-in).
- Latency: up to one check-in interval (30s–5min). Not useful for interactive ad-hoc use.
- Simpler server-side (no real-time coordination).
- **Reject:** Too slow for ad-hoc interactive use. The whole point of ad-hoc execution is immediacy.

**Approach C: Hybrid (stream with DB fallback)**

Try stream first; if agent not connected, write to DB for pick-up on next check-in.

Trade-offs:
- Covers both connected and disconnected agents.
- More complex: two code paths for result delivery.
- For the v1.3 milestone, the fallback path may not be worth the complexity.
- **Recommendation:** Start with stream-only (Approach A). Add DB fallback in v1.4 if operators report disconnected-agent issues.

**Verdict:** Approach A. The AgentStream infrastructure makes this low-risk. Disconnected-agent fallback is a v1.4 concern.

---

### Capability 2: Task/Script Management

**The question:** What is a "task" vs a "command," and where do task scripts live?

**Approach A: Puppet Bolt model — scripts as files in a known directory**

Tasks are scripts (bash, python, etc.) stored in a `tasks/` directory on the server. Each task has an optional JSON metadata file describing parameters and types. Server pushes the script binary + params to the agent for execution. Agent executes and returns structured JSON output.

Trade-offs:
- Familiar to Puppet Bolt users.
- Scripts are version-controlled alongside other configs. GitOps-native.
- Parameters validated against schema before execution (type safety).
- Agent needs to execute arbitrary scripts from server (security surface).
- Task scripts live on the server and are pushed per-execution (bandwidth for large scripts).
- **Recommended approach.** Consistent with Hobnob's filesystem-as-source-of-truth philosophy.

**Approach B: Salt execution module model — tasks defined in code**

Tasks are Go functions registered on the agent, called remotely with arguments.

Trade-offs:
- Strongly typed; no code execution of arbitrary scripts.
- Requires agent recompile to add new tasks.
- Not configurable by operators without recompilation.
- **Reject for Hobnob.** Agents should be updatable independently of task definitions. Operators need to add tasks without releasing new agent binaries.

**Approach C: Inline scripts in workflow YAML**

Scripts are embedded directly in workflow YAML as multi-line strings.

Trade-offs:
- Simple for small scripts.
- No reuse — same script must be duplicated in every workflow that uses it.
- YAML escaping of shell scripts is painful.
- **Reject.** Encourages copy-paste, makes reuse impossible.

**Verdict:** Approach A. Scripts in `tasks/` directory with JSON metadata. Task execution = server reads script + params → validates params → pushes to agent via stream → agent executes → returns JSON result. The script is hashed for integrity, cached on the agent between executions if unchanged.

---

### Capability 3: Workflow/Plan Definitions

**The question:** How are multi-step workflows defined and evaluated?

**Approach A: YAML workflow files (Ansible Tower workflow model)**

Workflows are YAML files in a `workflows/` directory. Each workflow declares:
- A list of steps
- Each step: `name`, `command` or `task`, `targets`, `on_success`, `on_failure`, `when` condition
- Optional: `max_failures` (abort threshold)

Evaluation is server-side: the workflow engine reads the YAML, processes steps in order (or parallel for independent steps), waits for per-step results, evaluates `on_success`/`on_failure` edges, and aborts if failure threshold exceeded.

Trade-offs:
- Consistent with Hobnob's YAML-only philosophy.
- GitOps-native — workflows live in version control alongside node configs.
- Limited expressiveness — no loops, no dynamic target computation in the workflow definition itself.
- Targets must be specified statically in the workflow file or via CLI override at run time.
- **Recommended for the majority of use cases.**

**Approach B: Puppet Bolt plans (Puppet DSL or YAML sub-language)**

A mini-language (Puppet's DSL, or a subset for YAML plans) with variables, conditionals, loops, and function calls.

Trade-offs:
- Highly expressive — can compute targets dynamically, loop over results, etc.
- Significant complexity to implement a language evaluator server-side.
- Requires sysadmins to learn a new mini-language, defeating Hobnob's "sysadmins read YAML immediately" principle.
- **Reject.** The design principle is "every concept explainable in one sentence." A workflow language is not.

**Approach C: Declarative DAG workflow (step dependencies declared explicitly)**

Like Ansible Tower's workflow DAGs: each step declares its predecessors. Server builds a DAG, executes parallel branches concurrently, waits for dependencies to complete.

Trade-offs:
- Enables parallel step execution (valuable for large, independent setup steps).
- More complex to specify in YAML (user must think about DAG topology explicitly).
- Harder to reason about than sequential YAML.
- **Partial recommendation:** For v1.3, support sequential steps with `on_success`/`on_failure` branches. Add parallel DAG execution in v1.4 when the use cases are clearer.

**Verdict:** Approach A (sequential YAML) for v1.3. The workflow engine is a server-side state machine: process step → wait for all targets to report → evaluate edges → proceed or abort. No language evaluator. No loop constructs. Workflow files live in `workflows/` alongside other configs and hot-reload via fsnotify.

---

### Capability 4: Rolling Execution Strategies

**The question:** How does the server control execution batching and failure thresholds?

**Approach A: Server-side batch scheduler with health-gate**

Server divides the target set into batches (by count or percentage). After each batch completes, server optionally runs a health-check command against the just-completed thralls. If the health check fails on too many thralls, the job aborts.

Parameters:
- `batch_size`: integer (N thralls) or percentage (N%)
- `max_failures`: integer or percentage — abort threshold within a batch
- `health_check`: optional command to run after each batch (must return exit 0)
- `batch_delay`: optional pause between batches (default 0)

Trade-offs:
- Predictable, simple for operators to reason about.
- Does not reduce risk at the first thrall — still possible to break the entire fleet if batch_size is 100% and all fail simultaneously.
- Health check as a command is flexible but requires the operator to write a meaningful check.
- **Recommended approach.** Consistent with how Ansible Tower, Kubernetes rolling updates, and AWS Elastic Beanstalk all implement rolling.

**Approach B: Canary-first rolling (1 → N% → all)**

First batch is always exactly 1 thrall (the canary). If it succeeds, proceed with larger batches.

Trade-offs:
- Better risk profile — catches failures before they hit the full fleet.
- The 1-thrall canary is only valid if thralls are homogeneous. Heterogeneous fleets may have the canary succeed on a benign thrall while failing elsewhere.
- Can be achieved with Approach A by setting `batch_size: 1` then running again with a larger batch.
- **Do not implement as a distinct mode.** Operators can achieve this with Approach A parameters.

**Approach C: Circuit-breaker abort (abort job, not just batch)**

If `max_failures` is exceeded in any single batch, the entire job is aborted and no further batches proceed.

Trade-offs:
- Prevents cascading failure across the fleet.
- Reverted changes on completed batches are not automatically rolled back — operator must take manual remediation steps (or a new workflow).
- **This is a component of Approach A, not a separate approach.** The `max_failures` threshold and job abort are always part of rolling execution.

**Verdict:** Approach A with circuit-breaker abort built in. Parameters: `batch_size` (default 10% of targets), `max_failures` (default 0 — any failure aborts), `health_check` (optional), `batch_delay` (default 0s). Server tracks batch progress in DB; job record shows current batch, completed, failed counts.

---

### Capability 5: Live Query

**The question:** How does an operator get current state from thralls without waiting for their next check-in?

**Approach A: Query request via AgentStream + cached fallback**

Server sends a `QueryRequest` message over the AgentStream to connected agents. Agent handles it immediately (outside the check-in loop), queries the requested trait paths, and sends back a `QueryResponse`. Server aggregates responses with a deadline. For disconnected agents, fall back to last-known cached trait data from `thrall_traits` table.

Trade-offs:
- Connected agents respond in milliseconds.
- Disconnected agents get stale cached data (labeled as such in response).
- Adds a new agent-side handler branch to the stream loop.
- Query scope is limited to traits the agent knows how to collect (25 collectors). Cannot query arbitrary system state beyond trait paths without an ad-hoc command.
- **Recommended for trait queries.** The stream is the right channel; the fallback makes it graceful for offline thralls.

**Approach B: Trigger an out-of-cycle trait re-collection**

Server sends a `RefreshTraits` command over the stream. Agent immediately runs all (or specified) collectors and reports via the existing `ReportTraits` RPC.

Trade-offs:
- Reuses existing ReportTraits infrastructure — no new message types.
- Runs ALL collectors, which may be expensive (some collectors scan all packages — can take seconds).
- Less targeted than querying specific trait paths.
- Result arrives via existing ReportTraits path (stored in DB, not directly returned to the caller).
- **Useful for selective refresh.** Complementary to Approach A: use Approach A for cheap targeted queries, this approach for "refresh all traits now."

**Approach C: Dedicated query RPC (new unary gRPC call)**

Add a `LiveQuery` RPC to HobnobAdmin. Server uses the AgentStream to forward the query and await response, bridging the gRPC unary call with the stream mechanism.

Trade-offs:
- Cleaner interface for callers (standard unary RPC with timeout).
- Hides the stream complexity from API consumers.
- Implementation complexity: server must correlate the outbound stream message with the incoming response, matching by job/correlation ID.
- **Good longer-term design.** For v1.3, implement the stream messages first, wrap in a unary RPC in a subsequent iteration.

**Verdict:** Approach A for trait queries (new QueryRequest/QueryResponse stream messages). Approach B (RefreshTraits) as a complementary "force full refresh" operation. Approach C as the future API shape. For v1.3, expose live query via REST endpoint that uses Approach A internally with a configurable timeout (default 10s) and returns per-thrall results with a `fresh: true/false` indicator.

---

## Feature Dependencies

```
[AgentStream (existing bidirectional stream)]
    |
    +--enables--> [Ad-hoc Command Execution]
    |                  |
    |                  +--requires--> [Job Record (new DB table)]
    |                  +--requires--> [Command Result type (stdout/stderr/exit_code)]
    |                  +--enables--> [Task Execution]
    |                                     |
    |                                     +--requires--> [Task Registry (tasks/ directory)]
    |                                     +--requires--> [Parameter Schema Validation]
    |                                     +--enables--> [Workflow Steps]
    |
    +--enables--> [Live Query]
    |                  |
    |                  +--requires--> [QueryRequest/QueryResponse stream messages]
    |                  +--requires--> [Per-query correlation ID]
    |
    +--enables--> [Rolling Execution]
                       |
                       +--requires--> [Job Record + Batch tracking]
                       +--requires--> [Trait-based targeting]
                       +--requires--> [Ad-hoc Command Execution (for health checks)]

[Trait-based Targeting]
    |
    +--requires--> [Trait data in DB (existing thrall_traits table)]
    +--requires--> [Trait query filter (new query layer)]

[Workflow Engine]
    |
    +--requires--> [Task Execution]
    +--requires--> [Ad-hoc Command Execution]
    +--requires--> [Rolling Execution]
    +--requires--> [Workflow YAML files (workflows/ directory)]
    +--requires--> [Server-side workflow state machine]

[CLI subcommands (hobnobcli run/query/workflow)]
    |
    +--requires--> [Job REST endpoints]
    +--requires--> [Result retrieval endpoints]
```

### Dependency Notes

- **Ad-hoc command requires AgentStream:** Commands are delivered over the existing bidirectional stream. This is already proven by force-push. No new transport needed.
- **Tasks require ad-hoc command infrastructure:** A task is a command with a script payload and parameter validation. The execution path is the same; tasks add a script delivery step before the command.
- **Workflows require tasks and commands:** A workflow step is either a task or a command. The workflow engine orchestrates them; it does not execute directly.
- **Rolling execution requires job record and targeting:** Rolling needs to know the full target set upfront to compute batches. Targeting produces the initial set; the job record tracks batch progress.
- **Live query is independent of command/task execution:** Query uses the stream but does not create a "run" in the traditional sense. It is a read operation, not a write/execute operation. No run record needed for pure trait queries.
- **Workflow conflicts with config management runs:** A workflow run and a config management convergence run can occur simultaneously on the same thrall. The agent must handle both — the stream carries both types of messages. The agent should process them independently (workflow commands do not affect config management state).

---

## MVP Definition

### Launch With (v1.3)

Minimum set of orchestration features to validate the concept and address the primary operator use cases.

- [ ] Ad-hoc command execution via AgentStream — the simplest orchestration primitive; proves the execution path
- [ ] Multi-thrall targeting by hostname glob and single trait filter — covers 90% of targeting needs
- [ ] Result collection per thrall (stdout, stderr, exit code) — operators need to see what happened
- [ ] Job record with per-thrall status tracking — visibility into running vs completed vs failed
- [ ] Timeout enforcement (agent-side deadline + server-side marking) — prevents hung jobs
- [ ] Live query for trait paths via AgentStream with cached fallback — "what kernel is running on these boxes right now?"
- [ ] Rolling execution with batch_size and max_failures parameters — core safety control
- [ ] `hobnobcli run command` subcommand — CLI is the primary interface
- [ ] `hobnobcli run query` subcommand — for live fact queries
- [ ] Job history queryable via CLI and REST — `hobnobcli runs list`, `hobnobcli runs get <id>`

### Add After Validation (v1.4)

Features to add once core execution and targeting are working reliably.

- [ ] Task definitions (scripts in `tasks/` with JSON metadata + parameter validation) — validated by demand: if operators ask "can I run the same script repeatedly with different params?", add tasks
- [ ] Sequential workflows with on_success/on_failure branching — validated by demand: if operators ask "can I run A then B if A succeeded?"
- [ ] Health-check commands in rolling execution — extends basic rolling with automated gate
- [ ] Compound trait targeting (AND/OR of multiple trait conditions) — for fleets where single-trait targeting is insufficient
- [ ] Disconnected-agent DB fallback for ad-hoc commands (poll-on-check-in) — if disconnected-agent complaints are common

### Future Consideration (v2+)

Features to defer until the orchestration capability is established and adoption is measured.

- [ ] Parallel DAG workflow steps (concurrent independent branches) — adds complexity; sequential is sufficient for most cases
- [ ] Webhook events for job completion/failure — consistent with existing webhook infrastructure, adds value for CI/CD integration
- [ ] Scheduled job execution (cron-like) — "run this task every Sunday at 2am against all web servers"
- [ ] Workflow approval gates (require human approval before proceeding to next step) — enterprise governance requirement
- [ ] Task result chaining (pass output of task A as input to task B) — requires structured task output; deferred until task adoption is proven
- [ ] SSE streaming of job progress to web UI — nice UX; deferred until web UI is updated for orchestration

---

## Feature Prioritization Matrix

| Feature | User Value | Implementation Cost | Priority |
|---------|------------|---------------------|----------|
| Ad-hoc command via stream | HIGH | MEDIUM | P1 |
| Multi-thrall glob targeting | HIGH | LOW | P1 |
| Result collection per thrall | HIGH | LOW | P1 |
| Job record + per-thrall status | HIGH | MEDIUM | P1 |
| Timeout enforcement | HIGH | LOW | P1 |
| Rolling execution (batch + threshold) | HIGH | MEDIUM | P1 |
| `hobnobcli run command` subcommand | HIGH | LOW | P1 |
| Live query via stream | MEDIUM | MEDIUM | P1 |
| `hobnobcli run query` subcommand | MEDIUM | LOW | P1 |
| Job history (list + get) | MEDIUM | LOW | P1 |
| Task definitions with parameter schema | HIGH | HIGH | P2 |
| Sequential workflow engine | HIGH | HIGH | P2 |
| Health-check commands in rolling | MEDIUM | LOW | P2 |
| Compound trait targeting | MEDIUM | MEDIUM | P2 |
| Disconnected-agent DB fallback | MEDIUM | MEDIUM | P2 |
| Parallel DAG workflow steps | LOW | HIGH | P3 |
| Webhook events for job completion | MEDIUM | LOW | P3 |
| Scheduled job execution | MEDIUM | HIGH | P3 |
| Workflow approval gates | LOW | HIGH | P3 |
| Task result chaining | MEDIUM | HIGH | P3 |

**Priority key:**
- P1: Must have for v1.3 launch — the orchestration milestone is incomplete without these
- P2: Should have, add in v1.4 — validates and extends the core
- P3: Future consideration — deferred until adoption warrants the complexity

---

## Competitor Feature Analysis

| Feature | Ansible | Salt (SaltStack) | Puppet Bolt | Hobnob v1.3 Approach |
|---------|---------|------------------|-------------|----------------------|
| Ad-hoc execution | `ansible -m shell -a "cmd" hosts` (SSH, agentless) | `salt 'target' cmd.run "cmd"` (ZeroMQ, persistent) | `bolt command run "cmd" --targets` (SSH/WinRM) | Command over AgentStream (gRPC mTLS, persistent) |
| Transport for ad-hoc | SSH (per-execution connection) | ZeroMQ persistent bus | SSH (per-execution connection) | Existing AgentStream (already open) |
| Targeting model | Inventory groups + patterns | Compound targeting with grains (facts), regex, CIDR | Inventory files + target groups | Hostname glob + trait query against live DB data |
| Result format | Per-host dict with stdout/stderr/rc | Per-minion dict with return value | Per-target result object with JSON | Per-thrall record: stdout, stderr, exit_code, duration |
| Task model | Ansible modules (Python, Go, etc.) | Execution modules (Python) | Bolt tasks (any script + JSON metadata) | Task scripts in `tasks/` with JSON parameter schema |
| Workflow model | Playbooks (YAML, sequential with conditionals) | Orchestrate runner (SLS + requisites) | Bolt plans (Puppet DSL or YAML) | Sequential YAML workflows in `workflows/` directory |
| Rolling execution | `serial:` keyword in playbooks (% or count) | Not first-class; manual via orchestration SLS | Manual loop in Bolt plan | First-class `batch_size` + `max_failures` params |
| Live fact query | `ansible -m setup` (re-runs fact gathering via SSH) | `salt 'target' grains.item key` (returns from cache or live) | `bolt facts --targets` (SSH connection per-run) | QueryRequest over AgentStream (sub-second for connected) |
| Result persistence | Not persisted by default; callback plugins required | Job cache (expires by default) | Not persisted | Job records in SQLite; queryable indefinitely |
| Connected agent required? | No (SSH on-demand) | Yes (ZeroMQ must be running) | No (SSH on-demand) | Yes for live delivery; DB fallback for disconnected |

### Key Competitive Observations

1. **Transport advantage:** Ansible and Puppet Bolt establish SSH connections per-run, which adds 100-500ms overhead and requires SSH credentials. Salt and Hobnob both use persistent connections, but Hobnob's mTLS gRPC stream is more secure and already authenticated. Sub-second command delivery is a genuine advantage.

2. **Targeting advantage:** Salt's grains are cached at minion startup by default — they can be stale. Ansible's dynamic inventory requires external scripts or plugins. Hobnob's trait data is updated every check-in interval (typically every few minutes) and is the foundation of the existing conditional system. Targeting based on this data is already proven infrastructure.

3. **Rolling execution advantage:** In open-source Ansible, `serial:` controls batch size but there is no built-in health-check gate between batches. Puppet Bolt's rolling is manual (the operator writes a loop with health checks in the plan). Hobnob making this first-class with configurable parameters and automatic health-gate abort is a genuine differentiator among open-source tools.

4. **Persistence advantage:** Ansible's output goes to stdout (requires external logging). Salt's job cache is temporary. Bolt doesn't persist. Storing all execution results in SQLite (already Hobnob's pattern for config management runs) means full history is available without external log infrastructure.

5. **Complexity gap:** Ansible playbooks and Salt orchestration SLS files are full YAML-with-Jinja2 programs. Bolt plans require Puppet DSL knowledge. Hobnob's sequential YAML workflows with `when:` conditions from the existing conditional infrastructure keep the "sysadmin reads it immediately" principle intact for orchestration.

---

## Sources

- [Ansible ad hoc commands documentation](https://docs.ansible.com/projects/ansible/latest/command_guide/intro_adhoc.html) — table stakes reference (HIGH confidence)
- [Ansible return values documentation](https://docs.ansible.com/ansible/latest/reference_appendices/common_return_values.html) — result format reference (HIGH confidence)
- [SaltStack Orchestrate Runner](https://docs.saltproject.io/en/latest/topics/orchestrate/orchestrate_runner.html) — workflow model comparison (HIGH confidence)
- [SaltStack Grains targeting](https://docs.saltproject.io/en/latest/topics/targeting/grains.html) — targeting model comparison (HIGH confidence)
- [Puppet Bolt tasks documentation](https://www.puppet.com/docs/bolt/latest/writing_tasks.html) — task model reference (HIGH confidence)
- [Puppet Bolt plans documentation](https://www.puppet.com/docs/bolt/latest/writing_yaml_plans.html) — workflow/plan model (HIGH confidence)
- [Puppet Bolt orchestrating workflows](https://www.puppet.com/docs/bolt/latest/plans.html) — multi-step workflow patterns (HIGH confidence)
- [AWS Elastic Beanstalk rolling deployment strategies](https://docs.aws.amazon.com/elasticbeanstalk/latest/dg/using-features.rolling-version-deploy.html) — rolling execution patterns (HIGH confidence)
- [Rolling deployments strategies and patterns (systemdr)](https://systemdr.substack.com/p/issue-125-rolling-deployments-strategies) — rolling strategies analysis (MEDIUM confidence)
- Hobnob codebase — existing AgentStream, run record, trait storage infrastructure (HIGH confidence)

---

*Feature research for: Orchestration capabilities (ad-hoc commands, tasks, workflows, rolling execution, live query)*
*Researched: 2026-03-11*
