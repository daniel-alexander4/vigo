// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package audit

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// docs/internal.docs/numbers-of-record.md is the single source of truth for
// every repeated figure across Vigo's docs. Its "Derived — code counts" rows
// cache a value the code owns; each names a Verify command. This test re-runs
// those derivations from the code SoT and asserts the cached value matches —
// so the registry can't drift the way it was meant to stop the docs drifting.
//
// Guarding shape: precise, code-derivable rows get a deriver below. Rows whose
// value is approximate (~) or list/policy-shaped (no single scrapable SoT) are
// listed in unguardedNumbers with a reason. A Derived row that is neither
// guarded nor allowlisted fails the test — a new precise figure can't slip in
// unchecked.

// builtinExecutorTypes returns the set of built-in resource-type names declared
// via `executors.insert("name", …)` in agent/src/runner/mod.rs, excluding the
// internal `mock` type and the runtime-loaded `custom` type. This is the code
// SoT for the resource-type count; TestExecutorCountConsistent reads the same
// set (plus `custom`).
func builtinExecutorTypes(t *testing.T, root string) map[string]bool {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "agent", "src", "runner", "mod.rs"))
	if err != nil {
		t.Fatalf("reading runner/mod.rs: %v", err)
	}
	insertRe := regexp.MustCompile(`executors\.insert\("([^"]+)"`)
	seen := map[string]bool{}
	for _, m := range insertRe.FindAllStringSubmatch(string(data), -1) {
		if m[1] == "mock" {
			continue
		}
		seen[m[1]] = true
	}
	if len(seen) == 0 {
		t.Fatal("found no executor types in runner/mod.rs")
	}
	return seen
}

// traitCollectorNames returns the canonical collector name strings from the
// `"name" => Some(Self::Variant)` arms of CollectorKind::from_name in
// agent/src/traits/mod.rs — the same table TestEveryTraitHasDocPage reads.
func traitCollectorNames(t *testing.T, root string) map[string]bool {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "agent", "src", "traits", "mod.rs"))
	if err != nil {
		t.Fatalf("reading traits/mod.rs: %v", err)
	}
	armRe := regexp.MustCompile(`"([a-z_]+)"\s*=>\s*Some\(Self::`)
	seen := map[string]bool{}
	for _, m := range armRe.FindAllStringSubmatch(string(data), -1) {
		seen[m[1]] = true
	}
	if len(seen) == 0 {
		t.Fatal("found no collector names in traits/mod.rs from_name")
	}
	return seen
}

// countProtoRPCs returns the number of `rpc Name` declarations inside the named
// proto service block in protobuf/vigo.proto.
func countProtoRPCs(t *testing.T, root, service string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "protobuf", "vigo.proto"))
	if err != nil {
		t.Fatalf("reading vigo.proto: %v", err)
	}
	blockRe := regexp.MustCompile(`(?s)service ` + service + `\s*\{(.*?)\n\}`)
	block := blockRe.FindStringSubmatch(string(data))
	if block == nil {
		t.Fatalf("could not find `service %s` block in vigo.proto", service)
	}
	return len(regexp.MustCompile(`\brpc\s+[A-Za-z]`).FindAllString(block[1], -1))
}

// countFrameworkSlugs returns the number of quoted slugs in the
// canonicalFrameworkSlugs declaration in server/modlint/frameworks.go.
func countFrameworkSlugs(t *testing.T, root string) int {
	t.Helper()
	// The framework registry (server/frameworks) is the single source of
	// truth for the framework set; every other enumeration (modlint's
	// canonical slugs, the report builders' metadata, the UI maps, …)
	// derives from All here.
	data, err := os.ReadFile(filepath.Join(root, "server", "frameworks", "frameworks.go"))
	if err != nil {
		t.Fatalf("reading frameworks.go: %v", err)
	}
	blockRe := regexp.MustCompile(`(?s)All\s*=\s*\[\]Framework\{(.*?)\n\}`)
	block := blockRe.FindStringSubmatch(string(data))
	if block == nil {
		t.Fatal("could not find the All registry slice in server/frameworks/frameworks.go")
	}
	// One entry per line beginning `{"`.
	return len(regexp.MustCompile(`(?m)^\s*\{"`).FindAllString(block[1], -1))
}

func countNetworkDeviceExecutors(t *testing.T, root string) int {
	t.Helper()
	netRe := regexp.MustCompile(`^(network_|cisco_|junos_|ssh_exec)`)
	n := 0
	for name := range builtinExecutorTypes(t, root) {
		if netRe.MatchString(name) {
			n++
		}
	}
	return n
}

// unguardedNumbers names the Derived rows this test intentionally does not
// re-derive, each with the reason. A Derived row absent from both this map and
// the deriver set fails the test, forcing a conscious choice for any new figure.
var unguardedNumbers = map[string]string{
	"Supported operating systems":    "build-target policy, not a single scrapable SoT",
	"Swarm content subsystems":       "fixed subsystem list (policy), not a code count",
	"AI assistant tools (read-only)": "approximate (~14); registry SoT is itself loose",
	"Connwaer capabilities":          "independently versioned; 'active checks' has no single SoT",
}

func TestNumbersOfRecordParity(t *testing.T) {
	root := repoRoot(t)

	// Derivers keyed by the registry's metric label (backticks stripped).
	derivers := map[string]func() int{
		"Resource types (total)":                        func() int { return len(builtinExecutorTypes(t, root)) + 1 }, // + custom
		"Built-in resource types":                       func() int { return len(builtinExecutorTypes(t, root)) },
		"Network-device executors (subset of built-in)": func() int { return countNetworkDeviceExecutors(t, root) },
		"Trait collectors":                              func() int { return len(traitCollectorNames(t, root)) },
		"VigoAgent gRPC RPCs":                           func() int { return countProtoRPCs(t, root, "VigoAgent") },
		"VigoAdmin gRPC RPCs":                           func() int { return countProtoRPCs(t, root, "VigoAdmin") },
		"VigoPeer gRPC RPCs":                            func() int { return countProtoRPCs(t, root, "VigoPeer") },
		"Compliance framework slugs":                    func() int { return countFrameworkSlugs(t, root) },
	}

	rows := parseDerivedRows(t, root)
	if len(rows) == 0 {
		t.Fatal("parsed no rows from the Derived — code counts table")
	}

	seenMetric := map[string]bool{}
	for _, r := range rows {
		seenMetric[r.metric] = true
		if derive, ok := derivers[r.metric]; ok {
			if r.value < 0 {
				t.Errorf("registry row %q is guarded but its value %q is not a plain integer", r.metric, r.raw)
				continue
			}
			if got := derive(); got != r.value {
				t.Errorf("numbers-of-record drift: %q says %d, code derives %d", r.metric, r.value, got)
			}
			continue
		}
		if _, ok := unguardedNumbers[r.metric]; !ok {
			t.Errorf("Derived row %q is neither guarded by a deriver nor in unguardedNumbers — "+
				"add a deriver (re-deriving from the code SoT) or allowlist it with a reason", r.metric)
		}
	}

	// Guard the guards: every deriver / allowlist key must still match a real
	// row, so a renamed or removed row can't leave a stale entry behind.
	for metric := range derivers {
		if !seenMetric[metric] {
			t.Errorf("deriver references %q but no such row exists in the Derived table", metric)
		}
	}
	for metric := range unguardedNumbers {
		if !seenMetric[metric] {
			t.Errorf("unguardedNumbers references %q but no such row exists in the Derived table", metric)
		}
	}
}

type derivedRow struct {
	metric string
	value  int    // -1 if the value cell holds no plain integer (e.g. "~14")
	raw    string // the value cell, for diagnostics
}

// parseDerivedRows reads the "Derived — code counts" table from
// numbers-of-record.md, returning one entry per data row. The metric cell is
// trimmed and stripped of backticks; the value cell is parsed to an int when it
// is a plain number, else value is -1.
func parseDerivedRows(t *testing.T, root string) []derivedRow {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "docs", "internal.docs", "numbers-of-record.md"))
	if err != nil {
		t.Fatalf("reading numbers-of-record.md: %v", err)
	}
	lines := strings.Split(string(data), "\n")

	intRe := regexp.MustCompile(`^\d+$`)
	var rows []derivedRow
	inTable := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") {
			inTable = strings.Contains(trimmed, "Derived")
			continue
		}
		if !inTable || !strings.HasPrefix(trimmed, "|") {
			continue
		}
		cols := strings.Split(strings.Trim(trimmed, "|"), "|")
		if len(cols) < 2 {
			continue
		}
		metric := strings.ReplaceAll(strings.TrimSpace(cols[0]), "`", "")
		if metric == "Metric" || strings.HasPrefix(metric, "---") {
			continue // header / separator
		}
		valCell := strings.TrimSpace(cols[1])
		valTok := strings.Trim(valCell, "*") // strip bold markers
		row := derivedRow{metric: metric, value: -1, raw: valCell}
		if intRe.MatchString(valTok) {
			row.value, _ = strconv.Atoi(valTok)
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].metric < rows[j].metric })
	return rows
}
