
---
title: HIPAA Compliance Checklist
---

# HIPAA Compliance Checklist

This checklist maps HIPAA Security Rule requirements (45 CFR 164.312) to Vigo's technical controls. Use it to verify your deployment meets HIPAA requirements when managing systems that process or store ePHI.

## Administrative Safeguards (164.308)

| # | Requirement | Standard | Status | Vigo Implementation |
|---|-------------|----------|--------|----------------------|
| 1 | Security management process | 164.308(a)(1) | Built-in | Tamper-evident audit trail, compliance dashboards |
| 2 | Risk analysis | 164.308(a)(1)(ii)(A) | Manual | Document risk analysis using audit logs + compliance reports |
| 3 | Risk management | 164.308(a)(1)(ii)(B) | Built-in | Drift detection, automatic remediation, compliance thresholds |
| 4 | Sanction policy | 164.308(a)(1)(ii)(C) | Manual | Organizational policy (not a technical control) |
| 5 | Information system activity review | 164.308(a)(1)(ii)(D) | Built-in | `vigocli audit list`, audit chain verification |
| 6 | Workforce security | 164.308(a)(3) | Built-in | RBAC (admin/viewer), user management |
| 7 | Security awareness training | 164.308(a)(5) | Manual | Organizational responsibility |
| 8 | Password management | 164.308(a)(5)(ii)(D) | Built-in | 12-char minimum, complexity rules, bcrypt hashing |
| 9 | Security incident procedures | 164.308(a)(6) | Documented | See [Incident Response Plan](incident-response.md) |
| 10 | Contingency plan | 164.308(a)(7) | Built-in | Automated backups (Litestream), disaster recovery docs |
| 11 | Business associate contracts | 164.308(b) | Template | See [BAA Template](baa-template.md) |

## Physical Safeguards (164.310)

| # | Requirement | Standard | Status | Vigo Implementation |
|---|-------------|----------|--------|----------------------|
| 12 | Facility access controls | 164.310(a) | Manual | Organizational responsibility (data center access) |
| 13 | Workstation use | 164.310(b) | Manual | Organizational policy |
| 14 | Workstation security | 164.310(c) | Partial | Session timeout (15 min idle) enforces logoff |
| 15 | Device and media controls | 164.310(d)(1) | Documented | See [Decommissioning Procedures](decommissioning.md) |
| 16 | Disposal | 164.310(d)(2)(i) | Documented | Node and server decommission procedures |
| 17 | Media re-use | 164.310(d)(2)(ii) | Documented | Secure-erase guidance in decommissioning docs |

## Technical Safeguards (164.312)

| # | Requirement | Standard | Status | Vigo Implementation |
|---|-------------|----------|--------|----------------------|
| 18 | Access control | 164.312(a)(1) | Built-in | Authentication required on all access, RBAC |
| 19 | Unique user identification | 164.312(a)(2)(i) | Built-in | Per-user accounts with UUID, username uniqueness enforced |
| 20 | Emergency access procedure | 164.312(a)(2)(ii) | Built-in | Localhost trust + `vigocli auth emergency-access` with audit trail |
| 21 | Automatic logoff | 164.312(a)(2)(iii) | Built-in | 15-minute idle session timeout (configurable) |
| 22 | Encryption and decryption | 164.312(a)(2)(iv) | Built-in | AES-256-GCM secrets, AES-256-GCM backup encryption |
| 23 | Audit controls | 164.312(b) | Built-in | SHA-256 hash chain audit trail, auth/user/token events |
| 24 | Integrity | 164.312(c)(1) | Built-in | ED25519 signatures, hash chain tamper detection |
| 25 | Authentication | 164.312(d) | Built-in | Basic (bcrypt), OIDC, isowebauth; API tokens (bcrypt at rest) |
| 26 | Transmission security | 164.312(e)(1) | Built-in | mTLS (TLS 1.3) on all gRPC and REST traffic |
| 27 | Encryption (transmission) | 164.312(e)(2)(ii) | Built-in | No plaintext transport ever — mTLS enforced |

## Quick Start

```bash
cp example-configs/modules/compliance/hipaa/*.vgo.example /srv/vigo/stockpile/modules/
for f in /srv/vigo/stockpile/modules/hipaa-*.vgo.example; do mv "$f" "${f%.example}"; done
cp example-configs/compliance-roles.vgo.example /srv/vigo/stockpile/compliance-roles.vgo
```

Assign the `hipaa` role to nodes that handle ePHI:

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*

---

```yaml
envoys:
  - match: "*.hipaa.example.com"
    roles: [hipaa]
```

Then publish and verify: `vigocli config publish && vigocli report compliance`

## Deployment Verification Checklist

Run these checks on your production deployment:

```bash
# 1. Verify auth is enabled (not "none")
grep -A1 "method:" /srv/vigo/server.yaml

# 2. Verify TLS certificates exist
ls -la /srv/vigo/tls/

# 3. Verify secrets are encrypted (not plaintext)
file /srv/vigo/secrets/vigo/web/auth/admin
# Should show "data", not readable text

# 4. Verify audit chain integrity
vigocli audit verify

# 5. Check session idle timeout
grep session_idle_timeout /srv/vigo/server.yaml
# Default: 15m (HIPAA compliant)

# 6. Verify backup encryption
ls -la /path/to/backup/*.sha256

# 7. List recent auth events (verify logging is working)
vigocli audit list --type auth.login --since 24h

# 8. Verify no plaintext secrets in config
grep -r "password" /srv/vigo/stockpile/ | grep -v "secret:" | grep -v "#"
# Should return nothing — all secrets use secret: prefix

# 9. Check user accounts
vigocli webusers list
# Verify only authorized users exist

# 10. Verify gRPC uses mTLS
openssl s_client -connect localhost:1530 </dev/null 2>/dev/null | grep "Protocol"
# Should show TLSv1.3
```

## Audit Event Types

All these events are recorded in the tamper-evident audit chain:

| Event Type | Description | HIPAA Mapping |
|------------|-------------|---------------|
| `auth.login` | Successful authentication | 164.312(b) |
| `auth.login_failed` | Failed authentication attempt | 164.312(b) |
| `auth.logout` | User logout | 164.312(b) |
| `user.create` | User account created | 164.312(b) |

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*

---
| `user.delete` | User account deleted | 164.312(b) |
| `user.role_change` | User role modified | 164.312(b) |
| `token.create` | API token created | 164.312(b) |
| `token.revoke` | API token revoked | 164.312(b) |
| `emergency.access` | Emergency access invoked | 164.312(a)(2)(ii) |
| `envoy.revoke` | Managed node revoked | 164.312(b) |
| `envoy.delete` | Managed node deleted (cascade) | 164.310(d)(2)(i) |
| `config.publish` | Configuration published | 164.312(b) |
| `config.rollback` | Configuration rolled back | 164.312(b) |
| `task.*` | Ad-hoc command dispatched | 164.312(b) |

## Gap Analysis

Items that require organizational policies (not technical controls):

1. **Risk analysis documentation** — conduct and document annually
2. **Sanction policy** — define consequences for HIPAA violations
3. **Security awareness training** — train workforce on HIPAA requirements
4. **Facility access controls** — physical security of server infrastructure
5. **Incident response testing** — test the incident response plan annually
6. **Business associate agreements** — execute BAAs with all subcontractors

## References

- [HIPAA Security Rule](https://www.hhs.gov/hipaa/for-professionals/security/index.html)
- [NIST SP 800-66](https://csrc.nist.gov/publications/detail/sp/800-66/rev-2/final) — Implementing the HIPAA Security Rule
- [Emergency Access Procedure](emergency-access.md)
- [Decommissioning Procedures](decommissioning.md)
- [BAA Template](baa-template.md)
- [Incident Response Plan](incident-response.md)
- [Authentication](../operations/authentication.md)
- [Tamper-Evident Audit Trail](tamper-evident-audit.md)
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
