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
	thralls, err := c.queries.ListThralls(ctx)
	if err != nil {
		return err
	}

	// Single pass: compute all thrall metrics.
	ThrallsTotal.Set(float64(len(thralls)))

	now := time.Now()
	oneHourAgo := now.Add(-1 * time.Hour)
	var activeCount, totalNonRevoked, compliantCount int
	for _, n := range thralls {
		recentlyActive := n.LastSeen.Valid && n.LastSeen.Time.After(oneHourAgo)

		if recentlyActive {
			activeCount++
		}
		if n.LastSeen.Valid {
			TimeSinceCheckin.WithLabelValues(n.Hostname).Set(now.Sub(n.LastSeen.Time).Seconds())
		}
		if n.Revoked == 0 {
			totalNonRevoked++
			if recentlyActive {
				compliantCount++
			}
		}
	}
	ThrallsActive.Set(float64(activeCount))

	if totalNonRevoked > 0 {
		CompliancePct.Set(float64(compliantCount) / float64(totalNonRevoked))
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
