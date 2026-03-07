---
phase: quick
plan: 1
type: execute
wave: 1
depends_on: []
files_modified:
  - server/config/template.go
  - server/config/loader_test.go
  - server/grpc/checkin.go
autonomous: true
requirements: [QUICK-1]

must_haves:
  truths:
    - "A var like `greeting: \"Hello from {{ .Facts.os.distro }}\"` resolves to `Hello from ubuntu` at check-in time"
    - "Vars without template syntax pass through unchanged"
    - "Non-string var values are skipped (no panic on int/bool/map vars)"
    - "Rendered vars are available to resource attribute template rendering"
    - "Template errors in vars surface as check-in errors (not silent failures)"
  artifacts:
    - path: "server/config/template.go"
      provides: "RenderVars function"
      exports: ["RenderVars"]
    - path: "server/grpc/checkin.go"
      provides: "Var rendering call before resource attribute rendering"
  key_links:
    - from: "server/grpc/checkin.go"
      to: "server/config/template.go"
      via: "config.RenderVars(vars, facts) call before configToProtoModules"
      pattern: "config\\.RenderVars"
---

<objective>
Render module vars through RenderTemplate so vars can use Go template syntax (`.Facts`, `.Vars`).

Purpose: Currently only resource attributes are rendered through `config.RenderTemplate` at check-in time. Vars are passed raw, so a var like `greeting: "Hello from {{ .Facts.os.distro }}"` would reach the agent unresolved. This adds a var rendering pass before resource attribute rendering.

Output: `RenderVars` function in `server/config/template.go`, wired into `server/grpc/checkin.go` CheckIn handler.
</objective>

<execution_context>
@/home/dan/.claude/get-shit-done/workflows/execute-plan.md
@/home/dan/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
@server/config/template.go
@server/config/types.go
@server/grpc/checkin.go
@server/config/loader_test.go

<interfaces>
From server/config/template.go:
```go
// RenderTemplate renders a Go template string with .Facts and .Vars namespaces.
func RenderTemplate(tmpl string, facts map[string]any, vars map[string]any) (string, error)
```

From server/config/types.go:
```go
type ClientConfig struct {
    Client      string            `yaml:"client"`
    Environment string            `yaml:"environment"`
    Modules     []ModuleConfig    `yaml:"modules"`
    Vars        map[string]any    `yaml:"vars"`
    EnvVars     map[string]map[string]any `yaml:"env_vars"`
}
```

From server/grpc/checkin.go (lines 71-80):
```go
vars := cfg.Vars
if vars == nil {
    vars = make(map[string]any)
}

modules, err := configToProtoModules(cfg, facts, vars)
```
</interfaces>
</context>

<tasks>

<task type="auto" tdd="true">
  <name>Task 1: Add RenderVars function and wire into CheckIn</name>
  <files>server/config/template.go, server/config/loader_test.go, server/grpc/checkin.go</files>
  <behavior>
    - Test: var with `.Facts` reference renders correctly (e.g., `"Hello from {{ .Facts.os.distro }}"` with facts `{"os": {"distro": "ubuntu"}}` becomes `"Hello from ubuntu"`)
    - Test: var with `.Vars` reference renders correctly (e.g., `"port={{ .Vars.base_port }}"` with another var `base_port: "8080"` becomes `"port=8080"`)
    - Test: plain string var without template syntax passes through unchanged
    - Test: non-string var values (int, bool) are skipped without error
    - Test: invalid template syntax in a var returns an error
    - Test: empty vars map returns empty map without error
  </behavior>
  <action>
1. In `server/config/template.go`, add a `RenderVars` function:
   ```go
   // RenderVars renders Go template syntax in var values. Only string values
   // are rendered; non-string values pass through unchanged. Facts are available
   // as .Facts, and other vars (in their current state) are available as .Vars.
   // This should be called BEFORE resource attribute rendering so that rendered
   // vars are available to resource templates.
   func RenderVars(vars map[string]any, facts map[string]any) (map[string]any, error) {
   ```
   - Iterate over all vars. For each string value, call `RenderTemplate(val, facts, vars)`.
   - Store rendered value back. Non-string values copy through.
   - Return the new map (or modify in place and return). On any template error, return a wrapped error identifying the var name.
   - Note: This is a single-pass render. A var referencing another var will see the OTHER var's pre-rendered value (which is fine for the primary use case of .Facts references). Document this in the function comment.

2. In `server/config/loader_test.go`, add `TestRenderVars` with the test cases from the behavior block above. Import from the same `config` package (internal test file).

3. In `server/grpc/checkin.go`, in the `CheckIn` function, after the `vars` nil-check block (after line 74) and BEFORE the `configToProtoModules` call (line 76), add:
   ```go
   vars, err = config.RenderVars(vars, facts)
   if err != nil {
       slog.Error("failed to render vars", "client_id", clientID, "error", err)
       return nil, status.Errorf(codes.Internal, "rendering vars: %v", err)
   }
   ```
   This shadows the `err` from the previous block (which is already checked), so use `=` not `:=` since `vars` is already declared. Actually, `err` is already declared from the facts block, so `vars, err = config.RenderVars(vars, facts)` works.
  </action>
  <verify>
    <automated>cd /home/dan/repos/isogov && go test ./server/config/... -run TestRenderVars -v && go build ./...</automated>
  </verify>
  <done>
    - RenderVars function exists in server/config/template.go
    - All TestRenderVars subtests pass (facts ref, vars ref, plain passthrough, non-string skip, invalid template error, empty map)
    - CheckIn handler calls RenderVars before configToProtoModules
    - Full project builds cleanly
  </done>
</task>

</tasks>

<verification>
```bash
cd /home/dan/repos/isogov && go test ./server/config/... -v && go build ./...
```
</verification>

<success_criteria>
- `RenderVars` renders `.Facts` references in var values at check-in time
- Non-string var values pass through without error
- Template errors in vars produce clear error messages identifying the var name
- Existing `TestTemplateRendering` still passes (no regression)
- Full project compiles
</success_criteria>

<output>
After completion, create `.planning/quick/1-render-module-vars-through-rendertemplat/1-SUMMARY.md`
</output>
