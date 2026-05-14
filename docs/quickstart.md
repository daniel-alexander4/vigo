
# Quickstart

Stand up the Vigo server, enroll your first envoy, and apply your first module.

## 1. Start the server

```bash
sudo mkdir -p /srv/vigo
docker run --rm -v /srv/vigo:/srv/vigo \
  us-west1-docker.pkg.dev/project-69f2499e-5082-48f0-b19/vigo/vigo:latest --seed-only
cd /srv/vigo
docker compose up -d
```

`--seed-only` writes `/srv/vigo/docker-compose.yml`, `server.yaml`, `.env`, TLS material, and example configs, then exits. `docker compose up -d` starts the server. REST + Web UI on `https://localhost:8443`, gRPC on `localhost:1530`.

Single-`docker run` if you don't want compose:

```bash
sudo mkdir -p /srv/vigo
docker run -d --name vigo \
  -p 8443:8443 -p 1530:1530 \
  -v /srv/vigo:/srv/vigo \
  us-west1-docker.pkg.dev/project-69f2499e-5082-48f0-b19/vigo/vigo:latest
```

Open `https://localhost:8443` — accept the self-signed cert, you'll land on the dashboard.

## 2. Enroll envoys

**The server's own host first.** Every Vigo server runs a co-hosted `vigo` agent so it can participate in swarm operations alongside its envoys. From the server host:

```bash
curl -sSfk https://localhost:8443/bootstrap | sudo sh
```

Then any other machine you want to manage:

```bash
# On the envoy:
curl -sSfk https://<server-ip>:8443/bootstrap | sudo sh
```

The `-k` is intentional. The Vigo server runs its own CA and presents a self-signed cert. Trust gets established on the *next* step: the bootstrap script downloads the agent, generates a per-host ED25519 keypair, and registers it with the server (token-free from loopback by default; require tokens elsewhere). Every subsequent gRPC call between the agent and the server is mTLS-pinned against that pair — `-k` only relaxes the one-time bootstrap fetch, not the steady-state security.

The bootstrap script also installs the per-OS service (systemd / launchd / rc.d / rcctl / SMF / Windows Service).

After enrollment each envoy appears in the dashboard at `https://localhost:8443/envoys` and begins checking in every 5 minutes.

## 3. Write a module

```yaml
# /srv/vigo/stacks/stockpile/modules/motd.vgo
name: motd
resources:
  - name: motd-file
    type: file
    target_path: /etc/motd
    content: |
      ========================================
      Managed by Vigo
      Hostname: {{ .Traits.network.hostname }}
      OS: {{ .Traits.os.distro }}
      ========================================
    owner: root
    group: root
    mode: "0644"
```

## 4. Assign the module to envoys

The simplest assignment — every envoy gets it:

```yaml
# /srv/vigo/stacks/stockpile/common.vgo
modules: [motd]
```

For per-host assignment use a matchfile:

```yaml
# /srv/vigo/stacks/stockpile/envoys.vgo
envoys:
  - match: "web*.prod"
    modules: [motd, nginx]
  - match: "db*.prod"
    modules: [motd, postgres]
  - match: "*"
    modules: [motd]
```

## 5. Publish

```bash
sudo vigocli config publish
```

Validates the tree, syncs to `/srv/vigo/stacks/.live/`, triggers a server reload. If validation fails, the built-in AI assistant prints fix suggestions to your terminal.

## 6. Force an immediate check-in (don't wait 5 minutes)

```bash
sudo vigocli envoys push --all
```

## 7. Verify

```bash
sudo vigocli runs list --limit 5
```

Or the Web UI at `https://localhost:8443/runs`. You should see `motd` applied successfully on every enrolled envoy.

## Where to go next

- [Write your first real module](howto/write-your-first-module.md) — packages, services, dependencies, conditionals
- [Set up Scrier](howto/set-up-scrier.md) — admin remote-access (SSH / RDP / VNC) without inbound ports
- [Compliance reporting](howto/compliance-reporting.md) — turn module tags into HIPAA / SOC 2 / PCI / NIST coverage reports
- [Reference / writing modules](reference/writing-modules.md) — the full module-authoring guide
- [Reference / vigocli](reference/vigocli/) — every CLI command
- [Reference / agent resources](reference/vigo/resources/) — every executor type
- [Reference / agent traits](reference/vigo/traits/) — every trait collector

---
*Confidential — Alexander4, LLC. Not for redistribution. See [legal/license.md](legal/license.md).*
