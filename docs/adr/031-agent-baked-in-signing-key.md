---
title: "ADR-031: Agent baked-in signing key for self-update verification"
---

# ADR-031: Agent baked-in signing key for self-update verification

**Status:** Approved — implementation in this commit.
**Date:** 2026-05-24. Direction ratified by Dan in the same session.

## Context

CLAUDE.md promises: *"The Rust agent self-updates only from ed25519-signed binaries (SHA-256 + signature verified, atomic replace, `execv`)."*

The implementation as of 0.66.93 violated that guarantee along two paths in `agent/src/update/mod.rs::apply` (lines 336–350 at the time of this writing):

1. **Signature omitted by server** — when the version response carried an empty `signature` field, the agent logged `debug!` and skipped ED25519 verification entirely, installing whatever matched the advertised SHA-256.
2. **Signing-key fetch failed** — when `fetch_signing_key()` (calling `GET /api/v1/agent/signing-key`) returned an error, the agent logged `warn!` and skipped verification.

Both paths fail-open. A compromised server (or a MITM with TLS-level access) could omit signatures and the agent would install whatever it was served.

The bug runs deeper than the two skip paths, though. Even with fail-close, the previous design fetched the signing **public key from the same server that delivered the binary**. A fully-compromised server could return its own pubkey alongside its own signature, "verification succeeds," and the agent installs the malicious binary. The verification was circular — the server was the trust root for the very key meant to authenticate the server's output.

The prior pending entry offered two paths:
- **(a) Fail-closed only** — change the two skips to bails; keep fetching the pubkey from the server.
- **(b) Baked-in fallback pubkey** — ship a pubkey in the agent binary at build time; verify against that, regardless of what the server says.

Option (a) is a half-step: it closes the omission attack but leaves the substitution attack open (compromised server returns attacker-chosen pubkey AND attacker-chosen signature). Option (b) is the proper fix.

## Decision

### Shape — baked-in pubkey, fail-closed verification, dev-mode sentinel

The agent's update-verification pubkey is **injected at build time** from the same `SIGN_KEY` that signs distributed binaries, so the build pipeline cannot produce an agent whose baked-in pubkey disagrees with the `.sig` files it will receive — they derive from one key.

### Build-time injection

`agent/build.rs` reads `VIGO_AGENT_SIGNING_PUBKEY` and emits `cargo:rustc-env=VIGO_AGENT_SIGNING_PUBKEY=<hex>`. `agent/src/update/mod.rs` declares `const SIGNING_PUBKEY_HEX: &str = env!("VIGO_AGENT_SIGNING_PUBKEY")`. The constant lives in the binary; an attacker cannot change it without rebuilding the agent.

`Makefile` derives the env var from `SIGN_KEY` via a new sign-tool mode:

```makefile
VIGO_AGENT_SIGNING_PUBKEY := $(shell test -f $(SIGN_KEY) && \
    go run ./server/hardening/cmd/sign -key $(SIGN_KEY) -pubkey-only 2>/dev/null)
```

`build-agent`, `dist-agent`, `dist-agent-windows`, and `dist-agent-macos` all export it into cargo. The same `SIGN_KEY` is then used by `dist-sign-agent` to produce the `.sig` files — one private key, two consumers, no possibility of drift.

### Fail-closed verification

`verify_signature_or_bail(digest, signature_hex, pubkey_hex)` is the single verification entry point. It refuses to proceed if:

1. `signature_hex.is_empty()` — server didn't include a signature.
2. `pubkey_hex == SIGNING_PUBKEY_DEV_SENTINEL` — agent was built without a real signing key (see dev-mode below).
3. The signature doesn't verify against the pubkey.

Each path returns an operator-actionable error. `apply()` calls `verify_signature_or_bail(&digest, &signature, SIGNING_PUBKEY_HEX)?` after the SHA-256 check; no other code path performs (or skips) signature verification.

`fetch_signing_key()` is **removed from the agent** — the agent no longer trusts whatever pubkey the server returns. The server endpoint `/api/v1/agent/signing-key` is preserved for bootstrap scripts and operator debugging, with its doc-comment updated to note the agent no longer consumes it.

### Dev-mode sentinel — 32 zero bytes

Plain `cargo build` / `cargo test` runs without `VIGO_AGENT_SIGNING_PUBKEY` set. The natural alternative would be a cargo feature flag or a hard build-time failure — both rejected:

- **Cargo feature flag** couples runtime behavior to a compile-time toggle the build system has to remember. Easy to mis-target a release build with the dev feature on.
- **Hard build failure** breaks every `cargo check` / `cargo test` workflow for anyone who doesn't have a `SIGN_KEY` set up. Devs touching unrelated agent code suddenly need to configure signing.

Instead, `build.rs` falls back to a well-known sentinel — the 32-zero-byte pubkey, hex-encoded as `00…00`. Build always succeeds. At runtime, `verify_signature_or_bail` detects the sentinel and bails with:

> `agent built without signing key (dev sentinel pubkey baked in) — rebuild via 'make build-agent SIGN_KEY=<path>' before enabling self-update`

The sentinel is fail-safe: an attacker cannot trigger the sentinel path at runtime because the pubkey is a compile-time constant. A dev binary that somehow reached production would refuse every update with a clear error.

Pure-zero chosen over a named magic string because zeros are the conventional "uninitialized" pattern in crypto-land; reviewers reading the source recognize it on sight.

### `SIGN_KEY` operational shape

The signing private key lives in a file on Dan's machine, path supplied via `SIGN_KEY=…` to `make`. Three alternatives were considered:

- **Embed in tessera** (extend the existing license-signing tool to hold an agent-signing key alongside) — adds cross-repo coupling. Rejected: agent-signing and license-signing are different threat models, different rotation cadences, and tessera lives in a separate repo with its own release cycle.
- **A standalone `vigo-sign` tool** — the underlying sign tool already exists at `server/hardening/cmd/sign` and does generate/sign/(now) extract-pubkey. The location is a vestige of when it was conceived as part of server hardening; renaming/relocating is bikeshed. Keeping the path for now.
- **Per-build ephemeral key** — would mean every build generates a new key, which would break update continuity (each new agent build wouldn't trust binaries signed with the previous build's key). Non-starter.

Dan-side: backup the key file like any other long-lived secret; loss = full fleet re-deploy with a new key (see rotation below).

### Rotation — deferred to post-v1

V1 ships a single baked-in pubkey. Key rotation requires:

1. Generate new keypair, replace `SIGN_KEY` on the build machine.
2. Rebuild the agent (new `VIGO_AGENT_SIGNING_PUBKEY` baked in).
3. Sign new dist binaries with the new key.
4. Roll out the new agent to every envoy via the existing `.sig`-signed-with-old-key path.
5. After every envoy is on the new build, future updates verify against the new key.

This is **fleet-wide redeploy on every rotation** — acceptable at current zero-user scale, untenable at >1000 envoys. The post-v1 lever is a **multi-key transition pair**: bake in both *current* and *previous* pubkeys; an agent accepts a binary signed with either; once rotation completes, the next build drops the old pubkey. This requires a 3-build dance (current, transition with both, next) but eliminates the redeploy. Tracked as a follow-on pending entry; not built in this slice.

## Consequences

- **CLAUDE.md guarantee is now actually enforced** — the agent never installs a binary that doesn't carry a signature verifying against the baked-in pubkey.
- **Server compromise no longer enables fleet takeover** — an attacker who controls the vigosrv binary still cannot replace agents in the fleet because they don't hold the `SIGN_KEY` private key.
- **Operators must run `make build-agent SIGN_KEY=<path>`** to produce a deployable agent. The Makefile's `build-agent` target documents this; the runtime error message points at it.
- **Dev workflows are unchanged** — `cargo build` / `cargo test` still work without configuring signing; the resulting binary just refuses to self-update.
- **`/api/v1/agent/signing-key` endpoint is now vestigial** for the agent itself. Bootstrap scripts and operator debugging still consume it; keeping it costs nothing server-side.
- **Pre-shipped agents in the fleet (running 0.66.93 and earlier)** will update to the new build via the OLD (skip-on-error) path. Once on the new build, they enforce the new contract.

## Out of scope

- **Multi-key rotation transition** — see follow-on pending entry.
- **Hardware-backed signing** (HSM, YubiKey, etc.) — the file-on-disk shape is enough at current scale; HSM integration is post-v1.
- **Per-fleet signing keys** — every fleet today uses one `SIGN_KEY`. Multi-fleet partitioning (e.g., per-customer keys for an MSP scenario) is post-v1 and intersects the MSP/Compliance pending work.
