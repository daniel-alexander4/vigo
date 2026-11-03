# 2 · Enroll your first envoy

By the end of this page a *second* machine — a real managed node — will be checking in to your server. Five minutes.

Standing up the server enrolled its own host over loopback ([tutorial 1](stand-up-the-server.md)). Enrolling a *remote* machine is a distinct path: it needs a one-time token, because it isn't on the auto-signed loopback CIDR.

## Prerequisites

- [Tutorial 1](stand-up-the-server.md) done — a running server.
- A second Linux/macOS host that can reach the server on `:1530` and `:8443`.
- `vigocli` configured to talk to your server (on the server host it already is).

## 1. Mint a one-time token

Decide which hostnames you'll let enroll, and mint a token scoped to them:

```bash
vigocli tokens generate --pattern '*.example.com'
```

This prints a one-time token (hashed at rest server-side; it can't be reused). The `--pattern` is an enrollment guard — only hostnames matching it can register.

## 2. Run the installer on the new machine

On the **new** host, fetch and run the bootstrap installer, passing the token:

```bash
curl -sSfk https://<server-host>:8443/bootstrap | sudo sh -s -- --token <token>
```

The installer downloads the agent binary, generates a CSR, enrolls (the server signs it after checking the pattern + token), installs a native service (systemd / launchd / …), and starts it. The agent is installed at `/usr/local/sbin/vigo`.

## 3. Watch it land

Back in the web UI dashboard, **within a check-in interval the new envoy appears** with its hostname, traits, and a green status. Or from the CLI:

```bash
vigocli envoys list
```

You'll see both envoys — the server's own host and the one you just enrolled.

## Checkpoint

Two envoys, both checking in. Nothing is being *enforced* yet — you haven't written any config. That's next.

**Next:** [Your first configcrate →](your-first-configcrate.md)

See also: [Enroll an envoy](../howto/operate/enroll-an-envoy.md) for the full procedure (offline installs, custom service managers, re-enrollment).
