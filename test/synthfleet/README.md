# vigo-synthfleet — synthetic demo fleet harness

Stands up a realistic, multi-cloud **synthetic** envoy fleet against a live
vigosrv — believable AWS/GCP host hierarchy, per-host traits, and
mostly-converged-with-a-few-broken convergence — for demos, screenshots,
and exercising the fleet/UI surfaces at scale without real hardware.

It is deliberately a `test/` harness, **not** a `vigocli` verb: enrolling
thousands of fake envoys has no place in the production admin CLI.

## What it builds

- **~13,647 envoys** spread across 7 cloud regions × 3 AZs × 6 host
  classes (web/app/db/cache/monitoring/bastion). Counts and topology are
  flags.
- **Self-contained synthetic config**, written under two clearly
  namespaced dirs and removed wholesale by `backout`:
  - `stacks/configcrates/synthetic/*.vgo` — 4 synthetic configcrates
    (real resource params + real `compliance.provides` tags).
  - `stacks/synthetic/{aws,gcp}/<region>.vgo` — glob hostcrates mapping
    host-class patterns straight to those configcrates. **No roles** — the
    central `stacks/roles.vgo` is never touched.
- **Per-host traits** (os/distro/arch/cloud/region/az/instance) sent
  inline on every check-in, plus **5–10 deliberately broken** hosts that
  report a failing resource so Degraded/Failed pills, risk, and alerts
  show realistic non-green state.

## The marker — `*.synthetic`

Every synthetic hostname ends in the reserved `.synthetic` suffix. That
suffix is the whole safety story: every generated hostcrate pattern only
ever matches it, it can never collide with a real host, and `backout`
targets exactly it. Two unit tests enforce this invariant.

## Prerequisites (your call)

1. **Run it on the vigosrv host.** It reads the root-only local-admin
   token (`/srv/vigo/cli-admin-token`) and writes under `/srv/vigo/`.
2. **License.** Enrolling ~13.6k trips the hard license node-count gate —
   the harness does **not** bypass it. Put the server on a license with
   `MaxEnvoys ≥ ~13,700` first; otherwise `create` fails fast with a
   clear message. Freshness ticks never re-enroll, so they never re-trip
   the gate.
3. **`vigocli` on `PATH`** (or pass `--vigocli /path/to/vigocli`) — the
   publish step shells out to `vigocli config publish` (the canonical
   stacks → `.live/` sync; a bare reload would not see new files).

## Usage

```bash
go build -o /usr/local/bin/vigo-synthfleet ./test/synthfleet

# Stand the fleet up (generate config, publish, enroll, first tick):
sudo vigo-synthfleet create

# Keep it fresh durably — install + enable the timer:
sudo cp test/synthfleet/vigo-synthfleet.{service,timer} /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now vigo-synthfleet.timer

# Tear it all down:
sudo systemctl disable --now vigo-synthfleet.timer
sudo vigo-synthfleet backout
```

## Durability — systemd timer + stateless tick

"Stays attached until backout" is delivered by a **systemd timer firing a
stateless `tick`**, not a long-lived daemon:

- `create` persists every envoy's identity (id + ed25519 key + mTLS cert)
  to `/srv/vigo/synthfleet/state.json` (mode 0600 — it holds private
  keys).
- `tick` loads that manifest and fires one CheckIn + ReportResult per
  envoy, then exits. The timer fires it every 2 minutes (reported to the
  server as the check-in cycle, so the default freshness window keeps the
  fleet Online with margin).
- **Resume across interruptions** falls out for free: a **vigosrv
  restart** leaves the envoy rows in SQLite (they rehydrate at boot) and
  the next tick reconnects; a **host reboot** re-arms the timer
  (`OnBootSec`) which loads the manifest and resumes — no re-enrollment,
  no duplicate rows, no second license hit; a **transient outage** just
  means that tick failed and the next retries.

If every envoy comes back `NotFound` (e.g. the server DB was wiped),
`tick` exits non-zero — run `create` again to re-establish the fleet.

## Backout order

`backout` deletes the envoys (keyed on the `.synthetic` suffix — from the
manifest, or by listing the server if the manifest is lost), wipes both
`synthetic/` config dirs, republishes, and removes the manifest. Disable
the timer first so a tick doesn't race the delete (a racing tick just
logs `NotFound` and is harmless either way).

## Flags

`create` / `tick` / `backout` share one flag set:

| Flag | Default | Meaning |
|------|---------|---------|
| `--count` | `13647` | number of synthetic envoys (`create`) |
| `--broken` | `7` | how many report a failing resource (`create`) |
| `--cadence` | `2m` | tick interval reported as the check-in cycle (match the timer) |
| `--concurrency` | `64` | max in-flight enroll/checkin operations |
| `--server` | `127.0.0.1:1530` | gRPC address |
| `--api` | `https://127.0.0.1:8443` | REST API base |
| `--stacks-dir` | `/srv/vigo/stacks` | operator stacks tree |
| `--state` | `/srv/vigo/synthfleet/state.json` | identity manifest |
| `--admin-token` / `--admin-token-file` | (file) `/srv/vigo/cli-admin-token` | admin auth |
| `--vigocli` | `vigocli` | binary used to publish |
