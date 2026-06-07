# vigo-fakefleet — synthetic demo fleet harness

Stands up a realistic, multi-cloud **synthetic** envoy fleet against a live
vigosrv — believable AWS/GCP host hierarchy, per-host traits, and
mostly-converged-with-a-few-broken convergence — for demos, screenshots,
and exercising the fleet/UI surfaces at scale without real hardware.

It is deliberately a `test/` harness, **not** a `vigocli` verb: enrolling
thousands of fake envoys has no place in the production admin CLI.

## What it builds

- **~13,647 envoys** spread across a multi-axis hierarchy: 7 cloud regions
  × 3 AZs × 17 host classes (web/app/nginx/postgres/redis/kafka/grafana/…)
  × 3 **environments** (prod/stage/test). Counts and topology are flags.
- **Realistic, bare hostnames** (no domain suffix) so screenshots read as
  a real fleet:
  - About `--named` (default **100**) hosts keep a service hostname —
    `<class>-<env>-<zone>-NNN`, e.g. `nginx-prod-use1a-007`.
  - The rest are a worker pool — `worker-<class>-<env>-<zone>-NNNNN`,
    e.g. `worker-redis-stage-usw2b-01337`. The role still drives the
    configcrate, so the worker pool keeps per-tier convergence variety.
- **Synthetic config**, nested into the operator's own fleet dir
  (`--fleet-dir`, default `alexander4`) so the fake clouds sit alongside
  the real regions, and removed by `backout` (which deletes only the exact
  dirs `create` wrote, never the fleet dir itself):
  - `stacks/configcrates/synthetic/*.vgo` — 5 synthetic configcrates
    (real resource params + real `compliance.provides` tags). Definitions
    must live under `configcrates/` (loader-hardcoded).
  - `stacks/<fleet-dir>/<cloud>/<region>/<zone>/envoys.vgo` — glob
    hostcrates nested by cloud/region/zone (one shared file per zone, not
    per host), so `/envoys/tree` groups envoys under cloud/region/zone
    alongside the real fleet. Each zone file carries two match blocks per
    (class, env) — one for the named hosts, one for the worker pool — both
    mapping straight to the class's configcrate and carrying an
    `environment:`, so workers nest in the tree and gain the env axis too.
    **No roles** — `stacks/roles.vgo` is never touched. Nesting under the
    fleet dir means synthetic hosts inherit its `common.vgo` baseline.
  - `stacks/<fleet-dir>/<cloud>/environments.vgo` — per-environment
    overrides (prod adds fleet-wide monitoring + a stricter `log_level`;
    stage/test differ in vars), kept inside the harness-owned cloud dirs.
- **Per-host traits** (os/distro/arch/cloud/region/az/instance) sent
  inline on every check-in. Outcomes are spread across the fleet:
  `--broken` (default 20) report a failing resource (→ Failed) and
  `--changed` (default 500) report drift (→ Changed/Diverged), so the
  Degraded/Failed/drift pills, risk, and alerts show realistic non-green
  state.

## The safety marker — name shape, not a suffix

Hostnames are bare and realistic, so there is **no** reserved suffix.
Backout deletes by the persisted identity manifest (exact envoy ids).
If the manifest is lost, it falls back to deleting every server-side
envoy whose name has the **generated shape** —
`<class>-<env>-<zone>-NNN` or `worker-<class>-<env>-<zone>-NNNNN` with
class/env/zone drawn from the known topology (`looksSynthetic`). That
positive match can never hit the operator's real hosts (guarded by a
`realHosts` allowlist: `danlap`, `annlap`, `girlslaptop`, `plex`,
`raspberrypi`) or the vigosrv-self envoy. Unit tests enforce this.

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
go build -o /usr/local/bin/vigo-fakefleet ./test/fakefleet

# Stand the fleet up (generate config, publish, enroll, first tick):
sudo vigo-fakefleet create

# Keep it fresh durably — install + enable the daemon (1s check-ins):
sudo cp test/fakefleet/vigo-fakefleet.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now vigo-fakefleet.service

# Tear it all down:
sudo systemctl disable --now vigo-fakefleet.service
sudo vigo-fakefleet backout
```

## Durability — a `run` daemon under systemd

"Stays attached until backout" is delivered by a **long-running `run`
daemon**:

- `create` persists every envoy's identity (id + ed25519 key + mTLS cert)
  to `/srv/vigo/fakefleet/state.json` (mode 0600 — it holds private keys).
- `run` loads that manifest and holds **one persistent mTLS connection per
  envoy**, checking each in every `--cadence` (default **1s**, reported to
  the server as the check-in cycle) and reporting a convergence result
  every `--report-cadence` (default **1m**). The two cadences are
  decoupled deliberately: a CheckIn is the freshness signal, while a
  ReportResult is a run written to SQLite — a run per envoy per second at
  fleet scale would bury the run store. To make convergence runs land at
  1s too, set `--report-cadence 1s` (heavy disk write load).
- A true per-envoy 1s cadence at fleet scale **requires** the daemon: a
  full pass over 13.6k envoys takes far longer than a second, and a fresh
  process per second would re-handshake every connection. The daemon
  staggers each envoy's first tick across one cadence window so check-ins
  spread smoothly instead of bunching on the same instant.
- One connection = one file descriptor per envoy, so the unit raises
  `LimitNOFILE`; a **manual** `run` needs `ulimit -n` above the fleet size.
- By default `run` drives **unary** CheckIn/ReportResult over the held
  connection — that loads the check-in/report rate but leaves
  `vigo_streams_active` at zero (the server only counts *promoted bidi
  AgentStreams*). Pass **`--stream`** to make each envoy open and hold a real
  `AgentStream` instead: that is the only mode that exercises the per-stream
  connection-RSS ceiling (`checkin.max_connections`, ~220 KB server RSS per
  held stream) and drives `vigo_streams_active` up to the fleet size. Use the
  default for check-in-rate load; use `--stream` for connection-ceiling load.
- **Resume across interruptions** falls out for free: a **vigosrv restart**
  leaves the envoy rows in SQLite (they rehydrate at boot) and grpc
  transparently reconnects the persistent conns; a **host reboot**
  relaunches the enabled service, which reloads the manifest — no
  re-enrollment, no duplicate rows, no second license hit; a **transient
  outage** is just missed beats the next tick retries.

If every envoy comes back `NotFound` (e.g. the server DB was wiped), the
daemon counts them as `gone` — run `create` again to re-establish the fleet.

## Backout order

`backout` deletes the envoys (by the manifest's exact ids, or — if the
manifest is lost — every server-side envoy whose name has the generated
shape, see above), removes exactly the dirs `create` wrote (the
manifest's `cleanup_dirs`: `configcrates/synthetic` + each fleet-dir
cloud root — **never** the fleet dir itself), republishes, and removes
the manifest. Stop the daemon first so a tick doesn't race the delete
(a racing tick just logs `NotFound` and is harmless either way).

## Flags

`create` / `run` / `tick` / `backout` share one flag set:

| Flag | Default | Meaning |
|------|---------|---------|
| `--count` | `13647` | number of synthetic envoys (`create`) |
| `--named` | `100` | how many keep a realistic service hostname; the rest are `worker-NNNNN` (`create`) |
| `--broken` | `20` | how many report a failing resource → Failed (`create`) |
| `--changed` | `500` | how many report drift → Changed/Diverged (`create`) |
| `--cadence` | `1s` | check-in interval, also reported as the check-in cycle (`run`/`tick`) |
| `--report-cadence` | `1m` | how often the daemon reports a convergence run per envoy (`run`) |
| `--stream` | `false` | `run` holds a real bidi `AgentStream` per envoy instead of unary check-ins — the only mode that drives `vigo_streams_active` and the per-stream connection-RSS ceiling (`checkin.max_connections`) |
| `--concurrency` | `64` | max in-flight enroll/checkin operations |
| `--server` | `127.0.0.1:1530` | gRPC address |
| `--api` | `https://127.0.0.1:8443` | REST API base |
| `--stacks-dir` | `/srv/vigo/stacks` | operator stacks tree |
| `--fleet-dir` | `alexander4` | stacks subdir the fake `aws`/`gcp` clouds nest under |
| `--state` | `/srv/vigo/fakefleet/state.json` | identity manifest |
| `--admin-token` / `--admin-token-file` | (file) `/srv/vigo/cli-admin-token` | admin auth |
| `--vigocli` | `vigocli` | binary used to publish |
