// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package publish

import (
	"strings"
	"testing"
)

// TestRetractUsesAliasAndWarnsOnSkip: a repository declared with the
// `repo:` alias is reversed with that key, and a resource whose target
// cannot be found is reported as a skip instead of silently dropped.
func TestRetractUsesAliasAndWarnsOnSkip(t *testing.T) {
	src := `name: tools
resources:
  - name: tools-repo
    type: repository
    repo: "deb http://example.invalid/apt stable main"
    state: present
  - name: mystery-pkg
    type: package
    state: present
`
	out, warnings, err := GenerateRetractConfigcrate("tools", []byte(src))
	if err != nil {
		t.Fatalf("GenerateRetractConfigcrate: %v", err)
	}
	if !strings.Contains(out, "remove-tools-repo") || !strings.Contains(out, "example.invalid") {
		t.Fatalf("repo: alias not reversed:\n%s", out)
	}
	found := false
	for _, w := range warnings {
		if w.Resource == "mystery-pkg" && w.Confidence == "skip" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no skip warning for a package with no package attribute: %+v", warnings)
	}
}
