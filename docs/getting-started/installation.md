# Installation

Hobnob has three components: the server (`hobd`), the CLI (`hobnobcli`), and the agent (`hobnob`). The server and CLI ship as a container image. The agent is installed on each managed machine via a bootstrap script.

## Server

### 1. Seed the data directory

```bash
sudo mkdir -p /srv/hobnob
docker run --rm -v /srv/hobnob:/srv/hobnob ghcr.io/daniel-alexander4/hobnob:latest --seed-only
```

This populates `/srv/hobnob/` with everything needed to run the server:

```
/srv/hobnob/
├── docker-compose.yml       # production compose file
├── server.yaml              # server config
├── .env                     # non-secret env vars
├── tls/                     # mTLS certificates (auto-generated)
├── license/                 # license .lic files
├── db/                      # SQLite database
├── secrets/                 # encrypted secret files
├── hnb/
│   ├── proposed/            # edit .hnb config files here
│   │   ├── modules/
│   │   ├── roles/
│   │   ├── thralls/
│   │   ├── templates/
│   │   └── vars/
│   └── .live/               # published config (do not edit directly)
├── config-history/          # rollback snapshots
├── examples/                # example .hnb modules, roles, and nodes
└── docs/                    # browsable documentation
```

### 2. Start the server

```bash
cd /srv/hobnob
docker compose up -d
```

The server starts on ports **1530** (gRPC, agent check-in) and **8181** (REST API, web UI, metrics, bootstrap). On first access, the web UI shows setup instructions — create an admin account with `hobnobcli users create --username admin --role admin` and set a password with `hobnobcli users set-password --username admin`, then sign in at `http://localhost:8181/login`.

### 3. Verify

```bash
docker compose ps                    # container running
curl -s http://localhost:8181/healthz # health check
hobnobcli doctor                     # full diagnostic
```

### Optional services

Edit `HOBNOB_SERVICES` in `/srv/hobnob/.env` to enable optional containers:

```bash
# In .env, uncomment one of:
HOBNOB_SERVICES=ollama              # local AI assistant (free, CPU-only)
HOBNOB_SERVICES=postgres            # Postgres instead of SQLite
HOBNOB_SERVICES=ollama,postgres     # both
```

### Configuration

Edit `server.yaml` for server settings (check-in interval, TLS paths, spanner, SMTP, webhooks). Edit `.env` for optional services and environment variables. See [Server Configuration](server-config.md) for the full reference.

### Upgrading

```bash
cd /srv/hobnob
docker compose pull
docker compose up -d
```

Your config, database, secrets, and TLS certs persist in `/srv/hobnob/` across upgrades.

## CLI

The container automatically installs `hobnobcli` and `hnbvi` to `/usr/local/bin` on the host via the `/host-bin` volume mount. If the mount is not configured, install manually:

```bash
docker cp hobnob:/usr/local/bin/hobnobcli /usr/local/bin/hobnobcli
ln -sf hobnobcli /usr/local/bin/hnbvi
```

The CLI auto-detects the server at `http://localhost:8181`. For remote servers:

```bash
export HOBNOB_SERVER="http://hobnob-server:8181"
```

## Agent

The agent runs on managed machines (thralls). It supports Linux, macOS, FreeBSD, OpenBSD, NetBSD, illumos, and Windows.

### Linux

```bash
curl -sSf http://<server>:8181/bootstrap | sudo sh
```

Downloads the agent binary, generates a keypair, registers with the server, and installs a systemd service (`hobnob-thrall`).

### macOS

```bash
curl -sSf http://<server>:8181/bootstrap | sudo sh
```

Detects macOS and installs a launchd plist (`com.hobnob.thrall`).

### FreeBSD / NetBSD

```bash
curl -sSf http://<server>:8181/bootstrap | sudo sh
```

Installs an rc.d service script at `/usr/local/etc/rc.d/hobnob_thrall`.

### OpenBSD

```bash
curl -sSf http://<server>:8181/bootstrap | sudo sh
```

Registers the service via `rcctl` and enables it at boot.

### illumos

```bash
curl -sSf http://<server>:8181/bootstrap | sudo sh
```

Imports an SMF service manifest and enables it via `svcadm`.

### Windows

```powershell
irm http://<server>:8181/bootstrap?os=windows | iex
```

Downloads the agent, registers with the server, and installs a Windows service.

See [Bootstrap](bootstrap.md) for details on the enrollment flow, token-based authentication, and trusted enrollment patterns.

## Ports

| Port | Protocol | Purpose |
|------|----------|---------|
| 1530 | gRPC (mTLS) | Agent check-in, result reporting, streaming |
| 8181 | HTTP | REST API, web UI, metrics, bootstrap |

## Agent directory layout

```
/etc/hobnob-thrall/            # config + identity
├── thrall.uuid                # registration ID
├── certs/                     # TLS keypair
│   ├── ca.crt
│   ├── tls.crt
│   └── tls.key
└── executors/                 # custom executor scripts

/var/lib/hobnob/               # persistent state
├── state/                     # LMDB (policy cache, checksums, traits, pending results)
├── snapshots/                 # file snapshots for rollback
└── flags/                     # flag markers for when: expressions
```
