
# vigorepo CLI Reference

`vigorepo` is the user-facing inspection and recovery surface for [vigorepo](../../concepts/vigorepo.md) — the personal-DR git mirroring subsystem. It runs on envoys, reads only local state, and works when the server is unreachable. That last property is load-bearing: the command you need when your source envoy died is exactly the command that has to work when the rest of the fleet might also be unavailable.

## Invocation

The command lives inside the `vigo` agent binary. It can be invoked two equivalent ways:

```
vigo vigorepo <verb> [args]     # full form
vigorepo <verb> [args]          # via /usr/local/bin/vigorepo shim symlink
```

The shim symlink is installed at `/usr/local/bin/vigorepo` as part of the agent's systemd service install. It points at `/usr/local/sbin/vigo`; the agent detects `argv[0]` at startup and dispatches to the `vigorepo` subcommand tree when invoked under the shim name.

## Permission model

- Unprivileged users see only mirrors whose `owner` field matches their current username. `vigorepo recover vigo-web` run by `alice` cannot operate on a mirror owned by `bob`.
- Root bypasses the owner filter and can list, show, and recover any mirror on the envoy. This is the operator recovery path (admin is rescuing someone else's work).
- The underlying bare repos at `/var/lib/vigo/vigorepo/mirrors/<seeder>/<user>/<project>.git` are root-owned mode `0755`, so any local user can `git clone` them directly if they know the path. The subcommand's filter is a UX layer on top of that: unprivileged users don't see mirrors they don't own listed in `repo list`, which is what they actually care about.

## Commands

| Verb | Purpose |
|------|---------|
| `repo list` | List mirrors materialized on this envoy |
| `repo show <project>` | Print details for one mirror |
| `repo log <project> [-n N]` | Show the last N commits of a mirror (default 20) |
| `repo branches <project>` | List branches and tags in a mirror |
| `repo peers <project>` | List swarm peers the local agent has seen holding this mirror's blob |
| `recover <project> [opts]` | Clone a mirror to a user-owned path |
| `recover-all [--dest <dir>]` | Clone every mirror owned by the caller |
| `source list` | List repos this envoy publishes as a source |

### Project name resolution

Commands that take `<project>` accept any of:

1. The full label: `vigorepo:<seeder>:<user>:<project>`
2. The project name alone (exact match): `vigo-web`
3. A unique prefix of the project name: `vigo-`

If the argument is ambiguous (e.g. the prefix matches multiple mirrors, or the same project is mirrored from two different seeders), the command prints the candidates and exits non-zero.

## `vigorepo repo list`

Prints a table of every mirror materialized on this envoy and visible to the caller.

```
$ vigorepo repo list
LABEL                                          BRANCH   LAST COMMIT  AGE        SIZE       OWNER
vigorepo:danlap:dan:vigo-web                   main     f43f6ef3eea2 3h ago     11.1 MiB   dan
vigorepo:danlap:dan:dotfiles                   main     a891c4b27e34 2d ago     340.0 KiB  dan
```

**Flags:**
- `--owner <user>` — filter to mirrors owned by a specific user
- `--seeder <host>` — filter to mirrors published by a specific source envoy
- `--json` — emit machine-readable JSON instead of a table

## `vigorepo repo show <project>`

Prints a multi-line detail block for a single mirror: label, seeder, owner, bare repo path, size, branch, current HEAD sha + message + date, commit count, tags, first-commit date, last-materialized timestamp, blob sha. Useful before committing to a `recover`.

## `vigorepo repo log <project> [-n N]`

Runs `git log -<N> --oneline` against the bare repo and prints the result. Default `-n 20`. This is the fastest way to verify "is my most recent work in here" without cloning.

## `vigorepo repo branches <project>`

Lists both branches (`git branch -a`) and tags (`git tag -l`) in the mirror. Note that vigorepo always ships `HEAD` to receivers and normalizes the branch to `main` on arrival — so the branch listing is almost always just `main`. Tag preservation is full.

## `vigorepo repo peers <project>`

Prints the peers the local agent has seen serving this mirror's blob, based on `/var/lib/vigo/swarm/peers.json` and the local manifest. **Pure in-process read** — no network probing. The output tells you "which other envoys in the fleet *have* this thing, as far as my last gossip round knows." If you need liveness confirmation, you'll learn it by trying to `recover` from a different envoy.

This is the most load-bearing command when your source envoy is gone and you're deciding which receiver to log into to do the recovery.

## `vigorepo recover <project> [options]`

Clones the mirror's bare repo into a user-owned destination path. The clone runs as the calling user, so the resulting repository is owned by them and is immediately usable as a normal git repo (full history, working tree, `.git/` directory, writable).

```
$ vigorepo recover vigo-web
✓ Recovered vigo-web to /home/dan/vigorepo/vigo-web
  cd /home/dan/vigorepo/vigo-web
```

**Default destination:** `~<user>/vigorepo/<project>/` for the calling user.

**Flags:**
- `--dest <path>` — override the default destination
- `--branch <name>` — rename `main` to `<name>` immediately after clone (for users who know their original source branch was `master`, `develop`, etc.)
- `--force` — overwrite an existing destination (see collision guard below)
- `--dry-run` — show what would happen without actually cloning

### Collision guard

If the default destination already exists and contains **both** `.git/` and `.vigorepo.yaml`, vigorepo refuses to clone by default. The reason is safety: that pattern is the signature of an **active source repo** — the directory you're actively editing, publishing from, and committing into. The mirror is strictly older than the source (it's only updated on `git commit` in the source and then via swarm propagation), so recovering on top of the source would overwrite any uncommitted work with a stale snapshot.

The refusal message names the risk and offers two escape hatches:

```
error: /home/dan/vigorepo/vigo-web exists and looks like an active source repo
       (has .git/ and .vigorepo.yaml). Recovering here would overwrite it —
       and the mirror is older than your source, so uncommitted work would be
       lost.

       To recover to a different path:
         vigorepo recover vigo-web --dest ~/vigo-web-rescue

       To overwrite anyway (only if the source is already broken):
         vigorepo recover vigo-web --force
```

If the destination exists but does NOT look like an active source repo (e.g. plain non-empty directory with no `.git/`), the refusal is a generic "path is not empty" with a suggestion to use `--dest` or `--force`.

If the destination is missing, the command proceeds without any prompt.

## `vigorepo recover-all [--dest <dir>]`

Clones every mirror owned by the calling user into a common parent directory. Default `--dest` is `~<user>/vigorepo/`. Each mirror lands at `<dest>/<project>/`. Failures on individual mirrors are logged but do NOT abort the run — the command continues and prints a per-mirror summary at the end. Exit status is non-zero if any mirror failed.

This is the "my laptop died, give me everything" command. Run it on any receiver after you've ssh'd in.

## `vigorepo source list`

Lists repos this envoy is publishing as a source. Reads from `/var/lib/vigo/vigorepo/traits.json` (updated by the reactor on every 5-second tick), so the output is at most 5 seconds stale.

```
$ vigorepo source list
PROJECT         OWNER      MIRRORS  PAUSED   FILTERS                        STATUS
vigo-web        dan        5        no       exclude=raspberry*             publishing
dotfiles        dan        3        no       (none)                         publishing
notes           dan        3        yes      (none)                         paused
broken          dan        5        no       (none)                         error: sidecar parse failed at line 4
```

Useful when debugging "why isn't my new repo syncing?" — the status column surfaces sidecar parse errors, paused state, and publish failures in one table without needing to dig through server-side trait reports.

Other `source` verbs (`source show`, `source scan`) are not yet implemented and will return a "not yet implemented — see `vigorepo source list`" message if invoked.

## Exit codes

| Code | Meaning |
|------|---------|
| `0` | Success |
| `1` | Error (bad arguments, no matching mirror, git command failed, collision guard triggered without `--force`, etc.) |

`recover-all` uses exit code `1` when any individual mirror failed; the partial successes still print.

## See also

- [vigorepo concept](../../concepts/vigorepo.md) — the subsystem overview
- [vigorepo traits](../traits/vigorepo.md) — the trait fields the reactor emits
- [agent CLI](agent.md) — the `vigo` agent binary this command is housed in

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
