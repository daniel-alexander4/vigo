---
title: Correlate server + agent logs by trace_id
---

# Correlate server + agent logs by trace_id

You'll finish this page knowing how to take a single check-in, report, force-push, or task dispatch and query the **full** server + agent log line set for it from one place — typically Loki via `{trace_id="…"}`.

**When you'd use this:** you've seen a failed run, a slow check-in, or an unexpected drift event in the dashboard or in `vigocli runs`, and you want every log line both processes wrote about it. Or a force-push from `vigocli push` lit up a fleet-wide issue and you want to follow a single envoy's path through the incident.

**When you'd skip this:** you're already happy with per-process tailing. Log correlation costs you nothing — it's always on — but you only get value from it once you query the same `trace_id` across both sources.

## What's actually wired

Every agent-initiated request (`CheckIn`, `ReportResult`, `ReportTraits`) and every server-pushed event the agent processes (`TaskDispatch`, `ForcePush`) carries a W3C `traceparent` string in the proto message body. Server and agent both extract the 32-hex `trace_id` portion and attach it to:

- **Server** — every `slog` log line for that operation. Field: `trace_id`.
- **Server** — the integrations event payload at `Event.Details["trace_id"]` for every `run.*`, `drift.detected`, `resource.blocked`, `resource.unblocked`. Loki, Splunk, Sentinel, etc. all see it.
- **Agent** — every `tracing` log line emitted under the operation's span. Same field name: `trace_id`.

`TaskDispatch` round-trips end-to-end: the server generates the `trace_id` when dispatching, the agent uses it for execution logs, and echoes it back in `TaskResult` — so server-side "received task result" and agent-side "executing task" share the same field.

The format is standards-compliant W3C `traceparent` (`00-<trace_id_hex>-<span_id_hex>-<flags>`). The trace_id portion (32 lowercase hex characters) is what you query on. See ADR-028 for the field-placement reasoning.

## Querying in Loki

If you've followed [Ship events to Grafana Loki](loki-integration.md), `trace_id` is already in your event payload. In Explore:

```logql
{service="vigo"} | json | trace_id="b3a1c0f2eb1a4c7e8d9f0123abcdef01"
```

To follow a specific event back to all the logs about it:

1. Start in Loki Explore at `{service="vigo"} | json`.
2. Filter to the event of interest — e.g. `| event_name="run.failure"`.
3. Pick the `trace_id` field out of the JSON payload, then expand to a new query: `| trace_id="<value>"`.
4. Drop the event-name filter — you now have the full per-operation chain across server and agent.

Agent-side logs land in Loki only if you also ship the agent's host logs to Loki (Promtail, Vector, Grafana Alloy — any host log shipper); the agent itself doesn't have a Loki integration. Once you do, the `trace_id` field is already there because the agent emits it on every `tracing` log line for the operation. Query the same way.

## Querying in vigosrv logs directly

If you're SSH'd to the vigosrv host or running `docker logs vigo`:

```bash
docker logs vigo 2>&1 | grep 'trace_id=b3a1c0f2eb1a4c7e8d9f0123abcdef01'
```

The `slog` text formatter emits attributes as `key=value`, so plain `grep` works. On the agent side (`journalctl -u vigo` on systemd, `/var/log/vigo/agent.log` otherwise):

```bash
journalctl -u vigo | grep 'trace_id="b3a1c0f2eb1a4c7e8d9f0123abcdef01"'
```

The `tracing` crate emits attributes in `key="value"` form, hence the quotes.

## Known limitations

- **Audit table.** The hash-chained audit log in SQLite does **not** carry `trace_id` per record — that would require a schema migration and a chain-format bump. Instead, every audit entry is fanned out as an `audit.<eventType>` integration event that **does** include `trace_id` from the request scope (`server/audit/` → `Writer.OnRecord` → `integrations.Dispatch`). Query the audit story via Loki, not via `vigocli audit` for now.
- **`TunnelStream` scrier sessions.** Browser-driven SSH/RDP/VNC sessions go through `TunnelStream`, which is byte-relay and not in scope for this version. A scrier session has its own session ID; trace_id will land there in a follow-on.
- **Trait-triggered workflows.** When a trait change triggers a workflow (`server/grpc/traits.go` → `checkTraitTriggers`), the workflow runs in its own goroutine with a fresh context. The `ReportTraits` `trace_id` does **not** propagate into the workflow logs — the workflow is logically its own operation. Use the workflow's `run_id` to find its logs, then jump backward to the trait report via `envoy_id` + timestamp window if needed.
- **Older agents.** An agent that doesn't yet send `traceparent` simply gets a server-generated value attached at the request boundary. No protocol break.

## How to verify it's working

After upgrading to 0.66.50 or later, force-push one envoy and tail both logs:

```bash
# Terminal 1: server
docker logs -f vigo 2>&1 | grep trace_id

# Terminal 2: agent
ssh <envoy> 'sudo journalctl -fu vigo' | grep trace_id

# Terminal 3
vigocli push --envoy <envoy>
```

You should see the server log a `TaskDispatch`-sourced trace_id, the agent's "received force push via stream" carrying the same value, and the subsequent check-in / report cycle generating fresh trace_ids of its own (each is its own logical operation).
