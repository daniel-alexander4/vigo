// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"database/sql"
	"encoding/json"
	"net/http"

	"git.alexander4.io/vigo/server/auth"
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
	mgr         *sandgorgon.Manager
	queries     *db.Queries
	fleetIndex  *fleet.FleetIndex
	orgName     string
	serverAddr  string
	auditWriter auditRecorder
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

// SetAuditWriter attaches the tamper-evident audit writer.
func (h *SandgorgonHandler) SetAuditWriter(w auditRecorder) { h.auditWriter = w }

// SetLifecycleDataSource enables operational data (risk, compliance, convergence) in reports.
func (h *SandgorgonHandler) SetLifecycleDataSource(ds sandgorgon.LifecycleDataSource) {
	h.mgr.SetLifecycleDataSource(ds)
}

// RegisterRoutes mounts sandgorgon endpoints on the mux. Bare-metal
// lifecycle mutations (commission / decommission / import / return-
// to-available / provision, image register / remove) are admin-only
// — they touch BMC state, NIST 800-88 sanitization records, and PXE
// boot configuration. Read endpoints (asset/certificate/image/event
// listings, lifecycle reports, search) gate on fleet.read.
//
// PXE-serving routes (boot/{id}, preseed/{id}, dhcp-config) are
// also fleet.read for now — the booting machine itself can't satisfy
// any web auth, so PXE delivery either rides a separate path or is
// operator-side state used to write the actual PXE config off-box.
// Gating as fleet.read tightens the access of authenticated users
// without changing PXE flow (which never had auth credentials to
// begin with). Revisit when the PXE delivery path is finalized.
func (h *SandgorgonHandler) RegisterRoutes(mux *http.ServeMux) {
	read := auth.RequirePermission(auth.PermFleetRead)
	admin := auth.RequireAdmin("")
	mux.Handle("GET /api/v1/sandgorgon/assets", read(http.HandlerFunc(h.listAssets)))
	mux.Handle("GET /api/v1/sandgorgon/assets/{id}", read(http.HandlerFunc(h.getAsset)))
	mux.Handle("POST /api/v1/sandgorgon/commission", admin(http.HandlerFunc(h.commission)))
	mux.Handle("POST /api/v1/sandgorgon/decommission/{id}", admin(http.HandlerFunc(h.decommission)))
	mux.Handle("GET /api/v1/sandgorgon/certificates", read(http.HandlerFunc(h.listCertificates)))
	mux.Handle("GET /api/v1/sandgorgon/certificates/{id}", read(http.HandlerFunc(h.getCertificate)))
	mux.Handle("GET /api/v1/sandgorgon/certificates/{id}/html", read(http.HandlerFunc(h.getCertificateHTML)))
	mux.Handle("POST /api/v1/sandgorgon/import", admin(http.HandlerFunc(h.importCSV)))
	mux.Handle("POST /api/v1/sandgorgon/return-to-available/{id}", admin(http.HandlerFunc(h.returnToAvailable)))
	mux.Handle("POST /api/v1/sandgorgon/provision/{id}", admin(http.HandlerFunc(h.provision)))
	mux.Handle("GET /api/v1/sandgorgon/images", read(http.HandlerFunc(h.listImages)))
	mux.Handle("POST /api/v1/sandgorgon/images", admin(http.HandlerFunc(h.registerImage)))
	mux.Handle("DELETE /api/v1/sandgorgon/images/{id}", admin(http.HandlerFunc(h.removeImage)))
	mux.Handle("GET /api/v1/sandgorgon/dhcp-config", read(http.HandlerFunc(h.dhcpConfig)))
	mux.Handle("GET /api/v1/sandgorgon/boot/{id}", read(http.HandlerFunc(h.bootScript)))
	mux.Handle("GET /api/v1/sandgorgon/preseed/{id}", read(http.HandlerFunc(h.preseedConfig)))
	mux.Handle("GET /api/v1/sandgorgon/report/{id}", read(http.HandlerFunc(h.lifecycleReport)))
	mux.Handle("GET /api/v1/sandgorgon/report/{id}/html", read(http.HandlerFunc(h.lifecycleReportHTML)))
	mux.Handle("GET /api/v1/sandgorgon/events/{assetID}", read(http.HandlerFunc(h.listEvents)))
	mux.Handle("GET /api/v1/sandgorgon/assets/search", read(http.HandlerFunc(h.searchAssets)))
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
		writeError(w, http.StatusInternalServerError, "listing assets: "+err.Error())
		return
	}

	writeJSON(w, r, http.StatusOK, assets)
}

func (h *SandgorgonHandler) getAsset(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	asset, err := h.queries.GetSandgorgonAsset(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "asset not found")
		return
	}

	writeJSON(w, r, http.StatusOK, asset)
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
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.Hostname == "" && req.BMCAddress == "" {
		writeError(w, http.StatusBadRequest, "hostname or bmc_address is required")
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
		writeError(w, http.StatusInternalServerError, "creating asset: "+err.Error())
		return
	}

	recordAudit(r.Context(), h.auditWriter, "sandgorgon.commission", actorFromRequest(r), "rest",
		"commissioned asset "+asset.ID+" (hostname="+req.Hostname+", state="+state+")", "")

	writeJSON(w, r, http.StatusCreated, asset)
}

func (h *SandgorgonHandler) decommission(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req struct {
		Technician string `json:"technician"`
		Method     string `json:"method"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
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
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	recordAudit(r.Context(), h.auditWriter, "sandgorgon.decommission", actorFromRequest(r), "rest",
		"decommissioned asset "+id+" (technician="+req.Technician+", method="+req.Method+")", "")

	writeJSON(w, r, http.StatusOK, result)
}

func (h *SandgorgonHandler) listCertificates(w http.ResponseWriter, r *http.Request) {
	certs, err := h.queries.ListSandgorgonCertificates(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, r, http.StatusOK, certs)
}

func (h *SandgorgonHandler) getCertificate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cert, err := h.queries.GetSandgorgonCertificate(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "certificate not found")
		return
	}
	data := sandgorgon.CertificateFromDB(cert, h.orgName)
	writeJSON(w, r, http.StatusOK, data)
}

func (h *SandgorgonHandler) getCertificateHTML(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cert, err := h.queries.GetSandgorgonCertificate(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "certificate not found")
		return
	}
	data := sandgorgon.CertificateFromDB(cert, h.orgName)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := sandgorgon.RenderCertificateHTML(w, data); err != nil {
		writeError(w, http.StatusInternalServerError, "rendering certificate: "+err.Error())
	}
}

func (h *SandgorgonHandler) importCSV(w http.ResponseWriter, r *http.Request) {
	// Accept multipart file upload.
	if err := r.ParseMultipartForm(10 << 20); err != nil { // 10 MB max
		writeError(w, http.StatusBadRequest, "parsing form: "+err.Error())
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing 'file' form field")
		return
	}
	defer file.Close()

	result, err := h.mgr.ImportCSVReader(r.Context(), file, h.fleetIndex)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	recordAudit(r.Context(), h.auditWriter, "sandgorgon.import_csv", actorFromRequest(r), "rest",
		"imported sandgorgon CSV", "")

	writeJSON(w, r, http.StatusOK, result)
}

func (h *SandgorgonHandler) listEvents(w http.ResponseWriter, r *http.Request) {
	assetID := r.PathValue("assetID")
	events, err := h.queries.ListSandgorgonEventsByAsset(r.Context(), db.ListSandgorgonEventsByAssetParams{
		AssetID: assetID,
		Limit:   int64(100),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, r, http.StatusOK, events)
}

func (h *SandgorgonHandler) returnToAvailable(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.mgr.ReturnToAvailable(r.Context(), id); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	recordAudit(r.Context(), h.auditWriter, "sandgorgon.return_to_available", actorFromRequest(r), "rest",
		"returned asset "+id+" to available pool", "")
	writeJSON(w, r, http.StatusOK, map[string]string{"status": "ok", "lifecycle_state": "available"})
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
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, r, http.StatusOK, assets)
}

func (h *SandgorgonHandler) provision(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		ImageName   string `json:"image_name"`
		Hostname    string `json:"hostname"`
		EnrollToken string `json:"enroll_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.ImageName == "" {
		writeError(w, http.StatusBadRequest, "image_name is required")
		return
	}

	result, err := h.mgr.Provision(r.Context(), sandgorgon.ProvisionRequest{
		AssetID:     id,
		ImageName:   req.ImageName,
		Hostname:    req.Hostname,
		EnrollToken: req.EnrollToken,
	}, h.serverAddr)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	recordAudit(r.Context(), h.auditWriter, "sandgorgon.provision", actorFromRequest(r), "rest",
		"provisioned asset "+id+" with image "+req.ImageName+" hostname="+req.Hostname, "")

	writeJSON(w, r, http.StatusOK, result)
}

func (h *SandgorgonHandler) listImages(w http.ResponseWriter, r *http.Request) {
	images, err := h.queries.ListSandgorgonImages(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, r, http.StatusOK, images)
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
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
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
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	recordAudit(r.Context(), h.auditWriter, "sandgorgon.image.register", actorFromRequest(r), "rest",
		"registered sandgorgon image "+req.Name+" ("+req.OSFamily+" "+req.OSVersion+"/"+req.Arch+")", "")

	writeJSON(w, r, http.StatusCreated, image)
}

func (h *SandgorgonHandler) removeImage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.mgr.RemoveImage(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	recordAudit(r.Context(), h.auditWriter, "sandgorgon.image.remove", actorFromRequest(r), "rest",
		"removed sandgorgon image "+id, "")
	writeJSON(w, r, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *SandgorgonHandler) dhcpConfig(w http.ResponseWriter, r *http.Request) {
	dhcpType := r.URL.Query().Get("type")
	if dhcpType == "" {
		dhcpType = "isc"
	}
	config, err := sandgorgon.GenerateDHCPConfig(h.serverAddr, dhcpType)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(config))
}

func (h *SandgorgonHandler) bootScript(w http.ResponseWriter, r *http.Request) {
	assetID := r.PathValue("id")
	asset, err := h.queries.GetSandgorgonAsset(r.Context(), assetID)
	if err != nil {
		writeError(w, http.StatusNotFound, "asset not found")
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
		writeError(w, http.StatusNotFound, "no provisioning in progress for this asset")
		return
	}

	image, err := h.queries.GetSandgorgonImageByName(r.Context(), imageName)
	if err != nil {
		writeError(w, http.StatusNotFound, "image not found: "+imageName)
		return
	}

	script, err := sandgorgon.GenerateIPXEScript(image, h.serverAddr, asset.Hostname, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(script))
}

func (h *SandgorgonHandler) lifecycleReport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	report, err := h.mgr.BuildLifecycleReport(r.Context(), id, h.orgName)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, r, http.StatusOK, report)
}

func (h *SandgorgonHandler) lifecycleReportHTML(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	report, err := h.mgr.BuildLifecycleReport(r.Context(), id, h.orgName)
	if err != nil {
		writeError(w, http.StatusNotFound, "asset not found")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := sandgorgon.RenderLifecycleReportHTML(w, report); err != nil {
		writeError(w, http.StatusInternalServerError, "rendering report: "+err.Error())
	}
}

func (h *SandgorgonHandler) preseedConfig(w http.ResponseWriter, r *http.Request) {
	imageID := r.PathValue("id")
	hostname := r.URL.Query().Get("hostname")
	token := r.URL.Query().Get("token")

	image, err := h.queries.GetSandgorgonImage(r.Context(), imageID)
	if err != nil {
		writeError(w, http.StatusNotFound, "image not found")
		return
	}

	config, err := sandgorgon.GeneratePreseedConfig(image, h.serverAddr, hostname, token)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(config))
}
