// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package fleet

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"time"

	"strings"

	"github.com/daniel-alexander4/vigo/server/convergence"
	"github.com/daniel-alexander4/vigo/server/db"
)

// Rehydrate loads the FleetIndex from SQLite on startup. It populates all maps,
// parses pubkeys, loads traits, and computes convergence status for each envoy.
// Any envoys with unparseable pubkeys are still loaded but won't have cached keys.
func Rehydrate(ctx context.Context, fi *FleetIndex, queries *db.Queries, runStore db.RunStore, baseInterval time.Duration) error {
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

		// Compute convergence status.
		lastSeen := t.EnrolledAt
		if t.LastSeen.Valid {
			lastSeen = t.LastSeen.Time
		}

		var recent []convergence.RunInfo
		if twr.LatestRunStatus.Valid {
			recent = []convergence.RunInfo{{
				Status:       twr.LatestRunStatus.String,
				ChangedCount: twr.LatestRunChangedCount.Int64,
			}}
		}
		convStatus := convergence.ComputeConvergence(recent)

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

		// Parse tags from comma-separated string.
		var tags []string
		if t.Tags != "" {
			for _, tag := range strings.Split(t.Tags, ",") {
				tag = strings.TrimSpace(tag)
				if tag != "" {
					tags = append(tags, tag)
				}
			}
		}

		var maintenanceUntil time.Time
		if t.MaintenanceUntil.Valid {
			maintenanceUntil = t.MaintenanceUntil.Time
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
			LastRunID:             lastRunID,
			LastRunStatus:         nullStr(twr.LatestRunStatus),
			LastRunChangedCount:   nullInt(twr.LatestRunChangedCount),
			LastRunAt:             lastRunAt,
			Traits:                traits,
			PendingRotatedSecrets: pendingSecrets,
			ForcePush:             t.ForcePush != 0,
			Tags:                  tags,
			MaintenanceUntil:      maintenanceUntil,
		}

		fi.AddEnvoy(ts, pubKey)
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
