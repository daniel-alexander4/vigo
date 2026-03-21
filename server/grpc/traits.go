// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	pb "github.com/daniel-alexander4/vigo/gen/proto/vigo"
	"github.com/daniel-alexander4/vigo/server/db"
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

	// Store the traits.
	_, err := s.queries.InsertEnvoyTraits(ctx, db.InsertEnvoyTraitsParams{
		EnvoyID: envoyID,
		Traits: string(traitsJSON),
	})
	if err != nil {
		slog.Error("failed to insert envoy traits",
			"node_id", envoyID,
			"error", err,
		)
		return nil, status.Errorf(codes.Internal, "storing traits: %v", err)
	}

	// Schedule async pruning to enforce 10-snapshot retention.
	if s.traitsPruner != nil {
		s.traitsPruner.Mark(envoyID)
	}

	// Auto-trigger workflows on security trait changes.
	if s.workflowTrigger != nil {
		go s.checkTraitTriggers(envoyID, traitsJSON)
	}

	slog.Info("traits reported",
		"node_id", envoyID,
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
	for _, name := range triggered {
		hostname := ""
		if state := s.fleetIndex.GetEnvoy(envoyID); state != nil {
			hostname = state.Hostname
		}
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
}

// flattenTraitsJSON recursively flattens a nested JSON map into dot-separated keys.
func flattenTraitsJSON(prefix string, m map[string]any, out map[string]string) {
	for k, v := range m {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		switch val := v.(type) {
		case map[string]any:
			flattenTraitsJSON(key, val, out)
		default:
			out[key] = fmt.Sprintf("%v", val)
		}
	}
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
