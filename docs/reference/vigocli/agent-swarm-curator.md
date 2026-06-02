
# vigo swarm curator CLI Reference

`vigo swarm curator` is the envoy-side surface for [curator](../../howto/swarm/set-up-curator.md) — the P2P artifact registry (ADR-024). Founders publish artifacts; consumers pull them; everyone gets local inspection. The verb tree splits into read verbs (ungated), mutating verbs (gated by `curator: true` on the usercrate + an unlocked puddle), and local cleanup verbs.

`vigo swarm curator` is the **founder + consumer** side. The fleet-aggregator view and admin moderation (`block` / `unblock`) live on [`vigocli swarm curator`](swarm-curator.md). Per ADR-024 open question #1 resolved 2026-05-13, mutating verbs deliberately are NOT on the vigocli side — they need an unlocked per-user puddle to sign catalog entries, which doesn't fit vigocli's root-only / peer-auth posture.

## Invocation

```
vigo swarm curator <verb> [args]
```

The command lives inside the `vigo` agent binary. CLI verbs run as the calling user — root is not required for mutating verbs and would discard the user's puddle identity. The only verb that requires root is `pull` (it materializes into `/var/lib/vigo/artifacts/`, a root-owned tree). `gc` and `purge` benefit from root for the manifest-revoke half of their job.

**Usercrate authorization (mutating verbs).** Mutating verbs (`push` / `tag` / `untag` / `set-recipients` / `set-target` / `rm` / `prune` / `gc` / `purge`) refuse unless the invoking user has `curator: true` on the user resource in their usercrate. The agent's user executor writes the resolved flags to `/var/lib/vigo/usercrate-policy/<user>.json` on each reconcile pass; the CLI reads it as a precondition. Both layers — envoy-level (`swarm.curator.enabled` pattern list in `server.yaml`) and user-level (this flag) — must say yes.

`curator: true` is a **heavier grant than `gitback: true`** — "may publish to the fleet's registry, fleet-readable by default" vs "may found personal git repos." Removing the flag scrubs `~/.vigo-curator/` for that user on the next reconcile pass via `ScrubScope::Curator`. Already-published artifacts stay (you can't recall them by losing a flag).

## Artifact identity

Every artifact is identified by `artifact_id = sha256(founder_puddle_pubkey ‖ name)` rendered as 64-character lowercase hex. The hex form is self-certifying; the friendly form is `<founder-puddle-name>/<name>` via the ADR-022 fleet name map.

Verbs that take a `<ref>` argument accept (in order):

1. The full 64-char-lowercase-hex `artifact_id` (canonical, always wins)
2. An exact name match across **the caller's own local entries** (errors if two local entries share a name — pass the artifact_id)
3. A unique name prefix among the caller's local entries

Mutating verbs operate on the caller's *own* publications — mutating someone else's artifact is structurally impossible (the daemon verifies the entry signature anchors to the founder's pubkey baked into the artifact_id). Read verbs widen the resolution pool to include the cached fleet catalog so any artifact in the fleet can be inspected.

## Read verbs

These are ungated — any user can run them. Artifacts are fleet-readable; the `curator: true` flag only gates publishing.

### `vigo swarm curator status`

Envoy-wide curator posture: local publish state, helper-daemon socket presence, cached fleet catalog size, materialization cache size.

```
$ vigo swarm curator status
Curator on this envoy

  Local publish state:
    Users with publish state: 1
    Local catalog entries:    2
    Pending sentinels:        0

  Helper daemon:
    Socket present at /var/run/vigo/curator.sock (publish-ready)

  Cached fleet catalog (/var/lib/vigo/artifacts/catalog.json):
    Cached entries: 7

  Materialization cache (/var/lib/vigo/artifacts/cache.json):
    Materialized refs: 4
```

When the helper-daemon socket isn't present, publishing is disabled on this envoy — `swarm.curator.enabled` likely doesn't match this host. The "pending sentinels" count is a hint at how far behind the sentinel reactor is (60 s sweep; non-zero between a publish and its promotion is normal).

### `vigo swarm curator list`

One row per artifact this envoy knows about (local entries merged with the cached fleet catalog, deduped by artifact_id, higher `issued_at` wins).

```
$ vigo swarm curator list
NAME                     KIND          VERSIONS  TAGS   PLATFORMS            ORIGIN   ARTIFACT_ID
nginx-image              oci-archive   1         1      linux/amd64          cached   1d8a5e23fffa
vigo-agent               generic       12        3      darwin/arm64,linux/… local    e3b0c44298fc
```

The `KIND` column is the artifact's consume facade (`generic` / `oci-archive` / `apt-repo` / `dnf-repo` / `apk-repo`); the `ORIGIN` column distinguishes artifacts published by this envoy (`local`) from artifacts learned via the cached fleet catalog (`cached`).

### `vigo swarm curator inspect <ref>`

Full signed entry for one artifact: versions, tags, recipients, target, founder pubkey, rekey hint (when signing ≠ founder), dangling-tag footer.

```
$ vigo swarm curator inspect vigo-agent
Name:           vigo-agent
Artifact ID:    e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
Kind:           generic
Founder puddle: ed25519:8711b12bac759fea30363f0ed4ab95b49b90b1b51c1cd4c095cb788cd0a9aea8
Target:         *
Recipients:     *
Issued at:      2026-05-14 09:11:08 UTC

Versions:
  VERSION        TAGS           OS/ARCH        SIZE       SHA
  0.51.0         latest         linux/amd64    8.5 MiB    c1d4e3f2a8b9
                                linux/arm64    8.2 MiB    9f43a5e7c2d1
                                darwin/arm64   8.7 MiB    4a9e7b3c2f1d
```

### `vigo swarm curator versions <ref>`

Compact version + tag table without the per-platform sha breakdown — useful when scanning what's been published.

### `vigo swarm curator resolve <ref> <tag-or-version> <os> <arch>`

Show what `pull` would fetch. Hits the server's `/resolve` endpoint first; falls back to the cached catalog on a network error. Verifies the signed entry end-to-end before printing.

```
$ vigo swarm curator resolve vigo-agent latest linux amd64
Resolved e3b0c44298fc/vigo-agent @ 0.51.0 on linux/amd64
  blob_sha: c1d4e3f2a8b9...
  size:     8.5 MiB
  tag:      latest
```

### `vigo swarm curator pull <ref> <tag-or-version> <os> <arch>`

Resolve + fetch (from the local swarm cache, or relayed from the server) + verify the sha + materialize.

```
$ sudo vigo swarm curator pull vigo-agent latest linux amd64
Materialized → /var/lib/vigo/artifacts/e3b0c44298fc.../vigo-agent/0.51.0/linux-amd64
```

The destination is a root-owned tree (mode 0644 file) — `pull` refuses with a clear `sudo` pointer when invoked as a non-root user. Idempotent: a re-pull is a no-op when the target path already exists.

## Mutating verbs

These require `curator: true` on the usercrate + an unlocked puddle (`vigo swarm puddle unlock`). The CLI prepares everything, signs via the puddle session helper, and ships to `/var/run/vigo/curator.sock`. The daemon enforces signature/anchor verification, blob-size cap, and (for `PUSH`) sha-vs-bytes match before staging.

### `vigo swarm curator push`

```
vigo swarm curator push <name>
    --file <path>
    --version <v>
    --os <os>
    --arch <arch>
    [--tag <tag>]
    [--target <glob>]
    [--recipients <* | pubkey-list>]
    [--kind <kind>]
```

Publish a `{name, version, os, arch}` build. Founds the artifact if no local entry exists for `sha256(your_puddle_pubkey ‖ name)`; otherwise slots the platform into the existing entry (replacing any same-`{os,arch}` record under the named version, so re-pushing a fixed build for the same triplet works). Re-signs the entry under the current puddle key and streams the bytes through the helper daemon's `PUSH` verb. The daemon stages the blob into the swarm cache and records a manifest seed with the artifact's `target` — the existing filecast `Manager` distributes it to matching swarm-enabled envoys.

`--kind` declares which consume facade the artifact targets — `generic` (raw bytes, the default), `oci-archive` (a `docker pull`-able OCI image, served by the Docker Registry v2 shim), or `apt-repo` / `dnf-repo` / `apk-repo` (a built, signed package-repo tree). It is set at first publish and **immutable thereafter** — a re-push with a conflicting `--kind` is refused, because a different shape is a different artifact. Facades enforce it: the Docker shim serves only `oci-archive`, so pointing `docker pull` at a wrong-kind artifact returns a clear mismatch error instead of a malformed-archive failure.

```
$ vigo swarm curator push vigo-agent \
    --file dist/vigo-linux-amd64 \
    --version 0.51.0 \
    --os linux --arch amd64 \
    --tag latest \
    --target "*"
✓ Published vigo-agent@0.51.0 on linux/amd64 (8.5 MiB)
  artifact_id: e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
  blob_sha:    c1d4e3f2a8b9...
```

Flags:

| Flag | Meaning |
|---|---|
| `--file <path>` | Bytes to publish. Hashed (sha256) + sized client-side; the daemon re-hashes as it streams to confirm. |
| `--version <v>` | Version string for this build. Free-form (semver, calver, anything). |
| `--os <os>` / `--arch <arch>` | The `{os, arch}` slot inside the named version. |
| `--tag <tag>` | Optional: also set this tag to point at the version being pushed. |
| `--target <glob>` | Optional: set the artifact's `target` field on first publish (or override on subsequent push). Defaults to `*` (fleet-wide DR replica). |
| `--recipients <list>` | Optional: replace the artifact's `recipients`. Pass `*` or a comma-separated list of hex puddle pubkeys (with optional `ed25519:` prefix). Defaults to `["*"]`. Each pubkey is validated as 64 hex characters. |
| `--kind <kind>` | Optional: the artifact's consume facade — `generic` (default) / `oci-archive` / `apt-repo` / `dnf-repo` / `apk-repo`. Set at first publish, **immutable** on re-push. |

Per-blob size cap is `swarm.curator.max_bytes` (default 2 GiB); the daemon refuses over-cap pushes before reading the body. Disk space on the receiving side is also checked.

### `vigo swarm curator repo-publish`

```
vigo swarm curator repo-publish <name> --dir <pkgs> --kind <apt-repo|dnf-repo|apk-repo>
    --sign-key <id> [--suite <s>] [--component <c>] [--arch <a>]
    [--version <v>] [--tag <tag>]
```

Builds a signed package-repository tree from a directory of loose `.deb`/`.rpm`/`.apk` files and publishes it as a curator artifact — the one-command form of hand-building a repo tree and `push`ing it. See [Hosting a private package repository](../../howto/swarm/set-up-curator.md#hosting-a-private-package-repository).

The build shells out to the toolchain matching `--kind` — `dpkg-scanpackages` + `gpg` (`apt-repo`), `createrepo_c` + `gpg` (`dnf-repo`), `apk index` + `abuild-sign` (`apk-repo`) — which must be installed on the publishing host; a missing tool gives a clear "install …" error. The built tarball is then handed to the `push` path unchanged (puddle-signed, helper-daemon staged).

| Flag | Meaning |
|---|---|
| `--dir <pkgs>` | Directory of loose `.deb`/`.rpm`/`.apk` files (required). |
| `--kind` | `apt-repo`, `dnf-repo`, or `apk-repo` (required). |
| `--sign-key <id>` | A GPG key id for `apt-repo`/`dnf-repo`; an abuild RSA private-key path for `apk-repo` (required). |
| `--suite` / `--component` | apt suite / component. Default `stable` / `main`. |
| `--arch <a>` | apk repo architecture. Default `x86_64`. |
| `--version <v>` | Artifact version. Default: today's UTC date. |
| `--tag <tag>` | Artifact tag. Default `latest`. |

Requires `curator: true` on your usercrate and an unlocked puddle, same as `push`. Deliberately thin — one suite, one component, the arches present; the long tail (source packages, multiple components, incremental adds) stays on the hand-built recipe.

### `vigo swarm curator tag <ref> <tag>=<version>`

Set or move a tag. Validates that the version exists in the entry; lists the available versions on error.

```
$ vigo swarm curator tag vigo-agent stable=0.51.0
✓ Tagged e3b0c44298fc stable → 0.51.0
```

### `vigo swarm curator untag <ref> <tag>`

Remove a tag. Errors if the tag isn't set on the artifact.

```
$ vigo swarm curator untag vigo-agent canary
✓ Removed tag canary from e3b0c44298fc
```

### `vigo swarm curator set-recipients <ref> <list>`

Replace the artifact's `recipients`. Pass `*` for fleet-readable (the default) or a comma-separated list of hex puddle pubkeys.

```
$ vigo swarm curator set-recipients vigo-agent "ed25519:aaaa…,bbbb…"
✓ Set recipients on e3b0c44298fc to aaaa…, bbbb…
```

Recipients is **currently informational** at the resolve gate (slice 3b decision — artifacts are fleet-readable). The field stays in the catalog model for the post-v1 encrypted-blob feature; setting it now is forward-looking, not enforced.

### `vigo swarm curator set-target <ref> <glob>`

Replace the DR-replica target. The daemon refreshes the manifest seed for every blob **this envoy** seeded, so the filecast `Manager` redistributes to the new envoy set on the next pass.

```
$ vigo swarm curator set-target vigo-agent "us-west1-*"
✓ Set target on e3b0c44298fc to us-west1-*
  Manifest seeds refreshed; filecast Manager will redistribute on the next pass.
```

Peer-seeded blobs are NOT overwritten — a re-target only affects what this envoy is authoritative for. Other publishers' blobs ride their own re-target paths.

### `vigo swarm curator rm <ref> [--version <v>]`

Remove a version or scrub local state.

**With `--version`:** re-sign the entry without that version. Any tags pointing at the removed version are also dropped (to avoid dangling tags). Consumers stop resolving the version once the new entry's LWW takes effect.

```
$ vigo swarm curator rm vigo-agent --version 0.50.2
✓ Dropped version 0.50.2 from e3b0c44298fc
  Note: the blob still sits in the swarm cache until retention/GC expires it (slice 4d).
```

The blob bytes aren't revoked — `gc` (below) cleans up unreferenced curator blobs we seed.

**Without `--version`:** scrub local publish state for this artifact. Consumers keep their last-known entry — LWW has nothing newer to win with — so this is local-only and does NOT unpublish across the fleet. Use the per-version form first if you want to stop a specific version from resolving.

```
$ vigo swarm curator rm vigo-agent
✓ Scrubbed local publish state for e3b0c44298fc
  (/home/dan/.vigo-curator/e3b0c44298fc...)
  Note: this does NOT unpublish across the fleet — peers retain the last entry
        they saw. To stop a specific version from resolving, use
        `vigo swarm curator rm <ref> --version <v>` before this.
```

## Cleanup verbs

### `vigo swarm curator stale`

Read-only inspection of what `prune` would remove plus dangling tags. Doesn't change anything.

```
$ vigo swarm curator stale
Orphan .pending sentinels (older than 180s — reactor should have promoted them):
  /home/dan/.vigo-curator/e3b0…/entry.json.pending (age 421s)

Dangling tags (point at a version that doesn't exist):
  vigo-agent: canary → 0.99.0
```

A clean envoy prints `Curator state is clean — nothing to prune.`

### `vigo swarm curator prune`

Remove orphan `.pending` sentinels (older than 180 s — three reactor sweep cycles) and corrupt/unverifiable entry files. Dangling tags are flagged but not auto-fixed — those require a re-sign (use `untag`).

```
$ vigo swarm curator prune
Pruned 1 orphan .pending sentinel(s) and 0 corrupt entry file(s).
```

### `vigo swarm curator gc`

Free blob-cache disk by revoking `curator:` manifest entries that **this envoy seeded** and that no current catalog entry — local or cached fleet — references. Peers age them out via the manifest's revocation gossip.

Requires root (the manifest revoke runs against the root-owned swarm state). The CLI bails with a clear pointer otherwise.

```
$ sudo vigo swarm curator gc
Revoked 3 curator blob(s) we seeded that no current catalog entry references (24.6 MiB).
Peers will evict their copies via the manifest's revocation gossip; local cache reclaim happens
on the next retention prune cycle.
```

`gc` only touches blobs **this envoy seeded** (`seeder_id == this_envoy_id`). Blobs others have seeded aren't ours to revoke; the manifest enforces that boundary.

### `vigo swarm curator purge --yes`

Power-user wipe. Refuses without `--yes`.

As a non-root user, scrubs `~/.vigo-curator/` for the calling user. As root, also wipes `/var/lib/vigo/artifacts/catalog.json` and `cache.json` and revokes every `curator:` blob this envoy seeded.

```
$ vigo swarm curator purge --yes
✓ Scrubbed /home/dan/.vigo-curator
Note: re-run as root to also wipe /var/lib/vigo/artifacts/ and revoke this envoy's seeded
curator blobs in the manifest.
```

```
$ sudo vigo swarm curator purge --yes
  (/home/dan/.vigo-curator was already absent)
✓ Removed /var/lib/vigo/artifacts/catalog.json
✓ Removed /var/lib/vigo/artifacts/cache.json
✓ Revoked 4 curator blob(s) this envoy seeded.
```

## State layout

```text
~<user>/.vigo-curator/<artifact_id_hex>/
    entry.json                    # canonical signed entry (reactor-promoted; only the reactor writes)
    entry.json.pending            # freshly-signed entry the daemon dropped, awaiting promotion (60 s sweep)

/var/lib/vigo/swarm/.blobs/<sha>  # raw bytes, shared with filecast / gitback / lockbox

/var/lib/vigo/artifacts/
    catalog.json                  # cached fleet catalog from the server (per-check-in refresh)
    cache.json                    # materialization index: <aid>/<name>:<tag> + @<version> → path
    <aid>/<name>/<v>/<os>-<arch>  # materialized 0644 file (hardlink from .blobs/, copy across filesystems)
```

The `~/.vigo-curator/<aid>/entry.json` file is what the `swarm_curator` trait collector reads on every check-in — the path from your local publish to the fleet catalog runs through the root reactor (promotes `.pending` → `entry.json` after re-verifying), the trait collector (every check-in), `server/swarm/curatormesh/` (aggregates per-envoy traits, LWW on `issued_at`), and finally `CheckInResponse.curator_catalog` (cached at every envoy's `/var/lib/vigo/artifacts/catalog.json`). The same shape gitback's name claims take.

## Helper-daemon protocol

The daemon at `/var/run/vigo/curator.sock` speaks two verbs:

```
PUSH                 # publish a new blob
ENTRY                # re-sign an existing artifact without shipping bytes
```

`PUSH` carries the catalog entry as a JSON header plus the raw blob bytes; the daemon answers `READY\n` after header validation, the client streams the file, and the daemon answers `OK\n` (or `ERR <msg>\n`). `ENTRY` carries just the catalog entry as a JSON line; the daemon validates the signature + delegation chain, drops the entry as `.pending`, and (for `set-target`) refreshes the manifest seed for blobs we seed. See [the engineering doc](../../howto/swarm/set-up-curator.md#bytes) for the full wire shape.

The socket is mode 0666 + SO_PEERCRED — any local user can connect; the daemon identifies them by UID and gates on their usercrate `curator: true` flag. Same shape `gitback.sock` uses.

## Operability

Curator is gated per-envoy via the `swarm.curator.enabled` pattern list in `server.yaml`. Both `swarm.enabled` AND `swarm.curator.enabled` must allow the host:

```yaml
swarm:
  enabled: ["*"]
  curator:
    enabled: ["annlap", "plex", "girlslaptop"]
    max_bytes: 5368709120   # 5 GiB
```

When a host falls out of the pattern list, the curator helper daemon stops binding the socket on the next check-in (publishing disabled there). Already-held catalog entries and bytes are preserved — pattern flips are pause/resume, not evict.

The destructive lever is the **usercrate `curator: bool` flag**. Removing it triggers a scrub of `~/.vigo-curator/` for that user on the next reconcile pass via `ScrubScope::Curator`. Per-user state goes; the root-owned blob cache survives — admin cleanup is a separate path (`vigocli swarm curator block <ref>` for moderation, `vigo swarm curator gc` for disk reclaim).

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Success |
| `1` | Error (no puddle, no helper socket, signing rejected, version not present, ambiguous name, etc.) |

## Related

- [`vigocli swarm curator`](swarm-curator.md) — operator side: fleet rollup + admin block / unblock
- [Curator engineering doc](../../howto/swarm/set-up-curator.md) — identity, catalog shape, wire protocol, slicing
- ADR-024 — curator P2P artifact registry — the design
- [`vigo swarm puddle`](agent-swarm-puddle.md) — the per-user identity that anchors curator artifacts
- [`vigo swarm gitback`](agent-swarm-gitback.md) — the sibling subsystem curator's access primitives derive from
- [user executor](../vigo/resources/user.md) — where the `curator: bool` flag goes

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../../legal/license.md).*
