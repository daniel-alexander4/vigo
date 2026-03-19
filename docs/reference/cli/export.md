# vigogo export

Export compliance data from the Vigo server in formats suitable for SIEM, CMDB, and OSCAL tools.

## Usage

```bash
vigogo export <subcommand> [flags]
```

## Subcommands

| Subcommand | Description |
|------------|-------------|
| `siem` | Export run results as SIEM events |
| `cmdb` | Export fleet inventory as CMDB records |
| `oscal` | Export OSCAL Assessment Results envelope |

---

## siem

Export run results as a JSON array of SIEM events.

```bash
vigogo export siem [flags]
```

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--since` | | 24 hours ago | RFC 3339 timestamp. Only events after this time are returned |
| `--envoy` | | (all) | Filter by hostname |
| `--limit` | | 1000 | Maximum events to return (max 10000) |
| `--output` | | stdout | Write output to a file |

### Examples

```bash
# Last 24 hours of events
vigogo export siem

# Events since midnight for a specific envoy
vigogo export siem --since 2026-03-13T00:00:00Z --envoy web-01.example.com

# Last 100 events, saved to a file
vigogo export siem --limit 100 --output events.json
```

---

## cmdb

Export fleet inventory as a JSON array of CMDB records.

```bash
vigogo export cmdb [flags]
```

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--status` | | (all) | Filter by compliance status: `compliant`, `drifted`, `failing`, `stale` |
| `--environment` | | (all) | Filter by environment (e.g., `prod`, `staging`) |
| `--output` | | stdout | Write output to a file |

### Examples

```bash
# All envoys
vigogo export cmdb

# Only production envoys in compliant state
vigogo export cmdb --status compliant --environment prod

# Save to file
vigogo export cmdb --output fleet.json
```

---

## oscal

Export an OSCAL Assessment Results envelope.

```bash
vigogo export oscal [flags]
```

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--output` | | stdout | Write output to a file |

### Examples

```bash
# Print to stdout
vigogo export oscal

# Save to file
vigogo export oscal --output assessment.json
```

---

## Related

- [Compliance Export](../../compliance/compliance-export.md) -- configuration and integration guide
- [REST API export endpoints](../api/rest.md#export) -- API reference
