// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package fleet

import (
	"testing"

	"git.alexander4.io/vigo/ui"

	"github.com/stretchr/testify/assert"
)

// ADR-032 slice 2 filter axes (env, tag, role, os, arch, module,
// compliance) all share the same OR-within / AND-across semantics
// via splitCommaLower + anySubstringMatch[InSlice]. These tests pin
// each axis at the matchesEnvoyFilter contract level — single value,
// multi-value comma-OR, miss case. Hostname search is the existing
// axis; included here because slice 2 promoted it to comma-OR too
// and the contract needs to stay locked.

func TestMatchesEnvoyFilter_NoFiltersPassesEverything(t *testing.T) {
	row := ui.EnvoyRow{Hostname: "web01"}
	assert.True(t, matchesEnvoyFilter(row, "", "", "", nil, nil, nil, nil, nil, nil, nil, nil))
}

func TestMatchesEnvoyFilter_SearchCommaOR(t *testing.T) {
	row := ui.EnvoyRow{Hostname: "db05.dc1.example.com"}
	// Single value matches.
	assert.True(t, matchesEnvoyFilter(row, "", "", "", splitCommaLower("db"), nil, nil, nil, nil, nil, nil, nil))
	// Multi-value comma-OR — any matches.
	assert.True(t, matchesEnvoyFilter(row, "", "", "", splitCommaLower("web,db"), nil, nil, nil, nil, nil, nil, nil))
	// All values miss.
	assert.False(t, matchesEnvoyFilter(row, "", "", "", splitCommaLower("web,cache"), nil, nil, nil, nil, nil, nil, nil))
}

func TestMatchesEnvoyFilter_Environment(t *testing.T) {
	row := ui.EnvoyRow{Hostname: "x", Environment: "production"}
	assert.True(t, matchesEnvoyFilter(row, "", "", "", nil, splitCommaLower("prod"), nil, nil, nil, nil, nil, nil))
	assert.True(t, matchesEnvoyFilter(row, "", "", "", nil, splitCommaLower("staging,prod"), nil, nil, nil, nil, nil, nil))
	assert.False(t, matchesEnvoyFilter(row, "", "", "", nil, splitCommaLower("staging,dev"), nil, nil, nil, nil, nil, nil))
}

func TestMatchesEnvoyFilter_Tag(t *testing.T) {
	row := ui.EnvoyRow{Hostname: "x", Tags: []string{"canary", "team-web"}}
	assert.True(t, matchesEnvoyFilter(row, "", "", "", nil, nil, splitCommaLower("canary"), nil, nil, nil, nil, nil))
	assert.True(t, matchesEnvoyFilter(row, "", "", "", nil, nil, splitCommaLower("nope,team"), nil, nil, nil, nil, nil))
	assert.False(t, matchesEnvoyFilter(row, "", "", "", nil, nil, splitCommaLower("baseline"), nil, nil, nil, nil, nil))
}

func TestMatchesEnvoyFilter_Role(t *testing.T) {
	row := ui.EnvoyRow{Hostname: "x", Roles: []string{"web", "monitoring"}}
	assert.True(t, matchesEnvoyFilter(row, "", "", "", nil, nil, nil, splitCommaLower("web"), nil, nil, nil, nil))
	assert.True(t, matchesEnvoyFilter(row, "", "", "", nil, nil, nil, splitCommaLower("db,monit"), nil, nil, nil, nil))
	assert.False(t, matchesEnvoyFilter(row, "", "", "", nil, nil, nil, splitCommaLower("db,cache"), nil, nil, nil, nil))
}

func TestMatchesEnvoyFilter_OS(t *testing.T) {
	row := ui.EnvoyRow{Hostname: "x", OS: "ubuntu"}
	assert.True(t, matchesEnvoyFilter(row, "", "", "", nil, nil, nil, nil, splitCommaLower("ubuntu"), nil, nil, nil))
	assert.True(t, matchesEnvoyFilter(row, "", "", "", nil, nil, nil, nil, splitCommaLower("rhel,ubuntu"), nil, nil, nil))
	assert.False(t, matchesEnvoyFilter(row, "", "", "", nil, nil, nil, nil, splitCommaLower("rhel,debian"), nil, nil, nil))
}

func TestMatchesEnvoyFilter_Arch(t *testing.T) {
	row := ui.EnvoyRow{Hostname: "x", Arch: "arm64"}
	assert.True(t, matchesEnvoyFilter(row, "", "", "", nil, nil, nil, nil, nil, splitCommaLower("arm64"), nil, nil))
	assert.False(t, matchesEnvoyFilter(row, "", "", "", nil, nil, nil, nil, nil, splitCommaLower("amd64"), nil, nil))
}

func TestMatchesEnvoyFilter_Module(t *testing.T) {
	row := ui.EnvoyRow{Hostname: "x", Modules: []string{"nginx-hardened", "rsyslog"}}
	assert.True(t, matchesEnvoyFilter(row, "", "", "", nil, nil, nil, nil, nil, nil, splitCommaLower("nginx"), nil))
	assert.True(t, matchesEnvoyFilter(row, "", "", "", nil, nil, nil, nil, nil, nil, splitCommaLower("apache,sys"), nil))
	assert.False(t, matchesEnvoyFilter(row, "", "", "", nil, nil, nil, nil, nil, nil, splitCommaLower("apache"), nil))
}

func TestMatchesEnvoyFilter_Compliance(t *testing.T) {
	row := ui.EnvoyRow{Hostname: "x", ComplianceFrameworks: []string{"hipaa", "pci-dss"}}
	assert.True(t, matchesEnvoyFilter(row, "", "", "", nil, nil, nil, nil, nil, nil, nil, splitCommaLower("hipaa")))
	assert.True(t, matchesEnvoyFilter(row, "", "", "", nil, nil, nil, nil, nil, nil, nil, splitCommaLower("soc2,pci")))
	assert.False(t, matchesEnvoyFilter(row, "", "", "", nil, nil, nil, nil, nil, nil, nil, splitCommaLower("soc2,nist")))
}

func TestMatchesEnvoyFilter_AcrossAxesIsAND(t *testing.T) {
	row := ui.EnvoyRow{
		Hostname:    "web01.prod",
		Environment: "production",
		OS:          "ubuntu",
		Roles:       []string{"web"},
	}
	// All three axes satisfied → match.
	assert.True(t, matchesEnvoyFilter(row, "", "", "",
		nil,
		splitCommaLower("prod"),
		nil,
		splitCommaLower("web"),
		splitCommaLower("ubuntu"),
		nil, nil, nil))
	// One axis fails → whole expression fails.
	assert.False(t, matchesEnvoyFilter(row, "", "", "",
		nil,
		splitCommaLower("prod"),
		nil,
		splitCommaLower("web"),
		splitCommaLower("rhel"), // miss
		nil, nil, nil))
}

func TestSplitCommaLower_TrimsAndDropsEmpty(t *testing.T) {
	assert.Nil(t, splitCommaLower(""))
	assert.Equal(t, []string{"prod"}, splitCommaLower("prod"))
	assert.Equal(t, []string{"prod", "staging"}, splitCommaLower("prod, staging"))
	assert.Equal(t, []string{"prod", "staging"}, splitCommaLower("prod,,staging"))
	assert.Equal(t, []string{"prod", "staging"}, splitCommaLower("PROD , Staging"))
}

// ============================================================================
// EnumerateFilterAxes — pill enumeration, sorting, cap, selection state, URLs.
// Companion to matchesEnvoyFilter: that one decides which rows make it
// into the filtered list; this one builds the pill catalog the operator
// uses to mutate the filter.
// ============================================================================

func TestEnumerateFilterAxes_FreeTextBucketsAndSortsByCountDesc(t *testing.T) {
	rows := []ui.EnvoyRow{
		{Hostname: "a", OS: "ubuntu"},
		{Hostname: "b", OS: "ubuntu"},
		{Hostname: "c", OS: "ubuntu"},
		{Hostname: "d", OS: "rhel"},
		{Hostname: "e", OS: "rhel"},
		{Hostname: "f", OS: "debian"},
	}
	specs := []FilterAxisSpec{
		{Key: "os", Label: "OS", Extract: func(r ui.EnvoyRow) []string { return strOrNil(r.OS) }},
	}
	axes := EnumerateFilterAxes(nil, rows, specs, 0)
	assert.Len(t, axes, 1)
	axis := axes[0]
	assert.Equal(t, "os", axis.Key)
	assert.False(t, axis.IsEnum)
	// Ordered by count desc, then value asc.
	if assert.Len(t, axis.Choices, 3) {
		assert.Equal(t, "ubuntu", axis.Choices[0].Value)
		assert.Equal(t, 3, axis.Choices[0].Count)
		assert.Equal(t, "rhel", axis.Choices[1].Value)
		assert.Equal(t, 2, axis.Choices[1].Count)
		assert.Equal(t, "debian", axis.Choices[2].Value)
		assert.Equal(t, 1, axis.Choices[2].Count)
	}
}

func TestEnumerateFilterAxes_TopNCapPreservesSelected(t *testing.T) {
	rows := []ui.EnvoyRow{
		{Modules: []string{"a"}}, {Modules: []string{"a"}},
		{Modules: []string{"b"}}, {Modules: []string{"b"}},
		{Modules: []string{"c"}}, {Modules: []string{"d"}},
		{Modules: []string{"e"}},
	}
	specs := []FilterAxisSpec{
		{Key: "module", Label: "Module", Selected: "e", Extract: func(r ui.EnvoyRow) []string { return r.Modules }},
	}
	axes := EnumerateFilterAxes(nil, rows, specs, 2) // top-2 by count
	axis := axes[0]
	// Top-2 are a (2) and b (2). 'e' has count 1 but is selected, so kept.
	values := make([]string, 0, len(axis.Choices))
	for _, c := range axis.Choices {
		values = append(values, c.Value)
	}
	assert.Contains(t, values, "a")
	assert.Contains(t, values, "b")
	assert.Contains(t, values, "e")
	assert.NotContains(t, values, "c")
	assert.NotContains(t, values, "d")
	// The 'e' pill is selected.
	for _, c := range axis.Choices {
		if c.Value == "e" {
			assert.True(t, c.Selected, "selected pill should carry Selected=true")
		}
	}
}

func TestEnumerateFilterAxes_SelectedValueRendersEvenIfAbsentFromRows(t *testing.T) {
	// Operator filtered down to env=staging but no staging envoys
	// exist (e.g. they were all destroyed). The 'staging' pill must
	// still render so the operator can toggle it off and recover.
	rows := []ui.EnvoyRow{
		{Hostname: "a", Environment: "production"},
	}
	specs := []FilterAxisSpec{
		{Key: "env", Label: "Env", Selected: "staging", Extract: func(r ui.EnvoyRow) []string { return strOrNil(r.Environment) }},
	}
	axes := EnumerateFilterAxes(nil, rows, specs, 0)
	values := make(map[string]int)
	selected := make(map[string]bool)
	for _, c := range axes[0].Choices {
		values[c.Value] = c.Count
		selected[c.Value] = c.Selected
	}
	assert.Equal(t, 0, values["staging"])
	assert.True(t, selected["staging"])
	assert.Equal(t, 1, values["production"])
	assert.False(t, selected["production"])
}

func TestEnumerateFilterAxes_EnumAxisUsesProvidedOrderAndCounts(t *testing.T) {
	specs := []FilterAxisSpec{
		{
			Key:       "conv",
			Label:     "Failure",
			Selected:  "degraded",
			EnumOrder: []string{"converged", "degraded", "no data", "failed"},
			EnumCount: map[string]int{
				"converged": 10, "degraded": 3, "no data": 0, "failed": 1,
			},
		},
	}
	axes := EnumerateFilterAxes(nil, nil, specs, 0)
	axis := axes[0]
	assert.True(t, axis.IsEnum)
	if assert.Len(t, axis.Choices, 4) {
		// Order preserved from EnumOrder; counts from EnumCount.
		assert.Equal(t, "converged", axis.Choices[0].Value)
		assert.Equal(t, 10, axis.Choices[0].Count)
		assert.False(t, axis.Choices[0].Selected)
		assert.Equal(t, "degraded", axis.Choices[1].Value)
		assert.True(t, axis.Choices[1].Selected)
		assert.Equal(t, "no data", axis.Choices[2].Value)
		assert.Equal(t, 0, axis.Choices[2].Count)
	}
}

func TestEnumerateFilterAxes_FreeTextPillURLTogglesCSV(t *testing.T) {
	rows := []ui.EnvoyRow{{OS: "ubuntu"}, {OS: "rhel"}}
	specs := []FilterAxisSpec{
		{Key: "os", Label: "OS", Selected: "ubuntu", Extract: func(r ui.EnvoyRow) []string { return strOrNil(r.OS) }},
	}
	axes := EnumerateFilterAxes(map[string]string{"os": "ubuntu"}, rows, specs, 0)
	urls := map[string]string{}
	for _, c := range axes[0].Choices {
		urls[c.Value] = c.URL
	}
	// Clicking the already-selected pill clears the axis (URL is empty
	// → template falls through to base path).
	assert.Equal(t, "", urls["ubuntu"])
	// Clicking an unselected pill appends to the CSV.
	assert.Equal(t, "?os=ubuntu,rhel", urls["rhel"])
}

func TestEnumerateFilterAxes_EnumPillURLSetsOrClears(t *testing.T) {
	specs := []FilterAxisSpec{
		{
			Key:       "conv",
			Selected:  "converged",
			EnumOrder: []string{"converged", "degraded"},
			EnumCount: map[string]int{"converged": 5, "degraded": 2},
		},
	}
	axes := EnumerateFilterAxes(map[string]string{"conv": "converged"}, nil, specs, 0)
	urls := map[string]string{}
	for _, c := range axes[0].Choices {
		urls[c.Value] = c.URL
	}
	// Already-selected enum value: clicking clears the axis.
	assert.Equal(t, "", urls["converged"])
	// Clicking another enum value REPLACES (single-select semantics).
	assert.Equal(t, "?conv=degraded", urls["degraded"])
}

func TestEnumerateFilterAxes_PreservesUnrelatedBaseQS(t *testing.T) {
	rows := []ui.EnvoyRow{{Environment: "production"}}
	specs := []FilterAxisSpec{
		{Key: "env", Label: "Env", Extract: func(r ui.EnvoyRow) []string { return strOrNil(r.Environment) }},
	}
	axes := EnumerateFilterAxes(
		map[string]string{"sort": "hostname", "search": "web"},
		rows, specs, 0,
	)
	url := axes[0].Choices[0].URL
	// Toggling env adds env=production while preserving sort + search.
	assert.Contains(t, url, "env=production")
	assert.Contains(t, url, "search=web")
	assert.Contains(t, url, "sort=hostname")
}

func TestBuildEnvoyFilterAxes_ActiveCount(t *testing.T) {
	rows := []ui.EnvoyRow{{Environment: "production"}}
	counts := ConvergenceCounters{Total: 1, Converged: 1}
	_, active := BuildEnvoyFilterAxes(rows, counts, 0,
		"", "", "", "", "production", "", "", "", "", "", "")
	assert.Equal(t, 1, active, "one selected axis → ActiveFilterCount=1")

	_, active = BuildEnvoyFilterAxes(rows, counts, 0,
		"converged", "", "", "web", "production", "", "", "", "", "", "")
	assert.Equal(t, 3, active, "conv + search + env all count")
}

func TestBuildEnvoyFilterAxes_EmptyAxesSuppressedByCallerLogic(t *testing.T) {
	// EnumerateFilterAxes returns an axis with empty Choices when no
	// rows have any value for it. The template's {{if .Choices}} drops
	// the section. Verify the empty-Choices contract.
	rows := []ui.EnvoyRow{{Hostname: "a"}} // no env/tag/role/os/arch/module/compliance
	axes, _ := BuildEnvoyFilterAxes(rows, ConvergenceCounters{}, 0,
		"", "", "", "", "", "", "", "", "", "", "")
	for _, axis := range axes {
		if axis.IsEnum {
			continue // enum axes always render with their fixed order
		}
		assert.Empty(t, axis.Choices, "free-text axis %q should have no choices when no row sets it", axis.Key)
	}
}
