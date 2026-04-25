
# vigo cairn CLI Reference

`vigo cairn` is the user-facing inspection and recovery surface for [cairn](../../engineering/subsystems/cairn.md) — the personal-DR git mirroring subsystem. It runs on envoys, reads only local state, and works when the server is unreachable. That last property is load-bearing: the command you need when your source envoy died is exactly the command that has to work when the rest of the fleet might also be unavailable.

## Invocation

The command lives inside the `vigo` agent binary:

```
vigo cairn <verb> [args]
```

It does not require the daemon to be running and it makes no network calls. Every subcommand reads from files under `/var/lib/vigo/cairn/` and `/var/lib/vigo/swarm/`.

## Permission model

- Unprivileged users see only mirrors whose `owner` field matches their current username. `vigo cairn recover vigo-web` run by `alice` cannot operate on a mirror owned by `bob`.
- Root bypasses the owner filter and can list, show, and recover any mirror on the envoy. This is the operator recovery path (admin is rescuing someone else's work).
- The underlying bare repos at `/var/lib/vigo/cairn/mirrors/<seeder>/<user>/<project>.git` are root-owned mode `0755`, so any local user can `git clone` them directly if they know the path. The subcommand's filter is a UX layer on top of that: unprivileged users don't see mirrors they don't own listed in `vigo cairn list`, which is what they actually care about.

## Commands

| Verb | Purpose |
|------|---------|
| `list` | List mirrors materialized on this envoy |
| `show <project>` | Print details for one mirror |
| `log <project> [-n N]` | Show the last N commits of a mirror (default 20) |
| `branches <project>` | List branches and tags in a mirror |
| `peers <project>` | List swarm peers the local agent has seen holding this mirror's blob |
| `recover <project> [opts]` | Clone a mirror to a user-owned path |
| `recover-all [--dest <dir>]` | Clone every mirror owned by the caller |
| `prune [opts]` | Remove stale mirrors manually (dry-run by default, requires root with `--yes`) |
| `source add <path>` | Write a new `.cairn.yaml` sidecar in a repo |
| `source remove <project>` | Delete a sidecar |
| `source pause <project>` | Stop publishing (sets `paused: true`) |
| `source resume <project>` | Resume publishing |
| `source show <project>` | Print a sidecar + validation state |
| `source validate <path>` | Parse and validate a sidecar without writing |
| `source list` | List repos this envoy publishes as a source |

### Project name resolution

Commands that take `<project>` accept any of:

1. The full label: `cairn:<seeder>:<user>:<project>`
2. The project name alone (exact match): `vigo-web`
3. A unique prefix of the project name: `vigo-`

If the argument is ambiguous (e.g. the prefix matches multiple mirrors, or the same project is mirrored from two different seeders), the command prints the candidates and exits non-zero.

## `vigo cairn list`

Prints a table of every mirror materialized on this envoy and visible to the caller.

```
$ vigo cairn list
LABEL                                          BRANCH   LAST COMMIT  AGE        SIZE       OWNER
cairn:danlap:dan:vigo-web                   main     f43f6ef3eea2 3h ago     11.1 MiB   dan
cairn:danlap:dan:dotfiles                   main     a891c4b27e34 2d ago     340.0 KiB  dan
```

**Flags:**
- `--owner <user>` — filter to mirrors owned by a specific user
- `--seeder <host>` — filter to mirrors published by a specific source envoy
- `--json` — emit machine-readable JSON instead of a table

## `vigo cairn show <project>`

Prints a multi-line detail block for a single mirror: label, seeder, owner, bare repo path, size, branch, current HEAD sha + message + date, commit count, tags, first-commit date, last-materialized timestamp, blob sha. Useful before committing to a `recover`.

## `vigo cairn log <project> [-n N]`

Runs `git log -<N> --oneline` against the bare repo and prints the result. Default `-n 20`. This is the fastest way to verify "is my most recent work in here" without cloning.

## `vigo cairn branches <project>`

Lists both branches (`git branch -a`) and tags (`git tag -l`) in the mirror. Note that cairn always ships `HEAD` to receivers and normalizes the branch to `main` on arrival — so the branch listing is almost always just `main`. Tag preservation is full.

## `vigo cairn peers <project>`

Prints the peers the local agent has seen serving this mirror's blob, based on `/var/lib/vigo/swarm/peers.json` and the local manifest. **Pure in-process read** — no network probing. The output tells you "which other envoys in the fleet *have* this thing, as far as my last gossip round knows." If you need liveness confirmation, you'll learn it by trying to `recover` from a different envoy.

This is the most load-bearing command when your source envoy is gone and you're deciding which receiver to log into to do the recovery.

## `vigo cairn recover <project> [options]`

Clones the mirror's bare repo into a user-owned destination path. The clone runs as the calling user, so the resulting repository is owned by them and is immediately usable as a normal git repo (full history, working tree, `.git/` directory, writable).

```
$ vigo cairn recover vigo-web
✓ Recovered vigo-web to /home/dan/cairn/vigo-web
  cd /home/dan/cairn/vigo-web
```

**Default destination:** `~<user>/cairn/<project>/` for the calling user.

**Flags:**
- `--dest <path>` — override the default destination
- `--branch <name>` — rename `main` to `<name>` immediately after clone (for users who know their original source branch was `master`, `develop`, etc.)
- `--force` — overwrite an existing destination (see collision guard below)
- `--dry-run` — show what would happen without actually cloning

### Collision guard

If the default destination already exists and contains **both** `.git/` and `.cairn.yaml`, `vigo cairn recover` refuses to clone by default. The reason is safety: that pattern is the signature of an **active source repo** — the directory you're actively editing, publishing from, and committing into. The mirror is strictly older than the source (it's only updated on `git commit` in the source and then via swarm propagation), so recovering on top of the source would overwrite any uncommitted work with a stale snapshot.

The refusal message names the risk and offers two escape hatches:

```
error: /home/dan/cairn/vigo-web exists and looks like an active source repo
       (has .git/ and .cairn.yaml). Recovering here would overwrite it —
       and the mirror is older than your source, so uncommitted work would be
       lost.

       To recover to a different path:
         vigo cairn recover vigo-web --dest ~/vigo-web-rescue

       To overwrite anyway (only if the source is already broken):
         vigo cairn recover vigo-web --force
```

If the destination exists but does NOT look like an active source repo (e.g. plain non-empty directory with no `.git/`), the refusal is a generic "path is not empty" with a suggestion to use `--dest` or `--force`.

If the destination is missing, the command proceeds without any prompt.

## `vigo cairn recover-all [--dest <dir>]`

Clones every mirror owned by the calling user into a common parent directory. Default `--dest` is `~<user>/cairn/`. Each mirror lands at `<dest>/<project>/`. Failures on individual mirrors are logged but do NOT abort the run — the command continues and prints a per-mirror summary at the end. Exit status is non-zero if any mirror failed.

This is the "my laptop died, give me everything" command. Run it on any receiver after you've ssh'd in.

## `vigo cairn prune [options] [<label>]`

Remove stale mirrors from this receiver. **Dry-run by default** — a bare invocation prints the candidate list and exits without touching anything. You must pass `--yes` or name a specific mirror to actually delete.

```
$ vigo cairn prune
LABEL                                                   AGE          SIZE         OWNER
cairn:decommissioned-laptop:dan:old-project          45d ago      12.3 MiB     dan
cairn:decommissioned-laptop:dan:notes                47d ago      450.0 KiB    dan

dry run — 2 mirror(s) would be removed. re-invoke with --yes to apply.
```

**Flags:**
- `--older-than <duration>` — override the 30-day default threshold. Accepts `30d`, `60d`, `90d`, or a bare integer (interpreted as days). Hours, weeks, and months are not supported — the staleness use case is coarse-grained.
- `--yes` — actually remove the candidates (default is dry-run).
- `--dry-run` — explicit opt-in to dry-run behavior. Same as omitting `--yes`.
- `<label>` as a positional — target a single mirror by full label, exact project name, or unique project-name prefix. When set, the staleness filter is **bypassed**: the operator is explicitly naming a mirror and the command will prune it regardless of age.

**Permissions:**

- **Dry-run** (no `--yes`) works for any user. The caller sees only their own stale mirrors unless running as root (same scoping as `repo list`).
- **Actual removal** (`--yes`) requires root. The bare repos under `/var/lib/vigo/cairn/mirrors/<seeder>/<user>/<project>.git` are root-owned, so an unprivileged user cannot delete them even if they own the logical mirror. `prune --yes` run as a non-root user errors out with a clear message.

**Staleness threshold:** 30 days by default, matching the log warning and `cairn.stale_mirrors` trait surfaces. A mirror is stale if `now - last_refreshed > threshold`. The threshold is exclusive, so a mirror exactly at 30 days is not yet stale.

**Safety by design:**
- Default is list-only. Destructive action requires explicit `--yes` or a named target.
- Staleness is advisory — prune is the ONLY removal path. The reactor never auto-evicts, so a backup from a dead source envoy sits there indefinitely until an operator decides to free the space.
- When targeting a specific mirror by label/project, staleness is ignored — useful for the "this machine is gone and I know I don't need this mirror anymore" case, even if the mirror was refreshed recently (e.g. right before the source envoy died).

**Examples:**

```
# See what's stale without removing anything.
vigo cairn prune

# Same thing, explicit about intent.
vigo cairn prune --dry-run

# Remove everything stale per the default 30-day threshold.
sudo vigo cairn prune --yes

# Tighter threshold: 60 days.
sudo vigo cairn prune --older-than 60d --yes

# Remove exactly one mirror by project name, regardless of age.
sudo vigo cairn prune vigo-web
```

**Exit status:** 0 on success (including the dry-run case with no candidates). 1 if any bare-repo removal failed — the state file is still persisted with the successful removals, so re-running the command will retry only the failures.

## `vigo cairn source list`

Lists repos this envoy is publishing as a source. Reads from `/var/lib/vigo/cairn/traits.json` (updated by the reactor on every 5-second tick), so the output is at most 5 seconds stale.

```
$ vigo cairn source list
PROJECT         OWNER      MIRRORS  PAUSED   FILTERS                        STATUS
vigo-web        dan        5        no       exclude=raspberry*             publishing
dotfiles        dan        3        no       (none)                         publishing
notes           dan        3        yes      (none)                         paused
broken          dan        5        no       (none)                         error: sidecar parse failed at line 4
```

Useful when debugging "why isn't my new repo syncing?" — the status column surfaces sidecar parse errors, paused state, and publish failures in one table without needing to dig through server-side trait reports.

## `vigo cairn source add <path> [opts]`

Creates a new `.cairn.yaml` sidecar in the repo at `<path>` and enrolls it for cairn mirroring. The CLI is the supported authoring path — users don't hand-edit `.cairn.yaml` unless they want to.

```
$ vigo cairn source add ~/cairn/notes --tags personal --max-mirrors 3
✓ wrote /home/dan/cairn/notes/.cairn.yaml
✓ appended .cairn.yaml to /home/dan/cairn/notes/.gitignore
  reactor picks up new sources within 5 seconds — run `vigo cairn source list` to confirm.
```

**Flags:**
- `--owner <hostname>` — override the owner field (default: local hostname from `/etc/hostname`)
- `--max-mirrors <N>` — how many receivers to pick (default 5, clamped to the agent's `MIN_MIRRORS`..=`MAX_MIRRORS` range)
- `--tags a,b,c` — comma-separated tag list
- `--include <pattern>` — hostname glob to include; repeatable
- `--exclude <pattern>` — hostname glob to exclude; repeatable
- `--cross-subnet h1,h2` — explicit cross-subnet mirror hostnames (comma-separated)
- `--description "text"` — human-readable description (≤ 256 chars)
- `--paused` — create the sidecar already paused
- `--no-gitignore` — don't touch `.gitignore` (default: append `.cairn.yaml`)
- `--yes` / `-y` — skip confirmation prompts

**Defaults and hygiene:**
- `<path>` must point at an existing directory containing `.git/`. The CLI refuses otherwise.
- If a `.cairn.yaml` already exists, the CLI refuses and suggests `source show` or `source remove` + re-run.
- By default, `.cairn.yaml` is appended to the repo's `.gitignore` so a personal sidecar doesn't leak into an upstream PR. Use `--no-gitignore` when the sidecar should ride along with clones (personal repos across your own envoys).
- Never commits on the user's behalf. Tracking the sidecar is the user's explicit next action.

## `vigo cairn source remove <project>`

Deletes the sidecar for a project. Prompts unless `--yes`. Does NOT purge receiver-side mirrors on other envoys — those persist until each envoy's own `vigo cairn prune` runs.

```
$ vigo cairn source remove notes
Delete /home/dan/cairn/notes/.cairn.yaml? The project's local mirror on peers is NOT removed. [y/N] y
✓ removed /home/dan/cairn/notes/.cairn.yaml
  reactor stops publishing on next tick. Receiver mirrors on other envoys persist until their own `vigo cairn prune` runs.
```

## `vigo cairn source pause <project>` / `resume <project>`

Flip the `paused` field in the sidecar. Paused sources stop publishing new bundles on the next reactor tick; resuming picks publishing back up.

```
$ vigo cairn source pause notes
✓ paused /home/dan/cairn/notes/.cairn.yaml
```

## `vigo cairn source show <project>`

Prints the sidecar contents in a readable format, plus a validation pass — any parse errors surface here instead of waiting for the next reactor tick.

```
$ vigo cairn source show notes
project:     notes
path:        /home/dan/cairn/notes
sidecar:     /home/dan/cairn/notes/.cairn.yaml
owner:       danlap
max_mirrors: 3
paused:      false
tags:        personal
```

## `vigo cairn source validate <path>`

Dry-run a sidecar — parse and validate it without writing. Takes a path to the sidecar file, not a project. Useful for templating or CI flows.

```
$ vigo cairn source validate ./.cairn.yaml
✓ ./.cairn.yaml parses and validates (owner=danlap, max_mirrors=3, paused=false)
```

### Project name resolution for source verbs

`remove`, `pause`, `resume`, and `show` accept either:
- A bare project name (resolved under the invoking user's `~/cairn/<name>/`)
- An absolute or relative path to the project directory

Example: `vigo cairn source pause notes` and `vigo cairn source pause /home/dan/cairn/notes` are equivalent when run by user `dan`.

## Exit codes

| Code | Meaning |
|------|---------|
| `0` | Success |
| `1` | Error (bad arguments, no matching mirror, git command failed, collision guard triggered without `--force`, etc.) |
