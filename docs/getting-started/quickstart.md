
# Quickstart

Get Vigo running and manage your first envoy in 10 minutes.

## 1. Start the Server

```bash
sudo mkdir -p /srv/vigo
docker run --rm -v /srv/vigo:/srv/vigo us-west1-docker.pkg.dev/project-69f2499e-5082-48f0-b19/vigo/vigo:latest --seed-only
cd /srv/vigo
docker compose up -d
```

The first command seeds `/srv/vigo/` with `docker-compose.yml`, `server.yaml`, `.env`, and example configs. The second starts the server using the seeded compose file.

Or with a single `docker run` (no compose):

```bash
sudo mkdir -p /srv/vigo
docker run -d --name vigo \
  -p 8443:8443 -p 1530:1530 \
  -v /srv/vigo:/srv/vigo \
  us-west1-docker.pkg.dev/project-69f2499e-5082-48f0-b19/vigo/vigo:latest
```

The server starts on `localhost:8443` (REST/UI) and `localhost:1530` (gRPC). TLS certificates, `server.yaml`, `.env`, and example configs are auto-generated on first start. Open `https://localhost:8443` to see the dashboard.

## 2. Enroll a Envoy

On the machine you want to manage:

```bash
curl -sSfk https://<server-ip>:8443/bootstrap | sudo sh
```

The bootstrap script:
1. Downloads the `vigo` agent binary
2. Generates an ED25519 keypair
3. Registers with the server (token-free if the server has trusted enrollment configured)
4. Installs and starts the `vigo-envoy` systemd service

After enrollment, the envoy appears in the dashboard and begins checking in every 5 minutes.

## 3. Write a Module

Create a file at `/srv/vigo/stockpile/modules/motd.vgo`:

```yaml
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

## 4. Map the Envoy to the Module

The simplest approach: create a `common.vgo` at the root that assigns the module to all envoys via directory inheritance:

```yaml
# /srv/vigo/stockpile/common.vgo
modules: [motd]
```

Then create a catch-all leaf entry in a subdirectory:

```bash
mkdir -p /srv/vigo/stockpile/all
```

```yaml
# /srv/vigo/stockpile/all/all.vgo
envoys:
  - match: "*"
```

Every envoy inherits `motd` from `common.vgo`. As you add more modules to `common.vgo`, all envoys get them automatically.

Alternatively, for a flat layout without inheritance, you can put the envoy entry in a `envoys/` directory:

```yaml
# /srv/vigo/stockpile/envoys/envoys.vgo
envoys:
  - match: "*"
    modules: [motd]
```

## 5. Publish the Config

Configs in `stage/` don't take effect until published:

```bash
vigocli config publish
```

This validates the config, syncs it to the `.live/` directory, and triggers the server to reload.

## 6. Wait for Check-in

The agent checks in every 5 minutes (configurable). On the next check-in:

1. The agent sends its hostname and traits to the server
2. The server matches the hostname against `envoys.vgo` (first match wins)
3. The server resolves the `motd` module, renders the template with the envoy's traits
4. The agent receives the desired state and applies it:
   - If `/etc/motd` doesn't exist or differs → creates/updates it
   - If `/etc/motd` already matches → no action (idempotent)
5. The agent reports the result back to the server

## 7. Force an Immediate Check-in


Don't want to wait? Force a check-in on the envoy:

```bash
vigocli envoys push --all
```

Or force a specific host:

```bash
vigocli envoys push web1.example.com
```

## 8. Verify

Check the run result:

```bash
vigocli runs list --limit 5
```

Or view it in the web UI at `https://localhost:8443/runs`.

## What's Next

- [First Module](first-module.md) — Deeper module walkthrough with packages, services, and dependencies
- [Server Configuration](server-config.md) — Full server.yaml reference
- [Config Format](../concepts/config-format.md) — Modules, roles, vars, and environment_overrides
- [Executors](../reference/executors/_index.md) — All 74 resource types
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
