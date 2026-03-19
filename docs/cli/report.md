# vigogo report

Generate compliance reports mapped to security frameworks.

## Subcommands

### compliance

Generate a HIPAA compliance report with 164.312 control mappings, fleet status, and audit chain verification.

```bash
vigogo report compliance [flags]
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--format` | `json` | Output format: `json` or `html` |
| `--output` | | Write to file instead of stdout |

**Examples:**

```bash
# JSON to stdout
vigogo report compliance

# HTML report to file
vigogo report compliance --format html --output /tmp/report.html

# JSON to file
vigogo report compliance --format json --output /tmp/compliance.json
```

### hitrust

Generate a HITRUST CSF compliance report with control mappings across 8 domains (01 Access Control, 06 Compliance, 09 Operations, 10 Maintenance, etc.).

```bash
vigogo report hitrust [flags]
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--format` | `json` | Output format: `json` or `html` |
| `--output` | | Write to file instead of stdout |

**Examples:**

```bash
vigogo report hitrust
vigogo report hitrust --format html --output /tmp/hitrust-report.html
```

### soc2

Generate a SOC 2 Trust Services Criteria compliance report (CC1-CC9 Common Criteria, A1 Availability, C1 Confidentiality, PI1 Processing Integrity).

```bash
vigogo report soc2 [flags]
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--format` | `json` | Output format: `json` or `html` |
| `--output` | | Write to file instead of stdout |

**Examples:**

```bash
vigogo report soc2
vigogo report soc2 --format html --output /tmp/soc2-report.html
```

### pci

Generate a PCI DSS v4.0 compliance report (Req 1-12: network security, config hardening, access control, audit trails, file integrity monitoring).

```bash
vigogo report pci [flags]
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--format` | `json` | Output format: `json` or `html` |
| `--output` | | Write to file instead of stdout |

**Examples:**

```bash
vigogo report pci
vigogo report pci --format html --output /tmp/pci-report.html
```

### cis

Generate a CIS Benchmark compliance report for Ubuntu, RHEL, and Windows Server.

```bash
vigogo report cis [flags]
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--format` | `json` | Output format: `json` or `html` |
| `--output` | | Write to file instead of stdout |

**Examples:**

```bash
vigogo report cis
vigogo report cis --format html --output /tmp/cis-report.html
```

### nist

Generate a NIST 800-53 compliance report (41 controls across 10 families — AC, AU, CA, CM, CP, IA, IR, SC, SI).

```bash
vigogo report nist
vigogo report nist --format html --output /tmp/nist-report.html
```

### iso27001

Generate an ISO 27001 compliance report (33 Annex A controls across 11 domains).

```bash
vigogo report iso27001
vigogo report iso27001 --format html --output /tmp/iso27001-report.html
```

### gdpr

Generate a GDPR compliance report (13 articles covering technical measures under Art. 5, 17, 25, 30, 32, 33, 35).

```bash
vigogo report gdpr
vigogo report gdpr --format html --output /tmp/gdpr-report.html
```

### nerccip

Generate a NERC CIP compliance report (18 requirements across 8 CIP standards for energy sector).

```bash
vigogo report nerccip
vigogo report nerccip --format html --output /tmp/nerccip-report.html
```

### executive

Generate an executive compliance summary for non-technical audiences. Includes compliance score with rating, 30-day trend chart, framework coverage at a glance, fleet summary with friendly labels, top 10 issues in plain English, and audit chain status. The HTML version is designed for printing as PDF via Ctrl+P.

```bash
vigogo report executive [flags]
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--format` | `json` | Output format: `json` or `html` |
| `--output` | | Write to file instead of stdout |

**Examples:**

```bash
# JSON to stdout
vigogo report executive

# Print-friendly HTML
vigogo report executive --format html --output /tmp/executive-summary.html

# JSON to file
vigogo report executive --format json --output /tmp/executive.json
```
