# hobnobcli report

Generate HIPAA compliance reports.

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
