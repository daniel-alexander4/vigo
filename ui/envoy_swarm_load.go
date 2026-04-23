// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// EnvoySwarmLoadPageData feeds the per-envoy Swarm Load page. One
// section per subsystem — lode (admin-distributed payloads + the
// cached form of cairn bundles), cairn (materialized bare repos),
// echo (plaintext per-user sync), sigil (encrypted per-user sync).
// Totals roll up across all four so the operator can spot which
// subsystem is filling the disk before drilling into the rows.
type EnvoySwarmLoadPageData struct {
	PageData
	Hostname string
	EnvoyID  string

	Lode  []SwarmLoadRow
	Cairn []SwarmLoadRow
	Echo  []SwarmLoadRow
	Sigil []SwarmLoadRow

	// Totals are int64 (not the uint64 the wire protocols use) so the
	// humanBytes template func — which takes int64 — can render them
	// without a per-section conversion. Disk-footprint values stay
	// within int64 range indefinitely in practice.
	LodeTotal  int64
	CairnTotal int64
	EchoTotal  int64
	SigilTotal int64
	GrandTotal int64
}

// SwarmLoadRow is one row in any of the four per-subsystem tables.
// The fields are intentionally generic — the template renders the
// same columns (Name, Detail, Size, Status, When) for every
// section so size comparisons read at a glance.
type SwarmLoadRow struct {
	// Name is the user-friendly primary identifier (filename,
	// `~user/echo/<name>`, bare-repo path). Never a sha256 or
	// an internal label like `cairn:a:b:c`.
	Name string
	// Detail is a secondary line (seeder, git sha, user, etc.) —
	// the template renders it dim under Name.
	Detail string
	// Size in bytes. 0 is legal for downloading blobs that haven't
	// landed a chunk yet. int64 rather than uint64 so the template's
	// humanBytes helper accepts it without a per-row cast.
	Size int64
	// Human-readable size for direct rendering (template doesn't
	// have to call a helper).
	SizeHuman string
	// Status drives the pill: "complete", "downloading", "source",
	// "revoking", "stale", "live".
	Status string
	// Progress shows "12/40 chunks" for downloading rows, else empty.
	Progress string
	// When is a formatted timestamp (last refresh, mtime, distributed-at).
	When string
	// Link is an optional deep link. Lode rows link to /lode/{sha};
	// cairn rows link to /cairn/{label}. Echo/sigil rows leave this
	// empty — there's no deeper page for one file.
	Link string
}

// EnvoySwarmLoad renders the Swarm Load page for one envoy.
func (h *Handler) EnvoySwarmLoad(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	if _, err := uuid.Parse(idStr); err != nil {
		http.Error(w, "invalid envoy ID", http.StatusBadRequest)
		return
	}

	detail, err := h.dataSource.EnvoyDetailData(idStr, h.baseInterval)
	if err != nil {
		http.Error(w, "envoy not found", http.StatusNotFound)
		return
	}

	data := EnvoySwarmLoadPageData{
		PageData: PageData{Title: "Swarm Load: " + detail.Envoy.Hostname, ActiveNav: "envoys"},
		Hostname: detail.Envoy.Hostname,
		EnvoyID:  idStr,
	}

	// --- Lode (swarm blob cache, admin payloads + cairn bundles) ---
	if h.swarmTracker != nil {
		blobs := h.swarmTracker.BlobsOnEnvoy(idStr)
		for _, b := range blobs {
			size := int64(b.Size)
			row := SwarmLoadRow{
				Name:      userFriendlyBlobName(b.Label, b.SHA256),
				Detail:    blobDetailLine(b.Label, b.SHA256, b.SeederID, h.swarmFleetIndex),
				Size:      size,
				SizeHuman: humanSize(size),
				Status:    b.Status,
				When:      formatRelative(b.DistributedAt),
				Link:      "/lode/" + b.SHA256,
			}
			if b.Status == "downloading" && b.ChunksTotal > 0 {
				row.Progress = fmt.Sprintf("%d/%d chunks", b.ChunksHave, b.ChunksTotal)
			}
			data.Lode = append(data.Lode, row)
			data.LodeTotal += size
		}
	}

	// --- Cairn (materialized bare git repos) ---
	if h.cairnFleet != nil {
		mirrors := h.cairnFleet.EnvoyMirrors(r.Context(), idStr)
		for _, m := range mirrors {
			status := "live"
			if m.Stale {
				status = "stale"
			}
			size := int64(m.SizeBytes)
			data.Cairn = append(data.Cairn, SwarmLoadRow{
				Name:      m.BareRepoPath,
				Detail:    fmt.Sprintf("%s/%s — from %s · git %s", m.User, m.Project, m.Seeder, shortSha(m.GitSha)),
				Size:      size,
				SizeHuman: humanSize(size),
				Status:    status,
				When:      formatRelative(time.Unix(m.LastRefreshed, 0)),
				Link:      "/cairn/" + m.Label,
			})
			data.CairnTotal += size
		}
	}

	// --- Echo (per-user plaintext) ---
	if h.mesh != nil {
		for _, f := range h.mesh.EnvoyEchoFiles(r.Context(), idStr) {
			size := int64(f.Size)
			data.Echo = append(data.Echo, SwarmLoadRow{
				Name:      f.Path,
				Detail:    "user " + f.User,
				Size:      size,
				SizeHuman: humanSize(size),
				Status:    "live",
				When:      formatRelative(time.Unix(f.Mtime, 0)),
			})
			data.EchoTotal += size
		}

		// --- Sigil (per-user ciphertext) ---
		for _, f := range h.mesh.EnvoySigilFiles(r.Context(), idStr) {
			size := int64(f.Size)
			data.Sigil = append(data.Sigil, SwarmLoadRow{
				Name:      f.Path,
				Detail:    "user " + f.User,
				Size:      size,
				SizeHuman: humanSize(size),
				Status:    "live",
				When:      formatRelative(time.Unix(f.Mtime, 0)),
			})
			data.SigilTotal += size
		}
	}

	data.GrandTotal = data.LodeTotal + data.CairnTotal + data.EchoTotal + data.SigilTotal

	// Biggest first within each section — that's what "why is disk
	// full?" wants to see.
	for _, rows := range [][]SwarmLoadRow{data.Lode, data.Cairn, data.Echo, data.Sigil} {
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].Size > rows[j].Size })
	}

	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "envoy-swarm-load", data)
}

// userFriendlyBlobName strips the `cairn:` machine label into a
// readable identifier. Admin-distributed blobs already have a
// filename-shaped label; those pass through unchanged. Unlabeled
// blobs surface as their short sha so an operator has a handle to
// paste into /lode/{sha}.
func userFriendlyBlobName(label, sha string) string {
	if strings.HasPrefix(label, "cairn:") {
		parts := strings.SplitN(strings.TrimPrefix(label, "cairn:"), ":", 3)
		if len(parts) == 3 {
			return fmt.Sprintf("cairn: %s/%s (from %s)", parts[1], parts[2], parts[0])
		}
		return label
	}
	if label != "" {
		return label
	}
	return shortSha(sha)
}

// blobDetailLine is the dim second-line hint under a lode row.
func blobDetailLine(label, sha, seederID string, resolver SwarmHostnameResolver) string {
	seeder := seederID
	if resolver != nil && seederID != "" {
		if name := resolver.ResolveHostname(seederID); name != "" {
			seeder = name
		}
	}
	if seeder == "" {
		seeder = "admin"
	}
	return fmt.Sprintf("%s · seeded by %s", shortSha(sha), seeder)
}

func shortSha(sha string) string {
	if len(sha) >= 12 {
		return sha[:12]
	}
	return sha
}

// formatRelative renders a unix time as "3h ago", "2d ago", etc.
// Keeps the UI tight; the exact timestamp is a tooltip on the
// template side via `title=`.
func formatRelative(t time.Time) string {
	if t.IsZero() || t.Unix() <= 0 {
		return "—"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Format("2006-01-02")
	}
}
