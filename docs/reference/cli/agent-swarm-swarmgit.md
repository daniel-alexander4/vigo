
# vigo swarm swarmgit CLI Reference

`vigo swarm swarmgit` is the envoy-side surface for the unified-substrate swarmgit subsystem (ADR-015). The verb tree manages projects: founding new ones, inviting other fleets, joining as a member, listing local state, and queueing leave / revoke envelopes.

## Status: ADR-015 cutover in progress

Steps 1–3 (multi-call dispatch, capability tokens, per-user state + CLI) and step 4's first six slices (helper daemon + `git-remote-swarmgit`; push-driven fan-out + receive-side endpoint; LAN auto-discovery; envelope gossip with digest catch-up; trait re-emit + mesh schema + UI/CLI rewrite; REST member-roster + reanchor byte migration) have shipped. Verbs that mint or sign envelopes (`init`, `invite`, `revoke`, `leave`, `reanchor`) require puddle to be unlocked — see [`vigo swarm puddle`](agent-swarm-puddle.md).

What works end-to-end today:

- `init` / `invite` / `join` / `list` / `status` are fully functional locally. Run from inside a git work tree, `init` also adds the `swarm` remote and `git push --all && --tags` — founding and publishing a repo is one command; `--no-push` keeps it register-only. `project push` is the standalone re-publish verb (remote-add-if-missing, push `--all`/`--tags`).
- `leave` and `revoke` sign envelopes inline via the user's puddle session helper, write the signed envelope as a `*.pending` sentinel; the agent's root reactor sweeps every 60 s and persists them to `/var/lib/vigo/swarm/swarmgit/envelopes/<project_id>/{revokes,leaves}/`.
- `git clone swarmgit://<project_id>/<handle>` and `git push swarmgit://…` against the local envoy: the user-side `git-remote-swarmgit` binary opens `/var/run/vigo/swarmgit.sock` (SO_PEERCRED-gated), the agent verifies the user's token chain, and proxies to `git upload-pack` / `git receive-pack` against the bare repo.
- Cross-envoy push: after a successful local `git-receive-pack`, the helper daemon snapshots refs pre/post and (on delta) builds a `git bundle --all` and POSTs it to every known peer for the project over swarm mTLS (port 1531). Targets come from two deduplicated sources: peers that have advertised holding the project on the LAN multicast (`SWARMGITPROJECT:<host>:<pid_hex>:<handle>:<dr_scope>` announce lines — ADR-023 widened the line from the original 2-field `SWARMGITPROJECT:<host>:<pid_hex>`, which still parses during a rolling agent upgrade — indexed by `ProjectPeers`), and explicit hostnames listed in `project.json::cross_subnet_members`. Receivers verify the X-Vigo-Chain header against the URL's project_id, ingest via `git bundle verify` + `git fetch`, and return `{accepted, refs_changed}`.
- DR catch-up (0.46.0): a member that holds local project state but no bare repo (freshly joined, re-joined after `re-found`, auto-adopted at `puddle unlock` per ADR-023, etc.) pulls one. Every ~45 s the catch-up reactor walks each user's `~/.vigo-swarmgit/<pid>/`; for the first project missing its bare repo, it `GET`s `/swarm/swarmgit/bundle/<pid>` from a peer that announces holding it, with the same X-Vigo-Chain envelope a push carries, and ingests the returned `git bundle --all` through the push path. One repo backfilled per tick; a no-op once every locally-held project has refs. Removes the old need for a throwaway-tag push or a hand-edited `cross_subnet_members` to seed a new envoy. The reactor also flushes the world-readable `swarmgit-announced.json` cache that `puddle unlock` reads for ADR-023 auto-adoption.
- Cross-envoy envelope gossip: after `revoke` or `leave` writes a signed envelope locally, the reactor fans it out to every known peer via `POST /swarm/swarmgit/envelopes/<pid>/{revokes,leaves}`. Receivers verify the signature, re-check the project_id, and persist idempotently. Every 5 minutes the reactor reconciles via `GET /swarm/swarmgit/envelopes/<pid>/digest` against each peer; on mismatch it pulls `GET /swarm/swarmgit/envelopes/<pid>/all` and merges. Peers offline during the original gossip catch up within one reconciliation tick of coming back online.

What's still pending (follow-up slices of step 4):

All step-4 follow-up work has shipped. Cross-envoy push, fetch, envelope gossip, fleet observability, REST member-roster, and `reanchor` byte migration are all in tree.

### ADR-019 (founder-rekey delegations) — complete

[ADR-019](../../adr/019-swarmgit-founder-rekey.md) shipped across five slices at 0.35.18 → 0.35.22:

- **Slice 1 (0.35.18)** — `DelegationEnvelope` data layer: Ed25519-signed bridge from a retired founder pubkey to a new one, persisted at `~/.vigo-swarmgit/<pid>/delegations.json`. Hard cap at `DELEGATION_MAX_LEN = 5`, soft warn at `DELEGATION_WARN_LEN = 3`.
- **Slice 2 (0.35.19)** — chained verification: `tokens::verify_chain` walks the delegation list before the token chain, applying authority windows so retired founders can't sign post-window tokens (the deposed-founder attack). The helper daemon's per-user verify already loads the local delegation chain.
- **Slice 3 (0.35.20)** — rekey-time hooks: `puddle rekey-pair` and `rekey --from` automatically sign + append delegations on every founded project and rewrite member entries OLD → NEW. `--force-orphan-projects` flag covers the cap case. `project status` now displays delegation chain length, current founder pubkey, and warn-band / at-cap banners.
- **Slice 4 (0.35.21)** — multi-puddle rejoin: new `re-invite-all` (founder side) and `rejoin` (member side) verbs handle cross-puddle members after a founder rekey via fresh out-of-band capability codes, same trust hop as the original `invite` / `join` flow. The post-rotation hook now also rebuilds the founder's `tokens.json` with `[bootstrap, fresh_admin_under_NEW]` so subsequent re-invites work without manual intervention. The wire chain header (`X-Vigo-Chain`) gained a `PushChain` envelope that carries both the token chain and the founder's delegation list, with backwards-compat fallback for pre-ADR-019 senders that emitted bare `TokenChain` JSON.
- **Slice 5 (0.35.22)** — `project re-found` escape hatch: mints a fresh `project_id` from the current puddle pubkey, byte-migrates from the at-cap bare repo via the same `git fetch +refs/*:refs/*` path `reanchor` uses, and tombstones the old project_id on disk via a new `project.json::tombstoned_at` field. `--force-orphan-projects` on `rekey-pair` also writes the tombstone marker. Tombstoned projects keep their bare repo + URL on disk as an archive but reject helper-daemon git operations and skip `cmd_invite` / `cmd_re_invite_all`. Plus this status writeup and the formal ADR-019 document.

Single-puddle personal-DR survives a founder rekey transparently. Multi-puddle projects need a one-time `re-invite-all` (founder) + `rejoin` (each cross-puddle member) ceremony after the rekey. After 5 chained rekeys, `project re-found` is the path forward.

## Invocation

```
vigo swarm swarmgit status
vigo swarm swarmgit purge --yes
vigo swarm swarmgit project <verb> [args]
```

The command lives inside the `vigo` agent binary. CLI verbs run as the calling user — root is not required and would discard the user's per-puddle identity. Every verb reads from `~/.vigo-swarmgit/<project_id>/` (per-user) and the bare-repo dir at `/var/lib/vigo/swarm/swarmgit/projects/` (per-envoy, root-owned, mode `0700` — only the helper daemon proxies non-root reads).

**Usercrate authorization (0.41.0+).** Every verb on this page (except when invoked as root) refuses unless the invoking user has `swarmgit: true` set on the user resource in their usercrate. The agent's user executor writes the resolved flags to `/var/lib/vigo/usercrate-policy/<user>.json` on each reconcile pass; the CLI reads it as a precondition. Both layers — envoy-level (`swarm.swarmgit.enabled` pattern list in server.yaml) and user-level (this flag) — must say yes.

## Project identity

Every project is identified by `project_id = sha256(founder_puddle_pubkey ‖ handle)` rendered as 64-character lowercase hex. Project URLs take the form:

```
swarmgit://<project_id>/<handle>
```

The handle is human-readable; cryptographic identity is the project_id. Squatting on a handle is impossible — there's no central registry. Any fleet can found a project named `vigo-web`, but the resulting project_ids will differ.

**Friendly URLs (ADR-022).** If the founder's puddle has claimed a name (`vigo swarm puddle name set <name>`), `swarmgit://<name>/<handle>` works for git operations alongside the hex form — `git-remote-swarmgit` resolves `<name>` to the puddle pubkey via the cached fleet name map and computes `sha256(pubkey ‖ handle)`. On a fleet-name-map miss (or a fresh envoy whose map hasn't bootstrapped yet), the resolver falls back to the *requesting* user's own `~/.vigo-puddle/name-claim.json`, so `swarmgit://<my-name>/<repo>` resolves from the envoy that just ran `puddle name set` without waiting for the claim to propagate through the trait → server → re-fetch round trip. The friendly form is a convenience; the hex `swarmgit://<project_id>/<handle>` is the proof (a name is squattable — first claim wins). See [`vigo swarm puddle name`](agent-swarm-puddle.md#friendly-names-adr-022). The `<project_id>` arguments below take the hex id or a local handle prefix — *not* a friendly name (that's a URL-resolution feature, not a CLI-argument one).

Verbs that take a `<project_id>` argument also accept a unique handle prefix among projects this user has joined locally — `vigo swarm swarmgit project status vigo-web` resolves the same as the full hex id.

## Commands

| Verb | Purpose |
|------|---------|
| `status` | Show envoy-wide swarmgit posture (publisher + receiver state, helper-daemon socket) |
| `purge --yes` | Power-user wipe of local swarmgit state. As a non-root user: `~/.vigo-swarmgit/`. As root: also envoy-wide root-owned state. Distinct from per-project `leave --purge`; does not gossip. |
| `project init <handle> [--scope <kind>] [--repo <path>] [--no-push]` | Found a new project as the local fleet — and, from inside a git work tree, also add the `swarm` remote and `git push --all && --tags` to publish it |
| `project push [<handle>] [--repo <path>]` | Publish (or re-publish) a project from its working repo: add the `swarm` remote if missing, or re-point it after a `re-found`/`reanchor`, then `git push swarm --all && --tags`. Idempotent |
| `project invite <project_id> --puddle <fp> --role <role> [--expires <Nd>]` | Mint a child token for another fleet |
| `project join <project_id> [--invitation <file\|->]` | Adopt an invitation issued by an admin |
| `project leave <project_id> [--purge] [--yes]` | Drop local membership, queue LEAVE envelope |
| `project list [--json]` | List projects this user has joined locally |
| `project status <project_id>` | Show one project's local state |
| `project revoke <project_id> --token-id <hex> [--reason <text>]` | Queue a revocation for a previously-issued token |
| `project reanchor <project_id>` | Hard-fork the project under a new founder (interactive confirm) |
| `project re-invite-all [<project_id>]` | (ADR-019) Founder side, post-rekey: mint fresh invitations for every cross-puddle member |
| `project rejoin <project_id> [--invitation <file\|->]` | (ADR-019) Member side: idempotently update local chain in place after a founder rekey |
| `project re-found <project_id> [--force]` | (ADR-019) Escape hatch when chain is at cap: mint fresh project_id, byte-migrate, tombstone old |

## `status`

Envoy-wide swarmgit posture, parallel to [`vigo swarm lockbox status`](agent-swarm-lockbox.md#status). The bare repos under `/var/lib/vigo/swarm/swarmgit/projects/` are `0700 root:root` (non-root callers reach them only through the helper daemon), so `status` asks the helper daemon — which runs as root — for the authoritative envoy-wide view (per-user `~/.vigo-swarmgit/` state joined with the bare repos, head refs, and queued envelopes — exactly what the `swarmgit` trait carries). If the helper daemon's socket isn't reachable it falls back to a local scan, which sees only the calling user's per-user state and — unless you run it as root — no bare-repo facts; that output is marked `partial data:`. Distinct from `project status <id>`, which shows one project in detail.

```
vigo swarm swarmgit status
```

**Arguments:** none.

**Preconditions:** none. Read-only; safe for any user; does not require puddle to be unlocked.

**Output:**

```
$ vigo swarm swarmgit status
swarmgit status
  base:                /var/lib/vigo/swarm/swarmgit
  projects:            4 (3 with a local bare repo)
  bare repos:          511.4 MB on disk
  envelopes:           0 revoke(s), 0 leave(s) queued
    apractia                 swarm-wide    1 ref(s), 4.1 MB
    tessera                  swarm-wide    1 ref(s), 812.0 KB
    vigo                     swarm-wide    1 ref(s), 506.5 MB
    vigo-web                 members-only  no bare repo yet (awaiting first push or DR catch-up)
  helper daemon:       socket present (/var/run/vigo/swarmgit.sock)
```

On an envoy where no Unix user has founded or joined a project, the `projects:` line reads `none`. A project shows `no bare repo yet` until its first push lands or the DR catch-up reactor pulls it from a peer. The socket line shows `socket absent` when the helper daemon isn't running — in which case a `partial data:` line warns that the bare-repo counts above came from a local scan and may be incomplete (run as root, or restart the agent, for the full picture).

## `purge`

Power-user wipe of local swarmgit state on this envoy. Symmetric with [`vigo swarm lockbox purge`](agent-swarm-lockbox.md#vigo-swarm-lockbox-purge). Distinct from `project leave --purge` (one project's bare repo) and from the server-driven `_apply-blacklist` (envoy-wide bare repos via TaskDispatch).

```
vigo swarm swarmgit purge --yes
```

**Mode-aware behaviour:**

- **As a non-root user:** wipes `~/.vigo-swarmgit/` for the calling user (every joined project's per-user state — `project.json`, `tokens.json`, `delegations.json`, any pending sentinels). Root-owned bare repos under `/var/lib/vigo/swarm/swarmgit/projects/` survive; rerun under `sudo` to scrub those too.
- **As root:** wipes the envoy-wide root-owned `projects/` dir under `/var/lib/vigo/swarm/swarmgit/`. The user-side `~/.vigo-swarmgit/` of the invoking user is also cleared if it exists; per-user dot-dirs of OTHER users are not touched (each user's home is theirs).

**Flags:**

- `--yes` — required. Without it the verb prints what would be wiped and exits.

**Effects:**

- Files are removed via `fs::remove_dir_all` / `fs::remove_file`. No envelope is signed and no gossip is fanned out — peers age this envoy out via the swarm staleness window unless the operator runs `project leave [--yes]` per project first or announces out-of-band.
- If any `leave.pending` / `revoke.pending` sentinels existed at purge time, the verb prints a follow-up note so the operator knows that gossip never reached peers.

**When to use it.** Decommissioning an envoy where you want every swarmgit byte gone immediately. For a graceful departure where peers learn of it, use `project leave` per project first.

## `project init`

Founds a new project. The local fleet (per [ADR-014](../../adr/014-puddle-primitive.md)) becomes the founder; `project_id = sha256(local_puddle_pubkey ‖ handle)`. Run from inside a git work tree (or pass `--repo <path>`), `init` also adds the `swarm` remote and publishes the repo — founding and publishing are one command.

```
vigo swarm swarmgit project init <handle> [--scope swarm-wide|members-only] [--repo <path>] [--no-push]
```

**Arguments:**

- `<handle>` — human-readable name. 1-64 ASCII alphanumeric + `-` `_` `.`, no leading dot.
- `--scope` — distribution scope:
  - `swarm-wide` (default): bare repo lands on every swarmgit-authorized envoy in the swarm for DR purposes.
  - `members-only`: bare repo lands only on envoys whose puddle is in the project's `members` list.
- `--repo <path>` — the git work tree to publish. Default: the current directory if it's a git work tree.
- `--no-push` — register the project (`project.json` + `tokens.json`) only; don't touch any working repo. Publish later with `project push`.

**Preconditions:** puddle must be unlocked. Run `vigo swarm puddle unlock` first.

**Effects:**

1. Writes `project.json` and `tokens.json` (with the founder's bootstrap token) under `~/.vigo-swarmgit/<project_id>/`.
2. Unless `--no-push`: if the working repo (cwd or `--repo`) is a git work tree and the helper daemon socket is up, adds a `swarm` remote pointing at `swarmgit://<project_id>/<handle>` (hex form — always resolves) and runs `git push swarm --all && git push swarm --tags`, which seeds the bare repo and triggers fan-out. Warns (doesn't block) when `<handle>` doesn't match the work-tree's directory name. If you're not in a git work tree, or the helper isn't running, or a `swarm` remote already points elsewhere, it registers the project only and prints how to publish later.

```
$ cd ~/repos/vigo-web && vigo swarm swarmgit project init vigo-web
✓ Founded project vigo-web
  URL:           swarmgit://e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855/vigo-web
  Founder:       ed25519:f43f6ef3eea29e3780d73aa2cee4a78193419a69f43f6ef3eea29e3780d73aa2
  Scope:         swarm-wide
  State:         /home/dan/.vigo-swarmgit/e3b0c4…/
  Remote 'swarm' → swarmgit://e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855/vigo-web
… git push output …

Published. Verify replication with `vigocli swarm swarmgit show vigo-web`.
```

When the founder's puddle has claimed a name (`vigo swarm puddle name set`), the `URL:` line shows the friendly `swarmgit://<name>/<handle>` form with `Canonical URL:` beneath it.

## `project push`

Publishes (or re-publishes) a project from its working repo. The everyday companion to `init` — and what you run after a `re-found`/`reanchor` changes the `project_id`. Idempotent; safe to re-run.

```
vigo swarm swarmgit project push [<handle>] [--repo <path>]
```

**Arguments:**

- `<handle>` — a project handle (or `project_id` / unique prefix) you have local state for. With it, `push` ensures the `swarm` remote points at this project — adding it if missing, or re-pointing it (`git remote set-url`) if it currently addresses a different project. Without it, `push` requires an existing `swarm` remote and pushes to it as-is.
- `--repo <path>` — the git work tree. Default: the current directory.

**Preconditions:** must be run from inside a git work tree (or with `--repo`); the helper daemon socket must be up.

**Effects:** ensures the `swarm` remote (per the rule above), then `git push swarm --all && git push swarm --tags`.

```
$ cd ~/repos/vigo-web && vigo swarm swarmgit project push vigo-web
✓ Pushed /home/dan/repos/vigo-web to the swarm.
```

## `project invite`

Mints a child token for another fleet and prints a self-contained JSON invitation the recipient pipes into `project join`.

```
vigo swarm swarmgit project invite <project_id> --puddle <fp> --role admin|member [--expires <Nd>]
```

**Arguments:**

- `<project_id>` — full hex or unique handle prefix.
- `--puddle <fp>` — recipient's puddle pubkey. Accepts either bare 64-char hex or `ed25519:<hex>` form (the format `vigo swarm puddle pubkey` prints).
- `--role admin|member`:
  - `admin` — recipient can mint further child tokens (invite + revoke).
  - `member` — recipient can fetch + push but not invite or revoke.
- `--expires <Nd>` — optional. `30d` or bare `30` interpreted as days. Default is no expiry.

**Preconditions:** the local fleet must hold an admin token on the project.

**Output:** JSON invitation on stdout, instructions on stderr. Pipe stdout to the recipient by any side channel:

```
$ vigo swarm swarmgit project invite vigo-web \
    --fleet ed25519:abc... --role member > invite.json
$ scp invite.json other-laptop:~
```

The recipient runs:

```
$ vigo swarm swarmgit project join <project_id> --invitation invite.json
```

## `project join`

Adopts an invitation issued by an admin.

```
vigo swarm swarmgit project join <project_id> [--invitation <file|->]
```

**Arguments:**

- `<project_id>` — must match the invitation's anchor (sanity check — catches "wrong invitation pasted").
- `--invitation <file|->` — path to the JSON invitation, or `-` to read from stdin (default).

**Preconditions:** puddle must be initialized for this user (`vigo swarm puddle init` once, then per-envoy `join --from <host>`).

**Verification:** the chain is verified offline against the founder pubkey embedded in the invitation. The leaf token's subject must match the local puddle pubkey — an invitation issued for someone else's fleet is rejected.

## `project leave`

Drops local membership.

```
vigo swarm swarmgit project leave <project_id> [--purge] [--yes]
```

**Arguments:**

- `<project_id>`
- `--purge` — also wipe the bare repo bytes on this envoy. Requires root (the bare repo lives at `0700 root:root`).
- `--yes` — skip the confirmation prompt.

**Effects:**

- Signs a LEAVE envelope inline via the user's puddle session helper and writes it as a `leave.pending` sentinel under `~/.vigo-swarmgit/<project_id>/`. The agent's root reactor sweeps every 60 s and moves the signed envelope into `/var/lib/vigo/swarm/swarmgit/envelopes/<project_id>/leaves/`. After the next slice of step 4 lands, the same envelope will gossip to peers; today it persists locally only.
- Removes the per-user state directory (`project.json` + `tokens.json` + sentinels).
- With `--purge` and root, removes `/var/lib/vigo/swarm/swarmgit/projects/<project_id>.git`.

## `project list`

```
$ vigo swarm swarmgit project list
HANDLE                   SCOPE          MEMBERS        TOKENS   PROJECT_ID
vigo-web                 swarm-wide     1              1        e3b0c44298fc1c14…
team-notes               members-only   3              2        a891c4b27e3431af…
```

`--json` emits the full `ProjectConfig` array as JSON.

## `project status`

Multi-line detail block: handle, URL, project_id, founder, fan-out, member list, token chain depth, leaf role + subject + issued / expires timestamps, ADR-019 delegation chain length and current founder pubkey (with warn-band yellow flag at length ≥ `DELEGATION_WARN_LEN` and a hard "AT CAP" banner at `DELEGATION_MAX_LEN`), tombstone marker (when set, with a hint pointing at `re-found` or `project leave`), state-dir path, pending-sentinel flags.

```
vigo swarm swarmgit project status <project_id>
```

## `project revoke`

Admin only. Queues a revocation envelope for a previously-issued token.

```
vigo swarm swarmgit project revoke <project_id> --token-id <hex> [--reason <text>]
```

**Arguments:**

- `<project_id>`
- `--token-id <hex>` — 64-char hex token ID. Tokens are content-addressed (`token_id = sha256(canonical_bytes)`), surfaced via `project status` (chain depth) and the audit log.
- `--reason <text>` — optional human-readable reason captured in the envelope.

**Effects:** signs a REVOKE envelope inline via the user's puddle session helper (the leaf admin token's subject == this user's puddle pubkey, so the user holds an authorized signing key) and writes it as a `revoke.pending` sentinel under `~/.vigo-swarmgit/<project_id>/`. The agent's root reactor sweeps every 60 s and moves the signed envelope into `/var/lib/vigo/swarm/swarmgit/envelopes/<project_id>/revokes/<token_id_hex>.json`. From that point any push presenting a chain that includes the revoked token id is rejected by the helper daemon on this envoy. Cross-envoy gossip lands in the next slice of step 4. 90-day retention before reap, matching lockbox.

## `project reanchor`

Hard-fork the project under a new founder. Used when the original founder puddle (and all admin fleets) are gone — surviving members can adopt a new anchor and continue.

```
vigo swarm swarmgit project reanchor <project_id>
```

**Interactive:** prints the consequences (old URL becomes dead, members must re-add the new remote, tokens not portable) and refuses to proceed unless the user types the project handle to confirm. Same anti-footgun pattern as `git push --force-with-lease`.

**Effects:**

- Refuses if `sha256(this_user_puddle_pubkey ‖ handle) == old_project_id` (the calling user is already the founder, so reanchor is a no-op).
- Refuses if a project already exists locally under the proposed new project_id.
- Mints a fresh founder bootstrap admin token under this user's puddle and persists `project.json` + `tokens.json` under `~/.vigo-swarmgit/<new_project_id>/`. The new members list starts as `[this_fleet]`; surviving members from the old project must be re-invited via `project invite <new_project_id> --puddle <fp> --role <r>`.
- When run as root **and** the old bare repo exists at `/var/lib/vigo/swarm/swarmgit/projects/<old_project_id>.git`, runs `git -C <new_repo> fetch <old_repo> +refs/*:refs/*` to copy every ref + the objects backing them into the new bare repo. Non-root invocations (or envoys that don't hold the old bare repo) print a note and rebuild only the local auth surface; another member with root + the old bare repo can complete the migration later.
- The old project state is left in place — drop it via `project leave <old_project_id>` once the new project is healthy.

## `project re-invite-all`

(ADR-019) Founder-side flow run after `puddle rekey-pair`. Walks every multi-puddle project the local fleet founded, mints a fresh invitation for every cross-puddle member, prints the JSON to stdout with stderr separators per recipient. Single-puddle personal-DR projects are skipped (their members are sibling envoys in the user's own puddle, already inheriting trust via ADR-018's pair-claim ceremony).

```
vigo swarm swarmgit project re-invite-all [<project_id>]
```

**Arguments:**

- Optional positional `<project_id>` (or unique handle prefix) — restricts to a single project. Without an argument, walks every founded multi-puddle project.

**Preconditions:** puddle must be unlocked (the verb signs new leaf tokens via the session helper). The local user must be the founder of at least one multi-puddle project — the founder predicate uses `chain.last().new_founder_pubkey` (or `founder_puddle_pubkey` if no delegations) compared against the local puddle pubkey.

**Effects:**

- For every founded project where `members[]` minus the local fleet is non-empty, mints a fresh `Member` token chained off the local user's current admin leaf (which slice 3's rekey hook rebuilt under the new identity).
- Embeds the project's full delegation chain in each invitation so the recipient's verifier can walk the post-rekey leaf back to the genesis founder pubkey baked into `project_id`.
- Prints `(member fleet, project handle, invitation JSON)` triples to stdout; per-recipient header lines go to stderr so the operator can split the JSON blocks into per-recipient files via simple shell tooling. Pipe each JSON block out-of-band to the matching member puddle — same trust hop as the original `invite` flow.

**Threat model:** an attacker who has compromised a retired founder key could forge their own delegation envelope and present a chain that mathematically verifies. The OOB capability code (the `re-invite-all` JSON block being communicated through a trusted channel from a trusted founder) is the trust gate; receivers do not auto-accept gossiped delegations.

```
$ vigo swarm swarmgit project re-invite-all
─── re-invite for project 'team-repo' → ed25519:ab12… ───
{
  "version": 1,
  "project": { … },
  "chain": [ … ],
  "delegations": [ … ]
}

✓ 1 re-invite invitation(s) printed across founded multi-puddle project(s).
  Skipped 2 single-puddle project(s) (no cross-puddle members): vigo-web, dotfiles

Communicate each JSON block out-of-band to the matching member puddle,
then have them run:
    vigo swarm swarmgit project rejoin <project_id> --invitation -
```

## `project rejoin`

(ADR-019) Member-side flow run after the founder of a multi-puddle project has rekeyed and run `re-invite-all`. Idempotent extension of `project join` — does NOT bail when the project already exists locally.

```
vigo swarm swarmgit project rejoin <project_id> [--invitation <file|->]
```

**Arguments:**

- `<project_id>` (or unique handle prefix) — must match the project_id baked into the invitation JSON.
- `--invitation <file|->` — path to the invitation JSON, or `-` for stdin (default).

**Preconditions:** local puddle identity must be initialized. Unlike `join`, `rejoin` does NOT require the absence of an existing project state.

**Effects:**

- Verifies the invitation chain via `verify_chain` with the embedded delegations as the authority bridge. Rejects if the chain doesn't anchor to the genesis founder pubkey baked into `project_id`, if the leaf doesn't grant access to the local fleet, or if any delegation signature fails.
- Updates `project.json`, `tokens.json`, and `delegations.json` from the invitation. When the invitation has empty delegations (founder rolled back or re-founded under genesis), removes any stale local `delegations.json` so the verifier doesn't apply bogus authority windows.
- Preserves any unrelated local state (revoke / leave pending sentinels). The bare repo bytes on this envoy are untouched — `project_id` is stable across the founder's rekey, so no data migration is needed.

```
$ vigo swarm swarmgit project rejoin team-repo --invitation re-invite.json
✓ Re-joined project team-repo (chain refreshed)
  URL:        swarmgit://e3b0…/team-repo
  Role:       Member
  Members:    3
  Scope:      members-only
  Delegations: 1 hop(s) (chain anchors to genesis founder ed25519:ab12345…)

State updated under /home/dan/.vigo-swarmgit/e3b0…
```

## `project re-found`

(ADR-019) Escape hatch when a founded project's delegation chain is at the cap (`DELEGATION_MAX_LEN = 5`) or has been tombstoned by a `--force-orphan-projects` rekey. Mints a fresh `project_id` from the current puddle pubkey, byte-migrates from the at-cap bare repo via the same `git fetch +refs/*:refs/*` path `reanchor` uses, and tombstones the OLD project on disk.

Differs from `reanchor`: `reanchor` is for surviving members taking over after the original founder puddle is *gone* (refuses if the calling user IS the current founder); `re-found` is for the founder themselves resetting their own project chain (REQUIRES that the calling user is the current founder).

```
vigo swarm swarmgit project re-found <project_id> [--force]
```

**Arguments:**

- `<project_id>` (or unique handle prefix) — the at-cap or tombstoned project to re-found.
- `--force` — re-found a project whose chain is below cap and which isn't tombstoned. Manual chain-hygiene reset; use sparingly.

**Preconditions:**

- Puddle must be unlocked (the verb signs a fresh bootstrap token via the session helper).
- Caller must be the project's current founder. The predicate uses `chain.last().new_founder_pubkey` (or `founder_puddle_pubkey` if there are no delegations).
- The founder must have rekeyed at least once since founding the project — otherwise the new project_id (`sha256(current_puddle_pubkey ‖ handle)`) collides with the old, and re-found refuses with a hint to run `puddle rekey-pair` first.
- A project under the proposed new project_id must not already exist locally.

**Interactive:** prints the consequences (old URL becomes dead, new URL must be communicated to members, etc.) and refuses to proceed unless the operator types the project handle exactly. Same anti-footgun pattern as `git push --force-with-lease`.

**Effects:**

- Mints a fresh founder bootstrap admin token under the calling user's CURRENT puddle identity (the freshly-rekeyed key, not the genesis pubkey baked into the OLD project_id).
- Persists `project.json` + `tokens.json` under `~/.vigo-swarmgit/<new_project_id>/`. The new members list starts as `[this_fleet]`; surviving members from the old project must be re-invited via a follow-up `project re-invite-all <new_project_id>`.
- When run as root **and** the old bare repo exists at `/var/lib/vigo/swarm/swarmgit/projects/<old_project_id>.git`, runs `git -C <new_repo> fetch <old_repo> +refs/*:refs/*` to copy every ref + the objects backing them into the new bare repo. Non-root invocations (or envoys that don't hold the old bare repo) print a note and rebuild only the local auth surface; another root member with the old bare repo can complete the migration later.
- Sets `project.json::tombstoned_at` on the OLD project. The bare repo + URL stay on disk as an archive, but the helper daemon refuses git operations and CLI verbs (`invite`, `revoke`, `re-invite-all`) skip the project. Drop the archive entirely via `project leave <old_project_id>` once the new project is healthy.

## See also

- [ADR-015 — Unified swarmgit substrate](../../adr/015-unified-swarmgit-substrate.md)
- [ADR-014 — Puddle primitive](../../adr/014-puddle-primitive.md)
- [ADR-018 — Puddle rekey](../../adr/018-puddle-rekey.md)
- [ADR-019 — Swarmgit founder-rekey via anchor-preserving delegation chains](../../adr/019-swarmgit-founder-rekey.md) — the design behind `re-invite-all`, `rejoin`, `re-found`, the delegation chain primitive, and authority windows. Shipped 0.35.18–0.35.22.
- [vigo swarm puddle CLI reference](agent-swarm-puddle.md)
- [Capability-token primitives — `tokens.rs`](../../engineering/subsystems/swarmgit.md)
