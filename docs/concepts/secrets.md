# Secrets

**How does a secret get into a config without ever sitting in the config?**

You reference it. Anywhere a value can appear in a `.vgo` file, you write `secret:<path>` instead of the literal — `dsn: "secret:vigo/db/dsn"`. A ref can also sit **embedded inside a larger value**, including several in one compound string — `env: "PW=secret:vigo/db/pw,DB=app"` resolves the `secret:vigo/db/pw` token in place and leaves the rest literal (a ref runs up to the first character that isn't part of a path). Whole-value or embedded, every ref is validated at load and force-pushed on rotation. Vigo resolves the real value at the last possible moment, on the agent, at apply time, and never before.

## The path a secret does *not* take

The thing to understand is everywhere the plaintext **isn't**:

1. **At config load**, the server validates that each `secret:` ref exists in the backend — but substitutes an opaque marker (`__VIGO_SECRET__<path>__END__`) into the resolved config. The real value is never stored on the envoy's config object.
2. **In the PolicyBundle** sent to the agent, only the marker travels. Plaintext never crosses the wire.
3. **At apply time**, the agent calls `VigoAgent.ResolveSecrets` for the values its bundle actually needs. An envoy can only resolve refs whose marker appears in *its own* bundle — an un-entitled fetch fails `PermissionDenied` and emits an audit event.
4. Resolved values live in the agent's **in-memory cache** for the process lifetime only. A `secrets_epoch` field bumps on every reload + rotation, invalidating the cache.

So a secret never appears in plaintext in config, env vars, logs, the database, gRPC payloads, or run results. That's a hard, non-negotiable constraint.

## Backends

The backend is pluggable, resolved at config load (fails fast if unreachable):

- **`local`** (default) — paths map to AES-256-GCM-wrapped files under `<secrets-dir>`, encrypted under a master key. The server's read path is autonomous across restarts.
- **`isopass`** (prod) — an HTTP secrets provider.

The operator surface (`vigocli secrets {init,unlock,rotate,set,reveal,…}`) is unlock-gated; the server's autonomous read path is not.

## Where this shows up

- [Manage secrets](../reference/vigocli/secrets.md) — the CLI surface.
- [Harden secret memory](../howto/operate/harden-envoy-secret-memory.md) — locking down the agent's in-RAM cache.
- [The security model](security-model.md) — where secrets sit in the broader posture.

---
*Confidential — Alexander4, LLC. Not for redistribution.*
