
# gRPC Agent Service

The `VigoAgent` service handles all communication between the Rust agent and the server. It runs on port 1530 over mTLS. Every unary RPC includes an ED25519 signature in the request, verified by the server against the agent's stored public key.

Proto definition: `protobuf/vigo.proto`, service `VigoAgent`.

## RPCs

### Register

Enrolls a new agent with the server.

**Request: `RegisterRequest`**

| Field | Type | Description |
|-------|------|-------------|
| `hostname` | string | Agent's hostname |
| `ip_address` | string | Agent's IP address |
| `pubkey_pem` | string | ED25519 public key in PEM format |
| `token` | string | One-time enrollment token (plaintext; server validates against bcrypt hash) |
| `force` | bool | If true, revoke any existing active envoy with the same hostname and re-enroll |

**Response: `RegisterResponse`**

| Field | Type | Description |
|-------|------|-------------|
| `success` | bool | Whether enrollment succeeded |
| `envoy_id` | string | UUID assigned by the server |
| `message` | string | Human-readable status message |
| `spoke_addr` | string | If set, the agent should connect to this spoke instead |
| `server_pubkey_pem` | string | Server's ED25519 public key for bundle signature verification |

**Auth:** Token-based. The hostname must match an approved pattern, and the token must match the bcrypt hash stored for that pattern.

**Hostname uniqueness:** Registration is rejected if an active (non-revoked) envoy with the same hostname already exists. Set `force: true` to revoke the old envoy and re-enroll. This also overrides the ENRL-07 revoked-hostname block.

---

### CheckIn

The agent's pull loop. Returns the modules to apply based on current state.

**Request: `CheckInRequest`**

| Field | Type | Description |
|-------|------|-------------|
| `envoy_id` | string | Agent's UUID |
| `state_fingerprint` | string | Merkle root from the last response (enables per-envoy no-change detection) |
| `signature` | bytes | ED25519 signature |
| `timestamp` | Timestamp | Request timestamp (must be within signature window) |
| `traits_json` | bytes | Inline traits JSON (avoids separate ReportTraits call) |
| `policy_version` | uint64 | Agent's cached policy version (0 = no cache) |
| `policy_mode` | bool | True if agent supports policy bundles |
| `cached_module_hashes` | map<string, string> | Module name to content hash for delta transfer |
| `capabilities` | StreamCapabilities | Agent protocol capabilities (delta streaming) |
| `agent_version` | string | Agent binary version (e.g. "0.18.8") |
| `ip_address` | string | Agent's preferred outbound IP (refreshed each check-in) |

**Response: `CheckInResponse`**

| Field | Type | Description |

|-------|------|-------------|
| `modules` | repeated Module | Ordered module list with desired state |
| `interval` | string | Check-in interval (e.g., "5m") |
| `jitter_percent` | int32 | Jitter percentage for interval randomization |
| `custom_traits` | repeated CustomTraitEntry | Custom trait executables to sync |
| `no_change` | bool | True if fingerprint matched; agent can skip execution |
| `response_fingerprint` | string | Merkle root of the envoy's resolved config (sent back as `state_fingerprint`) |
| `policy_bundle` | PolicyBundle | Raw policy for agent-side evaluation |
| `policy_version` | uint64 | Current config version |
| `reassign_addr` | string | Non-empty means agent should reconnect to this address |
| `reassign_id` | string | New UUID on the target spoke |
| `stream_requested` | bool | Server wants the agent to open an AgentStream for dispatched work |

**Auth:** Signature-verified. Revoked agents receive an error.

---

### ReportResult

Submits per-module execution results after a convergence run.

**Request: `ReportResultRequest`**

| Field | Type | Description |
|-------|------|-------------|
| `run_id` | string | Run UUID |
| `envoy_id` | string | Agent's UUID |
| `results` | repeated ModuleResult | Per-resource outcomes |
| `signature` | bytes | ED25519 signature |
| `timestamp` | Timestamp | Request timestamp |

Each `ModuleResult` contains:

| Field | Type | Description |
|-------|------|-------------|
| `module_name` | string | Module that owns this resource |
| `resource_type` | string | Resource type (file, package, service, etc.) |
| `resource_name` | string | Resource name |
| `action_taken` | string | none, created, updated, deleted, blocked |
| `changed` | bool | Whether a change was made |
| `error_message` | string | Error detail if the resource failed |
| `duration_ms` | int32 | Execution duration in milliseconds |

**Response: `ReportResultResponse`**

| Field | Type | Description |
|-------|------|-------------|
| `acknowledged` | bool | Always true on success |

---

### ReportTraits

Submits collected traits (facts) from the agent.

**Request: `ReportTraitsRequest`**

| Field | Type | Description |
|-------|------|-------------|
| `envoy_id` | string | Agent's UUID |

| `traits_json` | bytes | JSON-encoded traits map |
| `signature` | bytes | ED25519 signature |
| `timestamp` | Timestamp | Request timestamp |

**Response: `ReportTraitsResponse`**

| Field | Type | Description |
|-------|------|-------------|
| `acknowledged` | bool | Always true on success |

---

### DownloadCustomTrait

Retrieves a custom trait executable by name.

**Request: `DownloadCustomTraitRequest`**

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Trait name (matches CustomTraitEntry.name) |

**Response: `DownloadCustomTraitResponse`**

| Field | Type | Description |
|-------|------|-------------|
| `data` | bytes | Raw executable bytes |

---

### AgentStream

Opens a bidirectional stream between agent and server. Agents default to unary polling and only open a stream when the server sets `stream_requested` in a CheckInResponse (adaptive stream promotion). The stream is used for task/query dispatch and policy pushes.

**Agent sends (`AgentStreamMessage`):**

| Variant | Description |
|---------|-------------|
| `check_in` | CheckInRequest on stream open |
| `report_result` | Run results |
| `report_traits` | Trait updates |
| `task_result` | Ad-hoc task execution results |
| `task_ack` | Acknowledgement of task dispatch |
| `query_response` | Response to live trait queries |
| `delta` | DeltaEvent (lightweight state change report) |

**Server sends (`ServerStreamMessage`):**

| Variant | Description |
|---------|-------------|
| `check_in_response` | Initial policy response |
| `report_result_ack` | Acknowledgement of run results |
| `report_traits_ack` | Acknowledgement of trait updates |
| `policy_push` | New policy bundle on config change |
| `force_push` | Signal to run convergence immediately |
| `task_dispatch` | Ad-hoc command to execute |
| `query_request` | Live trait query request |
| `delta_ack` | Acknowledgement of delta events |
| `stream_release` | Server tells agent it may close the promoted stream (all dispatched work complete) |


## Delta Streaming Protocol

When the agent advertises `supports_delta: true` in its `StreamCapabilities`, the server accepts `DeltaEvent` messages. These are lightweight alternatives to full `ReportResult` messages:

- **RunCompleted**: Reports run outcome with only changed/failed resources (no-ops omitted)
- **TraitsDelta**: Reports changed/removed traits (reserved for future use)
- **HeartbeatDelta**: No-op keepalive when there is nothing to report

Each `DeltaEvent` carries a monotonic sequence number. The server responds with a `DeltaAck` confirming the sequence.

## Policy Bundles

When `policy_mode: true`, the server returns a `PolicyBundle` containing:

- Raw policy modules with unevaluated templates and `when:` expressions
- Merged vars (secrets already resolved)
- Conditional variable definitions
- Merkle root over modules and vars (for per-envoy no-change detection)
- ED25519 signature covering content fields + Merkle root
- `max_age_seconds` TTL for offline operation

The agent evaluates templates and conditionals locally using its own traits, enabling offline convergence when the server is unreachable.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
