package metrics

import (
	"context"
	"log/slog"
	"time"

	"github.com/hobnob/hobnob/server/db"
)

// Collector periodically queries the database to update gauge metrics.
type Collector struct {
	queries *db.Queries
}

// NewCollector creates a new metrics collector.
func NewCollector(queries *db.Queries) *Collector {
	return &Collector{queries: queries}
}

// UpdateGauges queries the database and updates all gauge metrics.
func (c *Collector) UpdateGauges(ctx context.Context) error {
	nodes, err := c.queries.ListNodes(ctx)
	if err != nil {
		return err
	}

	// Total enrolled nodes.
	NodesTotal.Set(float64(len(nodes)))

	// Active nodes (last_seen within the last hour).
	now := time.Now()
	oneHourAgo := now.Add(-1 * time.Hour)
	var activeCount int
	for _, n := range nodes {
		if n.LastSeen.Valid && n.LastSeen.Time.After(oneHourAgo) {
			activeCount++
		}

		// Time since last check-in per node.
		if n.LastSeen.Valid {
			TimeSinceCheckin.WithLabelValues(n.Hostname).Set(now.Sub(n.LastSeen.Time).Seconds())
		}
	}
	NodesActive.Set(float64(activeCount))

	// Compliance heuristic: percentage of non-revoked nodes that checked in
	// recently. This approximates compliance — nodes that aren't checking in
	// can't be converging. For per-node compliance status (compliant/drifted/
	// failing/stale), see the compliance package and ComplianceStatus gauge.
	var totalActive, compliantCount int
	for _, n := range nodes {
		if n.Revoked {
			continue
		}
		totalActive++
		if n.LastSeen.Valid && n.LastSeen.Time.After(oneHourAgo) {
			compliantCount++
		}
	}
	if totalActive > 0 {
		CompliancePct.Set(float64(compliantCount) / float64(totalActive))
	} else {
		CompliancePct.Set(0)
	}

	return nil
}

// Run starts a periodic update loop that calls UpdateGauges at the given interval.
// It blocks until the context is cancelled.
func (c *Collector) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Initial update.
	if err := c.UpdateGauges(ctx); err != nil {
		slog.Error("metrics collector initial update failed", "error", err)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.UpdateGauges(ctx); err != nil {
				slog.Error("metrics collector update failed", "error", err)
			}
		}
	}
}
