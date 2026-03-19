// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package audit provides automated regression tests that catch documentation
// drift, missing docs, undocumented endpoints, ignored errors, secrets in logs,
// and count inconsistencies. These tests run as part of `go test ./...` and fail
// when problems are introduced.
package audit

import (
	"bufio"
	"fmt"
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
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
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

	modPath := filepath.Join(root, "agent-rs", "src", "runner", "mod.rs")
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

	// Some executors are registered under a short name (e.g., "acl", "registry")
	// but their doc pages use the platform-qualified name (e.g., "acl_windows.md",
	// "registry_windows.md"). Accept either the exact name or any <name>_<platform>.md
	// variant.
	docsDir := filepath.Join(root, "docs", "executors")

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
		t.Errorf("executors missing doc pages in docs/executors/:\n  %s", strings.Join(missing, "\n  "))
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

	docsDir := filepath.Join(root, "docs", "cli")

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
		t.Errorf("CLI commands missing doc pages in docs/cli/:\n  %s", strings.Join(missing, "\n  "))
	}
}

// ---------- 4. TestRESTEndpointsDocumented ----------

func TestRESTEndpointsDocumented(t *testing.T) {
	root := repoRoot(t)

	// Collect all registered API routes from server/api/ and server/bootstrap/.
	routeRe := regexp.MustCompile(`(?:HandleFunc|Handle)\(\s*"((?:GET|POST|PUT|DELETE|PATCH)\s+/api/v1/[^"]+)"`)

	var routes []string
	for _, dir := range []string{
		filepath.Join(root, "server", "api"),
		filepath.Join(root, "server", "bootstrap"),
	} {
		files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
		for _, f := range files {
			// Skip test files.
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			data, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			for _, m := range routeRe.FindAllStringSubmatch(string(data), -1) {
				routes = append(routes, m[1])
			}
		}
	}

	if len(routes) == 0 {
		t.Fatal("found no API routes in server/api/ or server/bootstrap/")
	}

	// Read docs/api/rest.md.
	restDoc, err := os.ReadFile(filepath.Join(root, "docs", "api", "rest.md"))
	if err != nil {
		t.Fatalf("reading docs/api/rest.md: %v", err)
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
		t.Errorf("API endpoints not documented in docs/api/rest.md:\n  %s", strings.Join(undocumented, "\n  "))
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

	// Build a combined pattern: _ = <dangerous func> or _ , _ = ...
	// We look for lines where the error is assigned to _.
	// Pattern: starts with _ = or _, _ = or has _ = at the beginning, then a call.
	// Keep it practical: look for "_ = funcName(" or "_ , _ = funcName(" patterns.
	patterns := make([]*regexp.Regexp, 0, len(dangerousFuncs))
	for _, fn := range dangerousFuncs {
		// Match: _ = os.Open(  or  _, _ = os.Open(
		re := regexp.MustCompile(`_\s*(?:,\s*_)?\s*[:=]\s*` + fn)
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
		if strings.Contains(rel, "gen/") || strings.Contains(rel, "gen"+string(filepath.Separator)) {
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

			// Skip lines with an explanatory comment (intentional ignore).
			if strings.Contains(line, "//") {
				prevLine = line
				continue
			}
			// Skip if previous line has a comment explaining the ignore.
			if strings.Contains(prevLine, "// ignore") || strings.Contains(prevLine, "// best-effort") ||
				strings.Contains(prevLine, "// intentional") || strings.Contains(prevLine, "// nolint") {
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
		if strings.Contains(rel, "gen/") || strings.Contains(rel, "gen"+string(filepath.Separator)) {
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

	// Count unique executor type names from runner/mod.rs.
	modPath := filepath.Join(root, "agent-rs", "src", "runner", "mod.rs")
	data, err := os.ReadFile(modPath)
	if err != nil {
		t.Fatalf("reading runner/mod.rs: %v", err)
	}

	insertRe := regexp.MustCompile(`executors\.insert\("([^"]+)"`)
	seen := map[string]bool{}
	for _, m := range insertRe.FindAllStringSubmatch(string(data), -1) {
		name := m[1]
		if name == "mock" {
			continue
		}
		seen[name] = true
	}

	// "custom" is a dynamic executor type loaded at runtime (not via
	// executors.insert), but it is a first-class type with its own doc page.
	// Include it in the count if it's referenced in the source.
	if strings.Contains(string(data), `"custom"`) {
		seen["custom"] = true
	}

	codeCount := len(seen)

	// Check docs/_index.md for the claimed count.
	indexPath := filepath.Join(root, "docs", "_index.md")
	indexData, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("reading docs/_index.md: %v", err)
	}

	// Look for patterns like "72 built-in" or "72 resource types".
	countRe := regexp.MustCompile(`(\d+)\s+built-in\s+resource`)
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

	// Also check docs/executors/_index.md.
	execIndexPath := filepath.Join(root, "docs", "executors", "_index.md")
	execIndexData, err := os.ReadFile(execIndexPath)
	if err != nil {
		t.Logf("could not read docs/executors/_index.md: %v", err)
		return
	}

	em := countRe.FindStringSubmatch(string(execIndexData))
	if em == nil {
		t.Log("could not find executor count in docs/executors/_index.md, skipping")
	} else {
		var execDocCount int
		fmt.Sscanf(em[1], "%d", &execDocCount)
		if codeCount != execDocCount {
			t.Errorf("executor count mismatch: code has %d unique types, docs/executors/_index.md claims %d",
				codeCount, execDocCount)
		}
	}
}

// ---------- 8. TestCLICommandCountConsistent ----------

func TestCLICommandCountConsistent(t *testing.T) {
	root := repoRoot(t)

	// Extract top-level command names from cli/main.go AddCommand block.
	mainPath := filepath.Join(root, "cli", "main.go")
	mainData, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatalf("reading cli/main.go: %v", err)
	}

	addCmdRe := regexp.MustCompile(`rootCmd\.AddCommand\(([\s\S]*?)\)`)
	block := addCmdRe.FindSubmatch(mainData)
	if block == nil {
		t.Fatal("could not find rootCmd.AddCommand in cli/main.go")
	}

	varRe := regexp.MustCompile(`(\w+)`)
	varNames := varRe.FindAllString(string(block[1]), -1)

	// For each variable, resolve the Use: field to get the command name.
	cliDir := filepath.Join(root, "cli")
	cliFiles, _ := filepath.Glob(filepath.Join(cliDir, "*.go"))

	cmdNames := map[string]string{}
	for _, f := range cliFiles {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		lines := strings.Split(string(data), "\n")
		var currentVar string
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.Contains(trimmed, "= &cobra.Command{") {
				parts := strings.Fields(trimmed)
				if len(parts) >= 2 && parts[0] == "var" {
					currentVar = parts[1]
				}
			}
			if currentVar != "" && strings.HasPrefix(trimmed, `Use:`) {
				useRe := regexp.MustCompile(`Use:\s*"([^"]+)"`)
				um := useRe.FindStringSubmatch(trimmed)
				if um != nil {
					cmdNames[currentVar] = strings.Fields(um[1])[0]
				}
				currentVar = ""
			}
		}
	}

	// Read CLAUDE.md to check that every command appears there.
	claudePath := filepath.Join(root, "CLAUDE.md")
	claudeData, err := os.ReadFile(claudePath)
	if err != nil {
		t.Fatalf("reading CLAUDE.md: %v", err)
	}
	claudeContent := string(claudeData)

	var missing []string
	for _, varName := range varNames {
		cmdName, ok := cmdNames[varName]
		if !ok {
			continue
		}
		// Check if the command name appears in CLAUDE.md's CLI description.
		// Look for the command name as a backtick-quoted string or in the
		// subcommands list.
		if !strings.Contains(claudeContent, "`"+cmdName+"`") &&
			!strings.Contains(claudeContent, " "+cmdName+",") &&
			!strings.Contains(claudeContent, " "+cmdName+" ") &&
			!strings.Contains(claudeContent, "`"+cmdName) &&
			!strings.Contains(claudeContent, cmdName+"`") {
			missing = append(missing, cmdName)
		}
	}

	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("CLI commands not mentioned in CLAUDE.md:\n  %s", strings.Join(missing, "\n  "))
	}
}
