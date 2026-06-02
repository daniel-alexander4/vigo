# 1 · Stand up the server

By the end of this page you'll have a running Vigo server, be logged into its web UI, and have the server's own machine enrolled as the first envoy. Budget ten minutes.

This is the first of four tutorials — work through them in order; each builds on the last.

## Prerequisites

- A Linux host with Docker + Docker Compose.
- Root (or `sudo`) on that host.
- That's it. No external services.

## 1. Bring it up

From a checkout of the server bundle:

```bash
make up        # docker compose up -d (vigosrv + guacd)
```

The server listens on **`:1530`** (gRPC, for agents) and **`:8443`** (REST + web UI). Give it a few seconds, then check it's healthy:

```bash
docker compose ps
```

You'll see the `vigo` container `Up` and `vigo-guacd` `Up (healthy)`.

## 2. Log in

Open **`https://127.0.0.1:8443`** in a browser and accept the self-signed certificate. Log in with the bootstrap admin credentials printed in the server logs (`docker compose logs vigo | grep -i admin`).

You'll land on the dashboard — empty for now, because no envoys exist yet.

## 3. Enroll the server's own machine

Every Vigo server runs a local agent alongside it, enrolled over loopback. This is the **first thing to do after install** — run it from the server host:

```bash
curl -sSfk https://127.0.0.1:8443/bootstrap | sudo sh
```

> Use the IPv4 literal **`127.0.0.1`** — not `localhost`, not the hostname. The agent writes this exact address into its config and dials it on every check-in, so the path must not depend on name resolution.

The default trusted-enrollment policy (private + loopback CIDRs) auto-signs this one without a token. Within a check-in interval, **refresh the dashboard — one envoy appears.** That's the server's own machine, now managed.

## Checkpoint

You have: a healthy server, a browser session, and one converging envoy. If the dashboard still shows zero envoys after a minute, see [Install](../howto/operate/install.md#troubleshooting).

**Next:** [Enroll your first envoy →](enroll-your-first-envoy.md) — a *separate* machine.

---
*Confidential — Alexander4, LLC. Not for redistribution.*
