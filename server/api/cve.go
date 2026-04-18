// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/daniel-alexander4/vigo/server/fleet"
	"github.com/daniel-alexander4/vigo/server/traits"
)

// CVEHandler serves CVE impact analysis endpoints.
type CVEHandler struct {
	fleetIndex *fleet.FleetIndex
}

// NewCVEHandler creates a CVEHandler.
func NewCVEHandler(fleetIndex *fleet.FleetIndex) *CVEHandler {
	return &CVEHandler{fleetIndex: fleetIndex}
}

// RegisterRoutes mounts CVE routes on the given mux.
func (h *CVEHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/cve/{cveID}", h.handleCVESearch)
}

// CVESearchResult is the response for a CVE impact search.
type CVESearchResult struct {
	CVEID         string         `json:"cve_id"`
	AffectedHosts []CVEHostMatch `json:"affected_hosts"`
	TotalHosts    int            `json:"total_hosts"`
}

// CVEHostMatch is a single host affected by a CVE.
type CVEHostMatch struct {
	Hostname  string `json:"hostname"`
	EnvoyID   string `json:"envoy_id"`
	Package   string `json:"package"`
	Installed string `json:"installed"`
	Fixed     string `json:"fixed"`
	Severity  string `json:"severity"`
	Scanner   string `json:"scanner"`
	// RunningBinaries lists currently-executing binaries affected by
	// this CVE on this host, sourced from the `cve_running_correlation`
	// trait. Empty when the trait is absent (agent hasn't emitted a
	// correlation yet), when no running binaries match this CVE, or
	// when the scanner output disagrees about which packages an
	// affected binary belongs to.
	RunningBinaries []CVERunningBinary `json:"running_binaries,omitempty"`
}

// CVERunningBinary is a single running binary affected by a CVE. Data
// comes from the agent-side `cve_running_correlation` trait, which
// joins scanner output with `/proc/*/exe` + dpkg/rpm ownership.
type CVERunningBinary struct {
	Package        string `json:"package"`
	PackageVersion string `json:"package_version"`
	BinaryPath     string `json:"binary_path"`
	PidCount       int    `json:"pid_count"`
	Deleted        bool   `json:"deleted"`
}

// Search performs a fleet-wide CVE search. When runningOnly is true,
// hosts without a running-binary match in their
// cve_running_correlation trait are dropped from the result. Matches
// retained always carry their running-binary list in the
// RunningBinaries slice so callers can render it even when filtering
// is off.
func (h *CVEHandler) Search(cveID string, runningOnly bool) CVEFleetResult {
	cveID = strings.ToUpper(cveID)
	result := CVEFleetResult{}
	for _, envoy := range h.fleetIndex.AllEnvoys() {
		if envoy.Revoked {
			continue
		}
		matches := searchCVEInTraits(envoy.ID, envoy.Hostname, envoy.Traits, cveID)
		running, runningSeverity := runningBinariesForCVE(envoy.Traits, cveID)
		if runningOnly && len(running) == 0 {
			continue
		}
		// Synthesize a row when only the correlation trait has the
		// CVE — the same case the REST handler treats as a
		// "running-correlation" scanner match.
		if len(matches) == 0 && len(running) > 0 {
			matches = append(matches, CVEHostMatch{
				Hostname: envoy.Hostname,
				EnvoyID:  envoy.ID,
				Severity: runningSeverity,
				Scanner:  "running-correlation",
			})
		}
		for _, m := range matches {
			fleetMatch := CVEFleetMatch{
				Hostname: m.Hostname, Package: m.Package, Installed: m.Installed,
				Fixed: m.Fixed, Severity: m.Severity, Scanner: m.Scanner,
			}
			for _, rb := range running {
				fleetMatch.RunningBinaries = append(fleetMatch.RunningBinaries, CVEFleetRunningBinary{
					BinaryPath:     rb.BinaryPath,
					Package:        rb.Package,
					PackageVersion: rb.PackageVersion,
					PidCount:       rb.PidCount,
					Deleted:        rb.Deleted,
				})
			}
			result.Hosts = append(result.Hosts, fleetMatch)
		}
	}
	result.TotalHosts = len(result.Hosts)
	return result
}

// CVEFleetResult is the search result for ui.CVESearcher.
type CVEFleetResult struct {
	TotalHosts int
	Hosts      []CVEFleetMatch
}

// CVEFleetMatch is a single match for ui.CVESearcher.
type CVEFleetMatch struct {
	Hostname        string
	Package         string
	Installed       string
	Fixed           string
	Severity        string
	Scanner         string
	RunningBinaries []CVEFleetRunningBinary
}

// CVEFleetRunningBinary is a single running-binary reference attached
// to a fleet match. Mirrors the CVE REST endpoint's running-binary
// shape for UI consumption.
type CVEFleetRunningBinary struct {
	BinaryPath     string
	Package        string
	PackageVersion string
	PidCount       int
	Deleted        bool
}

func (h *CVEHandler) handleCVESearch(w http.ResponseWriter, r *http.Request) {
	cveID := strings.ToUpper(r.PathValue("cveID"))
	if cveID == "" {
		http.Error(w, "cve ID required", http.StatusBadRequest)
		return
	}

	// ?running_only=true narrows the result to envoys where at least
	// one currently-executing binary is affected by this CVE,
	// according to the `cve_running_correlation` trait. Envoys that
	// report the CVE via security_scan but have no matching running
	// binaries (or no correlation trait at all) are dropped.
	runningOnly := r.URL.Query().Get("running_only") == "true"

	result := CVESearchResult{CVEID: cveID}

	for _, envoy := range h.fleetIndex.AllEnvoys() {
		if envoy.Revoked {
			continue
		}
		matches := searchCVEInTraits(envoy.ID, envoy.Hostname, envoy.Traits, cveID)
		running, runningSeverity := runningBinariesForCVE(envoy.Traits, cveID)
		if len(matches) == 0 && len(running) == 0 {
			continue
		}
		// Attach running-binary detail to every match for this envoy.
		// All matches from one envoy share the same running-binary
		// list — the CVE is the same, the host is the same.
		for i := range matches {
			matches[i].RunningBinaries = running
		}
		// Synthesize a match when the envoy has a running-binary hit
		// but the CVE doesn't appear in security_scan (rare: the
		// correlation trait is fresher than security_scan, or the
		// agent correlated from a scanner we don't aggregate counts
		// from). The synthesized row lets those hosts still show up.
		if len(matches) == 0 && len(running) > 0 {
			matches = append(matches, CVEHostMatch{
				Hostname:        envoy.Hostname,
				EnvoyID:         envoy.ID,
				Severity:        runningSeverity,
				Scanner:         "running-correlation",
				RunningBinaries: running,
			})
		}
		if runningOnly && len(running) == 0 {
			continue
		}
		result.AffectedHosts = append(result.AffectedHosts, matches...)
	}
	result.TotalHosts = len(result.AffectedHosts)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// searchCVEInTraits scans the unified security_scan.cves.N.{id,severity,packages}
// flattened traits for a specific CVE ID.
func searchCVEInTraits(envoyID, hostname string, traitMap map[string]string, cveID string) []CVEHostMatch {
	type cveEntry struct {
		id       string
		severity string
		packages string
	}

	byIndex := make(map[string]*cveEntry)
	for k, v := range traitMap {
		if !strings.HasPrefix(k, traits.PrefixCVEs) {
			continue
		}
		rest := k[len(traits.PrefixCVEs):]
		idx, field, ok := strings.Cut(rest, ".")
		if !ok {
			continue
		}
		e := byIndex[idx]
		if e == nil {
			e = &cveEntry{}
			byIndex[idx] = e
		}
		switch field {
		case "id":
			e.id = v
		case "severity":
			e.severity = v
		case "packages":
			e.packages = v
		}
	}

	var matches []CVEHostMatch
	for _, e := range byIndex {
		if strings.EqualFold(e.id, cveID) {
			matches = append(matches, CVEHostMatch{
				Hostname: hostname,
				EnvoyID:  envoyID,
				Severity: e.severity,
				Scanner:  "unified",
			})
		}
	}
	return matches
}

// runningBinariesForCVE walks the flattened `cve_running_correlation.running_cves.N.*`
// trait keys on a single envoy and returns the subset of rows whose
// `cve_id` field matches the requested CVE, along with the severity
// from any matched row (all rows for one CVE share a severity).
func runningBinariesForCVE(traitMap map[string]string, cveID string) ([]CVERunningBinary, string) {
	type rowBuilder struct {
		cveID          string
		severity       string
		pkg            string
		pkgVersion     string
		binaryPath     string
		pidCount       string
		deleted        string
	}

	rows := make(map[string]*rowBuilder)
	for k, v := range traitMap {
		if !strings.HasPrefix(k, traits.PrefixRunningCVEs) {
			continue
		}
		rest := k[len(traits.PrefixRunningCVEs):]
		idx, field, ok := strings.Cut(rest, ".")
		if !ok {
			continue
		}
		b := rows[idx]
		if b == nil {
			b = &rowBuilder{}
			rows[idx] = b
		}
		switch field {
		case traits.RunningCVEFieldID:
			b.cveID = v
		case traits.RunningCVEFieldSeverity:
			b.severity = v
		case traits.RunningCVEFieldPackage:
			b.pkg = v
		case traits.RunningCVEFieldPkgVersion:
			b.pkgVersion = v
		case traits.RunningCVEFieldBinaryPath:
			b.binaryPath = v
		case traits.RunningCVEFieldPidCount:
			b.pidCount = v
		case traits.RunningCVEFieldDeleted:
			b.deleted = v
		}
	}

	var out []CVERunningBinary
	var severity string
	for _, b := range rows {
		if !strings.EqualFold(b.cveID, cveID) {
			continue
		}
		if severity == "" {
			severity = b.severity
		}
		pid := 0
		if b.pidCount != "" {
			pid, _ = strconv.Atoi(b.pidCount)
		}
		out = append(out, CVERunningBinary{
			Package:        b.pkg,
			PackageVersion: b.pkgVersion,
			BinaryPath:     b.binaryPath,
			PidCount:       pid,
			Deleted:        b.deleted == "true",
		})
	}
	// Deterministic order for readable diffs: binary path asc.
	sort.Slice(out, func(i, j int) bool {
		return out[i].BinaryPath < out[j].BinaryPath
	})
	return out, severity
}
