# Installation

Hobnob has three components: the server (`hobd`), the CLI (`hobnobcli`), and the agent (`hobnob`).

## Docker (recommended)

The fastest way to run the server:

```bash
sudo mkdir -p /etc/hobnob-server
docker run -d --name hobnob \
  -p 8181:8181 -p 1530:1530 \
  -v /etc/hobnob-server:/etc/hobnob-server \
  hobnob/hobnob:latest
```

This starts the server on ports 1530 (gRPC) and 8181 (REST/UI). TLS certificates, a default config, and the SQLite database are auto-generated on first start. All persistent state lives in `/etc/hobnob-server`.

## Server Setup

1. Create the server directory:

```bash
sudo mkdir -p /etc/hobnob-server
```

2. Copy the example config:

```bash
cp server-configs/server.yaml.example /etc/hobnob-server/server.yaml
```

3. Edit `server.yaml` — at minimum set the database DSN:

```yaml
database:
  dsn: "/etc/hobnob-server/db/hobnob.db"
```

4. Create the thrall configs directory:

```bash
mkdir -p /etc/hobnob-server/thrall-configs/modules
mkdir -p /etc/hobnob-server/thrall-configs/roles
```

5. Start the server:

```bash
cd /etc/hobnob-server
hobd
```

The server auto-generates TLS certificates on first start when using the `local` secrets backend.

## CLI Setup

The CLI connects to the server's REST API. Point it at your server:

```bash
export HOBNOB_SERVER="https://localhost:8181"
export HOBNOB_CERT="/etc/hobnob-server/tls/cert"
export HOBNOB_KEY="/etc/hobnob-server/tls/key"
export HOBNOB_CA="/etc/hobnob-server/tls/ca"
```

Verify connectivity:

```bash
hobnobcli doctor
```

## Agent Installation

The agent runs on Linux, macOS, FreeBSD, OpenBSD, NetBSD, and Windows. On each managed machine (thrall), the agent can be installed via the bootstrap script.

### Linux

```bash
curl -sSf http://<server>:8181/bootstrap | sudo sh
```

This downloads the agent binary, generates a keypair, registers with the server, and installs a systemd service (`hobnob-thrall`).

### macOS

```bash
curl -sSf http://<server>:8181/bootstrap | sudo sh
```

The same bootstrap script detects macOS and installs a launchd plist (`com.hobnob.thrall`) instead of a systemd unit.

### FreeBSD / NetBSD

```bash
curl -sSf http://<server>:8181/bootstrap | sudo sh
```

On FreeBSD and NetBSD, the bootstrap script installs an rc.d service script at `/usr/local/etc/rc.d/hobnob_thrall`.

### OpenBSD

```bash
curl -sSf http://<server>:8181/bootstrap | sudo sh
```

On OpenBSD, the bootstrap script registers the service via `rcctl` and enables it at boot.

### Windows

```powershell
irm http://<server>:8181/bootstrap?os=windows | iex
```

The PowerShell bootstrap script downloads the agent, registers with the server, and installs a Windows service.

See [Bootstrap](bootstrap.md) for details on the enrollment flow.

## Ports

| Port | Protocol | Purpose |
|------|----------|---------|
| 1530 | gRPC (mTLS) | Agent check-in, result reporting, streaming |
| 8181 | HTTP | REST API, web UI, metrics, bootstrap |

## Directory Layout

### Server (`/etc/hobnob-server/`)

```
/etc/hobnob-server/
├── server.yaml              # Server configuration
├── db/
│   └── hobnob.db            # SQLite database
├── secrets/                 # Local secrets backend (AES-256-GCM encrypted)
│   └── hobnob/
│       ├── tls/             # Auto-generated TLS certs
│       └── db/dsn           # Database DSN
├── thrall-configs/          # Config source of truth
│   ├── modules/             # Module definitions (.hnb)
│   ├── roles/               # Role definitions (.hnb)
│   └── nodes.hnb            # Node-to-role mappings
├── tasks/                   # Reusable task definitions
├── workflows/               # Workflow definitions
├── custom-traits/           # Custom trait collector scripts
└── docs/                    # Documentation (this site)
```

### Agent (`/etc/hobnob-thrall/`)

```
/etc/hobnob-thrall/
├── agent.yaml               # Agent configuration
├── client.crt               # Client TLS certificate
├── client.key               # Client private key
└── ca.crt                   # CA certificate
/var/lib/hobnob/
└── state/                   # LMDB state store
    ├── policy/              # Cached policy bundles
    ├── checksums/           # File checksum cache
    ├── traits/              # Cached traits (with TTL)
    └── pending/             # Queued results for offline mode
```
