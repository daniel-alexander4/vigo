// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/db"
)

// auditBundleEndpoint registers the audit-bundle export route. The
// route lives outside RegisterRoutes() so it can be wired explicitly
// alongside the other report endpoints — keeps the per-framework
// handler list in RegisterRoutes free of one-off wiring.
func (h *ReportHandler) registerAuditBundleRoute(mux *http.ServeMux) {
	read := auth.RequirePermission(auth.PermAuditRead)
	mux.Handle("GET /api/v1/report/audit-bundle/{slug}", read(http.HandlerFunc(h.handleAuditBundle)))
}

// handleAuditBundle assembles a self-contained audit-ready zip for one
// framework over a configurable period (default 365d). The zip
// contains:
//
//	manifest.json     framework, period, generation timestamp, sha256
//	                  of every other artifact (sha256 over the on-disk
//	                  zip entry contents, not over the post-deflate
//	                  bytes)
//	report.pdf        WeasyPrint-rendered compliance report
//	report.html       same content as text/html (auditor-readable
//	                  fallback if pdf reader unavailable)
//	evidence.json     per-control: scope, status, modules, traits,
//	                  attached documents
//	docs/             decrypted copies of every operator-uploaded
//	                  doc attached to the framework's controls,
//	                  named <doc-id>__<filename>
//	audit-log.csv     audit-chain entries within the period
//	convergence.csv   daily convergence percentages within the period
//
// The handler streams the zip directly to the response — no
// intermediate file. SHA-256 of each entry is captured during write
// via an io.MultiWriter into the hash + the zip-entry writer.
//
// Permission: audit.read (gated above) + compliance.read (no docs
// returned for callers without it). The download itself records an
// audit-chain entry.
func (h *ReportHandler) handleAuditBundle(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	period, err := parseAuditBundlePeriod(r.URL.Query().Get("period"))
	if err != nil {
		http.Error(w, jsonErr("invalid period: "+err.Error()), http.StatusBadRequest)
		return
	}
	now := time.Now().UTC()
	since := now.Add(-period)

	report, err := h.BuildFrameworkReport(r, slug)
	if err != nil {
		http.Error(w, jsonErr("building framework report: "+err.Error()), http.StatusBadRequest)
		return
	}

	// Caller may lack compliance.read — in that case strip Documents
	// + the docs/ directory but keep the rest of the bundle so audit
	// and ops still get value.
	includeDocs := false
	if u := auth.UserFromContext(r.Context()); u != nil && u.HasPermission(auth.PermComplianceRead) {
		includeDocs = true
	}
	if !includeDocs {
		report.Documents = nil
	}

	filename := fmt.Sprintf("vigo-audit-bundle-%s-%s.zip", slug, now.Format("20060102"))
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	// Cannot retroactively change status if a write fails mid-stream;
	// log+continue is the only viable behavior. The zip footer will
	// still close cleanly because zip.Writer.Close finalizes whatever
	// entries it managed to write.

	zw := zip.NewWriter(w)
	defer zw.Close()

	manifest := bundleManifest{
		Framework:   report.FrameworkName,
		Slug:        slug,
		Title:       report.Title,
		PeriodDays:  int(period / (24 * time.Hour)),
		GeneratedAt: now.Format(time.RFC3339),
		GeneratedBy: report.GeneratedBy,
		Version:     report.Version,
	}

	// 1. report.html — always renderable, useful when no PDF reader.
	htmlBuf := &bytes.Buffer{}
	rwHTML := &fakeResponseWriter{Buffer: htmlBuf, Headers: http.Header{}}
	renderComplianceHTML(rwHTML, report)
	if hash, err := writeBundleEntry(zw, "report.html", htmlBuf.Bytes()); err == nil {
		manifest.Artifacts = append(manifest.Artifacts, bundleArtifact{Path: "report.html", SizeBytes: int64(htmlBuf.Len()), SHA256: hash, MimeType: "text/html"})
	}

	// 2. report.pdf — best-effort. WeasyPrint missing → log + skip;
	// auditor still has report.html.
	pdfBytes, pdfErr := RenderHTMLToPDF(r.Context(), htmlBuf.Bytes())
	if pdfErr == nil {
		if hash, err := writeBundleEntry(zw, "report.pdf", pdfBytes); err == nil {
			manifest.Artifacts = append(manifest.Artifacts, bundleArtifact{Path: "report.pdf", SizeBytes: int64(len(pdfBytes)), SHA256: hash, MimeType: "application/pdf"})
		}
	} else if !errors.Is(pdfErr, ErrWeasyPrintUnavailable) {
		manifest.Warnings = append(manifest.Warnings, "report.pdf rendering failed: "+pdfErr.Error())
	} else {
		manifest.Warnings = append(manifest.Warnings, "report.pdf omitted: weasyprint not installed in container")
	}

	// 3. evidence.json — full structured per-control + per-envoy state.
	evidenceJSON, err := json.MarshalIndent(buildEvidenceJSON(report, includeDocs), "", "  ")
	if err == nil {
		if hash, err := writeBundleEntry(zw, "evidence.json", evidenceJSON); err == nil {
			manifest.Artifacts = append(manifest.Artifacts, bundleArtifact{Path: "evidence.json", SizeBytes: int64(len(evidenceJSON)), SHA256: hash, MimeType: "application/json"})
		}
	}

	// 4. docs/ — operator-uploaded artifacts, decrypted. Caller must
	// have compliance.read (already filtered above into Documents).
	if includeDocs && h.docsResolver != nil {
		seen := make(map[string]bool, len(report.Documents))
		for _, d := range report.Documents {
			if seen[d.DocID] {
				continue
			}
			seen[d.DocID] = true
			plaintext, _, derr := h.docsResolver.Get(r.Context(), d.DocID)
			if derr != nil {
				manifest.Warnings = append(manifest.Warnings, fmt.Sprintf("docs/%s skipped: %s", d.DocID, derr.Error()))
				continue
			}
			path := "docs/" + d.DocID + "__" + sanitizeBundlePath(d.Filename)
			if hash, err := writeBundleEntry(zw, path, plaintext); err == nil {
				manifest.Artifacts = append(manifest.Artifacts, bundleArtifact{Path: path, SizeBytes: int64(len(plaintext)), SHA256: hash, MimeType: d.MimeType})
			}
		}
	}

	// 5. audit-log.csv — audit-chain entries since (now - period).
	if entries, err := h.queries.ListAuditEntriesSince(r.Context(), since); err == nil {
		csvBuf := &bytes.Buffer{}
		cw := csv.NewWriter(csvBuf)
		_ = cw.Write([]string{"id", "timestamp", "event_type", "actor", "source", "summary", "payload", "prev_hash", "hash"})
		for _, e := range entries {
			_ = cw.Write([]string{
				strconv.FormatInt(e.ID, 10),
				e.Timestamp.UTC().Format(time.RFC3339),
				e.EventType,
				e.Actor,
				e.Source,
				e.Summary,
				e.Payload,
				e.PrevHash,
				e.Hash,
			})
		}
		cw.Flush()
		if hash, err := writeBundleEntry(zw, "audit-log.csv", csvBuf.Bytes()); err == nil {
			manifest.Artifacts = append(manifest.Artifacts, bundleArtifact{Path: "audit-log.csv", SizeBytes: int64(csvBuf.Len()), SHA256: hash, MimeType: "text/csv"})
		}
	}

	// 6. convergence.csv — daily fleet convergence over the period.
	if rows, err := h.queries.ListConvergenceHistoryRange(r.Context(), db.ListConvergenceHistoryRangeParams{
		FromRecordedAt: since.Format(time.RFC3339),
		ToRecordedAt:   now.Format(time.RFC3339),
	}); err == nil {
		csvBuf := &bytes.Buffer{}
		cw := csv.NewWriter(csvBuf)
		_ = cw.Write([]string{"recorded_at", "total", "converged", "relapsed", "diverged", "failed", "offline", "no_data"})
		for _, row := range rows {
			_ = cw.Write([]string{
				row.RecordedAt,
				strconv.FormatInt(row.Total, 10),
				strconv.FormatInt(row.Converged, 10),
				strconv.FormatInt(row.Relapsed, 10),
				strconv.FormatInt(row.Diverged, 10),
				strconv.FormatInt(row.Failed, 10),
				strconv.FormatInt(row.Offline, 10),
				strconv.FormatInt(row.NoData, 10),
			})
		}
		cw.Flush()
		if hash, err := writeBundleEntry(zw, "convergence.csv", csvBuf.Bytes()); err == nil {
			manifest.Artifacts = append(manifest.Artifacts, bundleArtifact{Path: "convergence.csv", SizeBytes: int64(csvBuf.Len()), SHA256: hash, MimeType: "text/csv"})
		}
	}

	// 7. manifest.json — last so it can list every other artifact.
	manifestJSON, _ := json.MarshalIndent(manifest, "", "  ")
	_, _ = writeBundleEntry(zw, "manifest.json", manifestJSON)

	// Audit-chain entry on download (regardless of artifact-write
	// errors — the operator did download a bundle).
	recordAudit(r.Context(), h.auditWriter(), "compliance.bundle.download", actorFromRequest(r), "report",
		fmt.Sprintf("downloaded audit bundle for %s (period=%dd)", slug, manifest.PeriodDays),
		fmt.Sprintf(`{"slug":%q,"period_days":%d,"artifact_count":%d,"include_docs":%t}`, slug, manifest.PeriodDays, len(manifest.Artifacts), includeDocs))
}

// auditWriter returns the report handler's audit writer. Indirection
// avoids exposing an unexported field as public API.
func (h *ReportHandler) auditWriter() auditRecorder { return h.auditWriterRef }

// SetAuditWriter wires the audit-chain recorder for download-event
// logging. Optional — recordAudit handles nil safely.
func (h *ReportHandler) SetAuditWriter(w auditRecorder) { h.auditWriterRef = w }

// bundleManifest is the manifest.json shape inside the zip.
type bundleManifest struct {
	Framework   string           `json:"framework"`
	Slug        string           `json:"slug"`
	Title       string           `json:"title"`
	PeriodDays  int              `json:"period_days"`
	GeneratedAt string           `json:"generated_at"`
	GeneratedBy string           `json:"generated_by"`
	Version     string           `json:"version"`
	Artifacts   []bundleArtifact `json:"artifacts"`
	Warnings    []string         `json:"warnings,omitempty"`
}

// bundleArtifact catalogs one zip entry with its plaintext SHA-256.
// Verification: extract the zip, sha256sum each path, compare to this
// list. Mismatches indicate tampering or a corrupted bundle.
type bundleArtifact struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
	MimeType  string `json:"mime_type"`
}

// writeBundleEntry creates a zip entry at name, writes data through a
// SHA-256 capture, and returns the hex digest of the contents. The
// hash matches what `sha256sum <name>` reports after extraction —
// auditors can verify integrity with shell tools.
func writeBundleEntry(zw *zip.Writer, name string, data []byte) (string, error) {
	w, err := zw.Create(name)
	if err != nil {
		return "", err
	}
	hasher := sha256.New()
	mw := io.MultiWriter(w, hasher)
	if _, err := mw.Write(data); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// buildEvidenceJSON produces a structured per-control evidence file.
// Auditors typically want this alongside the PDF — programmatic
// queries against status, scope, modules, and attached docs without
// parsing layout-y HTML.
func buildEvidenceJSON(r *ComplianceReport, includeDocs bool) map[string]any {
	type controlEvidence struct {
		ControlID string                `json:"control_id"`
		Title     string                `json:"title"`
		Scope     string                `json:"scope"`
		Status    string                `json:"status"`
		Evidence  string                `json:"evidence,omitempty"`
		Documents []ReportDocAttachment `json:"documents,omitempty"`
	}
	docsByControl := make(map[string][]ReportDocAttachment)
	if includeDocs {
		for _, d := range r.Documents {
			docsByControl[d.ControlID] = append(docsByControl[d.ControlID], d)
		}
	}
	controls := make([]controlEvidence, 0, len(r.Controls))
	for _, c := range r.Controls {
		ev := controlEvidence{
			ControlID: c.ControlID,
			Title:     c.Title,
			Scope:     c.Scope,
			Status:    c.Status,
			Evidence:  c.Evidence,
		}
		if docs, ok := docsByControl[c.ControlID]; ok {
			ev.Documents = docs
		}
		controls = append(controls, ev)
	}
	return map[string]any{
		"framework":    r.FrameworkName,
		"slug":         r.Slug,
		"title":        r.Title,
		"generated_at": r.GeneratedAt,
		"version":      r.Version,
		"summary":      r.Summary,
		"audit_chain":  r.AuditChain,
		"controls":     controls,
		"fleet":        r.Fleet,
	}
}

// parseAuditBundlePeriod accepts integer days, with `d` / `mo` /
// `y` suffixes for convenience. Empty defaults to 365d. Negative or
// zero rejected.
//
//	"365d"  → 365 days
//	"30d"   → 30 days
//	"12mo"  → 12 × 30 days
//	"3y"    → 3 × 365 days
//	"180"   → 180 days (no suffix = days)
func parseAuditBundlePeriod(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 365 * 24 * time.Hour, nil
	}
	re := regexp.MustCompile(`^(\d+)(d|mo|y)?$`)
	m := re.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("expected format like 365d, 12mo, 3y, or bare integer days; got %q", s)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("period must be positive integer; got %q", s)
	}
	unit := m[2]
	switch unit {
	case "", "d":
		return time.Duration(n) * 24 * time.Hour, nil
	case "mo":
		return time.Duration(n) * 30 * 24 * time.Hour, nil
	case "y":
		return time.Duration(n) * 365 * 24 * time.Hour, nil
	}
	return 0, fmt.Errorf("unrecognized period unit %q", unit)
}

// sanitizeBundlePath strips path separators and parent traversals
// from a user-supplied filename so docs/<id>__<sanitized> always lands
// in the docs/ directory, never elsewhere in the zip.
func sanitizeBundlePath(name string) string {
	cleaned := filepath.Base(name)
	cleaned = strings.ReplaceAll(cleaned, "/", "_")
	cleaned = strings.ReplaceAll(cleaned, "\\", "_")
	if cleaned == "" || cleaned == "." || cleaned == ".." {
		return "unnamed"
	}
	return cleaned
}

// fakeResponseWriter satisfies http.ResponseWriter for in-process HTML
// rendering (renderComplianceHTML writes to a ResponseWriter). The
// audit-bundle handler uses it to capture the rendered HTML into a
// buffer for both the report.html artifact and as input to
// RenderHTMLToPDF.
type fakeResponseWriter struct {
	Buffer  *bytes.Buffer
	Headers http.Header
	Status  int
}

func (f *fakeResponseWriter) Header() http.Header { return f.Headers }
func (f *fakeResponseWriter) Write(p []byte) (int, error) {
	return f.Buffer.Write(p)
}
func (f *fakeResponseWriter) WriteHeader(s int) { f.Status = s }

// _ keeps context.Context imported for stable signatures even when
// a future refactor removes a direct ctx use.
var _ = context.Background
