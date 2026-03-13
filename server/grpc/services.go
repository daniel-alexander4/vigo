// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package grpc

import (
	"time"

	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
	"github.com/hobnob/hobnob/server/config"
	"github.com/hobnob/hobnob/server/db"
	"github.com/hobnob/hobnob/server/federation"
	"github.com/hobnob/hobnob/server/fleet"
	"github.com/hobnob/hobnob/server/license"
	"github.com/hobnob/hobnob/server/registry"
	hobnobsmtp "github.com/hobnob/hobnob/server/smtp"
	"github.com/hobnob/hobnob/server/tasks"
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
	traitsBatch    *db.TraitsBatcher
	traitsPruner   *db.TraitsPruner
	policyCache    *policyCache
	streamMgr        *streamManager
	taskRouter       *taskRouter
	queryRouter      *queryRouter
	taskStore        *tasks.Store
	hub              *federation.Hub    // non-nil when role=hub; routes enrollment to spokes
	licenseEnforcer  *license.Enforcer
	fleetIndex       *fleet.FleetIndex  // in-memory index for hot-path operations
	runBatcher       *fleet.RunBatcher  // async run persistence from delta events
	reassignStore    *ReassignStore     // pending thrall reassignments (spoke side)
}

// AdminService implements the HobnobAdmin gRPC service.
type AdminService struct {
	pb.UnimplementedHobnobAdminServer
	registry      *registry.Registry
	queries       *db.Queries
	runStore      db.RunStore
	configStore   *config.ConfigStore
	baseInterval  time.Duration
	hub           *federation.Hub   // non-nil when role=hub; aggregates from spokes
	fleetIndex    *fleet.FleetIndex // in-memory index for hot-path operations
	reassignStore *ReassignStore    // pending thrall reassignments (spoke side)
}
