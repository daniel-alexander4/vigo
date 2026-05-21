// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package metrics

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/risk"
	"git.alexander4.io/vigo/server/traits"
)

// EnvoySnapshot is the minimal envoy state needed by the metrics collector.
// Decoupled from fleet.EnvoyState to avoid import cycles (fleet imports metrics).
type EnvoySnapshot struct {
	Revoked           bool
	LastSeen          time.Time
	ConvergenceStatus string
	Traits            map[string]string
	ID                string
	Hostname          string
	ObservedCycle     time.Duration
}

// ConvergenceSnapshot holds pre-computed counters across the failure
// axis (Converged/Degraded/Failed/NoData), drift axis
// (Changed/Diverged), and reachability (Offline). Diverged ⊆ Changed.
type ConvergenceSnapshot struct {
	Converged int
	Degraded  int
	Failed    int
	NoData    int
	Changed   int
	Diverged  int
	Offline   int
}

// FleetSource provides fleet data for the metrics collector.
// Implemented by fleet.FleetIndex via an adapter in main.go.
type FleetSource interface {
	ConvergenceSummary() ConvergenceSnapshot
	AllEnvoySnapshots() []EnvoySnapshot
}

// Collector periodically queries the database and fleet to update gauge metrics.
// Runs in a background goroutine — never on the request hot path.
type Collector struct {
	queries          *db.Queries
	sqlDB            *sql.DB
	fleetSource      FleetSource
	metrics          *Metrics
	baseInterval     time.Duration
	offlineThreshold time.Duration
}

// NewCollector creates a new metrics collector. fleetSource may be nil (bolt mode).
// The Metrics value is the per-instance collector set; the gauges it owns are
// the ones this collector mutates each tick.
func NewCollector(queries *db.Queries, sqlDB *sql.DB, fleetSource FleetSource, m *Metrics, baseInterval, offlineThreshold time.Duration) *Collector {
	return &Collector{
		queries:          queries,
		sqlDB:            sqlDB,
		fleetSource:      fleetSource,
		metrics:          m,
		baseInterval:     baseInterval,
		offlineThreshold: offlineThreshold,
	}
}

// UpdateGauges queries the database and fleet and updates all gauge metrics.
func (c *Collector) UpdateGauges(ctx context.Context) error {
	envoys, err := c.queries.ListEnvoys(ctx)
	if err != nil {
		return err
	}

	// Single pass: compute all envoy metrics from DB.
	c.metrics.EnvoysTotal.Set(float64(len(envoys)))

	now := time.Now()
	oneHourAgo := now.Add(-1 * time.Hour)
	var activeCount, totalNonRevoked, convergedCount int
	for _, n := range envoys {
		recentlyActive := n.LastSeen.Valid && n.LastSeen.Time.After(oneHourAgo)

		if recentlyActive {
			activeCount++
		}
		if n.LastSeen.Valid {
			c.metrics.CheckinAgeDist.Observe(now.Sub(n.LastSeen.Time).Seconds())
		}
		if n.Revoked == 0 {
			totalNonRevoked++
			if recentlyActive {
				convergedCount++
			}
		}
	}
	c.metrics.EnvoysActive.Set(float64(activeCount))

	if totalNonRevoked > 0 {
		c.metrics.ConvergencePct.Set(float64(convergedCount) / float64(totalNonRevoked))
	} else {
		c.metrics.ConvergencePct.Set(0)
	}

	// Update database size metric.
	if c.sqlDB != nil {
		if size, err := db.DatabaseSizeBytes(c.sqlDB); err == nil {
			c.metrics.DatabaseSizeBytes.Set(float64(size))
		}
	}

	// Fleet-backed metrics (convergence breakdown, security, risk).
	if c.fleetSource != nil {
		// Convergence breakdown — O(1) from pre-computed counters.
		summary := c.fleetSource.ConvergenceSummary()
		c.metrics.ConvergenceConverged.Set(float64(summary.Converged))
		c.metrics.ConvergenceDegraded.Set(float64(summary.Degraded))
		c.metrics.ConvergenceFailed.Set(float64(summary.Failed))
		c.metrics.ConvergenceNoData.Set(float64(summary.NoData))
		c.metrics.ConvergenceChanged.Set(float64(summary.Changed))
		c.metrics.ConvergenceDiverged.Set(float64(summary.Diverged))
		c.metrics.ConvergenceOffline.Set(float64(summary.Offline))

		// Single snapshot — one RLock, reused for security + risk.
		allEnvoys := c.fleetSource.AllEnvoySnapshots()
		updateSecurityMetrics(c.metrics, allEnvoys)
		updateRiskMetrics(c.metrics, allEnvoys, c.baseInterval, c.offlineThreshold)
	}

	return nil
}

// updateSecurityMetrics aggregates security_scan.* traits across all envoys.
// Pure iteration — no locks, no I/O.
func updateSecurityMetrics(m *Metrics, envoys []EnvoySnapshot) {
	var critical, high, medium, low, hardening, rootkit, malware, integrity int64
	var scanned, hardeningCount int64

	for _, es := range envoys {
		if es.Revoked || es.Traits == nil {
			continue
		}
		hasScan := false
		for k, v := range es.Traits {
			if !strings.HasPrefix(k, traits.PrefixSecurityScan) {
				continue
			}
			var n int64
			fmt.Sscanf(v, "%d", &n)
			switch k {
			case traits.UniqueCritical:
				critical += n
				hasScan = true
			case traits.UniqueHigh:
				high += n
			case traits.UniqueMedium:
				medium += n
			case traits.UniqueLow:
				low += n
			case traits.LynisScore:
				hardening += n
				hardeningCount++
				hasScan = true
			case traits.ChkrootkitInfected:
				rootkit += n
				hasScan = true
			case traits.RkhunterWarnings:
				rootkit += n
				hasScan = true
			case traits.ClamavInfected:
				malware += n
				hasScan = true
			case traits.AideChanged:
				integrity += n
				hasScan = true
			}
		}
		if hasScan {
			scanned++
		}
	}

	m.SecurityCVECritical.Set(float64(critical))
	m.SecurityCVEHigh.Set(float64(high))
	m.SecurityCVEMedium.Set(float64(medium))
	m.SecurityCVELow.Set(float64(low))
	m.SecurityRootkitWarnings.Set(float64(rootkit))
	m.SecurityMalwareDetections.Set(float64(malware))
	m.SecurityIntegrityIssues.Set(float64(integrity))
	m.SecurityScannedHosts.Set(float64(scanned))

	if hardeningCount > 0 {
		m.SecurityHardeningAvg.Set(float64(hardening) / float64(hardeningCount))
	} else {
		m.SecurityHardeningAvg.Set(0)
	}
}

// updateRiskMetrics computes fleet risk scores and sets Prometheus gauges.
// Pure arithmetic — no I/O, no locks.
func updateRiskMetrics(m *Metrics, envoys []EnvoySnapshot, baseInterval, offlineThreshold time.Duration) {
	inputs := make([]risk.EnvoyInput, 0, len(envoys))
	for _, es := range envoys {
		if es.Revoked {
			continue
		}
		inputs = append(inputs, risk.EnvoyInput{
			ID:                es.ID,
			Hostname:          es.Hostname,
			LastSeen:          es.LastSeen,
			ConvergenceStatus: es.ConvergenceStatus,
			Traits:            es.Traits,
			ObservedCycle:     es.ObservedCycle,
		})
	}

	fr := risk.ScoreFleet(inputs, baseInterval, offlineThreshold, 0)

	m.RiskAvgScore.Set(fr.AvgScore)
	m.RiskMaxScore.Set(float64(fr.MaxScore))
	m.RiskDistribution.WithLabelValues("low").Set(float64(fr.Distribution[risk.LevelLow]))
	m.RiskDistribution.WithLabelValues("medium").Set(float64(fr.Distribution[risk.LevelMedium]))
	m.RiskDistribution.WithLabelValues("high").Set(float64(fr.Distribution[risk.LevelHigh]))
	m.RiskDistribution.WithLabelValues("critical").Set(float64(fr.Distribution[risk.LevelCritical]))
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
