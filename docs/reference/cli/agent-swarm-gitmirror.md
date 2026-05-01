
# vigo swarm gitmirror CLI Reference

`vigo swarm gitmirror` is the envoy-side surface for the unified-substrate gitmirror subsystem (ADR-015). The verb tree manages projects: founding new ones, inviting other fleets, joining as a member, listing local state, and queueing leave / revoke envelopes.

## Status: ADR-015 cutover in progress

Steps 1–3 (multi-call dispatch, capability tokens, per-user state + CLI) and the first two slices of step 4 (helper daemon + `git-remote-vigo` + revoke / leave envelopes; push-driven fan-out + receive-side `POST /swarm/gitmirror/push/<project_id_hex>` endpoint) have shipped. Verbs that mint or sign envelopes (`init`, `invite`, `revoke`, `leave`, `reanchor`) require self-fleet to be unlocked — see [`vigo swarm self-fleet`](agent-swarm-self-fleet.md).

What works end-to-end today:

- `init` / `invite` / `join` / `list` / `status` are fully functional locally.
- `leave` and `revoke` sign envelopes inline via the user's self-fleet session helper, write the signed envelope as a `*.pending` sentinel; the agent's root reactor sweeps every 60 s and persists them to `/var/lib/vigo/swarm/gitmirror/envelopes/<project_id>/{revokes,leaves}/`.
- `git clone vigo://<project_id>/<handle>` and `git push vigo://…` against the local envoy: the user-side `git-remote-vigo` binary opens `/var/run/vigo/gitmirror.sock` (SO_PEERCRED-gated), the agent verifies the user's token chain, and proxies to `git upload-pack` / `git receive-pack` against the bare repo.
- Cross-envoy push: after a successful local `git-receive-pack`, the helper daemon snapshots refs pre/post and (on delta) builds a `git bundle --all` and POSTs it to every known peer for the project over swarm mTLS (port 1531). Targets come from two deduplicated sources: peers that have advertised holding the project on the LAN multicast (`GITMIRRORPROJECT:<host>:<pid_hex>` announce lines, indexed by `ProjectPeers`), and explicit hostnames listed in `project.json::cross_subnet_members`. Receivers verify the X-Vigo-Chain header against the URL's project_id, ingest via `git bundle verify` + `git fetch`, and return `{accepted, refs_changed}`.

What's still pending (follow-up slices of step 4):

- Cross-envoy envelope gossip — revoke / leave envelopes persist locally but don't yet replicate to peers.
- `reanchor` accepts the type-the-handle confirmation but bails before the byte-level migration.

## Invocation

```
vigo swarm gitmirror project <verb> [args]
```

The command lives inside the `vigo` agent binary. CLI verbs run as the calling user — root is not required and would discard the user's per-fleet identity. Every verb reads from `~/.vigo-gitmirror/<project_id>/` (per-user) and the bare-repo dir at `/var/lib/vigo/swarm/gitmirror/projects/` (per-envoy, root-owned, mode `0700` — only the helper daemon proxies non-root reads).

## Project identity

Every project is identified by `project_id = sha256(founder_fleet_pubkey)` rendered as 64-character lowercase hex. Project URLs take the form:

```
vigo://<project_id>/<handle>
```

The handle is human-readable; cryptographic identity is the project_id. Squatting on a handle is impossible — there's no central registry. Any fleet can found a project named `vigo-web`, but the resulting project_ids will differ.

Verbs that take a `<project_id>` argument also accept a unique handle prefix among projects this user has joined locally — `vigo swarm gitmirror project status vigo-web` resolves the same as the full hex id.

## Commands

| Verb | Purpose |
|------|---------|
| `project init <handle> [--fanout <kind>]` | Found a new project as the local fleet |
| `project invite <project_id> --fleet <fp> --role <role> [--expires <Nd>]` | Mint a child token for another fleet |
| `project join <project_id> [--invitation <file\|->]` | Adopt an invitation issued by an admin |
| `project leave <project_id> [--purge] [--yes]` | Drop local membership, queue LEAVE envelope |
| `project list [--json]` | List projects this user has joined locally |
| `project status <project_id>` | Show one project's local state |
| `project revoke <project_id> --token-id <hex> [--reason <text>]` | Queue a revocation for a previously-issued token |
| `project reanchor <project_id>` | Hard-fork the project under a new founder (interactive confirm) |

## `project init`

Founds a new project. The local fleet (per [ADR-014](../../adr/014-self-fleet-primitive.md)) becomes the founder; `project_id = sha256(local_fleet_pubkey)`.

```
vigo swarm gitmirror project init <handle> [--fanout fleet-wide|members-only]
```

**Arguments:**

- `<handle>` — human-readable name. 1-64 ASCII alphanumeric + `-` `_` `.`, no leading dot.
- `--fanout` — distribution scope:
  - `fleet-wide` (default): bare repo lands on every (non-blacklisted) envoy in the fleet for DR purposes.
  - `members-only`: bare repo lands only on envoys whose fleet is in the project's `members` list.

**Preconditions:** self-fleet must be unlocked. Run `vigo swarm self-fleet unlock` first.

**Effects:** writes `project.json` and `tokens.json` (with the founder's bootstrap token) under `~/.vigo-gitmirror/<project_id>/`. Prints the canonical URL.

```
$ vigo swarm gitmirror project init vigo-web
✓ Founded project vigo-web
  URL:        vigo://e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855/vigo-web
  Project ID: e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
  Founder:    ed25519:f43f6ef3eea29e3780d73aa2cee4a78193419a69f43f6ef3eea29e3780d73aa2
  Fan-out:    fleet-wide
```

## `project invite`

Mints a child token for another fleet and prints a self-contained JSON invitation the recipient pipes into `project join`.

```
vigo swarm gitmirror project invite <project_id> --fleet <fp> --role admin|member [--expires <Nd>]
```

**Arguments:**

- `<project_id>` — full hex or unique handle prefix.
- `--fleet <fp>` — recipient's fleet pubkey. Accepts either bare 64-char hex or `ed25519:<hex>` form (the format `vigo swarm self-fleet pubkey` prints).
- `--role admin|member`:
  - `admin` — recipient can mint further child tokens (invite + revoke).
  - `member` — recipient can fetch + push but not invite or revoke.
- `--expires <Nd>` — optional. `30d` or bare `30` interpreted as days. Default is no expiry.

**Preconditions:** the local fleet must hold an admin token on the project.

**Output:** JSON invitation on stdout, instructions on stderr. Pipe stdout to the recipient by any side channel:

```
$ vigo swarm gitmirror project invite vigo-web \
    --fleet ed25519:abc... --role member > invite.json
$ scp invite.json other-laptop:~
```

The recipient runs:

```
$ vigo swarm gitmirror project join <project_id> --invitation invite.json
```

## `project join`

Adopts an invitation issued by an admin.

```
vigo swarm gitmirror project join <project_id> [--invitation <file|->]
```

**Arguments:**

- `<project_id>` — must match the invitation's anchor (sanity check — catches "wrong invitation pasted").
- `--invitation <file|->` — path to the JSON invitation, or `-` to read from stdin (default).

**Preconditions:** self-fleet must be initialized for this user (`vigo swarm self-fleet init` once, then per-envoy `join --from <host>`).

**Verification:** the chain is verified offline against the founder pubkey embedded in the invitation. The leaf token's subject must match the local fleet pubkey — an invitation issued for someone else's fleet is rejected.

## `project leave`

Drops local membership.

```
vigo swarm gitmirror project leave <project_id> [--purge] [--yes]
```

**Arguments:**

- `<project_id>`
- `--purge` — also wipe the bare repo bytes on this envoy. Requires root (the bare repo lives at `0700 root:root`).
- `--yes` — skip the confirmation prompt.

**Effects:**

- Signs a LEAVE envelope inline via the user's self-fleet session helper and writes it as a `leave.pending` sentinel under `~/.vigo-gitmirror/<project_id>/`. The agent's root reactor sweeps every 60 s and moves the signed envelope into `/var/lib/vigo/swarm/gitmirror/envelopes/<project_id>/leaves/`. After the next slice of step 4 lands, the same envelope will gossip to peers; today it persists locally only.
- Removes the per-user state directory (`project.json` + `tokens.json` + sentinels).
- With `--purge` and root, removes `/var/lib/vigo/swarm/gitmirror/projects/<project_id>.git`.

## `project list`

```
$ vigo swarm gitmirror project list
HANDLE                   FAN-OUT        MEMBERS        TOKENS   PROJECT_ID
vigo-web                 fleet-wide     1              1        e3b0c44298fc1c14…
team-notes               members-only   3              2        a891c4b27e3431af…
```

`--json` emits the full `ProjectConfig` array as JSON.

## `project status`

Multi-line detail block: handle, URL, project_id, founder, fan-out, member list, token chain depth, leaf role + subject + issued / expires timestamps, state-dir path, pending-sentinel flags.

```
vigo swarm gitmirror project status <project_id>
```

## `project revoke`

Admin only. Queues a revocation envelope for a previously-issued token.

```
vigo swarm gitmirror project revoke <project_id> --token-id <hex> [--reason <text>]
```

**Arguments:**

- `<project_id>`
- `--token-id <hex>` — 64-char hex token ID. Tokens are content-addressed (`token_id = sha256(canonical_bytes)`), surfaced via `project status` (chain depth) and the audit log.
- `--reason <text>` — optional human-readable reason captured in the envelope.

**Effects:** signs a REVOKE envelope inline via the user's self-fleet session helper (the leaf admin token's subject == this user's fleet pubkey, so the user holds an authorized signing key) and writes it as a `revoke.pending` sentinel under `~/.vigo-gitmirror/<project_id>/`. The agent's root reactor sweeps every 60 s and moves the signed envelope into `/var/lib/vigo/swarm/gitmirror/envelopes/<project_id>/revokes/<token_id_hex>.json`. From that point any push presenting a chain that includes the revoked token id is rejected by the helper daemon on this envoy. Cross-envoy gossip lands in the next slice of step 4. 90-day retention before reap, matching cryptsync.

## `project reanchor`

Hard-fork the project under a new founder. Used when the original founder fleet (and all admin fleets) are gone — surviving members can adopt a new anchor and continue.

```
vigo swarm gitmirror project reanchor <project_id>
```

**Interactive:** prints the consequences (old URL becomes dead, members must re-add the new remote, tokens not portable) and refuses to proceed unless the user types the project handle to confirm. Same anti-footgun pattern as `git push --force-with-lease`.

**Status:** the confirmation is wired in step 3, but the actual byte-level migration of refs and objects into the new project_id is the last item of step 4 (after fan-out, gossip, and the server-mesh rewrite). Today the verb bails after the type-the-handle check with a pointer to that follow-up slice.

## Hidden verbs

`_apply-blacklist` is dispatched by the server via TaskDispatch when an envoy newly matches `swarm.gitmirror.blacklist`. It wipes every locally-held bare repo under `/var/lib/vigo/swarm/gitmirror/projects/`. Idempotent. Not advertised in `--help`.

## See also

- [ADR-015 — Unified gitmirror substrate](../../adr/015-unified-gitmirror-substrate.md)
- [ADR-014 — Self-fleet primitive](../../adr/014-self-fleet-primitive.md)
- [vigo swarm self-fleet CLI reference](agent-swarm-self-fleet.md)
- [Capability-token primitives — `tokens.rs`](../../engineering/subsystems/gitmirror.md)
