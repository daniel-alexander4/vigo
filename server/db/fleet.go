// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package db

import (
	"context"
	"database/sql"
	"time"
)

// EnvoyRecentRuns holds an envoy plus its last N run statuses for convergence computation.
type EnvoyRecentRuns struct {
	ID           string
	Hostname     string
	IPAddress    string
	LastSeen     sql.NullTime
	AgentVersion string
	// Up to 3 most recent runs (newest first). Empty if no runs.
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

// fleetConvergenceQuery fetches all active envoys with their last 3 runs in a
// single query. Uses a correlated subquery with ROW_NUMBER emulation for SQLite.
const fleetConvergenceQuery = `
SELECT
    e.id, e.hostname, e.ip_address, e.last_seen, e.agent_version,
    r.id, r.status, r.changed_count, r.failed_count, r.module_count, r.started_at
FROM envoys e
LEFT JOIN runs r ON r.envoy_id = e.id
    AND r.id IN (
        SELECT r2.id FROM runs r2
        WHERE r2.envoy_id = e.id
        ORDER BY r2.started_at DESC
        LIMIT 3
    )
WHERE e.revoked = 0
ORDER BY e.hostname, r.started_at DESC
`

// ListFleetConvergence returns all active envoys with their last 3 runs.
// This replaces the N+1 pattern of ListEnvoys + per-envoy ListRunsByEnvoy.
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

// RunWithHostname is a run joined with its envoy hostname.
type RunWithHostname struct {
	ID           string
	EnvoyID      string
	Hostname     string
	StartedAt    time.Time
	FinishedAt   sql.NullTime
	Status       string
	ModuleCount  int64
	ChangedCount int64
	FailedCount  int64
	SkippedCount int64
}

const listRunsWithHostname = `
SELECT r.id, r.envoy_id, e.hostname, r.started_at, r.finished_at,
       r.status, r.module_count, r.changed_count, r.failed_count, r.skipped_count
FROM runs r
JOIN envoys e ON e.id = r.envoy_id
ORDER BY r.started_at DESC
LIMIT ?
`

// ListRunsWithHostname returns recent runs with the envoy hostname joined in.
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
			&r.Status, &r.ModuleCount, &r.ChangedCount, &r.FailedCount, &r.SkippedCount); err != nil {
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
