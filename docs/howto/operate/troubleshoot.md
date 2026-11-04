
# Troubleshoot common issues

This page is grep-friendly: when an envoy throws an error or a session fails, search this page for the exact string the operator sees. Each symptom links to its fix. The diagnostic-tool reference is at the bottom for when none of the known symptoms match.

## Symptoms (in operator-grep form)

### `SSH pubkey not authorized on this envoy`

Scrier-SSH session fails immediately with this server-side error. The operator's pubkey (from `users.ssh_public_key` for browser sessions, or auto-detected by the CLI for `vigocli scrier ssh`) isn't in any regular user's `~/.ssh/authorized_keys` on the target envoy.

**Fix:** add the operator's pubkey to a regular user on the envoy. Typical path: declare the user in a usercrate with `authorized_keys:` and publish. Then retry. See [Set up Scrier](set-up-scrier.md#make-sure-the-operators-pubkey-is-in-some-regular-users-authorized_keys-on-each-envoy).

### `no SSH pubkey available — either run vigocli scrier ssh...`

The web user has no `ssh_public_key` set AND the connect request didn't carry one (i.e., it came from the browser, not the CLI).

**Fix:** run `vigocli scrier ssh <envoy>` once from a host where your SSH key lives — the CLI auto-detects and write-throughs your pubkey to `users.ssh_public_key`. Browser sessions thereafter work. Or `PUT /api/v1/users/{id}` with `ssh_public_key` directly.

### `agent <uuid> did not open TunnelStream within 90s`

Scrier session times out waiting for the envoy's agent to dial back. The agent received the wake signal (or should have) but didn't open the TunnelStream.

**Fix:** check the envoy's agent state — `vigocli envoys list <uuid>` for staleness; `vigocli traits livequery <uuid> host_self.fs_root_pct` for disk pressure (a starved agent can fail to open new sockets); inspect the agent's host with `vigo status` on-box. Disk-pressure recoveries: clear journald (`journalctl --vacuum-size=200M`) and apt cache (`apt clean`); restart the agent.

### `scrier: verify pubkey not found in any regular user's authorized_keys`

Agent-side error from the scrier scan. The operator's pubkey isn't in `/home/*/.ssh/authorized_keys` for any regular (uid != 0) user.

**Fix:** same as the first symptom above. Root is **never** a scrier landing target; if the operator's key lives only in `/root/.ssh/authorized_keys`, the scan won't find it. Add it to a regular user.

### Envoy shows as stale but the agent is running

The freshness predicate marked the envoy offline despite the agent running. Likely: the check-in interval is configured too low and the convergence cycle takes longer than the interval, so check-ins back up.

**Fix:** check `vigo status` on the envoy for the observed inter-CheckIn cadence. If it's much higher than the configured `checkin.interval`, the convergence pass is bottlenecked. Bump the interval, or set the `checkin.offline_threshold` override in `server.yaml` (see [reference/server-yaml.md](../../reference/server-yaml.md)).

### `server does not recognize this envoy (unknown id) — re-bootstrap required`

The agent's enrolled identity (its client UUID) has no row on the server, so every check-in is rejected with gRPC `NotFound` and the agent backs off (5 min) and keeps applying its last cached policy offline. Cause: the server's DB was reset/rebuilt, or the envoy was deleted (`vigocli envoys delete`) — while the agent kept running with its old UUID. (Distinct from a *revoked* envoy, which is rejected with `PermissionDenied "envoy is revoked"` and must stay rejected.) The server logs the orphan once per 10 min as `WARN check-in from unknown envoy`, and `vigo_sig_verify_failed_total{reason="envoy_not_found"}` climbs.

**Fix:** re-bootstrap the agent on that host to re-enroll it with a fresh identity: `curl -sSfk https://<server>:8443/bootstrap | sudo sh`. (The agent does **not** re-enroll itself — re-enrollment stays a deliberate, gated act.) If the host is decommissioned, stop its agent instead.

### `vigocli config publish` fails with validation errors

The published config didn't pass one of the validation gates (YAML syntax, cross-reference integrity, circular dependency detection, idempotency, guardrails, URL aliveness).

**Fix:** the built-in AI assistant streams fix suggestions to your terminal when validation fails. Read those. For specific gates, see [Publish your config](../configcrates/publish-config.md). To skip URL-aliveness checks in air-gapped environments, use `--skip-url-checks`.

### vigosrv exits at startup with `config load failed`

The published config tree at `/srv/vigo/.live/` has a structural error (typically duplicate names, YAML syntax, dangling references, or a layout violation). vigosrv refuses to start rather than run with no policy — a config-enforcement server with no config silently strands the fleet on its last-cached state, which is worse than a loud crash.

**Fix:** repair the source under `/srv/vigo/stacks/`, then `vigocli config publish` (the file ops run even when the server is down), then restart vigosrv. The startup error message names the failing rule.

### Configcrate convergence loop — same resource changes every cycle

A resource declares state that some external process keeps reverting (a package manager auto-rolls back the version, another tool rewrites the file, the OS updates a value).

**Fix:** `vigocli config trace <envoy> <configcrate>` shows the resolution chain. Inspect what's writing the file outside of Vigo. The drift-counter on the envoy's detail page distinguishes `Diverged` (≥5 consecutive runs of changes — that's this case) from `Changed` (one-off).

### Convergence rollback fired mid-publish

`server/publish/` auto-rolls back a publish whose convergence drops below threshold.

**Fix:** the previous good `.live/` is restored automatically; the operator sees the rollback in `vigocli config history` and the publish-audit-log. Inspect the runs that triggered the rollback to find what changed; fix and re-publish.

### `kex_exchange_identification: read: Connection reset by peer` on an envoy's sshd (not scrier)

Direct SSH to the envoy host (not via scrier) is rejected. Usually disk-pressure or resource-starvation on the host.

**Fix:** check disk + load. Vigo's swarm subsystems refuse pushes below 10% free disk — that often correlates with sshd misbehavior on the same host. Clear journald + apt cache; restart sshd if needed.

### `vigo run` sits with no output

A command's output is captured, not streamed, so a long resource looks identical to a wedged one until it finishes. Since 0.76.89 the agent breaks the tie itself: every 15 seconds it logs a `still running` line with the process state and the bytes moved, so `state=D` with a climbing `io_delta_bytes` tells you a slow package unpack is working. See [Progress reporting](../../reference/vigo/resources/exec.md#progress-reporting).

**If it really is wedged**, the agent now says so and kills it: `child process is STOPPED and cannot make progress`. The cause is a command that tried to read the terminal. Foreground commands get `/dev/null` on stdin, so this only happens when something opens `/dev/tty` directly — some Debian maintainer scripts do, and `DEBIAN_FRONTEND=noninteractive` does not prevent it.

**Fix:** make the command non-interactive. To confirm the diagnosis by hand:

```bash
ps -o pid,stat,wchan:20,args -p <pid>   # STAT of T = stopped; D = working on disk
ls -l /proc/<pid>/fd/0                  # should be /dev/null, never /dev/pts/N
```

A `T` in `STAT` is never healthy. `D` almost always is.

## Diagnostic Tools

### vigo status (agent-side)

Run on any envoy to inspect the agent's local state without contacting the server:

```bash
sudo vigo status
```

This shows the envoy UUID, server address, cached policy details (version, configcrates, resources), cached traits age, pending result queue depth, and server pubkey status. It reads from the config file and LMDB state store only -- no network required.

Useful for verifying:
- Whether the agent is registered (`Envoy ID` vs `(not registered)`)
- Which policy version the agent is running (`Policy > Version`)
- Whether offline results are queued (`Pending results` count)
- Whether the agent has the server's bundle-signing key (`Server pubkey`)

See [Agent CLI](../../reference/vigocli/agent.md#status) for full output examples.

### vigocli doctor

The `doctor` command checks all server subsystems and reports their health:

```bash
vigocli doctor
```

This runs 7 checks: database, TLS certificates, secrets provider, spanner, SMTP, config, and stale envoys. Each check reports `pass`, `warn`, `fail`, or `skip`.

### vigocli runs

List recent convergence runs to spot failures:

```bash
vigocli runs list
vigocli runs list --envoy web-01.example.com
vigocli runs show <run-id>
```

Show what's drifting across the fleet in one command:

```bash
vigocli runs drift
vigocli runs drift --envoy web-01.example.com
```

### Prometheus Metrics

Monitor the `/metrics` endpoint for:

- `vigo_checkins_total{status="error"}` -- check-in failures
- `vigo_sig_verify_failed_total` -- signature verification failures
- `vigo_config_reload_errors_total` -- config reload problems
- `vigo_checkin_age_seconds` -- stale envoy detection


See [Metrics](set-up-monitoring.md) for the full list.

---

## Common Issues

### Bootstrap Fails with "Connection refused"

**Symptoms:** `curl: (7) Failed to connect to <server> port 8443: Connection refused` immediately after starting the server.

**Cause:** The server takes 30–60 seconds to start — it compiles TLS certs on first run, runs database migrations, loads configs, and rehydrates the FleetIndex. The bootstrap endpoint is not available until the server logs `server ready`.

**Fix:** Wait for the server to finish starting:

```bash
# Watch for "server ready" in the logs
docker compose logs -f vigo | grep -m1 "server ready"

# Then bootstrap
curl -sSfk https://<server>:8443/bootstrap | sudo sh
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
   openssl s_client -connect server:1530 -CAfile /etc/vigo/ca.pem
   ```

3. Verify the agent's client certificate was signed by the same CA:
   ```bash
   openssl verify -CAfile /etc/vigo/ca.pem /etc/vigo/client.crt
   ```

4. Check the agent log for specific errors:
   ```bash
   journalctl -u vigo-envoy -f
   ```

**Common causes:**
- CA certificate mismatch between server and agent
- Expired TLS certificates (check with `vigocli doctor`)
- Firewall blocking port 1530

### Check-in Failures

**Symptoms:** Agent checks in but receives errors. Server logs show signature verification failures.

**Diagnosis:**


1. Check the server log for signature errors:
   ```
   vigo_sig_verify_failed_total
   ```

2. Verify clock synchronization between agent and server. The signature window is 5 minutes by default (`tuning.signature_window`). If clocks are skewed beyond this, signatures will be rejected.

3. Check if the envoy is revoked:
   ```bash
   vigocli nodes list
   ```

4. Verify the agent's key pair matches the public key stored on the server. The agent generates an ED25519 key pair at enrollment, and the public key is stored in the `envoys` table.

**Common causes:**
- Clock skew exceeding the signature window
- Agent re-enrolled with a new key pair but old UUID
- Envoy was revoked

### Server Started with Zero Configcrates

**Symptoms:** Server is running but envoys get empty policy (no resources applied). Logs show `config loaded with validation errors — starting with empty config`.

**Cause:** Idempotency validation errors in `.live/` configcrates. The server treats these as non-fatal — it starts with empty config so the REST API, UI, and gRPC remain available for diagnosis.

**Fix:**
1. Check the error details: `vigocli doctor` or look at the server logs
2. Fix the configcrate in `stacks/` (the configcrate linter will catch and repair most issues)
3. Republish: `vigocli config publish`

### Config Reload Errors

**Symptoms:** Config changes are not picked up. `vigo_config_reload_errors_total` increases.

**Diagnosis:**

1. Check server logs for reload errors:
   ```
   grep "config reload" /var/log/vigo-server.log
   ```

2. Validate config files manually:
   ```bash
   vigocli config publish --dry-run
   ```

3. Common YAML errors:
   - Duplicate keys in the same mapping
   - Invalid `when:` expressions
   - Circular `depends_on` references
   - Missing configcrate definitions referenced by roles

**Common causes:**
- YAML syntax errors
- Configcrate referenced in a role but not defined in `configcrates/`
- Circular dependency in `depends_on`

### Envoy Reports Compliant but Resources Not Applied

**Symptoms:** A envoy shows `compliant` with `CHANGED 0, FAILED 0`, but the expected changes (files, users, sudoers, etc.) are not present on the machine.


**Cause:** The agent binary is too old to understand newer resource parameters. The agent sees the resource, checks the fields it knows about (e.g., user exists, shell correct), reports "already compliant," and silently ignores parameters it doesn't recognize (e.g., `sudo_nopasswd`, `password`, `authorized_keys`).

**Diagnosis:**

1. Check the agent version:
   ```bash
   vigocli envoys show <hostname>
   ```
   Look at the `Agent:` line. Compare with the version the server is distributing.

2. Verify the resource is in the resolved config:
   ```bash
   vigocli config trace <hostname>
   ```
   If the configcrate is listed but resources aren't being applied, it's an agent version mismatch.

3. Check the run result for the specific envoy:
   ```bash
   vigocli runs list --envoy <envoy-uuid> --limit 1
   ```

**Fix:** Update the agent binary. If you can push through the existing agent:

```bash
# Re-bootstrap via push — downloads and installs the latest agent binary
vigocli task run "curl -sSfk https://<server>:8443/bootstrap | sh" --target "<hostname>"
```

If you can SSH with a key (no sudo needed to connect):

```bash
make build-agent
scp bin/vigo user@hostname:/tmp/vigo
ssh user@hostname "sudo mv /tmp/vigo /usr/local/sbin/vigo && sudo systemctl restart vigo-envoy"
```

If you're completely locked out (can't sudo, can't push), you'll need console or physical access to the machine.

### Envoys Showing as Stale

**Symptoms:** Envoys appear as "stale" in the UI or CLI despite being online.

**Diagnosis:**

1. Check the stale threshold. A envoy is stale if it has not checked in within `2.5 × checkin.interval`:
   ```yaml
   checkin:
     interval: "5m"   # stale after 12 minutes 30 seconds
   ```

2. On the envoy, verify the agent is running:
   ```bash
   systemctl status vigo-envoy
   ```

3. Check agent logs for errors:
   ```bash
   journalctl -u vigo-envoy --since "10 minutes ago"
   ```


4. Check the agent's local state:
   ```bash
   sudo vigo status
   ```
   Look at `Pending results` (queued but undelivered) and `Policy > Version` (stale cache).

5. Verify network connectivity:
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

1. Check FleetIndex size -- it holds all envoy state in memory. Each envoy with traits consumes approximately 2-5 KB:
   ```
   vigo_nodes_total * ~4KB = expected memory for FleetIndex
   ```

2. Check the recent runs ring buffer -- it holds the last 1000 runs in memory.

3. If using `run_store: memory`, each envoy keeps up to `run_store_capacity` runs in memory.

4. Monitor `vigo_database_size_bytes` for database growth.

**Mitigation:**
- Lower `database.retention` (default `30d`) to shrink the retained window
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
   ls -la /srv/vigo/secrets/
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
    - "vigo.example.com"       # or DNS name agents use
```

Then regenerate certs and restart:

```bash
cd /srv/vigo
rm -f tls/cert tls/key
docker compose restart
```

Re-bootstrap envoys so they pick up the new CA:

```bash
sudo systemctl stop vigo-envoy
curl -sSfk https://<server>:8443/bootstrap | sudo sh
```

### Bootstrap Fails with "Failure writing output"

**Symptoms:** `curl: (23) Failure writing output to destination` during bootstrap.

**Cause:** The old agent binary at `/usr/local/sbin/vigo` is locked by a running process. Curl can't overwrite a binary that's currently executing.

**Fix:**

```bash
# Stop the agent first
sudo systemctl stop vigo-envoy

# Then re-run bootstrap
curl -sSfk https://<server>:8443/bootstrap | sudo sh
```

### Resetting a Envoy

To completely reset a envoy and re-bootstrap from scratch:

```bash
# 1. Stop and remove the service
sudo systemctl stop vigo-envoy
sudo systemctl disable vigo-envoy
sudo rm -f /etc/systemd/system/vigo-envoy.service
sudo systemctl daemon-reload

# 2. Remove agent data and binary
sudo rm -rf /etc/vigo-envoy
sudo rm -rf /var/lib/vigo
sudo rm -f /usr/local/sbin/vigo
sudo rm -f /usr/local/bin/git-remote-gitback

# 3. Re-bootstrap
curl -sSfk https://<server>:8443/bootstrap | sudo sh
```

Or use the reset script, which runs the same steps. It never touches server state in `/srv/vigo`, so it is safe on a server host that runs its own envoy:

```bash
sudo scripts/reset-envoy.sh
curl -sSfk https://<server>:8443/bootstrap | sudo sh
```

The envoy will re-enroll with a new UUID and keypair. The old envoy entry on the server becomes orphaned — revoke it with:

```bash
vigocli envoys revoke <old-uuid>
```

### Resetting the Server

To wipe all server state and start fresh:

```bash
cd /srv/vigo
docker compose down
sudo find /srv/vigo -mindepth 1 -delete
docker run --rm -v /srv/vigo:/srv/vigo us-west1-docker.pkg.dev/project-69f2499e-5082-48f0-b19/vigo/vigo:latest --seed-only
docker compose up -d
```

This re-seeds `server.yaml`, `.env`, `docker-compose.yml`, TLS certs, and examples.

---

## Log Messages to Watch

| Log Message | Meaning |
|------------|---------|
| `config reload failed` | YAML parse or validation error |
| `signature verification failed` | Agent sent an invalid ED25519 signature |
| `envoy revoked` | A check-in was rejected because the envoy is revoked |
| `state flusher: batch last_seen failed` | SQLite write error during FleetIndex flush |
| `webhook: delivery failed after retries` | All retry attempts for a webhook exhausted |
| `litestream exited` | Backup replication subprocess crashed |
| `rate limit exceeded` | AI assistant request rate limit hit |

---
*Verified on Vigo 0.51.6 · 2026-05-13.*
