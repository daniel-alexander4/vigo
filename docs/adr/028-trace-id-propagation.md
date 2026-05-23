---
title: "ADR-028: Trace-ID propagation for cross-process log correlation"
---

# ADR-028: Trace-ID propagation for cross-process log correlation

**Status:** Approved 2026-05-23, implementation in progress.
**Date:** 2026-05-23

## Context

The Loki integration (0.66.42) made structured server logs queryable by
envoy, endpoint, and severity. The next operator question — "show me
every server *and* agent log line for *this specific* check-in or
report" — needs a correlation key that flows across the process
boundary.

Full OpenTelemetry would solve this and more (flame graphs, sampled
spans, OTLP export to Tempo/Jaeger), but it is a real instrumentation
project: new dependency trees in both languages, per-event allocations
on the agent's hot path (`apply_resource`, `execute_module`), sampling
defaults to tune, and an ADR-level design call for how the persistent
`AgentStream` participates in tracing. Nobody is asking for that today.

What operators actually need today is a single field — call it
`trace_id` — present in every log line on both sides of a single
operation, so `{trace_id="…"}` in Loki returns the full picture.

## Decision

Propagate a **W3C `traceparent` value in the proto message body** of
every agent-initiated request and every relevant server-pushed event.
Both sides extract the embedded trace_id and surface it as a
`slog` (server) / `tracing` (agent) field, and the integrations event
dispatcher includes it in `Event.Details["trace_id"]`.

### Wire format

W3C `traceparent` — `00-{trace_id_32hex}-{span_id_16hex}-{flags_2hex}`,
55 characters. Strictly parsed; malformed or absent values trigger a
fresh server-side generation (16 random bytes for trace_id, zero
span_id, sampled-flag set). The 32-hex `trace_id` portion is the
field surfaced to logs and the integrations payload.

The full W3C string is what travels on the wire so the deferred full
OTel item (see [pending][1]) is purely additive: when an `otelgrpc`
interceptor is added later, it consumes the same `traceparent` value
without re-format.

### Field placement: proto body, not gRPC metadata

The non-obvious call. Standards-compliant OTel transports `traceparent`
in gRPC metadata. We don't, because of `AgentStream`.

gRPC metadata is **per-call** — set once when the call is opened, read
once on the server side. The persistent bidirectional `AgentStream`
opens *once* and then multiplexes many logical operations under that
one metadata frame (CheckIn-via-stream, ReportResult-via-stream,
ReportTraits-via-stream, server-pushed task dispatch, force-push,
policy push). One metadata frame cannot give per-operation granularity
for streamed calls.

Putting `traceparent` in the message body works uniformly: every
arm of `AgentStreamMessage` / `ServerStreamMessage` that needs its own
trace_id has a field, every per-arm handler reads it from the request
itself, the unary-vs-streamed distinction disappears.

The full-OTel follow-on harmonizes the two paths: for plain unary RPCs
the otelgrpc interceptor will read metadata; for streamed events the
proto-body field continues to serve. Documented in this ADR so future
readers don't try to "fix" the placement.

### Coverage

Both directions:

- **Agent→server.** `CheckInRequest`, `ReportResultRequest`,
  `ReportTraitsRequest` each get `traceparent`. The agent generates a
  fresh value per call.
- **Server→agent.** Push messages where an operator-triggered
  operation needs end-to-end traceability — `TaskDispatch`,
  `ForcePush`, `PolicyPush` — get the field too. The server generates
  the value at the admin-API entry point and propagates through.

Out of scope for the initial implementation:

- `TunnelStream` byte-stream messages (scrier sessions — useful, but a
  separate follow-on once the agent-RPC story is in place).
- The audit log hash chain. Adding `trace_id` to the canonical bytes of
  audit records would require a schema migration and a chain-format
  bump; not justified for this slice. Bridged audit→events still pick
  up `trace_id` from the request scope where available — the audit
  record itself stays trace_id-less for v1, queryable via Loki by
  the operational event that bridged it.

### Always-on; cardinality firewall

No `tracing:` server.yaml section. No toggle. `trace_id` is
unconditionally generated when absent and unconditionally added to
slog calls and `Event.Details`.

**`trace_id` is never a Prometheus metric label.** It is exclusive to
slog attributes, `tracing` span fields, and integrations payload maps
(Loki/Splunk/Sentinel). The high-cardinality firewall is explicit
because the same field name appears in both surfaces and the boundary
is easy to forget.

### Validation

The server treats `traceparent` as untrusted input. Length-capped at
55 characters, strictly regex-matched against
`^[0-9a-f]{2}-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$`. Any failure →
generate a fresh value. Never log the raw input string — only the
parsed `trace_id` portion.

## Consequences

- Operators query Loki `{trace_id="…"}` and get the cross-process log
  set for a single check-in / report / task dispatch.
- One 16-byte random read + one slog attribute + one
  `tracing::Span::enter()` per RPC. Trivial against the gRPC roundtrip
  cost; satisfies the CLAUDE.md hot-path doctrine.
- No new dependencies on either side. No new server.yaml config.
- Forward-compatible with the deferred full OTel item — `traceparent`
  is the standards-compliant context format; adopting OTel later adds
  span allocations and OTLP export without changing the wire shape.
- Trace_id from the audit table is not directly queryable (audit
  records don't carry the field). Documented limitation; operators
  query via Loki using the bridged events instead.

## Alternatives considered


- **Full OpenTelemetry SDK + OTLP exporter.** Deferred. Pick up when
  a paying prospect or compliance reviewer asks for an OTel checkbox
  specifically. Until then, `trace_id` + Loki delivers the
  operator-debuggability value at ~10% of the hot-path cost and zero
  new deps.
- **gRPC metadata only.** Standards-compliant for unary, but cannot
  give per-operation granularity for streamed `AgentStream` events
  (one metadata frame multiplexes many logical operations). Rejected.
- **Plain UUID (not W3C format).** Simpler, but breaks forward-compat
  with the deferred OTel item — every old trace_id would be unparseable
  by an OTel SDK. Not worth the savings.
- **Audit-record `trace_id` field.** Would require a schema migration
  and a hash-chain canonical-bytes bump. Deferred; the bridged-event
  path delivers the same query value via Loki.
