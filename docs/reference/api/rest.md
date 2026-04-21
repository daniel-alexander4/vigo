
# REST API

In addition to the grpc-gateway proxy (which exposes all `VigoAdmin` RPCs under `/api/v1/`), the server mounts several direct REST handlers. These endpoints are not proxied through grpc-gateway and exist for features that benefit from direct HTTP handling.

All endpoints return JSON and accept JSON request bodies. Errors use the format `{"message": "..."}`.

## Tasks

Ad-hoc command dispatch to connected agents.

### POST /api/v1/task

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
| `taskName` | No | Name of a reusable task definition from `stockpile/tasks/*.vgo` |
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

Cancel a specific task run. Sets the task and its pending/dispatched targets to `cancelled` status.

**Response:**

```json
{"status": "cancelled", "id": "abc123"}
```

### POST /api/v1/task/cancel-running

Cancel all tasks stuck in `running` or `pending` state. Also cancels their pending/dispatched targets.

**Response:**

```json
{"status": "cancelled", "count": 2}
```

---

## Workflows

Multi-step task orchestration with conditional branching.

### POST /api/v1/workflows

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

Get a workflow run with per-step results.

### POST /api/v1/workflows/{id}/abort

Abort a running workflow. Returns `{"status": "aborting"}`.

---

## Live Query

### POST /api/v1/query

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

Health check endpoint that validates server subsystems.

**Response:**

```json
{
  "status": "healthy",
  "checks": [
    {"name": "database", "status": "pass"},
    {"name": "tls", "status": "pass", "message": "expires 2027-03-13"},
    {"name": "secrets", "status": "pass", "message": "local"},
    {"name": "license", "status": "pass", "message": "compliant, 42/100 envoys"},
    {"name": "spanner", "status": "skip", "message": "standalone"},
    {"name": "smtp", "status": "skip", "message": "not configured"},
    {"name": "config", "status": "pass", "message": "15 modules, 8 entries"},
    {"name": "stale_envoys", "status": "pass"}
  ]
}
```

Check statuses: `pass`, `warn`, `fail`, `skip`. Overall status: `healthy`, `degraded`, `unhealthy`.

---

## License

### GET /api/v1/license

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

Returns spanner role and spoke statuses (hub mode only).

### GET /api/v1/spanner/spokes/{id}

Returns detailed status for a single spoke.

### GET /api/v1/spanner/spokes/{id}/envoys

List envoys on a specific spoke.

### GET /api/v1/spanner/spokes/{id}/runs

List recent runs on a specific spoke.

### POST /api/v1/spanner/spokes/{id}/push

Force-push to envoys on a specific spoke.

### POST /api/v1/spanner/reassign


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

Drain all envoys from a spoke (move to target spoke or auto-route by patterns).

---

## Config

### GET /api/v1/config/trace

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
    {"key": "nginx_port", "value": 443, "source": "environment_overrides:production"},
    {"key": "log_level", "value": "warn", "source": "inline"}
  ],
  "conditional_vars": ["is_debian", "has_enough_ram"],
  "resource_count": 14
}
```

When `?module=` is provided, the `inheritance`, `modules`, and `role_expansion` fields are filtered to only entries relevant to that module. Vars are kept unfiltered.

If no entry matches, `matched` is `false` and other fields are omitted.

### GET /api/v1/config/tree

Returns the directory inheritance structure for visualization. Shows `common.vgo` files at each level, leaf entries with their match patterns, and accumulated module totals.

**Response:** A tree of nodes with `dir`, `file`, `modules`, `roles`, `match`, `total`, `excluded`, and `children` fields.

### GET /api/v1/config/search

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

List recent config publish records.

**Query parameters:**

| Parameter | Default | Description |
|-----------|---------|-------------|
| `limit` | `20` | Maximum records (max 100) |

### POST /api/v1/config/publishes

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


Get details for a specific config publish, including the diff summary.

---

## Config Versions and Rollback

### GET /api/v1/config/versions

List available config snapshots for rollback.

**Response:**

```json
{
  "versions": ["20260313T143000Z", "20260312T091500Z"]
}
```

### POST /api/v1/config/rollback/{version}

Rollback to a previous config snapshot.

---

## Config Freeze

### POST /api/v1/config/freeze

Set a global config publish freeze.

**Request body:**

```json
{
  "freeze_until": "2026-03-14T06:00:00Z"
}
```

### POST /api/v1/config/unfreeze

Remove the config publish freeze.

### POST /api/v1/config/reload

Re-read all config files from disk. Called automatically by `vigocli config publish`.

### POST /api/v1/config/retract

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

Get a single config change, including the full diff summary.

### POST /api/v1/config/changes/{id}/approve

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

Set tags on a envoy.

**Request body:**

```json
{
  "tags": ["webserver", "production"]
}
```

### GET /api/v1/envoys/{id}/tags

Get tags for a envoy.

---

## Envoy Maintenance

### PUT /api/v1/envoys/{id}/maintenance

Set a maintenance window for a envoy.

**Request body:**

```json
{
  "until": "2026-03-14T06:00:00Z"
}
```

### DELETE /api/v1/envoys/{id}/maintenance

Clear the maintenance window.

---

## Envoy Compliance

### GET /api/v1/envoys/{id}/compliance

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

## Envoy Force Update

### POST /api/v1/envoys/{id}/update

Force an immediate check-in for a specific envoy. The envoy will receive the update at its next poll cycle.

### POST /api/v1/envoys/update

Force an immediate check-in for all envoys.

---

## Users

User account management. Requires admin authentication (except first-user bootstrap).

### GET /api/v1/users

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

Update a user account (role, password). Admin only.


### DELETE /api/v1/users/{id}

Delete a user account. Admin only. Cannot delete the last admin.

### POST /api/v1/users/totp/setup

Generate a TOTP secret for the authenticated user. Returns the `otpauth://` URL for QR code scanning. TOTP is not yet enabled until confirmed via `/totp/confirm`.

**Response (200 OK):**

```json
{
  "url": "otpauth://totp/Vigo:admin?secret=JBSWY3DPEHPK3PXP&issuer=Vigo"
}
```

### POST /api/v1/users/totp/confirm

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

List API tokens for the authenticated user. Token values are not returned.

### DELETE /api/v1/auth/tokens/{id}

Revoke an API token.

---

## Preview

### GET /api/v1/preview

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

Returns the OpenAPI specification as JSON.

---

## Health Check

### GET /healthz

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

Formal HTML cyber insurance attestation. Self-contained, suitable for direct viewing, emailing as an attachment, or printing. The document has a cover block with the organization name, a meta-data block (report ID, attestation period, preparer), an executive statement, five numbered sections (current posture, highest-risk assets, 90-day trend, fleet composition, methodology), and an attestation signatory block with four blank signature lines.

**Theme behavior:** the page follows the viewer's `prefers-color-scheme` on screen (dark background in dark browser sessions, light background in light sessions), and forces black-on-white when printed or saved as PDF via `@media print` — so the attachment-delivered version always looks like a formal black-ink document regardless of viewer preference.

**Query parameters:**

| Parameter | Default | Description |
|-----------|---------|-------------|
| `download` | — | If `1`, sets `Content-Disposition: attachment` for file download |

**Response:** HTML page with `Content-Type: text/html`.

### GET /api/v1/risk/insurance.pdf

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
      "evidence": "..."
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
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
