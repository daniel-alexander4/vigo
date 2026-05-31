
# Install the Vigo server

You'll finish this page with a Vigo server running in Docker on a host of your choice, listening on `:8443` (REST + Web UI) and `:1530` (gRPC), with TLS material auto-generated and a seeded config tree under `/srv/vigo/`. From there, [Enroll an envoy](enroll-an-envoy.md) is the natural next step.

**When you'd use this:** standing up Vigo for the first time, or re-installing after wiping a host. If you already have a running server and want to *upgrade* it, see the upgrade note at the bottom of this page.

**When you'd skip this:** if someone else stood up the server and you only need to enroll your laptop or a managed node, skip ahead to [Enroll an envoy](enroll-an-envoy.md).

Vigo has three components: the server (`vigosrv`), the CLI (`vigocli`), and the agent (`vigo`). The server and CLI ship as a container image. The agent is installed on each managed machine via a bootstrap script.

![Vigo dashboard after install and first check-in](../img/screenshots/vigo.dashboard.png)

## Server

### 1. Seed the data directory

```bash
sudo mkdir -p /srv/vigo
docker run --rm -v /srv/vigo:/srv/vigo us-west1-docker.pkg.dev/project-69f2499e-5082-48f0-b19/vigo/vigo:latest --seed-only
```

This populates `/srv/vigo/` with everything needed to run the server:

```
/srv/vigo/
├── docker-compose.yml       # production compose file
├── server.yaml              # server config
├── .env                     # non-secret env vars
├── tls/                     # mTLS certificates (auto-generated)
├── license/                 # license .lic files
├── db/                      # SQLite database
├── secrets/                 # encrypted secret files
├── stacks/                  # config operation root
│   ├── stockpile/           # operator-edited working tree
│   │   ├── configcrates/    # reusable configcrate definitions
│   │   ├── usercrates/      # user-scoped configcrates (inert until included via `usercrates:`)
│   │   ├── templates/       # Go template fragments
│   │   ├── tasks/           # reusable task definitions
│   │   ├── workflows/       # workflow definitions
│   │   ├── envoys.vgo       # hostcrate (any .vgo with envoys: works)
│   │   ├── roles.vgo        # role definitions (single, fleet-wide)
│   │   ├── common.vgo       # directory-inherited defaults
│   │   ├── waivers.vgo      # compliance waivers (any depth)
│   │   ├── environments.vgo # per-env configcrate/var overrides
│   │   └── compliance.vgo   # directory-inherited claims
│   ├── examples/            # read-only install templates
│   │                        #   (rebuilt from image on every container start;
│   │                        #    vigocli config examples copy)
│   ├── anti-examples/       # read-only retract templates (mirrors examples/configcrates)
│   │                        #   (rebuilt from image on every container start;
│   │                        #    vigocli config anti-examples copy / mirror-check)
│   └── .live/               # published config (locked between publishes)
├── config-history/          # rollback snapshots
└── docs/                    # browsable documentation
```

### 2. Start the server

```bash
cd /srv/vigo
docker compose up -d
```

The server starts on ports **1530** (gRPC, agent check-in) and **8443** (HTTPS — REST API, web UI, metrics, bootstrap). Three CLI steps stand the install up from here, **in order**:

1. **Bootstrap the secrets vault** — `vigocli secrets init --key-file /srv/vigo/secrets/.master.key` (prompts for the unlock passphrase), then `vigocli secrets unlock`.
2. **Enroll the local agent** — `curl -sSfk https://127.0.0.1:8443/bootstrap | sudo sh` from the server host. **Use `127.0.0.1`, not `localhost` or this host's own hostname** — the agent the script installs dials whatever address the curl URL used on every subsequent check-in, and only the IPv4 loopback literal is resolver-independent (no nsswitch, no mDNS, no IPv6 link-local trap). This step enrolls *only* the server's own host; every other machine you want to manage follows the separate procedure in [Enroll an envoy](enroll-an-envoy.md), which dials the server by its reachable hostname. The server has a canonical assumption (per CLAUDE.md) that a `vigo` agent runs on the same host alongside the container; the admin-create step below uses that agent's `users` trait to verify the OS-user mapping. Wait until the agent has reported one check-in (~30s) before continuing.
3. **Create your per-operator admin** — `sudo vigocli webusers create --username <yourname> --role admin` from the server host, where `<yourname>` is a real human OS account on this machine (uid≥1000, real shell). The CLI auto-reads your pubkey from `/home/<yourname>/.ssh/id_ed25519.pub` (fallback `id_rsa.pub`); no `--ssh-key-file` flag for admin role. Then `vigocli secrets set password vigo/web/auth/<yourname>` to set your login password.

Sign in at `https://localhost:8443/login` as `<yourname>`. The default `admin` user is deliberately not auto-created — admin webusers map 1:1 with real OS users so audit attribution always names a real human and browser scrier sessions seed your pubkey automatically. If you need multiple operators, repeat step 3 for each.

If no envoy has checked in yet, the dashboard shows a first-run panel with the step-2 bootstrap command and an otherwise-empty nav — both fill in automatically once the first envoy enrolls.

### 3. Verify

```bash
docker compose ps                    # container running
curl -sk https://127.0.0.1:8443/healthz # health check
vigocli doctor                     # full diagnostic
```

### 4. Enroll the server's own envoy (do this first)

Vigo assumes every server runs a co-hosted `vigo` agent on the same host as the vigosrv container. The server's own envoy is what makes the server visible in `vigocli swarm status`, lets it participate in puddle / lockbox / gitback, and seeds payloads to peers via the swarm substrate. This step is the canonical first thing to do after the container is up — and it is **distinct from enrolling other envoys** (covered in [Enroll an envoy](enroll-an-envoy.md)) because the server's own agent dials *itself*, and that path must not depend on hostname resolution.

From the server host itself:

```bash
curl -sSfk https://127.0.0.1:8443/bootstrap | sudo sh
```

`127.0.0.1` is the IPv4 loopback literal — no nsswitch, no DNS, no mDNS, no IPv6 link-local. The bootstrap script writes whatever address the curl URL used into `/etc/vigo-envoy/config`, so the agent dials that exact address on every check-in. If you use `localhost` or this host's own hostname, the agent's check-in path silently inherits a dependency on the system's name-resolution stack — and the moment any layer of it shifts (Starlink prefix delegation rotates, NetworkManager re-announces, the host gets renamed), the agent stops being able to dial home. Use `127.0.0.1` even if `localhost` works today.

The bootstrap script downloads the agent binary, generates a CSR, registers via the trusted-enrollment path (loopback is in the default `trusted_enrollment` CIDR list — no token needed), and installs `vigo-envoy.service` under systemd. After it finishes:

```bash
systemctl is-active vigo-envoy       # → active
vigocli envoys list                  # the server should appear under its host's hostname
```

The agent's enrolled hostname is whatever `hostname` returns on the host. Match this with `server.hostname` in `server.yaml` (used for compliance display) so the two views agree.

If you skip this step, swarm features that route through the server envoy will silently no-op for the server's own hostname. `vigocli swarm filecast distribute` still works (writes directly to the bind-mounted seed dir), but most other swarm operations don't involve the server in the peer set.

### Optional services

Edit `VIGO_SERVICES` in `/srv/vigo/.env` to enable optional containers:

```bash
# In .env, uncomment one of:
VIGO_SERVICES=ollama              # local AI assistant (free, CPU-only)
VIGO_SERVICES=postgres            # Postgres instead of SQLite
VIGO_SERVICES=ollama,postgres     # both
```

### Timezone

The container inherits the host's timezone via a read-only mount of `/etc/localtime`. All timestamps in the web UI, reports, and audit logs display in the server's local timezone. If you need to override the timezone, set the `TZ` environment variable in `.env`:

```bash
TZ=America/New_York
```

### Configuration

Edit `server.yaml` for server settings (check-in interval, TLS paths, spanner federation, SMTP, webhooks). Edit `.env` for optional services and environment variables. See [Server Configuration](../reference/server-yaml.md) for the full reference.

### Upgrading

```bash
cd /srv/vigo
docker compose pull
docker compose up -d
```

Your config, database, secrets, and TLS certs persist in `/srv/vigo/` across upgrades.

## CLI

The container automatically installs `vigocli` to `/usr/local/bin` on the host via the `/host-bin` volume mount. If the mount is not configured, install manually:

```bash
docker cp vigo:/usr/local/bin/vigocli /usr/local/bin/vigocli
```

The CLI auto-detects the server at `https://localhost:8443`. For remote servers:

```bash
export VIGO_SERVER="http://vigo-server:8443"
```

## Agent

The agent runs on managed machines (envoys). It supports Linux, macOS, FreeBSD, OpenBSD, NetBSD, illumos, and Windows.

### Linux

```bash
curl -sSfk https://<server>:8443/bootstrap | sudo sh
```

Downloads the agent binary, generates a keypair, registers with the server, and installs a systemd service (`vigo-envoy`).

### macOS

```bash
curl -sSfk https://<server>:8443/bootstrap | sudo sh
```


Detects macOS and installs a launchd plist (`com.vigo.envoy`).

### FreeBSD / NetBSD

```bash
curl -sSfk https://<server>:8443/bootstrap | sudo sh
```

Installs an rc.d service script at `/usr/local/etc/rc.d/vigo_envoy`.

### OpenBSD

```bash
curl -sSfk https://<server>:8443/bootstrap | sudo sh
```

Registers the service via `rcctl` and enables it at boot.

### illumos

```bash
curl -sSfk https://<server>:8443/bootstrap | sudo sh
```

Imports an SMF service manifest and enables it via `svcadm`.

### Windows

```powershell
# PowerShell 7+
irm https://<server>:8443/bootstrap?os=windows -SkipCertificateCheck | iex

# PowerShell 5
[Net.ServicePointManager]::ServerCertificateValidationCallback = {$true}; irm https://<server>:8443/bootstrap?os=windows | iex
```

Downloads the agent, registers with the server, and installs a Windows service. The cert-skip flags are the equivalent of curl's `-k` for the self-signed CA.

See [Bootstrap](enroll-an-envoy.md) for details on the enrollment flow, token-based authentication, and trusted enrollment patterns.

## Ports

| Port | Protocol | Purpose |
|------|----------|---------|
| 1530 | gRPC (mTLS) | Agent check-in, result reporting, streaming |
| 8443 | HTTPS | REST API, web UI, metrics, bootstrap |

## Agent directory layout

```
/etc/vigo-envoy/            # config + identity
├── envoy.uuid                # registration ID
├── certs/                     # TLS keypair
│   ├── ca.crt
│   ├── tls.crt
│   └── tls.key
└── executors/                 # custom executor scripts

/var/lib/vigo/               # persistent state
├── state/                     # LMDB (policy cache, checksums, traits, pending results)
├── snapshots/                 # file snapshots for rollback
└── flags/                     # flag markers for when: expressions
```

## What success looks like

A healthy server reports its components individually via `vigocli doctor`:

```text
$ docker exec vigo /usr/local/bin/vigocli doctor
  database ................ OK
  tls ..................... OK (expires 2027-05-05)
  secrets ................. OK (local)
  license ................. OK (Compliant, 5/100 envoys)
  spanner .................. SKIP (standalone)
  integrations ............ SKIP (no integrations configured)
  config .................. OK (43 configcrates, 5 entries)
  swarm_pattern_order ..... OK (all swarm.*.enabled pattern lists well-ordered)
  stale_envoys ............ OK
  Status: healthy
```

`SKIP` is expected for `spanner` (you're not running hub-spoke) and `integrations` (you haven't wired any external alerting yet). `database / tls / secrets / license / config` should all be `OK` on a fresh install. A `WARN` on `stale_envoys` after the first check-in cycle means at least one envoy hasn't reported in `2.5 × max(interval, observed_cycle)` seconds — go look at it.

## What's next

- **Add a managed node** → [Enroll an envoy](enroll-an-envoy.md).
- **Write your first declarative config** → [Write your first configcrate](write-your-first-module.md).
- **If the server didn't come up clean** → [Troubleshoot common issues](troubleshoot.md).

---
*Verified on Vigo 0.51.6 · 2026-05-13.*

*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../legal/license.md).*
