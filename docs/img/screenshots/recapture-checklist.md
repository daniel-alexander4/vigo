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

## To capture (7 missing — features that landed post-0.23.52)

| File | URL | Notes |
|---|---|---|
| `vigo.scrier-ssh.png` | `/scrier/<envoy-id>?protocol=ssh` | Live SSH session in browser. Show the terminal pane with a real prompt. |
| `vigo.swarm-curator.png` | `/swarm/curator` | Curator fleet view — needs at least one published artifact |
| `vigo.lockbox.png` | `/lockbox` | Lockbox fleet view — presence matrix per (user × envoy × file) |
| `vigo.gitback.png` | `/gitback` | Gitback fleet view — projects + ref divergence |
| `vigo.longdrawer.png` | `/longdrawer` | Longdrawer fleet view — file presence matrix |
| `vigo.audit.png` | `/audit` | Hash-chained audit log entries |
| `vigo.swarm-tungol.png` | `/tungol/steorras/<id>` | Per-steorra detail (needs paid license + tungol topology) |

## Where they're embedded

Run `grep -r "screenshots/" docs/howto/` for the current call sites. The compliance ones I just added land in `compliance-reporting.md`; install/enroll/first-module/publish/monitoring already use day-1 ones.
