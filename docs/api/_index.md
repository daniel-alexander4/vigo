# API Reference

Hobnob exposes two API surfaces:

- **gRPC (port 1530)** -- Agent-to-server communication. All traffic uses mutual TLS (mTLS) with ED25519 signature verification on every request. Defined as the `HobnobAgent` and `HobnobAdmin` services in `proto/hobnob.proto`.
- **REST (port 8181)** -- Admin and CLI communication. Plain HTTP designed for internal networks or placement behind a reverse proxy. Includes both grpc-gateway proxied endpoints (under `/api/v1/`) and direct REST handlers for traits, inventory, tasks, workflows, query, AI, doctor, license, and federation.

## Proto File

All gRPC message definitions live in [`proto/hobnob.proto`](../../proto/hobnob.proto). The proto defines two services:

| Service | Port | Transport | Purpose |
|---------|------|-----------|---------|
| `HobnobAgent` | 1530 | gRPC over mTLS | Agent enrollment, check-in, result reporting, streaming |
| `HobnobAdmin` | 8181 | gRPC-gateway REST | Admin operations via CLI and UI |

## Authentication

**gRPC (agent):** Every request includes an ED25519 signature over the payload and timestamp. The server verifies the signature against the agent's stored public key. The TLS layer provides mutual authentication via client certificates.

**REST (admin):** Pluggable authentication with three modes: `none` (default, zero overhead), `oidc` (OpenID Connect SSO), or `isowebauth` (SSH key signing). When auth is enabled, REST endpoints require a session cookie or `Authorization: Bearer` API token (`hbt_` prefix). See [Authentication](../operations/authentication.md) for details. All `/api/v1/` endpoints return JSON. Some endpoints (backup, doctor) require admin role.

## Further Reading

- [gRPC Agent Service](grpc-agent.md) -- the 6-RPC agent service
- [gRPC Admin Service](grpc-admin.md) -- the 23-RPC admin service
- [REST API](rest.md) -- direct REST endpoints (tasks, workflows, query, AI, etc.)
- [Webhooks](webhooks.md) -- event-driven HTTP callbacks
