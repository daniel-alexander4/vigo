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
