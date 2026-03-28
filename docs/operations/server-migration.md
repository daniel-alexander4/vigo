
# Server Migration

How to move a Vigo server to a new machine without re-enrolling agents.

## Overview

Agents authenticate via mTLS -- each one holds a client certificate signed by Vigo's CA and a UUID assigned at enrollment. As long as the new server has the same CA key and database, agents reconnect seamlessly. They do not need to re-enroll.

The migration boils down to:

1. Back up the old server
2. Stand up a new server with the same data
3. Point agents to the new address
4. Verify

## Prerequisites

- SSH access to both old and new machines
- `vigogo` CLI configured to talk to the old server
- The new machine has Docker installed (or Vigo installed natively)

## Step 1: Back up the old server

```bash
vigogo backup create --output vigo-backup.tar.gz
```

This captures everything needed to reconstruct the server:

| Component | What it contains |
|-----------|-----------------|
| Database (`db/vigo.db`) | Enrolled envoys, public keys, run history, users, tokens |
| CA key + cert (`tls/ca.*`) | The certificate authority that signed every agent's client cert |
| Server TLS cert/key (`tls/server.*`) | The server's own TLS identity |
| Policy files (`stack/`) | Modules, roles, envoy mappings |
| Secrets (`secrets/`) | Encrypted secret files + master key |
| Server config (`server.yaml`) | All server configuration |

**The CA private key is the critical piece.** If agents can't verify the server's CA, or the server can't verify agents' client certs, check-ins will fail. The backup includes it.

## Step 2: Set up the new server

Copy the backup to the new machine and restore:

```bash
# On the new machine
scp user@old-server:/path/to/vigo-backup.tar.gz .

# If using Docker (recommended)
docker run -d --name vigo \
  -v /srv/vigo:/srv/vigo \
  -p 1530:1530 -p 8443:8443 \
  ghcr.io/daniel-alexander4/vigo:latest

# Restore the backup
vigogo backup restore --file vigo-backup.tar.gz --server https://new-server:8443
```

If running natively, extract the backup contents to `/srv/vigo/` and start `vigosrv`.

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*

---

### Update server.yaml if needed

If the new machine has a different hostname or the TLS cert has a different SAN, you may need to regenerate the server certificate:

```bash
make certs SERVER_CN=new-hostname.example.com
```

The CA key stays the same -- only the server's own cert changes. Agent client certs remain valid.

## Step 3: Point agents to the new server

How you do this depends on how agents were configured at enrollment.

### Option A: DNS name (zero-touch)

If agents connect via a DNS name (e.g., `vigo.example.com`), update the DNS record to point to the new server's IP. Agents will reconnect on their next check-in cycle (default: 15 seconds).

No agent-side changes needed. This is the recommended setup.

### Option B: Direct IP (requires agent config update)

If agents connect via a hardcoded IP address, you need to update the `server_addr` in each agent's config file.

The agent config is at `/etc/vigo/agent.yaml` (Linux/BSD) or `C:\ProgramData\vigo\agent.yaml` (Windows):

```yaml
server_addr: "new-ip-or-hostname:1530"
```

After editing, restart the agent:

```bash
# Linux (systemd)
sudo systemctl restart vigo

# Windows
Restart-Service vigo
```

**Tip:** If you have many agents, use Vigo itself to dispatch the config change before cutting over. From the old server:

```bash
# Dispatch a task to update agent config on all nodes (while old server is still running)
vigogo task run "sed -i 's/old-ip/new-ip/' /etc/vigo/agent.yaml && systemctl restart vigo" --target "*"
```

### Option C: Floating IP / load balancer

If you use a floating IP (keepalived, cloud elastic IP) or a load balancer in front of Vigo, reassign the IP or update the backend target. Agents see no change.

## Step 4: Verify

Once the new server is running and agents are pointed at it:

```bash
# Check that agents are checking in
vigogo envoys
# Verify all nodes are active
vigogo doctor

# Check a specific node
vigogo risk web01.example.com
```

Agents that were offline during the migration will reconnect automatically on their next check-in cycle. The server will show them as "lost contact" until they check in.

## Cutover checklist

- [ ] Backup created from old server
- [ ] Backup restored on new server
- [ ] `vigogo doctor` passes on new server
- [ ] DNS updated or agent configs updated
- [ ] Agents checking in (verify with `vigogo envoys`)
- [ ] Webhooks and integrations still reach the new server (update firewall rules if IP changed)
- [ ] SMTP notifications working (test with `vigogo test smtp`)
- [ ] Monitoring updated (Prometheus scrape target, Grafana datasource)
- [ ] Old server decommissioned

## Rollback

If something goes wrong, point agents back to the old server (revert DNS or agent config). The old server still has all the data. Agents will resume check-ins immediately.

## Related

- [Backup & Recovery](backup.md) -- backup configuration and Litestream replication
- [Disaster Recovery](disaster-recovery.md) -- rebuilding from scratch after data loss
- [Peer Replication](peer-replication.md) -- primary/replica HA setup
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
