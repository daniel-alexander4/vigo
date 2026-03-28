// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package db

import (
	"context"
	"time"
)

// EnvoyWithTraits holds a envoy's identity and its latest trait snapshot.
type EnvoyWithTraits struct {
	ID          string
	Hostname    string
	IPAddress   string
	Traits      string // raw JSON
	CollectedAt time.Time
}

const getAllLatestTraits = `
SELECT t.id, t.hostname, t.ip_address, tt.traits, tt.collected_at
FROM envoys t
INNER JOIN envoy_traits tt ON tt.envoy_id = t.id
WHERE tt.collected_at = (
  SELECT MAX(tt2.collected_at) FROM envoy_traits tt2 WHERE tt2.envoy_id = t.id
)
AND t.revoked = 0
ORDER BY t.hostname
`

// EnvoyTraitPair holds the two most recent trait snapshots for change detection.
type EnvoyTraitPair struct {
	ID              string
	Hostname        string
	CurrentTraits   string
	PreviousTraits  string // empty if only one snapshot exists
	CurrentCollected  time.Time
	PreviousCollected time.Time
}

const getTraitPairs = `
SELECT e.id, e.hostname,
       t1.traits, t1.collected_at,
       COALESCE(t2.traits, ''), COALESCE(t2.collected_at, t1.collected_at)
FROM envoys e
INNER JOIN envoy_traits t1 ON t1.envoy_id = e.id
  AND t1.collected_at = (SELECT MAX(x.collected_at) FROM envoy_traits x WHERE x.envoy_id = e.id)
LEFT JOIN envoy_traits t2 ON t2.envoy_id = e.id
  AND t2.collected_at = (
    SELECT MAX(x.collected_at) FROM envoy_traits x
    WHERE x.envoy_id = e.id AND x.collected_at < t1.collected_at
  )
WHERE e.revoked = 0
ORDER BY e.hostname
`

// GetTraitPairs returns the two most recent trait snapshots per envoy for
// detecting changes between consecutive check-ins.
func (q *Queries) GetTraitPairs(ctx context.Context) ([]EnvoyTraitPair, error) {
	rows, err := q.db.QueryContext(ctx, getTraitPairs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []EnvoyTraitPair
	for rows.Next() {
		var r EnvoyTraitPair
		if err := rows.Scan(&r.ID, &r.Hostname, &r.CurrentTraits, &r.CurrentCollected, &r.PreviousTraits, &r.PreviousCollected); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// GetAllLatestTraits returns every non-revoked envoy joined with its most
// recent trait snapshot. Envoys that have never reported traits are excluded.
func (q *Queries) GetAllLatestTraits(ctx context.Context) ([]EnvoyWithTraits, error) {
	rows, err := q.db.QueryContext(ctx, getAllLatestTraits)
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
