// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

// Package filemeshcore holds the file-mesh status machinery shared by
// longdrawermesh and lockboxmesh — the two swarm meshes that project a
// per-user file/tombstone view across the fleet. The file-cell index,
// tombstone merge, and (most importantly) the per-file status
// derivation switch — live / divergent / partial / tombstoned — were
// byte-identical in both packages; a fix to the switch had to be
// applied twice or the two views silently drifted. This is the single
// source for that logic. Each mesh keeps its own Fleet / UserSummary /
// EnvoyPresence shapes (lockbox adds recipient-drift + failed-file
// fields) and threads these helpers.
package filemeshcore

import "sort"

// EnvoyInput decouples the meshes from server/fleet so both the UI and
// REST handler can feed them without an import cycle.
type EnvoyInput struct {
	ID       string
	Hostname string
	Revoked  bool
}

// FileRow joins one filename across every envoy that knows about it.
// Status is per-(user,filename): "live" if every reporting envoy has it
// with a matching sha, "divergent" if sha disagrees, "partial" if some
// envoys have it and some don't, "tombstoned" if every reporting envoy
// holds a tombstone for it.
type FileRow struct {
	Name     string             `json:"name"`
	Status   string             `json:"status"` // live | divergent | partial | tombstoned
	PerEnvoy []FileCellPerEnvoy `json:"per_envoy"`
}

type FileCellPerEnvoy struct {
	Hostname string `json:"hostname"`
	State    string `json:"state"` // live | tombstoned | missing
	Sha256   string `json:"sha256,omitempty"`
	Size     uint64 `json:"size,omitempty"`
	Mtime    int64  `json:"mtime,omitempty"`
}

// TombstoneRow summarizes a deletion propagation across the mesh.
type TombstoneRow struct {
	Name         string   `json:"name"`
	DeletionTime int64    `json:"deletion_time"`
	KnownOn      []string `json:"known_on"` // envoys that hold this tombstone
}

// Index accumulates one user's file cells and tombstones across envoys.
// Lookups by name go through maps, so building a summary is linear in the
// number of reported entries rather than quadratic in the file count.
// The zero value is ready to use.
type Index struct {
	Files      []FileRow
	Tombstones []TombstoneRow
	fileIdx    map[string]int
	tombIdx    map[string]int
}

// AddFileCell records one envoy's state for a filename, coalescing into
// the existing FileRow when the name is already present.
func (x *Index) AddFileCell(hostname, name, sha string, size uint64, mtime int64, state string) {
	cell := FileCellPerEnvoy{Hostname: hostname, State: state, Sha256: sha, Size: size, Mtime: mtime}
	if x.fileIdx == nil {
		x.fileIdx = make(map[string]int)
	}
	if i, ok := x.fileIdx[name]; ok {
		x.Files[i].PerEnvoy = append(x.Files[i].PerEnvoy, cell)
		return
	}
	x.fileIdx[name] = len(x.Files)
	x.Files = append(x.Files, FileRow{Name: name, PerEnvoy: []FileCellPerEnvoy{cell}})
}

// MergeTombstone folds one envoy's tombstone into the row set, tracking
// which envoys hold it and keeping the latest deletion time.
func (x *Index) MergeTombstone(name string, deletionTime int64, hostname string) {
	if x.tombIdx == nil {
		x.tombIdx = make(map[string]int)
	}
	if i, ok := x.tombIdx[name]; ok {
		r := &x.Tombstones[i]
		r.KnownOn = append(r.KnownOn, hostname)
		if deletionTime > r.DeletionTime {
			r.DeletionTime = deletionTime
		}
		return
	}
	x.tombIdx[name] = len(x.Tombstones)
	x.Tombstones = append(x.Tombstones, TombstoneRow{Name: name, DeletionTime: deletionTime, KnownOn: []string{hostname}})
}

// FinalizeTombstones sorts the row set and each row's KnownOn list so
// the UI renders a stable order across reloads.
func FinalizeTombstones(tombstones []TombstoneRow) {
	for i := range tombstones {
		sort.Strings(tombstones[i].KnownOn)
	}
	sort.Slice(tombstones, func(i, j int) bool {
		return tombstones[i].Name < tombstones[j].Name
	})
}

// FinalizeFiles fills missing cells for every reporting envoy, derives
// each file's status, and sorts. It mutates files in place and returns
// the derived aggregates (unique live-file count, total bytes over the
// largest live copy of each file, and the names of divergent/partial
// files) for the caller to assign onto its own summary — the meshes'
// UserSummary shapes differ, so the counts come back rather than being
// written through. reportingHosts is the set of hostnames whose envoy
// status is "reporting"; a file absent on one of them becomes a
// "missing" cell.
func FinalizeFiles(files []FileRow, reportingHosts []string) (fileCount int, totalBytes int64, divergences []string) {
	for i := range files {
		f := &files[i]
		seen := make(map[string]struct{}, len(f.PerEnvoy))
		for _, c := range f.PerEnvoy {
			seen[c.Hostname] = struct{}{}
		}
		for _, host := range reportingHosts {
			if _, ok := seen[host]; !ok {
				f.PerEnvoy = append(f.PerEnvoy, FileCellPerEnvoy{
					Hostname: host, State: "missing",
				})
			}
		}
		sort.Slice(f.PerEnvoy, func(a, b int) bool {
			return f.PerEnvoy[a].Hostname < f.PerEnvoy[b].Hostname
		})

		liveCount := 0
		tombCount := 0
		missingCount := 0
		shas := make(map[string]struct{})
		var maxSize uint64
		for _, c := range f.PerEnvoy {
			switch c.State {
			case "live":
				liveCount++
				if c.Sha256 != "" {
					shas[c.Sha256] = struct{}{}
				}
				if c.Size > maxSize {
					maxSize = c.Size
				}
			case "tombstoned":
				tombCount++
			case "missing":
				missingCount++
			}
		}
		switch {
		case tombCount > 0 && liveCount == 0:
			f.Status = "tombstoned"
		case liveCount > 0 && len(shas) > 1:
			f.Status = "divergent"
			divergences = append(divergences, f.Name)
		case missingCount > 0 && liveCount > 0:
			f.Status = "partial"
			divergences = append(divergences, f.Name)
		default:
			f.Status = "live"
		}
		if liveCount > 0 {
			fileCount++
			totalBytes += int64(maxSize)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return fileCount, totalBytes, divergences
}
