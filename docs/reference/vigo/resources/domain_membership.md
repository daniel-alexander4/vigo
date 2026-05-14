
# domain_membership

Manages Active Directory domain membership idempotently via PowerShell. Joins machines to a domain, verifies current membership, and removes machines from a domain.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `domain` | Yes | -- | Domain name (e.g., `example.com`, `ad.corp.local`). |
| `state` | No | `present` | `present` to join the domain, `absent` to leave it. |
| `username` | Conditional | -- | Domain admin username (e.g., `admin@example.com` or `DOMAIN\admin`). Required for `state=present`. |
| `password` | Conditional | -- | Domain admin password. Required for `state=present`. Use `secret:` prefix. |
| `ou_path` | No | -- | Organizational Unit path for the computer object (e.g., `OU=Servers,DC=example,DC=com`). |
| `restart_after_join` | No | `true` | Set to `false` to skip the automatic restart after join/leave. |

## States

- **`present`** -- Ensure the machine is joined to the specified domain. If already joined to the correct domain, no action is taken.
- **`absent`** -- Ensure the machine is not joined to any domain. If already in a workgroup, no action is taken.

## Idempotency

The executor queries domain membership via `Win32_ComputerSystem` WMI before acting:

1. If the machine is already joined to the specified domain, no action is taken.
2. If the machine is in a workgroup and `state=present`, `Add-Computer` is called with the provided credentials.
3. If `ou_path` is specified, the computer object is created in that OU.
4. If the machine is domain-joined and `state=absent`, `Remove-Computer` is called.
5. By default, the machine restarts after a join or leave operation. Set `restart_after_join: "false"` to defer the restart.

## Examples

### Join a domain

```yaml
resources:
  - name: join-domain
    type: domain_membership
    domain: corp.example.com
    username: "admin@corp.example.com"
    password: "secret:vigo/ad/join-password"
```

### Join a domain into a specific OU

```yaml
resources:
  - name: join-domain-ou
    type: domain_membership
    domain: corp.example.com
    username: "CORP\\svc-join"
    password: "secret:vigo/ad/join-password"
    ou_path: "OU=WebServers,OU=Servers,DC=corp,DC=example,DC=com"
```

### Join without immediate restart

```yaml
resources:
  - name: join-domain-no-reboot
    type: domain_membership
    domain: corp.example.com
    username: "admin@corp.example.com"
    password: "secret:vigo/ad/join-password"
    restart_after_join: "false"

  - name: scheduled-reboot
    type: reboot_windows
    message: "Rebooting to complete domain join"
    timeout: "60"
    depends_on: [join-domain-no-reboot]
```

### Leave a domain

```yaml
resources:
  - name: leave-domain
    type: domain_membership
    domain: corp.example.com
    state: absent
    username: "admin@corp.example.com"
    password: "secret:vigo/ad/leave-password"
```

## Platform

Windows only. Requires network connectivity to a domain controller.

## Notes

- Domain join and leave operations require a reboot to take effect. The `restart_after_join` parameter defaults to `true` and triggers an immediate `Restart-Computer -Force`. Set `restart_after_join: "false"` and use the [`reboot_windows`](reboot_windows.md) executor for controlled reboots.
- Credentials are passed via `PSCredential` objects. Use the `secret:` prefix to keep passwords out of plaintext config.
- For `state=absent`, credentials are optional. If omitted, the machine attempts an unauthenticated leave (works when the machine account still has permissions).
- After joining, subsequent convergence runs detect the domain membership and take no action, making this safe to leave in config permanently.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../../legal/license.md).*
