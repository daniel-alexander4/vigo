// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"git.alexander4.io/vigo/server/convergence"
	"git.alexander4.io/vigo/server/daemon"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/freshness"
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
	Total     int // non-revoked fleet size
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

// SwarmSnapshot holds fleet-total counts for the swarm substrate, the
// content subsystems, and the spanner roster. Every value is a
// fleet-wide total — no per-entity labels (cardinality, and /metrics is
// a public endpoint, so no user/project/host identifiers).
type SwarmSnapshot struct {
	SubstrateEnvoys int   // envoys participating in the blob substrate
	ManifestActive  int   // active seed-manifest entries
	FootprintBytes  int64 // total cached blob bytes fleet-wide

	GitbackProjects      int
	GitbackEnvoys        int
	GitbackDivergentRefs int

	CuratorArtifacts  int
	CuratorPublishers int

	LockboxUsers           int
	LockboxFiles           int
	LockboxDivergentFiles  int
	LockboxFailedFiles     int
	LockboxRecipientDrifts int

	LongdrawerUsers          int
	LongdrawerFiles          int
	LongdrawerDivergentFiles int

	SpannerBoltsActive  int
	SpannerBoltsRevoked int
}

// SwarmSource provides fleet-total swarm + spanner counts for the
// metrics collector. Declared here as an interface so this package
// doesn't import the swarm/spanner packages; implemented by an adapter
// in main.go. May be nil (the collector then skips the swarm gauges).
type SwarmSource interface {
	SwarmSnapshot(ctx context.Context) SwarmSnapshot
}

// ConfigcrateSource lists the loaded configcrate names (config.ConfigStore).
// May be nil; the drift metric then counts every configcrate as "other".
type ConfigcrateSource interface {
	ListConfigcrateNames() []string
}

// swarmRefreshInterval gates the swarm/spanner gauge refresh. The
// content-subsystem aggregators rebuild from per-envoy DB traits, and
// their own 30s cache never helps a poller whose cadence equals the
// TTL — so the refresh is deliberately decoupled from the (faster)
// gauge tick. Swarm counts change slowly; 5 minutes stays plenty fresh.
const swarmRefreshInterval = 5 * time.Minute

// Collector periodically queries the database and fleet to update gauge metrics.
// Runs in a background goroutine — never on the request hot path.
type Collector struct {
	sqlDB            *sql.DB
	fleetSource      FleetSource
	swarmSource      SwarmSource
	configcrates     ConfigcrateSource
	metrics          *Metrics
	baseInterval     time.Duration
	offlineThreshold time.Duration

	// lastSwarmRefresh gates the swarm/spanner gauge refresh to
	// swarmRefreshInterval. Mutated only from the single Run goroutine.
	lastSwarmRefresh time.Time
}

// NewCollector creates a new metrics collector. fleetSource and swarmSource
// may be nil (tests / swarm disabled); a nil fleetSource leaves the envoy
// and convergence gauges at zero. The Metrics value is
// the per-instance collector set; the gauges it owns are the ones this
// collector mutates each tick.
func NewCollector(sqlDB *sql.DB, fleetSource FleetSource, swarmSource SwarmSource, configcrates ConfigcrateSource, m *Metrics, baseInterval, offlineThreshold time.Duration) *Collector {
	return &Collector{
		sqlDB:            sqlDB,
		fleetSource:      fleetSource,
		swarmSource:      swarmSource,
		configcrates:     configcrates,
		metrics:          m,
		baseInterval:     baseInterval,
		offlineThreshold: offlineThreshold,
	}
}

// UpdateGauges queries the database and fleet and updates all gauge metrics.
func (c *Collector) UpdateGauges(ctx context.Context) error {
	// Update database size metric.
	if c.sqlDB != nil {
		if size, err := db.DatabaseSizeBytes(c.sqlDB); err == nil {
			c.metrics.DatabaseSizeBytes.Set(float64(size))
		}
	}

	// Fleet-backed metrics (envoy counts, check-in age, convergence
	// breakdown, security, risk). The FleetIndex is the authoritative
	// fleet state, so nothing here re-reads the envoys table.
	if c.fleetSource != nil {
		// Convergence breakdown — O(1) from pre-computed counters.
		summary := c.fleetSource.ConvergenceSummary()
		c.metrics.EnvoysTotal.Set(float64(summary.Total))
		c.metrics.ConvergenceConverged.Set(float64(summary.Converged))
		c.metrics.ConvergenceDegraded.Set(float64(summary.Degraded))
		c.metrics.ConvergenceFailed.Set(float64(summary.Failed))
		c.metrics.ConvergenceNoData.Set(float64(summary.NoData))
		c.metrics.ConvergenceChanged.Set(float64(summary.Changed))
		c.metrics.ConvergenceDiverged.Set(float64(summary.Diverged))
		c.metrics.ConvergenceOffline.Set(float64(summary.Offline))

		// Fleet-wide convergence fraction (0.0-1.0): the convergence SoT
		// helper over its active denominator (Total - Offline), scaled from
		// the helper's 0-100 to this gauge's 0-1 — the same figure the
		// /convergence page shows.
		c.metrics.ConvergencePct.Set(convergence.ConvergedPct(int64(summary.Converged), int64(summary.Total)-int64(summary.Offline)) / 100)

		// Single snapshot — one RLock, reused for activity, check-in age,
		// security and risk.
		allEnvoys := c.fleetSource.AllEnvoySnapshots()
		updateActivityMetrics(c.metrics, allEnvoys, time.Now(), c.baseInterval, c.offlineThreshold)
		updateSecurityMetrics(c.metrics, allEnvoys)
		updateRiskMetrics(c.metrics, allEnvoys, c.baseInterval, c.offlineThreshold)
	}

	// Refresh the configcrate set that bounds the drift metric's label.
	if c.configcrates != nil {
		c.metrics.SetKnownConfigcrates(c.configcrates.ListConfigcrateNames())
	}

	// Swarm + spanner gauges. The content-subsystem aggregators rebuild
	// from per-envoy DB traits, so the refresh is gated to
	// swarmRefreshInterval rather than run on every (faster) gauge tick.
	// The zero-value lastSwarmRefresh makes the first tick after boot
	// refresh.
	if c.swarmSource != nil && time.Since(c.lastSwarmRefresh) >= swarmRefreshInterval {
		updateSwarmMetrics(c.metrics, c.swarmSource.SwarmSnapshot(ctx))
		c.lastSwarmRefresh = time.Now()
	}

	return nil
}

// updateActivityMetrics sets the active-envoy gauge and the check-in age
// snapshot. "Active" is the canonical freshness predicate (not stale for the
// envoy's own cycle — the rule the UI envoys page and the stale notifier
// use), not a fixed one-hour window. Revoked and never-seen envoys are
// excluded from both. The age distribution is a snapshot of the fleet now,
// replaced every tick, never accumulated across ticks.
func updateActivityMetrics(m *Metrics, envoys []EnvoySnapshot, now time.Time, baseInterval, offlineThreshold time.Duration) {
	var active int
	ages := make([]float64, 0, len(envoys))
	for _, es := range envoys {
		if es.Revoked || es.LastSeen.IsZero() {
			continue
		}
		if !freshness.IsStaleFromCycle(es.LastSeen, baseInterval, es.ObservedCycle, offlineThreshold) {
			active++
		}
		ages = append(ages, now.Sub(es.LastSeen).Seconds())
	}
	m.EnvoysActive.Set(float64(active))
	m.CheckinAgeDist.Set(ages)
}

// updateSwarmMetrics sets the swarm-substrate, content-subsystem, and
// spanner gauges from a SwarmSnapshot. Pure assignment — no I/O.
func updateSwarmMetrics(m *Metrics, s SwarmSnapshot) {
	m.SwarmSubstrateEnvoys.Set(float64(s.SubstrateEnvoys))
	m.SwarmManifestActive.Set(float64(s.ManifestActive))
	m.SwarmFootprintBytes.Set(float64(s.FootprintBytes))

	m.GitbackProjects.Set(float64(s.GitbackProjects))
	m.GitbackEnvoys.Set(float64(s.GitbackEnvoys))
	m.GitbackDivergentRefs.Set(float64(s.GitbackDivergentRefs))

	m.CuratorArtifacts.Set(float64(s.CuratorArtifacts))
	m.CuratorPublishers.Set(float64(s.CuratorPublishers))

	m.LockboxUsers.Set(float64(s.LockboxUsers))
	m.LockboxFiles.Set(float64(s.LockboxFiles))
	m.LockboxDivergentFiles.Set(float64(s.LockboxDivergentFiles))
	m.LockboxFailedFiles.Set(float64(s.LockboxFailedFiles))
	m.LockboxRecipientDrifts.Set(float64(s.LockboxRecipientDrifts))

	m.LongdrawerUsers.Set(float64(s.LongdrawerUsers))
	m.LongdrawerFiles.Set(float64(s.LongdrawerFiles))
	m.LongdrawerDivergentFiles.Set(float64(s.LongdrawerDivergentFiles))

	m.SpannerBoltsActive.Set(float64(s.SpannerBoltsActive))
	m.SpannerBoltsRevoked.Set(float64(s.SpannerBoltsRevoked))
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
	daemon.Guard("metrics-collector", func() {
		if err := c.UpdateGauges(ctx); err != nil {
			slog.Error("metrics collector initial update failed", "error", err)
		}
	})

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			daemon.Guard("metrics-collector", func() {
				if err := c.UpdateGauges(ctx); err != nil {
					slog.Error("metrics collector update failed", "error", err)
				}
			})
		}
	}
}
