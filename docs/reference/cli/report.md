
# vigocli report

Generate compliance reports mapped to security frameworks.

## Subcommands

### compliance

Generate a HIPAA compliance report with 164.312 control mappings, fleet status, and audit chain verification.

```bash
vigocli report compliance [flags]
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--format` | `json` | Output format: `json` or `html` |
| `--output` | | Write to file instead of stdout |

**Examples:**

```bash
# JSON to stdout
vigocli report compliance

# HTML report to file
vigocli report compliance --format html --output /tmp/report.html

# JSON to file
vigocli report compliance --format json --output /tmp/compliance.json
```

### hitrust

Generate a HITRUST CSF compliance report with control mappings across 8 domains (01 Access Control, 06 Compliance, 09 Operations, 10 Maintenance, etc.).

```bash
vigocli report hitrust [flags]
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--format` | `json` | Output format: `json` or `html` |
| `--output` | | Write to file instead of stdout |

**Examples:**

```bash
vigocli report hitrust
vigocli report hitrust --format html --output /tmp/hitrust-report.html
```

### soc2

Generate a SOC 2 Trust Services Criteria compliance report (CC1-CC9 Common Criteria, A1 Availability, C1 Confidentiality, PI1 Processing Integrity).

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*

---

```bash
vigocli report soc2 [flags]
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--format` | `json` | Output format: `json` or `html` |
| `--output` | | Write to file instead of stdout |

**Examples:**

```bash
vigocli report soc2
vigocli report soc2 --format html --output /tmp/soc2-report.html
```

### pci

Generate a PCI DSS v4.0 compliance report (Req 1-12: network security, config hardening, access control, audit trails, file integrity monitoring).

```bash
vigocli report pci [flags]
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--format` | `json` | Output format: `json` or `html` |
| `--output` | | Write to file instead of stdout |

**Examples:**

```bash
vigocli report pci
vigocli report pci --format html --output /tmp/pci-report.html
```

### cis

Generate a CIS Benchmark compliance report for Ubuntu, RHEL, and Windows Server.

```bash
vigocli report cis [flags]
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--format` | `json` | Output format: `json` or `html` |
| `--output` | | Write to file instead of stdout |

**Examples:**

```bash
vigocli report cis
vigocli report cis --format html --output /tmp/cis-report.html
```

### nist

Generate a NIST 800-53 compliance report (41 controls across 10 families — AC, AU, CA, CM, CP, IA, IR, SC, SI).

```bash
vigocli report nist
vigocli report nist --format html --output /tmp/nist-report.html
```

### iso27001

Generate an ISO 27001 compliance report (33 Annex A controls across 11 domains).

```bash
vigocli report iso27001
vigocli report iso27001 --format html --output /tmp/iso27001-report.html
```

### gdpr

Generate a GDPR compliance report (13 articles covering technical measures under Art. 5, 17, 25, 30, 32, 33, 35).

```bash
vigocli report gdpr
vigocli report gdpr --format html --output /tmp/gdpr-report.html
```

### nerccip

Generate a NERC CIP compliance report (18 requirements across 8 CIP standards for energy sector).

```bash
vigocli report nerccip
vigocli report nerccip --format html --output /tmp/nerccip-report.html
```

### cyberessentials

Generate a Cyber Essentials+ compliance report (29 controls across 5 themes — UK Government cybersecurity certification).

```bash
vigocli report cyberessentials
vigocli report cyberessentials --format html --output /tmp/ce-report.html
```

### executive

Generate an executive compliance summary for non-technical audiences. Includes compliance score with rating, 30-day trend chart, framework coverage at a glance, fleet summary with friendly labels, top 10 issues in plain English, and audit chain status. The HTML version is designed for printing as PDF via Ctrl+P.

```bash
vigocli report executive [flags]
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--format` | `json` | Output format: `json` or `html` |
| `--output` | | Write to file instead of stdout |

**Examples:**

```bash
# JSON to stdout
vigocli report executive

# Print-friendly HTML
vigocli report executive --format html --output /tmp/executive-summary.html

# JSON to file
vigocli report executive --format json --output /tmp/executive.json
```
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
