
# Security

The envoy detail page at `/envoys/{id}` serves as the security information view for individual managed nodes. It shows enrollment credentials, compliance status, and audit history.

## Layout

### Envoy Header

- Hostname (as page title), IP address, enrollment date, last seen timestamp
- Convergence status badge (clickable link to run detail for drift/error states)
- Agent version (e.g., "Agent 0.2.19") — shown when the envoy has reported its version

### Tabs

Content is loaded via HTMX fragments. Seven tabs:

- **Drift History** (default) — convergence status timeline with changed/failed counts per run
- **Recent Runs** — last 20 runs with expandable per-module results
- **Traits** — flattened key-value pairs from the most recent trait collection (OS, hardware, packages, etc.)
- **View Config** — effective resolved configuration (matched pattern, roles, modules, merged vars with secrets redacted)
- **Config Trace** — inheritance chain showing how config was assembled from common.vgo, node entries, and module defaults
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
