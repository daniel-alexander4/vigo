# 1 · Stand up the server

By the end of this page you'll have a running Vigo server, be logged into its web UI, and have the server's own machine enrolled as the first envoy. Budget ten minutes, plus about 20 for the first image build.

This is the first of four tutorials — work through them in order; each builds on the last.

## Prerequisites

- A Linux host with Docker (Compose and Buildx plugins), `git` and `rsync`, plus Go 1.25 if you create the optional signing key.
- Ports 8443, 1530 and 1531 reachable from the machines you will enroll.
- Root (or `sudo`) on that host.
- That's it. No external services.

## 1. Bring it up

Prebuilt images are not published yet, so build the server image from source first. The build runs inside Docker. The host needs Docker with the Compose and Buildx plugins, plus `git` and `rsync`; on Ubuntu, `sudo apt-get install -y git rsync docker.io docker-compose-v2 docker-buildx` installs them. The first build takes about 20 minutes. The optional signing key lets envoys update their agent on their own; without it, every upgrade means re-running the bootstrap on each envoy. Creating it needs Go 1.25, and you should keep the key safe.

```bash
git clone https://github.com/daniel-alexander4/vigo.git && cd vigo
go run ./server/hardening/cmd/sign -genkey -output signing.key   # optional: lets envoys self-update
sudo scripts/upgrade.sh full --norun   # builds the server image and the amd64 agent
IMAGE=vigo:latest   # the tag the build gives the local image
```

Then seed the data directory and start it:

```bash
sudo mkdir -p /srv/vigo
sudo docker run --rm -v /srv/vigo:/srv/vigo "$IMAGE" --seed-only
cd /srv/vigo && sudo docker compose up -d
```

The server listens on **`:1530`** (gRPC, for agents) and **`:8443`** (REST + web UI). Give it a few seconds, then check it's healthy:

```bash
docker compose ps
```

You'll see `vigo` and `vigo-guacd` both `Up (healthy)`. While the server is still starting, `vigo` shows `Up (health: starting)`; `curl -sk https://127.0.0.1:8443/healthz` prints `ok` once it is ready.

## 2. Log in

Open **`https://127.0.0.1:8443`** in a browser and accept the self-signed certificate. There is no default admin login. Do step 3 first, then create your admin from a real OS account on this host with `sudo vigocli webusers create --username <yourname> --role admin` and `sudo vigocli webusers set-password --username <yourname>`, and sign in as `<yourname>`.

You'll land on the dashboard — empty for now, because no envoys exist yet.

## 3. Enroll the server's own machine

Every Vigo server runs a local agent alongside it, enrolled over loopback. This is the **first thing to do after install** — run it from the server host:

```bash
curl -sSfk https://127.0.0.1:8443/bootstrap | sudo sh
```

> Use the IPv4 literal **`127.0.0.1`** — not `localhost`, not the hostname. The agent writes this exact address into its config and dials it on every check-in, so the path must not depend on name resolution.

The default trusted-enrollment policy (private + loopback CIDRs) auto-signs this one without a token. Within a check-in interval, **refresh the dashboard — one envoy appears.** That's the server's own machine, now managed.

![The Vigo dashboard — fleet health, security, compliance, and risk at a glance](../img/screenshots/vigo.dashboard.png)

## Checkpoint

You have: a healthy server, a browser session, and one converging envoy. If the dashboard still shows zero envoys after a minute, see [Install](../howto/operate/install.md#troubleshooting).

**Next:** [Enroll your first envoy →](enroll-your-first-envoy.md) — a *separate* machine.
