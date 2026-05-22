// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"git.alexander4.io/vigo/server/convergence"
)

// EnvoyRecentRuns holds an envoy plus its last N run statuses for convergence computation.
type EnvoyRecentRuns struct {
	ID           string
	Hostname     string
	IPAddress    string
	LastSeen     sql.NullTime
	AgentVersion string
	// Up to convergence.DivergedThreshold most recent runs (newest first).
	// Sized to match the drift-axis classifier — fewer would mean
	// ComputeDrift can never return DriftDiverged (the ring of recent
	// runs would always undercount consecutive changes), so the row
	// badge would silently disagree with the fleet pill count that the
	// FleetIndex (which has a deeper history) reports.
	Runs []RecentRun
}

// RecentRun is a minimal run record for convergence computation.
type RecentRun struct {
	ID           string
	Status       string
	ChangedCount int64
	FailedCount  int64
	ModuleCount  int64
	StartedAt    time.Time
}

// fleetConvergenceQuery fetches all active envoys with their last N runs in a
// single query, where N == convergence.DivergedThreshold so the row-level
// ComputeDrift call sees the same history depth as the FleetIndex's ring
// buffer — without this, the LIMIT silently caps drift detection below the
// threshold and DriftDiverged is unreachable from the DB path. Uses a
// correlated subquery with ROW_NUMBER emulation for SQLite.
var fleetConvergenceQuery = fmt.Sprintf(`
SELECT
    e.id, e.hostname, e.ip_address, e.last_seen, e.agent_version,
    r.id, r.status, r.changed_count, r.failed_count, r.module_count, r.started_at
FROM envoys e
LEFT JOIN runs r ON r.envoy_id = e.id
    AND r.id IN (
        SELECT r2.id FROM runs r2
        WHERE r2.envoy_id = e.id
        ORDER BY r2.started_at DESC
        LIMIT %d
    )
WHERE e.revoked = 0
ORDER BY e.hostname, r.started_at DESC
`, convergence.DivergedThreshold)

// ListFleetConvergence returns all active envoys with up to
// convergence.DivergedThreshold most-recent runs each. Replaces the N+1
// pattern of ListEnvoys + per-envoy ListRunsByEnvoy.
func (q *Queries) ListFleetConvergence(ctx context.Context) ([]EnvoyRecentRuns, error) {
	rows, err := q.db.QueryContext(ctx, fleetConvergenceQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	envoyMap := make(map[string]*EnvoyRecentRuns)
	var order []string

	for rows.Next() {
		var (
			eid, hostname, ip, agentVer string
			lastSeen                    sql.NullTime
			runID                       sql.NullString
			runStatus                   sql.NullString
			runChanged                  sql.NullInt64
			runFailed                   sql.NullInt64
			runModules                  sql.NullInt64
			runStarted                  sql.NullTime
		)
		if err := rows.Scan(&eid, &hostname, &ip, &lastSeen, &agentVer,
			&runID, &runStatus, &runChanged, &runFailed, &runModules, &runStarted); err != nil {
			return nil, err
		}

		entry, ok := envoyMap[eid]
		if !ok {
			entry = &EnvoyRecentRuns{
				ID:           eid,
				Hostname:     hostname,
				IPAddress:    ip,
				LastSeen:     lastSeen,
				AgentVersion: agentVer,
			}
			envoyMap[eid] = entry
			order = append(order, eid)
		}
		if runID.Valid {
			entry.Runs = append(entry.Runs, RecentRun{
				ID:           runID.String,
				Status:       runStatus.String,
				ChangedCount: runChanged.Int64,
				FailedCount:  runFailed.Int64,
				ModuleCount:  runModules.Int64,
				StartedAt:    runStarted.Time,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := make([]EnvoyRecentRuns, 0, len(order))
	for _, id := range order {
		result = append(result, *envoyMap[id])
	}
	return result, nil
}

// ModuleRunChange is a single (module, run) pair telling whether that
// module had ANY resource that the agent reported `changed: true` for
// during that run. The run-detail page feeds slices of these — one per
// module, newest run first — into convergence.ComputeDrift to decide
// whether the module is in DriftDiverged. Surfacing drift at the
// module grain (not the envoy grain) lets the page paint a "Diverged"
// pill on the right rows.
type ModuleRunChange struct {
	ModuleName string
	RunID      string
	StartedAt  time.Time
	AnyChanged bool
}

// moduleChangesQuery returns rows for the last DivergedThreshold runs of
// a given envoy ending at or before a supplied anchor timestamp (the
// run currently being viewed). One row per (module, run); `AnyChanged`
// is true iff at least one resource on that module in that run reported
// `changed: true`. Joins so we can pull started_at without an extra
// lookup; SQLite's MAX over an INTEGER column returns 0 or 1.
var moduleChangesQuery = fmt.Sprintf(`
SELECT rr.module_name, rr.run_id, r.started_at, MAX(rr.changed) AS any_changed
FROM run_results rr
JOIN runs r ON r.id = rr.run_id
WHERE r.id IN (
    SELECT id FROM runs
    WHERE envoy_id = ?
      AND started_at <= ?
    ORDER BY started_at DESC
    LIMIT %d
)
GROUP BY rr.module_name, rr.run_id, r.started_at
ORDER BY rr.module_name, r.started_at DESC
`, convergence.DivergedThreshold)

// ListEnvoyModuleChangesRecent returns the module-change matrix for the
// last convergence.DivergedThreshold runs of `envoyID`, ending at the
// run whose `startedAt` is supplied (inclusive). Rows are ordered so
// each module's runs appear newest-first — directly feedable to
// convergence.ComputeDrift without another sort.
func (q *Queries) ListEnvoyModuleChangesRecent(ctx context.Context, envoyID string, startedAt time.Time) ([]ModuleRunChange, error) {
	rows, err := q.db.QueryContext(ctx, moduleChangesQuery, envoyID, startedAt)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ModuleRunChange
	for rows.Next() {
		var mc ModuleRunChange
		var anyChanged int64
		if err := rows.Scan(&mc.ModuleName, &mc.RunID, &mc.StartedAt, &anyChanged); err != nil {
			return nil, err
		}
		mc.AnyChanged = anyChanged != 0
		out = append(out, mc)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// EnvoyPagedRow is an envoy with its latest run info, returned by the paged query.
type EnvoyPagedRow struct {
	ID             string
	Hostname       string
	IPAddress      string
	EnrolledAt     time.Time
	LastSeen       sql.NullTime
	AgentVersion   string
	// Latest run (nullable).
	LastRunID      sql.NullString
	LastRunAt      sql.NullTime
	LastRunStatus  sql.NullString
	LastRunChanged sql.NullInt64
	LastRunFailed  sql.NullInt64
	LastRunModules sql.NullInt64
}

const listEnvoysPagedWithRun = `
SELECT
    e.id, e.hostname, e.ip_address, e.enrolled_at, e.last_seen, e.agent_version,
    lr.id, lr.started_at, lr.status, lr.changed_count, lr.failed_count, lr.module_count
FROM envoys e
LEFT JOIN runs lr ON lr.envoy_id = e.id
    AND lr.started_at = (
        SELECT MAX(r2.started_at) FROM runs r2 WHERE r2.envoy_id = e.id
    )
WHERE e.revoked = 0
  AND (?1 = '' OR e.hostname > ?1)
  AND (?2 = '' OR e.hostname LIKE '%' || ?2 || '%')
ORDER BY e.hostname
LIMIT ?3
`

// ListEnvoysPagedWithRun returns active envoys with their latest run, paginated.
// Replaces ListActiveEnvoysPaged + per-envoy ListRunsByEnvoy.
func (q *Queries) ListEnvoysPagedWithRun(ctx context.Context, afterHostname, search string, limit int64) ([]EnvoyPagedRow, error) {
	rows, err := q.db.QueryContext(ctx, listEnvoysPagedWithRun, afterHostname, search, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []EnvoyPagedRow
	for rows.Next() {
		var r EnvoyPagedRow
		if err := rows.Scan(&r.ID, &r.Hostname, &r.IPAddress, &r.EnrolledAt, &r.LastSeen, &r.AgentVersion,
			&r.LastRunID, &r.LastRunAt, &r.LastRunStatus, &r.LastRunChanged, &r.LastRunFailed, &r.LastRunModules); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// RunWithHostname is a run joined with its envoy hostname plus a
// dashboard-flavoured "top failed module" hint sourced from run_results.
// The hint is the first per-module row whose error_message is non-empty,
// ordered by created_at; empty when nothing failed in this run. Used to
// cut down the click-to-triage path on the dashboard recent-activity list.
type RunWithHostname struct {
	ID              string
	EnvoyID         string
	Hostname        string
	StartedAt       time.Time
	FinishedAt      sql.NullTime
	Status          string
	Trigger         string
	ModuleCount     int64
	ChangedCount    int64
	FailedCount     int64
	SkippedCount    int64
	TopFailedModule string
}

const listRunsWithHostname = `
SELECT r.id, r.envoy_id, e.hostname, r.started_at, r.finished_at,
       r.status, r.trigger, r.module_count, r.changed_count, r.failed_count, r.skipped_count,
       COALESCE((
         SELECT rr.module_name FROM run_results rr
         WHERE rr.run_id = r.id
           AND rr.error_message IS NOT NULL
           AND rr.error_message <> ''
         ORDER BY rr.created_at
         LIMIT 1
       ), '') AS top_failed_module
FROM runs r
JOIN envoys e ON e.id = r.envoy_id
ORDER BY r.started_at DESC
LIMIT ?
`

// ListRunsWithHostname returns recent runs with the envoy hostname joined in
// plus the first failing module's name (empty when the run had no failures).
// Replaces ListAllRuns + a separate ListEnvoys call for hostname lookup.
func (q *Queries) ListRunsWithHostname(ctx context.Context, limit int64) ([]RunWithHostname, error) {
	rows, err := q.db.QueryContext(ctx, listRunsWithHostname, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []RunWithHostname
	for rows.Next() {
		var r RunWithHostname
		if err := rows.Scan(&r.ID, &r.EnvoyID, &r.Hostname, &r.StartedAt, &r.FinishedAt,
			&r.Status, &r.Trigger, &r.ModuleCount, &r.ChangedCount, &r.FailedCount, &r.SkippedCount,
			&r.TopFailedModule); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// GetAllLatestTraitsPaged returns envoys with traits, paginated by hostname cursor.
func (q *Queries) GetAllLatestTraitsPaged(ctx context.Context, afterHostname string, limit int64) ([]EnvoyWithTraits, error) {
	const query = `
SELECT t.id, t.hostname, t.ip_address, tt.traits, tt.collected_at
FROM envoys t
INNER JOIN envoy_traits tt ON tt.envoy_id = t.id
WHERE tt.collected_at = (
  SELECT MAX(tt2.collected_at) FROM envoy_traits tt2 WHERE tt2.envoy_id = t.id
)
AND t.revoked = 0
AND (?1 = '' OR t.hostname > ?1)
ORDER BY t.hostname
LIMIT ?2
`
	rows, err := q.db.QueryContext(ctx, query, afterHostname, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []EnvoyWithTraits
	for rows.Next() {
		var r EnvoyWithTraits
		if err := rows.Scan(&r.ID, &r.Hostname, &r.IPAddress, &r.Traits, &r.CollectedAt); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}
