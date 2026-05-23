
# vigocli compliance

Umbrella command for compliance reporting, evidence export, and remediation workflows. The actual work is done by subcommands registered from their own CLI modules:

| Subcommand | Purpose | Full reference |
|---|---|---|
| `vigocli compliance report` | Generate HIPAA / SOC 2 / PCI DSS / NIST 800-53 / ISO 27001 / etc. compliance reports (JSON, HTML, OSCAL) | [report](report.md) |
| `vigocli compliance export` | Export SIEM, CMDB, and OSCAL feeds | [export](export.md) |
| `vigocli compliance provisions` | List the Vigo-curated provision catalog used by modules' `provides:` field | (see below) |

Run `vigocli compliance --help` on a live server for the current set of subcommands.

## provisions

Lists the Vigo-curated catalog used by modules' `provides:` field. Each provision is a kebab-case tag (e.g. `time-sync`, `audit-trail`) that the loader expands into framework → control coverage. Adding `provides: [time-sync]` to a module's header replaces a hand-rolled `compliance:` block listing the same controls across HIPAA / PCI / NIST / ISO / CIS — the cross-walk lives once, in this catalog.

The catalog is code-curated (`server/compliance/provisions.go`); the command reads it from the binary, so it works offline and on a fresh server.

```bash
vigocli compliance provisions                       # list all known provisions
vigocli compliance provisions --framework pci-dss   # only provisions that cover PCI controls
vigocli compliance provisions --control 10.6.1      # which provisions satisfy a specific control
vigocli compliance provisions --show time-sync      # full framework -> controls for one provision
```

### Flags

| Flag | Description |
|------|-------------|
| `--framework <slug>` | Filter to provisions that cover this framework (e.g. `pci-dss`, `nist-800-53`). |
| `--control <id>` | Filter to provisions that include this control ID (e.g. `10.6.1`, `AU-8`). |
| `--show <name>` | Print the full framework → controls map for one provision. |

### Example output

```text
$ vigocli compliance provisions --show time-sync
time-sync  (System time synchronization)

  Installs and runs a time-sync daemon (chrony or ntpd) that keeps the
  system clock aligned with an authoritative source. Required for
  audit-log timestamp correlation across the fleet.

  cis-rhel:    2.1.1.1, 2.1.1.2
  cis-ubuntu:  2.1.1.1, 2.1.1.2
  iso-27001:   A.12.4.4
  nist-800-53: AU-8
  pci-dss:     10.6.1, 10.6.2, 10.6.3
```

## See also

- [Compliance](../compliance-matrix.md) — conceptual overview of how Vigo measures compliance
- [Compliance Report](../compliance-matrix.md) — HIPAA-specific report format
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../../legal/license.md).*
