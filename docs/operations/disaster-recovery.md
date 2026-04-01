
# Disaster Recovery

This playbook covers how to recover a Vigo server from scratch after hardware failure, data corruption, or any scenario where the server is lost. It assumes you have a backup created by `vigocli backup create` or a Litestream replica.

For backup configuration and creation, see [Backup & Recovery](backup.md).

## What You Need to Recover

A complete Vigo server consists of five components, all stored under `/srv/vigo/`:

| Component | Path | Purpose | Backup method |
|-----------|------|---------|---------------|
| Database | `db/vigo.db` | Enrolled envoys, run history, traits, users, tokens | `vigocli backup` or Litestream |
| Config files | `stack/stage/` | Modules, roles, envoy mappings, vars | `vigocli backup` (also: git) |
| Secrets | `secrets/` | AES-256-GCM encrypted files + `.master.key` | `vigocli backup` |
| TLS certs | `tls/` | CA + server cert/key (plain PEM) | `vigocli backup` |
| Server config | `server.yaml` | Ports, intervals, backends, SMTP, export | `vigocli backup` |

Nothing else needs to be backed up. The FleetIndex, compliance counters, and policy cache are all rebuilt from the database on startup.

## What Happens to Agents During an Outage

Agents are designed to survive server unavailability indefinitely:

- **Exponential backoff**: Check-in retries at 5s, 10s, 20s ... up to 5-minute intervals. No crash, no data loss.
- **Offline convergence**: If the agent has a cached policy bundle (stored in LMDB at `/var/lib/vigo/state/`), it continues applying resources locally on its normal interval.
- **Pending results queue**: Run results and trait updates are queued locally and delivered automatically when the server returns.
- **No re-enrollment**: Agents reconnect using their existing UUID and ED25519 keypair. The server verifies their signature against the stored public key in the restored database. No tokens or manual intervention needed.

**The only scenario requiring re-enrollment** is if the database is lost without a backup (envoy records gone) or if agent state files at `/var/lib/vigo/state/` are deleted.

## Disk Failure Walkthrough

This section walks through the most common disaster scenario: the disk on your server dies.

**Moment of failure.** The disk dies. The vigosrv container stops. gRPC and REST endpoints go dark.

**What happens to the envoys.** Nothing bad. Every agent has a cached signed policy bundle in LMDB (`/var/lib/vigo/state/`). They continue converging on their normal check-in interval. Check-in fails, the agent logs a warning, applies exponential backoff on retries, and keeps enforcing cached policy. Run results and trait updates queue locally in the LMDB pending queue. No envoy stops enforcing state.

**What you've lost.** Dashboard visibility, the ability to publish config changes, new envoy enrollment, task dispatch, and trait reporting. No envoy loses compliance.

**Recovery.**

1. **Provision a new server.** Spin up a new VM or container host. Seconds if cloud, minutes if bare metal.

2. **Restore `/srv/vigo`.** This is where your backup strategy determines recovery time:

   | Backup method | Recovery time | Data loss window |
   |---|---|---|
   | Cloud volume snapshot (hourly) | ~1-2 min (attach snapshot) | Up to 1 hour of config changes |
   | Litestream + filesystem backup | ~1-2 min | Seconds for DB, depends on config backup frequency |
   | rsync/Borg/restic to remote (hourly) | ~2-5 min | Up to 1 hour |
   | Full VM snapshot | ~1-2 min (boot from snapshot) | Since last snapshot |

3. **Start the container.** `docker run` with the restored `/srv/vigo` volume mount. vigosrv starts, loads config, rehydrates FleetIndex from SQLite. Takes seconds, even at 10,000 envoys.

4. **Envoys reconnect.** Agents in exponential backoff retry within seconds to minutes. On successful check-in, pending results drain from the LMDB queue. FleetIndex populates with fresh timestamps. The dashboard comes alive.

**Total recovery time: 5-15 minutes.** Most of that is provisioning the new server and restoring the volume. vigosrv startup itself is seconds.

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*

---

**The gap to watch.** Litestream only covers the SQLite database. Config files (`stack/`), secrets, TLS certs, and `server.yaml` need a separate filesystem backup. If your last filesystem backup was an hour ago and you published config changes 10 minutes before the disk died, those changes are lost. Keep `stack/stage/` in git to eliminate this risk.

## Recovery Procedures

### Scenario 1: Full Server Restore from Manual Backup

You have a backup archive created by `vigocli backup create`.

```bash
# 1. Install Vigo on the new host
#    (Docker image, package, or build from source)

# 2. Restore from backup archive
vigocli backup restore /path/to/vigo-backup.tar.gz

# 3. Publish config to activate it
vigocli config publish

# 4. Start the server
systemctl start vigosrv
# or: docker compose up -d
```

The server will:
1. Load secrets using the restored `.master.key`
2. Open the restored database, run any pending migrations
3. Rebuild the FleetIndex from the database (all envoys, traits, compliance status)
4. Start accepting agent check-ins on ports 1530 (gRPC) and 8443 (REST)

Agents will reconnect automatically within their backoff window (at most 5 minutes after the server becomes reachable).

### Scenario 2: Restore from Litestream Replica

You have continuous replication configured and the database is lost, but `/srv/vigo/` (configs, secrets, TLS) is intact.

```bash
# 1. Stop the server
systemctl stop vigosrv

# 2. Remove the corrupted database
rm /srv/vigo/db/vigo.db
rm -f /srv/vigo/db/vigo.db-wal
rm -f /srv/vigo/db/vigo.db-shm

# 3. Restore from Litestream replica
litestream restore -o /srv/vigo/db/vigo.db \
  s3://my-bucket/vigo/backup

# 4. Start the server
systemctl start vigosrv
```

### Scenario 3: Database-Only Restore

Config and secrets are intact, but the database is lost or corrupted.

```bash
systemctl stop vigosrv
vigocli backup restore /path/to/backup.tar.gz --db-only

systemctl start vigosrv
```

### Scenario 4: Lost Master Key (Secrets Unrecoverable)

If the master key (`.master.key`) is lost and you have no backup that includes it, encrypted secrets cannot be decrypted. You must re-create them:

```bash
# 1. Restore everything except secrets
vigocli backup restore /path/to/backup.tar.gz --db-only

# 2. A new master key will be auto-generated on next start.
#    Re-create all secrets:
vigocli secrets set vigo/db/dsn
vigocli secrets set vigo/smtp/password
# ... repeat for each secret referenced in your config

# 3. Publish and start
vigocli config publish
systemctl start vigosrv
```

### Scenario 5: Complete Loss (No Backup)

If both the server and all backups are lost:

1. Install Vigo fresh, configure `server.yaml` and TLS certs
2. Re-create config files (`stack/stage/`) from git or documentation
3. Re-create secrets
4. Start the server — it will initialize an empty database
5. **Agents must re-enroll**: configure trusted enrollment patterns, then either:
   - Wait for agents to retry (they will eventually hit the server, fail auth, and need bootstrapping)
   - Run `vigocli envoys rebootstrap --all` to signal all agents to update
   - Or manually bootstrap each node: `curl -sSfk https://<server>:8443/bootstrap | sudo sh`

## Verification After Restore

After restoring, verify the server is healthy:

```bash
# Check server is running
curl -sk https://localhost:8443/api/v1/health

# Verify enrolled envoys are visible
vigocli nodes

# Check compliance status
vigocli nodes | grep -c compliant

# Verify a specific envoy can check in
# (wait for its next check-in cycle, or force it)
vigocli envoys push web-01.example.com

# Check the dashboard
open https://<server-ip>:8443/
```

If envoys show as "lost contact" after restore, that's expected -- they'll transition back to converged/relapsed after their next check-in.

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*

---

## Spanner Recovery

In a hub-spoke spanner topology:

- **Spoke failure**: Restore the spoke independently. Envoys enrolled to that spoke reconnect automatically. The hub will resume receiving aggregated data on the spoke's next heartbeat.
- **Hub failure**: Restore the hub. Spokes will reconnect and resume spanner heartbeats. Envoy reassignments and drain operations should be re-verified after hub restore.
- **Hub and spoke have independent databases** — restoring one does not affect the other.

## Best Practices

### Back Up Config Files in Git

The config source of truth (`stack/stage/`) is plain YAML on the filesystem. Track it in git for versioned history, diff visibility, and easy restore:

```bash
cd /srv/vigo/stack/stage
git init && git add -A && git commit -m "initial config"

# After changes:
git add -A && git commit -m "add redis module for cache nodes"
```

This gives you `git log`, `git diff`, and `git revert` for config changes independent of database backups.

### Protect the Master Key

The secrets master key (`secrets/.master.key`) is the single most critical file. Without it, all encrypted secrets are unrecoverable.

- Include it in every backup (the default `vigocli backup create` does this)
- Consider storing a copy in a separate secure location (password manager, hardware security module, or a different backup system)
- Never commit it to git

### Schedule Regular Backups

```bash
# Daily backup, 7-day retention
0 2 * * * /usr/local/bin/vigocli backup create \
  /backup/vigo-$(date +\%Y\%m\%d).tar.gz \
  && find /backup -name 'vigo-*.tar.gz' -mtime +7 -delete
```

For continuous protection, configure Litestream in addition to daily snapshots. Litestream provides near-zero RPO (< 1 second), while snapshots provide portable, self-contained recovery points.

### Test Your Restores

Periodically verify that backups are restorable:

```bash
# Dry-run to verify archive integrity
vigocli backup restore /backup/vigo-latest.tar.gz --dry-run

# Full test restore to a staging environment
vigocli backup restore /backup/vigo-latest.tar.gz \
  --target /tmp/vigo-test
```

## Recovery Time Expectations

| Scenario | Typical recovery time |
|----------|----------------------|
| Database restore from Litestream | Minutes (download + server start) |
| Full restore from backup archive | Minutes (extract + publish + start) |
| Agent reconnection after server returns | 0–5 minutes (backoff window) |
| Pending results drain | Seconds (queued results delivered on first check-in) |
| FleetIndex rehydration | Seconds (even at 10,000+ envoys) |
| Complete loss, no backup | Hours (re-install, re-configure, re-enroll all agents) |

## Related

- [Backup & Recovery](backup.md) — Litestream configuration and manual snapshot commands
- [Database](database.md) — SQLite and Postgres configuration
- [FleetIndex](fleet-index.md) — In-memory index rebuilt on startup
- [Troubleshooting](troubleshooting.md) — Common issues and diagnostics
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
