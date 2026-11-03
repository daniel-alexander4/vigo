// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package audit

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestConfigDocsTagsResolve guards the `docs:` struct tags on the config
// sections in server/config/types.go. The server-config UI renders each one as
// a `/docs/<path>` "Read the documentation →" link
// (ui/templates/pages/server-config-section.html), so a stale tag is a live
// 404 in the UI. The 2026-06-01 docs rebuild moved doc paths and left 26 of 29
// tags pointing at dead pre-rebuild locations (`operations/…`,
// `getting-started/…`); this test catches that class of drift forever.
//
// Two checks:
//  1. every `docs:` tag resolves to a real docs/ page (the path before the
//     #anchor — anchors aren't file-checkable, same convention as
//     TestDocLinksNotBroken);
//  2. every top-level ServerConfig section has a matching `## <key>` heading in
//     reference/server-yaml.md, so the #<key> anchor each tag carries lands on a
//     real section rather than dangling.
func TestConfigDocsTagsResolve(t *testing.T) {
	root := repoRoot(t)
	docsDir := filepath.Join(root, "docs")

	data, err := os.ReadFile(filepath.Join(root, "server", "config", "types.go"))
	if err != nil {
		t.Fatalf("reading server/config/types.go: %v", err)
	}
	src := string(data)

	// --- Check 1: every docs: tag resolves to a real page ---
	tagRe := regexp.MustCompile(`docs:"([^"]+)"`)
	tags := tagRe.FindAllStringSubmatch(src, -1)
	if len(tags) == 0 {
		t.Fatal("no docs: struct tags found in server/config/types.go — extractor broken")
	}
	for _, m := range tags {
		page := m[1]
		if i := strings.IndexByte(page, '#'); i >= 0 {
			page = page[:i] // strip #anchor
		}
		// A docs: path resolves to docs/<page>.md or docs/<page>/_index.md.
		if _, err := os.Stat(filepath.Join(docsDir, page+".md")); err == nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(docsDir, page, "_index.md")); err == nil {
			continue
		}
		t.Errorf("docs: tag %q resolves to no docs page (tried %s.md and %s/_index.md)", m[1], page, page)
	}

	// --- Check 2: every ServerConfig section has a `## <key>` heading ---
	// Bound the key scan to the ServerConfig struct so nested-struct yaml keys
	// (sub-fields like `url`, `enabled`) don't count as top-level sections.
	scStart := strings.Index(src, "type ServerConfig struct {")
	if scStart < 0 {
		t.Fatal("type ServerConfig struct not found in types.go")
	}
	scBody := src[scStart:]
	if end := strings.Index(scBody, "\n}"); end >= 0 {
		scBody = scBody[:end]
	}

	syData, err := os.ReadFile(filepath.Join(docsDir, "reference", "server-yaml.md"))
	if err != nil {
		t.Fatalf("reading reference/server-yaml.md: %v", err)
	}
	sy := string(syData)

	keyRe := regexp.MustCompile(`yaml:"([a-z_]+)"`)
	for _, m := range keyRe.FindAllStringSubmatch(scBody, -1) {
		key := m[1]
		headingRe := regexp.MustCompile(`(?m)^## ` + regexp.QuoteMeta(key) + `$`)
		if !headingRe.MatchString(sy) {
			t.Errorf("config section %q has no `## %s` heading in reference/server-yaml.md — the #%s anchor its docs: tag carries will dangle", key, key, key)
		}
	}
}
