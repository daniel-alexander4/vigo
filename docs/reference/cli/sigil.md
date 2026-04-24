
# vigocli sigil

Read-only admin view of the [sigil](../../engineering/subsystems/sigil.md) mesh across the fleet — per-user always-encrypted directory sync on the [swarm](../../engineering/subsystems/swarm.md) substrate. Every envoy reports its per-user sigil state (ciphertext files, recipients digest, session unlock status) through the `sigil` trait; this command renders the fleet-wide join.

Sigil enrollment is per-user on the envoy via `vigo sigil enroll`. No admin write path — `vigocli sigil` never decrypts content, never touches keys, and never forces unlock. It only shows what each envoy reports.

> Looking for the per-user encryption CLI (init / enroll / unlock / encrypt / decrypt / lock)? See [`vigo sigil`](agent-sigil.md) — that command lives inside the `vigo` agent binary and runs on envoys.

## Subcommands

| Subcommand | Description |
|------------|-------------|
| `status` | Fleet-wide totals (users, envoys, ciphertext files, recipient drifts, unlocked sessions) |
| `list` | One row per user across the fleet |
| `show` | Detailed per-envoy presence for one user |

All subcommands support `--json` for machine-readable output.

## status

```bash
vigocli sigil status
```

```
Sigil fleet — generated 2026-04-23 18:14:51

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
vigocli sigil list
```

```
USER   ENVOYS  FILES  SIZE     UNLOCKED  STATE
dan    3       12     482 KB   1         in sync
ops    2       5      18 KB    0         recipient drift
```

| Column | Description |
|--------|-------------|
| USER | Unix username reported by the envoys |
| ENVOYS | Number of envoys where this user is sigil-enrolled |
| FILES | Total ciphertext file count across all envoys |
| SIZE | Sum of ciphertext file sizes |
| UNLOCKED | Count of envoys currently running a `vigo sigil unlock` session |
| STATE | `in sync` (green), `recipient drift` (red), or `N divergence(s)` (red) |

## show

```bash
vigocli sigil show <user>
```

```bash
vigocli sigil show dan
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

`SESSION` is `unlocked` (green) when the envoy is running a `vigo sigil unlock` session for this user. Admin visibility into session state is observational only — it reports whether the session helper is present, never the unlocked key.

Per-envoy states follow the same meanings as [vigocli echo show](echo.md#show).

## Related

- [Sigil Subsystem](../../engineering/subsystems/sigil.md) — How the encryption-first per-user sync works
- [`vigo sigil`](agent-sigil.md) — Per-user CLI on the envoy (init, enroll, unlock, encrypt, decrypt, lock)
- [Swarm Substrate](../../engineering/subsystems/swarm.md) — The envoy P2P transport sigil rides on
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
