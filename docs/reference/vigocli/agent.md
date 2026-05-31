
# Agent CLI Reference

`vigo` is the Rust agent binary that runs on managed machines (envoys). It is a separate binary from `vigocli` (the admin CLI).

In normal operation the agent runs as a daemon via a system service. It also provides subcommands for registration, one-shot convergence, diagnostics, and trait inspection.

## Global Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--server <ADDR>` | `localhost:1530` | Server gRPC address |
| `--ca-file <PATH>` | | CA certificate file |
| `--cert-file <PATH>` | | Client certificate file |
| `--key-file <PATH>` | | Client key file |
| `--log-level <LEVEL>` | `info` | Log level (trace, debug, info, warn, error) |
| `--api-addr <URL>` | `https://localhost:8443` | Server REST API address |
| `--executors-dir <PATH>` | `/etc/vigo-envoy/executors` | Custom executor scripts directory |
| `--snapshot-dir <PATH>` | `/var/lib/vigo/snapshots` | File snapshots directory |
| `--snapshot-retention <N>` | `5` | Max snapshots per file |
| `--trait-refresh-cycles <N>` | `10` | Trait refresh interval in check-in cycles |
| `--result-cache-cycles <N>` | `5` | Result cache lifetime in cycles |
| `--dry-run` | | Preview mode (no changes applied) |
| `--once` | | Run a single convergence cycle and exit |

The agent data directory is fixed at `/etc/vigo-envoy` on Linux (and the platform equivalent on other systems) — it holds the config file, certs, envoy UUID, and server pubkey. Runtime state lives under `/var/lib/vigo/` (state store, snapshots, swarm blobs). Settings are resolved in order: CLI flags > `/etc/vigo-envoy/config` > defaults. The config file is written automatically during `register` or `bootstrap`.

## Commands

| Command | Description |
|---------|-------------|
| *(none)* | Run as a daemon (normal operation) |
| `daemon` | Same as no command -- run the daemon loop |
| `register` | Register with the server |
| `bootstrap` | Full bootstrap (download certs, register, install service) |
| `run` | One-shot convergence |
| `status` | Show local agent state (config, cached policy, pending results) |
| `traits` | Run trait collectors and print JSON |
| `version` | Print agent version |
| `repo` | [Inspect and recover gitback personal-DR mirrors](swarm-gitback.md) |
| `crypt` | [Manage lockbox — per-user encrypted P2P directory](swarm-lockbox.md) |

---

## daemon (default)

With no subcommand, the agent runs as a daemon: it checks in with the server on a regular interval, applies resources, reports results, and handles streaming tasks. This is the normal mode when running as a system service.

```bash
vigo
vigo --once          # single cycle, then exit
vigo --dry-run       # preview mode
```

---

## register

Register this machine with the server using a one-time enrollment token.

```bash
vigo register --token <TOKEN> [--force]
```

| Flag | Description |
|------|-------------|
| `--token <TOKEN>` | One-time enrollment token (required) |
| `--force` | Revoke any existing enrollment for this hostname and re-register |

The server validates the token and hostname pattern, stores the agent's ED25519 public key, and returns a envoy UUID. The UUID and server address are saved to the config file for subsequent runs.

---

## bootstrap

Full bootstrap: generate TLS keypair/CSR, register via the REST API, receive signed certificates, and optionally install a system service.

```bash
vigo bootstrap --server <URL> [--token <TOKEN>] [--install-service] [--grpc-port <PORT>] [--force]
```

| Flag | Description |
|------|-------------|
| `--server <URL>` | Server REST API URL (required, e.g. `https://vigo.example.com:8443`) |
| `--token <TOKEN>` | Enrollment token (optional if trusted enrollment is configured) |
| `--install-service` | Install and start the platform service (systemd, launchd, rc.d, rcctl, SMF, Windows Service) |
| `--grpc-port <PORT>` | Server gRPC port (default: `1530`) |
| `--force` | Revoke any existing enrollment for this hostname and re-register |

In practice, the bootstrap shell script from `curl -sSfk https://<server>:8443/bootstrap | sudo sh` calls this command. See [Bootstrap](../../howto/enroll-an-envoy.md) for the full enrollment workflow.

---

## run

Run a single convergence cycle: check in, apply resources, report results, then exit.

```bash
vigo run [--configcrate <name>] [--dry-run]
```

| Flag | Description |
|------|-------------|
| `--configcrate <name>` | Only apply resources from this configcrate |
| `--dry-run` | Preview mode -- check state but do not apply changes |

Produces a color-coded trace to stderr showing each resource's status (created, updated, deleted, skipped, none, or error).

### Preemption

If the daemon is mid-convergence when `vigo run` is invoked, the manual run takes priority. It writes a preempt sentinel file and waits for the daemon to yield (up to 120 seconds). The daemon checks for the sentinel between resources and aborts its current cycle early, releasing the convergence lock. `vigo run` then acquires the lock and runs its own convergence.

If the daemon is not converging, `vigo run` acquires the lock immediately with no delay.

---

## status

Show local agent state by reading from the config file and LMDB state store. No server communication occurs -- this command works even when the server is unreachable.

```bash
vigo status
```

Displays:

- **Envoy ID** -- the UUID assigned during registration, or `(not registered)` if the agent has not enrolled

- **Server** -- the gRPC address the agent connects to
- **Bolt** -- the bolt address if the agent was routed by a spanner hub
- **Fallback servers** -- configured failover server addresses
- **Data directory** -- path to the agent's config and key material
- **State store** -- path to the LMDB state directory
- **Policy** -- cached policy bundle details: version, matched pattern, environment, roles, configcrate/resource counts, max age, and a per-configcrate resource listing
- **Traits** -- cached trait collector count and cache age
- **Pending results** -- number of convergence results queued for reporting (drains when the server is reachable)
- **Server pubkey** -- whether the server's bundle-signing public key is stored locally

### Example Output

```
Envoy ID:        a1b2c3d4-e5f6-7890-abcd-ef1234567890
Server:           vigo.example.com:1530
Fallback servers: vigo2.example.com:1530
Data directory:   /etc/vigo-envoy
State store:      /var/lib/vigo/state

Policy:
  Cached:         yes
  Version:        20260316T140000Z
  Match:          web-01.example.com
  Environment:    production
  Roles:          webserver, monitoring
  Configcrates:        3 (nginx, node_exporter, logrotate)
  Resources:      12
  Max age:        86400s
  [nginx]
    package: nginx
    file: /etc/nginx/nginx.conf
    file: /etc/nginx/sites-enabled/default
    service: nginx
  [node_exporter]
    package: prometheus-node-exporter
    service: prometheus-node-exporter
  [logrotate]
    file: /etc/logrotate.d/nginx

Traits:
  Cached:         yes
  Collectors:     25
  Cache age:      4m 32s

Pending results:  0
Server pubkey:    present
```

When the LMDB state store is not accessible (e.g., running as a non-root user on a machine where the state directory is root-owned), the output gracefully degrades:

```
Envoy ID:        a1b2c3d4-e5f6-7890-abcd-ef1234567890
Server:           vigo.example.com:1530
Fallback servers: none
Data directory:   /etc/vigo-envoy
State store:      /var/lib/vigo/state

Policy:           (state store unavailable: permission denied)
Traits:           (state store unavailable)
Pending results:  (state store unavailable)
Server pubkey:    absent
```

---

## traits

Run trait collectors and print the results as JSON to stdout. With no arguments, runs all collectors.

```bash
vigo traits                    # all collectors
vigo traits os network         # specific collectors
vigo traits os.family           # dot-path query (returns a single value)
```

When given collector names, only those collectors run. When given a dot-separated path, it runs all collectors and extracts the value at that path.

---

## version

Print the agent version and exit.

```bash
vigo version
```

---

## Related

- [Bootstrap](../../howto/enroll-an-envoy.md) -- Agent enrollment workflow
- [Compiled Promises](../../reference/writing-configcrates.md) -- Offline convergence and LMDB state store
- [Guardrails](../../howto/troubleshoot.md) -- Self-protection for agent paths
- [Troubleshooting](../../howto/troubleshoot.md) -- Common issues and diagnostics
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../../legal/license.md).*
