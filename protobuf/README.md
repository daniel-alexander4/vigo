# Vigo Protocol Buffers

Protocol Buffer definitions for Vigo's gRPC services. Defines the agent-facing and admin-facing service contracts, all message types, and REST gateway annotations.

- **File:** `protobuf/vigo.proto`
- **Package:** `vigo.v1`
- **Go import:** `github.com/vigo/vigo/generated.code/proto/vigo`
- **Two services:** VigoAgent (gRPC-only), VigoAdmin (gRPC + REST)
- **Generated code:** `generated.code/proto/vigo/`

## Regenerating

```bash
make proto
```

This runs `buf generate` which produces:

- `generated.code/proto/vigo/vigo.pb.go` -- message types
- `generated.code/proto/vigo/vigo_grpc.pb.go` -- gRPC client/server stubs
- `generated.code/proto/vigo/vigo.pb.gw.go` -- grpc-gateway REST proxy

## Services

### VigoAgent (gRPC-only, port 1530)

Agent-facing service. All RPCs (except Register) require ED25519 signature verification.

| RPC | Request | Response | Description |
|-----|---------|----------|-------------|
| Register | RegisterRequest | RegisterResponse | Enroll new agent |
| CheckIn | CheckInRequest | CheckInResponse | Pull config + report traits |
| ReportResult | ReportResultRequest | ReportResultResponse | Submit execution results |
| ReportTraits | ReportTraitsRequest | ReportTraitsResponse | Submit collected traits |
| DownloadCustomTrait | DownloadCustomTraitRequest | DownloadCustomTraitResponse | Fetch custom trait executable |

### VigoAdmin (gRPC + REST, port 8443)

Admin-facing service with grpc-gateway REST annotations.

| RPC | Method | REST Endpoint | Description |
|-----|--------|--------------|-------------|
| AddApprovedPattern | POST | /api/v1/approved | Add enrollment pattern |
| ListApprovedPatterns | GET | /api/v1/approved | List patterns |
| RevokeApprovedPattern | DELETE | /api/v1/approved/{id} | Remove pattern |
| ListEnvoys | GET | /api/v1/envoys | List envoys |
| GetEnvoy | GET | /api/v1/envoys/{id} | Get envoy details |
| RevokeEnvoy | POST | /api/v1/envoys/{id}/revoke | Revoke envoy |
| DeleteEnvoy | DELETE | /api/v1/envoys/{id} | Delete envoy |
| ForcePush | POST | /api/v1/push | Trigger check-in |
| DryRun | POST | /api/v1/envoys/{envoy_id}/dry-run | Preview changes |
| ListRuns | GET | /api/v1/runs | List runs |
| GetRun | GET | /api/v1/runs/{id} | Get run details |
| GenerateToken | POST | /api/v1/tokens | Generate enrollment token |
| AddWebhook | POST | /api/v1/webhooks | Register webhook |
| ListWebhooks | GET | /api/v1/webhooks | List webhooks |
| DeleteWebhook | DELETE | /api/v1/webhooks/{id} | Remove webhook |
| GetConvergenceSummary | GET | /api/v1/convergence | Fleet convergence |
| GetEnvoyConvergence | GET | /api/v1/envoys/{id}/convergence | Envoy convergence |
| Rollback | POST | /api/v1/envoys/{envoy_id}/rollback | Rollback changes |
| ListPackages | GET | /api/v1/packages | List packages |
| ListPackageDrift | GET | /api/v1/packages/drift | Package drift |

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
