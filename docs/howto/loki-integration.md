---
title: Ship events to Grafana Loki
---

# Ship events to Grafana Loki

You'll finish this page with the Vigo server pushing every event (`envoy.offline`, `run.failure`, `security.cve.critical`, `audit.*`, etc.) into a Loki instance as labeled log streams that you can query in LogQL.

**When you'd use this:** anyone already running a Grafana stack who wants Vigo's event stream alongside their other application logs — same Grafana, same alert routes, same retention policy. Particularly useful for `audit.*` events: the hash-chained audit log stays the source of truth in SQLite, but Loki gives you ad-hoc query and Explore-style drilling without writing SQL.

**When you'd skip this:** all your alerts already route to Slack / PagerDuty / Sentinel / Splunk via the [other integrations](set-up-monitoring.md#integrations) — Loki is one more sink, not a replacement.

## How it fits

Loki is one more target on the Vigo events dispatcher. Same plumbing as Splunk HEC, Datadog, Sentinel, Elastic — every event the server emits gets fanned out to whatever's enabled. The Loki target:

- Pushes to `<url>/loki/api/v1/push` with the JSON streams format.
- Uses **low-cardinality labels** only: `service=vigo`, `severity=<info|warning|critical>`, `event_name=<envoy.offline|run.failure|audit.config_published|...>`.
- Puts **high-cardinality data** (`hostname`, `envoy_id`, `summary`, `details`) in the log line body as JSON — query with LogQL's `| json` parser.
- Bridges the **audit trail**: every entry the audit-writer chains into SQLite is also dispatched as `audit.<eventType>` so it surfaces in Loki alongside everything else.
- Carries a per-operation **`trace_id`** in `Details` so you can pivot from any event to every server- and agent-side log line for the same check-in / report / task dispatch — see [Correlate server + agent logs by trace_id](log-correlation.md).

This shape avoids the most common Loki anti-pattern: high-cardinality labels (one label value per envoy) that blow up the index.

## Configuration

Add a `loki:` block under `integrations:` in `server.yaml`:

```yaml
integrations:
  loki:
    enabled: true
    url: "https://logs-prod-us-central1.grafana.net"
    tenant_id: "123456"              # X-Scope-OrgID for multi-tenant Loki; omit for single-tenant
    username: "123456"               # basic-auth username (Grafana Cloud: your instance ID)
    password: "secret:vigo/loki/password"
    # bearer_token: "secret:vigo/loki/token"  # alternative to basic-auth; mutually exclusive
    events: []                        # empty = forward everything (recommended)
```

Then publish:

```sh
vigocli config publish
```

Vigo reloads the integrations dispatcher; the next event is the first one Loki sees.

### Auth modes

| Mode | What to set | When |
|---|---|---|
| Unauthenticated | leave `username`, `password`, `bearer_token` empty | Loki on a private network with no auth proxy |
| Basic-auth | `username` + `password` | nginx-fronted Loki, Grafana Cloud Logs (username = instance ID) |
| Bearer | `bearer_token` | Loki behind an OAuth/JWT proxy |

`tenant_id` is independent of auth mode — set it whenever the Loki deployment is multi-tenant (X-Scope-OrgID is required there).

### Selecting events

`events: []` forwards everything. To narrow:

```yaml
    events: ["envoy.offline", "run.failure", "security.cve.critical", "config.reload.failure"]
```

To filter out the audit fan-out (high-volume in active fleets):

```yaml
    # Exclude audit.* by listing only non-audit events; there is no negative pattern.
    events: ["envoy.offline", "envoy.online", "run.failure", "run.success", "drift.detected",
             "security.cve.critical", "security.cve.high", "config.reload.failure",
             "convergence.threshold", "secret.rotated"]
```

## Querying

Stream selector:

```logql
{service="vigo"}
```

By severity:

```logql
{service="vigo", severity="critical"}
```

Just the audit trail:

```logql
{service="vigo", event_name=~"audit\\..*"}
```

Filter by hostname (in the body, not a label):

```logql
{service="vigo"} | json | hostname="web-01"
```

Count events per envoy over the last hour:

```logql
sum by (hostname) (count_over_time({service="vigo"} | json [1h]))
```

## Alerts via Loki Ruler

If you run Loki's [Ruler](https://grafana.com/docs/loki/latest/alert/) and prefer log-based alerting over the Prometheus alert rules in [grafana/alerts.yaml](grafana/alerts.yaml):

```yaml
# loki-rules.yaml
groups:
  - name: vigo-loki
    rules:
      - alert: VigoSigVerifyFailureRate
        expr: |
          sum(rate({service="vigo", event_name="security.sig_verify_failed"} [5m])) > 0
        labels: { severity: critical }
        annotations:
          summary: "Signature verification failures detected"

      - alert: VigoConfigReloadFailures
        expr: |
          sum(rate({service="vigo", event_name="config.reload.failure"} [15m])) > 0
        labels: { severity: warning }
        annotations:
          summary: "Vigo config reload failed"
```

The Prometheus and Loki rule files don't conflict — most operators ship both: Prometheus for metric-derived health (counts, percentages, latencies), Loki for log-derived events (failures, rare conditions).

## Operational notes

- **Delivery is best-effort, async.** Each event dispatches in a goroutine with a 10-second timeout per target. Delivery failures log at `slog.Error` level (`integration dispatch failed target=loki ...`); they don't block check-in or convergence.
- **Suppression applies.** Vigo's existing fleet-wide event suppression (10+ identical events from different envoys in 60s collapses to one grouped event) applies before dispatch — Loki sees the same suppressed stream every other integration target sees.
- **Loki rejects ingest if a stream's labels change.** Don't add custom labels here without thinking; the three labels (service/severity/event_name) are deliberately stable.
- **TLS material is reused.** The HTTP client uses the standard Go transport — Vigo trusts whatever's in the system's CA bundle. No way to pin a custom CA for the Loki sink alone today; if your Loki uses a private CA, install it system-wide on the vigosrv host.

## Verify

After publish:

1. Watch the server logs for `integrations dispatcher enabled targets=N` (count includes Loki when enabled).
2. Trigger a synthetic event — force-push a module that won't apply, or revoke + re-enroll a test envoy.
3. In Grafana, open Explore → Loki → `{service="vigo"}` and confirm the line lands.
4. Run `vigocli config publish` against any unchanged stockpile — that fires an `audit.config_published` event you can find at `{service="vigo", event_name="audit.config_published"}`.

If Loki returns `4xx`, check the server logs for `integration dispatch failed target=loki`; the error message carries the Loki response code (usually 401 / 403 from auth, or 400 from a malformed tenant header).

---

*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../legal/license.md).*
