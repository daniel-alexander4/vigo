# The security model

**What stops a bad actor — on the wire, at the server, and on the node?** Vigo's answer is a set of constraints that hold by construction, not by configuration you might forget.

## The non-negotiables

These are invariants, not options:

- **All gRPC is mTLS.** No plaintext transport, ever.
- **Every agent request is ed25519-signed** against the pubkey stored at enrollment. A forged or replayed request from an unknown key is rejected.
- **One-time enrollment tokens are hashed at rest**, and a revoked client gets an error on check-in and cannot re-enroll with the same UUID.
- **Secrets never appear in plaintext** in config, env vars, logs, the database, gRPC payloads, or run results — see [secrets](secrets.md).
- **All configcrate executors are idempotent** — check before act — so a re-applied resource can't do damage twice.

## Authentication & authorization

- **Pluggable auth** (`basic` / `oidc` / `isowebauth`), always required. Sessions in SQLite; API tokens carry a `vgot_` prefix and are SHA-256-hashed at rest (a 256-bit-random token doesn't need bcrypt, and bcrypt-per-request would be a DoS surface).
- **RBAC**: `admin` / `viewer` / `compliance`. The compliance role is denied `audit.read` — the raw hash-chained audit log is admin/viewer-only.
- On the server host, `vigocli` authenticates with a boot-minted, root-only local-admin token; source IP grants nothing.

## Tamper-evidence & hardening

- **The audit log is a SHA-256 hash chain** over every privileged operation — you can't quietly edit history without breaking the chain. It also fans out to SIEMs through the integrations dispatcher.
- **Startup hardening fail-closes**: a detached ed25519 binary-signature check gates startup *before* the server is built. Debugger / injection detection is warn-only (heuristics too coarse to fail-close) and records as audit events.
- **The agent self-updates only from ed25519-signed binaries** (SHA-256 + signature verified, atomic replace), and guardrails protect paths/services/packages and scan dangerous commands.

## The shape of it

Trust is **rooted in keys, not networks**: mTLS for the channel, ed25519 for every request and every binary, hashing for tokens and the audit chain. A compromised network position buys an attacker nothing they can't cryptographically prove.

## Where this shows up

- [Secrets](secrets.md) · [Scrier](scrier.md) · [Puddle identity](puddle-identity.md).
- The `/security` UI (CVE, attack surface, signed binaries, hardening warnings).

---
*Confidential — Alexander4, LLC. Not for redistribution.*
