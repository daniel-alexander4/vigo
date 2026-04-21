
# GRC Platform Integration

Vigo can push compliance evidence directly into third-party Governance, Risk, and Compliance (GRC) platforms — Vanta, Drata, ServiceNow GRC, and similar. Each GRC target is configured independently with its own endpoint, API key, and framework mapping. The server translates Vigo's internal compliance state into whatever format the target expects and pushes on a schedule.

## Why it exists

If the organization has already standardized on a GRC platform — most SaaS companies pursuing SOC 2, ISO 27001, or HITRUST do — the evidence Vigo produces (module enforcement, trait scans, convergence records) is the ground truth an auditor eventually wants to see. Pushing it into the GRC platform means evidence flows there continuously rather than being pulled via the REST API or manually uploaded.

This is distinct from the general [integrations](integrations.md) surface (Slack, PagerDuty, Jira, ITSM, SIEM) which target operational workflows. GRC is compliance-scoped: only control evidence flows out, and only to GRC-category targets.

## Config

```yaml
grc:
  integrations:
    - name: vanta
      enabled: true
      endpoint: "https://api.vanta.com/v1"
      api_key: "secret:vigo/grc/vanta_api_key"
      push_interval: "1h"
      frameworks:
        soc2: "soc2-type2"
        iso27001: "iso_27001"
    - name: drata
      enabled: true
      endpoint: "https://public-api.drata.com"
      api_key: "secret:vigo/grc/drata_api_key"
      push_interval: "6h"
      frameworks:
        soc2: "SOC2"
        hipaa: "HIPAA"
```

Each entry supports:

| Field | Purpose |
|-------|---------|
| `name` | Platform identifier: `vanta`, `drata`, or `servicenow`. |
| `endpoint` | API endpoint URL for the platform. |
| `api_key` | Token for authentication. Supports the `secret:` prefix. |
| `push_interval` | How often to push evidence. `1h` is sensible for most fleets; `6h`/`24h` for lower-change deployments. |
| `enabled` | Toggle. Leave at `false` during initial setup to validate credentials via `vigocli grc test` (planned) before pushing real data. |
| `frameworks` | Map of Vigo's internal framework key (e.g. `soc2`, `hipaa`) to the platform's matching framework identifier. Required — the GRC platform won't know what to do with evidence that isn't tagged to a framework it tracks. |

## Framework mapping

Each GRC platform uses its own vocabulary for frameworks. Vigo's internal keys are the slugs used in the compliance catalog (`soc2`, `hipaa`, `pci_dss`, `iso_27001`, `nist_800_53`, `cis_v8`, `gdpr`, etc.). The `frameworks:` map translates from Vigo's key to the platform's framework ID so evidence lands under the correct audit program on the other side.

Get the platform-side IDs from:

- **Vanta** — Settings → Frameworks; the slug in the URL after `/frameworks/`.
- **Drata** — each framework has a stable name shown in the dashboard.
- **ServiceNow GRC** — Framework records under Policy and Compliance Management.

## Credentials

All `api_key` values should use the `secret:` prefix to pull from the configured secrets backend. Storing API tokens inline in `server.yaml` is never the right move — they end up in version control, logs, and backups.

See [Secrets](../security/secrets.md) for how the secret reference is resolved.

## What gets pushed

Vigo pushes evidence artifacts per control:

- Module enforcement records (which controls each envoy's module set enforces)
- Trait scan outputs (security scans, hardening scores, port lists) tagged to their controls
- Convergence timestamps per envoy per control
- Waiver documentation when a control is waived

Exact artifact shape depends on the target platform's API. Each integration handler lives under `server/integrations/grc/` and translates Vigo's internal compliance state into the platform's expected payload.

## Related

- [Integrations](integrations.md) — operational integrations (chat, paging, ticketing, SIEM). GRC is compliance-scoped; those are operational.
- [Compliance](../compliance/overview.md) — how Vigo tracks framework control coverage internally.
- [Compliance export](../compliance/compliance-export.md) — the one-off SIEM/CMDB/OSCAL export path for consumers that don't have an inbound API.
- [Secrets](../security/secrets.md) — how `secret:` references are resolved.
