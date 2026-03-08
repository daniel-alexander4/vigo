package grpc

import (
	"time"

	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
	"github.com/hobnob/hobnob/server/config"
	"github.com/hobnob/hobnob/server/db"
	"github.com/hobnob/hobnob/server/registry"
	"github.com/hobnob/hobnob/server/traits"
	"github.com/hobnob/hobnob/server/webhook"
)

// AgentService implements the HobnobAgent gRPC service.
type AgentService struct {
	pb.UnimplementedHobnobAgentServer
	registry       *registry.Registry
	configStore    *config.ConfigStore
	queries        *db.Queries
	dispatcher     *webhook.Dispatcher
	traitsScanner  *traits.Scanner
}

// AdminService implements the HobnobAdmin gRPC service.
type AdminService struct {
	pb.UnimplementedHobnobAdminServer
	registry     *registry.Registry
	queries      *db.Queries
	configStore  *config.ConfigStore
	baseInterval time.Duration
}
