// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package grc pushes compliance evidence to external GRC platforms
// (Vanta, Drata, ServiceNow GRC, Archer) on a configurable schedule.
package grc

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/daniel-alexander4/vigo/server/config"
	"github.com/daniel-alexander4/vigo/server/fleet"
)

// Pusher periodically pushes compliance evidence to configured GRC platforms.
type Pusher struct {
	integrations []config.GRCIntegration
	fleetIndex   *fleet.FleetIndex
	configStore  *config.ConfigStore
	client       *http.Client
}

// NewPusher creates a GRC pusher.
func NewPusher(integrations []config.GRCIntegration, fleetIndex *fleet.FleetIndex, configStore *config.ConfigStore) *Pusher {
	var enabled []config.GRCIntegration
	for _, i := range integrations {
		if i.Enabled {
			enabled = append(enabled, i)
		}
	}
	return &Pusher{
		integrations: enabled,
		fleetIndex:   fleetIndex,
		configStore:  configStore,
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
		slog.Info("GRC integration started", "name", intg.Name, "endpoint", intg.Endpoint, "interval", interval)
		go p.runLoop(ctx, intg, interval)
	}

	<-ctx.Done()
}

func (p *Pusher) runLoop(ctx context.Context, intg config.GRCIntegration, interval time.Duration) {
	// Push immediately on startup.
	p.pushEvidence(ctx, intg)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.pushEvidence(ctx, intg)
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
	Relapsed  int `json:"relapsed"`
	Diverged  int `json:"diverged"`
	Errors    int `json:"errors"`
}

type AuditStatus struct {
	Valid      bool  `json:"valid"`
	EntryCount int64 `json:"entry_count"`
}

// FrameworkEvidence is per-standard compliance evidence.
type FrameworkEvidence struct {
	Standard        string           `json:"standard"`
	GRCFrameworkID  string           `json:"grc_framework_id,omitempty"`
	ID              string           `json:"id"`
	Name            string           `json:"name"`
	CoveragePercent float64          `json:"coverage_percent"`
	ScorePercent    float64          `json:"score_percent"`
	TotalControls   int              `json:"total_controls"`
	CoveredControls int              `json:"covered_controls"`
	MissingControls int              `json:"missing_controls"`
	Satisfied       int              `json:"satisfied"`
	Partial         int              `json:"partial"`
	NotSatisfied    int              `json:"not_satisfied"`
	EnvoyCount      int              `json:"envoy_count"`
	Controls        []ControlStatus  `json:"controls,omitempty"`
}

// ControlStatus is a single control's compliance status.
type ControlStatus struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Evidence string `json:"evidence"`
}

func (p *Pusher) pushEvidence(ctx context.Context, intg config.GRCIntegration) {
	envoys := p.fleetIndex.AllEnvoys()
	var activeHostnames []string
	for _, e := range envoys {
		if !e.Revoked {
			activeHostnames = append(activeHostnames, e.Hostname)
		}
	}

	payload := &EvidencePayload{
		Timestamp: time.Now().UTC(),
		Source:    "vigo",
		Fleet: FleetSummary{
			Total: len(activeHostnames),
		},
	}

	// Count converged/relapsed/diverged/errors from fleet index.
	summary := p.fleetIndex.ConvergenceSummary()
	payload.Fleet.Converged = summary.Converged
	payload.Fleet.Relapsed = summary.Relapsed
	payload.Fleet.Diverged = summary.Diverged
	payload.Fleet.Errors = summary.Errors

	// Compute per-standard coverage with control detail.
	for vigoKey, grcID := range intg.Frameworks {
		var totalCovered, totalMissing, totalControls, envoyCount int
		for _, hostname := range activeHostnames {
			cov := p.configStore.EnvoyStandardCoverage(hostname, vigoKey)
			if cov == nil || cov.TotalControls == 0 {
				continue
			}
			envoyCount++
			totalControls = cov.TotalControls
			totalCovered += len(cov.CoveredControls)
			totalMissing += len(cov.MissingControls)
		}
		if envoyCount == 0 {
			continue
		}
		avgCovered := float64(totalCovered) / float64(envoyCount)
		pct := avgCovered / float64(totalControls) * 100

		fe := FrameworkEvidence{
			Standard:        vigoKey,
			GRCFrameworkID:  grcID,
			ID:              grcID,
			Name:            vigoKey,
			CoveragePercent: pct,
			TotalControls:   totalControls,
			CoveredControls: int(avgCovered),
			MissingControls: int(float64(totalMissing) / float64(envoyCount)),
			EnvoyCount:      envoyCount,
			ScorePercent:    pct,
			Satisfied:       int(avgCovered),
			NotSatisfied:    totalControls - int(avgCovered),
		}
		payload.Frameworks = append(payload.Frameworks, fe)
	}

	// Format using platform-specific adapter.
	adapter := GetAdapter(intg.Name)
	body, err := adapter.Format(payload)
	if err != nil {
		slog.Error("GRC push: format failed", "name", intg.Name, "error", err)
		return
	}

	req, err := http.NewRequestWithContext(ctx, "POST", intg.Endpoint, bytes.NewReader(body))
	if err != nil {
		slog.Error("GRC push: request creation failed", "name", intg.Name, "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", intg.APIKey))

	resp, err := p.client.Do(req)
	if err != nil {
		slog.Error("GRC push: request failed", "name", intg.Name, "error", err)
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
