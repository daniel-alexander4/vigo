// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"encoding/json"
	"net/http"
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
}

// Search performs a fleet-wide CVE search. Satisfies ui.CVESearcher.
func (h *CVEHandler) Search(cveID string) CVEFleetResult {
	cveID = strings.ToUpper(cveID)
	result := CVEFleetResult{}
	for _, envoy := range h.fleetIndex.AllEnvoys() {
		if envoy.Revoked {
			continue
		}
		for _, m := range searchCVEInTraits(envoy.ID, envoy.Hostname, envoy.Traits, cveID) {
			result.Hosts = append(result.Hosts, CVEFleetMatch{
				Hostname: m.Hostname, Package: m.Package, Installed: m.Installed,
				Fixed: m.Fixed, Severity: m.Severity, Scanner: m.Scanner,
			})
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
	Hostname  string
	Package   string
	Installed string
	Fixed     string
	Severity  string
	Scanner   string
}

func (h *CVEHandler) handleCVESearch(w http.ResponseWriter, r *http.Request) {
	cveID := strings.ToUpper(r.PathValue("cveID"))
	if cveID == "" {
		http.Error(w, "cve ID required", http.StatusBadRequest)
		return
	}

	result := CVESearchResult{CVEID: cveID}

	for _, envoy := range h.fleetIndex.AllEnvoys() {
		if envoy.Revoked {
			continue
		}
		matches := searchCVEInTraits(envoy.ID, envoy.Hostname, envoy.Traits, cveID)
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
