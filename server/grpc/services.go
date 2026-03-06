package grpc

import (
	pb "github.com/isogov/isogov/gen/proto/isogov"
	"github.com/isogov/isogov/server/config"
	"github.com/isogov/isogov/server/db"
	"github.com/isogov/isogov/server/registry"
)

// AgentService implements the IsgovAgent gRPC service.
type AgentService struct {
	pb.UnimplementedIsgovAgentServer
	registry    *registry.Registry
	configStore *config.ConfigStore
	queries     *db.Queries
}

// AdminService implements the IsgovAdmin gRPC service.
// Most admin RPCs are stubs until Phase 3 (gvctl/REST API work).
type AdminService struct {
	pb.UnimplementedIsgovAdminServer
	registry *registry.Registry
	queries  *db.Queries
}
