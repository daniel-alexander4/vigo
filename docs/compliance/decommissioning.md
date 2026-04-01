
---
title: Decommissioning Procedures
---

# Decommissioning Procedures

HIPAA 164.310(d)(2)(i) requires procedures for secure disposal of hardware and electronic media containing ePHI. This page covers decommissioning managed nodes (envoys) and the Vigo server itself.

## Decommissioning a Managed Node (Envoy)

When a managed node is retired, deprovisioned, or repurposed:

### 1. Remove from fleet management

```bash
# Revoke the envoy's enrollment — it can no longer check in or re-enroll
vigocli envoys revoke <hostname-or-uuid>
```

Revocation is immediate. The envoy receives an error on its next check-in and cannot re-enroll with the same UUID. The revocation is recorded in the tamper-evident audit trail.

### 2. Purge agent data on the node

SSH to the node and remove all Vigo agent data:

```bash
# Stop the agent service
sudo systemctl stop vigo-envoy    # Linux (systemd)
sudo launchctl unload /Library/LaunchDaemons/com.vigo.agent.plist  # macOS
sudo svcadm disable vigo-envoy    # illumos

# Remove agent binary and state
sudo rm -rf /usr/local/bin/vigo
sudo rm -rf /var/lib/vigo/          # LMDB state store, cached policy, pending results
sudo rm -rf /etc/vigo/              # Client certificates, agent config

# Remove the service definition
sudo rm -f /etc/systemd/system/vigo-envoy.service  # systemd
sudo rm -f /Library/LaunchDaemons/com.vigo.agent.plist  # macOS
sudo rm -f /var/svc/manifest/site/vigo-envoy.xml  # illumos
```

On Windows:

```powershell
# Stop and remove the Windows service
Stop-Service vigo-envoy
sc.exe delete vigo-envoy

# Remove agent files
Remove-Item -Recurse -Force "C:\ProgramData\vigo"
Remove-Item -Force "C:\Program Files\vigo\vigo.exe"
```

### 3. Remove sensitive data from the node

If the node processed or stored ePHI:

- Wipe application data directories per your data retention policy

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*

---
- For full decommission: secure-erase the disk (NIST SP 800-88 guidelines)
- For repurposing: ensure no residual ePHI remains in application logs, temp files, or swap

### 4. Remove from server records

The envoy's enrollment record, traits, and run history remain in the server database for audit purposes. To fully purge:

```bash
# Delete the envoy and all associated data (traits, run results, compliance history)
vigocli envoys delete <hostname-or-uuid>
```

**Note:** Retain records if your compliance policy requires historical audit trails for decommissioned systems.

### 5. Update configuration

Remove the node's entry from the node mapping configuration:

```bash
# Edit the config to remove the node's match entry
vigocli vi nodes.vgo

# Publish the updated config
vigocli config publish
```

## Decommissioning the Vigo Server

When the entire Vigo deployment is being retired:

### 1. Export audit trail

Before shutdown, export the complete audit trail for compliance records:

```bash
# Export full audit history
vigocli audit list --limit 10000 > /path/to/audit-export.json

# Verify chain integrity one final time
vigocli audit verify

# Create a final backup
vigocli backup create
```

### 2. Revoke all envoys

```bash
# List all active envoys
vigocli envoys list

# Revoke each one (they will stop checking in)
vigocli envoys revoke --all
```

### 3. Purge agents on all nodes

Run the agent cleanup steps (above) on every managed node. For large fleets, use a task command before revoking:

```bash
# Dispatch a cleanup task to all nodes before revoking
vigocli task run "systemctl stop vigo-envoy && rm -rf /var/lib/vigo /etc/vigo /usr/local/bin/vigo" --target "*"
```

### 4. Destroy server data

```bash
# Stop the server
# (Docker)
docker compose down

# Remove all server data
sudo rm -rf /srv/vigo/          # Config, database, secrets, TLS certs
sudo rm -rf /var/log/vigo/      # Logs (if configured)

# Remove Docker volumes (if applicable)
docker volume rm vigo_data
```

### 5. Secure media disposal

For the server host:
- If physical hardware: follow NIST SP 800-88 for media sanitization
- If cloud VM: destroy the instance and any attached volumes
- If Docker: remove images and prune volumes

## Data Retained After Decommissioning

| Data | Location | Action |
|------|----------|--------|
| Agent LMDB state | `/var/lib/vigo/state/` on each node | Delete during node decommission |
| Agent TLS certs | `/etc/vigo/` on each node | Delete during node decommission |
| Server database | `/srv/vigo/vigo.db` | Export audit trail, then delete |
| Server secrets | `/srv/vigo/secrets/` | Delete during server decommission |
| Server TLS certs | `/srv/vigo/tls/` | Delete during server decommission |
| Backup archives | Configured backup location | Retain per retention policy, then securely delete |
| Litestream replicas | Configured S3/NFS target | Delete per retention policy |

## HIPAA Mapping

| HIPAA Requirement | Implementation |
|-------------------|----------------|
| 164.310(d)(2)(i) Disposal | Documented procedure for node and server decommission |
| 164.310(d)(2)(ii) Media re-use | Secure-erase guidance for repurposed nodes |
| 164.312(b) Audit controls | Audit trail export before server decommission |
| 164.312(c)(1) Integrity | Revocation prevents decommissioned nodes from modifying fleet state |
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
