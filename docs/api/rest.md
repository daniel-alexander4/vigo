# REST API

In addition to the grpc-gateway proxy (which exposes all `HobnobAdmin` RPCs under `/api/v1/`), the server mounts several direct REST handlers. These endpoints are not proxied through grpc-gateway and exist for features that benefit from direct HTTP handling.

All endpoints return JSON and accept JSON request bodies. Errors use the format `{"message": "..."}`.

## Tasks

Ad-hoc command dispatch to connected agents.

### POST /api/v1/tasks

Dispatch a command to matching thralls.

**Request body:**

```json
{
  "command": "uptime",
  "target": "*.web.*",
  "operator": "admin",
  "timeout": 30,
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
| `batchSize` | No | Rolling execution batch size (e.g., `25%`, `5`) |
| `maxFailures` | No | Abort after this many failures (e.g., `3`, `10%`) |
| `healthCheck` | No | Command to run between batches |
| `dryRun` | No | If true, return matched targets without executing |
| `taskName` | No | Name of a reusable task definition from `tasks/*.yaml` |
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

### GET /api/v1/tasks

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

### GET /api/v1/tasks/{id}

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
    "thrall": "web-01.example.com"
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
  "tools_used": ["get_thrall", "list_runs"],
  "tokens_used": 1234
}
```

**Response (SSE, if `Accept: text/event-stream`):**

Events are sent as server-sent events with JSON payloads:

- `{"type": "status", "status": "thinking", "tools_called": ["get_thrall"]}`
- `{"type": "delta", "text": "The thrall is..."}`
- `{"type": "done", "tools_used": ["get_thrall"], "tokens_used": 1234}`

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
    {"name": "license", "status": "pass", "message": "compliant, 12/25 thralls"},
    {"name": "federation", "status": "skip", "message": "standalone"},
    {"name": "smtp", "status": "skip", "message": "not configured"},
    {"name": "config", "status": "pass", "message": "15 modules, 8 entries"},
    {"name": "stale_thralls", "status": "pass"}
  ]
}
```

Check statuses: `pass`, `warn`, `fail`, `skip`. Overall status: `healthy`, `degraded`, `unhealthy`.

---

## License

### GET /api/v1/license

Returns current license status including stage, tier, thrall counts, and expiration.

**Response:**

```json
{
  "stage": 0,
  "license": {
    "customer_name": "Acme Corp",
    "customer_id": "acme",
    "license_id": "lic-20260313-acme",
    "tier": "enterprise",
    "max_thralls": 500,
    "issued_at": "2026-03-13",
    "expires_at": "2027-03-13",
    "version": 1
  },
  "active_thralls": 12,
  "max_thralls": 500,
  "expires_at": "2027-03-13",
  "days_remaining": 365
}
```

The `tier` field is `"free"`, `"standard"`, `"growth"`, or `"enterprise"`. Omitted for legacy licenses without a tier (treated as free).

---

## Traits

### GET /api/v1/thralls/{hostname}/traits

Returns the latest collected traits for a thrall by hostname.

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

Query thralls by trait values.

**Query parameters:**

| Parameter | Description |
|-----------|-------------|
| `where` | Repeatable. Each is `dot.path=value` (e.g., `where=os.distro=ubuntu`) |
| `show` | Comma-separated dot-paths to include in output (default: all traits) |

**Response:**

```json
{
  "count": 5,
  "thralls": [
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

## Federation

### GET /api/v1/federation/status

Returns federation role and spoke statuses (hub mode only).

### GET /api/v1/federation/spokes/{id}

Returns detailed status for a single spoke.

### GET /api/v1/federation/spokes/{id}/thralls

List thralls on a specific spoke.

### GET /api/v1/federation/spokes/{id}/runs

List recent runs on a specific spoke.

### POST /api/v1/federation/spokes/{id}/push

Force-push to thralls on a specific spoke.

### POST /api/v1/federation/reassign

Reassign a thrall from one spoke to another.

**Request body:**

```json
{
  "thrall_id": "...",
  "from_spoke_id": "spoke-east",
  "to_spoke_id": "spoke-west"
}
```

### POST /api/v1/federation/spokes/{id}/drain

Drain all thralls from a spoke (move to target spoke or auto-route by patterns).

---

## Config

### GET /api/v1/config/trace

Trace config resolution for a hostname. Shows which node entry matched, how roles expanded (including includes), where each variable came from, and the final module list.

**Query parameters:**

| Parameter | Required | Description |
|-----------|----------|-------------|
| `hostname` | Yes | The hostname to trace |

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
  "modules": ["firewall", "sshd", "nginx", "logrotate"],
  "var_sources": [
    {"key": "nginx_port", "value": 443, "source": "env_vars[production]"},
    {"key": "log_level", "value": "info", "source": "node vars"}
  ],
  "conditional_vars": ["is_debian", "has_enough_ram"],
  "resource_count": 14
}
```

If no entry matches, `matched` is `false` and other fields are omitted.

---

## Other Endpoints

| Method | Path | Description |
|--------|------|-------------|
| GET | `/metrics` | Prometheus metrics (see [Metrics](../operations/metrics.md)) |
| GET | `/healthz` | Health check (spoke mode only) |
| GET | `/bootstrap` | Agent bootstrap script |
| GET | `/bootstrap/agent/{os}/{arch}` | Agent binary download |
| GET | `/bootstrap/ca.pem` | CA certificate download |

---

## Export

Compliance evidence endpoints for integration with SIEM, CMDB, and OSCAL tools. Each endpoint must be individually enabled in `server.yaml` under the `export:` section. Disabled endpoints return 404.

### GET /api/v1/export/siem

Returns a JSON array of SIEM events derived from run results.

**Query parameters:**

| Parameter | Default | Description |
|-----------|---------|-------------|
| `since` | 24 hours ago | RFC 3339 timestamp. Only events after this time are returned |
| `thrall` | (all) | Filter by hostname |
| `limit` | 1000 | Maximum events to return (max 10000) |

**Response:**

```json
[
  {
    "timestamp": "2026-03-13T10:05:00Z",
    "hostname": "web-01.example.com",
    "thrall_id": "a1b2c3d4",
    "run_id": "run-5678",
    "module_name": "nginx",
    "resource_name": "nginx.conf",
    "resource_type": "file",
    "action": "updated",
    "changed": true,
    "duration_ms": 42,
    "run_status": "compliant",
    "source": "hobnob"
  }
]
```

| Field | Type | Description |
|-------|------|-------------|
| `timestamp` | string | RFC 3339 time of the resource action |
| `hostname` | string | Thrall hostname |
| `thrall_id` | string | Thrall UUID |
| `run_id` | string | Convergence run ID |
| `module_name` | string | Module that owns the resource |
| `resource_name` | string | Resource name within the module |
| `resource_type` | string | Resource type (file, package, service, etc.) |
| `action` | string | Action taken (created, updated, removed, noop, failed) |
| `changed` | bool | Whether the resource was modified |
| `error` | string | Error message (omitted when empty) |
| `duration_ms` | int | Time spent on this resource in milliseconds |
| `run_status` | string | Overall run status (compliant, drifted, failing) |
| `source` | string | Always `"hobnob"` |

### GET /api/v1/export/cmdb

Returns a JSON array of CMDB records from the FleetIndex (zero DB queries).

**Query parameters:**

| Parameter | Default | Description |
|-----------|---------|-------------|
| `status` | (all) | Filter by compliance status: `compliant`, `drifted`, `failing`, `stale` |
| `environment` | (all) | Filter by environment (e.g., `prod`, `staging`) |

**Response:**

```json
[
  {
    "hostname": "web-01.example.com",
    "ip": "10.0.1.10",
    "thrall_id": "a1b2c3d4",
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
| `hostname` | string | Thrall hostname |
| `ip` | string | IP address |
| `thrall_id` | string | Thrall UUID |
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

### GET /api/v1/export/oscal

Returns a minimal valid OSCAL Assessment Results JSON envelope (v1.1.2). The `findings` array is currently empty; future versions will map Hobnob compliance data to OSCAL control findings.

**Response:**

```json
{
  "assessment-results": {
    "uuid": "...",
    "metadata": {
      "title": "Hobnob Assessment Results",
      "last-modified": "2026-03-13T10:00:00Z",
      "version": "1.0.0",
      "oscal-version": "1.1.2"
    },
    "results": [
      {
        "uuid": "...",
        "title": "Hobnob Compliance Assessment",
        "start": "2026-03-13T10:00:00Z",
        "findings": [],
        "remarks": "Stub: OSCAL findings mapping is not yet implemented."
      }
    ]
  }
}
```
