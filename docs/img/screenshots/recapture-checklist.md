# Screenshot recapture checklist

The 14 PNGs in `docs/img/screenshots/` were captured against Vigo 0.23.52 (vigo-web's `static/img/screenshots/`). Current release is 0.51.6. Visuals are stable enough to use without an "earlier release" caveat per Dan's call (2026-05-13), but they should be refreshed when convenient.

## How to recapture

- Capture from a current home-lab envoy (`plex`, `danlap`, or `raspberrypi`) against vigosrv 0.51.6+ running locally.
- Browser: Chromium / Brave, light theme off (use Catppuccin Mocha if possible).
- Resolution: 1440×900 viewport, full-page screenshot.
- Strip cookie / sidebar artifacts before committing. Crop to the relevant area.
- Output: PNG at `docs/img/screenshots/vigo.<page>.png`. Replace the existing file; don't add a v2 suffix.

## To refresh (14 existing — captured on 0.23.52)

| File | URL | Notes |
|---|---|---|
| `vigo.dashboard.png` | `/` | Fleet roll-up with current 5-envoy fleet |
| `vigo.envoys.png` | `/envoys` | Envoy list (5 rows: danlap/annlap/raspberrypi/plex/girlslaptop) |
| `vigo.envoy-detail.png` | `/envoys/<id>` | Pick a converged envoy (e.g., plex) |
| `vigo.envoy-config.png` | `/envoys/<id>?tab=config` | Same envoy, config-trace tab |
| `vigo.server-config.png` | `/config` | Show `.live/` tree, not stockpile/ |
| `vigo.compliance.png` | `/compliance` | Framework cards |
| `vigo.compliance-frameworks.png` | `/compliance/hipaa` (or current framework view) | Framework drill-down |
| `vigo.inventory.png` | `/inventory` | Package / process / port view |
| `vigo.risk.png` | `/risk` | Fleet risk-posture cards |
| `vigo.security.png` | `/security` | CVE roll-up |
| `vigo.security-cve-detail.png` | `/security/cve/<id>` | Pick a current CVE with impact |
| `vigo.security-ai-remediation.png` | `/security/cve/<id>?tab=remediation` | AI remediation panel |
| `vigo.integrations.png` | `/integrations` | Outbound integration list |
| `vigo.docs-ai.png` | `/ask` (or wherever AI ask UI lives now) | AI assistant with a real query |

## Captured 2026-06-01 (admin pages, v0.72.3, dark Mocha, 1440×900)

Captured against the home-lab vigosrv (`dan` admin login, headless Chromium) with real fleet data from the weekend swarm-substrate e2e:

| File | URL | Status |
|---|---|---|
| `vigo.swarm-puddle.png` | `/swarm/puddle` | ✅ Puddles fleet view (0.72.0 feature) — 1 puddle, 2 envoys, Healthy. Embedded in `howto/swarm/set-up-puddle.md`. |
| `vigo.swarm.png` | `/swarm` | ✅ Swarm overview — content-subsystem blob table + peers |
| `vigo.gitback.png` | `/swarm/gitback` | ✅ Gitback fleet view — 1 project, no divergence |
| `vigo.lockbox.png` | `/swarm/lockbox` | ✅ Lockbox fleet view — incl. live Drift problem panel |
| `vigo.swarm-curator.png` | `/swarm/curator` | ✅ Curator fleet view — 1 artifact, Ok |
| `vigo.longdrawer.png` | `/swarm/longdrawer` | ✅ Longdrawer fleet view — 1 user, 4 files |
| `vigo.swarm-poolq.png` | `/swarm/poolq` | ✅ Poolq fleet view — 1 topic, 3 messages |
| `vigo.audit.png` | `/audit` | ✅ Audit log — Chain Valid, 2.79M entries |

## Still to capture (need a live session / paid license)

| File | URL | Notes |
|---|---|---|
| `vigo.scrier-ssh.png` | `/scrier/<envoy-id>?protocol=ssh` | Live SSH session in browser — needs an interactive terminal session, can't be captured headless. |
| `vigo.swarm-spanner.png` | `/spanner/bolts/<id>` | Per-bolt detail (needs paid license + spanner topology) |

## Where they're embedded

Run `grep -r "screenshots/" docs/howto/` for the current call sites. The compliance ones I just added land in `compliance-reporting.md`; install/enroll/first-module/publish/monitoring already use day-1 ones.
