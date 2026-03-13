// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package fleet

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/hobnob/hobnob/server/compliance"
	"github.com/hobnob/hobnob/server/db"
)

// Rehydrate loads the FleetIndex from SQLite on startup. It populates all maps,
// parses pubkeys, loads traits, and computes compliance status for each thrall.
// Any thralls with unparseable pubkeys are still loaded but won't have cached keys.
func Rehydrate(ctx context.Context, fi *FleetIndex, queries *db.Queries, runStore db.RunStore, baseInterval time.Duration) error {
	start := time.Now()

	// Load all thralls with their latest run status in a single query.
	thrallsWithRuns, err := queries.ListThrallsWithLatestRun(ctx)
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

	// Build trait lookup: thrallID → traits JSON
	traitsByThrall := make(map[string]string, len(allTraits))
	for _, t := range allTraits {
		traitsByThrall[t.ID] = t.Traits
	}

	for _, twr := range thrallsWithRuns {
		t := twr.Thrall

		// Parse pubkey.
		pubKey, pkErr := db.ParsePubKey(t.PubkeyPem)
		if pkErr != nil {
			slog.Warn("rehydrate: failed to parse pubkey, thrall will need DB fallback",
				"thrall_id", t.ID,
				"hostname", t.Hostname,
				"error", pkErr,
			)
		}

		// Compute compliance status.
		lastSeen := t.EnrolledAt
		if t.LastSeen.Valid {
			lastSeen = t.LastSeen.Time
		}

		var runInfo *compliance.RunInfo
		if twr.LatestRunStatus.Valid {
			runInfo = &compliance.RunInfo{
				Status:       twr.LatestRunStatus.String,
				ChangedCount: twr.LatestRunChangedCount.Int64,
			}
		}
		compStatus := compliance.ComputeStatus(runInfo, lastSeen, baseInterval)

		// Parse pending rotated secrets.
		var pendingSecrets []string
		if t.PendingRotatedSecrets != "" && t.PendingRotatedSecrets != "[]" {
			_ = json.Unmarshal([]byte(t.PendingRotatedSecrets), &pendingSecrets)
		}

		// Parse traits into flattened map.
		var traits map[string]string
		if traitsJSON, ok := traitsByThrall[t.ID]; ok {
			var parsed map[string]any
			if err := json.Unmarshal([]byte(traitsJSON), &parsed); err == nil {
				traits = make(map[string]string)
				flattenJSON("", parsed, traits)
			}
		}

		lastRunAt := time.Time{}
		if twr.LatestRunStatus.Valid {
			// We don't have exact run time from the batch query, use LastSeen as proxy.
			lastRunAt = lastSeen
		}

		ts := ThrallState{
			ID:                    t.ID,
			Hostname:              t.Hostname,
			IP:                    t.IpAddress,
			PubkeyPEM:             t.PubkeyPem,
			EnrolledAt:            t.EnrolledAt,
			LastSeen:              lastSeen,
			Revoked:               t.Revoked != 0,
			ComplianceStatus:      compStatus,
			LastRunStatus:         nullStr(twr.LatestRunStatus),
			LastRunChangedCount:   nullInt(twr.LatestRunChangedCount),
			LastRunAt:             lastRunAt,
			Traits:                traits,
			PendingRotatedSecrets: pendingSecrets,
			ForcePush:             t.ForcePush != 0,
		}

		fi.AddThrall(ts, pubKey)
	}

	elapsed := time.Since(start)
	slog.Info("fleet index rehydrated",
		"thralls", len(thrallsWithRuns),
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
