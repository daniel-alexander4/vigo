# Convergence Walkthrough

A step-by-step trace of what happens when you add a new module, publish it, and a thrall converges — with realistic timing and traffic flow for each phase.

## Scenario

You add an nginx module that installs a package, writes a config file, and starts the service. A thrall named `web-01.example.com` picks it up on its next check-in.

## Timeline

![Convergence Walkthrough](img/convergence-walkthrough.svg)

<!--
```
Operator workstation                Hobnob server              Thrall (web-01)                  Internet
        |                                |                           |                              |
   0s   | edit hnb/proposed/modules/     |                           |                              |
        | nginx.hnb                      |                           |                              |
        |                                |                           |                              |
   1s   | hobnobcli config publish ----->|                           |                              |
        |                                | validate YAML (~5ms)      |                              |
        |                                | sync proposed → .live     |                              |
        |                                | reload config (~10ms)     |                              |
        |<---- "published 1 file" -------|                           |                              |
        |                                |                           |                              |
        |                 (wait 0-5min for next check-in interval)   |                              |
        |                                |                           |                              |
 ~3min  |                                |<-- CheckIn (gRPC/mTLS) ---|                              |
        |                                |    UUID + traits + fingerprint                            |
        |                                |                           |                              |
        |                                | match hostname (~300ns)   |                              |
        |                                | resolve roles → modules   |                              |
        |                                | merge vars (~1µs)         |                              |
        |                                | render templates (~1ms)   |                              |
        |                                | sign bundle (ED25519)     |                              |
        |                                |                           |                              |
        |                                |--- PolicyBundle (5-20KB)->|                              |
        |                                |    modules + resources    |                              |
        |                                |    + rendered content     |                              |
        |                                |    + signature            |                              |
        |                                |                           |                              |
        |                                |                    verify bundle signature (~10µs)        |
        |                                |                    cache bundle in LMDB                   |
        |                                |                           |                              |
        |                                |                    ┌──────────────────┐                   |
        |                                |                    │ EXECUTE RESOURCES │                   |
        |                                |                    └──────────────────┘                   |
        |                                |                           |                              |
        |                                |                    1. Install nginx package               |
        |                                |                    ·  apt-get install nginx ──────────────>  (apt repo)
        |                                |                    ·  download .deb (~15s)   <────────────   (~30MB)
        |                                |                    ·  install (~5s)                       |
        |                                |                           |                              |
        |                                |                    2. Write /etc/nginx/nginx.conf         |
        |                                |                    ·  compare SHA256 (~1ms)               |
        |                                |                    ·  write file (~1ms)                   |
        |                                |                    ·  set owner/group/mode                |
        |                                |                           |                              |
        |                                |                    3. Start nginx service                 |
        |                                |                    ·  triggered by notify                 |
        |                                |                    ·  systemctl restart (~2s)             |
        |                                |                           |                              |
        |                                |                    total exec time: ~25s                  |
        |                                |                           |                              |
        |                                |<-- ReportResult ---------|                              |
        |                                |    3 resources:           |                              |
        |                                |    nginx-pkg: created     |                              |
        |                                |    nginx-conf: created    |                              |
        |                                |    nginx-svc: updated     |                              |
        |                                |                           |                              |
        |                                | store run results         |                              |
        |                                | update compliance: drifted|                              |
        |                                |                           |                              |
        |                 (next check-in, ~5min later)               |                              |
        |                                |                           |                              |
 ~8min  |                                |<-- CheckIn --------------|                              |
        |                                |--- no_change (100B) ---->|                              |
        |                                |    fingerprint matches    |                              |
        |                                |                           |                              |
        |                                |                    check nginx-pkg: present ✓             |
        |                                |                    check nginx-conf: SHA match ✓          |
        |                                |                    check nginx-svc: running ✓             |
        |                                |                           |                              |
        |                                |<-- ReportResult ---------|                              |
        |                                |    0 changed, 0 failed   |                              |
        |                                | compliance: compliant ✓  |                              |
```
-->

## Traffic Flow

The key insight: **most traffic bypasses the server entirely**. The server only sends declarative intent — the thrall does the actual work directly.

### What goes through the server (gRPC/mTLS, port 1530)

| Direction | Payload | Size | Frequency |
|-----------|---------|------|-----------|
| Thrall → Server | CheckIn (UUID, traits, fingerprint) | 2-5 KB | Every 5 min |
| Server → Thrall | PolicyBundle (modules, resources, rendered content) | 5-50 KB | Only when config changed |
| Server → Thrall | no_change response | ~100 B | When fingerprint matches |
| Thrall → Server | ReportResult (per-resource outcomes) | 1-10 KB | After each convergence |

### What goes directly from thrall to the internet (or LAN)

| Operation | Destination | Size | Notes |
|-----------|-------------|------|-------|
| `apt-get install` / `dnf install` | Package mirror | 1-500 MB | Downloaded by the thrall, not proxied through the server |
| `git clone` (git executor) | Git remote | varies | Thrall clones directly |
| `curl` in archive executor | URL in resource config | varies | Thrall downloads directly |
| Docker image pulls | Registry (Docker Hub, GHCR) | 50-500 MB | Thrall pulls directly |
| Repository key downloads | Key server URL | 1-5 KB | Thrall fetches directly |

### What stays local on the thrall

| Operation | Notes |
|-----------|-------|
| File writes, chmod, chown | Local filesystem only |
| Service start/stop/restart | Local systemd/launchd/etc. |
| User/group management | Local passwd/shadow |
| Cron job installation | Local crontab |
| Template rendering | Pre-rendered by server, written locally |
| Idempotency checks (SHA256, stat, dpkg -l) | Local state comparison |

## Timing Summary

| Phase | Duration | Where |
|-------|----------|-------|
| Config publish (validate + sync + reload) | < 1s | Operator → Server |
| Wait for next check-in | 0-5 min (configurable) | — |
| Check-in round-trip (cache hit) | < 1 ms | Thrall ↔ Server |
| Check-in round-trip (cache miss, config compilation) | 10-50 ms | Server |
| Bundle transfer | < 10 ms (LAN) | Server → Thrall |
| Package install | 5-60s | Thrall → Internet |
| File write + permission set | < 5 ms | Thrall local |
| Service restart | 1-5s | Thrall local |
| Result report | < 5 ms | Thrall → Server |
| **Total (first convergence)** | **~30s + wait** | |
| **Subsequent check-ins (no change)** | **< 50 ms** | |

## Force Push

Skip the check-in wait by forcing an immediate convergence:

```bash
hobnobcli push --target "web-01.example.com"
```

The server sets `stream_requested` on the thrall. If the thrall has an active stream, the push is immediate. If it's polling, it takes effect on the next check-in (up to one interval).

## Offline Convergence

If the server is unreachable, the thrall continues applying its last cached policy bundle from LMDB. Package installs that require internet access will still fail if the thrall has no network, but all local operations (file writes, service management, user creation) continue working.

## Related

- [Check-in Lifecycle](check-in-lifecycle.md) — Detailed check-in protocol
- [Compiled Promises](compiled-promises.md) — Bundle signing and offline mode
- [Resource DAG](resource-dag.md) — Execution ordering
