package grpc

import (
	"time"

	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
	"github.com/hobnob/hobnob/server/config"
	"github.com/hobnob/hobnob/server/db"
	"github.com/hobnob/hobnob/server/federation"
	"github.com/hobnob/hobnob/server/license"
	"github.com/hobnob/hobnob/server/registry"
	hobnobsmtp "github.com/hobnob/hobnob/server/smtp"
	"github.com/hobnob/hobnob/server/traits"
	"github.com/hobnob/hobnob/server/webhook"
)

// AgentService implements the HobnobAgent gRPC service.
type AgentService struct {
	pb.UnimplementedHobnobAgentServer
	registry       *registry.Registry
	configStore    *config.ConfigStore
	queries        *db.Queries
	runStore       db.RunStore
	dispatcher     *webhook.Dispatcher
	smtpNotifier   *hobnobsmtp.Notifier
	traitsScanner   *traits.Scanner
	thrallCache    *db.ThrallCache
	lastSeenBatch  *db.LastSeenBatcher
	traitsPruner   *db.TraitsPruner
	policyCache    *policyCache
	streamMgr        *streamManager
	hub              *federation.Hub    // non-nil when role=hub; routes enrollment to spokes
	licenseEnforcer  *license.Enforcer
}

// AdminService implements the HobnobAdmin gRPC service.
type AdminService struct {
	pb.UnimplementedHobnobAdminServer
	registry     *registry.Registry
	queries      *db.Queries
	runStore     db.RunStore
	configStore  *config.ConfigStore
	baseInterval time.Duration
	hub          *federation.Hub // non-nil when role=hub; aggregates from spokes
}
