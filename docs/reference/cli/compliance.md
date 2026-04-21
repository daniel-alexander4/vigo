
# vigocli compliance

Umbrella command for compliance reporting, evidence export, and remediation workflows. The actual work is done by subcommands registered from their own CLI modules:

| Subcommand | Purpose | Full reference |
|---|---|---|
| `vigocli compliance report` | Generate HIPAA / SOC 2 / PCI DSS / NIST 800-53 / ISO 27001 / etc. compliance reports (JSON, HTML, OSCAL) | [report](report.md) |
| `vigocli compliance export` | Export SIEM, CMDB, and OSCAL feeds | [export](export.md) |

Run `vigocli compliance --help` on a live server for the current set of subcommands.

## See also

- [Compliance](../../compliance/overview.md) — conceptual overview of how Vigo measures compliance
- [Compliance Report](../../compliance/reporting.md) — HIPAA-specific report format
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
