package grpc

import (
	"time"

	pb "github.com/isogov/isogov/gen/proto/isogov"
	"github.com/isogov/isogov/server/config"
	"github.com/isogov/isogov/server/db"
	"github.com/isogov/isogov/server/registry"
	"github.com/isogov/isogov/server/traits"
	"github.com/isogov/isogov/server/webhook"
)

// AgentService implements the IsgovAgent gRPC service.
type AgentService struct {
	pb.UnimplementedIsgovAgentServer
	registry       *registry.Registry
	configStore    *config.ConfigStore
	queries        *db.Queries
	dispatcher     *webhook.Dispatcher
	traitsScanner  *traits.Scanner
}

// AdminService implements the IsgovAdmin gRPC service.
type AdminService struct {
	pb.UnimplementedIsgovAdminServer
	registry     *registry.Registry
	queries      *db.Queries
	configStore  *config.ConfigStore
	baseInterval time.Duration
}
