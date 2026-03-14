# Quickstart

Get Hobnob running and manage your first thrall in 10 minutes.

## 1. Start the Server

```bash
sudo mkdir -p /srv/hobnob
docker run --rm -v /srv/hobnob:/srv/hobnob ghcr.io/daniel-alexander4/hobnob:latest --seed-only
cd /srv/hobnob
docker compose up -d
```

The first command seeds `/srv/hobnob/` with `docker-compose.yml`, `server.yaml`, `.env`, and example configs. The second starts the server using the seeded compose file.

Or with a single `docker run` (no compose):

```bash
sudo mkdir -p /srv/hobnob
docker run -d --name hobnob \
  -p 8181:8181 -p 1530:1530 \
  -v /srv/hobnob:/srv/hobnob \
  ghcr.io/daniel-alexander4/hobnob:latest
```

The server starts on `localhost:8181` (REST/UI) and `localhost:1530` (gRPC). TLS certificates, `server.yaml`, `.env`, and example configs are auto-generated on first start. Open `http://localhost:8181` to see the dashboard.

## 2. Enroll a Thrall

On the machine you want to manage:

```bash
curl -sSf http://<server-ip>:8181/bootstrap | sudo sh
```

The bootstrap script:
1. Downloads the `hobnob` agent binary
2. Generates an ED25519 keypair
3. Registers with the server (token-free if the server has trusted enrollment configured)
4. Installs and starts the `hobnob-thrall` systemd service

After enrollment, the thrall appears in the dashboard and begins checking in every 5 minutes.

## 3. Write a Module

Create a file at `/srv/hobnob/thrall-configs/modules/motd.hnb`:

```yaml
name: motd
resources:
  - name: motd-file
    type: file
    targetpath: /etc/motd
    content: |
      ========================================
      Managed by Hobnob
      Hostname: {{ .Traits.hostname }}
      OS: {{ .Traits.os.name }}
      ========================================
    owner: root
    group: root
    mode: "0644"
```

## 4. Map the Thrall to the Module

Create or edit `/srv/hobnob/thrall-configs/nodes.hnb`:

```yaml
thralls:
  - match: "*"
    modules: [motd]
```

This assigns the `motd` module to all thralls. The `*` glob matches any hostname.

## 5. Wait for Check-in

The agent checks in every 5 minutes (configurable). On the next check-in:

1. The agent sends its hostname and traits to the server
2. The server matches the hostname against `nodes.hnb` (first match wins)
3. The server resolves the `motd` module, renders the template with the thrall's traits
4. The agent receives the desired state and applies it:
   - If `/etc/motd` doesn't exist or differs → creates/updates it
   - If `/etc/motd` already matches → no action (idempotent)
5. The agent reports the result back to the server

## 6. Force an Immediate Check-in

Don't want to wait? Push to the thrall:

```bash
hobnobcli push --target "*"
```

Or push to a specific host:

```bash
hobnobcli push --target "web1.example.com"
```

## 7. Verify

Check the run result:

```bash
hobnobcli runs list --limit 5
```

Or view it in the web UI at `http://localhost:8181/runs`.

## What's Next

- [First Module](first-module.md) — Deeper module walkthrough with packages, services, and dependencies
- [Server Configuration](server-config.md) — Full server.yaml reference
- [Config Format](../concepts/config-format.md) — Modules, roles, vars, and environment_overrides
- [Executors](../executors/_index.md) — All 42 resource types
