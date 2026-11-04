// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

// Package audit provides automated regression tests that catch documentation
// drift, missing docs, undocumented endpoints, ignored errors, secrets in logs,
// and count inconsistencies. These tests run as part of `go test ./...` and fail
// when problems are introduced.
package audit

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// repoRoot walks up from the test file until it finds go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root (no go.mod found)")
		}
		dir = parent
	}
}

// ---------- 1. TestDocLinksNotBroken ----------

func TestDocLinksNotBroken(t *testing.T) {
	root := repoRoot(t)
	docsDir := filepath.Join(root, "docs")

	// Match markdown links: [text](path) — capture the path portion.
	// Requires '[' before '](' to avoid matching bare ](...)  inside code.
	linkRe := regexp.MustCompile(`\[([^\]]*)\]\(([^)]+)\)`)

	var broken []string

	err := filepath.Walk(docsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(docsDir, path)
		// docs/internal.docs/ holds engineering and strategy memos — not operator-facing, exempt from link checks.
		if rel == "internal.docs" || strings.HasPrefix(rel, "internal.docs"+string(filepath.Separator)) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		// docs/fr/ is the generated French mirror (i18n): it preserves the English
		// link targets byte-for-byte, so link validity is already covered by the
		// English tree above. Links into untranslated subtrees (e.g. the gated
		// docs/reference/) resolve to no docs/fr/ file but the docs handler falls
		// back to the English page per-file at runtime — a filesystem walk can't
		// see that fallback, so checking fr/ links here yields false positives.
		// Drift of fr/ vs en/ is guarded separately by TestFrenchDocsNotStale.
		if rel == "fr" || strings.HasPrefix(rel, "fr"+string(filepath.Separator)) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		lines := strings.Split(string(data), "\n")
		inCodeBlock := false
		for lineNo, line := range lines {
			// Track fenced code blocks (``` or ~~~).
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
				inCodeBlock = !inCodeBlock
				continue
			}
			if inCodeBlock {
				continue
			}

			matches := linkRe.FindAllStringSubmatch(line, -1)
			for _, m := range matches {
				target := m[2]

				// Skip targets that contain quotes — these are code fragments
				// (e.g., Jinja templates like salt['pillar.get']('key', 'default'))
				// not markdown links.
				if strings.ContainsAny(target, "'\"") {
					continue
				}
				// Skip targets with commas or spaces — not valid file paths.
				if strings.ContainsAny(target, ", ") {
					continue
				}

				// Skip external URLs.
				if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
					continue
				}
				// Skip anchor-only links.
				if strings.HasPrefix(target, "#") {
					continue
				}
				// Skip image links (common patterns).
				lower := strings.ToLower(target)
				if strings.HasSuffix(lower, ".png") || strings.HasSuffix(lower, ".jpg") ||
					strings.HasSuffix(lower, ".jpeg") || strings.HasSuffix(lower, ".gif") ||
					strings.HasSuffix(lower, ".svg") || strings.HasSuffix(lower, ".webp") {
					continue
				}
				// Skip mailto links.
				if strings.HasPrefix(target, "mailto:") {
					continue
				}

				// Strip fragment.
				if idx := strings.Index(target, "#"); idx >= 0 {
					target = target[:idx]
				}
				// Skip if empty after stripping fragment (was just #anchor).
				if target == "" {
					continue
				}

				// Resolve relative to the source file's directory.
				sourceDir := filepath.Dir(path)
				resolved := filepath.Join(sourceDir, target)
				resolved = filepath.Clean(resolved)

				if _, err := os.Stat(resolved); os.IsNotExist(err) {
					relSource, _ := filepath.Rel(docsDir, path)
					broken = append(broken, fmt.Sprintf("%s:%d -> %s", relSource, lineNo+1, target))
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking docs: %v", err)
	}

	if len(broken) > 0 {
		t.Errorf("found %d broken doc links:\n  %s", len(broken), strings.Join(broken, "\n  "))
	}
}

// ---------- 2. TestEveryExecutorHasDocPage ----------

func TestEveryExecutorHasDocPage(t *testing.T) {
	root := repoRoot(t)

	modPath := filepath.Join(root, "agent", "src", "runner", "mod.rs")
	data, err := os.ReadFile(modPath)
	if err != nil {
		t.Fatalf("reading runner/mod.rs: %v", err)
	}

	// Extract unique executor type names from executors.insert("name", ...) lines.
	insertRe := regexp.MustCompile(`executors\.insert\("([^"]+)"`)
	seen := map[string]bool{}
	for _, m := range insertRe.FindAllStringSubmatch(string(data), -1) {
		name := m[1]
		// Skip test mocks.
		if name == "mock" {
			continue
		}
		seen[name] = true
	}

	if len(seen) == 0 {
		t.Fatal("found no executor types in runner/mod.rs")
	}

	// Some executors are registered under a short name (e.g., "registry")
	// but their doc pages use the platform-qualified name (e.g.,
	// "registry_windows.md"). Accept either the exact name or any
	// <name>_<platform>.md variant.
	docsDir := filepath.Join(root, "docs", "reference", "vigo", "resources")

	// Build a set of all available doc page base names (without .md).
	docEntries, _ := os.ReadDir(docsDir)
	docNames := map[string]bool{}
	for _, e := range docEntries {
		if strings.HasSuffix(e.Name(), ".md") {
			docNames[strings.TrimSuffix(e.Name(), ".md")] = true
		}
	}

	var missing []string
	for name := range seen {
		// Direct match.
		if docNames[name] {
			continue
		}
		// Platform-suffixed match: look for <name>_<platform>.md.
		found := false
		for docName := range docNames {
			if strings.HasPrefix(docName, name+"_") {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, name)
		}
	}

	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("executors missing doc pages in docs/reference/vigo/resources/:\n  %s", strings.Join(missing, "\n  "))
	}
}

// ---------- 2b. TestEveryTraitHasDocPage ----------

func TestEveryTraitHasDocPage(t *testing.T) {
	root := repoRoot(t)

	// Canonical collector name strings from the from_name match arms
	// ("name" => Some(Self::Variant)) — shared deriver, same table the agent
	// uses to resolve a requested collector and the numbers-of-record parity
	// test counts. The string is authoritative (CamelCase enum variants don't
	// map 1:1 to the snake_case page basenames).
	seen := traitCollectorNames(t, root)

	// Some collectors ship a base page plus per-OS supplements (e.g.
	// os.md + os_windows.md). Accept either the exact name or any
	// <name>_<platform>.md variant — same convention as the executor
	// pages. (The non-enum network_device.md page documents the
	// network-device trait family and has no CollectorKind variant; it
	// is not required by this test and simply ignored.)
	docsDir := filepath.Join(root, "docs", "reference", "vigo", "traits")

	docEntries, _ := os.ReadDir(docsDir)
	docNames := map[string]bool{}
	for _, e := range docEntries {
		if strings.HasSuffix(e.Name(), ".md") {
			docNames[strings.TrimSuffix(e.Name(), ".md")] = true
		}
	}

	var missing []string
	for name := range seen {
		if docNames[name] {
			continue
		}
		found := false
		for docName := range docNames {
			if strings.HasPrefix(docName, name+"_") {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, name)
		}
	}

	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("trait collectors missing doc pages in docs/reference/vigo/traits/:\n  %s", strings.Join(missing, "\n  "))
	}
}

// ---------- 2c. TestEveryResourceParamDocumented ----------

// paramDocExcluded lists schema attributes that are intentionally not
// required in a resource's Parameters table. `name` is the universal
// resource-identity/title field (every resource block carries a `name:`),
// read by many executors only to label the ResourceResult — not a
// type-specific configuration param, so pages legitimately omit it.
var paramDocExcluded = map[string]bool{"name": true}

// resourceParamTableNames extracts the documented parameter names from a
// resource doc page: every backticked token in the first column of a table
// under "## Parameters" whose header row's first cell is "Parameter". The
// header gate skips value-reference sub-tables (e.g. container's curator
// Form|Example table, cron's schedule-field table) that also live in the
// Parameters section. Reading *all* backticked tokens in the cell handles
// combined cells like "`image` *or* `artifact`".
func resourceParamTableNames(md string) map[string]bool {
	names := map[string]bool{}
	sec := regexp.MustCompile(`(?ms)^##\s+Parameters\s*$(.*?)(?:^##\s|\z)`).FindStringSubmatch(md)
	if sec == nil {
		return names
	}
	backtick := regexp.MustCompile("`([^`]+)`")
	inParamTable := false
	for _, line := range strings.Split(sec[1], "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "|") {
			inParamTable = false
			continue
		}
		cells := strings.Split(trimmed, "|") // ["", c1, c2, ..., ""]
		if len(cells) < 3 {
			continue
		}
		first := strings.TrimSpace(cells[1])
		if strings.EqualFold(first, "Parameter") {
			inParamTable = true
			continue
		}
		if strings.Trim(first, "-: ") == "" { // separator row
			continue
		}
		if inParamTable {
			for _, m := range backtick.FindAllStringSubmatch(cells[1], -1) {
				names[strings.TrimSpace(m[1])] = true
			}
		}
	}
	return names
}

// TestEveryResourceParamDocumented asserts that every parameter the
// executor source reads (per the generated schema in schemas_gen.go) is
// documented in its resource page's Parameters table. It enforces only the
// schema→doc direction: the scraper undercounts (params read via loops,
// shared helpers, or destructuring escape it — e.g. cron's minute/hour/…
// via a keys array), so the reverse (doc-not-in-schema) would false-flag
// correctly-documented params and is deliberately not checked here.
func TestEveryResourceParamDocumented(t *testing.T) {
	root := repoRoot(t)

	genData, err := os.ReadFile(filepath.Join(root, "server", "validate", "schemas_gen.go"))
	if err != nil {
		t.Fatalf("reading schemas_gen.go: %v", err)
	}
	gen := string(genData)

	// Known type names (gate for distinguishing a per-OS supplement page
	// from a distinct type, e.g. file_line.md / service_recovery_windows.md
	// are their own types, not supplements of file / service).
	known := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\t"([^"]+)": true,`).FindAllStringSubmatch(gen, -1) {
		known[m[1]] = true
	}

	// Parse generatedSchemas: `"type": {"a","b",...},`.
	schemaBlock := regexp.MustCompile(`(?s)var generatedSchemas = map\[string\]\[\]string\{(.*?)\n\}`).FindStringSubmatch(gen)
	if schemaBlock == nil {
		t.Fatal("could not find generatedSchemas in schemas_gen.go")
	}
	rowRe := regexp.MustCompile(`(?m)^\t"([^"]+)":\s*\{(.*)\},`)
	attrRe := regexp.MustCompile(`"([^"]+)"`)

	docsDir := filepath.Join(root, "docs", "reference", "vigo", "resources")

	type drift struct {
		typ     string
		missing []string
	}
	var drifts []drift

	for _, row := range rowRe.FindAllStringSubmatch(schemaBlock[1], -1) {
		typ := row[1]
		var attrs []string
		for _, a := range attrRe.FindAllStringSubmatch(row[2], -1) {
			attrs = append(attrs, a[1])
		}

		// Collect doc params across the base page plus per-OS supplement
		// pages (`<typ>_<os>.md` whose basename is not itself a type).
		docParams := map[string]bool{}
		pages := []string{typ + ".md"}
		supplements, _ := filepath.Glob(filepath.Join(docsDir, typ+"_*.md"))
		for _, sp := range supplements {
			base := strings.TrimSuffix(filepath.Base(sp), ".md")
			if !known[base] {
				pages = append(pages, base+".md")
			}
		}
		any := false
		for _, p := range pages {
			data, err := os.ReadFile(filepath.Join(docsDir, p))
			if err != nil {
				continue
			}
			any = true
			for n := range resourceParamTableNames(string(data)) {
				docParams[n] = true
			}
		}
		if !any {
			continue // no doc page — covered by TestEveryExecutorHasDocPage
		}

		var missing []string
		for _, a := range attrs {
			if paramDocExcluded[a] || docParams[a] {
				continue
			}
			missing = append(missing, a)
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			drifts = append(drifts, drift{typ, missing})
		}
	}

	if len(drifts) > 0 {
		sort.Slice(drifts, func(i, j int) bool { return drifts[i].typ < drifts[j].typ })
		var b strings.Builder
		for _, d := range drifts {
			fmt.Fprintf(&b, "\n  %s: undocumented params %v", d.typ, d.missing)
		}
		t.Errorf("resource params read by the executor but missing from the doc Parameters table:%s\n\n"+
			"Document each in docs/reference/vigo/resources/<type>.md, or (if the param is a dead read) remove it from the executor.", b.String())
	}
}

// ---------- 3. TestEveryCLICommandHasDocPage ----------

func TestEveryCLICommandHasDocPage(t *testing.T) {
	root := repoRoot(t)

	// Read cli/main.go to find AddCommand calls.
	mainPath := filepath.Join(root, "cli", "main.go")
	mainData, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatalf("reading cli/main.go: %v", err)
	}

	// Extract variable names from rootCmd.AddCommand(varName, ...)
	addCmdRe := regexp.MustCompile(`rootCmd\.AddCommand\(([\s\S]*?)\)`)
	m := addCmdRe.FindSubmatch(mainData)
	if m == nil {
		t.Fatal("could not find rootCmd.AddCommand in cli/main.go")
	}

	// Parse the comma-separated variable names.
	varBlock := string(m[1])
	varRe := regexp.MustCompile(`(\w+)`)
	varNames := varRe.FindAllString(varBlock, -1)

	// For each variable, find its Use: field across all .go files in cli/.
	cliDir := filepath.Join(root, "cli")
	cliFiles, err := filepath.Glob(filepath.Join(cliDir, "*.go"))
	if err != nil {
		t.Fatalf("globbing cli/*.go: %v", err)
	}

	// Build a map of variable name -> Use field value.
	// Pattern: var <name> = &cobra.Command{ ... Use: "word ..." ...
	cmdNames := map[string]string{}
	for _, f := range cliFiles {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		content := string(data)

		// Find all var declarations with cobra.Command and their Use field.
		// Use a simple line-by-line scan approach.
		lines := strings.Split(content, "\n")
		var currentVar string
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)

			// Detect variable declaration.
			if strings.Contains(trimmed, "= &cobra.Command{") {
				parts := strings.Fields(trimmed)
				if len(parts) >= 2 && parts[0] == "var" {
					currentVar = parts[1]
				}
			}

			// Detect Use: field.
			if currentVar != "" && strings.HasPrefix(trimmed, `Use:`) {
				useRe := regexp.MustCompile(`Use:\s*"([^"]+)"`)
				um := useRe.FindStringSubmatch(trimmed)
				if um != nil {
					// Take only the first word (command name).
					cmdName := strings.Fields(um[1])[0]
					cmdNames[currentVar] = cmdName
				}
				currentVar = ""
			}
		}
	}

	docsDir := filepath.Join(root, "docs", "reference", "vigocli")

	var missing []string
	for _, varName := range varNames {
		cmdName, ok := cmdNames[varName]
		if !ok {
			t.Logf("warning: could not find Use: field for variable %q", varName)
			continue
		}

		docFile := filepath.Join(docsDir, cmdName+".md")
		if _, err := os.Stat(docFile); os.IsNotExist(err) {
			missing = append(missing, fmt.Sprintf("%s (var: %s)", cmdName, varName))
		}
	}

	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("CLI commands missing doc pages in docs/reference/vigocli/:\n  %s", strings.Join(missing, "\n  "))
	}
}

// ---------- 4. TestRESTEndpointsDocumented ----------

func TestRESTEndpointsDocumented(t *testing.T) {
	root := repoRoot(t)

	// Collect every registered /api/v1/ route across the server + UI trees.
	// A hardcoded directory list was the original gap — `ui/handler.go` and
	// `server/scrier/websocket.go` register /api/v1/ routes that bypassed the
	// drift check. Recursive walk eliminates that class of bug.
	routeRe := regexp.MustCompile(`(?:HandleFunc|Handle)\(\s*"((?:GET|POST|PUT|DELETE|PATCH)\s+/api/v1/[^"]+)"`)

	var routes []string
	for _, dir := range []string{
		filepath.Join(root, "server"),
		filepath.Join(root, "ui"),
	} {
		_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			for _, m := range routeRe.FindAllStringSubmatch(string(data), -1) {
				routes = append(routes, m[1])
			}
			return nil
		})
	}

	if len(routes) == 0 {
		t.Fatal("found no API routes in server/ or ui/")
	}

	// Read docs/reference/api.md.
	restDoc, err := os.ReadFile(filepath.Join(root, "docs", "reference", "api.md"))
	if err != nil {
		t.Fatalf("reading docs/reference/api.md: %v", err)
	}
	docContent := string(restDoc)

	// Normalize route patterns: replace {id} with a generic placeholder for matching.
	normalizeRoute := func(route string) string {
		// "GET /api/v1/tasks/{id}" -> "/api/v1/tasks/{id}"
		parts := strings.SplitN(route, " ", 2)
		if len(parts) == 2 {
			return parts[1]
		}
		return route
	}

	var undocumented []string
	for _, route := range routes {
		path := normalizeRoute(route)
		// Replace Go 1.22 path params {name} with a generic form for doc matching.
		// In the doc, routes appear like /api/v1/tasks/{id} or /api/v1/envoys/{hostname}/traits.
		// Check if the path (or a simplified version) appears in the doc.
		searchPath := path
		// Also try without the path param suffix for table-style docs.
		if strings.Contains(docContent, searchPath) {
			continue
		}
		// Try matching with {id} replaced by other param names.
		// Normalize all {param} to {} for a looser match.
		genericPath := regexp.MustCompile(`\{[^}]+\}`).ReplaceAllString(searchPath, `{`)
		found := false
		// Search for lines containing the path (possibly with different param names).
		for _, line := range strings.Split(docContent, "\n") {
			genericLine := regexp.MustCompile(`\{[^}]+\}`).ReplaceAllString(line, `{`)
			if strings.Contains(genericLine, genericPath) {
				found = true
				break
			}
		}
		if !found {
			undocumented = append(undocumented, route)
		}
	}

	sort.Strings(undocumented)
	if len(undocumented) > 0 {
		t.Errorf("API endpoints not documented in docs/reference/api.md:\n  %s", strings.Join(undocumented, "\n  "))
	}
}

// ---------- 5. TestNoIgnoredErrors ----------

func TestNoIgnoredErrors(t *testing.T) {
	root := repoRoot(t)
	serverDir := filepath.Join(root, "server")

	// Functions where ignoring the error return is a bug.
	dangerousFuncs := []string{
		`os\.Open\(`,
		`os\.Create\(`,
		`os\.OpenFile\(`,
		`os\.Remove\(`,
		`os\.RemoveAll\(`,
		`os\.Mkdir\(`,
		`os\.MkdirAll\(`,
		`json\.Unmarshal\(`,
		`json\.NewDecoder\(`,
		`json\.NewEncoder\(.*\.Encode\(`,
		`io\.Copy\(`,
		`io\.ReadAll\(`,
		`os\.ReadFile\(`,
		`os\.WriteFile\(`,
	}

	// Build a pattern per dangerous func matching any assignment whose
	// error slot is discarded to `_` — both the value-kept form
	// (`data, _ := os.ReadFile(...)`) and the all-blank forms
	// (`_ = os.Remove(...)`, `_, _ := os.Open(...)`). The `_` must sit
	// in the error position: preceded by `, ` (a later return) or
	// standing alone as the sole target. The dangerous-func whitelist
	// is the discriminator — this deliberately does NOT fire on Go's
	// `v, ok := m[k]` / type-assertion / bool-return idioms, whose
	// dropped slot isn't an error and which never call these funcs.
	patterns := make([]*regexp.Regexp, 0, len(dangerousFuncs))
	for _, fn := range dangerousFuncs {
		re := regexp.MustCompile(`(?:,\s*_|(?:^|[^,\w.])_)\s*:?=\s*` + fn)
		patterns = append(patterns, re)
	}

	var violations []string

	err := filepath.Walk(serverDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		// Skip test files.
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// Skip generated code.
		rel, _ := filepath.Rel(root, path)
		if strings.Contains(rel, "generated.code/") || strings.Contains(rel, "generated.code"+string(filepath.Separator)) {
			return nil
		}

		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()

		scanner := bufio.NewScanner(f)
		lineNo := 0
		var prevLine string
		for scanner.Scan() {
			lineNo++
			line := scanner.Text()

			// An ignore is intentional only when it carries a standard
			// annotation — on this line or the one above. A bare `//`
			// (e.g. a URL in a string) no longer suppresses the check,
			// which used to blind it to any commented line.
			if hasIgnoreAnnotation(line) || hasIgnoreAnnotation(prevLine) {
				prevLine = line
				continue
			}

			for _, re := range patterns {
				if re.MatchString(line) {
					violations = append(violations, fmt.Sprintf("%s:%d: %s", rel, lineNo, strings.TrimSpace(line)))
					break
				}
			}
			prevLine = line
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking server/: %v", err)
	}

	if len(violations) > 0 {
		t.Errorf("found %d ignored error returns in server/ code:\n  %s",
			len(violations), strings.Join(violations, "\n  "))
	}
}

// hasIgnoreAnnotation reports whether a line carries a standard
// intentional-ignore marker. This is the sanctioned way to tell
// TestNoIgnoredErrors a dropped error is deliberate.
func hasIgnoreAnnotation(line string) bool {
	for _, marker := range []string{"// ignore", "// best-effort", "// intentional", "// nolint"} {
		if strings.Contains(line, marker) {
			return true
		}
	}
	return false
}

// ---------- 6. TestNoSecretsInLogs ----------

func TestNoSecretsInLogs(t *testing.T) {
	root := repoRoot(t)
	serverDir := filepath.Join(root, "server")

	// Sensitive field names that should never appear as log values (unless safe).
	// Pattern: slog.String("password", variable) or "token", someVar (not static strings).
	sensitiveFields := []string{"password", "secret", "private_key", "api_key"}

	// Safe patterns to skip (field names that include these words but are safe).
	safePatterns := []string{
		"token_id", "token_prefix", "key_id", "key_path", "secret_path",
		"token_count", "token_name", "token_type", "tokens",
		"password_hash", "hashed_password", "has_password",
	}

	// Build regex for each sensitive field.
	// Match patterns like: slog.String("password", <not a string literal>)
	// or: "password", password  (in slog.Any / slog group calls)
	fieldPatterns := make([]*regexp.Regexp, 0, len(sensitiveFields))
	for _, field := range sensitiveFields {
		// Match: "password", <variable> where variable is not a quoted string.
		re := regexp.MustCompile(fmt.Sprintf(`"%s"\s*,\s*([^")\s][^,)]*[^")\s])`, field))
		fieldPatterns = append(fieldPatterns, re)
	}

	var violations []string

	err := filepath.Walk(serverDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if strings.Contains(rel, "generated.code/") || strings.Contains(rel, "generated.code"+string(filepath.Separator)) {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		lines := strings.Split(string(data), "\n")
		for lineNo, line := range lines {
			// Only check lines with slog or log calls.
			if !strings.Contains(line, "slog.") && !strings.Contains(line, "log.") {
				continue
			}

			for _, re := range fieldPatterns {
				m := re.FindStringSubmatch(line)
				if m == nil {
					continue
				}

				// Check against safe patterns.
				isSafe := false
				for _, safe := range safePatterns {
					if strings.Contains(line, safe) {
						isSafe = true
						break
					}
				}
				if isSafe {
					continue
				}

				// Skip if the line has a comment indicating it's intentional.
				if strings.Contains(line, "// safe") || strings.Contains(line, "// not a secret") {
					continue
				}

				violations = append(violations, fmt.Sprintf("%s:%d: %s", rel, lineNo+1, strings.TrimSpace(line)))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking server/: %v", err)
	}

	if len(violations) > 0 {
		t.Errorf("found %d potential secrets in log calls:\n  %s",
			len(violations), strings.Join(violations, "\n  "))
	}
}

// ---------- 7. TestExecutorCountConsistent ----------

func TestExecutorCountConsistent(t *testing.T) {
	root := repoRoot(t)

	// Built-in executor types from runner/mod.rs (shared deriver — same set
	// the numbers-of-record parity test reads), plus the runtime-loaded
	// `custom` type, which is a first-class type with its own doc page but is
	// not registered via executors.insert.
	seen := builtinExecutorTypes(t, root)
	seen["custom"] = true
	codeCount := len(seen)

	// Check docs/_index.md for the claimed count.
	indexPath := filepath.Join(root, "docs", "_index.md")
	indexData, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("reading docs/_index.md: %v", err)
	}

	// Match the honest total in a phrase like "69 resource types". The
	// count includes the dynamic `custom` type (added above), so the doc
	// must headline the total — not a "built-in"-qualified subset, which
	// would legitimately read 68 and never equal codeCount.
	countRe := regexp.MustCompile(`(\d+)\s+resource\s+types?`)
	m := countRe.FindStringSubmatch(string(indexData))
	if m == nil {
		t.Log("could not find executor count claim in docs/_index.md, skipping doc count check")
	} else {
		var docCount int
		fmt.Sscanf(m[1], "%d", &docCount)
		if codeCount != docCount {
			t.Errorf("executor count mismatch: code has %d unique executor types, docs/_index.md claims %d",
				codeCount, docCount)
		}
	}

	// Also check docs/reference/vigo/resources/_index.md.
	execIndexPath := filepath.Join(root, "docs", "reference", "vigo", "resources", "_index.md")
	execIndexData, err := os.ReadFile(execIndexPath)
	if err != nil {
		t.Logf("could not read docs/reference/vigo/resources/_index.md: %v", err)
		return
	}

	em := countRe.FindStringSubmatch(string(execIndexData))
	if em == nil {
		t.Log("could not find executor count in docs/reference/vigo/resources/_index.md, skipping")
	} else {
		var execDocCount int
		fmt.Sscanf(em[1], "%d", &execDocCount)
		if codeCount != execDocCount {
			t.Errorf("executor count mismatch: code has %d unique types, docs/reference/vigo/resources/_index.md claims %d",
				codeCount, execDocCount)
		}
	}
}

// ---------- 8. TestCLICommandCountConsistent ----------

// TestCLICommandCountConsistent checks the top-level vigocli command set
// registered in cli/main.go against the tracked CLI reference index
// (docs/reference/vigocli/_index.md), in both directions and by whole
// command name: every registered command has an index row linking its
// per-command page, and every index row names a registered command.
func TestCLICommandCountConsistent(t *testing.T) {
	root := repoRoot(t)

	code := registeredTopLevelCommands(t, root)

	indexPath := filepath.Join(root, "docs", "reference", "vigocli", "_index.md")
	indexData, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("reading %s: %v", indexPath, err)
	}
	documented := documentedTopLevelCommands(string(indexData))

	// cobra's built-in `completion` has no reference page of its own; every
	// other command must link one.
	noPage := map[string]bool{"completion": true}

	var undocumented, unregistered []string
	for name := range code {
		page, ok := documented[name]
		switch {
		case !ok:
			undocumented = append(undocumented, name)
		case page == "" && !noPage[name]:
			undocumented = append(undocumented, name+" (index row links no page)")
		case page != "":
			if _, err := os.Stat(filepath.Join(root, "docs", "reference", "vigocli", page)); err != nil {
				undocumented = append(undocumented, name+" (page "+page+" missing)")
			}
		}
	}
	for name := range documented {
		if !code[name] {
			unregistered = append(unregistered, name)
		}
	}
	sort.Strings(undocumented)
	sort.Strings(unregistered)
	if len(undocumented) > 0 {
		t.Errorf("vigocli commands registered in cli/main.go but not in docs/reference/vigocli/_index.md:\n  %s",
			strings.Join(undocumented, "\n  "))
	}
	if len(unregistered) > 0 {
		t.Errorf("docs/reference/vigocli/_index.md lists commands cli/main.go does not register:\n  %s",
			strings.Join(unregistered, "\n  "))
	}
}

// registeredTopLevelCommands returns the command names (first word of each
// Use:) of every var passed to rootCmd.AddCommand in cli/main.go, plus cobra's
// default `completion` command (cli never disables it). A var that cannot be
// resolved to a Use: is a hard failure, so the set can't silently shrink.
func registeredTopLevelCommands(t *testing.T, root string) map[string]bool {
	t.Helper()
	mainData, err := os.ReadFile(filepath.Join(root, "cli", "main.go"))
	if err != nil {
		t.Fatalf("reading cli/main.go: %v", err)
	}
	// Drop line comments so prose (or a ')') inside them can't leak in.
	src := regexp.MustCompile(`//[^\n]*`).ReplaceAllString(string(mainData), "")
	block := regexp.MustCompile(`rootCmd\.AddCommand\(([^)]*)\)`).FindStringSubmatch(src)
	if block == nil {
		t.Fatal("could not find rootCmd.AddCommand in cli/main.go")
	}
	var varNames []string
	for _, f := range strings.Split(block[1], ",") {
		if v := strings.TrimSpace(f); v != "" {
			varNames = append(varNames, v)
		}
	}

	cliFiles, _ := filepath.Glob(filepath.Join(root, "cli", "*.go"))
	useRe := regexp.MustCompile(`Use:\s*"([^"]+)"`)
	cmdNames := map[string]string{}
	for _, f := range cliFiles {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var currentVar string
		for _, line := range strings.Split(string(data), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.Contains(trimmed, "= &cobra.Command{") {
				parts := strings.Fields(trimmed)
				if len(parts) >= 2 && parts[0] == "var" {
					currentVar = parts[1]
				}
			}
			if currentVar != "" && strings.HasPrefix(trimmed, "Use:") {
				if um := useRe.FindStringSubmatch(trimmed); um != nil {
					cmdNames[currentVar] = strings.Fields(um[1])[0]
				}
				currentVar = ""
			}
		}
	}

	out := map[string]bool{"completion": true}
	for _, v := range varNames {
		name, ok := cmdNames[v]
		if !ok {
			t.Errorf("cli/main.go registers %s but no `var %s = &cobra.Command{ Use: ... }` was found in cli/", v, v)
			continue
		}
		out[name] = true
	}
	return out
}

// documentedTopLevelCommands parses the vigocli section of the CLI reference
// index and returns each table row's top-level command (first word of the
// first cell, so "swarm filecast" counts as swarm) mapped to the page its
// row links ("" for an unlinked `name` cell). A subcommand row never
// overwrites its parent's page.
func documentedTopLevelCommands(index string) map[string]string {
	if i := strings.Index(index, "\n## vigocli"); i >= 0 {
		index = index[i:]
	}
	linkRe := regexp.MustCompile(`^\|\s*\[([^\]]+)\]\(([^)]+)\)\s*\|`)
	codeRe := regexp.MustCompile("^\\|\\s*`([^`]+)`\\s*\\|")
	out := map[string]string{}
	for _, line := range strings.Split(index, "\n") {
		line = strings.TrimSpace(line)
		var cell, page string
		if m := linkRe.FindStringSubmatch(line); m != nil {
			cell, page = m[1], m[2]
		} else if m := codeRe.FindStringSubmatch(line); m != nil {
			cell = m[1]
		} else {
			continue
		}
		fields := strings.Fields(cell)
		if len(fields) == 0 || strings.HasPrefix(fields[0], "--") {
			continue // global-flags table
		}
		name := fields[0]
		if len(fields) > 1 {
			if _, seen := out[name]; !seen {
				out[name] = ""
			}
			continue
		}
		out[name] = page
	}
	return out
}
