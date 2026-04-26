# Substrate live-test index

The substrate (P2P swarm: peers + manifest gossip + lode + echo + cairn + sigil) ships with a multi-phase live-test harness that runs against a real fleet rather than mocked unit tests. Each phase is an independent bash script driving SSH + sudo into the home-lab envoys and asserting observable end-states.

| Phase | Script | Coverage |
|---|---|---|
| **phase-b** | [`substrate-phase-b-livetest.sh`](substrate-phase-b-livetest.sh) | Cairn source CLI verbs (Phase A); cairn + sigil `leave` / admin `evict` (Phase B). Sections 4–7 need a sigil password. |
| **phase-c** | [`substrate-phase-c-livetest.sh`](substrate-phase-c-livetest.sh) | Swarm health (peers.json + manifest gossip cross-fleet); lode distribute/list/revoke; echo round-trip with modify + delete. |
| **phase-d** | [`substrate-phase-d-livetest.sh`](substrate-phase-d-livetest.sh) | Sigil encrypt + push fan-out perms (`<user>:<group> 0600`); decrypt `-o` perms (gated); leaves-table + announcer log smoke. |

Run all three at once: [`substrate-livetest-all.sh`](substrate-livetest-all.sh) — runs each phase in sequence, aggregates pass/fail, continues on per-phase failure.

## Common assumptions

All phases share these prerequisites:

- **Test driver runs as the sigil user.** The driver's SSH and sudo creds are used. Default `SIGIL_USER=dan` per the home-lab.
- **Passwordless SSH** to PRIMARY/SECONDARY/THIRD from the driver host.
- **Sudo cached locally** before invocation: `sudo -v && ./substrate-livetest-all.sh`.
- **Sigil enrolled** for `SIGIL_USER` on at least PRIMARY (phase-d strictly requires it; phase-b §4–7 need it on SECONDARY too).
- **Default fleet shape**: PRIMARY=danlap, SECONDARY=annlap, THIRD=plex. Override via `PRIMARY=…` env when running.

## Sudo HOME-stripping caveat

Ubuntu's default `sudo` config resets `HOME=/root` and `USER=root` (preserving only `SUDO_USER`). The agent binary at `/usr/local/sbin/vigo` was originally HOME-first for resolving `~/.sigil/`, so direct `sudo /usr/local/sbin/vigo sigil <verb>` invocations fail with "not enrolled" against pre-0.30.26 binaries. **Phase-d** mitigates this with a `VIGO_INVOKE="sudo env HOME=$SIGIL_HOME LOGNAME=$SIGIL_USER USER=$SIGIL_USER $VIGO_BIN"` wrapper so the test works against pre-fix and post-fix binaries equally. **Phase-b** uses the same pattern in §4–7. Post-0.30.26 the resolution chain is `getuid() → SUDO_USER → USER`, so the wrapper becomes belt-and-suspenders.

## Phase-specific knobs

### phase-b

- `SIGIL_PASSWORD_FILE` — default `~/.sigil-test-password`, mode 0600, single line. Required for §4–7 (sigil leave/evict). Sections 1–3 (cairn surface) run regardless.
- `ABSENT` — default `girlslaptop`. Hostname expected to be unreachable; tests evict-against-offline-host gracefully.

### phase-c

- `LODE_TIMEOUT` — default 90s. How long to wait for lode distribution to converge across test envoys.
- `ECHO_TIMEOUT` — default 30s. Per-direction echo propagation timeout.

### phase-d

- `PRE_DECRYPT_UNLOCK` — default 0. Set to 1 (after running `vigo sigil unlock` once on PRIMARY) to enable §3, the decrypt `-o` perms assertion. Without this the test can't drive the password prompt automatically.
- `FANOUT_TIMEOUT` — default 30s.

## Coverage map (what's tested where)

| Subsystem | Phase | Notes |
|---|---|---|
| Swarm peer table (peers.json) | c §1 | Cross-fleet IP-based assertions. |
| Swarm manifest gossip | c §1 | Entry-count agreement check. |
| Lode distribute / status / revoke | c §2 | End-to-end with materialization checks. |
| Echo write / sync / modify / delete | c §3 | Round-trip across two envoys. |
| Cairn source CLI (`vigo cairn source …`) | b §1 | Add / remove / pause / resume / show / validate. |
| Cairn `leave` (default + `--pause` + `--purge`) | b §2 | Self-leave per envoy. |
| Cairn admin evict (`vigocli cairn evict`) | b §3 | Server-driven host removal. |
| Sigil `leave` (default + envelope propagation + `--purge`) | b §4, b §6 | Self-leave + LEAVE envelope. |
| Sigil admin evict (`vigocli sigil evict`) | b §5 | Server-driven user removal. |
| Sigil DELETE tombstone propagation | b §7 | Regression for delete path. |
| Sigil encrypt perms (local) | d §1 | `<user>:<group> 0600` post-encrypt. |
| Sigil push fan-out perms (receiver) | d §2 | Same perms on receiving envoy. |
| Sigil decrypt `-o` perms | d §3 | Plaintext lands as `<user>:<group> 0600`. Needs unlock. |
| Sigil leaves catch-up smoke | d §4 | leaves.json parseable + announcer log mentions sigil activity. |

## Known gaps (deferred)

These are not currently covered by any phase. Track via pending list, not via empty test stubs.

- **Cairn publisher → receiver mirror replication** — bundle generation, manifest update, receiver claim, materialization at `/var/lib/vigo/cairn/mirrors/<seeder>/<user>/<project>.git`.
- **Cross-subnet peer behavior** — `cross_subnet_peers:` config for sigil; `cross_subnet_mirrors:` for cairn. Needs a non-LAN test partner.
- **Disk-pressure guardrails** — sender skips push when receiver reports `free_pct ≤ 10`; receiver returns 507 before reading body. Hard to provoke on a healthy fleet.
- **Bandwidth caps / adaptive throttling** — no current automated probe.
- **Failure modes** — sender unreachable, receiver disk full mid-push, network partition during gossip.
- **Manifest TTL / GC** — pruner sweep behavior, stale-entry transitions.
- **TLS cert rotation** — fleet CA refresh; manual today.
- **Sigil leaves catch-up actual divergence** — phase-d §4 is a smoke test (parseable JSON + log mention). The "offline-during-LEAVE rejoin → digest mismatch → catch-up GET → apply_leave fan-out" arc isn't exercised end-to-end. Doable with a synthetic-leave injection but adds destructive setup.

## Auxiliary scripts

| Script | Purpose |
|---|---|
| [`substrate-phase-b-deploy.sh`](substrate-phase-b-deploy.sh) | Cross-compile + scp the agent binary to the fleet. Run before re-running phase-b post-fix. |
| [`substrate-perms-repair.sh`](substrate-perms-repair.sh) | Audit + repair root-owned files left over from pre-0.30.24 binaries. Automatic for sentinels + dirs; prints manual `unlock` + `reencrypt` commands for ciphertext. |

## Add a new phase

1. Copy phase-d as the template (Catppuccin palette, `section`/`step`/`pass`/`fail`/`skip` helpers, `cleanup` trap).
2. Pick an unused phase letter (next is `phase-e`).
3. Add an entry to the table in this doc and to `PHASES` in `substrate-livetest-all.sh`.
4. Document any phase-specific knobs.
5. Land it in the same commit as the feature it covers — phases are pre-deploy regression checks, not post-hoc bolt-ons.
