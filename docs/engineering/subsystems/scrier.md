
# Scrier — Remote Access

Scrier is Vigo's admin-only remote-access feature: interactive SSH, RDP, and VNC sessions to any enrolled envoy, routed through the Vigo server and the envoy's agent-initiated gRPC stream. Operators use Scrier from the browser (xterm.js for SSH, Guacamole for RDP/VNC) or from the CLI (`vigocli scrier ssh`). The envoy never has to expose any inbound port — the same outbound connection it uses for check-in carries shell and desktop traffic.

## Why Scrier Exists

Configuration management answers "what should be true." Remote access answers "what's going on right now." When an envoy is in a bad state that Vigo's automation can't yet reason about, operators need a shell on the box. In MSP and enterprise deployments that means a tool that:

1. **Works through customer firewalls with no inbound ports** — port 22 is not exposed, there's no bastion, there's no site-to-site VPN.
2. **Uses the same identity and auth surface** operators already use for Vigo (web user, admin role, API token).
3. **Produces an auditable record** of who connected where and when, with the same reliability as convergence runs.

Scrier is that tool. It reuses the envoy→server outbound gRPC channel that already exists for check-ins, so an operator on a laptop in Vigo HQ can shell into an envoy inside a customer's private network without either side changing firewall rules.

## How It Works

The data path is the same for browser and CLI; only the outermost client differs.

1. **Connect.** Operator calls `POST /api/v1/scrier/connect` with `{envoy_id, protocol, port}`. The server creates a `scrier_sessions` row in state `waiting` and returns a session ID.
2. **Summon agent.** The server pushes a `TunnelRequest` to the envoy over its already-open `AgentStream`. This is the only server-initiated leg; the control plane is otherwise envoy-driven.
3. **Agent dials back.** The agent opens a new `TunnelStream` gRPC connection to the server — a second outbound connection, deliberately kept off the check-in hot path so shell I/O can't stall policy traffic.
4. **Tunnel ready.** Server sends `TunnelOpen` on that stream. For SSH, `TunnelOpen` carries an ephemeral public key the agent installs in a target user's `authorized_keys`. For RDP/VNC, it carries the target host:port. Session status becomes `connecting`, then `ready` once the backend (SSH client or guacd) is fully attached.
5. **Relay.** The operator's client upgrades to a WebSocket at `/ws/scrier/{session_id}`. Bytes flow client ↔ server ↔ tunnel ↔ agent ↔ target daemon on the envoy. For SSH, the "target daemon" is `127.0.0.1:22` on the envoy itself — the agent acts as a local TCP proxy.
6. **Teardown.** Either side closing the WebSocket closes the tunnel. The agent removes the ephemeral SSH key from `authorized_keys` (via RAII; runs even on panic). The session row is marked `closed` with duration, and an `scrier.disconnect` audit record is written.

## Access Paths

| Surface | Protocols | Client |
|---------|-----------|--------|
| Browser (`/scrier/{envoy_id}`) | SSH, RDP, VNC | xterm.js (SSH) / Guacamole.js (RDP, VNC) |
| CLI (`vigocli scrier ssh`) | SSH only | Headless WebSocket client with raw-mode PTY |

Both clients speak the exact same `/ws/scrier/{id}` WebSocket protocol. Binary frames carry raw PTY/display bytes; text frames carry JSON control messages (today only `{"type":"resize","cols":N,"rows":M}`).

## Protocols

### SSH

The server runs the SSH client (using Go's `crypto/ssh`); the agent relays raw TCP bytes to `127.0.0.1:22` on the envoy. The shell lives on the envoy's own `sshd`, but `sshd` only needs to listen on localhost — it never needs to be exposed to the network.

Authentication is ephemeral-key-per-session. For each new session the server generates a fresh ED25519 keypair, sends the public half in `TunnelOpen`, and the agent installs it in the target user's `~/.ssh/authorized_keys` for the duration of the session. The key is removed when the tunnel closes (including on crash).

### RDP

Browser RDP routes through a `guacd` container that speaks RDP natively. The agent relays raw TCP bytes to the target Windows host:port (typically 3389). Guacamole handshakes on the server side and proxies display instructions over the WebSocket as binary frames.

### VNC (Shadow Mode)

VNC is used for **console shadowing** — watching or assisting what's actually on the user's screen, not logging in as a separate session. The agent spawns a VNC server attached to the active X display, and routes the connection through `guacd` the same way RDP does.

- **Linux**: the agent runs `x11vnc -once` on a dynamic ephemeral port, optionally prompting the console user for consent before starting. One-shot: the VNC server exits after the session drops.
- **Wayland**: not supported. No reliable Wayland-native VNC server exists at time of writing.
- **Windows**: deferred. TightVNC support is on the roadmap but not shipped.

## User Mapping

Scrier decides which OS user to log in as via the `scrier_users` mapping: `web_user → os_user`. The mapping is managed through the CLI:

```bash
vigocli scrier users map --user alice --os-user ubuntu
vigocli scrier users list
```

When a session starts, the agent scans `authorized_keys` for each local user to find the one whose key list matches the verify pubkey the server sent — that defines the target OS user. If no mapping exists, the session fails at setup.

There is deliberately no per-session OS-user override; the mapping is the single source of truth for "who is this operator, as which local user, on this envoy."

## Authentication & Authorization

- **Admin only.** Both the REST connect endpoint and the WebSocket relay enforce `user.IsAdmin()`. Viewer-role users cannot initiate Scrier sessions.
- **Bearer token or session cookie.** Browser sessions come in via SCS cookies; CLI sessions come in via the same `Authorization: Bearer <token>` used by every other `vigocli` call.
- **Localhost peer-auth** applies the same way it does to any other admin endpoint — a `vigocli` invocation from the server host or Docker bridge is implicitly admin. (Known gap: OIDC and isowebauth auth modes do not yet implement the localhost peer-auth fast path — tracked in `pending.md`.)

## Session Lifecycle & Audit

Every session is tracked in the `scrier_sessions` table with envoy ID, user ID, username, protocol, target port, tunnel ID, start/end timestamps, duration, status, and (reserved) `recording_path`. On session close an `scrier.disconnect` audit record is written to the unified audit log.

Session states:

| Status | Meaning |
|--------|---------|
| `waiting` | Session row created; server has not yet received the agent's TunnelStream. |
| `connecting` | Agent connected; server is setting up the SSH client or guacd handshake. |
| `ready` | Backend attached; WebSocket relay can begin. |
| `relaying` | Operator's WebSocket is attached and bytes are flowing. |
| `error` | Setup failed; error text on the session row. |
| `closed` | Session ended cleanly or abnormally. |

Stuck sessions (no WebSocket connected within the setup window) are garbage-collected by a background reaper.

## Firewall & Network Story

The envoy's agent-initiated outbound TCP to the Vigo server on port 1530 is the **only** path Scrier uses. Specifically, Scrier does *not* require:

- Any inbound port on the envoy (port 22 can be firewalled off from everything but localhost).
- A separate bastion or jump host.
- Site-to-site VPN or tailnet.
- A separate connection for operator-to-envoy traffic. The operator reaches the Vigo server; the Vigo server reaches the envoy over the already-open agent stream.

For MSP and regulated-customer environments where network changes need a change-control ticket, this means Scrier "just works" the moment the envoy enrolls — no firewall request needed.

## Configuration

Scrier is off by default. Enable it in `server.yaml`:

```yaml
scrier:
  enabled: true
  guacd_addr: "guacd:4822"        # Docker-compose service name:port
  proxy_hostname: "vigo"          # Hostname guacd uses to reach back to vigosrv
  allowed_ports:                  # Ports the agent is willing to tunnel to
    - 22
    - 3389
    - 5900
  max_sessions: 10                # Concurrent session cap on this server
  recording_enabled: false        # Schema placeholder — not yet written
  recording_dir: "/srv/vigo/recordings"
```

The `vigo-guacd` sidecar container is required for RDP/VNC. The Docker compose bundle ships with it; bare-metal deployments need to provide their own `guacd` instance.

## Not Yet Implemented

- **Session recording.** `recording_path` exists in the schema and `recording_enabled` is a config knob, but nothing actually writes recordings today. Both browser and CLI sessions are un-recorded.
- **SCP / file transfer.** Only interactive shell.
- **Windows shadow mode** (TightVNC).
- **CLI RDP / VNC.** `vigocli scrier ssh` exists; graphical protocols are browser-only.
- **Container/pod shell.** `kubectl exec` / `docker exec` over Scrier is explicitly not on the roadmap.

## Related

- [`vigocli scrier`](../../reference/cli/scrier.md) — CLI command reference (including `scrier ssh`)
- [Architecture](../architecture/overview.md) — How the `AgentStream` that Scrier piggybacks on is established
- [Observe Mode](../../operations/observe-mode.md) — Drift-only convergence, for when you're trying to understand an envoy's state without changing it (often used alongside a Scrier session)

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
