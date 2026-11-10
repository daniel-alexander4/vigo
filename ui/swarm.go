// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"net/http"
	"strconv"

	"git.alexander4.io/vigo/server/swarmview"
)

// SwarmSource is the minimum interface the UI needs to render the
// substrate overview. Implemented by *api.SwarmHandler.
type SwarmSource interface {
	SnapshotStatus() swarmview.Status
	SnapshotPeers(sortKey string, limit int) swarmview.Peers
}

// SetSwarmAPI wires the substrate-view source.
func (h *Handler) SetSwarmAPI(src SwarmSource) {
	h.swarmAPI = src
}

// SwarmPageData backs templates/pages/swarm.html.
type SwarmPageData struct {
	PageData
	Status        swarmview.Status
	Peers         swarmview.Peers
	StatusHuman   SwarmStatusHuman
	PeerRows      []SwarmPeerRowHuman
	Sort          string
	Limit         int
	TotalPeers    int
	ReturnedPeers int
	Truncated     bool
}

// SwarmStatusHuman renders human-readable variants of select Status
// fields for the template.
type SwarmStatusHuman struct {
	TotalFootprint string
}

// SwarmPeerRowHuman wraps a PeerRow with render-ready strings.
type SwarmPeerRowHuman struct {
	swarmview.PeerRow
	FootprintHuman string
	LastSeenRel    string
}

// Swarm renders the fleet-wide substrate overview at /swarm.
func (h *Handler) Swarm(w http.ResponseWriter, r *http.Request) {
	data := SwarmPageData{
		PageData: PageData{Title: "Swarm", ActiveNav: "swarm"},
	}
	h.setCommonWithRequest(&data.PageData, r)

	if h.swarmAPI == nil {
		h.renderer.Render(w, "swarm", data)
		return
	}

	sortKey := r.URL.Query().Get("sort")
	if sortKey == "" {
		sortKey = "footprint"
	}
	limit := 50
	if s := r.URL.Query().Get("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n >= 0 {
			limit = n
		}
	}

	data.Status = h.swarmAPI.SnapshotStatus()
	data.Peers = h.swarmAPI.SnapshotPeers(sortKey, limit)
	data.Sort = sortKey
	data.Limit = limit
	data.TotalPeers = data.Peers.Total
	data.ReturnedPeers = data.Peers.Returned
	data.Truncated = data.Peers.Total > data.Peers.Returned
	data.StatusHuman.TotalFootprint = humanSize(data.Status.TotalFootprintBytes)

	for _, p := range data.Peers.Envoys {
		row := SwarmPeerRowHuman{
			PeerRow:        p,
			FootprintHuman: humanSize(p.FootprintBytes),
		}
		if !p.LastSeen.IsZero() {
			row.LastSeenRel = formatRelative(p.LastSeen)
		} else {
			row.LastSeenRel = "—"
		}
		data.PeerRows = append(data.PeerRows, row)
	}

	h.renderer.Render(w, "swarm", data)
}
