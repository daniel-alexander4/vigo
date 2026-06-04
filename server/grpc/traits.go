// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/daemon"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/integrations"
	"git.alexander4.io/vigo/server/traits"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ReportTraits handles the VigoAgent.ReportTraits RPC. It stores the agent's
// collected traits as JSONB in the database and prunes old entries to enforce
// a 10-snapshot retention policy (DB-04).
func (s *AgentService) ReportTraits(ctx context.Context, req *pb.ReportTraitsRequest) (*pb.ReportTraitsResponse, error) {
	if _, err := uuid.Parse(req.GetEnvoyId()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid node_id: %v", err)
	}
	envoyID := req.GetEnvoyId()
	// Unlike CheckIn / ReportResult, this handler doesn't propagate ctx
	// downstream — traitsBatch.Mark, fleetIndex.UpdateTraits, and the
	// checkTraitTriggers goroutine are all sync in-memory operations
	// that don't take ctx. We only need the trace_id for the slog.Info
	// line below, so discard the trace-bearing ctx.
	_, traceID := EnsureTraceID(ctx, req.GetTraceparent())

	traitsJSON := req.GetTraitsJson()
	if len(traitsJSON) == 0 {
		return nil, status.Error(codes.InvalidArgument, "traits_json must not be empty")
	}

	// Validate that traits_json is valid JSON.
	if !json.Valid(traitsJSON) {
		return nil, status.Error(codes.InvalidArgument, "traits_json is not valid JSON")
	}

	// Validate trait count and individual value sizes (SEC-13).
	if err := validateTraitsSize(traitsJSON); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "traits validation: %v", err)
	}

	// Queue the snapshot for the batched insert+prune path — the same
	// path the stream-inline traits use (hash-deduplicated, 10-snapshot
	// retention enforced on flush). The FleetIndex update below is
	// inline, so the trait values are operator-visible immediately
	// regardless of when the history row lands.
	if s.traitsBatch != nil {
		s.traitsBatch.Mark(envoyID, string(traitsJSON))
	}

	// Update in-memory FleetIndex so the UI sees current traits immediately.
	var parsedTraits map[string]any
	if err := json.Unmarshal(traitsJSON, &parsedTraits); err == nil && s.fleetIndex != nil {
		flat := make(map[string]string)
		fleet.FlattenJSON("", parsedTraits, flat)
		if s.configStore != nil {
			fleet.AnnotatePackageSources(flat, s.configStore.ApprovedSources())
		}
		s.fleetIndex.UpdateTraits(envoyID, flat)
	}

	// Auto-trigger workflows on security trait changes. Detached goroutine
	// off the handler — route its panic recovery through the daemon SoT so a
	// bad trait payload can't crash the process.
	if s.workflowTrigger != nil {
		daemon.SafeGo("checkTraitTriggers", func() { s.checkTraitTriggers(envoyID, traitsJSON) })
	}

	slog.Info("traits reported",
		"envoy_id", envoyID,
		"trace_id", traceID,
		"traits_size", len(traitsJSON),
	)

	return &pb.ReportTraitsResponse{Acknowledged: true}, nil
}

// checkTraitTriggers compares new traits against the previous snapshot and
// fires any matching workflows. Runs in a goroutine to avoid blocking the RPC.
func (s *AgentService) checkTraitTriggers(envoyID string, newTraitsJSON []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Get the previous trait snapshot from FleetIndex (in-memory, fast).
	var oldTraits map[string]string
	if state := s.fleetIndex.GetEnvoy(envoyID); state != nil {
		oldTraits = state.Traits
	}
	if oldTraits == nil {
		oldTraits = make(map[string]string)
	}

	// Flatten new traits JSON to compare.
	var parsed map[string]any
	if err := json.Unmarshal(newTraitsJSON, &parsed); err != nil {
		return
	}
	newTraits := make(map[string]string)
	flattenTraitsJSON("", parsed, newTraits)

	// Find matching workflows.
	triggered := s.workflowTrigger.TriggeredWorkflows(oldTraits, newTraits)
	hostname := ""
	if state := s.fleetIndex.GetEnvoy(envoyID); state != nil {
		hostname = state.Hostname
	}
	for _, name := range triggered {
		operator := fmt.Sprintf("trait-trigger:%s:%s", envoyID, hostname)
		runID, err := s.workflowTrigger.RunWorkflow(ctx, name, operator)
		if err != nil {
			slog.Error("trait-triggered workflow failed to start",
				"workflow", name, "envoy_id", envoyID, "error", err)
		} else {
			slog.Info("trait-triggered workflow started",
				"workflow", name, "envoy_id", envoyID, "run_id", runID)
		}
	}

	// Fire security-specific alerts via webhook/SMTP.
	s.checkSecurityAlerts(ctx, envoyID, hostname, oldTraits, newTraits)
}

// checkSecurityAlerts detects security-relevant trait changes and fires
// webhook/SMTP notifications for each detected condition.
func (s *AgentService) checkSecurityAlerts(ctx context.Context, envoyID, hostname string, oldTraits, newTraits map[string]string) {
	type alert struct {
		event   string
		summary string
	}
	var alerts []alert

	// Rootkit detected: chkrootkit infected count went from 0 to >0.
	if oldTraits[traits.ChkrootkitInfected] == "0" && newTraits[traits.ChkrootkitInfected] != "0" && newTraits[traits.ChkrootkitInfected] != "" {
		alerts = append(alerts, alert{"security.rootkit", fmt.Sprintf("chkrootkit detected infection on %s", hostname)})
	}
	// rkhunter warnings appeared.
	if oldTraits[traits.RkhunterWarnings] == "0" && newTraits[traits.RkhunterWarnings] != "0" && newTraits[traits.RkhunterWarnings] != "" {
		alerts = append(alerts, alert{"security.rootkit", fmt.Sprintf("rkhunter detected warnings on %s", hostname)})
	}

	// Malware detected: ClamAV infected count went from 0 to >0.
	if oldTraits[traits.ClamavInfected] == "0" && newTraits[traits.ClamavInfected] != "0" && newTraits[traits.ClamavInfected] != "" {
		alerts = append(alerts, alert{"security.malware", fmt.Sprintf("ClamAV detected malware on %s", hostname)})
	}

	// Integrity breach: AIDE clean changed from true to false.
	if oldTraits[traits.AideClean] == "true" && newTraits[traits.AideClean] == "false" {
		alerts = append(alerts, alert{"security.integrity", fmt.Sprintf("AIDE detected unauthorized file changes on %s", hostname)})
	}

	// Critical CVE: unique critical count increased from 0.
	if oldTraits[traits.UniqueCritical] == "0" && newTraits[traits.UniqueCritical] != "0" && newTraits[traits.UniqueCritical] != "" {
		alerts = append(alerts, alert{"security.cve_critical", fmt.Sprintf("New critical CVEs detected on %s: %s", hostname, newTraits[traits.UniqueCritical])})
	}

	// Hardening score drop: Lynis score decreased by more than 10 points.
	if oldScore := oldTraits[traits.LynisScore]; oldScore != "" {
		if newScore := newTraits[traits.LynisScore]; newScore != "" {
			var oldS, newS int
			fmt.Sscanf(oldScore, "%d", &oldS)
			fmt.Sscanf(newScore, "%d", &newS)
			if oldS-newS > 10 {
				alerts = append(alerts, alert{"security.hardening_drop", fmt.Sprintf("Lynis hardening score dropped from %d to %d on %s", oldS, newS, hostname)})
			}
		}
	}

	// Dispatch all detected alerts.
	for _, a := range alerts {
		slog.Warn("security alert fired",
			"event", a.event, "envoy_id", envoyID, "hostname", hostname, "summary", a.summary)

		if s.integrations != nil {
			s.integrations.Dispatch(integrations.Event{
				Name:      a.event,
				Timestamp: time.Now(),
				Hostname:  hostname,
				EnvoyID:   envoyID,
				Summary:   a.summary,
				Details:   map[string]any{"summary": a.summary},
			})
		}
	}
}

// flattenTraitsJSON delegates to fleet.FlattenJSON for consistent flattening
// (including array expansion) across the codebase.
func flattenTraitsJSON(prefix string, m map[string]any, out map[string]string) {
	fleet.FlattenJSON(prefix, m, out)
}

const maxTraitCount = 500
const maxTraitValueBytes = 64 * 1024 // 64 KiB

// validateTraitsSize rejects traits payloads with too many keys or oversized values.
func validateTraitsSize(traitsJSON []byte) error {
	var traits map[string]json.RawMessage
	if err := json.Unmarshal(traitsJSON, &traits); err != nil {
		// Not a top-level object — allow, the JSON is already validated.
		return nil
	}
	if len(traits) > maxTraitCount {
		return fmt.Errorf("too many traits: %d (max %d)", len(traits), maxTraitCount)
	}
	for key, val := range traits {
		if len(val) > maxTraitValueBytes {
			return fmt.Errorf("trait %q value too large: %d bytes (max %d)", key, len(val), maxTraitValueBytes)
		}
	}
	return nil
}
