
# vigocli lockbox

Read-only admin view of the [lockbox](../../engineering/subsystems/lockbox.md) mesh across the fleet — per-user always-encrypted directory sync on the [swarm](../../engineering/subsystems/swarm.md) substrate. Every envoy reports its per-user lockbox state (ciphertext files, recipients digest, session unlock status) through the `lockbox` trait; this command renders the fleet-wide join.

Lockbox enrollment is per-user on the envoy via `vigo swarm lockbox enroll`. The admin surface is mostly read-only, plus one write verb — `evict` — for pulling a host out of a user's circle when the host itself can't self-leave (gone laptop, decommission).

> Looking for the per-user encryption CLI (init / enroll / unlock / encrypt / decrypt / lock / leave)? See [`vigo swarm lockbox`](agent-swarm-lockbox.md) — that command lives inside the `vigo` agent binary and runs on envoys.

## Subcommands

| Subcommand | Description |
|------------|-------------|
| `status` | Fleet-wide totals (users, envoys, ciphertext files, recipient drifts, unlocked sessions) |
| `list` | One row per user across the fleet |
| `show` | Detailed per-envoy presence for one user |
| `members [user]` | List enrolled envoys per user (read-only) |
| `evict --host <h> {--user <u> \| --all-users}` | Remove a host from a user's lockbox circle fleet-wide |

Read subcommands support `--json` for machine-readable output.

## evict

Dispatch a task to every envoy currently enrolled for the target user(s). Each agent runs `vigo swarm lockbox _apply-evict --user <u> --hostname <h>` internally: it looks up the host's age pubkey in its own peer table, drops the pubkey from `peers.json`, regenerates `recipients.txt`, drops `reencrypt.pending` so the next unlock rewraps existing ciphertext, and persists the LEAVE record.

Admin authority is the mTLS + admin-token channel (not a P2P signature). Offline envoys pick up the eviction on their next check-in via task-dispatch retry.

```bash
# Evict one host from one user's lockbox circle:
vigocli lockbox evict --host girlslaptop --user dan --yes

# Evict a host from every user's lockbox circle it participates in:
vigocli lockbox evict --host girlslaptop --all-users --yes
```

Scope options:
- `--host <h>` (required) — hostname to evict.
- `--user <u>` — scope to one user's circle.
- `--all-users` — every user the target host is enrolled for.
- Exactly one of `--user` or `--all-users` is required.
- `--yes` — skip the confirmation prompt.

See [`vigo swarm lockbox leave`](agent-swarm-lockbox.md) for the user-triggered self-unenroll path (used when the departing envoy is still online and the user runs the command themselves).

## status

```bash
vigocli lockbox status
```

```
Lockbox fleet — generated 2026-04-23 18:14:51

Users               2
Envoys reporting    5
Ciphertext files    17
Tombstones          1
Divergences         0
Recipient drifts    0
Unlocked sessions   1
Participants        7
```

`Divergences` and `Recipient drifts` are shown in yellow when non-zero. Recipient drift means the set of peer pubkeys the envoys are encrypting to has diverged — usually because one envoy hasn't seen a recent multicast announcement from a peer that others have.

## list

```bash
vigocli lockbox list
```

```
USER   ENVOYS  FILES  SIZE     UNLOCKED  STATE
dan    3       12     482 KB   1         in sync
ops    2       5      18 KB    0         recipient drift
```

| Column | Description |
|--------|-------------|
| USER | Unix username reported by the envoys |
| ENVOYS | Number of envoys where this user is lockbox-enrolled |
| FILES | Total ciphertext file count across all envoys |
| SIZE | Sum of ciphertext file sizes |
| UNLOCKED | Count of envoys currently running a `vigo swarm lockbox unlock` session |
| STATE | `in sync` (green), `recipient drift` (red), or `N divergence(s)` (red) |

## show

```bash
vigocli lockbox show <user>
```

```bash
vigocli lockbox show dan
```

```
User:             dan
Envoys:           3
Ciphertext files: 12
Total size:       482 KB
Unlocked on:      danlap

  HOST                   STATUS      FILES  TOMBSTONES  SESSION   LAST UPDATE
  danlap                 reporting   12     1           unlocked  2026-04-23 18:14
  girlslaptop            reporting   12     1           locked    2026-04-23 18:13
  homeserver             reporting   12     1           locked    2026-04-23 18:12
```

`SESSION` is `unlocked` (green) when the envoy is running a `vigo swarm lockbox unlock` session for this user. Admin visibility into session state is observational only — it reports whether the session helper is present, never the unlocked key.

Per-envoy states follow the same meanings as [vigocli longdrawer show](swarm-longdrawer.md#show).

## Related

- [Lockbox Subsystem](../../engineering/subsystems/lockbox.md) — How the encryption-first per-user sync works
- [`vigo swarm lockbox`](agent-swarm-lockbox.md) — Per-user CLI on the envoy (init, enroll, unlock, encrypt, decrypt, lock)
- [Swarm Substrate](../../engineering/subsystems/swarm.md) — The envoy P2P transport lockbox rides on
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
