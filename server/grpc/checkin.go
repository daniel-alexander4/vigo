package grpc

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/google/uuid"
	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
	"github.com/hobnob/hobnob/server/db"
	"github.com/hobnob/hobnob/server/metrics"
	"github.com/hobnob/hobnob/server/registry"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// CheckIn handles the HobnobAgent.CheckIn RPC. It validates the client,
// stores inline nails for admin queries, and returns a policy bundle for
// agent-side evaluation. The server never renders templates or evaluates
// conditionals — the agent does that locally with its own nails.
func (s *AgentService) CheckIn(ctx context.Context, req *pb.CheckInRequest) (*pb.CheckInResponse, error) {
	nodeID, err := uuid.Parse(req.GetNodeId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid node_id: %v", err)
	}

	// Validate node is not revoked.
	if err := s.registry.ValidateNode(ctx, nodeID); err != nil {
		metrics.CheckinsTotal.WithLabelValues("error").Inc()
		if errors.Is(err, registry.ErrNodeRevoked) {
			return nil, status.Error(codes.PermissionDenied, "node is revoked")
		}
		if errors.Is(err, registry.ErrNodeNotFound) {
			return nil, status.Error(codes.NotFound, "node not found")
		}
		return nil, status.Errorf(codes.Internal, "validating node: %v", err)
	}

	// Batch last-seen update (async flush every 10s).
	if s.lastSeenBatch != nil {
		s.lastSeenBatch.Mark(nodeID)
	} else {
		if err := s.queries.UpdateLastSeen(ctx, nodeID); err != nil {
			slog.Error("failed to update last_seen", "node_id", nodeID, "error", err)
		}
	}

	// Reuse node from interceptor context if available, otherwise cache lookup.
	var node db.Node
	if n := db.NodeFromContext(ctx); n != nil {
		node = *n
	} else if s.nodeCache != nil {
		var nErr error
		node, nErr = s.nodeCache.GetByID(ctx, nodeID)
		if nErr != nil {
			return nil, status.Errorf(codes.Internal, "looking up node: %v", nErr)
		}
	} else {
		var nErr error
		node, nErr = s.queries.GetNodeByID(ctx, nodeID)
		if nErr != nil {
			return nil, status.Errorf(codes.Internal, "looking up node: %v", nErr)
		}
	}

	// Store inline nails for admin queries.
	inlineNails := req.GetNailsJson()
	if len(inlineNails) > 0 && json.Valid(inlineNails) {
		if _, dbErr := s.queries.InsertNodeNails(ctx, db.InsertNodeNailsParams{
			NodeID: nodeID,
			Nails:  json.RawMessage(inlineNails),
		}); dbErr != nil {
			slog.Error("failed to store inline nails", "node_id", nodeID, "error", dbErr)
		} else {
			if pruneErr := s.queries.PruneNodeNails(ctx, nodeID); pruneErr != nil {
				slog.Error("failed to prune node nails", "node_id", nodeID, "error", pruneErr)
			}
		}
	}

	configVersion := s.configStore.Version()

	// Short-circuit: agent has a cached bundle with matching config version.
	if req.GetPolicyVersion() > 0 && req.GetPolicyVersion() == configVersion {
		metrics.CheckinsTotal.WithLabelValues("no_change").Inc()
		slog.Info("check-in no change",
			"node_id", nodeID,
			"hostname", node.Hostname,
			"policy_version", configVersion,
		)
		return &pb.CheckInResponse{
			NoChange:      true,
			PolicyVersion: configVersion,
		}, nil
	}

	// Build policy bundle: secrets resolved, vars merged, templates and
	// conditionals left raw for agent-side evaluation.
	cfg := s.configStore.Get(node.Hostname)

	bundle, err := buildPolicyBundle(cfg, configVersion, req.GetCachedModuleHashes())
	if err != nil {
		slog.Error("failed to build policy bundle", "node_id", nodeID, "error", err)
		return nil, status.Errorf(codes.Internal, "building policy bundle: %v", err)
	}

	// Include custom nails manifest.
	nailEntries, nailErr := s.nailsScanner.ScanDir()
	if nailErr != nil {
		slog.Warn("failed to scan nails directory", "error", nailErr)
	} else {
		for _, te := range nailEntries {
			bundle.CustomNails = append(bundle.CustomNails, &pb.CustomNailEntry{
				Name:   te.Name,
				Sha256: te.SHA256,
			})
		}
	}

	metrics.CheckinsTotal.WithLabelValues("policy_bundle").Inc()
	slog.Info("check-in",
		"node_id", nodeID,
		"hostname", node.Hostname,
		"policy_version", configVersion,
		"modules", len(bundle.Modules),
	)

	return &pb.CheckInResponse{
		PolicyBundle:  bundle,
		PolicyVersion: configVersion,
	}, nil
}
