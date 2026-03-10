# Hobnob Agent

## Summary

The `hobnob` agent daemon runs on managed nodes. It pulls desired state from the central server via gRPC over mTLS, applies idempotent resource changes, and reports results back.

- **Binary name:** `hobnob`
- **Architecture:** Pull-based -- the agent checks in with the server on a configurable interval
- **Security:** mTLS transport + ED25519 signature verification on every RPC
- **Modes:** Daemon mode (continuous loop) and one-shot commands (`run`, `traits`, `register`)

## Quickstart (Bootstrap)

### Curl-pipe bootstrap (trusted enrollment)

1. On the server, configure `trusted_enrollment` in `server.yaml`:
   ```yaml
   bootstrap:
     trusted_enrollment:
       - pattern: "*.example.com"
         cidrs: ["10.0.0.0/8"]   # optional CIDR restriction
   ```

2. On the managed node:
   ```
   curl -sSf http://hobd.example.com:8181/bootstrap | sudo sh
   ```
   This downloads the agent binary, generates keys + CSR, registers with the server (validated by hostname pattern + source IP), receives signed TLS certs, and installs a systemd service.

3. Verify:
   ```
   sudo systemctl status hobnob-node
   ```

### Manual bootstrap (token-based)

When trusted enrollment is not configured, use a one-time token:

```bash
# On the server
hobnobcli tokens generate
hobnobcli approved add "*.example.com" --token <token>

# On the managed node
sudo hobnob bootstrap --server http://hobd.example.com:8181 --token <token> --install-service
```

### Manual registration (gRPC, no CSR signing)

For nodes that already have TLS certificates:

```
hobnob register --server hobd.example.com:1530 --token <token>
```

## CLI Subcommands

| Command | Description |
|---------|-------------|
| `hobnob` (default) | Daemon mode, continuous check-in loop |
| `hobnob run` | Single convergence cycle, then exit |
| `hobnob run --module <name>` | Run only one module |
| `hobnob run --dry-run` | Preview changes without applying |
| `hobnob traits` | Print all system traits as JSON |
| `hobnob traits os` | Print only OS traits |
| `hobnob traits os.distro` | Print a single value |
| `hobnob register` | One-time enrollment with the server |
| `hobnob bootstrap` | Full bootstrap (register + TLS + systemd service) |
| `hobnob version` | Print version |

## Global Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--data-dir` | `/etc/hobnob-node` | Keypair, UUID, TLS certs |
| `--server` | `localhost:1530` | gRPC server address |
| `--ca-file` | | CA certificate path |
| `--cert-file` | | Client TLS certificate path |
| `--key-file` | | Client TLS private key path |
| `--log-level` | `info` | Log verbosity: debug, info, warn, error |
| `--api-addr` | `http://localhost:8181` | Server REST API address for self-update |
| `--executors-dir` | `/etc/hobnob-node/executors` | Directory for custom executor scripts |

## Architecture

The agent is organized into these subsystems:

| Subsystem | Path | Purpose |
|-----------|------|---------|
| Check-in loop | `agent/checkin/` | Pull loop with jitter (base_interval +/- jitter_percent), exponential backoff (5s to 5min), delta policy transfer |
| Auth | `agent/auth/` | ED25519 keypair generation + ECDSA P-256 TLS key management |
| Resource executors | `agent/modules/` | 23 built-in resource executors (see `agent/modules/README.md`) |
| Trait collectors | `agent/traits/` | 25 pluggable trait collectors (see `agent/traits/README.md`) |
| Policy evaluation | `agent/policy/` | Local policy bundle evaluation (conditional vars, template rendering) |
| Runner | `agent/runner/` | DAG-ordered execution with notify/subscribe triggers |
| Custom traits | `agent/customtraits/` | Custom trait executable sync + execution (JSON protocol, SHA256 caching, 10s timeout) |
| Snapshots | `agent/snapshots/` | File snapshot manager for rollback support |

## Data Directory Layout

```
/etc/hobnob-node/
├── agent.key           # ED25519 signing keypair (PKCS8 PEM)
├── client.uuid         # Node UUID assigned by server
├── certs/
│   ├── tls.key         # ECDSA P-256 TLS private key
│   ├── tls.crt         # Server-signed TLS client cert
│   └── ca.crt          # CA cert for server verification
├── traits/             # Custom trait executable cache
└── executors/          # Custom executor scripts
```

## Check-in Loop

The daemon runs an infinite check-in loop with the following cycle:

1. **Collect traits** -- volatile traits refresh every cycle, stable traits every 10 cycles
2. **Sync and run custom traits** -- download/update server-managed trait executables if configured
3. **Check in with server** -- send traits inline, receive policy via delta transfer
4. **Short-circuit** -- if no config change detected, skip execution
5. **Evaluate policy bundle locally** -- resolve conditional vars, render Go templates
6. **Execute resources in DAG order** -- idempotent check-then-act for each resource
7. **Notify pass** -- re-apply resources triggered by changed dependencies
8. **Report results** -- send per-resource outcomes to server
9. **Sleep with jitter** -- wait for next cycle

**Defaults:** 5-minute base interval, 10% jitter, exponential backoff from 5 seconds to 5 minutes on error.
