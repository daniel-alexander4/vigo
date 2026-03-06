package metrics

import (
	"context"
	"log/slog"
	"time"

	"github.com/isogov/isogov/server/db"
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
	clients, err := c.queries.ListClients(ctx)
	if err != nil {
		return err
	}

	// Total enrolled clients.
	ClientsTotal.Set(float64(len(clients)))

	// Active clients (last_seen within the last hour).
	now := time.Now()
	oneHourAgo := now.Add(-1 * time.Hour)
	var activeCount int
	for _, cl := range clients {
		if cl.LastSeen.Valid && cl.LastSeen.Time.After(oneHourAgo) {
			activeCount++
		}

		// Time since last check-in per client.
		if cl.LastSeen.Valid {
			TimeSinceCheckin.WithLabelValues(cl.Hostname).Set(now.Sub(cl.LastSeen.Time).Seconds())
		}
	}
	ClientsActive.Set(float64(activeCount))

	// Compliance percentage: non-revoked clients with a recent successful run
	// vs total non-revoked clients. Simple heuristic until full compliance engine.
	var totalActive, compliantCount int
	for _, cl := range clients {
		if cl.Revoked {
			continue
		}
		totalActive++
		if cl.LastSeen.Valid && cl.LastSeen.Time.After(oneHourAgo) {
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
