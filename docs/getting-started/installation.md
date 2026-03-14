# Installation

Hobnob has three components: the server (`hobd`), the CLI (`hobnobcli`), and the agent (`hobnob`).

## Docker Compose (recommended)

```bash
sudo mkdir -p /srv/hobnob
docker run --rm -v /srv/hobnob:/srv/hobnob hobnob/hobnob:latest true
cd /srv/hobnob
docker compose up -d
```

The first command seeds `/srv/hobnob/` with `docker-compose.yml`, `server.yaml`, `.env`, example configs, and documentation. The second starts the server.

This starts the server on ports 1530 (gRPC) and 8181 (REST/UI). On first start, the container seeds `/srv/hobnob/` with:

- `server.yaml` — server configuration
- `.env` — environment variables (non-secret)
- `tls/` — auto-generated mTLS certificates
- `hnb/proposed/` — directory for your `.hnb` config files
- `examples/` — example modules, roles, and node configs
- `docs/` — browsable documentation (also at `http://localhost:8181/docs`)

All persistent state lives in `/srv/hobnob` on the host.

### Optional services

Enable AI assistant (local Ollama) or Postgres by setting `COMPOSE_PROFILES`:

```bash
# With AI assistant
COMPOSE_PROFILES=ollama docker compose -f docker-compose.prod.yml up -d

# With Postgres instead of SQLite
COMPOSE_PROFILES=postgres docker compose -f docker-compose.prod.yml up -d

# Both
COMPOSE_PROFILES=ollama,postgres docker compose -f docker-compose.prod.yml up -d
```

### Directory layout

After first start, `/srv/hobnob/` contains:

```
/srv/hobnob/
├── server.yaml              # server config
├── .env                     # non-secret env vars
├── tls/                     # mTLS certificates (auto-generated)
├── license/                 # license .lic files
├── db/                      # SQLite database
├── secrets/                 # encrypted secret files
├── hnb/
│   ├── proposed/            # edit .hnb files here
│   │   ├── modules/
│   │   ├── roles/
│   │   └── thralls/
│   └── .live/               # published config (do not edit directly)
├── config-history/          # rollback snapshots
├── examples/                # example .hnb files
└── docs/                    # documentation
```

### Single-container alternative

If you prefer `docker run` without compose:

```bash
sudo mkdir -p /srv/hobnob
docker run -d --name hobnob \
  -p 8181:8181 -p 1530:1530 \
  -v /srv/hobnob:/srv/hobnob \
  hobnob/hobnob:latest
```

## Server Setup

1. Create the server directory:

```bash
sudo mkdir -p /srv/hobnob
```

2. Copy the example config:

```bash
cp server-configs/server.yaml.example /srv/hobnob/server.yaml
```

3. Edit `server.yaml` — at minimum set the database DSN:

```yaml
database:
  dsn: "/srv/hobnob/db/hobnob.db"
```

4. Create the thrall configs directory:

```bash
mkdir -p /srv/hobnob/thrall-configs/modules
mkdir -p /srv/hobnob/thrall-configs/roles
```

5. Start the server:

```bash
cd /srv/hobnob
hobd
```

The server auto-generates TLS certificates on first start when using the `local` secrets backend.

## CLI Setup

The CLI connects to the server's REST API. Point it at your server:

```bash
export HOBNOB_SERVER="https://localhost:8181"
export HOBNOB_CERT="/srv/hobnob/tls/cert"
export HOBNOB_KEY="/srv/hobnob/tls/key"
export HOBNOB_CA="/srv/hobnob/tls/ca"
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

### Server (`/srv/hobnob/`)

```
/srv/hobnob/
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
