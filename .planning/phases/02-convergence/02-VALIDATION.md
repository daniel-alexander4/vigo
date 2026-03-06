---
phase: 02
slug: convergence
status: draft
nyquist_compliant: false
wave_0_complete: false
created: 2026-03-06
---

# Phase 02 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | go test + testcontainers-go |
| **Config file** | none — existing go.mod covers deps |
| **Quick run command** | `go test ./... -short -count=1` |
| **Full suite command** | `go test ./... -count=1 -v` |
| **Estimated runtime** | ~30 seconds |

---

## Sampling Rate

- **After every task commit:** Run `go test ./... -short -count=1`
- **After every plan wave:** Run `go test ./... -count=1 -v`
- **Before `/gsd:verify-work`:** Full suite must be green
- **Max feedback latency:** 30 seconds

---

## Per-Task Verification Map

| Task ID | Plan | Wave | Requirement | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|-----------|-------------------|-------------|--------|
| 02-01-01 | 01 | 1 | CONV-01 | unit | `go test ./server/runner/... -run TestDAG` | W0 | pending |
| 02-01-02 | 01 | 1 | CONV-02 | unit | `go test ./server/runner/... -run TestNotify` | W0 | pending |
| 02-02-01 | 02 | 1 | AGNT-01,02,03 | unit | `go test ./agent/modules/... -run TestFile` | W0 | pending |
| 02-02-02 | 02 | 1 | AGNT-04 | unit | `go test ./agent/modules/... -run TestPackage` | W0 | pending |
| 02-02-03 | 02 | 1 | AGNT-05 | unit | `go test ./agent/modules/... -run TestService` | W0 | pending |
| 02-02-04 | 02 | 1 | AGNT-06 | unit | `go test ./agent/modules/... -run TestUser` | W0 | pending |
| 02-03-01 | 03 | 2 | CONV-03,04 | unit | `go test ./agent/facts/... -run TestCollect` | W0 | pending |
| 02-03-02 | 03 | 2 | CONF-09,10 | unit | `go test ./server/config/... -run TestTemplate` | W0 | pending |
| 02-04-01 | 04 | 2 | AGNT-07,08 | unit | `go test ./agent/checkin/... -run TestDaemon` | W0 | pending |
| 02-04-02 | 04 | 2 | CONV-05,06 | unit | `go test ./agent/... -run TestDryRun` | W0 | pending |
| 02-05-01 | 05 | 3 | CONV-07,08,09 | integration | `go test ./... -run TestE2E` | W0 | pending |

*Status: pending / green / red / flaky*

---

## Wave 0 Requirements

- [ ] Test helpers for creating mock gRPC server responses
- [ ] Test fixtures for sample YAML configs with resources
- [ ] testcontainers-go already in go.mod from Phase 1

*Existing infrastructure covers most phase requirements — Phase 1 established testcontainers-go patterns.*

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| Package install/remove | AGNT-04 | Requires real package manager | Test in Docker container with apt |
| Service start/stop | AGNT-05 | Requires systemd | Test in systemd-enabled container |
| Agent daemon jitter | AGNT-08 | Timing-sensitive | Observe logs for interval variation |

---

## Validation Sign-Off

- [ ] All tasks have automated verify or Wave 0 dependencies
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references
- [ ] No watch-mode flags
- [ ] Feedback latency < 30s
- [ ] `nyquist_compliant: true` set in frontmatter

**Approval:** pending
