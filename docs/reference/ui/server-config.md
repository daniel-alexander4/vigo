
# Server Config

**Route:** `/server/config`
**Role:** admin only

A read-only overview of `server.yaml` as it sits on disk. One card per top-level section (there are ~27 today — `server`, `database`, `auth`, `swarm`, `integrations`, etc.). Each card shows whether the section is currently enabled or disabled and a one-line description of what it configures.

## Enabled vs disabled

The card reflects the **live file on disk**, not the running server. An operator edits `server.yaml`, reloads the page, and the cards update immediately — the server itself still uses the copy loaded at startup until it is restarted.

A section's card shows **enabled** when:

- Its key is present in the top-level mapping of `server.yaml`, AND
- Its value is a non-empty mapping / sequence / scalar, AND
- It does not have an explicit `enabled: false` child.

A section's card shows **disabled** when:

- Its key is absent from the file (not typed or fully commented out), OR
- Its value is `null` / `~` / an empty mapping `{}`, OR
- It has an explicit `enabled: false` child.

## Navigation

`/server/config` is reachable from the sidebar under the **Vigo** group, admin-only. There is no longer a dashboard card — system configuration belongs in the admin surface, not on the fleet-state dashboard.

## Detail page

Clicking a card opens `/server/config/<section>` — a detail page with:

- A section header with a short prose description of what the section does and a **Read the documentation →** link to the relevant doc page (derived from the `docs:` struct tag on the section's field in `ServerConfig`).
- A banner noting that the running server uses the startup copy and that `secret:` references are never resolved.
- A **Current Config** block — pretty-printed YAML of this section's live values, re-serialized from the raw file. Renders as `(not configured — this section is absent or explicitly disabled in the live file)` when the section is absent or explicitly `enabled: false`.
- An **Example Config** block — shown only when the live section is absent. Annotated YAML starter template in Prometheus/Grafana style: fields with a `default:` tag are commented out with a trailing `# (default: X)` note; fields without a default are uncommented with a sensible starter value from the `example:` tag; sub-sections (integrations.*, auth.oidc, etc.) render as commented-out blocks that the operator uncomments to enable. A **Copy** button in the block header copies the whole YAML to the clipboard for pasting into `server.yaml`.
- An **Options** list — definition-list style, one entry per accepted field. Each entry shows the name (monospace), type, default, and possible values on a header row, with the description and a concrete example stacked beneath. Reads like published reference docs rather than a spreadsheet, which keeps longer multi-sentence descriptions readable and fits narrow viewports without horizontal scroll.
- (When applicable) a **Sub-sections** block enumerating nested blocks. For `integrations`, that means one sub-section per provider — Slack, PagerDuty, Jira, and so on. For `auth`, the OIDC and isowebauth sub-configs. Each sub-section carries its own Current Config YAML block when present, plus the same definition-list options treatment.

## Secrets

Values are sourced from the raw YAML file, so any `secret:` reference (`api_key: "secret:vigo/ai/key"`) is displayed verbatim — the page never resolves the reference and never fetches from the secrets provider. The resolved plaintext never leaves the provider backend.

## Schema source of truth

The schema (field names, descriptions, possible values, defaults, examples) is derived at runtime from struct tags on `ServerConfig` in `server/config/types.go`. The single source of truth is the Go type. The tags in play:

- `doc:"..."` — plain-English description (required; guardrail test fails without it). One short sentence; rendered on the listing-grid card and used as the umbrella on the section detail page when no `long:` tag is set.
- `long:"..."` — 2–3 sentence umbrella prose for the section detail page (top-level `ServerConfig` fields only). Reserved for the detail view so the listing cards stay terse and scannable. Falls back to `doc:` when absent.
- `docs:"..."` — relative path under `/docs/` for the **Read the documentation →** link on the section's detail page (top-level fields only). Optional; omit when there's no dedicated doc page.
- `default:"..."` — default value when omitted. Drives whether the Example Config block renders a field uncommented (no default → required) or commented-out with `# (default: X)` (has a default → optional).
- `options:"a|b|c"` — pipe-separated enum values.
- `example:"..."` — concrete value or usage hint rendered as the `example` line in the Options list, and used as the starter value for required fields in the Example Config block.

A guardrail test (`server/config/schema_test.go`) fails CI if a new field lands without a `doc:` struct tag, so documentation stays in lockstep with the code.

## Editing server.yaml

This page is **read only**. Vigo's configuration model is "edit the YAML file" — all writes happen via file edits, not the UI. After editing `server.yaml` restart `vigosrv` (`docker compose restart vigo`) to activate the new config.

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
