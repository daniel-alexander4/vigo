# Requirements: Hobnob

**Defined:** 2026-03-11
**Core Value:** Managed nodes converge to their declared desired state reliably, idempotently, and securely — with full audit visibility into what changed, when, and why.

## v1.3 Requirements

Requirements for v1.3 Orchestration. Each maps to roadmap phases 11-18.

### Protocol & Infrastructure

- [ ] **INF-01**: AgentStream proto extended with task dispatch, task result, query request, query response, and task ack message types in the existing oneof
- [ ] **INF-02**: SQLite schema extended with task_runs, task_run_targets, and workflow_runs tables separate from convergence run tables
- [ ] **INF-03**: Agent enforces mutual exclusion between convergence runs and task execution — never simultaneous
- [ ] **INF-04**: Task dispatch uses blocking send with timeout and agent acknowledgement — dropped tasks are explicit errors, not silent
- [ ] **INF-05**: Every task subprocess is context-bound to the stream lifecycle — SIGTERM then SIGKILL on stream close, no orphaned processes

### Ad-Hoc Commands

- [ ] **CMD-01**: Operator can run a shell command on one or more targeted thralls and receive stdout, stderr, and exit code per thrall
- [ ] **CMD-02**: Command execution enforces a configurable timeout (default 60s) — agent kills the process and reports timeout status
- [ ] **CMD-03**: Every command dispatch writes an immutable audit record (who, what, when, targets) before execution begins
- [ ] **CMD-04**: Command output is truncated at 1MB per thrall with a visible truncation indicator in the result

### Targeting

- [ ] **TGT-01**: Operator can target thralls by hostname glob pattern (e.g., `*.web.prod`)
- [ ] **TGT-02**: Operator can target thralls by a single trait filter (e.g., `os.distro=ubuntu`)
- [ ] **TGT-03**: Operator can target a single thrall by hostname or UUID
- [ ] **TGT-04**: Offline thralls are reported as `offline` status in results — not silently omitted and not counted as failures

### Tasks

- [ ] **TSK-01**: Operator can define reusable task scripts in a `tasks/` directory with a name, description, and parameter schema
- [ ] **TSK-02**: Task parameters are validated against the schema before dispatch — invalid parameters are rejected with clear error messages
- [ ] **TSK-03**: Task scripts are delivered to the agent at execution time and cleaned up after completion
- [ ] **TSK-04**: Operator can run a named task with parameters via CLI and receive structured per-thrall results

### Rolling Execution

- [ ] **ROL-01**: Operator can specify batch_size (count or percentage) to control how many thralls execute simultaneously
- [ ] **ROL-02**: Operator can specify max_failures (count or percentage) — execution aborts remaining batches when threshold is breached
- [ ] **ROL-03**: Operator can specify a health_check command that runs between batches — next batch proceeds only if health check passes
- [ ] **ROL-04**: Operator can preview the batch plan (dry-run) before executing — shows which thralls are in each batch
- [ ] **ROL-05**: Offline thralls are excluded from batch planning and reported separately — they do not count toward max_failures

### Live Query

- [ ] **QRY-01**: Operator can query a trait path across targeted thralls and receive current values from connected thralls
- [ ] **QRY-02**: Disconnected thralls fall back to cached trait values from the database with a staleness indicator
- [ ] **QRY-03**: Every query response includes explicit responded, timed_out, and offline counts — no silent omissions
- [ ] **QRY-04**: Query has a configurable timeout (default 10s) after which non-responsive thralls are marked timed_out

### Workflows

- [ ] **WFL-01**: Operator can define multi-step workflows as YAML files with sequential steps, each targeting different thrall groups
- [ ] **WFL-02**: Workflow steps support on_success and on_failure branching to control which step executes next
- [ ] **WFL-03**: Workflow state is persisted to SQLite before each step dispatch — server restart marks incomplete workflows as failed (not silently lost)
- [ ] **WFL-04**: Operator can run, monitor status, and abort a running workflow via CLI
- [ ] **WFL-05**: Workflow steps can use rolling execution parameters (batch_size, max_failures, health_check)

### CLI & API

- [ ] **INT-01**: `hobnobcli task run <command> --target <glob>` dispatches ad-hoc commands with real-time result display
- [ ] **INT-02**: `hobnobcli task status <id>` and `hobnobcli task list` show job history with per-thrall status
- [ ] **INT-03**: `hobnobcli query <trait-path> --target <glob>` shows live trait values with responded/offline counts
- [ ] **INT-04**: `hobnobcli workflow run <name>` and `hobnobcli workflow status <id>` manage workflow execution
- [ ] **INT-05**: REST API endpoints for all orchestration operations with SSE streaming for live result updates
- [ ] **INT-06**: Web UI pages for task history, workflow status, live query, and rolling execution progress

## v1.2 Requirements (Shipped)

<details>
<summary>v1.2 Structured Conditionals — all 15 requirements complete</summary>

### Conditional Vars

- [x] **COND-01**: Admin can define conditional vars with list matching (`only`, `not`)
- [x] **COND-02**: Admin can define conditional vars with numeric comparisons (`gt`, `lt`, `gte`, `lte`)
- [x] **COND-03**: Admin can define conditional vars with regex matching (`match`)
- [x] **COND-04**: Conditional vars evaluate to true/false and can be referenced by name in all four conditional fields

### Resource Conditionals

- [x] **COND-05**: `go_if_all_pass:` — resource runs when ALL listed conditions are true (string or list)
- [x] **COND-06**: `go_if_any_pass:` — resource runs when at least one listed condition is true (list)
- [x] **COND-07**: `skip_if_all_pass:` — resource skipped when ALL listed conditions are true (string or list)
- [x] **COND-08**: `skip_if_any_pass:` — resource skipped when ANY listed condition is true (list)
- [x] **COND-09**: All four fields combinable on a single resource (all constraints must hold)
- [x] **COND-10**: Existing `when:` field is removed and replaced by the new conditional fields

### Conditional Blocks

- [x] **COND-11**: `conditional_block:` wraps resources with shared conditional fields
- [x] **COND-12**: Resources inside blocks can have their own conditionals (both block and resource must pass)
- [x] **COND-13**: `depends_on` works across `conditional_block:` boundaries

### Module Conditionals

- [x] **COND-14**: Conditional fields on module references in roles YAML to skip entire modules
- [x] **COND-15**: Conditional fields on module references in clients.yaml to skip entire modules

</details>

## v1.4 Requirements (Deferred)

### Advanced Targeting
- **TGT-05**: Compound trait targeting with AND/OR of multiple conditions

### Advanced Workflows
- **WFL-06**: Workflow resume after server restart (re-execute from failed step)
- **WFL-07**: Parallel DAG workflow steps (concurrent independent steps)
- **WFL-08**: Workflow approval gates for human-in-the-loop governance
- **WFL-09**: Task result chaining (output of step A feeds step B as input)

### Advanced Operations
- **CMD-05**: Disconnected-agent DB fallback for ad-hoc commands (execute on next check-in)
- **INT-07**: SSE streaming of live stdout during command execution (line-by-line)
- **INT-08**: Webhook events for job completion and failure

## Out of Scope

| Feature | Reason |
|---------|--------|
| Named operations / allowlist | Raw shell + audit log chosen for v1.3 flexibility; RBAC is a separate concern |
| Scheduled / recurring tasks | Cron-like scheduling is a different feature — thralls already have cron executor |
| Push-based execution without streaming | Contradicts pull architecture; streaming is the delivery mechanism |
| Custom workflow DSL | YAML sequential steps are sufficient; DSL adds operator cognitive load |
| GUI workflow builder | Drag-and-drop workflow creation is overengineering; YAML files are the config path |
| Agent-to-agent task delegation | Agents only talk to the server — non-negotiable constraint |
| Freeform Go template `when:` expressions | Replaced by structured conditional fields in v1.2 |

## Traceability

| Requirement | Phase | Status |
|-------------|-------|--------|
| INF-01 | Phase 11 | Pending |
| INF-02 | Phase 11 | Pending |
| INF-03 | Phase 12 | Pending |
| INF-04 | Phase 12 | Pending |
| INF-05 | Phase 12 | Pending |
| CMD-01 | Phase 12 | Pending |
| CMD-02 | Phase 12 | Pending |
| CMD-03 | Phase 14 | Pending |
| CMD-04 | Phase 14 | Pending |
| TGT-01 | Phase 13 | Pending |
| TGT-02 | Phase 13 | Pending |
| TGT-03 | Phase 13 | Pending |
| TGT-04 | Phase 13 | Pending |
| TSK-01 | Phase 17 | Pending |
| TSK-02 | Phase 17 | Pending |
| TSK-03 | Phase 17 | Pending |
| TSK-04 | Phase 17 | Pending |
| ROL-01 | Phase 16 | Pending |
| ROL-02 | Phase 16 | Pending |
| ROL-03 | Phase 16 | Pending |
| ROL-04 | Phase 16 | Pending |
| ROL-05 | Phase 16 | Pending |
| QRY-01 | Phase 15 | Pending |
| QRY-02 | Phase 15 | Pending |
| QRY-03 | Phase 15 | Pending |
| QRY-04 | Phase 15 | Pending |
| WFL-01 | Phase 17 | Pending |
| WFL-02 | Phase 17 | Pending |
| WFL-03 | Phase 17 | Pending |
| WFL-04 | Phase 17 | Pending |
| WFL-05 | Phase 17 | Pending |
| INT-01 | Phase 14 | Pending |
| INT-02 | Phase 14 | Pending |
| INT-03 | Phase 15 | Pending |
| INT-04 | Phase 17 | Pending |
| INT-05 | Phase 14 | Pending |
| INT-06 | Phase 18 | Pending |
| COND-01 | Phase 7 | Complete |
| COND-02 | Phase 7 | Complete |
| COND-03 | Phase 7 | Complete |
| COND-04 | Phase 7 | Complete |
| COND-05 | Phase 8 | Complete |
| COND-06 | Phase 8 | Complete |
| COND-07 | Phase 8 | Complete |
| COND-08 | Phase 8 | Complete |
| COND-09 | Phase 8 | Complete |
| COND-10 | Phase 8 | Complete |
| COND-11 | Phase 9 | Complete |
| COND-12 | Phase 9 | Complete |
| COND-13 | Phase 9 | Complete |
| COND-14 | Phase 10 | Complete |
| COND-15 | Phase 10 | Complete |

**Coverage:**
- v1.3 requirements: 36 total
- Mapped to phases: 36
- Unmapped: 0

---
*Requirements defined: 2026-03-11*
*Last updated: 2026-03-11 after initial definition*
