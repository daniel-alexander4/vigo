# gRPC Admin Service

The `HobnobAdmin` service provides administrative operations. It runs on port 8181 via grpc-gateway, which translates REST requests into gRPC calls. The CLI (`hobnobcli`) and web UI communicate through these endpoints.

Proto definition: `proto/hobnob.proto`, service `HobnobAdmin`.

## RPCs

### Enrollment

| RPC | Method | Path | Description |
|-----|--------|------|-------------|
| `AddApprovedPattern` | POST | `/api/v1/approved` | Add a hostname/IP wildcard pattern with a one-time token |
| `ListApprovedPatterns` | GET | `/api/v1/approved` | List all approved enrollment patterns |
| `RevokeApprovedPattern` | DELETE | `/api/v1/approved/{id}` | Remove an approved pattern |
| `GenerateToken` | POST | `/api/v1/tokens` | Generate a cryptographically random one-time enrollment token |

#### AddApprovedPattern

**Request:**

| Field | Type | Description |
|-------|------|-------------|
| `pattern` | string | Hostname/IP glob pattern (e.g., `*.web.example.com`) |
| `token` | string | Plaintext enrollment token (server bcrypt-hashes it at rest) |

**Response:**

| Field | Type | Description |
|-------|------|-------------|
| `id` | string | Pattern UUID |
| `pattern` | string | The stored pattern |
| `created_at` | Timestamp | Creation time |

#### ListApprovedPatterns

Returns all patterns with usage status:

| Field | Type | Description |
|-------|------|-------------|
| `id` | string | Pattern UUID |
| `pattern` | string | The glob pattern |
| `created_at` | Timestamp | Creation time |
| `used` | bool | Whether the pattern has been consumed |
| `used_by` | string | Thrall UUID that consumed it |

---

### Thrall Management

| RPC | Method | Path | Description |
|-----|--------|------|-------------|
| `ListThralls` | GET | `/api/v1/thralls` | List all enrolled thralls |
| `GetThrall` | GET | `/api/v1/thralls/{id}` | Get details for a single thrall |
| `RevokeThrall` | POST | `/api/v1/thralls/{id}/revoke` | Revoke a thrall, blocking future check-ins |
| `DeleteThrall` | DELETE | `/api/v1/thralls/{id}` | Permanently remove a thrall and all history |
| `ForcePush` | POST | `/api/v1/push` | Trigger immediate check-in for one or all thralls |
| `DryRun` | POST | `/api/v1/thralls/{thrall_id}/dry-run` | Compute a plan without applying changes |

#### Thrall Response

| Field | Type | Description |
|-------|------|-------------|
| `id` | string | UUID |
| `hostname` | string | Thrall hostname |
| `ip_address` | string | IP address |
| `enrolled_at` | Timestamp | Enrollment time |
| `last_seen` | Timestamp | Last check-in time |
| `revoked` | bool | Whether the thrall is revoked |
| `revoked_at` | Timestamp | Revocation time (if revoked) |
| `pending_rotated_secrets` | repeated string | Secrets awaiting rotation on this thrall |

#### ForcePush

| Field | Type | Description |
|-------|------|-------------|
| `thrall_id` | string | Target thrall UUID (empty = all thralls) |

Response: `{ thralls_notified: int32 }`

#### DryRun Response

Returns a list of `PlannedAction`:

| Field | Type | Description |
|-------|------|-------------|
| `module_name` | string | Module name |
| `resource_type` | string | Resource type |
| `resource_name` | string | Resource name |
| `planned_action` | string | create, update, delete, or none |

---

### Run History

| RPC | Method | Path | Description |
|-----|--------|------|-------------|
| `ListRuns` | GET | `/api/v1/runs` | List recent runs, optionally filtered by thrall |
| `GetRun` | GET | `/api/v1/runs/{id}` | Get run details with per-module results |

#### Run Response

| Field | Type | Description |
|-------|------|-------------|
| `id` | string | Run UUID |
| `thrall_id` | string | Thrall UUID |
| `started_at` | Timestamp | Run start time |
| `finished_at` | Timestamp | Run end time |
| `status` | string | running, success, failure, partial |
| `module_count` | int32 | Total modules in run |
| `changed_count` | int32 | Modules that made changes |
| `failed_count` | int32 | Modules that failed |
| `skipped_count` | int32 | Modules that were skipped |

---

### Compliance

| RPC | Method | Path | Description |
|-----|--------|------|-------------|
| `GetComplianceSummary` | GET | `/api/v1/compliance` | Fleet-wide compliance status |
| `GetThrallCompliance` | GET | `/api/v1/thralls/{id}/compliance` | Single-thrall compliance with history |

#### ComplianceSummary Response

| Field | Type | Description |
|-------|------|-------------|
| `total` | int32 | Total enrolled thralls |
| `compliant` | int32 | Fully compliant |
| `drifted` | int32 | Converged with changes |
| `failing` | int32 | Last run failed |
| `stale` | int32 | Not seen within threshold |
| `unknown` | int32 | No run data |
| `compliance_pct` | float | Percentage compliant |

---

### Webhooks

| RPC | Method | Path | Description |
|-----|--------|------|-------------|
| `AddWebhook` | POST | `/api/v1/webhooks` | Register a webhook endpoint |
| `ListWebhooks` | GET | `/api/v1/webhooks` | List all webhooks |
| `DeleteWebhook` | DELETE | `/api/v1/webhooks/{id}` | Remove a webhook |

See [Webhooks](webhooks.md) for payload format and signing details.

---

### Rollback

| RPC | Method | Path | Description |
|-----|--------|------|-------------|
| `Rollback` | POST | `/api/v1/thralls/{thrall_id}/rollback` | Revert to a previous run's state |

| Field | Type | Description |
|-------|------|-------------|
| `thrall_id` | string | Target thrall |
| `run_id` | string | Optional: specific run to roll back to |
| `file_path` | string | Optional: rollback a single file |

---

### Package Audit

| RPC | Method | Path | Description |
|-----|--------|------|-------------|
| `ListPackages` | GET | `/api/v1/packages` | List package audit records |
| `ListPackageDrift` | GET | `/api/v1/packages/drift` | List packages with version drift |

---

### Environment Promotion

| RPC | Method | Path | Description |
|-----|--------|------|-------------|
| `PromoteEnv` | POST | `/api/v1/env/promote` | Promote env variables between tiers |

| Field | Type | Description |
|-------|------|-------------|
| `module_name` | string | Module to promote |
| `from_env` | string | Source environment |
| `to_env` | string | Target environment |

---

### Federation (Internal)

These RPCs are used for hub-to-spoke communication in federated deployments:

| RPC | Description |
|-----|-------------|
| `EnrollThrall` | Hub routes enrollment to target spoke (bypasses token re-validation) |
| `SetReassignment` | Hub marks a thrall for reassignment to a different spoke |
