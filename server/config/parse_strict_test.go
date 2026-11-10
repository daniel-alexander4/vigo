// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestConfigcrateUnknownTopLevelKeyRejected: a typo such as `resouces:`
// is an error from both decode entry points, not a crate with no resources.
func TestConfigcrateUnknownTopLevelKeyRejected(t *testing.T) {
	src := "name: web\nresouces:\n  - name: nginx\n    type: package\n    package: nginx\n    state: present\n"
	if _, err := ParseConfigcrateFile([]byte(src)); err == nil || !strings.Contains(err.Error(), `"resouces"`) {
		t.Fatalf("ParseConfigcrateFile err = %v, want an unknown-key error naming resouces", err)
	}
	var def ConfigcrateDefinition
	if err := yaml.Unmarshal([]byte(src), &def); err == nil {
		t.Fatal("yaml.Unmarshal into ConfigcrateDefinition accepted an unknown key")
	}
}

// TestConfigcrateRetiredComplianceShapeRejected: the pre-0.66.48
// framework→control list under compliance: is an error, not an empty
// claim set.
func TestConfigcrateRetiredComplianceShapeRejected(t *testing.T) {
	src := "name: web\ncompliance:\n  hipaa: [\"164.312(a)(1)\"]\nresources: []\n"
	if _, err := ParseConfigcrateFile([]byte(src)); err == nil || !strings.Contains(err.Error(), "retired") {
		t.Fatalf("ParseConfigcrateFile err = %v, want a retired-shape error", err)
	}
	ok := "name: web\ndescription: web tier\ncompliance:\n  provides: [time-sync]\nresources: []\n"
	def, err := ParseConfigcrateFile([]byte(ok))
	if err != nil {
		t.Fatalf("current shape rejected: %v", err)
	}
	if def.Description != "web tier" || len(def.Compliance.Provides) != 1 {
		t.Fatalf("decoded %+v", def)
	}
}

// TestShippedConfigcratesParse: every configcrate and usercrate shipped in the
// repo, live (.vgo) and install template (.vgo.example), parses under the
// strict decoder the loader uses. An operator copies a .vgo.example to .vgo,
// so a template that fails here fails on their first publish.
func TestShippedConfigcratesParse(t *testing.T) {
	roots := []string{"../../example-configs", "../../server-seed", "../../gcloud/demo-stacks"}
	n := 0
	for _, root := range roots {
		_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			if !strings.Contains(p, "/configcrates/") && !strings.Contains(p, "/usercrates/") {
				return nil
			}
			if !strings.HasSuffix(p, ".vgo") && !strings.HasSuffix(p, ".vgo.example") {
				return nil
			}
			data, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			n++
			if _, err := ParseConfigcrateFile(data); err != nil {
				t.Errorf("%s: %v", p, err)
			}
			return nil
		})
	}
	if n == 0 {
		t.Fatal("found no shipped configcrates to parse")
	}
}
