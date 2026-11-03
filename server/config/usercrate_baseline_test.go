// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"
)

// permissiveSecrets resolves every secret: key to a sentinel value. The
// baseline snapshot only cares about which modules attach to each envoy
// pattern — not the rendered content. Using the real backend would tie
// the test to operator unlock state; using mockProvider would fail-fast
// on every key the operator hasn't pre-seeded.
type permissiveSecrets struct{}

func (permissiveSecrets) Get(_ context.Context, _ string) (string, error) {
	return "baseline-placeholder", nil
}

func (permissiveSecrets) GetBytes(_ context.Context, _ string) ([]byte, error) {
	return []byte("baseline-placeholder"), nil
}

func (permissiveSecrets) Close() error { return nil }

// TestBaselineUsercrateSnapshot captures the effective per-envoy-pattern
// usercrate set under the CURRENT loader. Step-1 baseline / step-11
// equivalence gate for pending #41 (usercrates inert-until-included).
//
// Gated on STACKS_BASE — path containing stacks/, e.g.
//
//	STACKS_BASE=/srv/vigo/stacks \
//	STACKS_BASELINE_OUT=/tmp/baseline.live.json \
//	  go test ./server/config/ -run TestBaselineUsercrateSnapshot -count=1 -v
//
// Output JSON: { "<match-pattern>": ["<usercrate-name>", ...sorted], ... }.
//
// Detection: ConfigTrace.ConfigcrateSources[name] contains the "usercrates/"
// substring — the same predicate the trace UI uses.
func TestBaselineUsercrateSnapshot(t *testing.T) {
	base := os.Getenv("STACKS_BASE")
	if base == "" {
		t.Skip("STACKS_BASE not set")
	}

	cs, err := NewConfigStoreQuiet(base, permissiveSecrets{}, false)
	if err != nil {
		t.Fatalf("NewConfigStoreQuiet(%q): %v", base, err)
	}
	if rerr := cs.LastReloadError(); rerr != nil {
		t.Logf("LastReloadError (non-fatal): %v", rerr)
	}

	out := map[string][]string{}
	for pattern := range cs.MatchDirPaths() {
		host := sampleHostFromPattern(pattern)
		trace := cs.Trace(host)
		if !trace.Matched {
			t.Logf("WARN pattern %q sampled as host %q did not match — recording empty set", pattern, host)
			out[pattern] = []string{}
			continue
		}
		var crates []string
		for name, src := range trace.ConfigcrateSources {
			if strings.Contains(src, "usercrates/") {
				crates = append(crates, name)
			}
		}
		sort.Strings(crates)
		if crates == nil {
			crates = []string{}
		}
		out[pattern] = crates
	}

	enc, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatalf("json.MarshalIndent: %v", err)
	}
	enc = append(enc, '\n')

	if dest := os.Getenv("STACKS_BASELINE_OUT"); dest != "" {
		if werr := os.WriteFile(dest, enc, 0o644); werr != nil {
			t.Fatalf("write %q: %v", dest, werr)
		}
		t.Logf("baseline written: %s (%d patterns)", dest, len(out))
		return
	}
	t.Logf("baseline (%d patterns):\n%s", len(out), string(enc))
}

// sampleHostFromPattern synthesizes a representative hostname that satisfies
// a match pattern. Exact patterns return unchanged; glob wildcards are
// replaced (? → "x", * → "x"). Bracket classes ([abc], [!abc]) are not
// expanded — the WARN branch in the caller covers any miss.
func sampleHostFromPattern(p string) string {
	if !isGlobPattern(p) {
		return p
	}
	s := strings.ReplaceAll(p, "?", "x")
	s = strings.ReplaceAll(s, "*", "x")
	return s
}
