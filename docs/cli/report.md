# hobnobcli report

Generate compliance reports mapped to security frameworks.

## Subcommands

### compliance

Generate a HIPAA compliance report with 164.312 control mappings, fleet status, and audit chain verification.

```bash
hobnobcli report compliance [flags]
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--format` | `json` | Output format: `json` or `html` |
| `--output` | | Write to file instead of stdout |

**Examples:**

```bash
# JSON to stdout
hobnobcli report compliance

# HTML report to file
hobnobcli report compliance --format html --output /tmp/report.html

# JSON to file
hobnobcli report compliance --format json --output /tmp/compliance.json
```

### hitrust

Generate a HITRUST CSF compliance report with control mappings across 8 domains (01 Access Control, 06 Compliance, 09 Operations, 10 Maintenance, etc.).

```bash
hobnobcli report hitrust [flags]
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--format` | `json` | Output format: `json` or `html` |
| `--output` | | Write to file instead of stdout |

**Examples:**

```bash
hobnobcli report hitrust
hobnobcli report hitrust --format html --output /tmp/hitrust-report.html
```
