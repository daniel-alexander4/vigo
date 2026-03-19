---
title: Emergency Access Procedure
---

# Emergency Access Procedure

HIPAA 164.312(a)(2)(ii) requires documented procedures for obtaining access to ePHI during an emergency. This page describes the Hobnob emergency access procedure and the audit controls that support it.

## When to Use Emergency Access

Emergency access applies when the normal authentication path is unavailable:

- **Account lockout** — all admin accounts are locked, disabled, or passwords lost
- **OIDC/isowebauth failure** — the identity provider is down and no API tokens exist
- **System failure** — the server is partially functional and needs manual intervention
- **Incident response** — an active security incident requires immediate access to review or modify configuration

## Procedure

### 1. Access via localhost trust

Hobnob grants implicit admin access to CLI requests originating from the server host (loopback or Docker bridge). This is the emergency access path:

```bash
# SSH to the server host
ssh admin@hobnob-server

# All hobnobcli commands run with implicit admin — no token needed
hobnobcli thralls list
hobnobcli runs list
hobnobcli audit list
```

This works because localhost trust (`127.0.0.1`, `::1`, `172.16.0.0/12`) bypasses all authentication checks — the same model as PostgreSQL peer auth.

### 2. Record the emergency access event

Every use of emergency access **must** be recorded in the tamper-evident audit trail:

```bash
hobnobcli auth emergency-access --reason "OIDC provider outage, need to review compliance status"
```

This creates an `emergency.access` entry in the SHA-256 hash-chained audit log with:
- **Actor:** the authenticated user (or `localhost` for peer-auth access)
- **Summary:** the reason provided
- **Payload:** the client IP address

### 3. Perform necessary actions

With localhost admin access, the operator can:

- Review and export audit logs: `hobnobcli audit list`
- Check fleet compliance: `hobnobcli thralls list`
- Reset user passwords: `hobnobcli secrets set-web-password hobnob/web/auth/<username>`
- Create new admin accounts: `hobnobcli webusers create --username <name> --role admin`
- Generate API tokens: via the tokens REST API
- Export data: `hobnobcli export`

### 4. Restore normal access

Once the emergency is resolved:

1. Verify the identity provider is operational (if OIDC/isowebauth)
2. Reset any compromised credentials
3. Review the audit trail for the emergency period: `hobnobcli audit list --since 24h`
4. Document the incident per your organization's incident response plan

## Audit Trail

Emergency access events appear in the unified audit log alongside all other security events:

```bash
# List emergency access events
hobnobcli audit list --type emergency.access

# Verify audit chain integrity
hobnobcli audit verify
```

The `emergency.access` event type is recorded in the same tamper-evident SHA-256 hash chain as all other audit events. It cannot be modified or deleted without breaking the chain, which `hobnobcli audit verify` will detect.

## Security Considerations

- **Localhost trust requires shell access.** An attacker who has shell access to the server host already has access to the database, secrets, and configuration files. Localhost trust does not expand the attack surface.
- **Always record emergency access.** Unrecorded emergency access is a compliance violation. The `hobnobcli auth emergency-access` command should be the first thing run after gaining emergency access.
- **Review after every emergency.** The incident response plan should include reviewing all actions taken during the emergency access period.

## HIPAA Mapping

| HIPAA Requirement | Implementation |
|-------------------|----------------|
| 164.312(a)(2)(ii) Emergency access procedure | Localhost trust + `hobnobcli auth emergency-access` |
| 164.312(b) Audit controls | `emergency.access` event in tamper-evident hash chain |
| 164.312(a)(2)(iii) Automatic logoff | 15-minute idle session timeout (unrelated but complementary) |
