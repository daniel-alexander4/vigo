
# Sizing a vigosrv host

You'll finish this page knowing **how many envoys one server can comfortably hold**, what happens past that line, and the operator levers (vertical headroom, horizontal federation) that move it.

**When you'd use this:** before standing up a new fleet, when planning a hardware refresh, or when an existing server's memory use climbs under convergence waves.

**When you'd skip this:** small fleets — anything under a few thousand envoys runs well below the regime this page is about, with default config and any modest VM.

## The headline

**vigosrv's single-host ceiling is memory, and it's linear: about 220 KB of RAM per connected envoy.** Nothing else binds first — CPU never saturates, the connection/goroutine model is flat and predictable, and latency degrades gracefully under overload rather than collapsing. **By default the server sizes a held-connection cap from host RAM and, once that safe ceiling is reached, sheds new streams with `ResourceExhausted` — the agent backs off and retries — so it stays up under overload instead of being OOM-killed.** Reaching the kernel-OOM point requires explicitly disabling the cap (`checkin.max_connections: -1`); the numbers below are the *physical* behavior of an uncapped server, i.e. what the cap is sized to keep you below.

On this page's hardware (8 logical CPUs, **31 GiB RAM**, NVMe), measured live:

| Concurrent envoys | Server RSS | Outcome |
|---|---|---|
| 40,000 | 8.4 GB | comfortable |
| 50,000 | 11.2 GB | comfortable |
| **60,000** | **13.4 GB** | **comfortable — recommended safe ceiling for 31 GiB** |
| ~70,000 | ~14.7 GB | **OOM-killed** |

The safe ceiling sits at ~60k because you want headroom below the OOM point for the page cache, the co-located `vigo` agent, GC working set, and a margin for correlated check-in bursts. The hard wall on this box is ~70k.

**The sizing rule: budget ~220 KB of RAM per envoy** (≈ 256 MB per 1,000 envoys), plus ~1.5 GB base, and keep the connection working set under ~50% of host RAM. It scales linearly with memory:

| Host RAM | Safe ceiling | Hard wall (OOM) |
|---|---|---|
| 16 GiB | ~28,000 | ~33,000 |
| 32 GiB | ~60,000 | ~70,000 |
| 64 GiB | ~120,000 | ~140,000 |
| 128 GiB | ~260,000 | ~290,000 |

The "safe ceiling" column is what `checkin.max_connections: 0` auto-derives (40% of RAM ÷ 220 KB); the 32 GiB row is the measured anchor (60k comfortable / 70k OOM), the others scale linearly from it. The auto-derivation and these figures agree to within rounding.

**Past the safe ceiling, the answer is federation, not just a bigger box** (though a bigger box buys proportional headroom). Vigo's [spanner mode (ADR-026)](../adr/026-spanner-peer-equal-admissions.md) partitions the fleet by hostname pattern across peer-equal bolts; every bolt holds its own slice and they CRDT-gossip the admissions roster. That's the supported path beyond one host, and it's what also gives you survival of a single-host failure.

> **`checkin.max_connections` is on by default — auto-derived from host RAM.** Past this many concurrently-held agent streams, the server refuses new ones with `ResourceExhausted` (the agent backs off and retries) instead of accepting connections until the kernel OOM-kills it. An OOM-kill is a hard failure — it drops every in-flight connection and loses run records mid-transaction; `restart: unless-stopped` recovers in ~60 s but the shed is the graceful path. Left unset (`0`), the cap resolves at startup to the safe ceiling for the host (≈ 40% of RAM ÷ 220 KB — e.g. ~60,000 on a 31 GiB host), logged as `held-stream admission cap auto-derived from host memory`. Override it with an explicit number if you've measured a different line for your hardware; set `-1` to disable it entirely and accept the legacy OOM-at-the-wall behavior. Watch `vigo_streams_active` against the effective value. The cap protects one host; a fleet that legitimately outgrows it is the signal to add RAM or [federate](#when-to-federate-and-how).

## What "comfortable" looks like, on what hardware

The numbers above are measured live, not extrapolated. The harness is [`test/burstwave/`](../../test/burstwave/) (a coordinated read-path + write-path burst harness that exercises the real agent cycle); the capture wrapper is [`test/burstwave/capture.sh`](../../test/burstwave/capture.sh) (CPU pprof + goroutine + RSS + FD telemetry alongside the harness); the per-run analyzer is [`test/burstwave/report.py`](../../test/burstwave/report.py).

**Server-host hardware:**

| | |
|---|---|
| CPU | Intel i5-1155G7 — 4 cores, 8 logical CPUs (SMT-2), 2.5 GHz base / 4.5 GHz boost |
| Memory | 31 GiB DDR4 |
| Storage | NVMe SSD (Crucial P3), ext4 |
| OS / kernel | Linux Mint 22, kernel 6.8 |
| Container | Docker, **host networking**, container `nofile` raised to 1,048,576, no CPU/memory cgroup limit |
| Vigo version | 0.69.20 |

**Measured scaling (1-second check-in interval, `--results-per-report 12`, connections driven from multiple hosts so the offered load is real — see the methodology note below):**

| Concurrent envoys | Peak goroutines | Peak RSS | Check-in `p50` / `p99` | Host CPU peak | Verdict |
|---|---|---|---|---|---|
| 40,000 | 122,000 | 8.4 GB | 1.5 s / 3.0 s | 677% | comfortable |
| 50,000 | 152,000 | 11.2 GB | 3.0 s / 6.1 s | 619% | comfortable |
| 60,000 | 182,000 | 13.4 GB | 3.0 s / 24 s | 645% | comfortable (safe ceiling) |
| ~70,000 | (≈210,000 projected) | ~14.7 GB | — | ~650% | **OOM-killed** |

Three things the matrix shows:

- **Memory is the only limit that binds.** RSS tracks connection count at a dead-linear ~220 KB/conn with no leak (goroutines are exactly 3 per connection — one reader, one writer, one handler — so the goroutine column is just `3 × envoys + overhead`). The 60k row was a pre-registered prediction: 13.2 GB predicted, 13.4 GB measured.
- **CPU is never the limiter.** Peak host CPU stayed around 620–680% of the 800% available (8 logical cores) at every point, including right up to the OOM. You will run out of RAM long before CPU.
- **Latency degrades gracefully, it doesn't collapse.** Under heavy 1 s-interval overload the check-in `p50` plateaus around 3 s (the write path sheds excess with `ResourceExhausted`; reads stay served). The server reaches a stable backpressure equilibrium rather than thrashing — right up until it hits the RAM wall.

The `ResourceExhausted` you see in shed counts is the always-on `checkin.max_concurrent` / ReportResult admission cap doing its job: it bounds *write* concurrency against the single SQLite writer and sheds cleanly (agent backs off). That cap is healthy and is **not** the memory limiter — it protects the writer, not the process's RAM.

## Why the ceiling is per-connection memory

Each connected envoy costs ~220 KB of resident memory: gRPC's default per-connection read+write buffers (~64 KiB), the three goroutines and their stacks, HTTP/2 stream and flow-control state, and GC headroom on all of it. That's linear and predictable — which is exactly why the model is a single multiply. Double the host RAM, double the envoy ceiling.

Earlier Vigo builds (≤ 0.69.19) showed a *transport-instability* ceiling around 20k connections — `Unavailable` storms well below the memory wall. That was a too-aggressive 10 s `KeepaliveTimeout` default killing healthy connections under scheduler-queue depth; it was raised to 30 s in 0.69.20 (see the version history). With that fixed, there is no transport ceiling below the memory wall — the server scales connections linearly until it runs out of RAM.

## Tunables that move the line

These live in `server.yaml` (see [server-yaml.md](../reference/server-yaml.md)) and trade one resource for another. None turns a 31 GiB host into a comfortable 100k host on its own — they nudge a host that's near its line.

- **`checkin.max_connections`** (default `0` → auto-derived from host RAM; `-1` → uncapped). The direct guard against the memory wall: caps concurrently-held agent streams, refusing the excess with `ResourceExhausted` so the server sheds instead of OOMing. On by default — `0` resolves at startup to ≈ 40% of RAM ÷ 220 KB (the safe-ceiling table above); set an explicit number to override, or `-1` to turn it off. This is what turns "OOM at the wall" into "graceful backoff at the wall" — `grpc.memory_limit` below only slows the climb; it does not refuse connections.
- **`grpc.memory_limit`** (Go's `GOMEMLIMIT`). Set it below the host's available RAM so Go's GC works harder to stay under it instead of letting RSS climb into OOM territory. It trades CPU for survival and buys headroom, but it does not stop accepting connections — pair it with `checkin.max_connections` for an actual ceiling.
- **`grpc.gogc`** (default `100`). Lower (e.g. `50`) shrinks the GC working set and RSS peaks at the cost of more GC CPU; higher trades the other way.
- **`grpc.max_concurrent_streams`** (default `5000`). Bounds per-connection HTTP/2 stream state when an envoy misbehaves; doesn't change envoy count.
- **`checkin.interval`** (the *agent-side* poll cadence). Connection count, not request rate, sets the memory ceiling — so the interval does **not** move the memory line. It sets offered request rate (write-shed intensity and latency under load), not how many envoys you can hold. A longer interval makes a near-ceiling host calmer; it does not let you hold more envoys.
- **`checkin.max_concurrent`** (default `0` → CPU-scaled, always on). Don't disable it. See [Sizing `max_concurrent`](../reference/server-yaml.md#sizing-max_concurrent).

## When to federate (and how)

Federate via spanner when **any** of these is true for the planned fleet:

1. Envoy count on one host would exceed the safe ceiling for its RAM (the `~220 KB × envoys` working set crossing ~50% of host memory — ~60k on 31 GiB, scaling linearly with RAM).
2. Correlated check-in events (post-publish waves, mass re-enroll, network-partition recovery) could push concurrent connections past the safe ceiling even if steady state would fit. These spikes are real and they land on memory directly — size for the peak, not the average.
3. Fault tolerance matters and you can't lose the one host without losing the fleet.

The spanner setup is in [Set up spanner](set-up-spanner.md); the rationale is [ADR-026](../adr/026-spanner-peer-equal-admissions.md). Operator-visible cost: a few more moving parts (peer-equal bolts, hostname-pattern partitioning, multicast Tier-1 + REST Tier-2 admissions gossip) in exchange for linear scale-out and single-host-failure survival.

## Re-running the measurements yourself

**Methodology matters here — naïve load tests measure the harness, not the server.** To get a true server number you have to clear four client-side and deployment-side limits, or you'll hit a false ceiling long before the real one:

1. **Run the server with host networking** (the bundled compose does). Bridge networking routes every connection through a single per-port `docker-proxy` userland process that becomes the bottleneck well before the server does.
2. **Raise the container's `nofile`** (the bundled compose sets 1,048,576). The default 65,536 caps the server at ~63k connections — a file-descriptor wall, not a real limit.
3. **Drive load from multiple hosts.** A single client host dialing one `host:port` shares one ~28k ephemeral-port pool (`net.ipv4.ip_local_port_range`), so one box can't *offer* more than ~28k connections no matter how big the server is. Widen the range (`sysctl -w net.ipv4.ip_local_port_range="10000 65535"` → ~55k) and spread the load across several driver machines, synced with `--start-at`.
4. **Don't drive load from the server host itself** beyond a loopback baseline — the load generator competes with vigosrv for CPU and skews the result.

```bash
# build the harness once (per driver host; or scp the binary)
go build -o /tmp/burstwave ./test/burstwave

# one driver, server on loopback, with full server-side capture.
# --second-count 1 + --second-burst-at 1.0 keeps cohort B effectively disabled
# (one envoy released at end of A's window), so this is a clean single-cohort
# steady-state measurement. Drop those two flags to test the overlap regime.
test/burstwave/capture.sh --output /tmp/sizing-run -- \
  --server 127.0.0.1:1530 --api https://127.0.0.1:8443 \
  --admin-token "$(sudo cat /srv/vigo/cli-admin-token)" \
  --count 30000 --second-count 1 --interval 1s --results-per-report 12 \
  --warmup 30 --duration 300 --second-burst-at 1.0 \
  --server-container vigo

# additional driver hosts (over the LAN) join the same instant via --start-at:
/tmp/burstwave --server <server-ip>:1530 --api https://<server-ip>:8443 \
  --admin-token "<token>" --count 30000 --second-count 1 --interval 1s \
  --warmup 30 --duration 300 --second-burst-at 1.0 \
  --hostname-prefix wave-b- --start-at 2026-01-01T00:00:00Z --output /tmp/sizing-run-b
```

The capture wrapper produces `cpu.pprof`, `goroutines-peak.txt`, `heap-peak.pprof`, `resources.csv` (goroutines + RSS + FDs every 5 s), `stats.csv` (docker stats every 1 s), and `run.log`. Feed the run directory to `test/burstwave/report.py <dir>` for a plain-language verdict (it renames gRPC codes, splits startup-surge from steady-state, and flags whether a run hit a *harness* limit rather than a server one). Analyze the CPU profile with `go tool pprof -http=:8080 /tmp/sizing-run/cpu.pprof`.

---
*Verified on Vigo 0.69.20 · 2026-05-30.*

*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../legal/license.md).*
