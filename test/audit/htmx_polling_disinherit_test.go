// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package audit

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestHTMXPollingContainersDisinherit pins the invariant that every
// polling container (any element carrying `hx-trigger="every ..."` or
// `hx-trigger="load, every ..."`) ALSO carries `hx-disinherit`. Without
// this, anchors inside a polling container inherit the container's
// `hx-swap` via HTMX's normal attribute inheritance — and `hx-boost`
// then swaps the navigation response INTO the container instead of the
// body, leaving the operator stuck in a half-loaded "weird state"
// (see the 2026-05-27 hx-boost inheritance bug report).
//
// The audit walks every .html under ui/templates/, finds elements with
// `hx-trigger="every` (the polling pattern), and confirms each one's
// attribute set on that element includes `hx-disinherit=`. Adding a new
// polling container without the disinherit fails this test.
func TestHTMXPollingContainersDisinherit(t *testing.T) {
	root := repoRoot(t)
	templatesDir := filepath.Join(root, "ui", "templates")

	// Matches the opening tag of any element that carries a polling
	// hx-trigger. Captures the full attribute block so we can audit it
	// for hx-disinherit. The non-greedy ([^>]*?) plus terminating > keeps
	// us inside one element. Anchored at < to avoid matching attribute
	// values that happen to contain "hx-trigger=" as text.
	pollingTag := regexp.MustCompile(`<[a-z][^>]*?hx-trigger="(?:load,\s*)?every[^>]*?>`)

	var offenders []string
	err := filepath.Walk(templatesDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".html") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		matches := pollingTag.FindAllString(string(body), -1)
		for _, m := range matches {
			if !strings.Contains(m, "hx-disinherit=") {
				rel, _ := filepath.Rel(root, path)
				offenders = append(offenders, rel+": "+oneLine(m))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking templates: %v", err)
	}
	if len(offenders) > 0 {
		t.Errorf("polling containers missing hx-disinherit (boosted child anchors will swap into the container instead of body):\n  %s\n\nAdd `hx-disinherit=\"*\"` to each offending element.",
			strings.Join(offenders, "\n  "))
	}
}

// oneLine collapses whitespace in a multi-attr tag so the audit-failure
// message stays a single readable line per offender.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
