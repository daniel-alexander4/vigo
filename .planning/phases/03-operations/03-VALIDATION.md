---
phase: 03
slug: operations
status: draft
nyquist_compliant: false
wave_0_complete: false
created: 2026-03-06
---

# Phase 03 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | go test + testcontainers-go + httptest |
| **Config file** | none — existing go.mod covers deps |
| **Quick run command** | `go test ./... -short -count=1` |
| **Full suite command** | `go test ./... -count=1 -v` |
| **Estimated runtime** | ~45 seconds |

---

## Sampling Rate

- **After every task commit:** Run `go test ./... -short -count=1`
- **After every plan wave:** Run `go test ./... -count=1 -v`
- **Before `/gsd:verify-work`:** Full suite must be green
- **Max feedback latency:** 45 seconds

---

## Per-Task Verification Map

| Task ID | Plan | Wave | Requirement | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|-----------|-------------------|-------------|--------|
| 03-01-01 | 01 | 1 | CLI-01..05 | unit | `go test ./server/api/... -v -count=1` | W0 | pending |
| 03-01-02 | 01 | 1 | CLI-06..08 | unit | `go test ./server/api/... -v -count=1` | W0 | pending |
| 03-02-01 | 02 | 1 | OBS-01,02 | unit | `go test ./server/compliance/... -v -count=1` | W0 | pending |
| 03-02-02 | 02 | 1 | OBS-03 | unit | `go test ./server/... -run TestMetrics -v -count=1` | W0 | pending |
| 03-03-01 | 03 | 2 | OBS-04,05 | unit | `go test ./server/webhook/... -v -count=1` | W0 | pending |
| 03-03-02 | 03 | 2 | SECR-08..10 | unit | `go test ./server/watcher/... ./server/secrets/... -v -count=1` | W0 | pending |
| 03-04-01 | 04 | 3 | CLI-09..11 | unit | `go test ./cli/... -v -count=1` | W0 | pending |

*Status: pending / green / red / flaky*

---

## Wave 0 Requirements

- [ ] httptest helpers for REST API testing
- [ ] Mock isopass HTTP server for watcher tests
- [ ] testcontainers-go already in go.mod from Phase 1

*Existing infrastructure covers most requirements.*

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| isopass rotation polling | SECR-08 | Requires running isopass instance | Start isopass, rotate a secret, verify force-push queued |
| TLS hot reload | SECR-10 | Requires TLS cert swap at runtime | Replace cert files, verify new connections use new cert |
| Prometheus scrape | OBS-03 | Requires Prometheus to verify scraping | curl /metrics, verify output format |

---

## Validation Sign-Off

- [ ] All tasks have automated verify or Wave 0 dependencies
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references
- [ ] No watch-mode flags
- [ ] Feedback latency < 45s
- [ ] `nyquist_compliant: true` set in frontmatter

**Approval:** pending
