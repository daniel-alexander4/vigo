// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package modlint

import (
	"strings"
	"testing"
)

// TestExecTriggerWhenChanged_SubscribesWithoutGuard is the case that
// motivated the rule: a `systemctl daemon-reload` exec wired to
// subscribes but never gated, so it fires every cycle and reports
// changed every time.
func TestExecTriggerWhenChanged_SubscribesWithoutGuard(t *testing.T) {
	mod := &configcrate{
		Name: "brownnoise",
		Resources: []resource{{
			Name:       "brownnoise-daemon-reload",
			Type:       "exec",
			Subscribes: []string{"brownnoise-unit"},
			Attrs:      map[string]any{"command": "systemctl daemon-reload"},
		}},
	}
	findings := execTriggerWhenChangedRule{}.apply(mod, Options{})
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d: %v", len(findings), findings)
	}
	if findings[0].Severity != Warning {
		t.Errorf("expected Warning severity, got %v", findings[0].Severity)
	}
	if findings[0].FixApplied {
		t.Error("rule must not auto-fix — flipping an exec to trigger-only can silently disable it")
	}
	if !strings.Contains(findings[0].Message, "brownnoise-daemon-reload") {
		t.Errorf("expected resource name in message, got: %s", findings[0].Message)
	}
}

// TestExecTriggerWhenChanged_NotifyTargetWithoutGuard covers the mirror
// shape: the exec declares no subscribes but is named in another
// resource's notify:, which triggers it just the same.
func TestExecTriggerWhenChanged_NotifyTargetWithoutGuard(t *testing.T) {
	mod := &configcrate{
		Name: "app",
		Resources: []resource{
			{
				Name:   "app-config",
				Type:   "file",
				Notify: []string{"reload-app"},
				Attrs:  map[string]any{"target_path": "/etc/app.conf"},
			},
			{
				Name:  "reload-app",
				Type:  "exec",
				Attrs: map[string]any{"command": "systemctl reload app"},
			},
		},
	}
	findings := execTriggerWhenChangedRule{}.apply(mod, Options{})
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d: %v", len(findings), findings)
	}
	if !strings.Contains(findings[0].Message, "notify") {
		t.Errorf("expected notify attribution in message, got: %s", findings[0].Message)
	}
}

// TestExecTriggerWhenChanged_QuietCases pins the false-positive guards.
// Each of these is a legitimate shape the rule must not flag.
func TestExecTriggerWhenChanged_QuietCases(t *testing.T) {
	cases := []struct {
		name string
		res  resource
	}{
		{"gated on changed", resource{
			Name: "r", Type: "exec", When: "changed",
			Subscribes: []string{"unit"},
			Attrs:      map[string]any{"command": "systemctl daemon-reload"},
		}},
		{"changed combined with another expression", resource{
			Name: "r", Type: "exec", When: "changed && os_family('debian')",
			Subscribes: []string{"unit"},
			Attrs:      map[string]any{"command": "systemctl daemon-reload"},
		}},
		{"guarded by onlyif", resource{
			Name: "r", Type: "exec",
			Subscribes: []string{"unit"},
			Attrs:      map[string]any{"command": "do-thing", "onlyif": "test -f /flag"},
		}},
		{"guarded by unless", resource{
			Name: "r", Type: "exec",
			Subscribes: []string{"unit"},
			Attrs:      map[string]any{"command": "do-thing", "unless": "test -f /flag"},
		}},
		{"every-cycle execution declared via report_settled", resource{
			Name: "r", Type: "exec",
			Subscribes: []string{"unit"},
			Attrs:      map[string]any{"command": "apt-get update", "report_settled": "true"},
		}},
		{"no trigger at all", resource{
			Name: "r", Type: "exec",
			Attrs: map[string]any{"command": "do-thing"},
		}},
		{"not an exec", resource{
			Name: "r", Type: "service",
			Subscribes: []string{"unit"},
			Attrs:      map[string]any{"state": "running"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mod := &configcrate{Name: "m", Resources: []resource{tc.res}}
			if findings := (execTriggerWhenChangedRule{}).apply(mod, Options{}); len(findings) != 0 {
				t.Errorf("expected no findings, got %v", findings)
			}
		})
	}
}
