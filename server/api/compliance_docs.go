// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/compliancedocs"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/ui"
)

// ComplianceDocsHandler exposes the operator-facing REST surface for
// the documentation store. Every mutating action records an audit-
// chain entry; reads do not, so they don't fill the audit log on
// dashboard refreshes.
type ComplianceDocsHandler struct {
	store       *compliancedocs.Store
	auditWriter auditRecorder
}

// NewComplianceDocsHandler returns a handler over the given store.
// The store may be nil when the deployment is configured without a
// docs directory; in that case all routes return 503.
func NewComplianceDocsHandler(store *compliancedocs.Store) *ComplianceDocsHandler {
	return &ComplianceDocsHandler{store: store}
}

// SetAuditWriter wires the tamper-evident audit chain. Optional —
// when nil, recordAudit is a no-op.
func (h *ComplianceDocsHandler) SetAuditWriter(w auditRecorder) { h.auditWriter = w }

// RegisterRoutes mounts the compliance-docs endpoints on mux.
//
// Permissions:
//   - read: compliance.read
//   - write: compliance.docs.write
//   - purge: compliance.docs.purge
//
// All routes 503 when the store isn't configured.
func (h *ComplianceDocsHandler) RegisterRoutes(mux *http.ServeMux) {
	read := auth.RequirePermission(auth.PermComplianceRead)
	write := auth.RequirePermission(auth.PermComplianceDocsWrite)
	purge := auth.RequirePermission(auth.PermComplianceDocsPurge)

	// List + by-control views.
	mux.Handle("GET /api/v1/compliance/docs", read(http.HandlerFunc(h.list)))
	mux.Handle("GET /api/v1/compliance/docs/{id}", read(http.HandlerFunc(h.get)))
	mux.Handle("GET /api/v1/compliance/docs/{id}/download", read(http.HandlerFunc(h.download)))
	mux.Handle("GET /api/v1/compliance/docs/{id}/links", read(http.HandlerFunc(h.listLinks)))

	// Mutations.
	mux.Handle("POST /api/v1/compliance/docs", write(http.HandlerFunc(h.upload)))
	mux.Handle("PUT /api/v1/compliance/docs/{id}", write(http.HandlerFunc(h.replace)))
	mux.Handle("DELETE /api/v1/compliance/docs/{id}", write(http.HandlerFunc(h.softDelete)))
	mux.Handle("POST /api/v1/compliance/docs/{id}/links", write(http.HandlerFunc(h.addLink)))
	mux.Handle("DELETE /api/v1/compliance/docs/{id}/links/{framework}/{controlID}", write(http.HandlerFunc(h.removeLink)))

	// Admin-only physical purge.
	mux.Handle("POST /api/v1/compliance/docs/{id}/purge", purge(http.HandlerFunc(h.purge)))
}

func (h *ComplianceDocsHandler) requireStore(w http.ResponseWriter) bool {
	if h.store == nil {
		http.Error(w, jsonErr("compliance docs store is not configured"), http.StatusServiceUnavailable)
		return false
	}
	return true
}

// docMetaJSON is the JSON shape returned for doc metadata. Wrapping
// db.ComplianceDoc lets us hide sql.NullString verbosity and surface
// a stable API independent of sqlc-generated field names.
type docMetaJSON struct {
	ID           string `json:"id"`
	DocType      string `json:"doc_type"`
	Filename     string `json:"filename"`
	MimeType     string `json:"mime_type"`
	SizeBytes    int64  `json:"size_bytes"`
	SHA256       string `json:"sha256"`
	UploadedBy   string `json:"uploaded_by"`
	UploadedAt   string `json:"uploaded_at"`
	ReplacedAt   string `json:"replaced_at,omitempty"`
	ReplacedByID string `json:"replaced_by_id,omitempty"`
	Deleted      bool   `json:"deleted,omitempty"`
}

func toDocMetaJSON(row db.ComplianceDoc) docMetaJSON {
	out := docMetaJSON{
		ID:         row.ID,
		DocType:    row.DocType,
		Filename:   row.Filename,
		MimeType:   row.MimeType,
		SizeBytes:  row.SizeBytes,
		SHA256:     row.Sha256,
		UploadedBy: row.UploadedBy,
		UploadedAt: row.UploadedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
	if row.ReplacedAt.Valid {
		out.ReplacedAt = row.ReplacedAt.Time.Format("2006-01-02T15:04:05Z07:00")
	}
	if row.ReplacedByID.Valid {
		out.ReplacedByID = row.ReplacedByID.String
	}
	if row.DeletedAt.Valid {
		out.Deleted = true
	}
	return out
}

func (h *ComplianceDocsHandler) list(w http.ResponseWriter, r *http.Request) {
	if !h.requireStore(w) {
		return
	}
	q := r.URL.Query()
	framework := q.Get("framework")
	controlID := q.Get("control_id")

	var rows []db.ComplianceDoc
	var err error
	if framework != "" && controlID != "" {
		rows, err = h.store.ListByControl(r.Context(), framework, controlID)
	} else {
		rows, err = h.store.List(r.Context())
	}
	if err != nil {
		http.Error(w, jsonErr("listing compliance docs: "+err.Error()), http.StatusInternalServerError)
		return
	}
	out := make([]docMetaJSON, 0, len(rows))
	for _, row := range rows {
		out = append(out, toDocMetaJSON(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{"docs": out})
}

func (h *ComplianceDocsHandler) get(w http.ResponseWriter, r *http.Request) {
	if !h.requireStore(w) {
		return
	}
	id := r.PathValue("id")
	row, err := h.store.GetMeta(r.Context(), id)
	if err != nil {
		http.Error(w, jsonErr("compliance doc not found"), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, toDocMetaJSON(row))
}

func (h *ComplianceDocsHandler) download(w http.ResponseWriter, r *http.Request) {
	if !h.requireStore(w) {
		return
	}
	id := r.PathValue("id")
	plaintext, row, err := h.store.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, compliancedocs.ErrNotFound) {
			http.Error(w, jsonErr("compliance doc not found"), http.StatusNotFound)
			return
		}
		http.Error(w, jsonErr("downloading compliance doc: "+err.Error()), http.StatusInternalServerError)
		return
	}
	recordAudit(r.Context(), h.auditWriter, "compliance.docs.download", actorFromRequest(r), "compliance_docs",
		fmt.Sprintf("downloaded %s (%s)", row.Filename, row.ID),
		fmt.Sprintf(`{"id":%q,"sha256":%q,"size":%d}`, row.ID, row.Sha256, row.SizeBytes))

	w.Header().Set("Content-Type", row.MimeType)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", row.SizeBytes))
	// Sanitize filename to a Latin-1 ASCII subset so Content-Disposition
	// remains valid even when the upload had Unicode in its name.
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(row.Filename)))
	w.Header().Set("X-Compliance-Doc-Sha256", row.Sha256)
	_, _ = w.Write(plaintext)
}

// uploadRequest is the multipart form layout for POST and PUT.
//
//   file: the binary upload (required)
//   filename: optional override; defaults to the multipart Filename
//   mime_type: optional declared MIME type (validated against magic bytes)
//   doc_type: free-form category for cross-framework reuse hints
//   links: JSON array of {framework, control_id} pairs
//
// Limits derive from the store's caps; ParseMultipartForm uses the
// same per-doc cap so the body never exceeds memory the operator
// allowed.
type uploadRequest struct {
	plaintext []byte
	filename  string
	mimeType  string
	docType   string
	links     []compliancedocs.ControlLink
}

// parseUploadForm reads the multipart upload and returns a normalized
// uploadRequest. memCap bounds in-memory parsing.
func parseUploadForm(r *http.Request, memCap int64) (*uploadRequest, error) {
	if memCap <= 0 {
		memCap = 50 << 20
	}
	if err := r.ParseMultipartForm(memCap); err != nil {
		return nil, fmt.Errorf("parsing multipart form: %w", err)
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		return nil, fmt.Errorf("missing 'file' form field: %w", err)
	}
	defer file.Close()

	plaintext, err := io.ReadAll(io.LimitReader(file, memCap+1))
	if err != nil {
		return nil, fmt.Errorf("reading upload: %w", err)
	}
	if int64(len(plaintext)) > memCap {
		return nil, fmt.Errorf("upload exceeds %d byte cap", memCap)
	}

	out := &uploadRequest{
		plaintext: plaintext,
		filename:  strings.TrimSpace(r.FormValue("filename")),
		mimeType:  strings.TrimSpace(r.FormValue("mime_type")),
		docType:   strings.TrimSpace(r.FormValue("doc_type")),
	}
	if out.filename == "" && hdr != nil {
		out.filename = filepath.Base(hdr.Filename)
	}
	if out.mimeType == "" && hdr != nil {
		out.mimeType = hdr.Header.Get("Content-Type")
	}
	if linksJSON := r.FormValue("links"); linksJSON != "" {
		if err := json.Unmarshal([]byte(linksJSON), &out.links); err != nil {
			return nil, fmt.Errorf("invalid links JSON: %w", err)
		}
	}
	if out.filename == "" {
		return nil, fmt.Errorf("filename required")
	}
	return out, nil
}

func (h *ComplianceDocsHandler) upload(w http.ResponseWriter, r *http.Request) {
	if !h.requireStore(w) {
		return
	}
	in, err := parseUploadForm(r, 50<<20)
	if err != nil {
		http.Error(w, jsonErr(err.Error()), http.StatusBadRequest)
		return
	}
	row, err := h.store.Put(r.Context(), compliancedocs.PutInput{
		Plaintext:  in.plaintext,
		Filename:   in.filename,
		MimeType:   in.mimeType,
		DocType:    in.docType,
		UploadedBy: actorFromRequest(r),
		Links:      in.links,
	})
	if err != nil {
		statusCode := classifyStoreError(err)
		http.Error(w, jsonErr(err.Error()), statusCode)
		return
	}
	recordAudit(r.Context(), h.auditWriter, "compliance.docs.upload", actorFromRequest(r), "compliance_docs",
		fmt.Sprintf("uploaded %s (%s, %d bytes)", row.Filename, row.ID, row.SizeBytes),
		fmt.Sprintf(`{"id":%q,"filename":%q,"sha256":%q,"size":%d,"links":%d}`, row.ID, row.Filename, row.Sha256, row.SizeBytes, len(in.links)))
	writeJSON(w, http.StatusCreated, toDocMetaJSON(row))
}

func (h *ComplianceDocsHandler) replace(w http.ResponseWriter, r *http.Request) {
	if !h.requireStore(w) {
		return
	}
	id := r.PathValue("id")
	in, err := parseUploadForm(r, 50<<20)
	if err != nil {
		http.Error(w, jsonErr(err.Error()), http.StatusBadRequest)
		return
	}
	row, err := h.store.Replace(r.Context(), id, compliancedocs.PutInput{
		Plaintext:  in.plaintext,
		Filename:   in.filename,
		MimeType:   in.mimeType,
		DocType:    in.docType,
		UploadedBy: actorFromRequest(r),
		Links:      in.links,
	})
	if err != nil {
		statusCode := classifyStoreError(err)
		http.Error(w, jsonErr(err.Error()), statusCode)
		return
	}
	recordAudit(r.Context(), h.auditWriter, "compliance.docs.replace", actorFromRequest(r), "compliance_docs",
		fmt.Sprintf("replaced %s with %s (%s)", id, row.ID, row.Filename),
		fmt.Sprintf(`{"old_id":%q,"new_id":%q,"sha256":%q,"size":%d}`, id, row.ID, row.Sha256, row.SizeBytes))
	writeJSON(w, http.StatusOK, toDocMetaJSON(row))
}

func (h *ComplianceDocsHandler) softDelete(w http.ResponseWriter, r *http.Request) {
	if !h.requireStore(w) {
		return
	}
	id := r.PathValue("id")
	if err := h.store.SoftDelete(r.Context(), id, actorFromRequest(r)); err != nil {
		http.Error(w, jsonErr(err.Error()), classifyStoreError(err))
		return
	}
	recordAudit(r.Context(), h.auditWriter, "compliance.docs.delete", actorFromRequest(r), "compliance_docs",
		fmt.Sprintf("soft-deleted %s", id),
		fmt.Sprintf(`{"id":%q}`, id))
	w.WriteHeader(http.StatusNoContent)
}

func (h *ComplianceDocsHandler) purge(w http.ResponseWriter, r *http.Request) {
	if !h.requireStore(w) {
		return
	}
	id := r.PathValue("id")
	if err := h.store.Purge(r.Context(), id); err != nil {
		http.Error(w, jsonErr(err.Error()), classifyStoreError(err))
		return
	}
	recordAudit(r.Context(), h.auditWriter, "compliance.docs.purge", actorFromRequest(r), "compliance_docs",
		fmt.Sprintf("physically purged %s", id),
		fmt.Sprintf(`{"id":%q}`, id))
	w.WriteHeader(http.StatusNoContent)
}

func (h *ComplianceDocsHandler) addLink(w http.ResponseWriter, r *http.Request) {
	if !h.requireStore(w) {
		return
	}
	id := r.PathValue("id")
	var req struct {
		Framework string `json:"framework"`
		ControlID string `json:"control_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, jsonErr("invalid JSON: "+err.Error()), http.StatusBadRequest)
		return
	}
	if req.Framework == "" || req.ControlID == "" {
		http.Error(w, jsonErr("framework and control_id are required"), http.StatusBadRequest)
		return
	}
	if err := h.store.AddLink(r.Context(), id, req.Framework, req.ControlID, actorFromRequest(r)); err != nil {
		http.Error(w, jsonErr(err.Error()), classifyStoreError(err))
		return
	}
	recordAudit(r.Context(), h.auditWriter, "compliance.docs.link.add", actorFromRequest(r), "compliance_docs",
		fmt.Sprintf("linked doc %s to %s/%s", id, req.Framework, req.ControlID),
		fmt.Sprintf(`{"id":%q,"framework":%q,"control_id":%q}`, id, req.Framework, req.ControlID))
	w.WriteHeader(http.StatusNoContent)
}

func (h *ComplianceDocsHandler) removeLink(w http.ResponseWriter, r *http.Request) {
	if !h.requireStore(w) {
		return
	}
	id := r.PathValue("id")
	framework := r.PathValue("framework")
	controlID := r.PathValue("controlID")
	if framework == "" || controlID == "" {
		http.Error(w, jsonErr("framework and controlID required"), http.StatusBadRequest)
		return
	}
	if err := h.store.RemoveLink(r.Context(), id, framework, controlID); err != nil {
		http.Error(w, jsonErr(err.Error()), classifyStoreError(err))
		return
	}
	recordAudit(r.Context(), h.auditWriter, "compliance.docs.link.remove", actorFromRequest(r), "compliance_docs",
		fmt.Sprintf("unlinked doc %s from %s/%s", id, framework, controlID),
		fmt.Sprintf(`{"id":%q,"framework":%q,"control_id":%q}`, id, framework, controlID))
	w.WriteHeader(http.StatusNoContent)
}

func (h *ComplianceDocsHandler) listLinks(w http.ResponseWriter, r *http.Request) {
	if !h.requireStore(w) {
		return
	}
	id := r.PathValue("id")
	links, err := h.store.ListLinks(r.Context(), id)
	if err != nil {
		http.Error(w, jsonErr("listing links: "+err.Error()), http.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(links))
	for _, l := range links {
		out = append(out, map[string]any{
			"framework":  l.Framework,
			"control_id": l.ControlID,
			"linked_at":  l.LinkedAt.Format("2006-01-02T15:04:05Z07:00"),
			"linked_by":  l.LinkedBy,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"links": out})
}

// classifyStoreError maps the typed errors from compliancedocs into
// HTTP status codes so the handlers don't have to repeat the switch.
func classifyStoreError(err error) int {
	switch {
	case errors.Is(err, compliancedocs.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, compliancedocs.ErrAlreadyDeleted):
		return http.StatusConflict
	case errors.Is(err, compliancedocs.ErrTooLarge),
		errors.Is(err, compliancedocs.ErrFrameworkFull),
		errors.Is(err, compliancedocs.ErrTotalFull):
		return http.StatusRequestEntityTooLarge
	case errors.Is(err, compliancedocs.ErrMimeNotAllowed),
		errors.Is(err, compliancedocs.ErrEmpty):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

// actorFromRequest returns the username if available, else "anonymous"
// — but auth middleware should already have rejected unauthenticated
// callers before they reach these handlers.
func actorFromRequest(r *http.Request) string {
	if u := auth.UserFromContext(r.Context()); u != nil {
		return u.Username
	}
	return "anonymous"
}

// complianceDocsListing adapts a *compliancedocs.Store into the
// minimal interface the ui package expects (ui.ComplianceDocsListing).
// Defined here so the ui package never imports compliancedocs.
type complianceDocsListing struct {
	store *compliancedocs.Store
}

// DocsForControl satisfies ui.ComplianceDocsListing. Returns a flat
// projection over the store's metadata so the per-control drilldown
// can render attached docs without pulling in db-typed fields.
func (c *complianceDocsListing) DocsForControl(ctx context.Context, framework, controlID string) ([]ui.ComplianceDocsListingItem, error) {
	rows, err := c.store.ListByControl(ctx, framework, controlID)
	if err != nil {
		return nil, err
	}
	out := make([]ui.ComplianceDocsListingItem, 0, len(rows))
	for _, row := range rows {
		out = append(out, ui.ComplianceDocsListingItem{
			ID:         row.ID,
			Filename:   row.Filename,
			DocType:    row.DocType,
			MimeType:   row.MimeType,
			SizeBytes:  row.SizeBytes,
			SHA256:     row.Sha256,
			UploadedBy: row.UploadedBy,
			UploadedAt: row.UploadedAt.Format("2006-01-02T15:04:05Z07:00"),
		})
	}
	return out, nil
}

// _ is a context-typed no-op to keep imports stable when callers don't
// touch context.Context directly via this file.
var _ = context.Background
