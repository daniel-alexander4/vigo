
# vigocli export

Export compliance data from the Vigo server in formats suitable for SIEM, CMDB, and OSCAL tools.

## Usage

```bash
vigocli export <subcommand> [flags]
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
vigocli export siem [flags]
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
vigocli export siem

# Events since midnight for a specific envoy
vigocli export siem --since 2026-03-13T00:00:00Z --envoy web-01.example.com

# Last 100 events, saved to a file
vigocli export siem --limit 100 --output events.json
```

---

## cmdb

Export fleet inventory as a JSON array of CMDB records.

```bash
vigocli export cmdb [flags]
```

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*

---

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--status` | | (all) | Filter by convergence status: `converged`, `relapsed`, `diverged`, `failed`, `offline`, `no data` |
| `--environment` | | (all) | Filter by environment (e.g., `prod`, `staging`) |
| `--output` | | stdout | Write output to a file |

### Examples

```bash
# All envoys
vigocli export cmdb

# Only production envoys in compliant state
vigocli export cmdb --status compliant --environment prod

# Save to file
vigocli export cmdb --output fleet.json
```

---

## oscal

Export an OSCAL Assessment Results envelope.

```bash
vigocli export oscal [flags]
```

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--output` | | stdout | Write output to a file |

### Examples

```bash
# Print to stdout
vigocli export oscal

# Save to file
vigocli export oscal --output assessment.json
```

---

## Related

- [Compliance Export](../../compliance/compliance-export.md) -- configuration and integration guide
- [REST API export endpoints](../api/rest.md#export) -- API reference
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
