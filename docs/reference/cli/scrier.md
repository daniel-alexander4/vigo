
# vigocli scrier

Manage Scrier remote access from the CLI.

Scrier is Vigo's admin-only remote-access feature. Sessions route through
the Vigo server and the envoy's already-open outbound gRPC connection, so
the customer never has to open an inbound port for the operator to reach
the envoy.

## Subcommands

### ssh

Open an interactive shell on an enrolled envoy. Same tunnel path as the
browser-based Scrier SSH — just from the terminal instead of a browser.

```bash
vigocli scrier ssh web-01.prod
vigocli scrier ssh 3c5a7f2b-0000-0000-0000-000000000000
```

`<envoy>` may be a hostname or an envoy UUID.

The OS user the shell lands in is whichever regular user on the envoy has
your SSH pubkey in their `~/.ssh/authorized_keys`. Arrange (typically via
the same usercrate that creates the Unix account) for that key to land in
the right user's authorized_keys on each envoy you want to reach. Root is
never a Scrier landing target — use sudo for elevation.

**Pubkey discovery.** The CLI auto-detects your pubkey at invocation time;
you don't run any setup verb. The chain, first hit wins:

1. `--ssh-key-file PATH` — explicit override.
2. `$SSH_USER_AUTH` — the pubkey sshd recorded for the current SSH session. Strongest signal; requires `ExposeAuthInfo yes` in `/etc/ssh/sshd_config` on the host you're invoking from.
3. `/proc/self/loginuid` → that user's `~/.ssh/id_ed25519.pub` (then `id_rsa.pub`). Survives every sudo / `su -` transition because loginuid is a process attribute, not env. Linux-only.
4. `$SUDO_USER`'s home directory → same pubkey filenames. Belt-and-suspenders for `sudo vigocli` invocations that don't go through `su -`.
5. Error: `cannot auto-detect your SSH pubkey … pass --ssh-key-file PATH`.

The auto-detected pubkey is shipped with the connect request and write-through-stored on your web user (`users.ssh_public_key`) so subsequent browser sessions reuse it.

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--timeout` | `30s` | How long to wait for the envoy's agent to bring the session to `ready` before giving up. |
| `--ssh-key-file` | (auto-detect) | Path to an SSH public key file. Overrides the discovery chain — useful when you keep multiple keys and the auto-detected one isn't the one already in your envoys' `authorized_keys`. |

**How it works:**

1. CLI calls `POST /api/v1/scrier/connect` to create a `scrier_session` row.
2. Server pushes a `TunnelRequest` down the envoy's existing `AgentStream`; the agent opens a dedicated `TunnelStream` back.
3. Server sends the operator's pubkey + a fresh per-session ephemeral pubkey in `TunnelOpen`. The agent walks every regular user's authorized_keys (libc `getpwent` — covers `/home/*`, macOS `/Users/*`, BSDs, LDAP/NSS), finds which user holds the operator's pubkey, installs the ephemeral key in that user's file (marked `# vigo-scrier-ephemeral`), and reports the username back.
4. Server SSH-dials `127.0.0.1:22` on the envoy over the tunnel, authenticating as that user with the ephemeral private key.
5. CLI upgrades to a WebSocket at `/ws/scrier/{session_id}` and tunnels raw PTY bytes + resize control messages.

Nothing touches the customer's firewall: the envoy's outbound TCP to the Vigo server is the only path used.

**Exit codes:**

| Code | Meaning |
|------|---------|
| 0 | Remote shell exited normally (`exit`, Ctrl-D). |
| non-zero | Setup failed (envoy unreachable, web user has no pubkey, pubkey not found in any regular user's authorized_keys on the envoy, timeout, auth failure) or the tunnel dropped mid-session. |

**Requirements:**

- Admin role on the CLI's token (same gate the browser flow uses).
- The CLI can auto-detect a pubkey via the chain above, OR you pass `--ssh-key-file`.
- That pubkey is in `~/.ssh/authorized_keys` of some regular (non-root) user on the target envoy.
- The envoy must have `sshd` listening on `127.0.0.1:22`; Scrier does not expose SSH on the envoy's external interface.
- stdin and stdout must both be TTYs — piped invocations are refused with a clear error.

**Not yet implemented:**

- Session recording (`scrier.recording_enabled` is a schema placeholder; no writes yet for browser or CLI sessions).
- `scp` / file transfer.
- Windows — the CLI command is built under `//go:build unix`.

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
