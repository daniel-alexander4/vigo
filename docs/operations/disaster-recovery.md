# Disaster Recovery

This playbook covers how to recover a Hobnob server from scratch after hardware failure, data corruption, or any scenario where the server is lost. It assumes you have a backup created by `hobnobcli backup create` or a Litestream replica.

For backup configuration and creation, see [Backup & Recovery](backup.md).

## What You Need to Recover

A complete Hobnob server consists of five components, all stored under `/srv/hobnob/`:

| Component | Path | Purpose | Backup method |
|-----------|------|---------|---------------|
| Database | `db/hobnob.db` | Enrolled thralls, run history, traits, users, tokens | `hobnobcli backup` or Litestream |
| Config files | `hnb/proposed/` | Modules, roles, thrall mappings, vars | `hobnobcli backup` (also: git) |
| Secrets | `secrets/` | AES-256-GCM encrypted files + `.master.key` | `hobnobcli backup` |
| TLS certs | `tls/` | CA + server cert/key (plain PEM) | `hobnobcli backup` |
| Server config | `server.yaml` | Ports, intervals, backends, SMTP, export | `hobnobcli backup` |

Nothing else needs to be backed up. The FleetIndex, compliance counters, and policy cache are all rebuilt from the database on startup.

## What Happens to Agents During an Outage

Agents are designed to survive server unavailability indefinitely:

- **Exponential backoff**: Check-in retries at 5s, 10s, 20s ... up to 5-minute intervals. No crash, no data loss.
- **Offline convergence**: If the agent has a cached policy bundle (stored in LMDB at `/var/lib/hobnob/state/`), it continues applying resources locally on its normal interval.
- **Pending results queue**: Run results and trait updates are queued locally and delivered automatically when the server returns.
- **No re-enrollment**: Agents reconnect using their existing UUID and ED25519 keypair. The server verifies their signature against the stored public key in the restored database. No tokens or manual intervention needed.

**The only scenario requiring re-enrollment** is if the database is lost without a backup (thrall records gone) or if agent state files at `/var/lib/hobnob/state/` are deleted.

## Recovery Procedures

### Scenario 1: Full Server Restore from Manual Backup

You have a backup archive created by `hobnobcli backup create`.

```bash
# 1. Install Hobnob on the new host
#    (Docker image, package, or build from source)

# 2. Restore from backup archive
hobnobcli backup restore /path/to/hobnob-backup.tar.gz

# 3. Publish config to activate it
hobnobcli config publish

# 4. Start the server
systemctl start hobd
# or: docker compose up -d
```

The server will:
1. Load secrets using the restored `.master.key`
2. Open the restored database, run any pending migrations
3. Rebuild the FleetIndex from the database (all thralls, traits, compliance status)
4. Start accepting agent check-ins on ports 1530 (gRPC) and 8181 (REST)

Agents will reconnect automatically within their backoff window (at most 5 minutes after the server becomes reachable).

### Scenario 2: Restore from Litestream Replica

You have continuous replication configured and the database is lost, but `/srv/hobnob/` (configs, secrets, TLS) is intact.

```bash
# 1. Stop the server
systemctl stop hobd

# 2. Remove the corrupted database
rm /srv/hobnob/db/hobnob.db
rm -f /srv/hobnob/db/hobnob.db-wal
rm -f /srv/hobnob/db/hobnob.db-shm

# 3. Restore from Litestream replica
litestream restore -o /srv/hobnob/db/hobnob.db \
  s3://my-bucket/hobnob/backup

# 4. Start the server
systemctl start hobd
```

### Scenario 3: Database-Only Restore

Config and secrets are intact, but the database is lost or corrupted.

```bash
systemctl stop hobd

hobnobcli backup restore /path/to/backup.tar.gz --db-only

systemctl start hobd
```

### Scenario 4: Lost Master Key (Secrets Unrecoverable)

If the master key (`.master.key`) is lost and you have no backup that includes it, encrypted secrets cannot be decrypted. You must re-create them:

```bash
# 1. Restore everything except secrets
hobnobcli backup restore /path/to/backup.tar.gz --db-only

# 2. A new master key will be auto-generated on next start.
#    Re-create all secrets:
hobnobcli secrets set hobnob/db/dsn
hobnobcli secrets set hobnob/smtp/password
# ... repeat for each secret referenced in your config

# 3. Publish and start
hobnobcli config publish
systemctl start hobd
```

### Scenario 5: Complete Loss (No Backup)

If both the server and all backups are lost:

1. Install Hobnob fresh, configure `server.yaml` and TLS certs
2. Re-create config files (`hnb/proposed/`) from git or documentation
3. Re-create secrets
4. Start the server — it will initialize an empty database
5. **Agents must re-enroll**: generate new tokens, add approved patterns, then either:
   - Wait for agents to retry (they will eventually hit the server, fail auth, and need bootstrapping)
   - SSH to each node and re-run: `curl -sSfk https://<server>:8443/bootstrap | sudo sh`

## Verification After Restore

After restoring, verify the server is healthy:

```bash
# Check server is running
curl -sk https://localhost:8443/api/v1/health

# Verify enrolled thralls are visible
hobnobcli nodes

# Check compliance status
hobnobcli nodes | grep -c compliant

# Verify a specific thrall can check in
# (wait for its next check-in cycle, or force it)
hobnobcli push web-01.example.com

# Check the dashboard
open https://<server-ip>:8443/
```

If thralls show as "stale" after restore, that's expected — they'll transition back to compliant/drifted after their next check-in.

## Spanner Recovery

In a hub-spoke spanner topology:

- **Spoke failure**: Restore the spoke independently. Thralls enrolled to that spoke reconnect automatically. The hub will resume receiving aggregated data on the spoke's next heartbeat.
- **Hub failure**: Restore the hub. Spokes will reconnect and resume spanner heartbeats. Thrall reassignments and drain operations should be re-verified after hub restore.
- **Hub and spoke have independent databases** — restoring one does not affect the other.

## Best Practices

### Back Up Config Files in Git

The config source of truth (`hnb/proposed/`) is plain YAML on the filesystem. Track it in git for versioned history, diff visibility, and easy restore:

```bash
cd /srv/hobnob/hnb/proposed
git init && git add -A && git commit -m "initial config"

# After changes:
git add -A && git commit -m "add redis module for cache nodes"
```

This gives you `git log`, `git diff`, and `git revert` for config changes independent of database backups.

### Protect the Master Key

The secrets master key (`secrets/.master.key`) is the single most critical file. Without it, all encrypted secrets are unrecoverable.

- Include it in every backup (the default `hobnobcli backup create` does this)
- Consider storing a copy in a separate secure location (password manager, hardware security module, or a different backup system)
- Never commit it to git

### Schedule Regular Backups

```bash
# Daily backup, 7-day retention
0 2 * * * /usr/local/bin/hobnobcli backup create \
  /backup/hobnob-$(date +\%Y\%m\%d).tar.gz \
  && find /backup -name 'hobnob-*.tar.gz' -mtime +7 -delete
```

For continuous protection, configure Litestream in addition to daily snapshots. Litestream provides near-zero RPO (< 1 second), while snapshots provide portable, self-contained recovery points.

### Test Your Restores

Periodically verify that backups are restorable:

```bash
# Dry-run to verify archive integrity
hobnobcli backup restore /backup/hobnob-latest.tar.gz --dry-run

# Full test restore to a staging environment
hobnobcli backup restore /backup/hobnob-latest.tar.gz \
  --target /tmp/hobnob-test
```

## Recovery Time Expectations

| Scenario | Typical recovery time |
|----------|----------------------|
| Database restore from Litestream | Minutes (download + server start) |
| Full restore from backup archive | Minutes (extract + publish + start) |
| Agent reconnection after server returns | 0–5 minutes (backoff window) |
| Pending results drain | Seconds (queued results delivered on first check-in) |
| FleetIndex rehydration | Seconds (even at 10,000+ thralls) |
| Complete loss, no backup | Hours (re-install, re-configure, re-enroll all agents) |

## Related

- [Backup & Recovery](backup.md) — Litestream configuration and manual snapshot commands
- [Database](database.md) — SQLite and Postgres configuration
- [FleetIndex](fleet-index.md) — In-memory index rebuilt on startup
- [Troubleshooting](troubleshooting.md) — Common issues and diagnostics
