---

# vigocli tungol

Operations for the **tungol** (federation) and **steorra** (single vigosrv) primitives of the peer-equal federation redesign of spanner. Currently hosts ops-puddle lifecycle (ADR-025); the broader tungol / steorra command surface lands as the redesign progresses.

## Subcommands

| Subcommand | Description |
|------------|-------------|
| `ops-puddle init <friendly-name>` | Found the ops-puddle on this vigosrv |
| `ops-puddle status` | Show the ops-puddle's friendly name, pubkey, and member set |

## Persistent flags

| Flag | Default | Description |
|------|---------|-------------|
| `--key-file` | (auto-detected inside `--secrets-dir`) | Path to the master encryption key. |
| `--secrets-dir` | `/srv/vigo/secrets` | Directory holding the secrets backend's master key. |
| `--puddle-dir` | `/srv/vigo/puddle` | Directory holding this vigosrv's service-account puddle (ADR-025). |
| `--ops-puddle-dir` | `/srv/vigo/ops-puddle` | Directory holding the per-host ops-puddle wrap (ADR-025). |

## ops-puddle init &lt;friendly-name&gt;

Generates the ops-puddle's Ed25519 keypair, wraps it under this vigosrv's master-key-derived wrap key, and persists it under `--ops-puddle-dir`. This vigosrv's service-account puddle becomes member zero. Other vigosrvs join via the pair flow (Slice 2b, pending).

The friendly name participates in gitback URLs (`gitback://<name>/stockpile`) — pick something stable; renaming requires re-founding the ops-puddle. Name validation rules mirror the agent's per-user puddle rules: 1–64 ASCII alphanumeric + `-` `_` `.`, no leading dot, and no 64-hex-character `project_id` lookalike.

Requires an unlocked secrets vault (`vigocli secrets unlock`).

```
$ vigocli tungol ops-puddle init alexander4-ops
Ops-puddle founded
  Friendly name: alexander4-ops
  Pubkey:        ed25519:274438afd5550b90c720c405acf07d3194fcd75c4ba2038adb25f6e688082d48
  Dir:           /srv/vigo/ops-puddle
  Members:       1 (this vigosrv: ed25519:559812a17c032f71e0b2f842980951b95cde184b43e5522d06d41eb44c2f6b52)
```

## ops-puddle status

Reads the public files in `--ops-puddle-dir` (friendly name, pubkey, members list) without unwrapping the signing key. Safe to run on a locked vault.

```
$ vigocli tungol ops-puddle status
Ops-puddle status
  Friendly name: alexander4-ops
  Pubkey:        ed25519:274438afd5550b90c720c405acf07d3194fcd75c4ba2038adb25f6e688082d48
  Members (1):
    - ed25519:559812a17c032f71e0b2f842980951b95cde184b43e5522d06d41eb44c2f6b52 (joined 2026-05-17 14:08:00 UTC)
```
