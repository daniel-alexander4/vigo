
# vigocli scrier

Manage Scrier remote access from the CLI.

Scrier is Vigo's admin-only remote-access feature. Sessions route through
the Vigo server and the envoy's already-open outbound gRPC connection, so
the customer never has to open an inbound port for the operator to reach
the envoy.

## Subcommands

### users list

List configured web-user → OS-user mappings.

```bash
vigocli scrier users list
```

### users map

Map a web UI username to the OS username Scrier logs in as on the envoy.

```bash
vigocli scrier users map --user alice --os-user ubuntu
```

### ssh

Open an interactive shell on an enrolled envoy. Same tunnel path as the
browser-based Scrier SSH — just from the terminal instead of a browser.

```bash
vigocli scrier ssh web-01.prod
vigocli scrier ssh 3c5a7f2b-0000-0000-0000-000000000000
```

`<envoy>` may be a hostname or an envoy UUID. The OS user the shell logs
in as is determined by the mapping configured with
`vigocli scrier users map` — there is no per-invocation override.

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--timeout` | `30s` | How long to wait for the envoy's agent to bring the session to `ready` before giving up. |

**How it works:**

1. CLI calls `POST /api/v1/scrier/connect` to create a `scrier_session` row.
2. Server pushes a `TunnelRequest` down the envoy's existing `AgentStream`; the agent opens a dedicated `TunnelStream` back.
3. Server SSH-dials `127.0.0.1:22` on the envoy over that tunnel, using the ephemeral key pair it installed in the target OS user's `authorized_keys`.
4. CLI upgrades to a WebSocket at `/ws/scrier/{session_id}` and tunnels raw PTY bytes + resize control messages.

Nothing touches the customer's firewall: the envoy's outbound TCP to the Vigo server is the only path used.

**Exit codes:**

| Code | Meaning |
|------|---------|
| 0 | Remote shell exited normally (`exit`, Ctrl-D). |
| non-zero | Setup failed (envoy unreachable, scrier_users not mapped, timeout, auth failure) or the tunnel dropped mid-session. |

**Requirements:**

- Admin role on the CLI's token (same gate the browser flow uses).
- `vigocli scrier users map` configured for your web user → an OS user on the target envoy.
- The envoy must have `sshd` listening on `127.0.0.1:22`; Scrier does not expose SSH on the envoy's external interface.
- stdin and stdout must both be TTYs — piped invocations are refused with a clear error.

**Not yet implemented:**

- Session recording (`scrier.recording_enabled` is a schema placeholder; no writes yet for browser or CLI sessions).
- `scp` / file transfer.
- Per-invocation OS-user override (use `users map` to change who you log in as).
- Windows — the CLI command is built under `//go:build unix`.

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
