# Phase 6: Custom Traits - Context

**Gathered:** 2026-03-07
**Status:** Ready for planning

<domain>
## Phase Boundary

Admins can drop executable scripts into a server directory and have them automatically distributed to agents, executed with a timeout, and their output merged into the traits map under `custom.*`. This phase adds the server-side manifest builder, a new gRPC download RPC, agent-side sync/cache/execute logic, and trait merging. No new CLI commands or UI changes — custom traits appear through the existing trait inspection tools from Phase 5.

</domain>

<decisions>
## Implementation Decisions

### Manifest & Sync Model
- Manifest embedded in CheckInResponse — add a repeated `CustomTraitEntry` (name + SHA256 checksum) to the existing proto message
- Agent downloads new/changed executables via a new `DownloadCustomTrait` gRPC RPC on the agent service — keeps everything on the authenticated mTLS channel
- Agent caches executables at `/var/lib/isogov/traits/` — consistent with existing `/var/lib/isogov/snapshots/` convention
- Sync happens before fact collection in the check-in cycle: sync traits from manifest → execute custom traits → merge into facts map → report facts → check-in → execute modules. Custom traits are available in the same cycle's templates and `when:` expressions

### Executable Conventions
- Executables output a JSON object to stdout — flat or nested, parsed as `map[string]any`
- Trait name derived from filename without extension — file `app-version` (or `app-version.sh`) becomes `custom.app-version`
- Default execution timeout: 10 seconds (global, not per-executable)
- Server only includes files with the executable bit set in the manifest — non-executable files (READMEs, configs) are silently skipped

### Trait Merging & Namespacing
- Hard namespace separation — custom traits live exclusively under `custom.*`, never collide with built-in trait keys (os, hardware, network, packages)
- Failed or timed-out executables: key is simply absent from `custom.*` — no error sentinels in the traits map. Warning logged on agent
- Custom traits merged into the same facts map reported via ReportFacts — visible through `gvctl clients traits`, REST API, and UI with no changes needed
- Full nested JSON supported — executables can output arbitrarily nested objects, accessible via `{{ .Traits.custom.myapp.config.version }}`

### Server-Side Config
- Traits directory: `traits/` at repo root (separate top-level directory, not under client-configs)
- Fixed convention path — no server.yaml config needed. Server looks for `traits/` relative to working directory
- Server scans the directory and computes SHA256 checksums when building each CheckInResponse — no fsnotify watcher needed (trait files change rarely)
- Timeout is a global 10s default, optionally overridable in server.yaml under a `custom_traits` section if needed in the future

### Claude's Discretion
- Proto message design for manifest entries and download RPC
- Agent cache manifest format (tracking local checksums)
- Exact error handling for malformed JSON output from executables
- Test structure and mock strategy for gRPC download testing
- Whether to create `traits/` directory in deploy.sh

</decisions>

<code_context>
## Existing Code Insights

### Reusable Assets
- `agent/facts/collector.go`: `Collector` interface with `CollectAll()` — custom trait execution could implement this or use a parallel pattern
- `agent/checkin/loop.go`: Loop flow (collect facts → report → check-in → execute) — custom trait sync slots in before fact collection
- `agent/checkin/client.go`: gRPC client wrapper with signing — new `DownloadCustomTrait` method follows same pattern
- `agent/update/`: Binary download + checksum verification pattern — reusable for trait executable downloads

### Established Patterns
- Proto: single `isogov.proto` with agent-facing and admin-facing services
- gRPC client: `Client` struct in `agent/checkin/client.go` wraps all agent→server RPCs
- File operations: atomic write pattern from file executor, SHA256 checksums throughout
- Error resilience: non-fatal warnings (like fact collector failures) logged and skipped

### Integration Points
- `proto/isogov.proto`: Add `CustomTraitEntry` message + field on `CheckInResponse` + new `DownloadCustomTrait` RPC
- `server/grpc/checkin.go`: `CheckIn` handler needs to scan traits dir and build manifest
- `agent/checkin/loop.go`: `Loop()` needs custom trait sync + execution before `CollectAll()`
- `agent/checkin/client.go`: New `DownloadCustomTrait` method
- `agent/facts/collector.go`: `CollectAll` result gets `custom.*` keys merged in

</code_context>

<specifics>
## Specific Ideas

- Custom traits should "just work" with existing Phase 5 tooling — `isogov-agent traits custom` and `gvctl clients traits web01 custom.app-version` should work without any changes to the traits command
- The sync model mirrors how package managers work: compare checksums, download only what changed, cache locally
- Trait executables should be language-agnostic — any executable that outputs JSON to stdout works (shell scripts, Python, compiled Go binaries, etc.)

</specifics>

<deferred>
## Deferred Ideas

None — discussion stayed within phase scope

</deferred>

---

*Phase: 06-custom-traits*
*Context gathered: 2026-03-07*
