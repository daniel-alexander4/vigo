
# Compliance reporting

Vigo turns module-level `compliance:` tags into per-framework coverage reports. Modules say what controls they satisfy; the server unions that across the fleet and renders the result through the Web UI and `vigocli compliance` verbs. You publish the resulting report; auditors read it.

## Tag your modules

Every module that satisfies a control gets a `compliance:` block. Use the framework's actual control IDs:

```yaml
# /srv/vigo/stacks/stockpile/modules/openvpn.vgo
name: openvpn
compliance:
  hipaa:       ["164.312(e)(1)", "164.312(e)(2)(ii)"]
  pci-dss:     ["2.3", "4.1"]
  nist-800-53: ["SC-8", "SC-8(1)", "SC-13"]
  iso-27001:   ["A.13.1.1", "A.13.2.1"]
  cis-ubuntu:  ["3.5.1.1", "3.5.1.2"]
resources:
  - …
```

See the [compliance matrix](../reference/compliance-matrix.md) for the canonical control → Vigo-coverage table; use it to figure out which modules satisfy what.

A control counts as covered for an envoy when **any** of the following is true:

1. The envoy has a module tagged with that control.
2. A Connwaer trait check escalates it to satisfied.
3. A waiver applies (declared in `waivers.vgo`).

## Look at the coverage

**Per-framework summary (CLI):**

```bash
vigocli compliance frameworks
vigocli compliance frameworks --framework hipaa
vigocli compliance frameworks --framework pci-dss --format json
```

**Per-envoy detail (CLI):**

```bash
vigocli compliance envoy <envoy-id>
vigocli compliance envoy <envoy-id> --framework soc2
```

**Web UI:** `https://vigo:8443/compliance` — framework cards with coverage %, drill-down to per-control state and per-envoy attribution.

## Produce a report for an auditor

```bash
vigocli compliance report --framework hipaa --format pdf -o hipaa-2026-Q2.pdf
vigocli compliance report --framework soc2 --format pdf -o soc2-2026-Q2.pdf
vigocli compliance report --framework all  --format pdf -o full-2026-Q2.pdf
```

PDF generation runs via WeasyPrint as a subprocess off the hot path. Output includes:

- Executive summary with overall coverage % per framework.
- Per-control table: control ID, description, status (covered / partial / uncovered / waived), satisfying modules, attesting envoys, last-converged timestamp.
- Audit-trail extract: every change to the framework's coverage status over the report window, hash-chained for tamper-evidence.

For `--format json` (machine-readable) and `--format csv` (spreadsheet-friendly), the same data with different shaping. Use these to feed GRC platforms.

## GRC platform push (Vanta, Drata, Secureframe, Laika, Thoropass, ServiceNow GRC)

Vigo's GRC exporters format per-control pass/fail evidence and POST it to your GRC platform on a schedule. Configure in `server.yaml`:

```yaml
grc:
  enabled: true
  vendor: vanta              # or drata | secureframe | laika | thoropass | servicenow | generic
  endpoint: "https://api.vanta.com/v1/..."
  token: "secret:grc/vanta/token"
  push_interval: "1h"
  frameworks: [soc2, iso-27001]
```

See `reference/server-yaml.md` for the full per-vendor schema.

## Waivers

Some controls can't be satisfied by automation (process controls, physical-security controls, BCP exercises). Declare a waiver:

```yaml
# /srv/vigo/stacks/stockpile/waivers.vgo
waivers:
  - control: "pci-dss:9.1"           # physical access to cardholder data env
    rationale: "Co-located in SOC-2 audited DC; vendor-provided physical security."
    expires: "2027-04-01"
    owner: "compliance@example.com"
  - control: "nist-800-53:CP-4"      # contingency-plan testing
    rationale: "Annual DR exercise per BCP-2026-03; next exercise 2026-09."
    expires: "2027-03-01"
    owner: "compliance@example.com"
```

Waivers show up in the per-control attribution as the reason for coverage. Expiring waivers fire a notification 30 days before expiry.

## Roles and access

- **Admin role** sees everything and can export every report.
- **Compliance role** (`compliance.read` + `compliance.docs.write` by default) sees the compliance UI and can upload documented-scope artifacts; it deliberately can't read the raw audit log (`audit.read` is admin/viewer only).
- **Viewer role** sees coverage but not raw audit data.

## Per-framework regulatory checklists

The framework matrix tells you what controls each framework requires and which Vigo modules satisfy them. The checklists below are operator-side preparation work — what evidence to gather, what waivers to file, what conversations to have with the auditor — that goes beyond what Vigo enforces directly.

### HIPAA Security Rule checklist

1. **Inventory PHI flows.** List every envoy that touches PHI; tag them with a `phi: true` matchfile label. Vigo doesn't infer PHI scope — you declare it.
2. **§164.312(a)(1) access control.** Run the `user` and `sudo` modules; tag them with the relevant HIPAA control. Verify per-envoy via `vigocli compliance envoy <id> --framework hipaa`.
3. **§164.312(b) audit controls.** Vigo's hash-chained audit log satisfies the "audit mechanisms" requirement; export with `vigocli audit export --since 2026-01-01 --to 2026-03-31 --format pdf`.
4. **§164.312(c) integrity controls.** File-integrity-monitoring modules; pair with Connwaer's WORM verification if you're using it.
5. **§164.312(e)(1) transmission security.** mTLS is enforced by construction; document with `vigocli compliance report --framework hipaa`.
6. **Business Associate Agreement.** Outside Vigo's scope. Maintain in your contract repository; link from the compliance report.
7. **Annual risk assessment.** Vigo's `/risk` page produces fleet-wide scoring; export with `vigocli risk report --format pdf`.

### SOC 2 Type II checklist

1. **Define your trust services criteria scope.** Security, Availability, Processing Integrity, Confidentiality, Privacy — pick the ones in scope and tag modules accordingly.
2. **Common Criteria (CC) controls.** Most CC controls map to platform hardening (`security-hardening` module set), monitoring (`set-up-monitoring`), and audit (the built-in audit log). Tag and export.
3. **Continuous monitoring requirement.** Vigo's 15-second-to-5-minute convergence interval IS your continuous monitoring; document the interval and drift-correction behavior in your SOC 2 narrative.
4. **Change management.** `vigocli config publish` produces a hash-chained audit trail; export it for the auditor's change-management sample.
5. **Subservice organizations.** Outside Vigo's scope.
6. **Annual penetration test.** Outside Vigo's scope; results feed into the report as evidence.

### PCI-DSS checklist

1. **CDE scoping.** Tag every envoy in the cardholder-data environment (`cde: true`). Modules that target CDE envoys carry the `pci-dss:` compliance tag.
2. **Requirement 1 — firewall.** Vigo doesn't replace your firewall; it can verify host-firewall state via the `firewall` executor or Connwaer's network-segmentation check.
3. **Requirement 2 — defaults.** The `security-hardening` module disables default accounts and changes default passwords; cross-reference via `vigocli compliance envoy <id> --framework pci-dss`.
4. **Requirement 6 — secure dev.** Vigo's build provenance (binary signatures, supply-chain attestations) is captured in the audit log.
5. **Requirement 10 — logging.** mTLS-secured audit shipping to a SIEM; the `integrations` config covers Splunk, Datadog, generic syslog.
6. **Quarterly ASV scans.** Outside Vigo's scope.

### NY DFS Cyber (23 NYCRR 500) checklist

1. **§500.02 cybersecurity program.** Vigo's framework matrix is the artifact.
2. **§500.05 penetration testing.** Outside Vigo's scope; feed results into the report.
3. **§500.06 audit trail.** Hash-chained audit log; configurable retention via `audit.retention_days` in `server.yaml`.
4. **§500.09 risk assessment.** Vigo's risk-scoring engine; export with `vigocli risk report`.
5. **§500.11 third-party policy.** Outside Vigo's scope.
6. **§500.16 incident response.** Vigo's `notify` actions + integrations push to PagerDuty/Slack/ServiceNow — document in your IRP.

### Decommissioning an envoy (audit trail)

1. **Revoke first, delete later.** `vigocli envoys revoke <id>` — the envoy can no longer check in, but the historical record stays.
2. **Wipe sensitive state** if you're physically reusing the hardware: `vigo swarm puddle leave --yes` on the envoy (if puddle membership existed); standard NIST 800-88 wipe procedure for the disk.
3. **Document the decommission** with a final compliance report extract: `vigocli compliance envoy <id> --framework all --format pdf -o decommission-<hostname>-2026-Q2.pdf`.
4. **Delete the envoy record** after your retention window: `vigocli envoys delete <id>`.

### Incident response checklist

1. **Detection.** Drift alerts, the `/risk` page, CVE-impact search — all surface anomalies. Configure `drift-alerting` and `cve-impact` integrations to feed PagerDuty / Slack.
2. **Triage with Scrier.** `vigocli scrier ssh <suspect-envoy>` — no inbound port needed, every session is audit-logged.
3. **Contain.** `vigocli envoys quarantine <id>` revokes the envoy's enrollment and pauses convergence; reverses cleanly with `unquarantine`.
4. **Forensic snapshot.** `vigocli envoys snapshot <id>` exports the envoy's full trait + run-history + config-resolution chain at the moment of the incident.
5. **Reset and re-enroll.** If the envoy is compromised: wipe and re-bootstrap; the new envoy gets a fresh ED25519 keypair, so the compromised host can't impersonate it on re-enrollment.
6. **Post-mortem.** Audit-log extract over the incident window; attach to your incident ticket.

---
*Confidential — Alexander4, LLC. Not for redistribution. See [../legal/license.md](../legal/license.md).*
