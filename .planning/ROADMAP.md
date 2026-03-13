# Roadmap: Isogov

## Milestones

- [x] **v1.0 MVP** - Phases 1-4 (shipped 2026-03-06)
- [x] **v1.1 Traits Visibility & Custom Traits** - Phases 5-6 (shipped 2026-03-07)
- [x] **v1.2 Structured Conditionals** - Phases 7-10 (shipped 2026-03-08)
- [ ] **v1.3 Orchestration** - Phases 11-18 (in progress)

## Phases

<details>
<summary>v1.0 MVP (Phases 1-4) - SHIPPED 2026-03-06</summary>

- [x] Phase 1: Foundation (5/5 plans) — completed 2026-03-06
- [x] Phase 2: Convergence (5/5 plans) — completed 2026-03-06
- [x] Phase 3: Operations (5/5 plans) — completed 2026-03-06
- [x] Phase 4: Web UI & Deployment (4/4 plans) — completed 2026-03-06

</details>

<details>
<summary>v1.1 Traits Visibility & Custom Traits (Phases 5-6) - SHIPPED 2026-03-07</summary>

- [x] Phase 5: Trait Inspection (2/2 plans) — completed 2026-03-07
- [x] Phase 6: Custom Traits (2/2 plans) — completed 2026-03-07

</details>

<details>
<summary>v1.2 Structured Conditionals (Phases 7-10) - SHIPPED 2026-03-08</summary>

- [x] Phase 7: Conditional Vars (2/2 plans) — completed 2026-03-07
- [x] Phase 8: Resource Conditionals (2/2 plans) — completed 2026-03-07
- [x] Phase 9: Conditional Blocks (2/2 plans) — completed 2026-03-07
- [x] Phase 10: Module Conditionals (1/1 plans) — completed 2026-03-08

</details>

### v1.3 Orchestration (In Progress)

**Milestone Goal:** Enable operators to run ad-hoc commands, one-off tasks, multi-step workflows, and live queries against managed thralls via the existing bidirectional gRPC streaming infrastructure.

- [ ] **Phase 11: Proto and Schema Foundation** - Extend AgentStream proto with orchestration message types and add task/workflow SQLite tables
- [ ] **Phase 12: Agent Task Executor and Stream Plumbing** - Implement agent-side task execution with mutual exclusion, timeouts, and context-bound subprocess management
- [ ] **Phase 13: Server StreamManager and Result Routing** - Add server-side task dispatch with backpressure semantics and result routing infrastructure
- [ ] **Phase 14: REST API and CLI for Ad-Hoc Tasks** - Expose task dispatch and targeting through REST endpoints and hobnobcli subcommands
- [ ] **Phase 15: Live Query** - Enable operators to query trait paths across thralls with explicit responded/timed_out/offline accounting
- [ ] **Phase 16: Rolling Execution** - Add batch-controlled execution with failure thresholds and health-check gates between batches
- [ ] **Phase 17: Tasks and Workflows** - Implement reusable task scripts with parameter validation and multi-step workflow engine
- [ ] **Phase 18: Web UI Integration** - Add orchestration pages to the web UI for task history, workflow status, live query, and rolling progress

## Phase Details

### Phase 11: Proto and Schema Foundation
**Goal**: The protocol contract and database schema for all orchestration capabilities exist before any behavior is built
**Depends on**: Phase 10
**Requirements**: INF-01, INF-02
**Success Criteria** (what must be TRUE):
  1. The proto oneof for AgentStream includes task dispatch, task result, query request, query response, and task ack message types, and `make proto` regenerates valid Go and Rust code without errors
  2. Running migrations produces task_runs, task_run_targets, workflow_runs, and workflow_step_results tables in SQLite, distinct from existing run tables
  3. sqlc generates typed query functions for all new tables and the generated code compiles
  4. All existing tests continue to pass — no behavior change in this phase
**Plans**: TBD

### Phase 12: Agent Task Executor and Stream Plumbing
**Goal**: An agent receives a task dispatch over its existing stream, executes the command, and returns stdout/stderr/exit-code — never simultaneously with a convergence run
**Depends on**: Phase 11
**Requirements**: INF-03, INF-04, INF-05, CMD-01, CMD-02
**Success Criteria** (what must be TRUE):
  1. Operator can dispatch a shell command to a connected thrall and receive stdout, stderr, and exit code in the result
  2. A task dispatch sent while convergence is running on the agent is queued and acknowledged only after convergence completes — the two never run simultaneously
  3. A task with a timeout set expires at the configured deadline: the agent kills the process and returns a timeout status, not a hang
  4. Closing the stream while a task subprocess is running causes SIGTERM followed by SIGKILL — no orphaned processes remain after stream disconnect
  5. A task dispatch that the agent cannot accept returns an explicit error to the server, not a silent drop
**Plans**: TBD

### Phase 13: Server StreamManager and Result Routing
**Goal**: The server can dispatch tasks to connected thralls with backpressure guarantees and route results back to waiting callers
**Depends on**: Phase 12
**Requirements**: TGT-01, TGT-02, TGT-03, TGT-04
**Success Criteria** (what must be TRUE):
  1. Operator can target thralls by hostname glob pattern and the server resolves matching connected thralls before dispatch
  2. Operator can target thralls by a single trait filter (e.g., os.distro=ubuntu) and only matching thralls receive the dispatch
  3. Operator can target a single thrall by hostname or UUID directly
  4. Thralls that are offline at dispatch time appear in the result with status `offline`, not omitted and not counted as failures
**Plans**: TBD

### Phase 14: REST API and CLI for Ad-Hoc Tasks
**Goal**: Operators can dispatch commands, monitor results, and view job history through hobnobcli and the REST API — with every dispatch logged before execution
**Depends on**: Phase 13
**Requirements**: CMD-03, CMD-04, INT-01, INT-02, INT-05
**Success Criteria** (what must be TRUE):
  1. `hobnobcli task run <command> --target <glob>` dispatches the command and displays per-thrall stdout/stderr/exit-code as results arrive
  2. `hobnobcli task status <id>` shows the current per-thrall status breakdown (pending/dispatched/complete/failed/timeout/offline) for any job
  3. `hobnobcli task list` shows job history with target summary and overall status
  4. Every command dispatch writes an immutable audit record (operator, command, targets, timestamp) before any execution begins — the record exists even if all thralls fail
  5. Output exceeding 1MB per thrall is truncated with a visible `[output truncated]` indicator in the result display
  6. REST endpoints exist for POST task dispatch, GET task status, GET task list, and SSE streaming of live results
**Plans**: TBD

### Phase 15: Live Query
**Goal**: Operators can retrieve current trait values from targeted thralls immediately, with explicit accounting for every thrall that did not respond
**Depends on**: Phase 13
**Requirements**: QRY-01, QRY-02, QRY-03, QRY-04, INT-03
**Success Criteria** (what must be TRUE):
  1. `hobnobcli query <trait-path> --target <glob>` returns current trait values from connected thralls within the configured timeout
  2. Thralls that are disconnected appear in query results with their last-known cached value from the database and a staleness indicator showing when it was collected
  3. Every query response includes explicit counts for responded, timed_out, and offline — the operator always knows how many thralls did not contribute live data
  4. A query with default timeout (10s) marks non-responsive connected thralls as timed_out after that deadline, never waiting indefinitely
**Plans**: TBD

### Phase 16: Rolling Execution
**Goal**: Operators can execute commands across a fleet in controlled batches with automatic abort when failures exceed a configurable threshold
**Depends on**: Phase 14
**Requirements**: ROL-01, ROL-02, ROL-03, ROL-04, ROL-05
**Success Criteria** (what must be TRUE):
  1. Operator can set batch_size as a count or percentage and the execution proceeds in sequential batches of that size, not all thralls simultaneously
  2. Operator can set max_failures as a count or percentage and execution stops before starting the next batch when that threshold is breached by the completed batch
  3. Operator can specify a health_check command that runs between batches — the next batch only starts if the health check exits 0
  4. `hobnobcli task run --dry-run --batch-size N` displays the batch plan (which thralls are in each batch) without executing anything
  5. Offline thralls are excluded from batch planning entirely and reported separately in results — they do not count toward max_failures
**Plans**: TBD

### Phase 17: Tasks and Workflows
**Goal**: Operators can define reusable task scripts with validated parameters and compose them into multi-step workflows with conditional branching
**Depends on**: Phase 16
**Requirements**: TSK-01, TSK-02, TSK-03, TSK-04, WFL-01, WFL-02, WFL-03, WFL-04, WFL-05, INT-04
**Success Criteria** (what must be TRUE):
  1. Operator can define a task script in `tasks/` with a name, description, and parameter schema, then run it with `hobnobcli task run <name> --param key=value` and receive structured per-thrall results
  2. A task invoked with parameters that do not match its schema is rejected before dispatch with a clear error message naming the invalid parameters
  3. Task scripts are delivered to the agent at execution time and cleaned up after completion — no script artifacts remain on the thrall
  4. Operator can define a multi-step workflow YAML in `workflows/` and run it with `hobnobcli workflow run <name>`, with `on_success`/`on_failure` branching controlling which step executes next
  5. `hobnobcli workflow status <id>` shows the current step, per-step result summary, and overall workflow status at any point during or after execution
  6. `hobnobcli workflow abort <id>` stops a running workflow before the next step dispatches — in-flight steps complete, pending steps do not start
  7. A server restart while a workflow is running marks it as `failed` with reason `restart_interrupted` — the workflow is never silently lost or resumed with unknown state
**Plans**: TBD

### Phase 18: Web UI Integration
**Goal**: The web UI surfaces orchestration activity — task history, workflow status, live query, and rolling execution progress — as readable pages backed by the existing REST API
**Depends on**: Phase 17
**Requirements**: INT-06
**Success Criteria** (what must be TRUE):
  1. The task history page lists recent jobs with target summary, status, and a drilldown to per-thrall stdout/stderr for any job
  2. The workflow status page shows current and historical workflow runs with step-level detail and per-thrall results for each step
  3. The live query page accepts a trait path and target glob, submits the query, and displays responded/timed_out/offline counts alongside the returned values
  4. The rolling execution progress view updates in real time, showing which batch is executing, completed batch outcomes, and whether execution was aborted
**Plans**: TBD

## Progress

**Execution Order:**
Phases execute in numeric order: 11 -> 12 -> 13 -> 14 -> 15 -> 16 -> 17 -> 18

| Phase | Milestone | Plans Complete | Status | Completed |
|-------|-----------|----------------|--------|-----------|
| 1. Foundation | v1.0 | 5/5 | Complete | 2026-03-06 |
| 2. Convergence | v1.0 | 5/5 | Complete | 2026-03-06 |
| 3. Operations | v1.0 | 5/5 | Complete | 2026-03-06 |
| 4. Web UI & Deployment | v1.0 | 4/4 | Complete | 2026-03-06 |
| 5. Trait Inspection | v1.1 | 2/2 | Complete | 2026-03-07 |
| 6. Custom Traits | v1.1 | 2/2 | Complete | 2026-03-07 |
| 7. Conditional Vars | v1.2 | 2/2 | Complete | 2026-03-07 |
| 8. Resource Conditionals | v1.2 | 2/2 | Complete | 2026-03-07 |
| 9. Conditional Blocks | v1.2 | 2/2 | Complete | 2026-03-07 |
| 10. Module Conditionals | v1.2 | 1/1 | Complete | 2026-03-08 |
| 11. Proto and Schema Foundation | v1.3 | 0/? | Not started | - |
| 12. Agent Task Executor and Stream Plumbing | v1.3 | 0/? | Not started | - |
| 13. Server StreamManager and Result Routing | v1.3 | 0/? | Not started | - |
| 14. REST API and CLI for Ad-Hoc Tasks | v1.3 | 0/? | Not started | - |
| 15. Live Query | v1.3 | 0/? | Not started | - |
| 16. Rolling Execution | v1.3 | 0/? | Not started | - |
| 17. Tasks and Workflows | v1.3 | 0/? | Not started | - |
| 18. Web UI Integration | v1.3 | 0/? | Not started | - |
