---
title: Decommissioning Procedures
---

# Decommissioning Procedures

HIPAA 164.310(d)(2)(i) requires procedures for secure disposal of hardware and electronic media containing ePHI. This page covers decommissioning managed nodes (thralls) and the Hobnob server itself.

## Decommissioning a Managed Node (Thrall)

When a managed node is retired, deprovisioned, or repurposed:

### 1. Remove from fleet management

```bash
# Revoke the thrall's enrollment — it can no longer check in or re-enroll
hobnobcli thralls revoke <hostname-or-uuid>
```

Revocation is immediate. The thrall receives an error on its next check-in and cannot re-enroll with the same UUID. The revocation is recorded in the tamper-evident audit trail.

### 2. Purge agent data on the node

SSH to the node and remove all Hobnob agent data:

```bash
# Stop the agent service
sudo systemctl stop hobnob-thrall    # Linux (systemd)
sudo launchctl unload /Library/LaunchDaemons/com.hobnob.agent.plist  # macOS
sudo svcadm disable hobnob-thrall    # illumos

# Remove agent binary and state
sudo rm -rf /usr/local/bin/hobnob
sudo rm -rf /var/lib/hobnob/          # LMDB state store, cached policy, pending results
sudo rm -rf /etc/hobnob/              # Client certificates, agent config

# Remove the service definition
sudo rm -f /etc/systemd/system/hobnob-thrall.service  # systemd
sudo rm -f /Library/LaunchDaemons/com.hobnob.agent.plist  # macOS
sudo rm -f /var/svc/manifest/site/hobnob-thrall.xml  # illumos
```

On Windows:

```powershell
# Stop and remove the Windows service
Stop-Service hobnob-thrall
sc.exe delete hobnob-thrall

# Remove agent files
Remove-Item -Recurse -Force "C:\ProgramData\hobnob"
Remove-Item -Force "C:\Program Files\hobnob\hobnob.exe"
```

### 3. Remove sensitive data from the node

If the node processed or stored ePHI:

- Wipe application data directories per your data retention policy
- For full decommission: secure-erase the disk (NIST SP 800-88 guidelines)
- For repurposing: ensure no residual ePHI remains in application logs, temp files, or swap

### 4. Remove from server records

The thrall's enrollment record, traits, and run history remain in the server database for audit purposes. To fully purge:

```bash
# Delete the thrall and all associated data (traits, run results, compliance history)
hobnobcli thralls delete <hostname-or-uuid>
```

**Note:** Retain records if your compliance policy requires historical audit trails for decommissioned systems.

### 5. Update configuration

Remove the node's entry from the node mapping configuration:

```bash
# Edit the config to remove the node's match entry
hobnobcli vi nodes.hnb

# Publish the updated config
hobnobcli config publish
```

## Decommissioning the Hobnob Server

When the entire Hobnob deployment is being retired:

### 1. Export audit trail

Before shutdown, export the complete audit trail for compliance records:

```bash
# Export full audit history
hobnobcli audit list --limit 10000 > /path/to/audit-export.json

# Verify chain integrity one final time
hobnobcli audit verify

# Create a final backup
hobnobcli backup create
```

### 2. Revoke all thralls

```bash
# List all active thralls
hobnobcli thralls list

# Revoke each one (they will stop checking in)
hobnobcli thralls revoke --all
```

### 3. Purge agents on all nodes

Run the agent cleanup steps (above) on every managed node. For large fleets, use a push command before revoking:

```bash
# Push a cleanup script to all nodes before revoking
hobnobcli push exec --target "*" --command "systemctl stop hobnob-thrall && rm -rf /var/lib/hobnob /etc/hobnob /usr/local/bin/hobnob"
```

### 4. Destroy server data

```bash
# Stop the server
# (Docker)
docker compose down

# Remove all server data
sudo rm -rf /srv/hobnob/          # Config, database, secrets, TLS certs
sudo rm -rf /var/log/hobnob/      # Logs (if configured)

# Remove Docker volumes (if applicable)
docker volume rm hobnob_data
```

### 5. Secure media disposal

For the server host:
- If physical hardware: follow NIST SP 800-88 for media sanitization
- If cloud VM: destroy the instance and any attached volumes
- If Docker: remove images and prune volumes

## Data Retained After Decommissioning

| Data | Location | Action |
|------|----------|--------|
| Agent LMDB state | `/var/lib/hobnob/state/` on each node | Delete during node decommission |
| Agent TLS certs | `/etc/hobnob/` on each node | Delete during node decommission |
| Server database | `/srv/hobnob/hobnob.db` | Export audit trail, then delete |
| Server secrets | `/srv/hobnob/secrets/` | Delete during server decommission |
| Server TLS certs | `/srv/hobnob/tls/` | Delete during server decommission |
| Backup archives | Configured backup location | Retain per retention policy, then securely delete |
| Litestream replicas | Configured S3/NFS target | Delete per retention policy |

## HIPAA Mapping

| HIPAA Requirement | Implementation |
|-------------------|----------------|
| 164.310(d)(2)(i) Disposal | Documented procedure for node and server decommission |
| 164.310(d)(2)(ii) Media re-use | Secure-erase guidance for repurposed nodes |
| 164.312(b) Audit controls | Audit trail export before server decommission |
| 164.312(c)(1) Integrity | Revocation prevents decommissioned nodes from modifying fleet state |
