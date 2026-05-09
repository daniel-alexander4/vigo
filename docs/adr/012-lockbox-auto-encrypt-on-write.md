---
title: "ADR-012: Lockbox auto-encrypt-on-write"
---

# ADR-012: Lockbox auto-encrypt-on-write

**Status:** Complete (shipped 0.34.7).
**Date:** 2026-04-29

## Context

Lockbox is encryption-first by design. The agent's inotify/kqueue watcher (`agent/src/swarm/lockbox/watcher.rs`) reads each new file in `~<user>/lockbox/` and verifies it starts with the age magic header `age-encryption.org/v1\n` before enqueuing a push. Files without the magic are bailed at `load_and_verify` (`watcher.rs:420`) and a single WARN — `lockbox watcher: skipping non-pushable event` — is logged before the watcher returns. Encryption itself happens only via the explicit `vigo swarm lockbox encrypt <file>` CLI verb, which requires the per-user session helper to be unlocked.

This created a UX trap. The mental model most users carry for a sync folder is "drop file → it syncs." Lockbox's actual model was "drop ciphertext → it syncs; plaintext drops are silently skipped." The gap surfaced 2026-04-28 when a 2.6 KiB tar.gz dropped into `~/lockbox/` on one envoy never reached its peer; the only signal was a single WARN line buried in the agent journal that named neither what was wrong nor what to do about it.

The core tension is between two lockbox promises: (1) "drop and forget" UX, and (2) ciphertext-at-rest invariant. The current model preserves (2) by punting on (1). The opposite extreme (always-encrypt-on-write, queue-through-locked-sessions) blurs (2) by leaving plaintext on disk during the lock window with system-tracked promises about it. The decision below threads a path that ships (1) without expanding the at-rest exposure beyond what the user explicitly creates with their own hands.

## Decision

Plaintext files dropped into `~<user>/lockbox/` are auto-encrypted in place by the watcher, then pushed via the existing fan-out path. Five sub-decisions ratify the behavior.

### 1. Locked-at-write policy — sweep-on-unlock

When the watcher detects plaintext while the per-user session is unlocked: encrypt in place via tempfile + atomic rename, then push via the existing pipeline.

When the session is locked at the moment of write: emit a single actionable WARN with the filename and the recovery action (`vigo swarm lockbox unlock`), leave the plaintext in place, do not maintain a persistent queue. On the next successful `unlock`, walk `~<user>/lockbox/` once and encrypt every file that fails the age-magic check. The directory itself is the queue — already-ciphertext files no-op the magic check on the sweep pass and are skipped naturally.

### 2. Encryption failure policy — three-class error

The encrypt primitive classifies any error into one of three classes:

| Class | Examples | Behavior |
|---|---|---|
| Terminal-actionable | recipients.txt empty, disk full, permission denied, source vanished mid-read | Single specific WARN; leave plaintext; no retry; no rename. The user fixes the cause; the next watcher event retries naturally. |
| Terminal-unrecoverable | age library reports input cannot be encoded; identity-key corrupt | Single ERROR; rename to `<filename>.<unix_ts>.failed`; no retry. |
| Transient / unknown | generic I/O error, signal-interrupted encrypt, anything not matching the above | Inline retry 3× with backoff (100ms / 500ms / 2s). On exhaustion: ERROR + rename to `<filename>.<unix_ts>.failed`. |

The watcher filters `*.failed` and the staging-tempfile prefix at the top of `resolve_event` so renamed files don't loop back into the pipeline. Recovery: user renames `<filename>.<ts>.failed` back to `<filename>` and the watcher fires from clean state. No `vigo swarm lockbox recover` verb — directory remains the queue.

### 3. Size threshold — match the push cap

Auto-encrypt is gated by the same 1 GiB cap that bounds `POST /lockbox/push/...`. Files over the cap get a clear WARN (`<file>` size=`<N>` exceeds `<cap>` push cap; not auto-encrypting) and remain plaintext. A single boundary keeps the model simple: if lockbox can transmit it, lockbox will encrypt it; otherwise it does not waste CPU/IO on encryption that would fail at the transport step anyway. Two-tier ("can encrypt but can't push") would create a worse UX than refusing both at the same boundary.

### 4. Default-on, no config flag

Auto-encrypt-on-write becomes the new default behavior on agent upgrade. No `swarm.lockbox.auto_encrypt` flag in server.yaml or `~/.lockbox/config.yaml`. The zero-users doctrine (CLAUDE.md) endorses behavior changes without backwards-compat shims; a flag-gated rollout would re-create the original UX trap from a different angle (operator forgets to flip the flag, user sees broken behavior).

### 5. Logging and status output

Every watcher branch emits a single class-aware line that names what happened and what to do.

| Path | Level | Line |
|---|---|---|
| Plaintext + unlocked + ≤ cap → encrypt | INFO | `lockbox watcher: auto-encrypting <user>/<filename> (size=<N>)` |
| Encrypt success → push | INFO | (existing `PushJob enqueued`, unchanged) |
| Plaintext + locked | WARN, one-shot per (user, filename, mtime) | `lockbox watcher: <user>/<filename> is plaintext; session locked — will encrypt on next 'vigo swarm lockbox unlock'` |
| Plaintext + > cap | WARN, one-shot | `lockbox watcher: <user>/<filename> size=<N> exceeds <cap> push cap; not auto-encrypting` |
| Encrypt fail — terminal-actionable | WARN, one-shot | `lockbox watcher: cannot encrypt <user>/<filename>: <specific cause>; left plaintext` |
| Encrypt fail — terminal-unrecoverable | ERROR | `lockbox watcher: encrypt of <user>/<filename> failed permanently: <cause>; renamed to <filename>.<ts>.failed` |
| Encrypt fail — transient | WARN per attempt + ERROR on final | `lockbox watcher: encrypt attempt <n>/3 failed for <user>/<filename>: <err>` / final ERROR with rename note |
| Unlock sweep | INFO per file + summary | `lockbox sweep: encrypted <user>/<filename>` per file; `lockbox sweep: <N> file(s) encrypted, <K> failed for <user>` summary |

One-shot dedupe keys on `(user, filename, mtime)` so re-fired inotify events for the same plaintext don't flood the journal. Re-WARN fires when the user writes the file again (mtime changes).

`vigo swarm lockbox status` breaks down the existing `files: <N>` line into actionable subcounts:

```
  files:            5
    ciphertext:     3
    plaintext:      1   (run 'vigo swarm lockbox unlock' to encrypt and sync)
    failed:         1   (vigo-lic-backup.tar.gz.20260429-194712.failed)
```

Counts derive from a single `read_dir` scan at status-call time — no persistent state. Failed basenames are listed inline so the user knows which files need attention.

## Alternatives Considered

**Status quo plus a clearer WARN.** Cheapest path: rewrite the `skipping non-pushable event` line to name what to do and update docs. Rejected because it preserves the "drop and forget" gap — the user's mental model still does not match reality, and the WARN is only useful to users who already know to grep the journal.

**Auto-encrypt with a persistent queue.** A `pending_encrypt.json` state file tracking files written while locked, walked on unlock. Rejected because the directory itself is the queue — a `read_dir` at unlock time catches the same set with no state to maintain, no GC, no risk of the queue and the filesystem disagreeing.

**Quarantine plaintext out of `~/lockbox/`.** Move user-written plaintext into `~/.lockbox/quarantine/` until encryptable. Rejected because it makes the user's file appear to vanish from where they put it — the "drop and forget" promise dies as soon as the user goes looking for the file they just wrote.

**Per-user `auto_encrypt: bool` flag in `~/.lockbox/config.yaml`.** Rejected as scope creep; a flag without a concrete scenario asking for it adds doc surface, support burden, and a consistency hole (documented behavior diverges from observed behavior depending on a config the user may have set once and forgotten).

## Consequences

**Files to modify (agent only — no server change):**

- `agent/src/swarm/lockbox/watcher.rs` — class-aware branches, `.failed` and staging-prefix filter at the top of `resolve_event`, one-shot dedupe state, encrypt trigger.
- `agent/src/swarm/lockbox/` — new in-place encrypt primitive: tempfile in `~/.lockbox/staging/<uuid>` + atomic rename into `~<user>/lockbox/<filename>`. Three-class error classification. 3× retry with backoff for the transient class.
- `agent/src/swarm/lockbox/session.rs` (or wherever unlock completion is signalled) — fire sweep on successful unlock; encrypt every plaintext file currently in `~<user>/lockbox/`.
- `agent/src/swarm/lockbox/cli.rs` — `status` subcounts via `read_dir` at call time.
- Rust unit tests for failure-class classification and one-shot dedupe.

**Documentation:**

- `docs/concepts/lockbox.md` — describe the auto-encrypt-on-write model, the unlock-sweep, the failure recovery via rename.

**Operational:**

- Agent rebuild + cross-compile + redeploy required. Z-level version bump (enhancement to existing feature).
- No server change — lockbox transport, push handler, and recipient exchange are unchanged.

**Behavioral consequences:**

- The implicit "every file in `~<user>/lockbox/` is ciphertext at rest" invariant changes to "every file in `~<user>/lockbox/` is ciphertext at rest under correct operation; plaintext drops while locked persist until next unlock or user action." This relaxation is documented; the externally visible value prop ("ciphertext over the wire, ciphertext at rest under correct operation") is preserved.
- Plaintext never crosses the wire under any path. The watcher's age-magic gate at `load_and_verify` remains the transmission boundary — auto-encrypt produces ciphertext locally before any push fires.
