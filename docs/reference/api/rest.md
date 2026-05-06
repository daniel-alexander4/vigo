
# REST API

In addition to the grpc-gateway proxy (which exposes all `VigoAdmin` RPCs under `/api/v1/`), the server mounts several direct REST handlers. These endpoints are not proxied through grpc-gateway and exist for features that benefit from direct HTTP handling.

All endpoints return JSON and accept JSON request bodies. Errors use the format `{"message": "..."}`.

## Authorization

Every endpoint requires both authentication (session cookie or `vgot_*` API token) and a specific permission. Read endpoints (status, list, get) require `fleet.read`; mutation endpoints require a permission matched to the action — `config.publish` for publish/freeze/reload/retract, `config.rollback` for rollback, `push.execute` for task dispatch, `workflow.execute` for workflow run/abort, `query.execute` for live queries and the AI assistant, `fleet.write` for envoy maintenance and force-update, `audit.read` for the raw audit log. Database administration, peer promotion, secret-rotation callbacks, and emergency-access recording require the admin role. The compliance framework reports + audit-bundle download ride `compliance.read`; supporting-doc upload requires `compliance.docs.write`. See the [Authentication doc](../../operations/authentication.md#roles-and-permissions) for the full role/permission matrix.

## Tasks

Ad-hoc command dispatch to connected agents.

### POST /api/v1/task

**Permission:** `push.execute`

Dispatch a command to matching envoys.

**Request body:**

```json
{
  "command": "uptime",
  "target": "*.web.*",
  "operator": "admin",
  "timeout": 30,
  "nice": 0,
  "batchSize": "25%",
  "maxFailures": "3",
  "healthCheck": "curl -sf http://localhost/health",
  "dryRun": false,
  "taskName": "",
  "params": {}
}
```

| Field | Required | Description |
|-------|----------|-------------|
| `command` | Yes (or `taskName`) | Shell command to execute |
| `target` | Yes (or `taskName`) | Hostname glob or trait filter (e.g., `*.web.*`, `os.family=debian`) |
| `operator` | No | Who initiated the task (default: `api`) |
| `timeout` | No | Max seconds per target (default: 60) |
| `nice` | No | CPU/IO priority 0–19 (0 = normal, 19 = lowest; default: 0) |
| `batchSize` | No | Rolling execution batch size (e.g., `25%`, `5`) |
| `maxFailures` | No | Abort after this many failures (e.g., `3`, `10%`) |
| `healthCheck` | No | Command to run between batches |
| `dryRun` | No | If true, return matched targets without executing |
| `taskName` | No | Name of a reusable task definition from `.live/tasks/*.vgo` |
| `params` | No | Parameter overrides for named tasks |

**Response (201 Created):**

```json
{
  "id": "abc123",
  "status": "running",
  "command": "uptime",
  "targetGlob": "*.web.*",
  "totalTargets": 12,
  "targets": [...]
}
```


### GET /api/v1/task

**Permission:** `fleet.read`

List recent task runs.

**Query parameters:**

| Parameter | Default | Description |
|-----------|---------|-------------|
| `limit` | 20 | Max results to return |

**Response:**

```json
{
  "tasks": [
    {
      "id": "abc123",
      "command": "uptime",
      "target": "*.web.*",
      "operator": "admin",
      "status": "complete",
      "totalTargets": 12,
      "createdAt": "2026-03-13T10:00:00Z",
      "finishedAt": "2026-03-13T10:00:05Z"
    }
  ]
}
```

### GET /api/v1/task/{id}

**Permission:** `fleet.read`

Get a task run with per-target results.

**Response:**

```json
{
  "task": {
    "id": "abc123",
    "command": "uptime",
    "target": "*.web.*",
    "operator": "admin",
    "status": "complete",
    "totalTargets": 2,
    "timeoutSecs": 60,
    "createdAt": "2026-03-13T10:00:00Z",
    "finishedAt": "2026-03-13T10:00:05Z"
  },
  "targets": [
    {
      "id": "t1",
      "hostname": "web-01.example.com",
      "status": "complete",
      "exitCode": 0,
      "stdout": " 10:00:05 up 42 days",
      "durationMs": 120
    }
  ]
}
```


### POST /api/v1/task/{id}/cancel

**Permission:** `push.execute`

Cancel a specific task run. Sets the task and its pending/dispatched targets to `cancelled` status.

**Response:**

```json
{"status": "cancelled", "id": "abc123"}
```

### POST /api/v1/task/cancel-running

**Permission:** `push.execute`

Cancel all tasks stuck in `running` or `pending` state. Also cancels their pending/dispatched targets.

**Response:**

```json
{"status": "cancelled", "count": 2}
```

---

## Workflows

Multi-step task orchestration with conditional branching.

### POST /api/v1/workflows

**Permission:** `workflow.execute`

Start a workflow execution.

**Request body:**

```json
{
  "name": "deploy-web",
  "operator": "admin"
}
```

**Response (201 Created):**

```json
{
  "id": "wf-abc123",
  "status": "running"
}
```

### GET /api/v1/workflows

**Permission:** `fleet.read`

List recent workflow runs.

**Query parameters:** `limit` (default 20)

**Response:**

```json
{
  "workflows": [
    {
      "id": "wf-abc123",
      "workflowName": "deploy-web",
      "status": "complete",
      "currentStep": 3,
      "totalSteps": 3,
      "operator": "admin",
      "createdAt": "2026-03-13T10:00:00Z"
    }
  ]
}
```

### GET /api/v1/workflows/{id}

**Permission:** `fleet.read`

Get a workflow run with per-step results.

### POST /api/v1/workflows/{id}/abort

**Permission:** `workflow.execute`

Abort a running workflow. Returns `{"status": "aborting"}`.

---

## Live Query

### POST /api/v1/query

**Permission:** `query.execute`

Query trait values from connected agents in real time.

**Request body:**

```json
{
  "traitPath": "os.distro",
  "target": "*.web.*",
  "timeout": 10
}
```

| Field | Required | Description |
|-------|----------|-------------|
| `traitPath` | Yes | Dot-separated trait path (e.g., `os.distro`, `hardware.memory_mb`) |
| `target` | Yes | Hostname glob or trait filter |
| `timeout` | No | Seconds to wait for responses (default: 10) |

**Response:**

```json
{
  "responded": [
    {"hostname": "web-01", "value": "ubuntu", "found": true}
  ],
  "timedOut": [
    {"hostname": "web-03"}
  ],
  "offline": [
    {"hostname": "web-05", "value": "ubuntu", "found": true, "collectedAt": "..."}
  ],
  "counts": {"responded": 1, "timedOut": 1, "offline": 1}
}
```

---

## AI Assistant

### POST /api/v1/ask

**Permission:** `query.execute`

Ask the AI assistant a question about your fleet.

**Request body:**

```json
{
  "question": "Why is web-01.example.com failing?",
  "context": {
    "envoy": "web-01.example.com"
  },
  "history": [
    {"role": "user", "content": "previous question"},
    {"role": "assistant", "content": "previous answer"}
  ]
}
```

**Response (JSON):**

```json
{
  "answer": "web-01.example.com is failing because...",
  "tools_used": ["get_envoy", "list_runs"],
  "tokens_used": 1234
}
```

**Response (SSE, if `Accept: text/event-stream`):**

Events are sent as server-sent events with JSON payloads:

- `{"type": "status", "status": "thinking", "tools_called": ["get_envoy"]}`
- `{"type": "delta", "text": "The envoy is..."}`
- `{"type": "done", "tools_used": ["get_envoy"], "tokens_used": 1234}`

---

## Doctor

### GET /api/v1/doctor

**Permission:** `fleet.read`

Health check endpoint that validates server subsystems.

**Response:**

```json
{
  "status": "healthy",
  "checks": [
    {"name": "database", "scope": "server", "status": "pass"},
    {"name": "tls", "scope": "server", "status": "pass", "message": "expires 2027-03-13"},
    {"name": "secrets", "scope": "server", "status": "pass", "message": "local"},
    {"name": "license", "scope": "server", "status": "pass", "message": "compliant, 42/100 envoys"},
    {"name": "spanner", "scope": "server", "status": "skip", "message": "standalone"},
    {"name": "integrations", "scope": "server", "status": "skip", "message": "no integrations configured"},
    {"name": "config", "scope": "server", "status": "pass", "message": "15 modules, 8 entries"},
    {"name": "stale_envoys", "scope": "fleet", "status": "pass"}
  ]
}
```

Check statuses: `pass`, `warn`, `fail`, `skip`. Check scope: `server` (Vigo itself) or `puddle` (managed envoys). Overall status: `healthy`, `degraded`, `unhealthy`. The Vigo Health page renders only `server`-scope checks; `puddle` conditions surface on the dashboard and envoys views.

---

## License

### GET /api/v1/license

**Permission:** `fleet.read`

Returns current license status including stage, tier, envoy counts, and expiration.

**Response:**

```json
{
  "stage": 0,
  "license": {
    "customer_name": "Acme Corp",
    "customer_id": "acme",
    "license_id": "lic-20260313-acme",
    "tier": "enterprise",
    "max_envoys": 500,
    "issued_at": "2026-03-13",
    "expires_at": "2027-03-13",
    "version": 1
  },
  "active_envoys": 12,
  "max_envoys": 500,
  "expires_at": "2027-03-13",
  "days_remaining": 365
}
```

The `tier` field is `"free"`, `"standard"`, `"growth"`, or `"enterprise"`. Omitted for legacy licenses without a tier (treated as free).

---

## Traits

### GET /api/v1/envoys/{hostname}/traits

**Permission:** `fleet.read`

Returns the latest collected traits for a envoy by hostname.

**Response:**

```json
{
  "hostname": "web-01.example.com",
  "collected_at": "2026-03-13T10:00:00Z",
  "traits": {
    "os": {"distro": "ubuntu", "version": "24.04"},
    "hardware": {"memory_mb": 8192, "cpu_count": 4}
  }
}
```

---

## Inventory

### GET /api/v1/inventory

**Permission:** `fleet.read`

Query envoys by trait values.

**Query parameters:**

| Parameter | Description |
|-----------|-------------|
| `where` | Repeatable. Each is `dot.path=value` (e.g., `where=os.distro=ubuntu`) |
| `show` | Comma-separated dot-paths to include in output (default: all traits) |

**Response:**

```json
{
  "count": 5,
  "envoys": [
    {
      "hostname": "web-01.example.com",
      "ip_address": "10.0.1.10",
      "collected_at": "2026-03-13T10:00:00Z",
      "traits": {"os.distro": "ubuntu"}
    }
  ]
}
```

---

## Spanner

### GET /api/v1/spanner/status

**Permission:** `fleet.read`

Returns spanner role and spoke statuses (hub mode only).

### GET /api/v1/spanner/spokes/{id}

**Permission:** `fleet.read`

Returns detailed status for a single spoke.

### GET /api/v1/spanner/spokes/{id}/envoys

**Permission:** `fleet.read`

List envoys on a specific spoke.

### GET /api/v1/spanner/spokes/{id}/runs

**Permission:** `fleet.read`

List recent runs on a specific spoke.

### POST /api/v1/spanner/spokes/{id}/push

**Permission:** Admin role

Force-push to envoys on a specific spoke.

### POST /api/v1/spanner/reassign

**Permission:** Admin role


Reassign a envoy from one spoke to another.

**Request body:**

```json
{
  "envoy_id": "...",
  "from_spoke_id": "spoke-east",
  "to_spoke_id": "spoke-west"
}
```

### POST /api/v1/spanner/spokes/{id}/drain

**Permission:** Admin role

Drain all envoys from a spoke (move to target spoke or auto-route by patterns).

---

## Config

### GET /api/v1/config/trace

**Permission:** `fleet.read`

Trace config resolution for a hostname. Shows the full directory inheritance chain with vars and override markers at each level, which modules came from which files or roles, how roles expanded, and where each variable's final value originated.

**Query parameters:**

| Parameter | Required | Description |
|-----------|----------|-------------|
| `hostname` | Yes | The hostname to trace |
| `module` | No | Filter trace to only the branch relevant to this module |

**Response:**

```json
{
  "hostname": "web01.prod.example.com",
  "matched": true,
  "match_pattern": "web*.prod.example.com",
  "pattern_type": "glob",
  "environment": "production",
  "roles": ["webserver"],
  "role_expansion": [
    {
      "role": "webserver",
      "includes": ["base-security"],
      "modules": ["firewall", "sshd", "nginx", "logrotate"]
    }
  ],
  "inheritance": [
    {
      "dir": "",
      "file": "common.vgo",
      "modules": ["monitoring"],
      "vars": {"dns_server": "1.1.1.1"}
    },
    {
      "dir": "production",
      "file": "common.vgo",
      "modules": ["log-shipping"],
      "vars": {"dns_server": "10.0.0.1", "log_level": "warn"},
      "overrides": ["dns_server"]
    }
  ],
  "modules": ["monitoring", "log-shipping", "firewall", "sshd", "nginx", "logrotate"],
  "module_sources": {
    "monitoring": "common.vgo",
    "log-shipping": "production/common.vgo",
    "firewall": "role:webserver",
    "sshd": "role:webserver",
    "nginx": "role:webserver",
    "logrotate": "role:webserver"
  },
  "var_sources": [
    {"key": "nginx_port", "value": 443, "source": "environments.vgo:production"},
    {"key": "log_level", "value": "warn", "source": "match-block"}
  ],
  "resource_count": 14
}
```

When `?module=` is provided, the `inheritance`, `modules`, and `role_expansion` fields are filtered to only entries relevant to that module. Vars are kept unfiltered.

If no entry matches, `matched` is `false` and other fields are omitted.

### GET /api/v1/config/tree

**Permission:** `fleet.read`

Returns the directory inheritance structure for visualization. Shows `common.vgo` files at each level, leaf entries with their match patterns, and accumulated module totals.

**Response:** A tree of nodes with `dir`, `file`, `modules`, `roles`, `match`, `total`, `excluded`, and `children` fields.

### GET /api/v1/config/search

**Permission:** `fleet.read`

Search envoy entries by match pattern, module name, or var name. Returns matching entries with their full inheritance chain and accumulated modules.

**Query parameters:**

| Parameter | Required | Description |
|-----------|----------|-------------|
| `match` | No | Glob pattern to match against envoy `match:` patterns (e.g., `*web*`) |
| `module` | No | Module name — find entries that use this module (inherited or direct) |
| `var` | No | Var name — find entries that define this var |

At least one parameter is required.

**Response:**

```json
{
  "entries": [
    {
      "match": "web*.prod.example.com",
      "dir": "production/web",
      "inheritance": [
        {"dir": "", "file": "common.vgo", "modules": ["sshd", "monitoring"]},
        {"dir": "production", "file": "common.vgo", "modules": ["log-shipping"]}
      ],
      "own_modules": ["nginx"],
      "total": ["sshd", "monitoring", "log-shipping", "nginx"],
      "vars": {"log_level": "warn", "nginx_port": 443}
    }
  ]
}
```

---

## Audit

### GET /api/v1/audit

**Permission:** `audit.read`

Unified audit log across config publishes, task runs, and workflows.

**Query parameters:**

| Parameter | Default | Description |
|-----------|---------|-------------|
| `since` | — | Time filter: duration (`24h`) or RFC3339 timestamp |
| `operator` | — | Filter by operator name |
| `type` | — | Event type: `config_publish`, `task_run`, `workflow_run` |
| `limit` | `50` | Maximum events (max 200) |

**Response:**

```json
{
  "events": [
    {
      "id": "a1b2c3d4",
      "type": "config_publish",
      "operator": "admin",
      "timestamp": "2026-03-13T14:30:00Z",
      "summary": "3 files changed"
    }
  ]
}
```

### GET /api/v1/audit/verify

**Permission:** `audit.read`

Verifies the SHA-256 hash chain integrity of the audit log.

**Response:**

```json
{
  "valid": true,
  "entry_count": 1234,
  "first_broken": null,
  "error": ""
}
```

If the chain is broken:

```json
{
  "valid": false,
  "entry_count": 1234,
  "first_broken": 567,
  "error": "hash mismatch at entry 567: expected a1b2c3d4... got e5f6a7b8..."
}
```

### POST /api/v1/emergency-access

**Permission:** Admin role

Record an emergency access event in the tamper-evident audit chain (HIPAA 164.312(a)(2)(ii)). Used by `vigocli auth emergency-access` for break-glass scenarios.

**Request body:**

```json
{
  "reason": "Account lockout — resetting credentials for operator jsmith"
}
```

| Field | Required | Description |
|-------|----------|-------------|
| `reason` | Yes | Free-text justification for the emergency access |

**Response (200 OK):**

```json
{
  "status": "recorded"
}
```

---

## Config Publishes

### GET /api/v1/config/publishes

**Permission:** `fleet.read`

List recent config publish records.

**Query parameters:**

| Parameter | Default | Description |
|-----------|---------|-------------|
| `limit` | `20` | Maximum records (max 100) |

### POST /api/v1/config/publishes

**Permission:** `config.publish`

Record a config publish for audit trail.

**Request body:**

```json
{
  "operator": "admin",
  "changed_files": ["modules/nginx.vgo"],
  "removed_files": [],
  "diff_summary": "--- modules/nginx.vgo\n+++ modules/nginx.vgo\n..."
}
```

### GET /api/v1/config/publishes/{id}

**Permission:** `fleet.read`


Get details for a specific config publish, including the diff summary.

---

## Config Versions and Rollback

### GET /api/v1/config/versions

**Permission:** `fleet.read`

List available config snapshots for rollback.

**Response:**

```json
{
  "versions": ["20260313T143000Z", "20260312T091500Z"]
}
```

### POST /api/v1/config/rollback/{version}

**Permission:** `config.rollback`

Rollback to a previous config snapshot.

---

## Config Freeze

### POST /api/v1/config/freeze

**Permission:** `config.publish`

Set a global config publish freeze.

**Request body:**

```json
{
  "freeze_until": "2026-03-14T06:00:00Z"
}
```

### POST /api/v1/config/unfreeze

**Permission:** `config.publish`

Remove the config publish freeze.

### POST /api/v1/config/reload

**Permission:** `config.publish`

Re-read all config files from disk. Called automatically by `vigocli config publish`.

### POST /api/v1/config/retract

**Permission:** `config.publish`

Generate retract modules for removed modules. Called automatically by `vigocli config publish` when `publish.retraction.enabled` and `ai.enabled` are both true. Can also be called directly.

**Request body:**

```json
{
  "modules": [
    {"name": "nginx", "content": "name: nginx\nresources:\n  - name: install\n    type: package\n    package: nginx"}
  ]
}
```

**Response:**

```json
{
  "enabled": true,
  "results": [
    {
      "name": "nginx",
      "retract_yaml": "name: nginx-retract\n...",
      "warnings": [{"resource": "install", "type": "package", "confidence": "high", "message": "remove nginx"}],
      "envoys": [{"hostname": "web1.example.com", "last_run": "2026-04-01T12:00:00Z"}]
    }
  ]
}
```

When retraction or AI is disabled, returns `{"enabled": false, "reason": "..."}`. See [Module Retraction](../../engineering/writing-modules/retraction.md).

---

## Config Changes (Approval)

These endpoints are only active when `publish.approval_required: true` in `server.yaml`. When disabled, all endpoints return 404.

### POST /api/v1/config/changes

**Permission:** `config.publish`

Create a pending config change. The change must be approved by a different admin before it takes effect.

**Request body:**

```json
{
  "diff_summary": "--- modules/nginx.vgo\n+++ modules/nginx.vgo\n..."
}
```

**Response (201 Created):**

```json
{
  "id": "chg-abc123",
  "status": "pending",
  "requested_by": "admin",
  "diff_summary": "...",
  "created_at": "2026-03-16T10:00:00Z",
  "expires_at": "2026-03-17T10:00:00Z"
}
```

### GET /api/v1/config/changes

**Permission:** `fleet.read`

List pending config changes.

**Response:**

```json
{
  "changes": [
    {
      "id": "chg-abc123",
      "status": "pending",
      "requested_by": "admin",
      "created_at": "2026-03-16T10:00:00Z",
      "expires_at": "2026-03-17T10:00:00Z"
    }
  ]
}
```

### GET /api/v1/config/changes/{id}

**Permission:** `fleet.read`

Get a single config change, including the full diff summary.

### POST /api/v1/config/changes/{id}/approve

**Permission:** `config.publish`

Approve and activate a pending config change. Returns 403 if the approver is the same user who requested the change.

**Response:**

```json
{
  "id": "chg-abc123",
  "status": "approved",
  "approved_by": "ops-lead",
  "approved_at": "2026-03-16T11:00:00Z"
}
```

### POST /api/v1/config/changes/{id}/reject

**Permission:** `config.publish`

Reject a pending config change.


**Request body:**

```json
{
  "reason": "nginx worker count too high for staging"
}
```

**Response:**

```json
{
  "id": "chg-abc123",
  "status": "rejected",
  "rejected_by": "ops-lead",
  "reason": "nginx worker count too high for staging",
  "rejected_at": "2026-03-16T11:00:00Z"
}
```

---

### POST /api/v1/secrets/rotated

**Permission:** Admin role (internal — called by the watcher subsystem)

Notify the server that a secret has been changed. Triggers `watch_secret` resources on affected envoys at their next check-in. Called automatically by `vigocli secrets set`.

**Request body:**

```json
{
  "path": "vigo/grafana/admin-password"
}
```

**Response:**

```json
{
  "path": "vigo/grafana/admin-password",
  "affected": 2
}
```

### POST /api/v1/secrets/accessed

**Permission:** Admin role (internal — called by the watcher subsystem)

Record an audit trail entry when a secret is revealed via CLI. Called automatically by `vigocli secrets reveal`.

**Request body:**

```json
{
  "path": "vigo/db/dsn"
}
```

**Response:**

```json
{
  "recorded": true
}
```

The event is recorded in the tamper-evident audit chain as type `secret_accessed`.

---

## Envoy Tags

### PUT /api/v1/envoys/{id}/tags

**Permission:** `fleet.write` (rides the gRPC gateway; gate enforced by the gRPC interceptor)

Set tags on a envoy.

**Request body:**

```json
{
  "tags": ["webserver", "production"]
}
```

### GET /api/v1/envoys/{id}/tags

**Permission:** `fleet.read`

Get tags for a envoy.

---

## Envoy Maintenance

### PUT /api/v1/envoys/{id}/maintenance

**Permission:** `fleet.write`

Set a maintenance window for a envoy.

**Request body:**

```json
{
  "until": "2026-03-14T06:00:00Z"
}
```

### DELETE /api/v1/envoys/{id}/maintenance

**Permission:** `fleet.write`

Clear the maintenance window.

---

## Envoy Compliance

### GET /api/v1/envoys/{id}/compliance

**Permission:** Authenticated (route-level gate pending — `compliance.read` recommended)

Returns the current compliance status for a envoy.

**Response:**

```json
{
  "envoy_id": "a1b2c3d4",
  "hostname": "web-01.example.com",
  "status": "compliant",
  "last_run_at": "2026-03-16T10:00:00Z"
}
```

---

## Compliance Documentation

Operator-uploaded artifacts (BAA, IR plan, risk assessment, training records, signed contracts, etc.) that satisfy `documented`-scope framework controls. Files are envelope-encrypted at rest with the secrets master key under `compliance.docs.docs_dir` (default `/srv/vigo/compliance-docs/`); metadata is in `compliance_docs` and link rows in `compliance_doc_links`.

Available only when the deployment uses the `local` secrets backend — under any other backend the routes return `503 Service Unavailable`. Uploaded by [permission](../../operations/authentication.md): `compliance.docs.write` to upload/replace/soft-delete and to manage links; `compliance.read` to list/download; `compliance.docs.purge` to physically remove a soft-deleted doc and its ciphertext.

Every mutation records an entry in the [tamper-evident audit chain](../../compliance/tamper-evident-audit.md) with the doc's SHA-256.

### POST /api/v1/compliance/docs

**Permission:** `compliance.docs.write`

Upload a new artifact.

**Content-Type:** `multipart/form-data`

| Field | Required | Description |
|-------|----------|-------------|
| `file` | Yes | The binary upload. Magic-byte detected MIME must match the configured allowlist (PDF, DOCX, XLSX, PPTX, TXT, MD, PNG, JPG, SVG by default). |
| `filename` | No | Override the multipart `Filename`. Defaults to the upload's filename when omitted. |
| `mime_type` | No | Declared MIME type. Validated against magic-byte detection — a renamed `.pdf` containing JPEG bytes is rejected. |
| `doc_type` | No | Free-form category for cross-framework reuse hints (`BAA`, `IR Plan`, `Risk Assessment`, `Security Policy`, `Training Records`, `Pentest Report`, etc.). |
| `links` | No | JSON array of `{"framework": "...", "control_id": "..."}` pairs. The doc is linked to each (framework, control_id) on creation. May be empty; manage links later via the link endpoints. |

Size caps come from `compliance.docs.{max_doc_bytes, max_per_framework_bytes, max_total_bytes}` in `server.yaml`. Defaults: 50 MiB per doc, 5 GiB per framework, 50 GiB total.

**Response (201):** doc metadata.

```json
{
  "id": "8f3a...",
  "doc_type": "BAA",
  "filename": "acme-baa.pdf",
  "mime_type": "application/pdf",
  "size_bytes": 142133,
  "sha256": "9b71c1...",
  "uploaded_by": "alice",
  "uploaded_at": "2026-04-27T21:25:00-07:00"
}
```

### GET /api/v1/compliance/docs

**Permission:** `compliance.read`

List active (non-soft-deleted) doc metadata. Optional filter by `?framework=&control_id=` to narrow to docs attached to a specific control.

**Response:** `{"docs": [{...}, {...}]}`

### GET /api/v1/compliance/docs/{id}

**Permission:** `compliance.read`

Single-doc metadata. Soft-deleted docs are still readable here so audit history remains queryable.

### GET /api/v1/compliance/docs/{id}/download

**Permission:** `compliance.read`

Stream the decrypted doc. Sets `Content-Type` from the stored `mime_type`, `Content-Disposition: attachment; filename=...`, and `X-Compliance-Doc-Sha256` for integrity verification at the client.

### PUT /api/v1/compliance/docs/{id}

**Permission:** `compliance.docs.write`

Replace an existing doc. Same multipart shape as `POST`. The new doc inherits the predecessor's `(framework, control_id)` link set unless `links` is provided. The predecessor stays in place with `replaced_at` + `replaced_by_id` populated so audit history points to the new revision.

### DELETE /api/v1/compliance/docs/{id}

**Permission:** `compliance.docs.write`

Soft-delete: hides the doc from list/by-control views but keeps the row + ciphertext on disk so historical audit bundles can still verify it. Returns `409 Conflict` if already soft-deleted.

### POST /api/v1/compliance/docs/{id}/purge

**Permission:** `compliance.docs.purge`

Physical purge: removes ciphertext from disk and the metadata row from the database. Cascades through `compliance_doc_links`. Reserved for the `compliance.docs.purge` permission.

### GET /api/v1/compliance/docs/{id}/links

**Permission:** `compliance.read`

List every `(framework, control_id)` pair the doc is attached to.

**Response:**

```json
{
  "links": [
    {"framework": "hipaa", "control_id": "164.308(b)(1)", "linked_at": "...", "linked_by": "alice"},
    {"framework": "hitrust", "control_id": "09.f", "linked_at": "...", "linked_by": "alice"},
    {"framework": "soc2", "control_id": "CC9.2", "linked_at": "...", "linked_by": "alice"}
  ]
}
```

### POST /api/v1/compliance/docs/{id}/links

**Permission:** `compliance.docs.write`

Attach an existing doc to an additional `(framework, control_id)`. Idempotent — duplicate links are no-ops.

**Request body:**

```json
{ "framework": "soc2", "control_id": "CC9.2" }
```

### DELETE /api/v1/compliance/docs/{id}/links/{framework}/{controlID}

**Permission:** `compliance.docs.write`

Detach a doc from a single `(framework, control_id)` without touching the underlying file. The doc may still be linked to other controls.

---

## Audit Bundle Export

Self-contained zip suitable for handing to an external auditor. Bundles a single framework's compliance report (HTML + PDF), structured per-control evidence, the raw audit-chain entries for the period, daily fleet convergence history, and decrypted copies of every operator-uploaded compliance document attached to the framework's controls.

Permission gate: `audit.read` is mandatory; `compliance.read` is checked separately and gates inclusion of the `docs/` directory + the `documents` block in `evidence.json`. A caller with `audit.read` but not `compliance.read` still receives a usable bundle minus the artifacts.

The download itself records a tamper-evident audit-chain entry (`compliance.bundle.download`).

### GET /api/v1/report/audit-bundle/{slug}

**Permission:** `compliance.read`

Stream the zip.

| Query param | Default | Description |
|-------------|---------|-------------|
| `period` | `365d` | Lookback window for `audit-log.csv` and `convergence.csv`. Accepts integer days (`365`, `30`), `Nd`, `Nmo`, or `Ny`. |

**Response:** `Content-Type: application/zip`, attachment with filename `vigo-audit-bundle-<slug>-<YYYYMMDD>.zip`.

**Bundle layout:**

| Path | Description |
|------|-------------|
| `manifest.json` | Framework, period, generated-at, list of artifacts with their plaintext SHA-256 digests. Auditors verify integrity by extracting the zip and `sha256sum`-ing each path. |
| `report.pdf` | WeasyPrint-rendered compliance report. Omitted with a `warnings` entry in `manifest.json` if the container lacks `weasyprint`. |
| `report.html` | Same content as text/html, always present so the bundle is readable without a PDF viewer. |
| `evidence.json` | Per-control structured evidence: scope, status, modules, traits, attached doc IDs + filenames + SHA-256 (when `compliance.read` is held). |
| `docs/<doc-id>__<filename>` | Decrypted copies of every active compliance doc linked to the framework's controls. Filename sanitized — path separators stripped so docs/ is flat. Only present with `compliance.read`. |
| `audit-log.csv` | Audit-chain entries with timestamps within the period. Columns: id, timestamp, event_type, actor, source, summary, payload, prev_hash, hash. |
| `convergence.csv` | Daily fleet convergence snapshots within the period. Columns: recorded_at, total, converged, relapsed, diverged, failed, offline, no_data. |

**Verification example:**

```bash
unzip vigo-audit-bundle-hipaa-20260427.zip -d audit/
cd audit
jq -r '.artifacts[] | "\(.sha256)  \(.path)"' manifest.json | sha256sum -c
```

Every line should print `<path>: OK`. A mismatch indicates the bundle was tampered with after generation, the bundle was assembled across two different config publishes, or a doc was replaced/purged between zip-write attempts.

---

## Envoy Force Update

### POST /api/v1/envoys/{id}/update

**Permission:** `fleet.write`

Force an immediate check-in for a specific envoy. The envoy will receive the update at its next poll cycle.

### POST /api/v1/envoys/update

**Permission:** `fleet.write`

Force an immediate check-in for all envoys.

---

## Users

User account management. Requires admin authentication (except first-user bootstrap).

### GET /api/v1/users

**Permission:** Admin role

List all user accounts. Admin only.

**Response:**

```json
{
  "users": [
    {
      "id": "u1",
      "username": "admin",
      "role": "admin",
      "auth_method": "basic",
      "created_at": "2026-03-01T00:00:00Z"
    }
  ]
}
```

### POST /api/v1/users

**Permission:** Admin role (open until the first admin exists, then admin-gated)

Create a new user account. The first user created is automatically assigned the admin role regardless of the request.

**Request body:**

```json
{
  "username": "ops-lead",
  "password": "...",
  "role": "admin"
}
```

### PUT /api/v1/users/{id}

**Permission:** Admin role

Update a user account (role, password). Admin only.


### DELETE /api/v1/users/{id}

**Permission:** Admin role

Delete a user account. Admin only. Cannot delete the last admin.

### POST /api/v1/users/totp/setup

**Permission:** Authenticated (own account)

Generate a TOTP secret for the authenticated user. Returns the `otpauth://` URL for QR code scanning. TOTP is not yet enabled until confirmed via `/totp/confirm`.

**Response (200 OK):**

```json
{
  "url": "otpauth://totp/Vigo:admin?secret=JBSWY3DPEHPK3PXP&issuer=Vigo"
}
```

### POST /api/v1/users/totp/confirm

**Permission:** Authenticated (own account)

Confirm TOTP setup by providing a valid 6-digit code from the authenticator app. This enables TOTP for the user and returns one-time recovery codes.

**Request body:**

```json
{
  "code": "123456"
}
```

**Response (200 OK):**

```json
{
  "enabled": true,
  "recovery_codes": ["abc123", "def456", "..."]
}
```

### POST /api/v1/users/totp/disable

**Permission:** Authenticated (own account)

Disable TOTP for the authenticated user. Requires a valid TOTP code or recovery code.

**Request body:**

```json
{
  "code": "123456"
}
```

**Response (200 OK):**

```json
{
  "disabled": true
}
```

---

## API Tokens


Create and manage API tokens for programmatic access. Tokens use the `vgot_` prefix.

### POST /api/v1/auth/tokens

**Permission:** Authenticated (own account)

Create a new API token. Requires authentication.

**Request body:**

```json
{
  "name": "ci-pipeline",
  "expires_in": "720h"
}
```

**Response (201 Created):**

```json
{
  "id": "tok-abc123",
  "name": "ci-pipeline",
  "token": "vgot_...",
  "expires_at": "2026-04-15T10:00:00Z"
}
```

The plaintext token is only returned once at creation time.

### GET /api/v1/auth/tokens

**Permission:** Authenticated (own account)

List API tokens for the authenticated user. Token values are not returned.

### DELETE /api/v1/auth/tokens/{id}

**Permission:** Authenticated (own account)

Revoke an API token.

---

## Preview

### GET /api/v1/preview

**Permission:** `fleet.read`

Multi-node impact analysis showing which envoys would be affected by changes to a module.

**Query parameters:**

| Parameter | Required | Description |
|-----------|----------|-------------|
| `module` | Yes | Module name to analyze |

**Response:**

```json
{
  "module": "nginx",
  "affected": [
    {"hostname": "web-01.example.com", "envoy_id": "a1b2c3d4"},
    {"hostname": "web-02.example.com", "envoy_id": "e5f6g7h8"}
  ],
  "count": 2
}
```

---

## OpenAPI

### GET /api/v1/openapi.json

**Permission:** Authenticated (any role)

Returns the OpenAPI specification as JSON.

---

## Health Check

### GET /healthz

**Permission:** Public (no authentication)

Returns a simple health status. Useful for load balancer probes and monitoring.

**Response:**

```json
{"status":"ok","spoke_id":""}
```

The `spoke_id` field is populated when the server runs in spoke mode.

---

## Database Administration

These endpoints provide direct database inspection and maintenance. All endpoints require admin authentication.

### GET /api/v1/db/stats

**Permission:** Admin role

Returns database statistics including file size, WAL size, page count, page size, and per-table row counts.

**Response:**

```json
{
  "driver": "sqlite",
  "file_size": 4218924,
  "wal_size": 32768,
  "page_count": 1024,
  "page_size": 4096,
  "tables": {
    "envoys": 42,
    "runs": 1250,
    "envoy_traits": 42
  }
}
```

### GET /api/v1/db/tables

**Permission:** Admin role

Returns all tables with their row counts.

**Response:**


```json
{
  "envoys": 42,
  "runs": 1250,
  "envoy_traits": 42
}
```

### GET /api/v1/db/integrity

**Permission:** Admin role

Runs `PRAGMA integrity_check` and `PRAGMA foreign_key_check` (SQLite) or a connection ping (other drivers).

**Response:**

```json
{
  "driver": "sqlite",
  "status": "ok",
  "ok": true,
  "foreign_key_violations": 0
}
```

If foreign key violations are found, a `foreign_key_details` array is included.

### POST /api/v1/db/vacuum

**Permission:** Admin role

Runs `VACUUM` to reclaim unused space. SQLite only. Returns the database file size before and after.

**Response:**

```json
{
  "status": "ok",
  "before": 4218924,
  "after": 3145728,
  "reclaimed": 1073196
}
```

### POST /api/v1/db/checkpoint

**Permission:** Admin role

Forces a WAL checkpoint with `TRUNCATE` mode. SQLite only.

**Response:**

```json
{
  "status": "ok",
  "pages_total": 128,
  "pages_written": 128
}
```

### POST /api/v1/db/query

**Permission:** Admin role

Execute a read-only SQL query. Write operations (`INSERT`, `UPDATE`, `DELETE`, `DROP`, `ALTER`, `CREATE`, `ATTACH`, `DETACH`, `REINDEX`, `VACUUM`, `PRAGMA`) are rejected with 403.

**Request body:**


```json
{
  "sql": "SELECT hostname, ip_address FROM envoys WHERE status = 'active' LIMIT 10"
}
```

**Response:**

```json
{
  "columns": ["hostname", "ip_address"],
  "rows": [
    {"hostname": "web-01.example.com", "ip_address": "10.0.1.10"}
  ],
  "count": 1
}
```

---

## Other Endpoints

| Method | Path | Description |
|--------|------|-------------|
| GET | `/metrics` | Prometheus metrics (see [Metrics](../../operations/metrics.md)) |
| GET | `/healthz` | Health check (see above) |
| GET | `/bootstrap` | Agent bootstrap script |
| GET | `/bootstrap/agent/{os}/{arch}` | Agent binary download |
| GET | `/bootstrap/ca.pem` | CA certificate download |
| GET | `/api/v1/agent/latest` | Agent version, SHA256 checksum, and ED25519 signature (`?os=&arch=`) |
| GET | `/api/v1/agent/download/{os}/{arch}` | Agent binary download |
| GET | `/api/v1/agent/signing-key` | ED25519 signing public key (hex) |
| POST | `/api/v1/bootstrap/register` | Agent enrollment with CSR signing |

---

## Export

Compliance evidence endpoints for integration with SIEM, CMDB, and OSCAL tools. Each endpoint must be individually enabled in `server.yaml` under the `export:` section. Disabled endpoints return 404.

### GET /api/v1/export/siem

**Permission:** `fleet.read`

Returns a JSON array of SIEM events derived from run results.

**Query parameters:**

| Parameter | Default | Description |
|-----------|---------|-------------|
| `since` | 24 hours ago | RFC 3339 timestamp. Only events after this time are returned |
| `envoy` | (all) | Filter by hostname |
| `limit` | 1000 | Maximum events to return (max 10000) |

**Response:**

```json
[
  {
    "timestamp": "2026-03-13T10:05:00Z",
    "hostname": "web-01.example.com",
    "envoy_id": "a1b2c3d4",
    "run_id": "run-5678",
    "module_name": "nginx",
    "resource_name": "nginx.conf",
    "resource_type": "file",
    "action": "updated",
    "changed": true,
    "duration_ms": 42,
    "run_status": "compliant",
    "source": "vigo"
  }
]
```

| Field | Type | Description |
|-------|------|-------------|
| `timestamp` | string | RFC 3339 time of the resource action |
| `hostname` | string | Envoy hostname |
| `envoy_id` | string | Envoy UUID |
| `run_id` | string | Convergence run ID |
| `module_name` | string | Module that owns the resource |
| `resource_name` | string | Resource name within the module |
| `resource_type` | string | Resource type (file, package, service, etc.) |
| `action` | string | Action taken (created, updated, removed, noop, failed) |
| `changed` | bool | Whether the resource was modified |
| `error` | string | Error message (omitted when empty) |
| `duration_ms` | int | Time spent on this resource in milliseconds |
| `run_status` | string | Overall run status (converged, relapsed, diverged, errors) |
| `source` | string | Always `"vigo"` |

### GET /api/v1/export/cmdb

**Permission:** `fleet.read`

Returns a JSON array of CMDB records from the FleetIndex (zero DB queries).

**Query parameters:**

| Parameter | Default | Description |
|-----------|---------|-------------|
| `status` | (all) | Filter by convergence status: `converged`, `relapsed`, `diverged`, `degraded`, `failed`, `offline`, `no data` |
| `environment` | (all) | Filter by environment (e.g., `prod`, `staging`) |

**Response:**

```json
[
  {
    "hostname": "web-01.example.com",
    "ip": "10.0.1.10",
    "envoy_id": "a1b2c3d4",
    "os": "ubuntu",
    "os_version": "24.04",
    "architecture": "x86_64",
    "kernel": "6.8.0-101-generic",
    "cpu_count": 4,
    "memory_total": 8589934592,
    "compliance_status": "compliant",
    "last_seen": "2026-03-13T10:05:00Z",
    "environment": "prod",
    "enrolled_at": "2026-01-15T08:00:00Z",
    "uptime": 3628800
  }
]
```

| Field | Type | Description |
|-------|------|-------------|
| `hostname` | string | Envoy hostname |
| `ip` | string | IP address |
| `envoy_id` | string | Envoy UUID |
| `os` | string | OS family |
| `os_version` | string | OS version |
| `architecture` | string | CPU architecture |
| `kernel` | string | Kernel version |
| `cpu_count` | int | Number of CPUs |
| `memory_total` | int | Total memory in bytes |
| `compliance_status` | string | Current compliance status |
| `last_seen` | string | RFC 3339 time of last check-in |
| `environment` | string | Environment tier |
| `enrolled_at` | string | RFC 3339 enrollment time |
| `uptime` | int | System uptime in seconds |

---

## Backup

### POST /api/v1/backup/snapshot

**Permission:** `backup.manage` (rides the gRPC gateway; gate enforced by the gRPC interceptor)

Create an on-demand backup snapshot. Requires admin authentication.

**Response (201 Created):**

```json
{
  "path": "/backup/vigo-20260315T140000Z.tar.gz",
  "size": 4218924,
  "created_at": "2026-03-15T14:00:00Z"
}
```

### GET /api/v1/backup/download

**Permission:** `backup.manage` (rides the gRPC gateway; gate enforced by the gRPC interceptor)

Download the latest backup archive as a `.tar.gz` file. Requires admin authentication.

**Response:** Binary stream with `Content-Type: application/gzip` and `Content-Disposition: attachment; filename="vigo-backup.tar.gz"`.

---

## Peer Replication

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/peer/status` | Returns peer role, primary address, last sync time, files synced, config version. Returns 404 if peer replication not configured. |
| POST | `/api/v1/peer/promote` | Promotes the server to primary. Returns 404 if not configured. Split-brain resolved by config version. |

---

### GET /api/v1/export/oscal

**Permission:** `compliance.read`

Returns a minimal valid OSCAL Assessment Results JSON envelope (v1.1.2). The `findings` array is currently empty; future versions will map Vigo compliance data to OSCAL control findings.


**Response:**

```json
{
  "assessment-results": {
    "uuid": "...",
    "metadata": {
      "title": "Vigo Assessment Results",
      "last-modified": "2026-03-13T10:00:00Z",
      "version": "1.0.0",
      "oscal-version": "1.1.2"
    },
    "results": [
      {
        "uuid": "...",
        "title": "Vigo Compliance Assessment",
        "start": "2026-03-13T10:00:00Z",
        "findings": [],
        "remarks": "Stub: OSCAL findings mapping is not yet implemented."
      }
    ]
  }
}
```

## Risk Scoring

Per-envoy and fleet-wide risk scores computed from security scan traits, convergence status, and connectivity data.

### GET /api/v1/risk

**Permission:** `fleet.read`

Return the fleet-wide risk summary with distribution and top riskiest envoys.

**Response (200 OK):**

```json
{
  "total_envoys": 14,
  "scored_envoys": 12,
  "avg_score": 23.5,
  "max_score": 78,
  "distribution": {
    "low": 3,
    "medium": 4,
    "high": 3,
    "critical": 2
  },
  "top_risks": [
    {
      "envoy_id": "abc123",
      "hostname": "db01.prod.example.com",
      "score": 78,
      "level": "critical",
      "factors": [
        {"name": "Critical CVEs", "points": 30, "detail": "3 critical CVEs"},
        {"name": "Convergence Failed", "points": 15, "detail": "last run had failures"}
      ]
    }
  ]
}
```

### GET /api/v1/risk/{envoyID}

**Permission:** `fleet.read`

Return the risk score breakdown for a single envoy.

**Response (200 OK):**

```json
{
  "envoy_id": "abc123",
  "hostname": "db01.prod.example.com",
  "score": 78,
  "level": "critical",
  "factors": [
    {"name": "Critical CVEs", "points": 30, "detail": "3 critical CVEs"},
    {"name": "High CVEs", "points": 15, "detail": "3 high CVEs"},
    {"name": "Hardening Gap", "points": 18, "detail": "Lynis score 82/100"},
    {"name": "Convergence Failed", "points": 15, "detail": "last run had failures"}
  ]
}
```

**Error (404):** `{"error": "envoy not found"}`

### GET /api/v1/risk/history

**Permission:** `fleet.read`

Daily risk posture snapshots for trend analysis.

**Query parameters:**

| Parameter | Default | Description |
|-----------|---------|-------------|
| `days` | `90` | Number of days of history to return (max 365) |

**Response (200 OK):**

```json
{
  "history": [
    {
      "date": "2026-03-21",
      "avg_score": 23,
      "max_score": 78,
      "distribution": {
        "low": 3,
        "medium": 4,
        "high": 3,
        "critical": 2
      },
      "scored_envoys": 12,
      "total_envoys": 14
    },
    {
      "date": "2026-03-20",
      "avg_score": 25,
      "max_score": 80,
      "distribution": {
        "low": 2,
        "medium": 5,
        "high": 3,
        "critical": 2
      },
      "scored_envoys": 12,
      "total_envoys": 14
    }
  ]
}
```

### GET /api/v1/risk/insurance

**Permission:** `fleet.read`

Cyber insurance attestation report in JSON format. Includes a per-report identifier, organization name (from `branding.org_name`), the authenticated user who prepared it, the 90-day attestation period, current fleet risk posture, a daily trend series, and fleet composition.

**Query parameters:**

| Parameter | Default | Description |
|-----------|---------|-------------|
| `download` | — | If `1`, sets `Content-Disposition: attachment` for file download |

**Response (200 OK):**

```json
{
  "report_id": "VIGO-INS-20260417-A3F2B1",
  "organization": "Acme Corp",
  "prepared_by": "Jane Admin",
  "generated_at": "2026-04-17T14:00:00Z",
  "generated_by": "Vigo 0.23.25",
  "period_start": "2026-01-17",
  "period_end": "2026-04-17",
  "current_posture": {
    "avg_score": 23,
    "max_score": 78,
    "scored_envoys": 12,
    "total_envoys": 14,
    "distribution": { "low": 3, "medium": 4, "high": 3, "critical": 2 }
  },
  "trend": [
    { "date": "2026-04-17", "avg_score": 23, "max_score": 78, "critical": 2, "high": 3, "scored_envoys": 12 }
  ],
  "fleet_summary": {
    "total_envoys": 14,
    "os_family": { "linux": 13, "windows": 1 }
  }
}
```

The `report_id` field is a unique identifier per download (format `VIGO-INS-<YYYYMMDD>-<6-hex>`); include it in any underwriter correspondence to reference a specific report instance.

### GET /api/v1/risk/insurance.html

**Permission:** `fleet.read`

Formal HTML cyber insurance attestation. Self-contained, suitable for direct viewing, emailing as an attachment, or printing. The document has a cover block with the organization name, a meta-data block (report ID, attestation period, preparer), an executive statement, five numbered sections (current posture, highest-risk assets, 90-day trend, fleet composition, methodology), and an attestation signatory block with four blank signature lines.

**Theme behavior:** the page follows the viewer's `prefers-color-scheme` on screen (dark background in dark browser sessions, light background in light sessions), and forces black-on-white when printed or saved as PDF via `@media print` — so the attachment-delivered version always looks like a formal black-ink document regardless of viewer preference.

**Query parameters:**

| Parameter | Default | Description |
|-----------|---------|-------------|
| `download` | — | If `1`, sets `Content-Disposition: attachment` for file download |

**Response:** HTML page with `Content-Type: text/html`.

### GET /api/v1/risk/insurance.pdf

**Permission:** `fleet.read`

Native PDF version of the cyber insurance attestation, rendered server-side via WeasyPrint. Use this endpoint when emailing the attestation to an underwriter or attaching it to a case file — delivers a real `.pdf` with consistent cross-client rendering, no "save as PDF" dance.

**Response (200 OK):** PDF document with `Content-Type: application/pdf` and `Content-Disposition: attachment; filename="cyber-insurance-attestation-YYYY-MM-DD.pdf"`.

**Response (503 Service Unavailable):** returned if the server's WeasyPrint runtime is missing (dev environments without the published Docker image). Falls back to the HTML endpoint:

```json
{
  "error": "pdf generation unavailable on this server",
  "fallback": "/api/v1/risk/insurance.html"
}
```

The PDF uses the same underlying HTML + print CSS as the `.html` endpoint, so content is identical — this endpoint simply automates the print-to-PDF step on the server. Render time is typically 500 ms–2 s; subprocess timeout is 30 s.

### Risk levels

| Level | Score range |
|-------|------------|
| Low | 0 -- 19 |
| Medium | 20 -- 39 |
| High | 40 -- 69 |
| Critical | 70 -- 100 |

---

## CVE Impact Search

### GET /api/v1/cve/{cveID}

**Permission:** `fleet.read`

Search for a specific CVE across the fleet. Returns all affected hosts with package details from security scan traits.

**Response (200 OK):**

```json
{
  "cve_id": "CVE-2024-1234",
  "affected_hosts": [
    {
      "hostname": "web-01.example.com",
      "envoy_id": "a1b2c3d4",
      "package": "openssl",
      "installed": "3.0.2",
      "fixed": "3.0.14",
      "severity": "high",
      "scanner": "trivy"
    }
  ],
  "total_hosts": 1
}
```

---

## Compliance Reports

Framework-specific compliance reports in JSON and printable HTML formats. Each framework has a JSON endpoint and a self-contained, print-friendly HTML endpoint.

| Method | Path | Framework |
|--------|------|-----------|
| GET | `/api/v1/report/compliance` | HIPAA |
| GET | `/api/v1/report/compliance.html` | HIPAA (HTML) |
| GET | `/api/v1/report/hitrust` | HITRUST CSF |
| GET | `/api/v1/report/hitrust.html` | HITRUST CSF (HTML) |
| GET | `/api/v1/report/soc2` | SOC 2 Trust Services Criteria |
| GET | `/api/v1/report/soc2.html` | SOC 2 (HTML) |
| GET | `/api/v1/report/pci` | PCI DSS v4.0 |
| GET | `/api/v1/report/pci.html` | PCI DSS v4.0 (HTML) |
| GET | `/api/v1/report/cis` | CIS Benchmarks |
| GET | `/api/v1/report/cis.html` | CIS Benchmarks (HTML) |
| GET | `/api/v1/report/nist` | NIST 800-53 |
| GET | `/api/v1/report/nist.html` | NIST 800-53 (HTML) |
| GET | `/api/v1/report/iso27001` | ISO 27001 |
| GET | `/api/v1/report/iso27001.html` | ISO 27001 (HTML) |
| GET | `/api/v1/report/gdpr` | GDPR |
| GET | `/api/v1/report/gdpr.html` | GDPR (HTML) |
| GET | `/api/v1/report/nerccip` | NERC CIP |
| GET | `/api/v1/report/nerccip.html` | NERC CIP (HTML) |
| GET | `/api/v1/report/cyberessentials` | Cyber Essentials+ |

| GET | `/api/v1/report/cyberessentials.html` | Cyber Essentials+ (HTML) |
| GET | `/api/v1/report/iec62443` | IEC 62443 Industrial Cybersecurity |
| GET | `/api/v1/report/iec62443.html` | IEC 62443 (HTML) |
| GET | `/api/v1/report/executive` | Executive summary (cross-framework) |
| GET | `/api/v1/report/executive.html` | Executive summary (HTML) |

**JSON response (200 OK):**

```json
{
  "title": "SOC 2 Trust Services Criteria Report",
  "framework": "SOC 2",
  "generated_at": "2026-03-23T14:00:00Z",
  "findings": [
    {
      "control_id": "CC6.1",
      "title": "Logical and Physical Access Controls",
      "status": "satisfied",
      "scope": "enforced",
      "evidence": "...",
      "fleet_percent": 100.0,
      "fleet_covered": 5,
      "fleet_total": 5,
      "doc_count": 0
    }
  ],
  "summary": {
    "satisfied": 10,
    "partial": 2,
    "not_satisfied": 1,
    "not_applicable": 0
  }
}
```

**HTML response:** Self-contained page with `Content-Type: text/html`, suitable for printing or saving as PDF.

## Additional Endpoints

These endpoints are documented at summary level; `curl -v` the running server for full request/response schemas, or consult the handler registrations in `server/api/` and `server/bootstrap/`. If you end up relying on one of these, file a doc-pass to promote it to a full section above.

### Config

- **GET /api/v1/config/dedup** — list configs whose contents deduplicate against other paths (operational hygiene).
- **GET /api/v1/config/unused** — list published configs never matched by any envoy in the retention window.

### Envoys

- **GET /api/v1/envoys/{id}/convergence** — per-envoy convergence history (status transitions over time).

### Inventory

- **GET /api/v1/inventory/keys** — list all inventory keys known across the fleet (for CMDB joins).

### Compliance reports (framework extensions)

Same shape as the existing framework report endpoints: JSON at the bare path, HTML at `.html`.

- **GET /api/v1/report/abacyber** and **GET /api/v1/report/abacyber.html** — ABA Cybersecurity compliance report.
- **GET /api/v1/report/cisrhel** and **GET /api/v1/report/cisrhel.html** — CIS RHEL benchmark report.
- **GET /api/v1/report/cisubuntu** and **GET /api/v1/report/cisubuntu.html** — CIS Ubuntu benchmark report.
- **GET /api/v1/report/nydfscyber** and **GET /api/v1/report/nydfscyber.html** — NY DFS Cybersecurity (23 NYCRR 500) report.

### Swarm (substrate)

Substrate-level overview of the envoy P2P network. Content-subsystem operations live under their own prefixes; these endpoints describe the transport layer those subsystems ride on.

- **GET /api/v1/swarm/status** — fleet-wide totals (envoys online, substrate-active, seeders, manifest counts, footprint). _(fleet.read)_
- **GET /api/v1/swarm/peers** — per-envoy rollup with `?sort=footprint|blobs|hostname` and `?limit=N` (0 for unbounded). _(fleet.read)_
- **POST /api/v1/swarm/cleanup** — dispatch the hidden `vigo swarm _apply-cleanup` verb to envoys matching `?target=<pat>` (default `*`). Each envoy removes dangling `files/<label>` symlinks and `.partial/` dirs whose final blob has been assembled. Idempotent. Returns `{target, dispatched, task_run_id}` — pass `task_run_id` to `vigocli task status` for per-envoy detail. _(admin)_

### Payload

Administrator-pushed file distribution on the swarm substrate. These endpoints power `vigocli swarm payload`.

- **GET /api/v1/swarm/payload/list** — list all active payload payloads. _(fleet.read)_
- **GET /api/v1/swarm/payload/match** — preview which envoys match a given target expression. _(fleet.read)_
- **GET /api/v1/swarm/payload/status/{sha256}** — per-envoy distribution status for one payload. _(fleet.read)_
- **GET /api/v1/swarm/payload/broken** — payloads with unreachable peers or failed distributions. _(fleet.read)_
- **POST /api/v1/swarm/payload/distribute** — seed a new payload into the swarm for distribution. _(admin)_ Returns **403** when `swarm.enabled: false` or `swarm.payload.enabled: false`; denials are recorded as `swarm.payload.denied` in the [tamper-evident audit chain](../../compliance/tamper-evident-audit.md).
- **DELETE /api/v1/swarm/payload/{sha256}** — revoke a payload; agents evict on next check-in. _(admin)_ Allowed when the substrate is disabled (cleanup must work on a halted substrate).

### Gitmirror

- **GET /api/v1/swarm/gitmirror/fleet** — fleet-wide gitmirror project view (aggregated from per-envoy traits). _(fleet.read)_
- **GET /api/v1/swarm/gitmirror/projects/{project_id}/roster** — canonical member fleet list for one project. Returns `{project_id, handle, founder_puddle_pubkey, members[], dr_fanout, member_fleet_count}`. The helper daemon's lazy-fetch target at push time. 404 if no envoy in the fleet has reported the project. _(fleet.read)_

### Filesync

- **GET /api/v1/swarm/filesync/fleet** — fleet-wide filesync mesh view (per-user LAN plaintext sync state aggregated from every envoy's `filesync` trait).
- **GET /api/v1/swarm/filesync/envoy/{id}/users/{user}/files** — per-(envoy, user) file list drilldown for the `/filesync` page.

### Cryptsync

- **GET /api/v1/swarm/cryptsync/fleet** — fleet-wide cryptsync mesh view (per-user encrypted sync state aggregated from every envoy's `cryptsync` trait).
- **GET /api/v1/swarm/cryptsync/envoy/{id}/users/{user}/files** — per-(envoy, user) file list drilldown for the `/cryptsync` page.

### Scrier

- **GET /api/v1/scrier/users** — list the Scrier OS-user mapping table.
- **PUT /api/v1/scrier/users/{username}/osuser** — set the mapping for one admin username.

### Sandgorgon (bare-metal lifecycle)

Server-side counterparts to `vigocli sandgorgon` — see the [CLI reference](../cli/sandgorgon.md) for verb-level docs.

- **GET /api/v1/sandgorgon/assets** — list all managed hardware assets. _(fleet.read)_
- **GET /api/v1/sandgorgon/assets/search** — search assets by substring / tag / state. _(fleet.read)_
- **GET /api/v1/sandgorgon/assets/{id}** — one asset's full record. _(fleet.read)_
- **GET /api/v1/sandgorgon/boot/{id}** — PXE boot payload for one asset. _(fleet.read)_
- **GET /api/v1/sandgorgon/preseed/{id}** — preseed file served to installer. _(fleet.read)_
- **GET /api/v1/sandgorgon/dhcp-config** — DHCP snippet for PXE routing. _(fleet.read)_
- **GET /api/v1/sandgorgon/events/{assetID}** — lifecycle-event stream for one asset. _(fleet.read)_
- **GET /api/v1/sandgorgon/images** — list registered OS images. _(fleet.read)_
- **POST /api/v1/sandgorgon/images** — register a new OS image. _(admin)_
- **DELETE /api/v1/sandgorgon/images/{id}** — deregister an OS image (file not deleted). _(admin)_
- **GET /api/v1/sandgorgon/report/{id}** / **/api/v1/sandgorgon/report/{id}/html** — full lifecycle report. _(fleet.read)_
- **GET /api/v1/sandgorgon/certificates** — list all NIST 800-88 sanitization certificates. _(fleet.read)_
- **GET /api/v1/sandgorgon/certificates/{id}** / **/api/v1/sandgorgon/certificates/{id}/html** — one certificate (JSON or printable HTML). _(fleet.read)_
- **POST /api/v1/sandgorgon/commission** — register a new asset for lifecycle management. _(admin)_
- **POST /api/v1/sandgorgon/decommission/{id}** — decommission: revoke cert, sanitize disks, generate NIST 800-88 certificate. _(admin)_
- **POST /api/v1/sandgorgon/provision/{id}** — PXE boot the asset with a registered OS image. _(admin)_
- **POST /api/v1/sandgorgon/import** — import assets from CSV / NetBox / Snipe-IT. _(admin)_
- **POST /api/v1/sandgorgon/return-to-available/{id}** — return a retired asset to the available pool. _(admin)_

PXE-serving GETs (boot, preseed, dhcp-config) gate on `fleet.read` even though a PXE-booting machine itself can't satisfy any web-auth — those endpoints either ride a separate delivery path or hold operator-side state used to write the actual PXE config off-box. The fleet.read gate tightens authenticated-user access without changing PXE flow; revisit when the delivery path is finalized.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
