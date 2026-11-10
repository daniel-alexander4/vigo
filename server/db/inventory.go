// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

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
