# ADR-003: mTLS everywhere

**Status:** Accepted  
**Date:** 2025-01-01 (retroactive)

## Context

Agents on managed nodes communicate with the server over gRPC. The communication contains sensitive data: desired state (which may include secrets references), execution results, and system traits. Agents also communicate peer-to-peer for swarm blob distribution.

## Decision

All gRPC traffic uses mutual TLS. No plaintext mode, no "dev mode" bypass, no flag to disable it. The agent presents its client certificate (issued during enrollment), and the server presents its server certificate. Swarm peer-to-peer HTTPS on port 1531 also uses mTLS with the same certificates.

## Consequences

- Every enrolled agent has a cryptographic identity (its keypair + signed cert)
- Server can verify agent identity on every request without tokens or API keys
- Peer-to-peer swarm traffic is authenticated — agents only accept blobs from other enrolled agents
- Certificate revocation blocks an agent from all communication (server and peer)
- Trade-off: initial setup requires certificate infrastructure (CA, CSR signing). The bootstrap flow handles this automatically.
- Trade-off: no easy "curl the API" for debugging — must use `vigocli` which handles mTLS

## Alternatives considered

- **Token-based auth** — rejected because tokens can be stolen and replayed; mTLS provides mutual authentication
- **mTLS with plaintext fallback** — rejected because fallback modes always become the default in practice
