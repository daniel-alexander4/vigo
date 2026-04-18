
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

## Dashboard card

The dashboard shows a "Platform Config" stat card with **N enabled / M available** where *M* is the total number of top-level sections Vigo supports and *N* is the count currently enabled. The card links to this page. The card only appears for admin users.

## Detail page

Clicking a card opens `/server/config/<section>` — a detail page with:

- A banner noting that the running server uses the startup copy and that `secret:` references are never resolved.
- (When enabled) a **Current Values** table of every scalar field the live file actually sets.
- An **Available Options** table of every field this section accepts, with type, default, possible values, and a one-line description.
- (When applicable) a **Sub-sections** section enumerating nested blocks. For `integrations`, that means one sub-section per provider — Slack, PagerDuty, Jira, and so on. For `auth`, the OIDC and isowebauth sub-configs.

## Secrets

Values are sourced from the raw YAML file, so any `secret:` reference (`api_key: "secret:vigo/ai/key"`) is displayed verbatim — the page never resolves the reference and never fetches from the secrets provider. The resolved plaintext never leaves the provider backend.

## Schema source of truth

The schema (field names, descriptions, possible values, defaults) is derived at runtime from struct tags on `ServerConfig` in `server/config/types.go`. The single source of truth is the Go type. A guardrail test (`server/config/schema_test.go`) fails CI if a new field lands without a `doc:` struct tag, so documentation stays in lockstep with the code.

## Editing server.yaml

This page is **read only**. Vigo's configuration model is "edit the YAML file" — all writes happen via file edits, not the UI. After editing `server.yaml` restart `vigosrv` (`docker compose restart vigo`) to activate the new config.

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
