
# User Account Management

User accounts are the most complicated resource type vigo manages. This page explains why, and why that complexity shapes how stockpiles should organize user-management modules.

## Why the `user` executor is exceptional

The `user` executor (see [reference/executors/user.md](../reference/executors/user.md)) stands apart from every other executor in the tree in five ways. No other executor combines all of these.

### 1. Multi-file state

Most executors touch one file or one daemon. `file:` edits one file. `service:` talks to systemd or launchd. `package:` calls the distro package manager. The `user` executor reads and writes across a half-dozen independent state locations:

- `/etc/passwd` — existence, UID, GID, home, shell, GECOS
- `/etc/shadow` — password hash, password aging
- `/etc/group` — supplementary group membership
- `~<user>/.ssh/authorized_keys` — SSH access, with ownership fixup and symlink detection
- `~<user>/.xsession` — desktop session selection for RDP access
- `/etc/sudoers.d/<username>` — privilege escalation policy

Every convergence cycle, the executor reads all six to compute drift, and writes only the subset that needs to change. A single user resource can touch every one of those files in a single run.

### 2. List fields with merge-vs-replace semantics

`groups:` on a user resource is the only list-valued attribute in vigo that has **both** merge and replace semantics, toggled by a flag:

- **Default (merge):** `useradd -aG` on create, then selective add/remove on update. A user already in `wheel` outside vigo's knowledge stays in `wheel`. The resource adds missing groups from the list and otherwise leaves memberships alone.
- **With `purge_groups: true` (replace):** `useradd -G` / `usermod -G`. Vigo becomes authoritative — anything not in the list is removed.

No other executor has this toggle. `file:` always replaces. `package:` state is binary. Only `user` treats a list attribute as something that can be either "these are the groups we care about" or "these are the only groups."

`authorized_keys:` has a similar split: Vigo-managed keys are replaced on every run, but ephemeral Scrier remote-access keys are preserved across converges so an active Scrier session can't be terminated by a convergence run. Again, unique to `user`.

### 3. Process-killing recovery

If `usermod` fails with "user is currently used by process," the executor runs `pkill -u <username>` and retries. This is the only executor that kills processes on the host as part of its normal idempotency logic. The reason is that user modifications on a logged-in account fail deterministically — you either skip the change (accepting drift) or evict the user's running processes. The executor picks the latter because "the resource didn't converge" is worse than "that user got logged out."

### 4. Desktop session mapping and system UID auto-assignment

- **Desktop mapping:** `desktop: cinnamon` writes `~<user>/.xsession` with the correct exec command for cinnamon, and similarly for gnome, xfce, mate, plasma, lxde, lxqt, fvwm, i3, sway, openbox, budgie. This knowledge lives inside the executor because no other vigo concept needs it.
- **System UID auto-assignment:** `system: true` with a user already holding UID ≥ 1000 finds an available UID in the 100–999 range and reassigns. No other executor reorganizes kernel-level identifiers.

### 5. Cross-platform divergence

`type: user` maps to four different implementations:

- `user` (Linux)
- `user_macos`
- `user_freebsd`
- `user_windows` (which manages SIDs, local groups, RDP-User group membership, and very different sudoers equivalents)

The behavior is consistent enough that you can write one resource and have it apply everywhere, but the implementations under the hood are fully separate. This is true for `file:` and `service:` to a lesser extent, but `user`'s platform branches diverge the most.

## What that complexity implies for organization

All of the above is intrinsic to user management — you can't simplify the executor without giving up capability. But the complexity has organizational consequences that shape how you should lay out user-management modules in a stockpile.

### User accounts vary per envoy more than any other resource

A given user typically needs a slightly different configuration on every envoy they touch. The shell might be `/bin/fish` on a workstation and `/bin/bash` everywhere else. Supplementary groups might include `virtualbox` and `kvm` on workstations but `plex` on mediaservers. `sudo_nopasswd` might be `true` on trusted hosts and `false` in production. Keys might rotate. Desktop sessions apply on some boxes and not others.

By contrast, `package:` and `file:` resources tend to be uniform fleet-wide — nginx is nginx, the same `nginx.conf` template renders everywhere. Per-host variation is the exception, not the rule. For users it's the rule.

### Users have a lifecycle beyond convergence

- **Joiners:** new account, new home dir, new initial keys, specific per-host placement.
- **Rotators:** SSH key rotation, password changes, group changes over time as responsibilities shift.
- **Retirees:** `state: absent`, `userdel -r`, audit trail of when and why.

No other resource type has this lifecycle with the same level of audit relevance. A package install doesn't need per-user git blame. A user account does — "when did dan get sudo access on plex.home?" is a real question for compliance audits.

### Audit trail per person

Compliance frameworks (CIS, NIST 800-53, PCI-DSS, SOC 2, HIPAA) all have controls that require per-user auditing of account existence, privileges, credentials, and retirement. Keeping user resources grouped by person (rather than scattered across general-purpose modules) makes that audit trivial: `git log stockpile/usercrates/alice.vgo` answers every question about alice's fleet-wide account history.

## The recommended organizational pattern

From those three properties — heavy per-envoy variation, long lifecycle, per-person audit — a clear organizational shape falls out. Per-user config files are called **usercrates** (see [Glossary](../reference/glossary.md#usercrate)). They live in `stockpile/usercrates/` and auto-apply to every envoy in that directory's scope — no need to list them in `common.vgo`.

```
stockpile/usercrates/
  dan.vgo              # one file per human operator
  alice.vgo
  bob.vgo
  service-accounts.vgo # one file per category for non-humans (nginx, postgres, etc.)
  retired/             # moved here during retirement, see "Retirement" below
    ex-employee.vgo    # state: absent, kept for audit history
```

For per-site scoping (MSP-style multi-customer stockpiles), nest usercrates under the relevant directory — `stockpile/customerA/usercrates/alice.vgo` applies only to envoys matched under `customerA/`.

Each usercrate is a single-resource module:

```yaml
# stockpile/usercrates/dan.vgo
name: users/dan

resources:
  - name: dan
    type: user
    username: dan
    comment: Daniel Alexander
    # Per-envoy variation via lookup tables:
    shell:
      danlap:  /bin/fish
      default: /bin/bash
    groups:
      tag:workstation:  [docker, virtualbox, kvm]
      tag:mediaserver:  [docker, plex]
      default:          [docker]
    sudo_nopasswd:
      tag:workstation: "true"
      default:         "false"
    password: secret:vigo/os-users/dan
    authorized_keys: |
      ssh-ed25519 AAAA... dan@workstation
```

Compliance claims common to every usercrate (CIS 5.4.1, NIST AC-3, PCI 8.1.1, SOC 2 CC6.1, HIPAA 164.312…) are inherited from a `compliance.vgo` file in the same directory, keeping individual usercrates focused on each user's actual attributes.

### Why this pattern, specifically

- **One file per human, per category for non-humans.** Humans need per-user git blame and audit trail. A 20-person `humans.vgo` makes that messy. Service accounts, by contrast, are typically interchangeable ("the nginx account, the postgres account, the deploy bot") and benefit from being grouped so common attributes are visible together.
- **Auto-apply within directory scope.** Drop a file in `stockpile/usercrates/` and it applies fleet-wide. Drop it in `stockpile/customerA/usercrates/` and it scopes to customerA envoys only. No explicit listing in `common.vgo` required — the filesystem path IS the scope. Per-envoy exclusion via `exclude_usercrates: [alice]` on a match block.
- **Lookup tables handle per-host variation inside the usercrate.** See [Lookup Tables](lookups.md). The `shell:`, `groups:`, and `sudo_nopasswd:` fields all accept a tag/host/trait-keyed map, which removes the need for separate usercrates per envoy profile.
- **Directory-level compliance claims avoid duplication.** The compliance block that would otherwise be copy-pasted into every usercrate lives once in `stockpile/usercrates/compliance.vgo` and flows down. See [config-format.md](config-format.md#compliance-waivers--claims).
- **Retirement convention is two-phase.** When someone leaves, edit the resource to `state: absent`, move the file to `stockpile/usercrates/retired/<name>.vgo`, commit, publish, wait at least one full convergence cycle so every envoy runs `userdel`. Only then is it safe to delete the file. The `retired/` subdirectory is a historical roster — if you ever need to answer "did we provision `ex-employee` on this envoy between X and Y," git history plus the retained file gives you a complete answer.

### What NOT to do

- **Don't put user resources in general-purpose modules.** `modules/base.vgo` is the wrong home for a user resource. Git blame becomes useless, and per-person audit is impossible.
- **Don't split one user across multiple files.** The single-file-per-user boundary is load-bearing for audit. Putting dan's SSH keys in one file and dan's sudo config in another makes the history noisier without any corresponding benefit.
- **Don't use a general-purpose `modules: [users/dan]` override in every match block.** Put it in `common.vgo` once. Exceptions go in match blocks via `exclude_modules:`.
- **Don't skip the retirement two-phase.** Deleting a user file without first converting to `state: absent` leaves the account in place on every envoy the user ever touched. The loader's module-retraction mechanism tells agents to stop applying the module, but it does not run `userdel` — the resource must be in the config with `state: absent` at least once for the agent to run the deletion.

## Summary

User management looks deceptively similar to other vigo resource management on paper — it's just another resource type — but the executor is fundamentally more complicated and the organizational needs are fundamentally different. The recommended pattern (per-user files under `modules/users/`, directory-level compliance inheritance, lookup-table per-host variation, two-phase retirement) falls out directly from those needs. Follow it and user management stays auditable, per-host variation stays readable, and retirement stays clean.

## Related

- [reference/executors/user.md](../reference/executors/user.md) — parameter reference, examples, idempotency details
- [Lookup Tables](lookups.md) — per-envoy resource field variation via tag/host/trait keys
- [Config Format](config-format.md#compliance-waivers--claims) — directory-level compliance claim inheritance
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
