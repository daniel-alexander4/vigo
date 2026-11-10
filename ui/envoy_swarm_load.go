// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

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
// section per subsystem — filecast (admin-distributed files + the
// cached form of gitback bundles), gitback (materialized bare repos),
// longdrawer (plaintext per-user sync), lockbox (encrypted per-user sync).
// Totals roll up across all four so the operator can spot which
// subsystem is filling the disk before drilling into the rows.
type EnvoySwarmLoadPageData struct {
	PageData
	Hostname string
	EnvoyID  string

	Filecast []SwarmLoadRow
	Gitback []SwarmLoadRow
	Longdrawer  []SwarmLoadRow
	Lockbox []SwarmLoadRow

	// Totals are int64 (not the uint64 the wire protocols use) so the
	// humanBytes template func — which takes int64 — can render them
	// without a per-section conversion. Disk-footprint values stay
	// within int64 range indefinitely in practice.
	FilecastTotal  int64
	GitbackTotal int64
	LongdrawerTotal  int64
	LockboxTotal int64
	GrandTotal int64
}

// SwarmLoadRow is one row in any of the four per-subsystem tables.
// The fields are intentionally generic — the template renders the
// same columns (Name, Detail, Size, Status, When) for every
// section so size comparisons read at a glance.
type SwarmLoadRow struct {
	// Name is the user-friendly primary identifier (filename,
	// `~user/longdrawer/<name>`, bare-repo path). Never a sha256 or
	// an internal label like `gitback:a:b:c`.
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
	// Link is an optional deep link. Payload rows link to /payload/{sha};
	// gitback rows link to /gitback/{label}. Longdrawer/lockbox rows leave this
	// empty — there's no deeper page for one file.
	Link string
}

// EnvoySwarmLoadFragment renders the Swarm Load tab body for one
// envoy, swapped in under the envoy detail page's `#envoy-content`
// by the same HTMX pattern as Drift / Runs / Traits / Config / etc.
// Returns the bare fragment — the outer page chrome (hostname H1,
// tab bar) comes from envoy-detail.html.
func (h *Handler) EnvoySwarmLoadFragment(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	if _, err := uuid.Parse(idStr); err != nil {
		http.Error(w, "invalid envoy ID", http.StatusBadRequest)
		return
	}

	detail, err := h.dataSource.EnvoyDetailData(idStr, h.baseInterval, h.offlineThreshold)
	if err != nil {
		http.Error(w, "envoy not found", http.StatusNotFound)
		return
	}

	data := EnvoySwarmLoadPageData{
		PageData: PageData{Title: "Swarm Load: " + detail.Envoy.Hostname, ActiveNav: "envoys"},
		Hostname: detail.Envoy.Hostname,
		EnvoyID:  idStr,
	}

	// --- Payload (swarm blob cache, admin filecasts + gitback bundles) ---
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
				Link:      "/swarm/filecast/" + b.SHA256,
			}
			if b.Status == "downloading" && b.ChunksTotal > 0 {
				row.Progress = fmt.Sprintf("%d/%d chunks", b.ChunksHave, b.ChunksTotal)
			}
			data.Filecast = append(data.Filecast, row)
			data.FilecastTotal += size
		}
	}

	// --- Gitback (materialized bare git repos) ---
	if h.gitbackFleet != nil {
		projects := h.gitbackFleet.EnvoyProjects(r.Context(), idStr)
		for _, p := range projects {
			if !p.HasBareRepo {
				continue
			}
			size := int64(p.SizeBytes)
			// Surface the head sha of refs/heads/main when present;
			// otherwise pick any deterministic ref. Empty when the
			// repo has been initialized but holds no refs yet.
			headSha := ""
			if sha, ok := p.HeadRefs["refs/heads/main"]; ok {
				headSha = sha
			} else if len(p.HeadRefs) > 0 {
				// Stable pick: alphabetical first ref name.
				keys := make([]string, 0, len(p.HeadRefs))
				for k := range p.HeadRefs {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				headSha = p.HeadRefs[keys[0]]
			}
			data.Gitback = append(data.Gitback, SwarmLoadRow{
				Name:      p.BareRepoPath,
				Detail:    fmt.Sprintf("%s · %s · git %s", p.Handle, p.DrScope, shortSha(headSha)),
				Size:      size,
				SizeHuman: humanSize(size),
				Status:    "live",
				When:      "",
				Link:      "/swarm/gitback/" + p.ProjectID,
			})
			data.GitbackTotal += size
		}
	}

	// --- Longdrawer (per-user plaintext) ---
	if h.mesh != nil {
		for _, f := range h.mesh.EnvoyLongdrawerFiles(r.Context(), idStr) {
			size := int64(f.Size)
			data.Longdrawer = append(data.Longdrawer, SwarmLoadRow{
				Name:      f.Path,
				Detail:    "user " + f.User,
				Size:      size,
				SizeHuman: humanSize(size),
				Status:    "live",
				When:      formatRelative(time.Unix(f.Mtime, 0)),
			})
			data.LongdrawerTotal += size
		}

		// --- Lockbox (per-user ciphertext) ---
		for _, f := range h.mesh.EnvoyLockboxFiles(r.Context(), idStr) {
			size := int64(f.Size)
			data.Lockbox = append(data.Lockbox, SwarmLoadRow{
				Name:      f.Path,
				Detail:    "user " + f.User,
				Size:      size,
				SizeHuman: humanSize(size),
				Status:    "live",
				When:      formatRelative(time.Unix(f.Mtime, 0)),
			})
			data.LockboxTotal += size
		}
	}

	data.GrandTotal = data.FilecastTotal + data.GitbackTotal + data.LongdrawerTotal + data.LockboxTotal

	// Biggest first within each section — that's what "why is disk
	// full?" wants to see.
	for _, rows := range [][]SwarmLoadRow{data.Filecast, data.Gitback, data.Longdrawer, data.Lockbox} {
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].Size > rows[j].Size })
	}

	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.RenderFragment(w, "envoy-detail", "envoy-swarm-load-fragment", data)
}

// userFriendlyBlobName strips the `gitback:` machine label into a
// readable identifier. Admin-distributed blobs already have a
// filename-shaped label; those pass through unchanged. Unlabeled
// blobs surface as their short sha so an operator has a handle to
// paste into /payload/{sha}.
func userFriendlyBlobName(label, sha string) string {
	if strings.HasPrefix(label, "gitback:") {
		parts := strings.SplitN(strings.TrimPrefix(label, "gitback:"), ":", 3)
		if len(parts) == 3 {
			return fmt.Sprintf("gitback: %s/%s (from %s)", parts[1], parts[2], parts[0])
		}
		return label
	}
	if label != "" {
		return label
	}
	return shortSha(sha)
}

// blobDetailLine is the dim second-line hint under a payload row.
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
