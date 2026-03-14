# Security

The thrall detail page at `/thralls/{id}` serves as the security information view for individual managed nodes. It shows enrollment credentials, compliance status, and audit history.

## Layout

### Thrall Header

- Hostname, IP address, enrollment date, last seen timestamp
- Compliance status badge
- Revocation status

### Identity Section

- **Thrall UUID**: The server-assigned identifier
- **Public Key**: ED25519 public key registered at enrollment
- **Enrolled At**: When the thrall was first enrolled
- **Revoked**: Whether the thrall is revoked and when

### Run History

A table of recent convergence runs for this thrall:

| Column | Description |
|--------|-------------|
| Run ID | Link to run detail |
| Status | Success/failure/partial badge |
| Modules | Module count |
| Changed | Resources changed |
| Failed | Resources failed |
| Time | Run timestamp |

### Traits

The most recently collected traits (facts) for this thrall, displayed as a nested JSON tree. Includes OS information, hardware details, network configuration, installed packages, and custom traits.

### Configuration

The effective resolved configuration for this thrall, showing:
- Matched pattern from `nodes.hnb`
- Assigned roles and modules
- Merged variables (secrets redacted)

## Key Actions

- **Force push**: Trigger an immediate check-in for this thrall
- **Revoke**: Block the thrall from future check-ins (irreversible without manual intervention)
- **Delete**: Permanently remove the thrall and all its history
- **View effective config**: See exactly what modules and resources are assigned to this thrall
- **View traits**: Inspect discovered facts about the machine

## Data Sources

- Thrall details: FleetIndex (`GetThrall()`) or database
- Run history: Database `ListRuns` filtered by thrall ID
- Traits: Database `GetLatestThrallTraits` or FleetIndex trait cache
- Config: `ConfigStore.Get(hostname)` with secret redaction
