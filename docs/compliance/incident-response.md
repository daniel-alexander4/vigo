---
title: Incident Response Plan
---

# Incident Response Plan

This plan covers security incidents involving the Vigo configuration management system and the managed infrastructure it controls. It satisfies HIPAA 164.308(a)(6) — Security Incident Procedures.

## Incident Classification

| Severity | Description | Examples | Response Time |
|----------|-------------|----------|---------------|
| **Critical** | Active breach, data exfiltration, or system compromise | Unauthorized admin access, tampered audit chain, leaked secrets | Immediate |
| **High** | Potential breach or significant security degradation | Repeated failed logins from unknown IPs, revoked node still communicating | < 4 hours |
| **Medium** | Security misconfiguration or policy violation | Auth disabled in production, expired TLS certificates, stale admin accounts | < 24 hours |
| **Low** | Minor security hygiene issue | Unused API tokens, verbose logging enabled | Next business day |

## Detection

### Automated Detection

Vigo provides several built-in detection mechanisms:

**Audit chain integrity:**
```bash
vigogo audit verify
```
A broken chain (`valid: false`) indicates tampering with the audit database.

**Failed login monitoring:**
```bash
vigogo audit list --type auth.login_failed --since 1h
```
Multiple failed logins from the same IP may indicate a brute-force attack.

**Unauthorized access patterns:**
```bash
vigogo audit list --since 24h
```
Review for unexpected `emergency.access`, `user.create`, `user.role_change`, or `token.create` events.

**Fleet compliance drift:**
```bash
vigogo envoys list
```
Sudden compliance drops across multiple nodes may indicate configuration tampering.

**Webhook/SMTP alerts:**
Configure webhooks or SMTP notifications for compliance threshold violations. See [Drift Alerting](../operations/drift-alerting.md).

### Manual Detection

- Review server logs: `journalctl -u vigo` or Docker logs
- Check for unauthorized configuration changes: `vigogo audit list --type config_publish`
- Verify no unauthorized nodes enrolled: `vigogo envoys list`
- Check for leaked secrets: search logs, config exports for plaintext credentials

## Response Procedures

### Phase 1: Containment (Immediate)

**For compromised admin account:**
1. Record emergency access: `vigogo auth emergency-access --reason "Compromised admin account: <username>"`
2. Delete the compromised account: `vigogo webusers delete --username <compromised>`
3. Revoke all API tokens for the account
4. Reset passwords for all admin accounts: `vigogo secrets set-web-password vigo/web/auth/<username>`
5. Review audit trail for actions taken by the compromised account

**For compromised managed node:**
1. Revoke the node immediately: `vigogo envoys revoke <id>`
2. Record the incident: `vigogo auth emergency-access --reason "Compromised node: <hostname>"`
3. SSH to the node and stop the agent: `systemctl stop vigo-envoy`
4. Isolate the node from the network if possible
5. Preserve forensic evidence before remediation

**For compromised server:**
1. Take a backup immediately: `vigogo backup create /tmp/incident-backup.tar.gz --encrypt`
2. Export the full audit trail: `vigogo audit list --limit 10000 > /tmp/audit-export.json`
3. Verify audit chain integrity: `vigogo audit verify`
4. Rotate all secrets: TLS certificates, master encryption key, admin passwords
5. Review all enrolled nodes for unauthorized changes

**For tampered audit chain:**
1. This is always Critical severity — the audit trail is the system of record
2. Export the current database for forensic analysis
3. Identify the first broken entry: the `first_broken` field in verify output
4. Compare against database backups to determine what was changed
5. Restore from the most recent known-good backup

### Phase 2: Investigation

1. **Establish timeline:** Use `vigogo audit list --since <start>` to reconstruct the sequence of events
2. **Identify scope:** Determine which nodes, users, and data were affected
3. **Determine entry point:** Check for:
   - Compromised credentials (failed login patterns preceding successful login)
   - Exploited vulnerabilities (check server logs for unusual requests)
   - Insider threat (legitimate account used outside normal patterns)
4. **Assess data exposure:** Determine if ePHI was accessed, modified, or exfiltrated
5. **Preserve evidence:** Export audit logs, server logs, and database snapshots

### Phase 3: Eradication

1. Remove the attacker's access (accounts, tokens, enrolled nodes)
2. Patch any exploited vulnerabilities
3. Rotate all potentially compromised credentials:
   - Admin passwords
   - API tokens
   - TLS certificates (regenerate CA if CA key compromised)
   - Secrets master encryption key
4. Re-verify audit chain integrity after remediation

### Phase 4: Recovery

1. Restore from a known-good backup if necessary
2. Re-enroll any revoked nodes after verifying their integrity
3. Publish a clean configuration: `vigogo config publish`
4. Monitor closely for 48-72 hours for signs of persistent access
5. Verify all nodes converge to the desired state

### Phase 5: Post-Incident

1. **Document the incident:** timeline, scope, root cause, actions taken
2. **HIPAA breach notification:** If ePHI was compromised, notify:
   - Affected individuals within 60 days
   - HHS Secretary (via breach portal)
   - Media (if >500 individuals affected)
3. **Lessons learned:** Update security controls based on findings
4. **Update this plan:** Incorporate any process improvements

## Communication

| Audience | When | Channel |
|----------|------|---------|
| Incident response team | Immediately on detection | Secure channel (not email) |
| CISO / Security lead | Critical/High: immediately; Medium: same day | Direct contact |
| Legal counsel | Any suspected breach of ePHI | Direct contact |
| Affected individuals | Within 60 days of breach discovery (HIPAA) | Written notification |
| HHS Secretary | Within 60 days of breach discovery (HIPAA) | HHS breach portal |

## Roles and Responsibilities

| Role | Responsibility |
|------|----------------|
| **Incident Commander** | Coordinates response, makes containment decisions |
| **System Administrator** | Executes technical containment and remediation |
| **Security Analyst** | Leads investigation, preserves evidence |
| **Legal/Compliance** | Assesses breach notification requirements |
| **Communications** | Manages external notifications |

## Testing

Test this incident response plan at least annually:

1. **Tabletop exercise:** Walk through a simulated incident scenario
2. **Technical drill:** Practice containment procedures in a test environment
3. **Audit verification:** Run `vigogo audit verify` and review recent events
4. **Backup restoration:** Test restoring from an encrypted backup
5. **Update the plan** based on drill findings

## References

- HIPAA 164.308(a)(6) — Security Incident Procedures
- HIPAA 164.308(a)(6)(ii) — Response and Reporting
- HIPAA 164.402-414 — Breach Notification Rule
- NIST SP 800-61 — Computer Security Incident Handling Guide
- [Emergency Access Procedure](emergency-access.md)
- [Tamper-Evident Audit Trail](tamper-evident-audit.md)
- [Backup & Recovery](../operations/backup.md)
