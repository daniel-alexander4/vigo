# ADR-007: Pluggable secrets backend

**Status:** Accepted  
**Date:** 2025-01-01 (retroactive)

## Context

Module configs need to reference secrets (database passwords, API keys, certificates). These secrets must never appear in plaintext in YAML files, environment variables, logs, database records, or gRPC payloads.

## Decision

Secrets use a `secret:` prefix in YAML (e.g., `dsn: "secret:vigo/db/dsn"`). The config loader resolves these through a `secrets.Provider` interface at load time. Two implementations: `LocalProvider` (file-based, default) and `IsopassProvider` (production).

If the secrets provider is unreachable or misconfigured, the server fails fast — no insecure fallback.

## Consequences

- Secrets are never stored in config files — only references
- The provider interface is simple: `Get(path) → (value, error)`
- `LocalProvider` maps `secret:vigo/db/dsn` → file `/srv/vigo/secrets/vigo/db/dsn` — dead simple for development
- `IsopassProvider` integrates with a production secrets management system
- Adding a new backend (Vault, AWS Secrets Manager) means implementing one interface
- Trade-off: secrets must be provisioned outside of Vigo's config workflow. This is intentional — secrets management is a separate concern.
- Fail-fast on missing secrets prevents silent misconfiguration

## Alternatives considered

- **Encrypted values in YAML** — rejected because key management becomes the problem, and encrypted blobs in config files are hard to audit
- **Environment variables** — rejected because they leak into process tables, crash dumps, and child processes
