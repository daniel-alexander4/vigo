
# Set up Scrier

Scrier is Vigo's admin remote-access feature: interactive SSH, RDP, and VNC sessions to any enrolled envoy, routed through the Vigo server and the envoy's already-open outbound gRPC connection. **No inbound port is required on the envoy.** The same connection the agent uses for check-in carries the session bytes back.

## What you get

- **SSH** from the Web UI (xterm.js) and from `vigocli scrier ssh`.
- **RDP** from the Web UI (Guacamole).
- **VNC console-shadowing** from the Web UI — watch what's actually on a Linux user's screen, with optional consent prompt.
- Per-session ephemeral SSH keys (installed transiently in the target user's `authorized_keys`, removed at session close).
- Every session recorded in the `scrier_sessions` table; every connect/disconnect lands in the tamper-evident audit log.

## Enable it in `server.yaml`

```yaml
scrier:
  enabled: true
  guacd_addr: "guacd:4822"            # Docker-compose service hostname:port
  proxy_hostname: "vigo"              # how guacd reaches back to vigosrv
  allowed_ports:                      # ports the agent will tunnel to
    - 22
    - 3389
    - 5900
  max_sessions: 10                    # concurrent session cap
  recording_enabled: false            # schema placeholder; not yet writing
```

If you're using the bundled Docker Compose, `guacd_addr: "guacd:4822"` and `proxy_hostname: "vigo"` are correct as-is — the `vigo-guacd` sidecar ships in the same compose file. Bare-metal installs need to provide their own `guacd` instance.

Restart `vigosrv` after editing.

## Give each web user an SSH pubkey

Scrier's SSH path identifies the operator by their SSH pubkey. The agent scans every regular user's `~/.ssh/authorized_keys` on the target envoy and lands you in whichever user holds your key. **Root is never a Scrier landing target** — use `sudo` for elevation.

The CLI auto-detects your pubkey at every `vigocli scrier ssh` invocation (chain: `--ssh-key-file` flag → `$SSH_USER_AUTH` if sshd has `ExposeAuthInfo yes` → `/proc/self/loginuid`'s home directory → `$SUDO_USER`'s home) and ships it in the connect request, so the server-side `users.ssh_public_key` stays in sync without you having to set it manually. The first time you run `vigocli scrier ssh <envoy>` on a host where your SSH key lives, the column is populated.

For pure browser-only users (no CLI invocation ever), set the pubkey via REST:

```bash
TOKEN=...    # admin API token
curl -X PUT https://vigo:8443/api/v1/users/<id> \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"ssh_public_key":"ssh-ed25519 AAAA...","display_name":"...","email":"...","role":"admin"}'
```

The `PUT` replaces every field — include the unchanged ones.

## Make sure the operator's pubkey is in some regular user's authorized_keys on each envoy

This is the only state Scrier-SSH needs on the envoy itself. The conventional way is to declare a `user` resource in a usercrate that lists the operator's pubkey in `authorized_keys`:

```yaml
# /srv/vigo/stacks/stockpile/usercrates/dan.vgo
resources:
  - name: dan
    type: user
    username: dan
    shell: /bin/bash
    state: present
    sudo_nopasswd: "true"
    authorized_keys: |
      ssh-ed25519 AAAA... operator@laptop
```

The user executor writes this file on every convergence and strips Scrier's ephemeral marker lines before comparing, so transient session keys don't trigger drift alerts.

## Use it

**From the browser:**

```
https://vigo:8443/scrier/<envoy_id>?protocol=ssh
https://vigo:8443/scrier/<envoy_id>?protocol=rdp
https://vigo:8443/scrier/<envoy_id>?protocol=vnc
```

Admin-only. xterm.js for SSH, Guacamole client for RDP/VNC.

**From the terminal:**

```bash
vigocli scrier ssh annlap                   # by hostname
vigocli scrier ssh 3c5a7f2b-0000-...        # by envoy UUID
vigocli scrier ssh annlap --ssh-key-file ~/.ssh/id_other.pub   # override auto-detect
```

## How it works

```mermaid
sequenceDiagram
    autonumber
    participant Op as Operator
    participant Srv as Vigo server
    participant Agt as Agent on envoy
    participant Sd as sshd on envoy (127.0.0.1:22)

    Op->>Srv: POST /api/v1/scrier/connect<br/>(envoy_id, protocol, ssh_public_key?)
    Note right of Srv: validate pubkey,<br/>write-through users.ssh_public_key
    Srv->>Agt: TunnelRequest (over existing AgentStream)
    Agt->>Srv: open TunnelStream gRPC
    Srv->>Agt: TunnelOpen<br/>(verify_pubkey, ephemeral_pubkey)
    Note right of Agt: scan /home/*/.ssh/authorized_keys<br/>for verify_pubkey<br/>(getpwent, uid≠0)
    Agt->>Agt: install ephemeral_pubkey in matched user<br/>(# vigo-scrier-ephemeral marker)
    Agt->>Srv: TunnelOpenResult (ssh_username = <matched user>)
    Srv->>Sd: SSH dial 127.0.0.1:22 with ephemeral_privkey,<br/>auth as <matched user> (via tunnel)
    Op->>Srv: WebSocket /ws/scrier/{session_id}
    Srv-->>Op: raw PTY bytes ↔ Sd via tunnel
    Note over Agt: on close: strip ephemeral line<br/>(RAII; also a startup-sweep<br/>safety net)
```



1. Operator hits `POST /api/v1/scrier/connect` with `{envoy_id, protocol, port, ssh_public_key (optional)}`. The CLI populates `ssh_public_key` from local discovery; the browser leaves it empty.
2. Server creates a `scrier_sessions` row in state `waiting` and pushes a `TunnelRequest` to the envoy over its already-open `AgentStream` — the only server-initiated leg.
3. Agent opens a new `TunnelStream` gRPC connection back to the server. Server replies with `TunnelOpen`: SSH-target pubkey + per-session ephemeral pubkey for SSH, or target `host:port` for RDP/VNC.
4. For SSH, the agent scans regular users via `getpwent`, finds whichever user's `authorized_keys` contains the operator pubkey, appends the ephemeral key with a `# vigo-scrier-ephemeral` marker, and reports back the OS username. Server SSH-dials `127.0.0.1:22` over the tunnel using the ephemeral private key.
5. For RDP/VNC, the agent connects to `127.0.0.1:<port>` on the envoy; Guacamole handshakes server-side and proxies display instructions over the operator's WebSocket as binary frames.
6. Operator's client (xterm.js or Guacamole.js or the CLI's WebSocket reader) attaches to `/ws/scrier/{session_id}` and bytes flow.
7. Either side closing the WebSocket closes the tunnel. The agent strips its ephemeral line from `authorized_keys` — the cleanup runs even if the agent crashes mid-session, and a sweep at agent startup removes any stale lines left from previous crashes. The session row is marked `closed` and an `scrier.disconnect` audit record is written.

## Common failure modes

| Symptom | Cause | Fix |
|---|---|---|
| Session errors with `SSH pubkey not authorized on this envoy` | The operator's pubkey isn't in any regular user's `authorized_keys` on the target. | Add it to a usercrate's `authorized_keys` and republish; or `ssh-copy-id` it once and let the user executor pick it up. |
| Session errors with `no SSH pubkey available` | `users.ssh_public_key` is empty AND the connect request had no `ssh_public_key` field. | Run `vigocli scrier ssh <envoy>` once from a host with your SSH key on disk; or `PUT /api/v1/users/{id}` with `ssh_public_key`. |
| Session errors with `agent did not open TunnelStream within 90s` | The envoy isn't responding to the wake signal. Common cause: disk pressure or the agent is wedged. | Check `vigocli envoys list` for staleness; check the agent's host disk via `vigocli traits livequery host_self.fs_root_pct`. |
| RDP / VNC works but SSH doesn't | Tunnel infrastructure is fine; the break is in SSH-specific auth. | Almost always one of the two SSH cases above. Inspect server logs around `scrier session state`. |

## Limits

- Admin-only. The `compliance` and `viewer` roles can't initiate Scrier sessions.
- Session recording is a schema placeholder; nothing actually writes recordings yet.
- No SCP / file transfer. Interactive shell only.
- Wayland VNC isn't supported (no reliable Wayland-native VNC server at time of writing).
- Windows VNC (TightVNC) is on the roadmap, not shipped.
- The CLI requires a TTY — piped invocations are refused.

---
*Confidential — Alexander4, LLC. Not for redistribution. See [../legal/license.md](../legal/license.md).*
