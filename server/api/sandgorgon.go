// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"database/sql"
	"encoding/json"
	"net/http"

	"github.com/daniel-alexander4/vigo/server/db"
	"github.com/daniel-alexander4/vigo/server/fleet"
	"github.com/daniel-alexander4/vigo/server/sandgorgon"
)

func toNullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// SandgorgonHandler provides REST endpoints for bare metal lifecycle management.
type SandgorgonHandler struct {
	mgr        *sandgorgon.Manager
	queries    *db.Queries
	fleetIndex *fleet.FleetIndex
	orgName    string
}

// NewSandgorgonHandler creates a sandgorgon API handler.
func NewSandgorgonHandler(queries *db.Queries, fleetIndex *fleet.FleetIndex, orgName string) *SandgorgonHandler {
	mgr := sandgorgon.NewManager(queries)
	if fleetIndex != nil {
		mgr.SetFleetIndex(fleetIndex)
	}
	return &SandgorgonHandler{
		mgr:        mgr,
		queries:    queries,
		fleetIndex: fleetIndex,
		orgName:    orgName,
	}
}

// RegisterRoutes mounts sandgorgon endpoints on the mux.
func (h *SandgorgonHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/sandgorgon/assets", h.listAssets)
	mux.HandleFunc("GET /api/v1/sandgorgon/assets/{id}", h.getAsset)
	mux.HandleFunc("POST /api/v1/sandgorgon/commission", h.commission)
	mux.HandleFunc("POST /api/v1/sandgorgon/decommission/{id}", h.decommission)
	mux.HandleFunc("GET /api/v1/sandgorgon/certificates", h.listCertificates)
	mux.HandleFunc("GET /api/v1/sandgorgon/certificates/{id}", h.getCertificate)
	mux.HandleFunc("GET /api/v1/sandgorgon/certificates/{id}/html", h.getCertificateHTML)
	mux.HandleFunc("POST /api/v1/sandgorgon/import", h.importCSV)
	mux.HandleFunc("POST /api/v1/sandgorgon/return-to-available/{id}", h.returnToAvailable)
	mux.HandleFunc("GET /api/v1/sandgorgon/events/{assetID}", h.listEvents)
	mux.HandleFunc("GET /api/v1/sandgorgon/assets/search", h.searchAssets)
}

func (h *SandgorgonHandler) listAssets(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	var assets []db.SandgorgonAsset
	var err error

	if state != "" {
		assets, err = h.queries.ListSandgorgonAssetsByState(r.Context(), state)
	} else {
		assets, err = h.queries.ListSandgorgonAssets(r.Context())
	}
	if err != nil {
		http.Error(w, jsonErr("listing assets: "+err.Error()), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(assets)
}

func (h *SandgorgonHandler) getAsset(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	asset, err := h.queries.GetSandgorgonAsset(r.Context(), id)
	if err != nil {
		http.Error(w, jsonErr("asset not found"), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(asset)
}

func (h *SandgorgonHandler) commission(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Hostname     string `json:"hostname"`
		BMCAddress   string `json:"bmc_address"`
		BMCUsername  string `json:"bmc_username"`
		BMCPassword  string `json:"bmc_password"`
		SerialNumber string `json:"serial_number"`
		Manufacturer string `json:"manufacturer"`
		Model        string `json:"model"`
		AssetTag     string `json:"asset_tag"`
		RackLocation string `json:"rack_location"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, jsonErr("invalid JSON: "+err.Error()), http.StatusBadRequest)
		return
	}
	if req.BMCAddress == "" {
		http.Error(w, jsonErr("bmc_address is required"), http.StatusBadRequest)
		return
	}

	// Auto-discover hardware from BMC if serial/model not provided.
	if req.SerialNumber == "" || req.Model == "" {
		rf := sandgorgon.NewRedfishClient(req.BMCAddress, req.BMCUsername, req.BMCPassword)
		if info, err := rf.GetSystemInfo(r.Context()); err == nil {
			if req.SerialNumber == "" {
				req.SerialNumber = info.SerialNumber
			}
			if req.Model == "" {
				req.Model = info.Model
			}
			if req.Manufacturer == "" {
				req.Manufacturer = info.Manufacturer
			}
		}
		// Non-fatal: if BMC unreachable, proceed with whatever was provided.
	}

	// Resolve envoy from hostname.
	var envoyID string
	if req.Hostname != "" && h.fleetIndex != nil {
		envoyID = h.fleetIndex.GetByHostname(req.Hostname)
	}

	state := "available"
	if envoyID != "" {
		state = "commissioned"
	}

	asset, err := h.queries.CreateSandgorgonAsset(r.Context(), db.CreateSandgorgonAssetParams{
		Hostname:       req.Hostname,
		BmcAddress:     req.BMCAddress,
		BmcUsername:    req.BMCUsername,
		BmcPassword:    req.BMCPassword,
		SerialNumber:   req.SerialNumber,
		Manufacturer:   req.Manufacturer,
		Model:          req.Model,
		AssetTag:       req.AssetTag,
		RackLocation:   req.RackLocation,
		LifecycleState: state,
		EnvoyID:        toNullString(envoyID),
	})
	if err != nil {
		http.Error(w, jsonErr("creating asset: "+err.Error()), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(asset)
}

func (h *SandgorgonHandler) decommission(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req struct {
		Technician string `json:"technician"`
		Method     string `json:"method"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, jsonErr("invalid JSON: "+err.Error()), http.StatusBadRequest)
		return
	}
	if req.Technician == "" {
		req.Technician = "api"
	}
	if req.Method == "" {
		req.Method = "auto"
	}

	result, err := h.mgr.Decommission(r.Context(), sandgorgon.DecommissionRequest{
		AssetID:    id,
		Technician: req.Technician,
		Method:     req.Method,
	})
	if err != nil {
		http.Error(w, jsonErr(err.Error()), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (h *SandgorgonHandler) listCertificates(w http.ResponseWriter, r *http.Request) {
	certs, err := h.queries.ListSandgorgonCertificates(r.Context())
	if err != nil {
		http.Error(w, jsonErr(err.Error()), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(certs)
}

func (h *SandgorgonHandler) getCertificate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cert, err := h.queries.GetSandgorgonCertificate(r.Context(), id)
	if err != nil {
		http.Error(w, jsonErr("certificate not found"), http.StatusNotFound)
		return
	}
	data := sandgorgon.CertificateFromDB(cert, h.orgName)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func (h *SandgorgonHandler) getCertificateHTML(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cert, err := h.queries.GetSandgorgonCertificate(r.Context(), id)
	if err != nil {
		http.Error(w, "certificate not found", http.StatusNotFound)
		return
	}
	data := sandgorgon.CertificateFromDB(cert, h.orgName)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := sandgorgon.RenderCertificateHTML(w, data); err != nil {
		http.Error(w, "rendering certificate: "+err.Error(), http.StatusInternalServerError)
	}
}

func (h *SandgorgonHandler) importCSV(w http.ResponseWriter, r *http.Request) {
	// Accept multipart file upload.
	if err := r.ParseMultipartForm(10 << 20); err != nil { // 10 MB max
		http.Error(w, jsonErr("parsing form: "+err.Error()), http.StatusBadRequest)
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		http.Error(w, jsonErr("missing 'file' form field"), http.StatusBadRequest)
		return
	}
	defer file.Close()

	result, err := h.mgr.ImportCSVReader(r.Context(), file, h.fleetIndex)
	if err != nil {
		http.Error(w, jsonErr(err.Error()), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (h *SandgorgonHandler) listEvents(w http.ResponseWriter, r *http.Request) {
	assetID := r.PathValue("assetID")
	events, err := h.queries.ListSandgorgonEventsByAsset(r.Context(), db.ListSandgorgonEventsByAssetParams{
		AssetID: assetID,
		Limit:   int64(100),
	})
	if err != nil {
		http.Error(w, jsonErr(err.Error()), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(events)
}

func (h *SandgorgonHandler) returnToAvailable(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.mgr.ReturnToAvailable(r.Context(), id); err != nil {
		http.Error(w, jsonErr(err.Error()), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "lifecycle_state": "available"})
}

func (h *SandgorgonHandler) searchAssets(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		h.listAssets(w, r)
		return
	}
	pattern := "%" + q + "%"
	assets, err := h.queries.SearchSandgorgonAssets(r.Context(), pattern)
	if err != nil {
		http.Error(w, jsonErr(err.Error()), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(assets)
}

func jsonErr(msg string) string {
	b, _ := json.Marshal(map[string]string{"error": msg})
	return string(b)
}
