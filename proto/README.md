# Hobnob Protocol Buffers

Protocol Buffer definitions for Hobnob's gRPC services. Defines the agent-facing and admin-facing service contracts, all message types, and REST gateway annotations.

- **File:** `proto/hobnob.proto`
- **Package:** `hobnob.v1`
- **Go import:** `github.com/hobnob/hobnob/gen/proto/hobnob`
- **Two services:** HobnobAgent (gRPC-only), HobnobAdmin (gRPC + REST)
- **Generated code:** `gen/proto/hobnob/`

## Regenerating

```bash
make proto
```

This runs `buf generate` which produces:

- `gen/proto/hobnob/hobnob.pb.go` -- message types
- `gen/proto/hobnob/hobnob_grpc.pb.go` -- gRPC client/server stubs
- `gen/proto/hobnob/hobnob.pb.gw.go` -- grpc-gateway REST proxy

## Services

### HobnobAgent (gRPC-only, port 1530)

Agent-facing service. All RPCs (except Register) require ED25519 signature verification.

| RPC | Request | Response | Description |
|-----|---------|----------|-------------|
| Register | RegisterRequest | RegisterResponse | Enroll new agent |
| CheckIn | CheckInRequest | CheckInResponse | Pull config + report traits |
| ReportResult | ReportResultRequest | ReportResultResponse | Submit execution results |
| ReportTraits | ReportTraitsRequest | ReportTraitsResponse | Submit collected traits |
| DownloadCustomTrait | DownloadCustomTraitRequest | DownloadCustomTraitResponse | Fetch custom trait executable |

### HobnobAdmin (gRPC + REST, port 8443)

Admin-facing service with grpc-gateway REST annotations.

| RPC | Method | REST Endpoint | Description |
|-----|--------|--------------|-------------|
| AddApprovedPattern | POST | /api/v1/approved | Add enrollment pattern |
| ListApprovedPatterns | GET | /api/v1/approved | List patterns |
| RevokeApprovedPattern | DELETE | /api/v1/approved/{id} | Remove pattern |
| ListThralls | GET | /api/v1/thralls | List thralls |
| GetThrall | GET | /api/v1/thralls/{id} | Get thrall details |
| RevokeThrall | POST | /api/v1/thralls/{id}/revoke | Revoke thrall |
| DeleteThrall | DELETE | /api/v1/thralls/{id} | Delete thrall |
| ForcePush | POST | /api/v1/push | Trigger check-in |
| DryRun | POST | /api/v1/thralls/{thrall_id}/dry-run | Preview changes |
| ListRuns | GET | /api/v1/runs | List runs |
| GetRun | GET | /api/v1/runs/{id} | Get run details |
| GenerateToken | POST | /api/v1/tokens | Generate enrollment token |
| AddWebhook | POST | /api/v1/webhooks | Register webhook |
| ListWebhooks | GET | /api/v1/webhooks | List webhooks |
| DeleteWebhook | DELETE | /api/v1/webhooks/{id} | Remove webhook |
| GetComplianceSummary | GET | /api/v1/compliance | Fleet compliance |
| GetThrallCompliance | GET | /api/v1/thralls/{id}/compliance | Thrall compliance |
| Rollback | POST | /api/v1/thralls/{thrall_id}/rollback | Rollback changes |
| ListPackages | GET | /api/v1/packages | List packages |
| ListPackageDrift | GET | /api/v1/packages/drift | Package drift |
| PromoteEnv | POST | /api/v1/env/promote | Promote env vars |

## Key Messages

### Policy Transfer

- `CheckInRequest` -- includes `traits_json` (inline traits), `policy_version` (caching), `cached_module_hashes` (delta transfer)
- `CheckInResponse` -- includes `PolicyBundle` (raw policy for agent evaluation), `policy_version`, `no_change` flag
- `PolicyBundle` -- vars_json, conditional_vars, modules with content hashes
- `PolicyModule` -- `stub` flag enables delta transfer (agent uses cached version)

### Conditional Vars

- `ConditionalVarDef` -- name, trait (dot-path), operator, operand_json

### Custom Traits

- `CustomTraitEntry` -- name, sha256 (for cache validation)
