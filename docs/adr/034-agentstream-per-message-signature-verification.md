<!--
Copyright (c) 2026 Alexander4, LLC
SPDX-License-Identifier: LicenseRef-Proprietary
-->

# ADR-034: AgentStream per-message ed25519 verification

**Status:** Accepted — implemented in 0.74.15.
**Date:** 2026-06-05

## Context

Every agent request is authenticated by two independent layers: **mTLS**
(per-connection, client cert signed by the Vigo CA) and a **per-request ed25519
signature** against the envoy's *enrolled* pubkey (a different key from the TLS
cert — enrollment vs CSR). CLAUDE.md states this as a non-negotiable invariant:
"every client request is signature-verified against the stored pubkey."

The unary RPCs enforce it via `SignatureVerifyInterceptor` (a **unary**
interceptor → `verifyCheckIn` / `verifyReportResultCtx` / `verifyReportTraits` /
`verifyResolveSecretsCtx`). But the persistent **`AgentStream`** (bidi) bypassed
it entirely: the stream interceptor chain is recovery-only, the handshake checked
only that `envoy_id` existed and wasn't revoked, and the read loop dispatched
multiplexed `CheckIn`/`ReportResult`/`ReportTraits` straight to the handlers with
no signature check. The channel was authenticated by mTLS alone — and mTLS does
**not** bind identity here: `buildTLSConfig` is `RequireAndVerifyClientCert`
against the shared CA with no `VerifyPeerCertificate`/`VerifyConnection`, and the
client cert's CN is the hostname, never cross-checked against the claimed
`envoy_id`.

**Impact:** any enrolled envoy A (holding any valid CA-signed cert) could open an
AgentStream as `envoy_id = B` and receive B's policy bundle, ad-hoc tasks, and
scrier dispatch, and submit check-in results / traits as B — insider cross-envoy
impersonation, and a silent violation of the stated invariant on the stream path.
Bounded (requires an enrolled cert; `ResolveSecrets` is unary so secrets stay
ed25519 + entitlement-gated; proxied results had a config gateway check) but live.

This was an oversight, not a deliberate trade-off: the sibling stream
`TunnelStream` (scrier) already verifies its handshake signature
(`tunnel.go`) — the same codebase's convention for authenticating a stream,
simply missed on `AgentStream`.

## Decision

Restore the invariant on `AgentStream` by verifying signatures with the existing
helpers — no new abstraction, same `fleetIndex.Verify` fast path (cached expanded
pubkey):

1. **Handshake** — `verifyCheckIn` the first message before trusting `envoy_id`.
   This binds the whole stream to the holder of the enrolled key; TLS session
   integrity (TLS 1.3, `RequireAndVerifyClientCert`) then guarantees subsequent
   messages come from that same peer.
2. **Per-message** — verify every multiplexed `CheckIn`/`ReportResult`/
   `ReportTraits`; a verification failure tears the stream down. The agent already
   signs each of these (the signature was on the wire; the server just ignored it
   on the stream). For proxied `ReportResult` (`envoy_id` ≠ stream owner) this
   verifies the **device's** signature, which the config-based gateway check
   cannot prove on its own.

Signature-less stream messages (`TaskResult`/`Delta`/`QueryResponse`/`TaskAck`)
carry no per-message signature and are covered by the handshake binding + TLS
integrity. The signature window is resolved with the same `0 → 5min` default the
unary interceptor applies (passing a raw `0` would reject every message on
`checkTimestamp`).

Server-only change. Agents already sign every stream message, so there is no
agent change and no backward-compatibility break — the prior server merely
ignored the signatures; the new one checks them.

## Consequences

- `AgentStream` and the unary path now enforce the same invariant; the
  insider cross-envoy impersonation gap is closed.
- Adds one ed25519 verify per stream message (the unary cost, on the same fast
  path) — affordable: the server is network-bound, not CPU-bound.
- mTLS-cert-to-`envoy_id` binding was considered as an alternative (check cert CN
  == envoy hostname) but rejected: weaker than the ed25519 binding (no per-request
  non-repudiation), couples auth to hostname, and the per-message signatures are
  already present and free to verify.
