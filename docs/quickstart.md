# Quickstart

Stand up the Vigo server, enroll your first envoy, and apply your first configcrate. Ten minutes, copy-paste. If you'd rather be walked through it step by step, take the [tutorials](start/) instead — same path, more explanation.

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
  --network host \
  -v /srv/vigo:/srv/vigo \
  us-west1-docker.pkg.dev/project-69f2499e-5082-48f0-b19/vigo/vigo:latest
```

`--network host` binds 8443 (REST/UI), 1530 (gRPC), and 1531 (swarm) directly on the host — matching the generated compose, which host-networks the server so agent traffic doesn't route through Docker's per-port userland proxy. (Scrier's `guacd` sidecar isn't started by this bare `docker run`; use compose if you need RDP/VNC scrier sessions.)

Open `https://localhost:8443` — accept the self-signed cert, you'll land on the dashboard.

## 2. Enroll envoys

**The server's own host first.** Every Vigo server runs a co-hosted `vigo` agent so it can participate in swarm operations alongside its envoys. From the server host — **use the IPv4 loopback literal `127.0.0.1`, not `localhost` and not the host's own hostname**; the agent dials whatever address the curl URL used on every subsequent check-in, and only loopback is resolver-independent:

```bash
curl -sSfk https://127.0.0.1:8443/bootstrap | sudo sh
```

Then any *other* machine you want to manage — note this is a separate command shape, by design, because remote envoys reach the server by hostname / LAN IP while the server's own envoy reaches itself by loopback:

```bash
# On the envoy:
curl -sSfk https://<server-ip>:8443/bootstrap | sudo sh
```

The `-k` is intentional. The Vigo server runs its own CA and presents a self-signed cert. Trust gets established on the *next* step: the bootstrap script downloads the agent, generates a per-host ED25519 keypair, and registers it with the server (token-free from loopback and private-LAN CIDRs by default; require a one-time token for anything outside the trusted-enrollment ranges — mint one with `vigocli tokens generate --pattern '*.example.com'` and pass it as `sudo sh -s -- --token <token>`). Every subsequent gRPC call between the agent and the server is mTLS-pinned against that pair — `-k` only relaxes the one-time bootstrap fetch, not the steady-state security.

The bootstrap script also installs the per-OS service (systemd / launchd / rc.d / rcctl / SMF / Windows Service).

After enrollment each envoy appears in the dashboard at `https://localhost:8443/envoys` and begins checking in every 5 minutes.

## 3. Write a configcrate

```yaml
# /srv/vigo/stacks/configcrates/motd.vgo
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

## 4. Assign the configcrate to envoys

The simplest assignment — every envoy gets it:

```yaml
# /srv/vigo/stacks/common.vgo
configcrates: [motd]
```

For per-host assignment use a hostcrate:

```yaml
# /srv/vigo/stacks/envoys.vgo
envoys:
  - match: "web*.prod"
    configcrates: [motd, nginx]
  - match: "db*.prod"
    configcrates: [motd, postgres]
  - match: "*"
    configcrates: [motd]
```

## 5. Publish

```bash
sudo vigocli config publish
```

Validates the tree, syncs to `/srv/vigo/.live/`, triggers a server reload. If validation fails, the built-in AI assistant prints fix suggestions to your terminal.

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

- [Write your first real configcrate](howto/configcrates/write-your-first-configcrate.md) — packages, services, dependencies, conditionals
- [Set up Scrier](howto/operate/set-up-scrier.md) — admin remote-access (SSH / RDP / VNC) without inbound ports
- [Compliance reporting](howto/compliance/compliance-reporting.md) — turn configcrate tags into HIPAA / SOC 2 / PCI / NIST coverage reports
- [The configcrate language](reference/configcrate-language.md) — the full configcrate-authoring guide
- [Reference / vigocli](reference/vigocli/) — every CLI command
- [Reference / agent resources](reference/vigo/resources/) — every executor type
- [Reference / agent traits](reference/vigo/traits/) — every trait collector

---
*Confidential — Alexander4, LLC. Not for redistribution. See [legal/license.md](legal/license.md).*
