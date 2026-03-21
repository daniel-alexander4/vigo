
# Security

The envoy detail page at `/envoys/{id}` serves as the security information view for individual managed nodes. It shows enrollment credentials, compliance status, and audit history.

## Layout

### Envoy Header

- Hostname, IP address, enrollment date, last seen timestamp
- Compliance status badge
- Revocation status

### Identity Section

- **Envoy UUID**: The server-assigned identifier
- **Public Key**: ED25519 public key registered at enrollment
- **Enrolled At**: When the envoy was first enrolled
- **Revoked**: Whether the envoy is revoked and when

### Run History

A table of recent convergence runs for this envoy:

| Column | Description |
|--------|-------------|
| Run ID | Link to run detail |
| Status | Success/failure/partial badge |
| Modules | Module count |
| Changed | Resources changed |
| Failed | Resources failed |
| Time | Run timestamp |

### Traits

The most recently collected traits (facts) for this envoy, displayed as a nested JSON tree. Includes OS information, hardware details, network configuration, installed packages, and custom traits.

### Configuration

The effective resolved configuration for this envoy, showing:
- Matched pattern from `nodes.vgo`
- Assigned roles and modules
- Merged variables (secrets redacted)

## Key Actions

- **Force push**: Trigger an immediate check-in for this envoy
- **Revoke**: Block the envoy from future check-ins (irreversible without manual intervention)
- **Delete**: Permanently remove the envoy and all its history
- **View effective config**: See exactly what modules and resources are assigned to this envoy
- **View traits**: Inspect discovered facts about the machine

## Data Sources

- Envoy details: FleetIndex (`GetEnvoy()`) or database
- Run history: Database `ListRuns` filtered by envoy ID
- Traits: Database `GetLatestEnvoyTraits` or FleetIndex trait cache
- Config: `ConfigStore.Get(hostname)` with secret redaction
