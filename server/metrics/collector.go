// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package metrics

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/daniel-alexander4/vigo/server/db"
)

// Collector periodically queries the database to update gauge metrics.
type Collector struct {
	queries *db.Queries
	sqlDB   *sql.DB
}

// NewCollector creates a new metrics collector.
func NewCollector(queries *db.Queries, sqlDB *sql.DB) *Collector {
	return &Collector{queries: queries, sqlDB: sqlDB}
}

// UpdateGauges queries the database and updates all gauge metrics.
func (c *Collector) UpdateGauges(ctx context.Context) error {
	envoys, err := c.queries.ListEnvoys(ctx)
	if err != nil {
		return err
	}

	// Single pass: compute all envoy metrics.
	EnvoysTotal.Set(float64(len(envoys)))

	now := time.Now()
	oneHourAgo := now.Add(-1 * time.Hour)
	var activeCount, totalNonRevoked, compliantCount int
	for _, n := range envoys {
		recentlyActive := n.LastSeen.Valid && n.LastSeen.Time.After(oneHourAgo)

		if recentlyActive {
			activeCount++
		}
		if n.LastSeen.Valid {
			CheckinAgeDist.Observe(now.Sub(n.LastSeen.Time).Seconds())
		}
		if n.Revoked == 0 {
			totalNonRevoked++
			if recentlyActive {
				compliantCount++
			}
		}
	}
	EnvoysActive.Set(float64(activeCount))

	if totalNonRevoked > 0 {
		ConvergencePct.Set(float64(compliantCount) / float64(totalNonRevoked))
	} else {
		ConvergencePct.Set(0)
	}

	// Update database size metric.
	if c.sqlDB != nil {
		if size, err := db.DatabaseSizeBytes(c.sqlDB); err == nil {
			DatabaseSizeBytes.Set(float64(size))
		}
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
