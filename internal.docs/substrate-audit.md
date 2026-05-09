# Substrate Capability Audit — cairn + sigil

*Locked in 2026-04-24. Basis for unifying lifecycle + authoring operations across Vigo's user-scoped P2P substrates before any one-off feature lands.*

## 2026-05-04 — Phase C status update

The substrates have since been renamed: **cairn → swarmgit, sigil → lockbox** (refactor at 0.31.0). The matrix below uses the original names; substitute as you read.

Phase C closed gaps #3, #4, and #5 from "Gaps prioritized" below:

- **#3 Cairn local `status`** → shipped 0.38.8 as `vigo swarm swarmgit status` (top-level, parallel to `vigo swarm lockbox status`). Reads `state.json` + `traits.json` and surfaces helper-daemon socket presence.
- **#5 Full-wipe purge for both** → shipped 0.38.9 as `vigo swarm swarmgit purge --yes` (mode-aware: user-only vs. root-also-envoy-wide) and `vigo swarm lockbox purge --yes` (per-user). Distinct from `leave --purge` — purge is the no-gossip path.
- **#4 Members view for both** → shipped 0.38.10. Lockbox's `vigocli swarm lockbox members` already printed a tabwriter table; swarmgit gained a parallel `vigocli swarm swarmgit members [project|handle]`.

**Web UI buttons for leave/evict** (originally a fourth Phase C bullet in `pending.md`) was descoped per Dan's "swarm UI is observability-only — action verbs stay CLI" direction. The capability gaps it would have closed remain CLI-only by design.

Phase D (retire-hook) is still blocked on `vigocli envoys retire` not existing — gap #6 below.

---

## Scope

Vigo has four P2P substrates on the swarm transport. Only two are in scope for this audit:

- **cairn** — personal-DR git mirroring. Per-user membership (`.cairn.yaml` sidecars under `~<user>/cairn/<project>/`). Real state: peer tables, selected mirrors, receiver bare repos.
- **sigil** — encrypted per-user directory sync. Per-user membership via wrapped age identity. Real state: recipients.txt, peer tables, tombstones.

Excluded:

- **echo** — presence-based membership only (`mkdir ~/echo/` = opt in). Zero state beyond the folder itself. Adding lifecycle verbs would break its deliberate "dead simple, user's home dir" invariant. Verified in `agent/src/echo/mod.rs:190` — "Skip users without a ~/echo/ directory."
- **lode** — admin-scoped blob distribution (server → hosts via matchfile opt-in). Different shape entirely; no per-user membership.

## Legend

- ✓ exists · ✗ gap (should exist, missing) · ◐ partial · n/a shape-different by design

## Matrix

### Per-envoy local state
| Op | cairn | sigil |
|---|---|---|
| Status (overview) | ✗ no `status` verb | ✓ `vigo sigil status` |
| List local participation | ✓ `vigo cairn list` (receiver mirrors) | ✓ `vigo sigil list` (ciphertext files) |
| Show one item | ✓ `vigo cairn show <project>` | ✗ no per-file show |
| Purge local state | ◐ `vigo cairn prune` (stale-GC only) | ✗ manual `rm -rf ~/.sigil/` |

### Self-action lifecycle
| Op | cairn | sigil |
|---|---|---|
| Enroll this envoy | n/a (auto via matchfile `cairn: true`) | ✓ `vigo sigil enroll` (or `enroll.pending`) |
| Leave this envoy (self-unenroll) | ✗ today: flip matchfile, no self-verb | ✗ today: manual `rm -rf ~/.sigil/` + peers must `forget` |

### Source / target authoring
| Op | cairn | sigil |
|---|---|---|
| Add an enrollment target | ✗ hand-write `.cairn.yaml` sidecar | n/a (one circle per user, no sub-targets) |
| Remove a target | ✗ manual `rm .cairn.yaml` | n/a |
| Pause / resume | ✗ no pause concept | n/a |
| Show config | ✗ (docs promise `source show`, not implemented) | n/a |
| Validate config (dry-run) | ✗ | n/a |
| List what's authored here | ✓ `vigo cairn source list` | n/a |

### Admin visibility (vigocli, read-only)
| Op | cairn | sigil |
|---|---|---|
| Fleet status / list / show | ✓ all three | ✓ all three |
| Members view (who's in the mesh) | ◐ implicit in show pages | ✗ no dedicated members view |

### Admin control (vigocli)
| Op | cairn | sigil |
|---|---|---|
| Evict a host | ✗ | ✗ |
| Retire-hook | ✗ (no `vigocli envoys retire` exists) | ✗ (same prereq) |

### Content / crypto session (shape-different by design)
| Op | cairn | sigil |
|---|---|---|
| Recover content to working copy | ✓ `vigo cairn recover <project>` | n/a (decrypt serves this role) |
| Unlock / lock crypto session | n/a | ✓ `vigo sigil unlock` / `lock` |
| Encrypt / decrypt / delete | n/a | ✓ verbs exist |
| Rotate recipients | n/a | ✓ `vigo sigil reencrypt` |
| Peers debug | ✓ `vigo cairn peers <project>` | ✗ no per-user peers dump |

### Peer-table surgery
| Op | cairn | sigil |
|---|---|---|
| Forget a peer locally | n/a (any seeder is valid; no recipient pubkey to drop) | ✓ `vigo sigil forget <hostname>` (local-only today) |

## Gaps prioritized

1. **Cairn source authoring CLI** — `vigo cairn source {add,remove,pause,resume,show,validate}`. The CLI becomes the sole authoring surface; `.cairn.yaml` stays as the on-disk storage format (portable across clones, reactor-readable) but users never hand-edit it. Standalone cairn work; no cross-substrate coordination needed.
2. **Self-leave + admin evict (symmetric design across cairn + sigil)** — `vigo <sub> leave` + `vigocli <sub> evict --host <h>`. Requires unified vocabulary decision; shape differs per substrate but verb names and vigocli flag shapes should match.
3. **Cairn local `status`** — small parallel to `vigo sigil status`.
4. **Members view for both** — `vigocli <sub> members`.
5. **Full-wipe purge for both** — `vigo <sub> purge` (distinct from cairn's GC `prune`).
6. **Retire-hook** — blocked on `vigocli envoys retire` not existing; separate prerequisite work.

## Design principle — respect shape differences

Not every op should exist on every substrate. Shape-differences (marked `n/a` above) are legitimate design choices, not gaps:
- Sigil has no source-authoring because there are no sub-targets — one user, one circle.
- Cairn has no reencrypt because there are no cryptographic recipients.
- Echo has no lifecycle verbs because filesystem presence is the entire membership model.

Unify vocabulary (verb names, flag shapes, output formats) where the op semantically applies. Do not force parallelism where the substrate's design doesn't need it.

## Supersedes

- `~/.claude/plans/bubbly-wibbling-metcalfe.md` — original sigil-only leave/evict plan. Replaced by the unified work described in the companion plan.
