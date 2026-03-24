
# API Reference

Vigo exposes two API surfaces:

- **gRPC (port 1530)** -- Agent-to-server communication. All traffic uses mutual TLS (mTLS) with ED25519 signature verification on every request. Defined as the `VigoAgent` and `VigoAdmin` services in `proto/vigo.proto`.
- **REST (port 8443)** -- Admin and CLI communication. Plain HTTP designed for internal networks or placement behind a reverse proxy. Includes both grpc-gateway proxied endpoints (under `/api/v1/`) and direct REST handlers for traits, inventory, pushes, workflows, query, AI, doctor, license, and spanner.

## Proto File

All gRPC message definitions live in [`proto/vigo.proto`](../../../proto/vigo.proto). The proto defines two services:

| Service | Port | Transport | Purpose |
|---------|------|-----------|---------|
| `VigoAgent` | 1530 | gRPC over mTLS | Agent enrollment, check-in, result reporting, streaming |
| `VigoAdmin` | 8443 | gRPC-gateway REST (HTTPS) | Admin operations via CLI and UI |

## Authentication

**gRPC (agent):** Every request includes an ED25519 signature over the payload and timestamp. The server verifies the signature against the agent's stored public key. The TLS layer provides mutual authentication via client certificates.

**REST (admin):** Pluggable authentication with three methods: `basic` (default, username/password with secrets-backed credentials), `oidc` (OpenID Connect SSO), or `isowebauth` (SSH key signing). REST endpoints require a session cookie or `Authorization: Bearer` API token (`vgot_` prefix). Local CLI requests from the server host are trusted automatically (localhost peer auth). See [Authentication](../../operations/authentication.md) for details. All `/api/v1/` endpoints return JSON. Some endpoints (backup, users, db) require admin role.

## Further Reading

- [gRPC Agent Service](grpc-agent.md) -- the 6-RPC agent service
- [gRPC Admin Service](grpc-admin.md) -- the 23-RPC admin service
- [REST API](rest.md) -- direct REST endpoints (pushes, workflows, query, AI, etc.)
- [Webhooks](webhooks.md) -- event-driven HTTP callbacks
