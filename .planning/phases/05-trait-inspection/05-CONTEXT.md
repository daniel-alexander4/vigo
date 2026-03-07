# Phase 5: Trait Inspection - Context

**Gathered:** 2026-03-07
**Status:** Ready for planning

<domain>
## Phase Boundary

Admins can inspect collected traits for any node — locally on the agent via `isogov-agent traits` and remotely via `gvctl clients traits <hostname>` + REST endpoint. No new trait collection capabilities; this surfaces existing traits data.

</domain>

<decisions>
## Implementation Decisions

### Output Format
- JSON only — no YAML support
- Pretty-printed by default (indented with 2 spaces)
- REST endpoint wraps traits in an envelope: `{ "hostname": "...", "collected_at": "...", "traits": { ... } }`

### Agent Command UX
- `isogov-agent traits` — new cobra subcommand, fully offline (no server connection, no auth, no client UUID needed)
- Clean stdout output — suppress slog for traits command, pure JSON to stdout, errors to stderr
- Positional args for collector filtering: `isogov-agent traits os` shows only OS traits, `isogov-agent traits os hardware` shows both
- Dot-path accessor: `isogov-agent traits os.distro` returns just the value (unquoted string for scalars, JSON for objects)

### gvctl Output Style
- `gvctl clients traits <hostname>` — looks up by hostname (not UUID like other clients subcommands)
- Pretty JSON output matching agent format — consistent, pipeable to jq
- Dot-path accessor: `gvctl clients traits web01 os.distro` — same filtering as agent
- Envelope shown in full output, just the traits value shown when dot-path used

### Error Handling
- Hostname not found: clear error message + non-zero exit ("client 'web99' not found")
- No traits stored yet: error message + non-zero exit ("no traits available for 'web01' — client has not reported traits yet")
- Bad dot-path: error + exit 1 ("trait path 'os.nonexistent' not found")
- Agent collector failure: partial results — print what succeeded, skip failed collectors (matches existing CollectAll behavior)

### Claude's Discretion
- Exact REST endpoint route naming convention (follow existing API patterns)
- DB query approach for hostname-based lookup (may need new query or join)
- Test structure and coverage strategy

</decisions>

<code_context>
## Existing Code Insights

### Reusable Assets
- `agent/facts/collector.go`: `Collector` interface with `CollectAll()` returning `map[string]any`, `DefaultCollectors()` factory
- `agent/facts/{os,hardware,network,packages}.go`: Four built-in collectors with `Name()` returning category keys
- `server/db/facts.sql.go`: `GetLatestClientFacts()` returns `ClientFact` with `json.RawMessage` — already stores traits as JSON
- `cli/client.go`: `apiClient` struct with `get/post/delete` methods and 30s timeout
- `cli/format.go`: Shared formatting utilities (`formatTimestamp`, etc.)

### Established Patterns
- Agent CLI: cobra commands with `init()` registration in `agent/main.go` — `rootCmd.AddCommand(traitsCmd)` pattern
- gvctl CLI: one Go file per subcommand group (`cli/clients.go`), `init()` adds to parent
- REST API: handlers in `server/api/`, routes registered in server setup
- DB queries: sqlc-generated from `.sql` files in `server/db/queries/`

### Integration Points
- Agent: `facts.CollectAll(ctx, facts.DefaultCollectors())` already used in `runCmd` — reuse for traits command
- Server API: new handler in `server/api/` + route registration
- DB: `GetLatestClientFacts(clientID)` exists but takes UUID — need hostname→UUID lookup for the new endpoint
- gvctl: new subcommand under `clientsCmd` in `cli/clients.go`

</code_context>

<specifics>
## Specific Ideas

- Dot-path accessor should feel like `jq` — `isogov-agent traits os.distro` just prints the raw value, no wrapping
- For scalar values, print the raw value (no quotes); for objects/arrays, print as JSON
- gvctl envelope mirrors what the REST API returns — no client-side reformatting

</specifics>

<deferred>
## Deferred Ideas

None — discussion stayed within phase scope

</deferred>

---

*Phase: 05-trait-inspection*
*Context gathered: 2026-03-07*
