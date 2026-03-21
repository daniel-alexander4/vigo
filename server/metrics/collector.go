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

	"github.com/daniel-alexander4/vigo/server/db"
	"github.com/daniel-alexander4/vigo/server/risk"
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
}

// ConvergenceSnapshot holds pre-computed convergence counters.
type ConvergenceSnapshot struct {
	Converged      int
	RecurringDrift int
	Errors         int
	LostContact    int
	NoData         int
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
	queries      *db.Queries
	sqlDB        *sql.DB
	fleetSource  FleetSource
	baseInterval time.Duration
}

// NewCollector creates a new metrics collector. fleetSource may be nil (spoke mode).
func NewCollector(queries *db.Queries, sqlDB *sql.DB, fleetSource FleetSource, baseInterval time.Duration) *Collector {
	return &Collector{
		queries:      queries,
		sqlDB:        sqlDB,
		fleetSource:  fleetSource,
		baseInterval: baseInterval,
	}
}

// UpdateGauges queries the database and fleet and updates all gauge metrics.
func (c *Collector) UpdateGauges(ctx context.Context) error {
	envoys, err := c.queries.ListEnvoys(ctx)
	if err != nil {
		return err
	}

	// Single pass: compute all envoy metrics from DB.
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

	// Fleet-backed metrics (convergence breakdown, security, risk).
	if c.fleetSource != nil {
		// Convergence breakdown — O(1) from pre-computed counters.
		summary := c.fleetSource.ConvergenceSummary()
		ConvergenceConverged.Set(float64(summary.Converged))
		ConvergenceDrifted.Set(float64(summary.RecurringDrift))
		ConvergenceErrors.Set(float64(summary.Errors))
		ConvergenceLostContact.Set(float64(summary.LostContact))
		ConvergenceNoData.Set(float64(summary.NoData))

		// Single snapshot — one RLock, reused for security + risk.
		allEnvoys := c.fleetSource.AllEnvoySnapshots()
		updateSecurityMetrics(allEnvoys)
		updateRiskMetrics(allEnvoys, c.baseInterval)
	}

	return nil
}

// updateSecurityMetrics aggregates security_scan.* traits across all envoys.
// Pure iteration — no locks, no I/O.
func updateSecurityMetrics(envoys []EnvoySnapshot) {
	var critical, high, medium, low, hardening, rootkit, malware, integrity int64
	var scanned, hardeningCount int64

	for _, es := range envoys {
		if es.Revoked || es.Traits == nil {
			continue
		}
		hasScan := false
		for k, v := range es.Traits {
			if !strings.HasPrefix(k, "security_scan.") {
				continue
			}
			var n int64
			fmt.Sscanf(v, "%d", &n)
			switch k {
			case "security_scan.trivy.critical":
				critical += n
				hasScan = true
			case "security_scan.trivy.high":
				high += n
			case "security_scan.trivy.medium":
				medium += n
			case "security_scan.trivy.low":
				low += n
			case "security_scan.lynis.score":
				hardening += n
				hardeningCount++
				hasScan = true
			case "security_scan.chkrootkit.infected":
				rootkit += n
				hasScan = true
			case "security_scan.rkhunter.warnings":
				rootkit += n
				hasScan = true
			case "security_scan.clamav.infected":
				malware += n
				hasScan = true
			case "security_scan.aide.changed":
				integrity += n
				hasScan = true
			}
		}
		if hasScan {
			scanned++
		}
	}

	SecurityCVECritical.Set(float64(critical))
	SecurityCVEHigh.Set(float64(high))
	SecurityCVEMedium.Set(float64(medium))
	SecurityCVELow.Set(float64(low))
	SecurityRootkitWarnings.Set(float64(rootkit))
	SecurityMalwareDetections.Set(float64(malware))
	SecurityIntegrityIssues.Set(float64(integrity))
	SecurityScannedHosts.Set(float64(scanned))

	if hardeningCount > 0 {
		SecurityHardeningAvg.Set(float64(hardening) / float64(hardeningCount))
	} else {
		SecurityHardeningAvg.Set(0)
	}
}

// updateRiskMetrics computes fleet risk scores and sets Prometheus gauges.
// Pure arithmetic — no I/O, no locks.
func updateRiskMetrics(envoys []EnvoySnapshot, baseInterval time.Duration) {
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
		})
	}

	fr := risk.ScoreFleet(inputs, baseInterval, 0)

	RiskAvgScore.Set(fr.AvgScore)
	RiskMaxScore.Set(float64(fr.MaxScore))
	RiskDistribution.WithLabelValues("low").Set(float64(fr.Distribution[risk.LevelLow]))
	RiskDistribution.WithLabelValues("medium").Set(float64(fr.Distribution[risk.LevelMedium]))
	RiskDistribution.WithLabelValues("high").Set(float64(fr.Distribution[risk.LevelHigh]))
	RiskDistribution.WithLabelValues("critical").Set(float64(fr.Distribution[risk.LevelCritical]))
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
