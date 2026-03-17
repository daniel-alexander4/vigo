# Automated Regression Tests

Hobnob includes an automated regression test suite in `test/audit/` that catches documentation drift, missing docs, undocumented endpoints, ignored errors, secrets in logs, and count inconsistencies. These tests run as part of `go test ./...` and fail when problems are introduced.

## Running

```bash
go test ./test/audit/... -v
```

## Tests

### 1. TestDocLinksNotBroken

Walks every `.md` file in `docs/` and resolves relative markdown links. Fails if any linked file does not exist. Skips external URLs, anchors, images, and code blocks.

### 2. TestEveryExecutorHasDocPage

Parses `agent-rs/src/runner/mod.rs` to extract all registered executor type names (`executors.insert("name", ...)`). Verifies each has a corresponding page in `docs/executors/`. Accepts platform-suffixed variants (e.g., `acl_windows.md` for `acl`).

### 3. TestEveryCLICommandHasDocPage

Reads `cli/main.go` to find all top-level commands registered via `rootCmd.AddCommand(...)`, resolves each variable's `Use:` field, and verifies a matching doc page exists in `docs/cli/`.

### 4. TestRESTEndpointsDocumented

Scans `server/api/` and `server/bootstrap/` for route registrations (`HandleFunc("METHOD /api/v1/...")`) and verifies each appears in `docs/api/rest.md`. Path parameters are normalized for matching.

### 5. TestNoIgnoredErrors

Walks `server/` Go files (excluding tests and generated code) looking for discarded error returns from dangerous functions: `os.Open`, `os.Create`, `os.WriteFile`, `os.Remove`, `json.Unmarshal`, `io.Copy`, `io.ReadAll`, etc. Lines with explanatory comments (`// best-effort`, `// intentional`, etc.) are exempt.

### 6. TestNoSecretsInLogs

Scans `server/` for `slog`/`log` calls that pass sensitive field names (`password`, `secret`, `private_key`, `api_key`) with variable values (not string literals). Safe field names like `token_id`, `password_hash`, `key_path` are exempt.

### 7. TestExecutorCountConsistent

Counts unique executor types in `agent-rs/src/runner/mod.rs` and compares against the claimed count in `docs/_index.md` and `docs/executors/_index.md`. Fails on mismatch.

### 8. TestCLICommandCountConsistent

Extracts all CLI command names from `cli/main.go` and verifies each is mentioned in `CLAUDE.md`.
