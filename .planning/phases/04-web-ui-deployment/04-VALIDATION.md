---
phase: 04
slug: web-ui-deployment
status: draft
nyquist_compliant: false
wave_0_complete: false
created: 2026-03-06
---

# Phase 04 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | go test |
| **Config file** | none — existing infrastructure |
| **Quick run command** | `go test ./ui/... ./server/api/... ./agent/updater/...` |
| **Full suite command** | `go test ./...` |
| **Estimated runtime** | ~30 seconds |

---

## Sampling Rate

- **After every task commit:** Run `go test ./ui/... ./server/api/... ./agent/updater/...`
- **After every plan wave:** Run `go test ./...`
- **Before `/gsd:verify-work`:** Full suite must be green
- **Max feedback latency:** 30 seconds

---

## Per-Task Verification Map

| Task ID | Plan | Wave | Requirement | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|-----------|-------------------|-------------|--------|
| 04-01-01 | 01 | 1 | UI-08 | unit | `go test ./ui/...` | ❌ W0 | ⬜ pending |
| 04-02-01 | 02 | 1 | UI-01, UI-02 | unit | `go test ./ui/...` | ❌ W0 | ⬜ pending |
| 04-02-02 | 02 | 1 | UI-03, UI-04 | unit | `go test ./ui/...` | ❌ W0 | ⬜ pending |
| 04-03-01 | 03 | 2 | UI-05, UI-06 | unit | `go test ./ui/...` | ❌ W0 | ⬜ pending |
| 04-03-02 | 03 | 2 | UI-07 | unit | `go test ./ui/...` | ❌ W0 | ⬜ pending |
| 04-04-01 | 04 | 2 | INFRA-04, INFRA-05 | unit | `go test ./server/api/...` | ❌ W0 | ⬜ pending |
| 04-04-02 | 04 | 2 | AGNT-10 | unit | `go test ./agent/updater/...` | ❌ W0 | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

- [ ] `ui/handler_test.go` — test template rendering and route registration
- [ ] `server/api/bootstrap_test.go` — test bootstrap script generation
- [ ] `agent/updater/updater_test.go` — test version check and binary download

*Existing `go test` infrastructure covers test execution.*

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| Dashboard visual layout | UI-01 | Visual appearance requires browser | Open dashboard in browser, verify stat cards, compliance bar, activity table |
| HTMX navigation | UI-08 | Browser interaction needed | Click sidebar links, verify AJAX navigation, check browser back button |
| SVG sparkline rendering | UI-07 | Visual output verification | View compliance page, verify 30-day trend renders correctly |
| Bootstrap end-to-end | INFRA-04 | Requires target node + running server | Run curl command on clean VM, verify agent installs and enrolls |

---

## Validation Sign-Off

- [ ] All tasks have `<automated>` verify or Wave 0 dependencies
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references
- [ ] No watch-mode flags
- [ ] Feedback latency < 30s
- [ ] `nyquist_compliant: true` set in frontmatter

**Approval:** pending
