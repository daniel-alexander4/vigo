// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package fleet

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
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
	// single query. ListFleetConvergence returns up to 3 runs per envoy
	// which is exactly the diverged-detection horizon. Convergence status
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
				Status:       r.Status,
				ChangedCount: r.ChangedCount,
				FailedCount:  r.FailedCount,
				ModuleCount:  r.ModuleCount,
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
				Status:       twr.LatestRunStatus.String,
				ChangedCount: twr.LatestRunChangedCount.Int64,
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
				traits = make(map[string]string)
				FlattenJSON("", parsed, traits)
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

		// Prefer counts from the trailing run window if available, since
		// ListEnvoysWithLatestRun doesn't carry failed/module counts.
		var lastFailed, lastModules int64
		if len(recent) > 0 {
			lastFailed = recent[0].FailedCount
			lastModules = recent[0].ModuleCount
		}

		ts := EnvoyState{
			ID:                    t.ID,
			Hostname:              t.Hostname,
			IP:                    t.IpAddress,
			PubkeyPEM:             t.PubkeyPem,
			EnrolledAt:            t.EnrolledAt,
			LastSeen:              lastSeen,
			Revoked:               t.Revoked != 0,
			ConvergenceStatus:      convStatus,
			Drift:                 drift,
			LastRunID:             lastRunID,
			LastRunStatus:         nullStr(twr.LatestRunStatus),
			LastRunChangedCount:   nullInt(twr.LatestRunChangedCount),
			LastRunFailedCount:    lastFailed,
			LastRunModuleCount:    lastModules,
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

	elapsed := time.Since(start)
	slog.Info("fleet index rehydrated",
		"envoys", len(envoysWithRuns),
		"traits", len(allTraits),
		"elapsed", elapsed,
	)

	return nil
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
