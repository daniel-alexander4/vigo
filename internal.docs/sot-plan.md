# SoT Consolidation Plan

> **Status: complete (2026-05-03).** Phase 1 shipped at 0.37.4 / 0.37.7-9. Phase 2 shipped at 0.37.12-14, 0.37.16. Verification notes captured in commit messages where the implemented scope diverged from the plan (Commit 6 didn't need a new package; Commit 7 scoped to counts-only). Kept here as the design-rationale reference.

CLI ↔ UI single sources of truth, plus server-internal consolidations identified in the same survey pass.

## Guiding principle

**Shared = data and classification. Local = rendering.**

Each surface (CLI text/ANSI, UI HTML/CSS, SVG hex fills) owns its own rendering. What gets shared is the underlying typed data: enums, classifiers, parsers, derivations. A CLI severity colorizer and a UI severity CSS-class function are not duplication — they're two valid renderings of the same classification. The duplication to eliminate is the *classification logic itself* (the switch from `"critical"` to a level), and any data-shape projection that has no business being re-implemented per surface (multi-format timestamp parsing, level thresholding, run-status derivation, license stage labels).

This reshapes the plan from "shared colorizer functions" to "shared enums + classifiers, with rendering staying local." Cleaner separation of concerns; smaller blast radius per commit; rendering can diverge per surface without breaking SoT.

## Phase 1 — Shared data + per-surface rendering refactor

### A. Package landing

Domain-local SoTs hold the typed enums and classifiers. One shared `server/uifmt` package holds genuinely cross-cutting *data* (palette hex constants, multi-format timestamp parser). Rendering functions stay where they're rendered: `cli/format.go` for ANSI, `ui/render.go` for template funcs, `ui/viz_common.go` for SVG hex fills.

Layout:
- `server/uifmt/palette.go` — Catppuccin Mocha hex constants only (data, not ANSI escapes)
- `server/uifmt/time.go` — `ParseTimestamp(string) (time.Time, error)` (parsing only; formatting stays per-surface)
- `server/cve/severity.go` — new package; `SeverityLevel` enum, `Classify(string) SeverityLevel`, `Norm(string) string`
- `server/risk/level.go` — already has `Level`; add `ScoreToLevel(int) Level` if missing
- `server/convergence/status.go` — `Status` enum, `ClassifyStatus(string) Status`, `ComputeStatus(*db.Run) Status` (folds Phase 2 item 11)
- `server/convergence/action.go` — `Action` enum, `ClassifyAction(string) Action`
- `server/license/` — no new file; `Stage.String()` already exists at `server/license/enforcer.go:53`

### B. Verification before execution

**Item 7 (run actions) — done.** Survey was incomplete:
```
grep -rn "colorAction\|VizActionColor\|ActionTaken" cli/ ui/ server/
grep -rn 'eq .ActionTaken "create"' ui/templates/
```
CLI: `colorAction` defined `cli/preview.go:14`, used `cli/preview.go:75`, `cli/runs.go:122`, `cli/runs.go:262`. UI: `ui/viz_common.go:77 VizActionColor` plus inline ternaries `ui/templates/pages/run-detail.html:54` and `:93`.

**Item 8 (BracketBill) — verified clean. Dropped.**

**Item 6 (trait formatter) — withdrawn.** CLI and UI render trait values differently (`k=v` vs `{"k":"v"}`); per the guiding principle, that's acceptable per-surface rendering. No SoT to consolidate unless the underlying data normalization diverges, which has not been shown.

### C. Execution items

**1. Create `server/uifmt` foundation — palette hexes + timestamp parser**
- `server/uifmt/palette.go`: lift Catppuccin Mocha hex constants from `cli/format.go:33-196` and `ui/viz_common.go:19-34`. Export hexes only (`HexRed`, `HexGreen`, `HexPeach`, etc.). The `useColor` toggle for the CLI stays in `cli/format.go` since it's CLI-specific (TTY detection).
- `server/uifmt/time.go`: extract the multi-format RFC3339 parser from `cli/format.go:215-232 formatTimestamp` as `ParseTimestamp(string) (time.Time, error)`. UI's `ui/render.go:484-492 formatTime(time.Time)` already takes a parsed `time.Time`, so no parser duplication on UI side — but if UI parses RFC3339 strings anywhere, point those callers at `uifmt.ParseTimestamp`.
- `cli/format.go`: keep ANSI escape wrappers (`bold`, `green`, `red`, etc.) local; rebuild them from `uifmt.HexX` constants instead of inline hex literals. CLI's `formatTimestamp` keeps its `t.Format("2006-01-02 15:04")` line — only the parser call moves to `uifmt`.
- `ui/viz_common.go:19-34`: delete local hex constants; import from `uifmt`.
- `ui/render.go`: keep `formatTime` local (each surface owns its formatting). If any UI code parses RFC3339 strings, point at `uifmt.ParseTimestamp`.
- **CLAUDE.md update in this commit:** the line "The canonical Catppuccin Mocha palette constants live in `cli/format.go`" becomes "...live in `server/uifmt/palette.go`."
- Test: `go build ./...`; `grep -rn '#1e1e2e\|#cdd6f4\|#f38ba8' cli/ ui/ | grep -v uifmt` returns zero hex literals outside `uifmt`.

**2. CVE severity — `server/cve/severity.go`**
- New package. Exports:
  - `type SeverityLevel int` with constants `SevCritical`, `SevHigh`, `SevMedium`, `SevLow`, `SevInfo`, `SevUnassigned`.
  - `Classify(string) SeverityLevel` — input vocab handling (lowercase/whitespace tolerance) → enum.
  - `Norm(string) string` — canonical lowercase form (`"critical"`, `"high"`, …) for URLs and JSON keys.
- `cli/cve.go:64-71`: replace string switch with `switch cve.Classify(h.Severity)`. CLI-side coloring stays in `cli/cve.go`. **Note:** the existing CLI switch only colors critical/high/medium; the reshape is a chance to extend coverage to low/info if desired, but that's a CLI-render decision, not SoT scope.
- `ui/render.go`: `secSeverityClass`, `secSeverityLabel`, `secSeverityNorm` (lines 63-88) keep their bodies but switch on `cve.SeverityLevel` and call `cve.Norm()` for the norm helper.
- Test: `grep -rn 'case "critical"' cli/ ui/` returns matches only in `server/cve/severity.go`.

**3. Risk level — `server/risk/level.go` (audit + extend existing)**
- `risk.Level` already exists. If `ScoreToLevel(int) Level` (the threshold mapping that turns a score into a bucket) doesn't exist as an exported function, add it. This is the data shared between CLI and UI; the bucketing rule is currently re-implemented in both `cli/risk.go:272-298` and `ui/render.go:180-191`.
- `cli/risk.go:272-298`: keep `colorRiskScore`/`colorRiskLevel` local; rewrite to dispatch on `risk.ScoreToLevel(score)` / `risk.Level` directly.
- `ui/render.go:180-191 riskColor`: rewrite to dispatch on `risk.ScoreToLevel(score)` / `risk.Level`.
- Test: `grep -rn 'score >= 80\|score >= 60' cli/ ui/` returns no threshold literals outside `server/risk/`.

**4. Convergence status + action enums (folds Phase 2 item 11)**
- `server/convergence/status.go`: `type Status int` with constants `Converged`, `Relapsed`, `Diverged`, `Failed`, `Online`, `Offline`, `Stale`, `Unknown`. `(Status).String() string` for canonical lowercase. `ClassifyStatus(string) Status` for parsing input. **`ComputeStatus(*db.Run) Status`** for the run-status derivation (Phase 2 item 11 — verified no existing implementation).
- `server/convergence/action.go`: `type Action int` with `Create`, `Update`, `Delete`, `Noop`. `(Action).String()`, `ClassifyAction(string) Action`.
- `cli/format.go:199-212 colorStatus`: keep local; rewrite to dispatch on `convergence.Status`.
- `cli/preview.go:14-27 colorAction`: keep local; rewrite to dispatch on `convergence.Action`.
- `ui/render.go:466-481 statusColor`: keep local; rewrite to dispatch on `convergence.Status`.
- `ui/viz_common.go:59-74 VizStatusColor`, `:77-88 VizActionColor`: keep local (SVG hex rendering); rewrite to dispatch on enums and pull hexes from `uifmt`.
- `ui/templates/pages/run-detail.html:54, :93`: replace inline ternaries with a new template func `actionCSSClass` registered in `ui/render.go funcMap()` that dispatches on `convergence.Action` and returns `success|info|danger|muted`.
- **Audit pass:** any code that derives "did this run converge / relapse / diverge / fail" from raw run data should call `convergence.ComputeStatus(*db.Run)` instead of computing inline. Survey identified this lives at run-detail, run-list, and convergence-history layers — point them all at the new function.
- Test: `grep -rn 'case "converged"' cli/ ui/ server/` returns matches only in `server/convergence/`.

**5. License stage label dedup**
- `cli/license.go:156-166`: replace the int → string switch with `stageName := license.Stage(status.Stage).String()`. `Stage.String()` already exists at `server/license/enforcer.go:53`. **No new file in `server/license/`.**
- `ui/templates/pages/license.html:10, :40`: the inline ternary `{{if eq .StageNum 0}}success{{else if eq .StageNum 1}}warning{{else}}danger{{end}}` is rendering — keep it local. Optional cleanup: extract to a template func in `ui/render.go funcMap()` for symmetry, but no shared package required.
- **Ripple flagged:** changing the handler at `ui/handler.go:7115` to expose typed `Stage` instead of `StageNum` would touch `ui/handler.go:713` (struct field) and `ui/handler.go:7114` (assignment) plus the two template references. Optional — `StageNum` works fine as an int. Decide before doing.
- Test: `grep -rn '"Compliant"\|"Grace Period"\|"Enrollment Block"\|"Service Stop"' cli/ ui/` returns matches only in `server/license/enforcer.go`.

### D. Sequencing — Phase 1 commits

**Commit 1.** `server/uifmt` foundation (item 1) + CLAUDE.md update. Foundation; everything else imports `uifmt`.
**Commit 2.** Domain enums for severity + risk (items 2, 3). Two small new files; CLI/UI renderers retargeted to consume the enums. No template structural changes.
**Commit 3.** Convergence status + action enums + run-status derivation (item 4, folds Phase 2 item 11). Largest commit — touches `server/convergence/`, both surfaces, and `run-detail.html` template. Test plan: build + actually run vigosrv, eyeball run-detail page and run list, verify CLI run output renders correctly.
**Commit 4.** License stage label dedup (item 5). Smallest. Either with or without the `StageNum → Stage` handler ripple, depending on the call.

### E. Out of scope (Phase 1)

- Risk score math in `server/risk/score.go` — only the level bucketing moves; `ScoreEnvoy`/`ScoreFleet` stay put.
- `VizComplianceColor` (`ui/viz_common.go:90-102`) — single callsite, no CLI parallel.
- Rust agent. Nothing in `agent/` consumes these helpers.
- Trait formatter (item 6 in original plan) — withdrawn; per-surface rendering is fine.
- `connwaer/`, `tessera/license`, protobuf generation.
- Item 8 (BracketBill) — verified no CLI re-implementation exists.
- Existing `humanDuration`/`humanMs`/`humanBytes` template helpers (`ui/render.go:206-237`) — no CLI parallel.
- ANSI escape generation in `cli/format.go` — CLI-only rendering, stays local.
- HTML/SVG rendering in `ui/` — UI-only rendering, stays local.

---

## Phase 2 — Server-internal consolidations

Identified in the same survey. Mostly server-package duplication rather than CLI↔UI; payoffs vary. Some items still need verification before execution.

### High confidence — duplication observed

**7. Framework findings partition — caller hoist.** `server/api/report.go:347-374 partitionEnvoyFindings` is canonical, but its three callers (`ComputeEnvoyFrameworkStatus`, `ComputeEnvoyFrameworkDetail`, `ComputeFrameworkReport`) each repeat the `docCovered` resolver lookup. Hoist the lookup to a single caller-side step and pass through. ~30 min, zero risk.

**8. API error envelope.** `server/api/envoys.go:48-54` and other handlers mix `http.Error` with `writeJSONError` and inconsistent payload shapes. Create `server/api/errors.go` with `APIError{Code, Message}` + `writeError(w, code, msg)`; audit all handlers. High churn, low payoff per fix, matters for a uniform CLI error-handling story.

**9. Trait key vocabulary across Go ↔ Rust.** `connwaer.*` trait keys are hardcoded string literals in `server/api/report.go:74` and the Rust collectors. Silent breakage if either side renames. Generate from a shared list (proto enum or codegen). **Conservative call — only worth doing if drift has actually bitten.** If trait names are stable, this is over-engineering.

### Medium confidence — pattern fits, needs a quick verify

**10. Compliance control coverage index.** `server/api/report.go:755-795` and `EnvoyStandardCoverage` (`server/config/loader.go:1537`) both walk control → module mappings per envoy/framework — likely re-derived per request. New `server/compliance/control_coverage.go` with a memoized index built once at config publish, consumed by both. Preserve the AppControls vs. framework-controls boundary (CLAUDE.md is firm: AppControls never inflate envoy or fleet scoring).

**~~11. Convergence status derivation~~** — **folded into Phase 1 Commit 3** (item 4). `ComputeStatus(*db.Run) Status` lives in `server/convergence/status.go` alongside the `Status` enum. No separate Phase 2 commit needed.

**12. Module DAG topological sort — DRIFT.** **VERIFIED** by Phase 2 sweep. Cycle detection exists at `server/validate/validate.go:425-471` (DFS over `depends_on + before` edges). Module *ordering* in the check-in response does NOT exist — `server/grpc/policy.go:56-79` iterates `cfg.Modules` as-is without reordering. These are two separate concerns: validation already covered, execution-order is a missing feature, not a duplication. Reframe as: "ensure module response order respects `depends_on` (new feature, not consolidation)."

**~~13. Hostname pattern matching~~** — **STRUCK by Phase 2 sweep.** `server/registry/registry.go:173-207, 228-240` does walk patterns via `filepath.Match()`, but the use case is fundamentally different from `server/config/loader.go:551-559`'s routing index — registry must validate tokens against each matching pattern atomically. No consolidation opportunity.

**~~14. Approved pattern syntactic validation~~** — **STRUCK by Phase 2 sweep.** Neither `server/grpc/admin.go:36-64 AddApprovedPattern` nor `cli/approved.go:19-45` validates pattern syntax at creation time. Invalid patterns are only caught at enrollment when `filepath.Match()` runs (`server/registry/registry.go:174-175`). This is a missing feature, not duplication.

**15. `SecretError` adoption.** `server/config/loader.go:28-84` already exports a backend-aware `SecretError` with `Hint()`, but gRPC handlers likely hand-build secret-resolution errors. Propagate `SecretError` up to handlers; CLI gets better diagnostics for free.

### Low confidence — verify before counting

**16. Dashboard convergence summary — DRIFT.** **VERIFIED** by Phase 2 sweep. The server exports `fleetIndex.ConvergenceSummary()` at `server/metrics/collector.go:43`, but the UI dashboard does NOT consume it — `ui/fleet_cache.go:163-262` independently calls `convergence.ComputeConvergence(runs)` (line 183) on database-loaded runs. Real consolidation: either point UI at `ConvergenceSummary()` or extract the `ComputeConvergence` core to a shared package both call. Confidence promoted from Low → High.

### Phase 2 sequencing

**Commit 5.** Item 7 (framework findings partition). Trivial, do first to clear the easy win.
**Commit 6.** Item 10 (compliance control coverage index) after a half-day verify. Largest payoff in Phase 2 because compliance touchpoints sprawl.
**Commit 7.** Item 16 (dashboard convergence summary) — UI consumes the server-side summary or both call a shared core.
**Commit 8.** Item 8 (API error envelope) standalone cleanup. Noisy, low risk per file, do when you have time for the churn.

Item 12 (DAG sort / module ordering) is reclassified as a missing feature rather than consolidation; track separately. Item 9 (cross-language trait keys) and item 15 (`SecretError` adoption) sit in backlog — neither is urgent.

### Phase 2 — out of scope

- gRPC↔REST handler thin-wrapper audit — would need a per-RPC pass; defer until a specific endpoint shows skew.
- SQLC row → API struct re-projection — defer; flag if a specific endpoint shows skew.
- Sorting/filtering defaults — only worth it once 3+ inconsistent callsites exist.
