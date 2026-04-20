
# vigo vigosync CLI Reference

`vigo vigosync` is the user-facing key management and decrypt surface for encrypted [vigosync](../../concepts/vigosync.md) directories. It runs on envoys (inside the `vigo` agent binary), operates on files under the invoking user's home, and does not require root. The daemon is never contacted — every verb is a pure local file operation.

## Invocation

```
vigo vigosync <verb> [args]
```

The agent daemon does not need to be running. These verbs manipulate files under `~/.vigosync/` (keys + config) and `~/vigosync/` (the user's sync directory) under the invoking user's uid.

## Permission model

Every verb runs under the invoking user's uid:

- `init` creates `~/.vigosync/identity.txt` with mode `0600` (owner-only).
- `decrypt`, `recipients`, and `reencrypt` read `~/.vigosync/identity.txt` and `~/.vigosync/recipients.txt` under the caller's uid, so root cannot silently decrypt another user's content by running the CLI.
- The `vigo` agent daemon (running as root) only ever **encrypts** — it reads `~<user>/.vigosync/recipients.txt` (a public file) and never touches `identity.txt`. This keeps root-on-sender from being a trivial key-extraction target.

## Commands

| Verb | Purpose |
|------|---------|
| `init` | Generate an age keypair; write `identity.txt` + `recipients.txt` |
| `decrypt <file> [-o <dest>]` | Decrypt a ciphertext file (stdout if no `-o`) |
| `recipients list` | Print configured recipient public keys |
| `recipients add <pubkey>` | Add an age recipient |
| `recipients remove <pubkey>` | Remove an age recipient |
| `reencrypt` | Re-encrypt every file in `~/vigosync/` with the current recipient set |

---

## init

Generate a fresh age keypair on this machine. Writes:

- `~/.vigosync/identity.txt` — age private key, mode `0600`. **Keep private.**
- `~/.vigosync/recipients.txt` — one or more age public keys, one per line.

```bash
vigo vigosync init
```

Refuses to overwrite an existing `identity.txt` — move or delete it first if you want a fresh key (and run `vigo vigosync reencrypt` afterwards so existing files are readable with the new key).

The generated public key is printed to stdout. Add it to `recipients.txt` on any other machine where this identity should be able to decrypt, or run `vigo vigosync init` on that other machine to generate its own identity and exchange pubkeys.

Run `init` on every machine where you want to decrypt. A machine that holds only ciphertext (a "dark receiver") never runs `init`.

---

## decrypt

Decrypt a ciphertext file using `~/.vigosync/identity.txt`. Streams plaintext to stdout by default; `-o <dest>` writes to a file instead.

```bash
vigo vigosync decrypt ~/vigosync/notes.md
vigo vigosync decrypt ~/vigosync/trip.jpg -o /tmp/trip.jpg
```

Errors if the invoking user's `identity.txt` doesn't match any recipient embedded in the ciphertext — re-run on a machine that holds a matching identity, or grant this identity access via `recipients add` + `reencrypt` on a machine that already can decrypt.

---

## recipients list

Print the active recipients. Blank lines and `#` comments are skipped.

```bash
vigo vigosync recipients list
```

---

## recipients add

Append a recipient to `~/.vigosync/recipients.txt`.

```bash
vigo vigosync recipients add age1qz3...x7y
```

Validates the pubkey before writing (invalid age recipients are rejected). Idempotent — adding a pubkey that's already present is a no-op.

**Follow up with `vigo vigosync reencrypt`** — existing files were encrypted without the new recipient and won't be readable by their identity until they're re-encrypted.

---

## recipients remove

Remove a recipient from `~/.vigosync/recipients.txt`.

```bash
vigo vigosync recipients remove age1qz3...x7y
```

Errors if the pubkey isn't in the file. Does NOT revoke access to already-encrypted files — removed recipients can still decrypt any ciphertext that was created when they were still in the list. Run `vigo vigosync reencrypt` after removal to cut off access to future content.

---

## reencrypt

Walk `~/vigosync/` and re-encrypt every file with the current recipient set. Files that are already age-encrypted are decrypted (using the local identity) and re-encrypted to the fresh recipient list. Plaintext files (if any) are encrypted once. Each file is processed atomically via a temp-file + rename so a crash mid-re-encrypt leaves the original intact.

```bash
vigo vigosync reencrypt
```

Requires both `identity.txt` and `recipients.txt`. Reports the number of files re-encrypted and the number skipped (e.g. files encrypted with a different identity the caller can't read).

Use after:

- Adding a recipient (`recipients add`) — grant access to the new device.
- Removing a recipient (`recipients remove`) — cut off access for future content.
- Rotating the local identity (`init` after moving the old `identity.txt` aside).

Vigosync's existing last-write-wins state machinery picks up the re-encrypted files on the next scan cycle and propagates the new ciphertext to peers.

---

## File locations

| Path | Mode | Purpose |
|------|------|---------|
| `~/.vigosync/config.yaml` | `0644` typical | Per-user opt-in (`encrypt: true`, `extra_peers: [...]`) |
| `~/.vigosync/identity.txt` | `0600` | Age private key — keep private |
| `~/.vigosync/recipients.txt` | `0644` typical | Age public keys, one per line |
| `~/vigosync/` | user | The sync dir itself |

The agent's scan cycle only pays attention to `config.yaml` and `recipients.txt`. `identity.txt` is read exclusively by this CLI.

## Exit codes

| Code | Meaning |
|------|---------|
| `0` | Success |
| non-zero | Any failure (invalid pubkey, missing identity, decrypt failure, rename failure) |

## See also

- [Vigosync concept page](../../concepts/vigosync.md) — full architecture, threat model, and cross-subnet `extra_peers:`
- [`vigo repo`](vigorepo.md) — the sister CLI for vigorepo personal-DR mirror recovery

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
