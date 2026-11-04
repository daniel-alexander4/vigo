// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

// Package grc pushes compliance evidence to external GRC platforms
// (Vanta, Drata, ServiceNow GRC, Archer) on a configurable schedule.
package grc

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"git.alexander4.io/vigo/server/api"
	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/daemon"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/httpsec"
)

// FrameworkScorer is the canonical compliance scorer. Satisfied by
// *api.ReportHandler.
type FrameworkScorer interface {
	ComputeFrameworkScoreCtx(ctx context.Context, slug string) (*api.FrameworkScore, bool)
}

// Pusher periodically pushes compliance evidence to configured GRC platforms.
type Pusher struct {
	integrations []config.GRCIntegration
	fleetIndex   *fleet.FleetIndex
	scorer       FrameworkScorer
	client       *http.Client
}

// NewPusher creates a GRC pusher. scorer may be nil (frameworks are then
// omitted from the payload).
func NewPusher(integrations []config.GRCIntegration, fleetIndex *fleet.FleetIndex, scorer FrameworkScorer) *Pusher {
	var enabled []config.GRCIntegration
	for _, i := range integrations {
		if i.Enabled {
			enabled = append(enabled, i)
		}
	}
	return &Pusher{
		integrations: enabled,
		fleetIndex:   fleetIndex,
		scorer:       scorer,
		client:       &http.Client{Timeout: 30 * time.Second},
	}
}

// Start runs the push loop for each integration. Blocks until ctx is cancelled.
func (p *Pusher) Start(ctx context.Context) {
	if len(p.integrations) == 0 {
		return
	}

	for _, intg := range p.integrations {
		interval, err := time.ParseDuration(intg.PushInterval)
		if err != nil || interval < time.Minute {
			interval = 6 * time.Hour
		}
		slog.Info("GRC integration started", "name", intg.Name, "host", httpsec.URLHost(intg.Endpoint), "interval", interval)
		go p.runLoop(ctx, intg, interval)
	}

	<-ctx.Done()
}

func (p *Pusher) runLoop(ctx context.Context, intg config.GRCIntegration, interval time.Duration) {
	// runLoop runs in its own goroutine, spawned per integration by Start —
	// outside the SafeGo recover that wraps Start itself. Guard each push so a
	// panic in one integration's evidence build/transport is recovered and that
	// integration retries next tick, rather than crashing the whole process.
	name := "grc-push:" + intg.Name

	// Push immediately on startup.
	daemon.Guard(name, func() { p.pushEvidence(ctx, intg) })

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			daemon.Guard(name, func() { p.pushEvidence(ctx, intg) })
		}
	}
}

// EvidencePayload is the common evidence structure pushed to GRC platforms.
// Platform adapters in adapters.go transform this into platform-specific formats.
type EvidencePayload struct {
	Timestamp  time.Time           `json:"timestamp"`
	Source     string              `json:"source"`
	Fleet      FleetSummary        `json:"fleet"`
	Frameworks []FrameworkEvidence `json:"frameworks"`
	AuditChain AuditStatus         `json:"audit_chain"`
}

type FleetSummary struct {
	Total     int `json:"total"`
	Converged int `json:"converged"`
	Degraded  int `json:"degraded"`
	Failed    int `json:"failed"`
	Diverged  int `json:"diverged"` // drift axis: 5+ consecutive runs with changes
}

type AuditStatus struct {
	Valid      bool  `json:"valid"`
	EntryCount int64 `json:"entry_count"`
}

// FrameworkEvidence is per-standard compliance evidence.
type FrameworkEvidence struct {
	Standard        string          `json:"standard"`
	GRCFrameworkID  string          `json:"grc_framework_id,omitempty"`
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	CoveragePercent float64         `json:"coverage_percent"`
	TotalControls   int             `json:"total_controls"`
	Satisfied       int             `json:"satisfied"`
	Waived          int             `json:"waived"`
	Partial         int             `json:"partial"`
	NotSatisfied    int             `json:"not_satisfied"`
	EnvoyCount      int             `json:"envoy_count"`
	Controls        []ControlStatus `json:"controls,omitempty"`
}

// ControlStatus is a single control's compliance status.
type ControlStatus struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Evidence string `json:"evidence"`
}

// buildPayload assembles the evidence for one integration. Every framework
// number comes from the canonical compliance scorer (api.ReportHandler's
// ComputeFrameworkScoreCtx — the same SoT behind /compliance and the framework
// reports), so waived and partial controls are counted the way the report
// counts them.
func (p *Pusher) buildPayload(ctx context.Context, intg config.GRCIntegration) *EvidencePayload {
	summary := p.fleetIndex.ConvergenceSummary()
	payload := &EvidencePayload{
		Timestamp: time.Now().UTC(),
		Source:    "vigo",
		// Failure axis (Converged/Degraded/Failed) plus drift (Diverged:
		// envoys with persistent change loops). Total is non-revoked.
		Fleet: FleetSummary{
			Total:     summary.Total,
			Converged: summary.Converged,
			Degraded:  summary.Degraded,
			Failed:    summary.Failed,
			Diverged:  summary.Diverged,
		},
	}
	if p.scorer == nil {
		return payload
	}

	keys := make([]string, 0, len(intg.Frameworks))
	for k := range intg.Frameworks {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, standardKey := range keys {
		grcID := intg.Frameworks[standardKey]
		slug, ok := config.SlugForStandardKey[standardKey]
		if !ok {
			slog.Warn("GRC push: unknown framework key, skipped", "name", intg.Name, "framework", standardKey)
			continue
		}
		score, ok := p.scorer.ComputeFrameworkScoreCtx(ctx, slug)
		if !ok || score.Fleet.ActiveEnvoys == 0 {
			continue
		}
		payload.Frameworks = append(payload.Frameworks, frameworkEvidence(standardKey, grcID, score))
	}
	return payload
}

// frameworkEvidence maps a canonical framework score onto the GRC payload.
// The summary buckets are the scorer's fleet aggregate verbatim. Each
// control's status is its partition across the active fleet: satisfied when
// every envoy covers it, waived when every envoy has it waived,
// not_satisfied when no envoy covers, waives or partially meets it, and
// partial otherwise.
func frameworkEvidence(standardKey, grcID string, score *api.FrameworkScore) FrameworkEvidence {
	agg := score.Fleet
	fe := FrameworkEvidence{
		Standard:        standardKey,
		GRCFrameworkID:  grcID,
		ID:              grcID,
		Name:            score.Name,
		CoveragePercent: agg.CoveragePercent,
		TotalControls:   agg.Total,
		Satisfied:       agg.Covered,
		Waived:          agg.Waived,
		Partial:         agg.Partial,
		NotSatisfied:    agg.Missing,
		EnvoyCount:      agg.ActiveEnvoys,
	}
	n := agg.ActiveEnvoys
	for _, f := range score.Controls {
		pc := score.PerControl[f.ControlID]
		status := "partial"
		switch {
		case pc.Covered == n:
			status = "satisfied"
		case pc.Waived == n:
			status = "waived"
		case pc.Missing == n:
			status = "not_satisfied"
		}
		fe.Controls = append(fe.Controls, ControlStatus{
			ID:       f.ControlID,
			Title:    f.Title,
			Status:   status,
			Evidence: fmt.Sprintf("%s (covered on %d of %d envoys, waived on %d, partial on %d)", f.Evidence, pc.Covered, n, pc.Waived, pc.Partial),
		})
	}
	return fe
}

func (p *Pusher) pushEvidence(ctx context.Context, intg config.GRCIntegration) {
	payload := p.buildPayload(ctx, intg)

	// Format using platform-specific adapter.
	adapter := GetAdapter(intg.Name)
	body, err := adapter.Format(payload)
	if err != nil {
		slog.Error("GRC push: format failed", "name", intg.Name, "error", err)
		return
	}

	req, err := http.NewRequestWithContext(ctx, "POST", intg.Endpoint, bytes.NewReader(body))
	if err != nil {
		slog.Error("GRC push: request creation failed", "name", intg.Name, "error", httpsec.RedactURLError(err))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", intg.APIKey))

	resp, err := p.client.Do(req)
	if err != nil {
		slog.Error("GRC push: request failed", "name", intg.Name, "error", httpsec.RedactURLError(err))
		return
	}
	resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		slog.Info("GRC push: success", "name", intg.Name, "frameworks", len(payload.Frameworks))
	} else {
		slog.Error("GRC push: non-success status", "name", intg.Name, "status", resp.StatusCode)
	}
}

// PushNow triggers an immediate push to all integrations.
func (p *Pusher) PushNow(ctx context.Context) {
	for _, intg := range p.integrations {
		p.pushEvidence(ctx, intg)
	}
}
