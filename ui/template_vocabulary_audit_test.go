// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestNoRetiredConfigcrateVocabularyInTemplates guards the 0.71.0 module→configcrate
// rename on the UI's highest-churn surface — the HTML templates.
//
// "module" can't be banned globally (it collides with kernel_module, Go/Rust
// modules, "Trusted Platform Module", prose), which is why check-legacy-naming.sh
// only nets distinctive CamelCase identifiers. But inside ui/templates/, visible
// "module(s)" text essentially always means the configcrate config-kind — the
// rename left a long tail of stale labels/headings here that surfaced only when
// someone read the page. This test scans the *visible* template text (after
// stripping {{...}} actions, class="..." values, and comments) so a reintroduced
// "Module Inheritance" heading or `modules:` label fails `make test` instead of
// shipping. Add a genuinely-legitimate term (e.g. a TPM trait label) to allowlist.
func TestNoRetiredConfigcrateVocabularyInTemplates(t *testing.T) {
	// Substrings that legitimately contain "module" as visible template text.
	// Empty today — templates are clean after the rename sweep.
	allowlist := []string{}

	reAction := regexp.MustCompile(`(?s)\{\{.*?\}\}`) // template actions / data refs
	reClass := regexp.MustCompile(`class="[^"]*"`)    // CSS class names (e.g. trace-card__modules)
	reHTMLComment := regexp.MustCompile(`(?s)<!--.*?-->`)
	reConfigcrate := regexp.MustCompile(`(?i)\bmodules?\b`)

	err := filepath.WalkDir("templates", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".html") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// Strip the non-visible layers, then scan what an operator actually sees.
		visible := reHTMLComment.ReplaceAllString(string(raw), "")
		visible = reAction.ReplaceAllString(visible, "")
		visible = reClass.ReplaceAllString(visible, "")

		for _, loc := range reConfigcrate.FindAllStringIndex(visible, -1) {
			start := loc[0] - 25
			if start < 0 {
				start = 0
			}
			end := loc[1] + 25
			if end > len(visible) {
				end = len(visible)
			}
			snippet := strings.Join(strings.Fields(visible[start:end]), " ")
			allowed := false
			for _, a := range allowlist {
				if strings.Contains(snippet, a) {
					allowed = true
					break
				}
			}
			if !allowed {
				t.Errorf("%s: retired 'module' vocabulary in visible text — use 'configcrate' (…%s…)", path, snippet)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk templates: %v", err)
	}
}
