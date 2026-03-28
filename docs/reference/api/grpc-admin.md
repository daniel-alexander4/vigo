
# gRPC Admin Service

The `VigoAdmin` service provides administrative operations. It runs on port 8443 via grpc-gateway, which translates REST requests into gRPC calls. The CLI (`vigogo`) and web UI communicate through these endpoints.

Proto definition: `proto/vigo.proto`, service `VigoAdmin`.

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
| `used_by` | string | Envoy UUID that consumed it |

---

### Envoy Management

| RPC | Method | Path | Description |
|-----|--------|------|-------------|
| `ListEnvoys` | GET | `/api/v1/envoys` | List all enrolled envoys |
| `GetEnvoy` | GET | `/api/v1/envoys/{id}` | Get details for a single envoy |
| `RevokeEnvoy` | POST | `/api/v1/envoys/{id}/revoke` | Revoke a envoy, blocking future check-ins |
| `DeleteEnvoy` | DELETE | `/api/v1/envoys/{id}` | Permanently remove a envoy and all history |
| `ForcePush` | POST | `/api/v1/push` | Trigger immediate check-in for one or all envoys |
| `DryRun` | POST | `/api/v1/envoys/{envoy_id}/dry-run` | Compute a plan without applying changes |


---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*

---
#### Envoy Response

| Field | Type | Description |
|-------|------|-------------|
| `id` | string | UUID |
| `hostname` | string | Envoy hostname |
| `ip_address` | string | IP address |
| `enrolled_at` | Timestamp | Enrollment time |
| `last_seen` | Timestamp | Last check-in time |
| `revoked` | bool | Whether the envoy is revoked |
| `revoked_at` | Timestamp | Revocation time (if revoked) |
| `pending_rotated_secrets` | repeated string | Secrets awaiting rotation on this envoy |
| `agent_version` | string | Agent binary version reported on last check-in |
| `tags` | string | Comma-separated user-assigned tags |
| `maintenance_until` | Timestamp | End of maintenance window (empty if not in maintenance) |

#### ForcePush

| Field | Type | Description |
|-------|------|-------------|
| `envoy_id` | string | Target envoy UUID (empty = all envoys) |

Response: `{ envoys_notified: int32 }`

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
| `ListRuns` | GET | `/api/v1/runs` | List recent runs, optionally filtered by envoy |
| `GetRun` | GET | `/api/v1/runs/{id}` | Get run details with per-module results |

#### Run Response

| Field | Type | Description |
|-------|------|-------------|
| `id` | string | Run UUID |
| `envoy_id` | string | Envoy UUID |
| `started_at` | Timestamp | Run start time |
| `finished_at` | Timestamp | Run end time |
| `status` | string | running, success, failure, partial |
| `module_count` | int32 | Total modules in run |
| `changed_count` | int32 | Modules that made changes |
| `failed_count` | int32 | Modules that failed |
| `skipped_count` | int32 | Modules that were skipped |

---


---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*

---
### Convergence

| RPC | Method | Path | Description |
|-----|--------|------|-------------|
| `GetConvergenceSummary` | GET | `/api/v1/convergence` | Fleet-wide convergence status |
| `GetEnvoyConvergence` | GET | `/api/v1/envoys/{id}/convergence` | Single-envoy convergence with history |

#### ConvergenceSummary Response

| Field | Type | Description |
|-------|------|-------------|
| `total` | int32 | Total enrolled envoys |
| `converged` | int32 | Fully converged |
| `recurring_drift` | int32 | 3+ consecutive runs with changes |
| `errors` | int32 | Last run failed |
| `lost_contact` | int32 | Not seen within 2x interval |
| `no_data` | int32 | No run data |
| `convergence_pct` | float | Percentage converged |

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
| `Rollback` | POST | `/api/v1/envoys/{envoy_id}/rollback` | Revert to a previous run's state |

| Field | Type | Description |
|-------|------|-------------|
| `envoy_id` | string | Target envoy |
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

### Spanner (Internal)

These RPCs are used for hub-to-spoke communication in spanner deployments:

| RPC | Description |
|-----|-------------|
| `EnrollEnvoy` | Hub routes enrollment to target spoke (bypasses token re-validation) |
| `SetReassignment` | Hub marks a envoy for reassignment to a different spoke |
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
