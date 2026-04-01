
# NY DFS Cybersecurity (23 NYCRR 500)

This framework maps the New York Department of Financial Services cybersecurity regulation to Vigo's technical controls. 23 NYCRR 500 is a mandatory, audited regulation with enforcement actions and penalties.

## Who This Applies To

Any entity operating under or required to operate under a license, registration, charter, certificate, permit, accreditation, or similar authorization under the New York Banking Law, Insurance Law, or Financial Services Law. In practice, this includes:

- Law firms handling data for DFS-regulated financial institutions
- Banks, insurance companies, and financial services firms
- Third-party service providers to covered entities
- Mortgage companies, money transmitters, and licensed lenders

Law firms are particularly affected because most large firms in New York handle data for financial institution clients, making them "third-party service providers" under the regulation.

## Control Mapping

| Control | Title | Status | Vigo Implementation |
|---------|-------|--------|---------------------|
| NYCRR-500.02 | Cybersecurity Program | Partial | Technical controls enforced fleet-wide; written program document is organizational |
| NYCRR-500.03 | Cybersecurity Policy | Partial | Policy enforcement via modules; written policy approved by senior officer is organizational |
| NYCRR-500.05 | Penetration Testing | Partial | Continuous vulnerability scanning and CVE alerting; formal annual pentest is external |
| NYCRR-500.06 | Audit Trail | Enforced | Tamper-evident audit trail, configurable retention up to 6+ years |
| NYCRR-500.07 | Access Privileges | Enforced | Least-privilege user management, sudo restrictions, SSH key-only auth |
| NYCRR-500.08 | Application Security | Partial | Package management and patching; secure SDLC is organizational |
| NYCRR-500.09 | Risk Assessment | Partial | Per-envoy risk scoring, fleet dashboard; formal methodology is organizational |
| NYCRR-500.10 | Cybersecurity Personnel | Partial | Automated enforcement reduces burden; CISO designation is organizational |
| NYCRR-500.11 | Third-Party Security | Partial | Convergence monitoring of vendor nodes; vendor due diligence is organizational |
| NYCRR-500.12 | Multi-Factor Authentication | Partial | PAM MFA for system auth; application-level MFA may be external |
| NYCRR-500.13 | Data Retention and Disposal | Enforced | Retention policies, secure deletion, NIST 800-88 decommission |
| NYCRR-500.14 | Monitoring and Detection | Enforced | Security scanning, drift detection, real-time alerting |
| NYCRR-500.15 | Encryption of Nonpublic Info | Enforced | Disk encryption, TLS enforcement; Connwaer verification |
| NYCRR-500.16 | Incident Response Plan | Partial | Automated detection and alerting; written IRP is organizational |
| NYCRR-500.17 | Annual Certification | Partial | Compliance reports provide evidence; filing with DFS is organizational |

## Example Module Assignment

```yaml
# stack/stage/roles.vgo
roles:
  ny-dfs-cyber:
    modules:
      - nydfs-access-control
      - nydfs-encryption
      - nydfs-audit-trail
      - nydfs-monitoring
```

```yaml
# stack/stage/nodes.vgo
envoys:
  - match: "*.nylawfirm.com"
    roles: [ny-dfs-cyber]
```

## Generating Reports

```bash
# JSON report
vigocli report nydfscyber

# Printable HTML for annual certification filing
vigocli report nydfscyber --format html --output nycrr500-report.html

# Executive summary across all frameworks
vigocli report executive --format html --output executive-compliance.html
```

## Annual Certification (Section 500.17)

Each covered entity must submit an annual certification to DFS confirming compliance. Vigo's report provides the technical evidence supporting the certification:

1. Generate the report: `vigocli report nydfscyber --format html`
2. Review each control's status and evidence
3. Document organizational controls separately (written policies, CISO designation, vendor assessments)
4. The report plus organizational documentation forms the certification evidence package

## What Vigo Cannot Prove

- **Written cybersecurity program and policy** (500.02, 500.03) — organizational documents
- **CISO designation** (500.10) — organizational appointment
- **Formal penetration testing** (500.05) — requires external assessment
- **Third-party vendor assessments** (500.11) — contractual and due diligence processes
- **Incident response procedures** (500.16) — human response plan
- **Annual certification filing** (500.17) — DFS submission process

## Related

- [ABA Cybersecurity](aba-cybersecurity.md) — Baseline attorney obligations (all US jurisdictions)
- [SOC 2](soc2.md) — Overlapping controls for service organizations
- [NIST 800-53](nist-800-53.md) — Federal framework with significant 23 NYCRR 500 overlap
- [Compliance Reporting](reporting.md) — Report generation and export formats

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
