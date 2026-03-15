# Troubleshooting

Common issues and how to diagnose them.

## Diagnostic Tools

### hobnobcli doctor

The `doctor` command checks all server subsystems and reports their health:

```bash
hobnobcli doctor
```

This runs 8 checks: database, TLS certificates, secrets provider, license, federation, SMTP, config, and stale thralls. Each check reports `pass`, `warn`, `fail`, or `skip`.

### hobnobcli runs

List recent convergence runs to spot failures:

```bash
hobnobcli runs list
hobnobcli runs list --thrall web-01.example.com
hobnobcli runs status <run-id>
```

### Prometheus Metrics

Monitor the `/metrics` endpoint for:

- `hobnob_checkins_total{status="error"}` -- check-in failures
- `hobnob_sig_verify_failed_total` -- signature verification failures
- `hobnob_config_reload_errors_total` -- config reload problems
- `hobnob_checkin_age_seconds` -- stale thrall detection

See [Metrics](metrics.md) for the full list.

---

## Common Issues

### Bootstrap Fails with "Connection refused"

**Symptoms:** `curl: (7) Failed to connect to <server> port 8181: Connection refused` immediately after starting the server.

**Cause:** The server takes 30–60 seconds to start — it compiles TLS certs on first run, runs database migrations, loads configs, and rehydrates the FleetIndex. The bootstrap endpoint is not available until the server logs `server ready`.

**Fix:** Wait for the server to finish starting:

```bash
# Watch for "server ready" in the logs
docker compose logs -f hobnob | grep -m1 "server ready"

# Then bootstrap
curl -sSf http://<server>:8181/bootstrap | sudo sh
```

### Agent Won't Connect

**Symptoms:** Agent logs show TLS handshake failures or connection refused.

**Diagnosis:**

1. Verify the server is listening on port 1530:
   ```bash
   ss -tlnp | grep 1530
   ```

2. Check that the agent has the correct CA certificate:
   ```bash
   openssl s_client -connect server:1530 -CAfile /etc/hobnob/ca.pem
   ```

3. Verify the agent's client certificate was signed by the same CA:
   ```bash
   openssl verify -CAfile /etc/hobnob/ca.pem /etc/hobnob/client.crt
   ```

4. Check the agent log for specific errors:
   ```bash
   journalctl -u hobnob-thrall -f
   ```

**Common causes:**
- CA certificate mismatch between server and agent
- Expired TLS certificates (check with `hobnobcli doctor`)
- Firewall blocking port 1530

### Check-in Failures

**Symptoms:** Agent checks in but receives errors. Server logs show signature verification failures.

**Diagnosis:**

1. Check the server log for signature errors:
   ```
   hobnob_sig_verify_failed_total
   ```

2. Verify clock synchronization between agent and server. The signature window is 5 minutes by default (`tuning.signature_window`). If clocks are skewed beyond this, signatures will be rejected.

3. Check if the thrall is revoked:
   ```bash
   hobnobcli nodes list
   ```

4. Verify the agent's key pair matches the public key stored on the server. The agent generates an ED25519 key pair at enrollment, and the public key is stored in the `thralls` table.

**Common causes:**
- Clock skew exceeding the signature window
- Agent re-enrolled with a new key pair but old UUID
- Thrall was revoked

### Config Reload Errors

**Symptoms:** Config changes are not picked up. `hobnob_config_reload_errors_total` increases.

**Diagnosis:**

1. Check server logs for reload errors:
   ```
   grep "config reload" /var/log/hobnob-server.log
   ```

2. Validate config files manually:
   ```bash
   hobnobcli validate
   ```

3. Common YAML errors:
   - Duplicate keys in the same mapping
   - Invalid `when:` expressions
   - Circular `depends_on` references
   - Missing module definitions referenced by roles

**Common causes:**
- YAML syntax errors
- Module referenced in a role but not defined in `modules/`
- Circular dependency in `depends_on`

### Thralls Showing as Stale

**Symptoms:** Thralls appear as "stale" in the UI or CLI despite being online.

**Diagnosis:**

1. Check the stale threshold. A thrall is stale if it has not checked in within `2 * checkin.interval`:
   ```yaml
   checkin:
     interval: "5m"   # stale after 10 minutes
   ```

2. On the thrall, verify the agent is running:
   ```bash
   systemctl status hobnob-thrall
   ```

3. Check agent logs for errors:
   ```bash
   journalctl -u hobnob-thrall --since "10 minutes ago"
   ```

4. Verify network connectivity:
   ```bash
   curl -k https://server:1530   # should get a TLS error, not a connection error
   ```

**Common causes:**
- Agent process crashed or was stopped
- Network partition between agent and server
- DNS resolution failures on the agent

### High Memory Usage

**Symptoms:** Server memory grows over time.

**Diagnosis:**

1. Check FleetIndex size -- it holds all thrall state in memory. Each thrall with traits consumes approximately 2-5 KB:
   ```
   hobnob_nodes_total * ~4KB = expected memory for FleetIndex
   ```

2. Check the recent runs ring buffer -- it holds the last 1000 runs in memory.

3. If using `run_store: memory`, each thrall keeps up to `run_store_capacity` runs in memory.

4. Monitor `hobnob_database_size_bytes` for database growth.

**Mitigation:**
- Configure `database.retention` to prune old records
- Use `run_store: database` instead of `memory` for large fleets

### Secret Resolution Failures

**Symptoms:** Server fails to start or config shows unresolved `secret:` values.

**Diagnosis:**

1. Check which secrets backend is configured:
   ```yaml
   secrets:
     backend: local
   ```

2. For the `local` backend, verify secret files exist:
   ```bash
   ls -la /srv/hobnob/secrets/
   ```

3. Secrets use the path format `secret:path/to/secret` which maps to a file at the secrets directory.

**Common causes:**
- Secret file does not exist at the expected path
- File permissions prevent the server process from reading the secret
- Secrets backend is misconfigured

### Agent Certificate Error: "not valid for name"

**Symptoms:** Agent connects but gets `certificate not valid for name "192.168.x.x"`.

**Cause:** The auto-generated server TLS certificate doesn't include your host's LAN IP. Docker containers get their own hostname and IP, which are the defaults in the cert.

**Fix:** Add your server's IP or DNS name to `tls_sans` in `server.yaml`:

```yaml
server:
  tls_sans:
    - "192.168.1.2"              # your server's LAN IP
    - "hobnob.example.com"       # or DNS name agents use
```

Then regenerate certs and restart:

```bash
cd /srv/hobnob
rm -f tls/cert tls/key
docker compose restart
```

Re-bootstrap thralls so they pick up the new CA:

```bash
sudo systemctl stop hobnob-thrall
curl -sSf http://<server>:8181/bootstrap | sudo sh
```

### Bootstrap Fails with "Failure writing output"

**Symptoms:** `curl: (23) Failure writing output to destination` during bootstrap.

**Cause:** The old agent binary at `/usr/local/sbin/hobnob` is locked by a running process. Curl can't overwrite a binary that's currently executing.

**Fix:**

```bash
# Stop the agent first
sudo systemctl stop hobnob-thrall

# Then re-run bootstrap
curl -sSf http://<server>:8181/bootstrap | sudo sh
```

### Resetting a Thrall

To completely reset a thrall and re-bootstrap from scratch:

```bash
# 1. Stop and remove the service
sudo systemctl stop hobnob-thrall
sudo systemctl disable hobnob-thrall
sudo rm -f /etc/systemd/system/hobnob-thrall.service
sudo systemctl daemon-reload

# 2. Remove agent data and binary
sudo rm -rf /etc/hobnob-thrall
sudo rm -rf /var/lib/hobnob
sudo rm -f /usr/local/sbin/hobnob

# 3. Re-bootstrap
curl -sSf http://<server>:8181/bootstrap | sudo sh
```

Or use the reset script:

```bash
sudo scripts/reset-thrall.sh
curl -sSf http://<server>:8181/bootstrap | sudo sh
```

The thrall will re-enroll with a new UUID and keypair. The old thrall entry on the server becomes orphaned — revoke it with:

```bash
hobnobcli thralls revoke <old-uuid>
```

### Resetting the Server

To wipe all server state and start fresh (preserves license):

```bash
cd /srv/hobnob
docker compose down
sudo find /srv/hobnob -mindepth 1 -path /srv/hobnob/license -prune -o -print0 | xargs -0 rm -rf
docker run --rm -v /srv/hobnob:/srv/hobnob ghcr.io/daniel-alexander4/hobnob:latest --seed-only
docker compose up -d
```

This re-seeds `server.yaml`, `.env`, `docker-compose.yml`, TLS certs, and examples. Your license file is preserved.

---

## Log Messages to Watch

| Log Message | Meaning |
|------------|---------|
| `config reload failed` | YAML parse or validation error |
| `signature verification failed` | Agent sent an invalid ED25519 signature |
| `thrall revoked` | A check-in was rejected because the thrall is revoked |
| `state flusher: batch last_seen failed` | SQLite write error during FleetIndex flush |
| `webhook: delivery failed after retries` | All retry attempts for a webhook exhausted |
| `litestream exited` | Backup replication subprocess crashed |
| `rate limit exceeded` | AI assistant request rate limit hit |
