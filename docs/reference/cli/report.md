
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

Generate a NIST 800-53 compliance report (287 Moderate-baseline controls across 20 families: AC, AT, AU, CA, CM, CP, IA, IR, MA, MP, PE, PL, PM, PS, PT, RA, SA, SC, SI, SR).

```bash
vigocli report nist
vigocli report nist --format html --output /tmp/nist-report.html
```

### iso27001

Generate an ISO 27001 compliance report (93 Annex A controls across the 2022-revision themes: organizational, people, physical, technological).

```bash
vigocli report iso27001
vigocli report iso27001 --format html --output /tmp/iso27001-report.html
```

### gdpr

Generate a GDPR compliance report (46 articles covering technical and organizational measures, data subject rights, breach handling, DPO, and transfers).

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

### audit-bundle

Download a self-contained zip suitable for handing to an external auditor: HTML + PDF report, per-control evidence, audit-chain entries, fleet convergence history, and decrypted copies of every uploaded compliance document linked to the framework's controls.

```bash
vigocli report audit-bundle <framework> [flags]
```

**Args:**

| Arg | Description |
|-----|-------------|
| `framework` | Framework slug — `compliance` (HIPAA), `soc2`, `pci`, `nist`, `iso27001`, `gdpr`, `cisubuntu`, etc. See `vigocli report --help` for the full list. |

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--period` | `365d` | Lookback window for `audit-log.csv` and `convergence.csv`. Accepts `Nd` (days), `Nmo` (30-day months), `Ny` (365-day years), or a bare integer (days). |
| `--output` | `vigo-audit-bundle-<framework>.zip` | Local path to write the zip. |

**Permissions:** `audit.read` is mandatory; `compliance.read` is checked separately on the server and gates inclusion of the `docs/` directory in the bundle. Without `compliance.read`, the bundle still ships but its `docs/` folder is empty and `evidence.json` omits the per-control document list.

**Examples:**

```bash
# Default: 365-day HIPAA bundle, written to ./vigo-audit-bundle-compliance.zip
vigocli report audit-bundle compliance

# Quarterly PCI snapshot
vigocli report audit-bundle pci --period 90d --output pci-q1.zip

# Annual NIST 800-53 evidence package
vigocli report audit-bundle nist --period 12mo --output nist-annual.zip
```

**Verify the bundle after extraction:**

```bash
unzip vigo-audit-bundle-compliance.zip -d audit/
cd audit
jq -r '.artifacts[] | "\(.sha256)  \(.path)"' manifest.json | sha256sum -c
```

Every line should print `<path>: OK`. A mismatch indicates the bundle was tampered with after generation.

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
