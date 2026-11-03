
# vigocli compliance

Umbrella command for compliance reporting, evidence export, and remediation workflows. The actual work is done by subcommands registered from their own CLI configcrates:

| Subcommand | Purpose | Full reference |
|---|---|---|
| `vigocli compliance report` | Generate HIPAA / SOC 2 / PCI DSS / NIST 800-53 / ISO 27001 / etc. compliance reports (JSON, HTML, OSCAL) | [report](report.md) |
| `vigocli compliance export` | Export SIEM, CMDB, and OSCAL feeds | [export](export.md) |
| `vigocli compliance provisions` | List the Vigo-curated provision catalog used by configcrates' `provides:` field | (see below) |
| `vigocli compliance docs` | Manage operator-uploaded compliance evidence documents (upload, list, link, delete) | (see below) |

Run `vigocli compliance --help` on a live server for the current set of subcommands.

## provisions

Lists the Vigo-curated catalog used by configcrates' `provides:` field. Each provision is a kebab-case tag (e.g. `time-sync`, `audit-trail`) that the loader expands into framework → control coverage. Adding `provides: [time-sync]` to a configcrate's header replaces a hand-rolled `compliance:` block listing the same controls across HIPAA / PCI / NIST / ISO / CIS — the cross-walk lives once, in this catalog.

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

## docs

Manage operator-uploaded compliance **evidence documents** — policies, contracts, IR plans, BAAs, and other artifacts that satisfy `documented`-scope controls. They're envelope-encrypted at rest under the master key (so this requires the **local** secrets backend). The web UI displays these **read-only**; every write goes through this command — the single action path.

```bash
vigocli compliance docs upload ir-plan.pdf --type "IR Plan"                       # upload an artifact
vigocli compliance docs upload baa.pdf --type BAA --link hipaa:164.308 --link soc2:CC1.1   # upload + link to controls
vigocli compliance docs list                                                      # list all docs
vigocli compliance docs list --framework hipaa --control 164.308                  # docs for one control
vigocli compliance docs download <id> -o evidence.pdf                             # download (default: server filename)
vigocli compliance docs link <id> hipaa 164.308                                   # link an existing doc to a control
vigocli compliance docs unlink <id> hipaa 164.308                                 # remove a link
vigocli compliance docs delete <id>                                               # soft delete
vigocli compliance docs delete <id> --purge                                       # permanently remove
```

`--link` takes `framework:control` and is repeatable. Uploads cap at 50 MiB; accepted types include PDF, DOCX, XLSX, PPTX, TXT, MD, PNG, JPG, SVG.

## See also

- [Compliance](../compliance-matrix.md) — conceptual overview of how Vigo measures compliance
- [Compliance Report](../compliance-matrix.md) — HIPAA-specific report format
