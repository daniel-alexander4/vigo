# ADR-005: Rust agent, Go server

**Status:** Accepted  
**Date:** 2025-01-01 (retroactive)

## Context

Vigo has two main runtime components: the agent (runs on every managed node) and the server (central coordinator). They have different operational profiles and constraints.

## Decision

The agent is written in Rust. The server and CLI are written in Go.

## Consequences

**Agent in Rust:**
- Single static binary, no runtime dependencies — deploy by copying a file
- Minimal resource footprint: low memory, no GC pauses, predictable CPU usage
- Must be invisible to the host — Rust's zero-cost abstractions make this achievable
- Cross-platform: compiles to Linux, macOS, FreeBSD, OpenBSD, NetBSD, illumos, Windows from one codebase
- Trade-off: slower development velocity than Go, more complex error handling

**Server in Go:**
- Fast development for HTTP/gRPC services — rich ecosystem (grpc-gateway, sqlc, buf)
- Good enough performance for a central server that handles API requests and config processing
- Go templates power the web UI and config template rendering
- Shared language with the CLI reduces context switching
- Trade-off: GC pauses acceptable on the server (not on thousands of agent nodes)

## Alternatives considered

- **All Go** — rejected because Go's GC and memory overhead are unacceptable for an agent that must be invisible on every managed node
- **All Rust** — rejected because Rust's web/gRPC ecosystem is less mature for rapid server development, and the server doesn't have the same resource constraints
