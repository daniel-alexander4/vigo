
# Security

The envoy detail page at `/envoys/{id}` serves as the security information view for individual managed nodes. It shows enrollment credentials, compliance status, and audit history.

## Layout

### Envoy Header

- Hostname (as page title), IP address, enrollment date, last seen timestamp
- Convergence status badge (clickable link to run detail for drift/error states)
- Agent version (e.g., "Agent 0.2.19") — shown when the envoy has reported its version

### Tabs

Content is loaded via HTMX fragments. Six tabs:

- **Recent Runs** (default) — last 20 runs with expandable per-module results; each run shows a spectrum-colored failure-ratio badge
- **Traits** — host-summary stat cards (OS, CPU, Memory, leaf count + collected timestamp) plus a filterable, recursive `<details>` tree of the full nested trait payload, with a Raw JSON escape hatch at the bottom. The filter input matches against the dotted key path and auto-expands matching groups.
- **View Config** — effective resolved configuration (matched pattern, roles, modules, merged vars with secrets redacted)
- **Config Trace** — inheritance chain showing how config was assembled from common.vgo, match blocks, and module defaults
- **Standards Compliance** — per-framework compliance cards with coverage percentages
- **Attack Surface** — risk score, CVE summary, listening ports

## Key Actions

- **Force convergence**: Trigger an immediate check-in for this envoy
- **Revoke**: Block the envoy from future check-ins (irreversible without manual intervention)
- **Delete**: Permanently remove the envoy and all its history
- **View effective config**: See exactly what modules and resources are assigned to this envoy
- **View traits**: Inspect discovered facts about the machine

## Data Sources

- Envoy details: FleetIndex (`GetEnvoy()`) or database
- Run history: Database `ListRuns` filtered by envoy ID
- Traits: Database `GetLatestEnvoyTraits` or FleetIndex trait cache
- Config: `ConfigStore.Get(hostname)` with secret redaction
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
