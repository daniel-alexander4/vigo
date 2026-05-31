// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"git.alexander4.io/vigo/server/convergence"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/freshness"
	"git.alexander4.io/vigo/server/integrations"
	"git.alexander4.io/vigo/server/risk"
	"git.alexander4.io/vigo/server/swarm/curatormesh"
	"git.alexander4.io/vigo/server/traits"
)

// persistConvergenceSnapshot writes the current convergence counters to
// the compliance_history table for long-term storage (survives server
// restarts).
func (s *Server) persistConvergenceSnapshot(ctx context.Context) {
	summary := s.fleetIndex.ConvergenceSummary()
	if summary.Total == 0 {
		return
	}
	if err := s.queries.UpsertConvergenceSnapshot(ctx, db.UpsertConvergenceSnapshotParams{
		Total:     int64(summary.Total),
		Converged: int64(summary.Converged),
		Degraded:  int64(summary.Degraded),
		Failed:    int64(summary.Failed),
		NoData:    int64(summary.NoData),
		Changed:   int64(summary.Changed),
		Diverged:  int64(summary.Diverged),
		Offline:   int64(summary.Offline),
	}); err != nil {
		slog.Error("failed to persist compliance snapshot", "error", err)
	}
}

// persistRiskSnapshot computes the fleet-wide risk score and writes a
// daily snapshot to the risk_posture_history table.
func (s *Server) persistRiskSnapshot(ctx context.Context) {
	allEnvoys := s.fleetIndex.AllEnvoys()
	if len(allEnvoys) == 0 {
		return
	}
	inputs := make([]risk.EnvoyInput, len(allEnvoys))
	for i, es := range allEnvoys {
		inputs[i] = risk.EnvoyInput{
			ID:                es.ID,
			Hostname:          es.Hostname,
			Revoked:           es.Revoked,
			LastSeen:          es.LastSeen,
			ConvergenceStatus: es.ConvergenceStatus,
			Traits:            es.Traits,
			ObservedCycle:     s.fleetIndex.EnvoyCycle(es.ID),
		}
	}
	fr := risk.ScoreFleet(inputs, s.baseInterval, s.offlineThreshold, 0)
	if err := s.queries.UpsertRiskPostureSnapshot(ctx, db.UpsertRiskPostureSnapshotParams{
		AvgScore:     int64(fr.AvgScore),
		MaxScore:     int64(fr.MaxScore),
		DistLow:      int64(fr.Distribution[risk.LevelLow]),
		DistMedium:   int64(fr.Distribution[risk.LevelMedium]),
		DistHigh:     int64(fr.Distribution[risk.LevelHigh]),
		DistCritical: int64(fr.Distribution[risk.LevelCritical]),
		ScoredEnvoys: int64(fr.ScoredEnvoys),
		TotalEnvoys:  int64(fr.TotalEnvoys),
	}); err != nil {
		slog.Error("failed to persist risk snapshot", "error", err)
	}
}

// computeAndStoreMTTR scans yesterday's failed/partial runs, finds their
// resolutions, and writes aggregate MTTR stats. Runs daily off the hot
// path.
func (s *Server) computeAndStoreMTTR(ctx context.Context) {
	yesterdayTime := time.Now().AddDate(0, 0, -1)
	yesterday := yesterdayTime.Format("2006-01-02")

	failed, err := s.queries.ListFailedRunsForDate(ctx, yesterdayTime)
	if err != nil {
		slog.Error("mttr: failed to list failed runs", "date", yesterday, "error", err)
		return
	}

	var durations []int64
	resolved := 0
	open := 0

	for _, f := range failed {
		next, err := s.queries.GetNextSuccessfulRun(ctx, db.GetNextSuccessfulRunParams{
			EnvoyID:   f.EnvoyID,
			StartedAt: f.StartedAt,
		})
		if err != nil {
			open++
			continue
		}
		dur := next.StartedAt.Sub(f.StartedAt).Seconds()
		durations = append(durations, int64(dur))
		resolved++
	}

	var avgSec, p50Sec, p95Sec int64
	if len(durations) > 0 {
		sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
		var sum int64
		for _, d := range durations {
			sum += d
		}
		avgSec = sum / int64(len(durations))
		p50Sec = durations[len(durations)/2]
		p95idx := int(float64(len(durations)) * 0.95)
		if p95idx >= len(durations) {
			p95idx = len(durations) - 1
		}
		p95Sec = durations[p95idx]
	}

	if err := s.queries.UpsertMTTRStats(ctx, db.UpsertMTTRStatsParams{
		RecordedAt:     yesterday,
		TotalIncidents: int64(len(failed)),
		Resolved:       int64(resolved),
		MttrSeconds:    avgSec,
		P50Seconds:     p50Sec,
		P95Seconds:     p95Sec,
		OpenIncidents:  int64(open),
	}); err != nil {
		slog.Error("mttr: failed to persist stats", "date", yesterday, "error", err)
	} else if len(failed) > 0 {
		slog.Info("mttr stats computed", "date", yesterday, "incidents", len(failed), "resolved", resolved, "mttr_avg_s", avgSec)
	}
}

// runFrameworkDriftAlerter periodically scans recent drift events and
// sends enriched notifications that include the affected framework
// controls. Runs off the hot path — the check-in handler sends basic
// drift.detected; this job adds framework context asynchronously.
func (s *Server) runFrameworkDriftAlerter(ctx context.Context) {
	integ := s.integrations
	if integ == nil || s.configStore == nil {
		return
	}

	// Track which runs we've already alerted on.
	lastChecked := time.Now()

	alertInterval := s.baseInterval * 2
	if alertInterval < time.Minute {
		alertInterval = time.Minute
	}

	ticker := time.NewTicker(alertInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Find runs with changes since last check.
			runs, err := s.queries.ListAllRuns(ctx, 50)
			if err != nil {
				continue
			}

			envoyMap := make(map[string]string) // envoy ID → hostname
			if s.fleetIndex != nil {
				for _, ts := range s.fleetIndex.AllEnvoys() {
					envoyMap[ts.ID] = ts.Hostname
				}
			}

			for _, r := range runs {
				if r.StartedAt.Before(lastChecked) {
					break
				}
				if r.ChangedCount == 0 {
					continue
				}

				hostname := envoyMap[r.EnvoyID]
				if hostname == "" {
					continue
				}

				// Look up which frameworks/controls are affected.
				trace := s.configStore.Trace(hostname)
				if !trace.Matched || len(trace.ComplianceTags) == 0 {
					continue
				}

				var affected []string
				for _, ct := range trace.ComplianceTags {
					for fw, ids := range ct.Frameworks {
						for _, id := range ids {
							affected = append(affected, fw+": "+id)
						}
					}
				}

				if len(affected) == 0 {
					continue
				}

				// Truncate to avoid huge emails.
				display := strings.Join(affected, ", ")
				if len(display) > 200 {
					display = display[:200] + "..."
				}

				integ.Dispatch(integrations.Event{
					Name:      "convergence.drift",
					Timestamp: r.StartedAt,
					Hostname:  hostname,
					EnvoyID:   r.EnvoyID,
					Summary:   fmt.Sprintf("drift on %s affected %s", hostname, display),
					Details: map[string]any{
						"run_id":            r.ID,
						"changed":           r.ChangedCount,
						"affected_controls": display,
					},
				})
			}

			lastChecked = time.Now()
		}
	}
}

// runStaleChecker fires a envoy.stale notification when a envoy
// transitions from non-stale to stale. The "is stale" predicate lives in
// package freshness (2.5× the base interval) and is shared with the UI,
// doctor, and risk scorer. The checker runs at the base interval
// frequency. It always updates the FleetIndex offline flag; SMTP
// notifications are optional.
func (s *Server) runStaleChecker(ctx context.Context) {
	integ := s.integrations
	wasStale := make(map[string]bool) // envoy ID → was stale last tick
	canNotify := integ != nil

	ticker := time.NewTicker(s.baseInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Use FleetIndex when available (zero DB queries).
			if s.fleetIndex != nil {
				allEnvoys := s.fleetIndex.AllEnvoys()
				nowStale := make(map[string]bool, len(wasStale))
				for _, ts := range allEnvoys {
					if ts.Revoked || ts.LastSeen.IsZero() {
						continue
					}
					stale := freshness.IsStaleFromCycle(ts.LastSeen, s.baseInterval, s.fleetIndex.EnvoyCycle(ts.ID), s.offlineThreshold)
					s.fleetIndex.SetOffline(ts.ID, stale)
					if !stale {
						continue
					}
					nowStale[ts.ID] = true
					if wasStale[ts.ID] {
						continue
					}
					if canNotify {
						integ.Dispatch(integrations.Event{
							Name:      "envoy.stale",
							Timestamp: time.Now().UTC(),
							Hostname:  ts.Hostname,
							EnvoyID:   ts.ID,
							Summary:   fmt.Sprintf("%s stale since %s", ts.Hostname, ts.LastSeen.Format("15:04 UTC")),
							Details:   map[string]any{"last_seen": ts.LastSeen.Format("2006-01-02 15:04:05 UTC")},
						})
						integ.Dispatch(integrations.Event{
							Name:      "envoy.offline",
							Timestamp: time.Now().UTC(),
							Hostname:  ts.Hostname,
							EnvoyID:   ts.ID,
							Summary:   fmt.Sprintf("%s went offline", ts.Hostname),
							Details:   map[string]any{"last_seen": ts.LastSeen.Format("2006-01-02 15:04:05 UTC")},
						})
					}
				}
				// Detect envoys that recovered (were stale, now not).
				if canNotify {
					for id := range wasStale {
						if !nowStale[id] {
							if ts, ok, _ := s.fleetIndex.GetEnvoyLite(id); ok {
								integ.Dispatch(integrations.Event{
									Name:      "envoy.online",
									Timestamp: time.Now().UTC(),
									Hostname:  ts.Hostname,
									EnvoyID:   id,
									Summary:   fmt.Sprintf("%s back online", ts.Hostname),
								})
							}
						}
					}
				}
				wasStale = nowStale
				continue
			}

			envoys, err := s.queries.ListEnvoys(ctx)
			if err != nil {
				slog.Error("stale checker: listing envoys", "error", err)
				continue
			}

			nowStale := make(map[string]bool, len(wasStale))
			for _, t := range envoys {
				if t.Revoked != 0 {
					continue
				}
				if !t.LastSeen.Valid {
					continue
				}
				// DB-only fallback path (fleetIdx unavailable): no observed cycle
				// data, so the threshold is derived from configured interval alone.
				if !freshness.IsStaleFromCycle(t.LastSeen.Time, s.baseInterval, 0, s.offlineThreshold) {
					continue
				}

				nowStale[t.ID] = true
				if wasStale[t.ID] {
					continue // already notified
				}

				if canNotify {
					integ.Dispatch(integrations.Event{
						Name:      "envoy.stale",
						Timestamp: time.Now().UTC(),
						Hostname:  t.Hostname,
						EnvoyID:   t.ID,
						Summary:   fmt.Sprintf("%s stale since %s", t.Hostname, t.LastSeen.Time.Format("15:04 UTC")),
						Details:   map[string]any{"last_seen": t.LastSeen.Time.Format("2006-01-02 15:04:05 UTC")},
					})
				}
			}
			wasStale = nowStale
		}
	}
}

// runComplianceChecker periodically computes the convergence percentage
// across all active envoys and fires a convergence.threshold SMTP
// notification when it drops below the configured threshold. Only fires
// once per transition.
func (s *Server) runComplianceChecker(ctx context.Context, threshold int) {
	integ := s.integrations
	if integ == nil || threshold <= 0 {
		return
	}

	slog.Info("compliance threshold checker started", "threshold", threshold)

	wasBelowThreshold := false
	ticker := time.NewTicker(s.baseInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Use FleetIndex when available (O(1) compliance summary).
			if s.fleetIndex != nil {
				summary := s.fleetIndex.ConvergenceSummary()
				total := summary.Total
				if total == 0 {
					continue
				}
				pct := (summary.Converged * 100) / total
				belowNow := pct < threshold

				if belowNow && !wasBelowThreshold {
					integ.Dispatch(integrations.Event{
						Name:      "convergence.threshold",
						Timestamp: time.Now().UTC(),
						Summary:   fmt.Sprintf("convergence %d%% below threshold %d%%", pct, threshold),
						Details: map[string]any{
							"converged_pct": pct,
							"threshold":     threshold,
							"converged":     summary.Converged,
							"total":         total,
						},
					})
				}
				wasBelowThreshold = belowNow
				continue
			}

			envoys, err := s.queries.ListEnvoys(ctx)
			if err != nil {
				slog.Error("compliance checker: listing envoys", "error", err)
				continue
			}

			// Collect active envoy IDs.
			var ids []string
			envoyMap := make(map[string]db.Envoy)
			for _, t := range envoys {
				if t.Revoked != 0 {
					continue
				}
				ids = append(ids, t.ID)
				envoyMap[t.ID] = t
			}
			if len(ids) == 0 {
				continue
			}

			latestRuns, err := s.runStore.LatestRunPerEnvoy(ctx, ids)
			if err != nil {
				slog.Error("compliance checker: fetching latest runs", "error", err)
				continue
			}

			compliantCount := 0
			for _, id := range ids {
				run, ok := latestRuns[id]
				if !ok {
					continue // no run data → not converged
				}
				ri := convergence.RunInfo{
					Status:       run.Status,
					ChangedCount: int64(run.ChangedCount),
					FailedCount:  int64(run.FailedCount),
					ConfigcrateCount:  int64(run.ConfigcrateCount),
				}
				if convergence.ComputeStatus(ri) == convergence.StatusConverged {
					compliantCount++
				}
			}

			pct := (compliantCount * 100) / len(ids)
			belowNow := pct < threshold

			if belowNow && !wasBelowThreshold {
				integ.Dispatch(integrations.Event{
					Name:      "convergence.threshold",
					Timestamp: time.Now().UTC(),
					Summary:   fmt.Sprintf("convergence %d%% below threshold %d%%", pct, threshold),
					Details: map[string]any{
						"converged_pct": pct,
						"threshold":     threshold,
						"converged":     compliantCount,
						"total":         len(ids),
					},
				})
			}
			wasBelowThreshold = belowNow
		}
	}
}

// runSecurityPostureSnapshot takes a daily security posture snapshot.
// All CVE counts are unique/deduplicated — read directly from agent
// traits. Runs once at startup and then every 24 hours.
func (s *Server) runSecurityPostureSnapshot(ctx context.Context) {
	takeSnapshot := func() {
		envoys := s.fleetIndex.AllEnvoys()
		var critical, high, medium, low, hardening, integrity, rootkit, malware, scanned, total int64
		for _, e := range envoys {
			if e.Revoked {
				continue
			}
			total++
			if e.Traits == nil {
				continue
			}
			hasSecScan := false
			for k, v := range e.Traits {
				var n int64
				fmt.Sscanf(v, "%d", &n)
				switch k {
				case traits.UniqueCritical:
					critical += n
					hasSecScan = true
				case traits.UniqueHigh:
					high += n
				case traits.UniqueMedium:
					medium += n
				case traits.UniqueLow:
					low += n
				case traits.LynisScore:
					hardening += n
				case traits.AideChanged:
					if n > 0 {
						integrity++
					}
				case traits.RkhunterWarnings:
					if n > 0 {
						rootkit++
					}
				case traits.ChkrootkitInfected:
					if n > 0 {
						rootkit++
					}
				case traits.ClamavInfected:
					if n > 0 {
						malware++
					}
				}
			}
			if hasSecScan {
				scanned++
			}
		}
		avgHardening := int64(0)
		if scanned > 0 {
			avgHardening = hardening / scanned
		}

		if err := s.queries.InsertSecurityPostureSnapshot(ctx, db.InsertSecurityPostureSnapshotParams{
			CveCritical:        critical,
			CveHigh:            high,
			CveMedium:          medium,
			CveLow:             low,
			AvgHardening:       avgHardening,
			IntegrityIncidents: integrity,
			RootkitWarnings:    rootkit,
			MalwareDetections:  malware,
			ScannedHosts:       scanned,
			TotalHosts:         total,
		}); err != nil {
			slog.Error("security posture snapshot failed", "error", err)
		} else {
			slog.Info("security posture snapshot recorded",
				"unique_cve_critical", critical, "unique_cve_high", high, "scanned", scanned, "total", total)
		}
	}

	takeSnapshot()

	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			takeSnapshot()
		}
	}
}

// runAuditReverify warms and periodically re-anchors the audit-chain verifier.
// The audit-status cache (server/api/report.go) verifies incrementally from an
// in-memory checkpoint; this loop runs a full-chain VerifyFull at boot — which
// re-checks the whole chain after any downtime, when offline tampering is
// possible — and once a day thereafter, catching tampering below the current
// checkpoint. Off the hot path; a full walk is seconds even at fleet scale.
func (s *Server) runAuditReverify(ctx context.Context) {
	if s.reportHandler == nil {
		return
	}
	v := s.reportHandler.AuditVerifier()
	if v == nil {
		return
	}
	reverify := func() {
		vctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		switch res, err := v.VerifyFull(vctx); {
		case err != nil:
			slog.Warn("audit re-verify failed", "error", err)
		case !res.Valid:
			slog.Error("audit chain verification FAILED",
				"first_broken", res.FirstBroken, "entries", res.EntryCount, "detail", res.Error)
		default:
			slog.Info("audit chain re-verified", "entries", res.EntryCount)
		}
	}

	reverify() // boot warm-up + post-downtime full re-verify
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reverify()
		}
	}
}

// Compliance snapshotting now lives on *api.ReportHandler.PersistComplianceSnapshot
// — the natural home given it depends only on ReportHandler's existing
// configStore + queries + ComputeFrameworkScoreCtx. Server's daily
// snapshotter loop dispatches through that method; ConfigHandler does
// the same on every successful config publish.

// runCuratorFeed periodically (every `interval`, with a first pass after
// a short delay) rebuilds the fleet artifact catalog (ADR-024) via
// curatormesh, reconciles its per-platform blobs into the authoritative
// `swarm.ManifestStore` — `RecordSeed` for blobs the catalog still
// references, `Revoke` for `curator:*` entries it no longer does — and
// hands the marshaled catalog to the agent service so it rides every
// CheckInResponse. Blocks until ctx is cancelled.
func (s *Server) runCuratorFeed(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	select {
	case <-ctx.Done():
		return
	case <-time.After(20 * time.Second): // let the fleet index rehydrate first
		s.curatorFeedOnce(ctx)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.curatorFeedOnce(ctx)
		}
	}
}

func (s *Server) curatorFeedOnce(ctx context.Context) {
	var inputs []curatormesh.EnvoyInput
	if s.fleetIndex != nil {
		envoys := s.fleetIndex.AllEnvoys()
		inputs = make([]curatormesh.EnvoyInput, 0, len(envoys))
		for _, e := range envoys {
			inputs = append(inputs, curatormesh.EnvoyInput{ID: e.ID, Hostname: e.Hostname, Revoked: e.Revoked})
		}
	}
	cat, err := s.curatorAgg.Snapshot(ctx, inputs)
	if err != nil {
		slog.Warn("curator feed: catalog snapshot failed", "error", err)
		return
	}
	desired := curatormesh.ManifestBlobs(cat)
	present := make(map[string]struct{}, len(desired))
	for _, b := range desired {
		present[b.SHA256] = struct{}{}
		s.manifestStore.RecordSeed(b.SHA256, b.Label, b.Target, "", "curator-catalog", b.Size)
	}
	for _, e := range s.manifestStore.Entries() {
		if e.Status == "active" && strings.HasPrefix(e.Label, curatormesh.ManifestLabelPrefix) {
			if _, ok := present[e.SHA256]; !ok {
				s.manifestStore.Revoke(e.SHA256)
			}
		}
	}
	if b, err := json.Marshal(cat.Artifacts); err == nil {
		s.agentSvc.SetCuratorCatalog(b)
	} else {
		slog.Warn("curator feed: marshal catalog failed", "error", err)
	}
}
