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

### soc2

Generate a SOC 2 Trust Services Criteria compliance report (CC1-CC9 Common Criteria, A1 Availability, C1 Confidentiality, PI1 Processing Integrity).

```bash
hobnobcli report soc2 [flags]
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--format` | `json` | Output format: `json` or `html` |
| `--output` | | Write to file instead of stdout |

**Examples:**

```bash
hobnobcli report soc2
hobnobcli report soc2 --format html --output /tmp/soc2-report.html
```

### pci

Generate a PCI DSS v4.0 compliance report (Req 1-12: network security, config hardening, access control, audit trails, file integrity monitoring).

```bash
hobnobcli report pci [flags]
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--format` | `json` | Output format: `json` or `html` |
| `--output` | | Write to file instead of stdout |

**Examples:**

```bash
hobnobcli report pci
hobnobcli report pci --format html --output /tmp/pci-report.html
```
