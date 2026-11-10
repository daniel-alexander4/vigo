// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestUIIsObservabilityOnly guards the "web UI is observability-only" doctrine:
// no template may carry a state-mutating control. The web UI renders fleet and
// server state; every write (config, compliance evidence, …) goes through
// vigocli — the single action path.
//
// Allowed: GET forms (filters/sorts), the auth logout form (POST /logout), and
// the AI ask drawer (a JS fetch to a read-only endpoint, not a mutating form).
// Forbidden: any `hx-post`/`hx-put`/`hx-delete`/`hx-patch`, and any
// `<form method=POST>` that isn't /logout. If a write surface is genuinely
// needed, add the equivalent `vigocli` verb and keep the UI read-only.
func TestUIIsObservabilityOnly(t *testing.T) {
	hxMut := regexp.MustCompile(`(?i)hx-(post|put|delete|patch)`)
	formTag := regexp.MustCompile(`(?is)<form[^>]*>`)

	err := filepath.WalkDir("templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".html") {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		s := string(data)

		if m := hxMut.FindString(s); m != "" {
			t.Errorf("%s: mutating %q in a UI template — the UI is observability-only; move the action to vigocli", path, m)
		}

		for _, tag := range formTag.FindAllString(s, -1) {
			low := strings.ToLower(tag)
			if !strings.Contains(low, `method="post"`) && !strings.Contains(low, "method='post'") {
				continue // GET form (filter/sort) — fine
			}
			if !strings.Contains(low, "/logout") {
				t.Errorf("%s: state-mutating POST form in a UI template — only the /logout auth form is allowed; move writes to vigocli:\n  %s", path, strings.TrimSpace(tag))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk templates: %v", err)
	}
}
