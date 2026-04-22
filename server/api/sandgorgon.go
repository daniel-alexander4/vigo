// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"database/sql"
	"encoding/json"
	"net/http"

	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/sandgorgon"
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
	serverAddr string
}

// NewSandgorgonHandler creates a sandgorgon API handler.
func NewSandgorgonHandler(queries *db.Queries, fleetIndex *fleet.FleetIndex, orgName, serverAddr string) *SandgorgonHandler {
	mgr := sandgorgon.NewManager(queries)
	if fleetIndex != nil {
		mgr.SetFleetIndex(fleetIndex)
	}
	return &SandgorgonHandler{
		mgr:        mgr,
		queries:    queries,
		fleetIndex: fleetIndex,
		orgName:    orgName,
		serverAddr: serverAddr,
	}
}

// SetLifecycleDataSource enables operational data (risk, compliance, convergence) in reports.
func (h *SandgorgonHandler) SetLifecycleDataSource(ds sandgorgon.LifecycleDataSource) {
	h.mgr.SetLifecycleDataSource(ds)
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
	mux.HandleFunc("POST /api/v1/sandgorgon/provision/{id}", h.provision)
	mux.HandleFunc("GET /api/v1/sandgorgon/images", h.listImages)
	mux.HandleFunc("POST /api/v1/sandgorgon/images", h.registerImage)
	mux.HandleFunc("DELETE /api/v1/sandgorgon/images/{id}", h.removeImage)
	mux.HandleFunc("GET /api/v1/sandgorgon/dhcp-config", h.dhcpConfig)
	mux.HandleFunc("GET /api/v1/sandgorgon/boot/{id}", h.bootScript)
	mux.HandleFunc("GET /api/v1/sandgorgon/preseed/{id}", h.preseedConfig)
	mux.HandleFunc("GET /api/v1/sandgorgon/report/{id}", h.lifecycleReport)
	mux.HandleFunc("GET /api/v1/sandgorgon/report/{id}/html", h.lifecycleReportHTML)
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
		MACAddress   string `json:"mac_address"`
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
	if req.Hostname == "" && req.BMCAddress == "" {
		http.Error(w, jsonErr("hostname or bmc_address is required"), http.StatusBadRequest)
		return
	}

	// Resolve envoy from hostname and auto-populate from traits.
	var envoyID string
	if req.Hostname != "" && h.fleetIndex != nil {
		envoyID = h.fleetIndex.GetByHostname(req.Hostname)
		if envoyID != "" {
			envoy, exists, _ := h.fleetIndex.GetEnvoyLite(envoyID)
			if exists && envoy != nil {
				if req.SerialNumber == "" {
					req.SerialNumber = envoy.Traits["dmi.product_serial"]
				}
				if req.Model == "" {
					req.Model = envoy.Traits["dmi.product_name"]
				}
				if req.Manufacturer == "" {
					req.Manufacturer = envoy.Traits["dmi.product_vendor"]
				}
				if req.MACAddress == "" {
					req.MACAddress = envoy.Traits["network.mac_addresses"]
				}
			}
		}
	}

	// Auto-discover hardware from BMC if available and serial/model still missing.
	if req.BMCAddress != "" && (req.SerialNumber == "" || req.Model == "") {
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

	state := "available"
	if envoyID != "" {
		state = "commissioned"
	}

	asset, err := h.queries.CreateSandgorgonAsset(r.Context(), db.CreateSandgorgonAssetParams{
		Hostname:       req.Hostname,
		BmcAddress:     req.BMCAddress,
		BmcUsername:    req.BMCUsername,
		BmcPassword:    req.BMCPassword,
		MacAddress:     req.MACAddress,
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

func (h *SandgorgonHandler) provision(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		ImageName   string `json:"image_name"`
		Hostname    string `json:"hostname"`
		EnrollToken string `json:"enroll_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, jsonErr("invalid JSON: "+err.Error()), http.StatusBadRequest)
		return
	}
	if req.ImageName == "" {
		http.Error(w, jsonErr("image_name is required"), http.StatusBadRequest)
		return
	}

	result, err := h.mgr.Provision(r.Context(), sandgorgon.ProvisionRequest{
		AssetID:     id,
		ImageName:   req.ImageName,
		Hostname:    req.Hostname,
		EnrollToken: req.EnrollToken,
	}, h.serverAddr)
	if err != nil {
		http.Error(w, jsonErr(err.Error()), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (h *SandgorgonHandler) listImages(w http.ResponseWriter, r *http.Request) {
	images, err := h.queries.ListSandgorgonImages(r.Context())
	if err != nil {
		http.Error(w, jsonErr(err.Error()), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(images)
}

func (h *SandgorgonHandler) registerImage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		OSFamily    string `json:"os_family"`
		OSVersion   string `json:"os_version"`
		Arch        string `json:"arch"`
		ImageType   string `json:"image_type"`
		Path        string `json:"path"`
		KernelPath  string `json:"kernel_path"`
		InitrdPath  string `json:"initrd_path"`
		BootParams  string `json:"boot_params"`
		PreseedType string `json:"preseed_type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, jsonErr("invalid JSON: "+err.Error()), http.StatusBadRequest)
		return
	}

	image, err := h.mgr.RegisterImage(r.Context(), sandgorgon.ImageRegistration{
		Name:        req.Name,
		OSFamily:    req.OSFamily,
		OSVersion:   req.OSVersion,
		Arch:        req.Arch,
		ImageType:   req.ImageType,
		Path:        req.Path,
		KernelPath:  req.KernelPath,
		InitrdPath:  req.InitrdPath,
		BootParams:  req.BootParams,
		PreseedType: req.PreseedType,
	})
	if err != nil {
		http.Error(w, jsonErr(err.Error()), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(image)
}

func (h *SandgorgonHandler) removeImage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.mgr.RemoveImage(r.Context(), id); err != nil {
		http.Error(w, jsonErr(err.Error()), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (h *SandgorgonHandler) dhcpConfig(w http.ResponseWriter, r *http.Request) {
	dhcpType := r.URL.Query().Get("type")
	if dhcpType == "" {
		dhcpType = "isc"
	}
	config, err := sandgorgon.GenerateDHCPConfig(h.serverAddr, dhcpType)
	if err != nil {
		http.Error(w, jsonErr(err.Error()), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(config))
}

func (h *SandgorgonHandler) bootScript(w http.ResponseWriter, r *http.Request) {
	assetID := r.PathValue("id")
	asset, err := h.queries.GetSandgorgonAsset(r.Context(), assetID)
	if err != nil {
		http.Error(w, "asset not found", http.StatusNotFound)
		return
	}

	// Find the most recent provisioning event to get the image name.
	events, _ := h.queries.ListSandgorgonEventsByAsset(r.Context(), db.ListSandgorgonEventsByAssetParams{
		AssetID: assetID,
		Limit:   10,
	})

	var imageName string
	for _, e := range events {
		if e.EventType == "provisioning_started" {
			// Parse "image: ubuntu-24.04-amd64, hostname: web01" from detail.
			if idx := len("image: "); len(e.Detail) > idx {
				detail := e.Detail[idx:]
				if comma := len(detail); comma > 0 {
					for i, c := range detail {
						if c == ',' {
							imageName = detail[:i]
							break
						}
					}
					if imageName == "" {
						imageName = detail
					}
				}
			}
			break
		}
	}

	if imageName == "" {
		http.Error(w, "no provisioning in progress for this asset", http.StatusNotFound)
		return
	}

	image, err := h.queries.GetSandgorgonImageByName(r.Context(), imageName)
	if err != nil {
		http.Error(w, "image not found: "+imageName, http.StatusNotFound)
		return
	}

	script, err := sandgorgon.GenerateIPXEScript(image, h.serverAddr, asset.Hostname, "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(script))
}

func (h *SandgorgonHandler) lifecycleReport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	report, err := h.mgr.BuildLifecycleReport(r.Context(), id, h.orgName)
	if err != nil {
		http.Error(w, jsonErr(err.Error()), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(report)
}

func (h *SandgorgonHandler) lifecycleReportHTML(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	report, err := h.mgr.BuildLifecycleReport(r.Context(), id, h.orgName)
	if err != nil {
		http.Error(w, "asset not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := sandgorgon.RenderLifecycleReportHTML(w, report); err != nil {
		http.Error(w, "rendering report: "+err.Error(), http.StatusInternalServerError)
	}
}

func (h *SandgorgonHandler) preseedConfig(w http.ResponseWriter, r *http.Request) {
	imageID := r.PathValue("id")
	hostname := r.URL.Query().Get("hostname")
	token := r.URL.Query().Get("token")

	image, err := h.queries.GetSandgorgonImage(r.Context(), imageID)
	if err != nil {
		http.Error(w, "image not found", http.StatusNotFound)
		return
	}

	config, err := sandgorgon.GeneratePreseedConfig(image, h.serverAddr, hostname, token)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(config))
}

func jsonErr(msg string) string {
	b, _ := json.Marshal(map[string]string{"error": msg})
	return string(b)
}
