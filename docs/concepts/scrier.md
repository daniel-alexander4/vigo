# Scrier

**How do you get an interactive shell on a node that has no inbound ports open?** You don't connect *to* it — it connects *out*, and **Scrier** rides that existing connection.

## The short answer

Scrier gives an admin a live SSH, RDP, or VNC session to any envoy **without opening a single inbound port on the node**. The session reverses out through the agent's already-open `AgentStream`, with the byte stream carried on a separate `TunnelStream`. From the node's perspective, nothing new listens; it's the same outbound mTLS connection it always holds.

## Why this is safe by construction

- **No inbound surface.** The agent never accepts a connection for Scrier; it dials `127.0.0.1:{port}` *locally* and relays bytes up its own stream. There is no lateral-movement path — the tunnel can only reach loopback on the target.
- **Transient, per-session keys.** Each session installs a fresh per-session Ed25519 keypair in the matched user's `authorized_keys`, used once, removed after.
- **Root is never a landing target.** Scrier lands the operator in a *regular* user's session.
- **The right user, proven by key.** A web user's `ssh_public_key` is the verification source: the agent walks every regular OS user (via libc `getpwent`) and lands the operator in whichever user's `authorized_keys` holds that pubkey.

RDP and VNC ride a `guacd` sidecar so the graphical session renders in the browser.

## The mental model

Think of it as **reverse remote access**: instead of "operator → firewall → node", it's "node → server ← operator", with the server brokering a tunnel over connections that already exist. That's what makes it work across NAT, behind firewalls, on networks you don't control.

## Where this shows up

- [Set up Scrier](../howto/operate/set-up-scrier.md) — user mappings, SSH keys, first session.
- [`vigocli scrier`](../reference/vigocli/scrier.md) + the browser terminal in the UI.
- [The security model](security-model.md) — where Scrier sits in the posture.
