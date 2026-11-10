// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package fleet

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"git.alexander4.io/vigo/server/convergence"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/freshness"
)

// Rehydrate loads the FleetIndex from SQLite on startup. It populates all maps,
// parses pubkeys, loads traits, and computes convergence status for each envoy.
// Any envoys with unparseable pubkeys are still loaded but won't have cached keys.
// approvedSources is the compliance.approved_sources allowlist (nil or
// empty disables package_source_audit annotation).
func Rehydrate(ctx context.Context, fi *FleetIndex, queries *db.Queries, runStore db.RunStore, baseInterval, offlineThreshold time.Duration, approvedSources []string) error {
	start := time.Now()

	// Load all envoys with their latest run status in a single query.
	envoysWithRuns, err := queries.ListEnvoysWithLatestRun(ctx)
	if err != nil {
		return err
	}

	// Load all latest traits in a single query.
	allTraits, err := queries.GetAllLatestTraits(ctx)
	if err != nil {
		// Non-fatal — we can run without traits.
		slog.Warn("rehydrate: failed to load traits, continuing without", "error", err)
		allTraits = nil
	}

	// Build trait lookup: envoyID → traits JSON
	traitsByEnvoy := make(map[string]string, len(allTraits))
	for _, t := range allTraits {
		traitsByEnvoy[t.ID] = t.Traits
	}

	// Load the trailing run window used by convergence computation in a
	// single query. ListFleetConvergence returns up to DivergedThreshold
	// (convergence.DivergedThreshold) runs per envoy, which is exactly the
	// diverged-detection horizon. Convergence status
	// is recomputed below from this trailing history, so row status and
	// fleet counts stay consistent across a server restart.
	fleetRuns, err := queries.ListFleetConvergence(ctx)
	if err != nil {
		slog.Warn("rehydrate: failed to load fleet convergence runs, continuing with empty histories", "error", err)
	}
	runsByEnvoy := make(map[string][]convergence.RunInfo, len(fleetRuns))
	for _, fr := range fleetRuns {
		if len(fr.Runs) == 0 {
			continue
		}
		runs := make([]convergence.RunInfo, 0, len(fr.Runs))
		for _, r := range fr.Runs {
			runs = append(runs, convergence.RunInfo{
				Status:        r.Status,
				ChangedCount:  r.ChangedCount,
				FailedCount:   r.FailedCount,
				ResourceCount: r.ResourceCount,
			})
		}
		runsByEnvoy[fr.ID] = runs
	}

	for _, twr := range envoysWithRuns {
		t := twr.Envoy

		// Parse pubkey.
		pubKey, pkErr := db.ParsePubKey(t.PubkeyPem)
		if pkErr != nil {
			slog.Warn("rehydrate: failed to parse pubkey, envoy will need DB fallback",
				"envoy_id", t.ID,
				"hostname", t.Hostname,
				"error", pkErr,
			)
		}

		// Compute convergence status from the trailing run window loaded
		// above. Falls back to a single-run slice synthesised from the
		// latest-run columns if this envoy has no rows in fleetRuns (can
		// happen if the query failed or the envoy was just enrolled and
		// hasn't produced a run yet — ComputeConvergence handles the
		// empty-slice "no data" case correctly).
		lastSeen := t.EnrolledAt
		if t.LastSeen.Valid {
			lastSeen = t.LastSeen.Time
		}

		recent := runsByEnvoy[t.ID]
		if recent == nil && twr.LatestRunStatus.Valid {
			recent = []convergence.RunInfo{{
				Status:        twr.LatestRunStatus.String,
				ChangedCount:  twr.LatestRunChangedCount.Int64,
				FailedCount:   twr.LatestRunFailedCount.Int64,
				ResourceCount: twr.LatestRunResourceCount.Int64,
			}}
		}
		convStatus := convergence.ComputeConvergence(recent)
		drift := convergence.ComputeDrift(recent)

		// Parse pending rotated secrets.
		var pendingSecrets []string
		if t.PendingRotatedSecrets != "" && t.PendingRotatedSecrets != "[]" {
			if err := json.Unmarshal([]byte(t.PendingRotatedSecrets), &pendingSecrets); err != nil {
				slog.Warn("malformed pending_rotated_secrets JSON", "envoy_id", t.ID, "error", err)
			}
		}

		// Parse traits into flattened map.
		var traits map[string]string
		if traitsJSON, ok := traitsByEnvoy[t.ID]; ok {
			var parsed map[string]any
			if err := json.Unmarshal([]byte(traitsJSON), &parsed); err == nil {
				traits = FlattenForIndex(parsed)
				AnnotatePackageSources(traits, approvedSources)
			}
		}

		lastRunID := ""
		if twr.LatestRunID.Valid {
			lastRunID = twr.LatestRunID.String
		}
		lastRunAt := time.Time{}
		if twr.LatestRunStatus.Valid {
			// We don't have exact run time from the batch query, use LastSeen as proxy.
			lastRunAt = lastSeen
		}

		var maintenanceUntil time.Time
		if t.MaintenanceUntil.Valid {
			maintenanceUntil = t.MaintenanceUntil.Time
		}

		// Failed/module counts come from recent[0] — the trailing run window
		// when present, else the latest-run fallback synthesised above (both
		// now carry the counts).
		var lastFailed, lastConfigcrates int64
		if len(recent) > 0 {
			lastFailed = recent[0].FailedCount
			lastConfigcrates = recent[0].ResourceCount
		}

		ts := EnvoyState{
			ID:                    t.ID,
			Hostname:              t.Hostname,
			IP:                    t.IpAddress,
			PubkeyPEM:             t.PubkeyPem,
			CertSPKI:              t.CertSpkiSha256,
			EnrolledAt:            t.EnrolledAt,
			LastSeen:              lastSeen,
			Revoked:               t.Revoked != 0,
			ConvergenceStatus:     convStatus,
			Drift:                 drift,
			LastRunID:             lastRunID,
			LastRunStatus:         nullStr(twr.LatestRunStatus),
			LastRunChangedCount:   nullInt(twr.LatestRunChangedCount),
			LastRunFailedCount:    lastFailed,
			LastRunResourceCount:  lastConfigcrates,
			LastRunAt:             lastRunAt,
			RecentRuns:            recent,
			Traits:                traits,
			PendingRotatedSecrets: pendingSecrets,
			ForcePush:             t.ForcePush != 0,
			MaintenanceUntil:      maintenanceUntil,
		}

		fi.AddEnvoy(ts, pubKey)

		// Mark offline if last seen exceeds the canonical stale threshold.
		// At rehydrate time the in-memory cycle ring is empty, so observed=0
		// (bootstrap) and the threshold is derived from configured interval
		// alone. The threshold widens automatically once each envoy reports
		// its first inter-CheckIn delta.
		if !ts.Revoked && freshness.IsStaleFromCycle(ts.LastSeen, baseInterval, 0, offlineThreshold) {
			fi.SetOffline(ts.ID, true)
		}
	}

	// Revoked envoys are not rehydrated (ListEnvoysWithLatestRun filters them),
	// so seed the revoked-id set: the check-in verify path answers "revoked"
	// from memory instead of reading the DB for every id it doesn't hold. Fatal
	// on error — without the set a revoked envoy would read as unknown.
	revokedIDs, err := queries.ListRevokedEnvoyIDs(ctx)
	if err != nil {
		return fmt.Errorf("rehydrate: listing revoked envoys: %w", err)
	}
	fi.AddRevokedIDs(revokedIDs)

	elapsed := time.Since(start)
	slog.Info("fleet index rehydrated",
		"envoys", len(envoysWithRuns),
		"revoked", len(revokedIDs),
		"traits", len(allTraits),
		"elapsed", elapsed,
	)

	warnUnpinnedEnvoys(fi)

	return nil
}

// unpinnedWarnNames caps how many hostnames the startup unpinned-envoy WARN
// lists; the count is always exact.
const unpinnedWarnNames = 20

// warnUnpinnedEnvoys logs one startup WARN naming the non-revoked envoys that
// carry no enrolled cert pin. The verify path refuses them, so each must be
// re-enrolled; `vigocli doctor` reports the same set.
func warnUnpinnedEnvoys(fi *FleetIndex) {
	unpinned := fi.UnpinnedEnvoys()
	if len(unpinned) == 0 {
		return
	}
	names := make([]string, 0, min(len(unpinned), unpinnedWarnNames))
	for i, e := range unpinned {
		if i == unpinnedWarnNames {
			break
		}
		names = append(names, e.Hostname+" ("+e.EnvoyID+")")
	}
	slog.Warn("cert-pin: envoys with no enrolled client-cert pin will be refused at check-in until re-enrolled — run on each: curl -sSfk https://<server>:8443/bootstrap | sudo sh",
		"count", len(unpinned),
		"envoys", strings.Join(names, ", "),
		"listed", len(names),
	)
}

func nullStr(ns sql.NullString) string {
	if ns.Valid {
		return ns.String
	}
	return ""
}

func nullInt(ni sql.NullInt64) int64 {
	if ni.Valid {
		return ni.Int64
	}
	return 0
}
