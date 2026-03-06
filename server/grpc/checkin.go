package grpc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	pb "github.com/isogov/isogov/gen/proto/isogov"
	"github.com/isogov/isogov/server/config"
	"github.com/isogov/isogov/server/registry"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// CheckIn handles the IsgovAgent.CheckIn RPC. It validates the client is not
// revoked, looks up its configuration, renders templates with facts, and
// returns the compiled module list.
func (s *AgentService) CheckIn(ctx context.Context, req *pb.CheckInRequest) (*pb.CheckInResponse, error) {
	clientID, err := uuid.Parse(req.GetClientId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid client_id: %v", err)
	}

	// Validate client is not revoked.
	if err := s.registry.ValidateClient(ctx, clientID); err != nil {
		if errors.Is(err, registry.ErrClientRevoked) {
			return nil, status.Error(codes.PermissionDenied, "client is revoked")
		}
		if errors.Is(err, registry.ErrClientNotFound) {
			return nil, status.Error(codes.NotFound, "client not found")
		}
		return nil, status.Errorf(codes.Internal, "validating client: %v", err)
	}

	// Update last-seen timestamp.
	if err := s.queries.UpdateLastSeen(ctx, clientID); err != nil {
		slog.Error("failed to update last_seen", "client_id", clientID, "error", err)
		// Non-fatal: continue with check-in.
	}

	// Look up client by ID to get hostname for config resolution.
	client, err := s.queries.GetClientByID(ctx, clientID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "looking up client: %v", err)
	}

	// Resolve client config -- explicit YAML -> classification -> empty modules.
	cfg := s.configStore.Get(client.Hostname)

	// Convert config modules to proto modules, rendering templates at check-in time.
	// Facts would come from the agent's ReportFacts -- for now we use an empty map
	// since facts are reported separately and stored in the DB.
	facts := make(map[string]any)
	vars := cfg.Vars
	if vars == nil {
		vars = make(map[string]any)
	}

	modules, err := configToProtoModules(cfg, facts, vars)
	if err != nil {
		slog.Error("failed to compile modules", "client_id", clientID, "error", err)
		return nil, status.Errorf(codes.Internal, "compiling modules: %v", err)
	}

	slog.Info("check-in",
		"client_id", clientID,
		"hostname", client.Hostname,
		"modules", len(modules),
	)

	return &pb.CheckInResponse{
		Modules: modules,
	}, nil
}

// configToProtoModules converts a ClientConfig's modules into proto Module
// messages, rendering any Go template strings with the provided facts and vars.
func configToProtoModules(cfg *config.ClientConfig, facts, vars map[string]any) ([]*pb.Module, error) {
	if len(cfg.Modules) == 0 {
		return nil, nil
	}

	modules := make([]*pb.Module, 0, len(cfg.Modules))
	for _, mod := range cfg.Modules {
		pbMod := &pb.Module{
			Name:      mod.Name,
			DependsOn: mod.DependsOn,
		}

		for _, res := range mod.Resources {
			params := make(map[string]string)
			for k, v := range res.Attributes {
				strVal := fmt.Sprintf("%v", v)
				// Render template if it contains Go template syntax.
				rendered, err := config.RenderTemplate(strVal, facts, vars)
				if err != nil {
					return nil, fmt.Errorf("rendering template for resource %q attr %q: %w",
						res.Name, k, err)
				}
				params[k] = rendered
			}

			pbRes := &pb.Resource{
				Name:   res.Name,
				Type:   res.Type,
				Params: params,
			}
			pbMod.Resources = append(pbMod.Resources, pbRes)

			// Set module-level resource type from first resource if not set.
			if pbMod.ResourceType == "" {
				pbMod.ResourceType = res.Type
			}

			// Copy resource-level notify/subscribes/when to module level.
			pbMod.Notify = append(pbMod.Notify, res.Notify...)
			pbMod.Subscribes = append(pbMod.Subscribes, res.Subscribes...)
			if res.When != "" {
				pbMod.When = res.When
			}
		}

		modules = append(modules, pbMod)
	}

	return modules, nil
}
