package db

import (
	"context"
	"time"
)

// ThrallWithTraits holds a thrall's identity and its latest trait snapshot.
type ThrallWithTraits struct {
	ID          string
	Hostname    string
	IPAddress   string
	Traits      string // raw JSON
	CollectedAt time.Time
}

const getAllLatestTraits = `
SELECT t.id, t.hostname, t.ip_address, tt.traits, tt.collected_at
FROM thralls t
INNER JOIN thrall_traits tt ON tt.thrall_id = t.id
WHERE tt.collected_at = (
  SELECT MAX(tt2.collected_at) FROM thrall_traits tt2 WHERE tt2.thrall_id = t.id
)
AND t.revoked = 0
ORDER BY t.hostname
`

// GetAllLatestTraits returns every non-revoked thrall joined with its most
// recent trait snapshot. Thralls that have never reported traits are excluded.
func (q *Queries) GetAllLatestTraits(ctx context.Context) ([]ThrallWithTraits, error) {
	rows, err := q.db.QueryContext(ctx, getAllLatestTraits)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []ThrallWithTraits
	for rows.Next() {
		var r ThrallWithTraits
		if err := rows.Scan(&r.ID, &r.Hostname, &r.IPAddress, &r.Traits, &r.CollectedAt); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}
