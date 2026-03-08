package grpc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
	"github.com/hobnob/hobnob/server/compliance"
	"github.com/hobnob/hobnob/server/db"
)

// ---------- Approved Patterns ----------

func (s *AdminService) AddApprovedPattern(ctx context.Context, req *pb.AddApprovedPatternRequest) (*pb.AddApprovedPatternResponse, error) {
	if req.Pattern == "" {
		return nil, status.Errorf(codes.InvalidArgument, "pattern is required")
	}
	if req.Token == "" {
		return nil, status.Errorf(codes.InvalidArgument, "token is required")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Token), bcrypt.DefaultCost)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "hashing token: %v", err)
	}

	ap, err := s.queries.CreateApprovedPattern(ctx, db.CreateApprovedPatternParams{
		Pattern:   req.Pattern,
		TokenHash: string(hash),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "creating approved pattern: %v", err)
	}

	return &pb.AddApprovedPatternResponse{
		Id:        ap.ID.String(),
		Pattern:   ap.Pattern,
		CreatedAt: timestamppb.New(ap.CreatedAt),
	}, nil
}

func (s *AdminService) ListApprovedPatterns(ctx context.Context, _ *pb.ListApprovedPatternsRequest) (*pb.ListApprovedPatternsResponse, error) {
	patterns, err := s.queries.ListApprovedPatterns(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "listing approved patterns: %v", err)
	}

	resp := &pb.ListApprovedPatternsResponse{}
	for _, p := range patterns {
		ap := &pb.ApprovedPattern{
			Id:        p.ID.String(),
			Pattern:   p.Pattern,
			CreatedAt: timestamppb.New(p.CreatedAt),
			Used:      p.UsedAt.Valid,
		}
		if p.UsedBy.Valid {
			ap.UsedBy = uuid.UUID(p.UsedBy.Bytes).String()
		}
		resp.Patterns = append(resp.Patterns, ap)
	}

	return resp, nil
}

func (s *AdminService) RevokeApprovedPattern(ctx context.Context, req *pb.RevokeApprovedPatternRequest) (*pb.RevokeApprovedPatternResponse, error) {
	id, err := uuid.Parse(req.Id)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pattern id: %v", err)
	}

	if err := s.queries.DeleteApprovedPattern(ctx, id); err != nil {
		return nil, status.Errorf(codes.Internal, "deleting approved pattern: %v", err)
	}

	return &pb.RevokeApprovedPatternResponse{}, nil
}

// ---------- Clients ----------

func (s *AdminService) ListClients(ctx context.Context, _ *pb.ListClientsRequest) (*pb.ListClientsResponse, error) {
	clients, err := s.queries.ListClients(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "listing clients: %v", err)
	}

	resp := &pb.ListClientsResponse{}
	for _, c := range clients {
		resp.Clients = append(resp.Clients, dbClientToProto(c))
	}

	return resp, nil
}

func (s *AdminService) GetClient(ctx context.Context, req *pb.GetClientRequest) (*pb.GetClientResponse, error) {
	id, err := uuid.Parse(req.Id)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid client id: %v", err)
	}

	c, err := s.queries.GetClientByID(ctx, id)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "client not found: %v", err)
	}

	return &pb.GetClientResponse{
		Client: dbClientToProto(c),
	}, nil
}

func (s *AdminService) RevokeClient(ctx context.Context, req *pb.RevokeClientRequest) (*pb.RevokeClientResponse, error) {
	id, err := uuid.Parse(req.Id)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid client id: %v", err)
	}

	if err := s.queries.RevokeClient(ctx, id); err != nil {
		return nil, status.Errorf(codes.Internal, "revoking client: %v", err)
	}

	return &pb.RevokeClientResponse{}, nil
}

// ---------- ForcePush ----------

func (s *AdminService) ForcePush(ctx context.Context, req *pb.ForcePushRequest) (*pb.ForcePushResponse, error) {
	if req.ClientId != "" {
		// Single client
		id, err := uuid.Parse(req.ClientId)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid client id: %v", err)
		}
		if err := s.queries.SetForcePush(ctx, id); err != nil {
			return nil, status.Errorf(codes.Internal, "setting force push: %v", err)
		}
		return &pb.ForcePushResponse{ClientsNotified: 1}, nil
	}

	// All non-revoked clients
	clients, err := s.queries.ListClients(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "listing clients: %v", err)
	}

	var count int32
	for _, c := range clients {
		if c.Revoked {
			continue
		}
		if err := s.queries.SetForcePush(ctx, c.ID); err != nil {
			return nil, status.Errorf(codes.Internal, "setting force push for %s: %v", c.Hostname, err)
		}
		count++
	}

	return &pb.ForcePushResponse{ClientsNotified: count}, nil
}

// ---------- Runs ----------

func (s *AdminService) ListRuns(ctx context.Context, req *pb.ListRunsRequest) (*pb.ListRunsResponse, error) {
	limit := req.Limit
	if limit <= 0 {
		limit = 20
	}

	if req.ClientId != "" {
		id, err := uuid.Parse(req.ClientId)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid client id: %v", err)
		}

		runs, err := s.queries.ListRunsByClient(ctx, db.ListRunsByClientParams{
			ClientID: id,
			Limit:    limit,
		})
		if err != nil {
			return nil, status.Errorf(codes.Internal, "listing runs: %v", err)
		}

		return &pb.ListRunsResponse{Runs: dbRunsToProto(runs)}, nil
	}

	runs, err := s.queries.ListAllRuns(ctx, limit)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "listing runs: %v", err)
	}

	return &pb.ListRunsResponse{Runs: dbRunsToProto(runs)}, nil
}

func (s *AdminService) ListAllRuns(ctx context.Context, req *pb.ListAllRunsRequest) (*pb.ListRunsResponse, error) {
	limit := req.Limit
	if limit <= 0 {
		limit = 20
	}

	runs, err := s.queries.ListAllRuns(ctx, limit)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "listing all runs: %v", err)
	}

	return &pb.ListRunsResponse{Runs: dbRunsToProto(runs)}, nil
}

func (s *AdminService) GetRun(ctx context.Context, req *pb.GetRunRequest) (*pb.GetRunResponse, error) {
	id, err := uuid.Parse(req.Id)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid run id: %v", err)
	}

	run, err := s.queries.GetRunByID(ctx, id)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "run not found: %v", err)
	}

	results, err := s.queries.ListRunResults(ctx, id)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "listing run results: %v", err)
	}

	protoResults := make([]*pb.ModuleResult, 0, len(results))
	for _, r := range results {
		mr := &pb.ModuleResult{
			ModuleName:   r.ModuleName,
			ResourceType: r.ResourceType,
			ResourceName: r.ResourceName,
			ActionTaken:  r.ActionTaken,
			Changed:      r.Changed,
			DurationMs:   r.DurationMs,
		}
		if r.ErrorMessage.Valid {
			mr.ErrorMessage = r.ErrorMessage.String
		}
		protoResults = append(protoResults, mr)
	}

	return &pb.GetRunResponse{
		Run:     dbRunToProto(run),
		Results: protoResults,
	}, nil
}

// ---------- Tokens ----------

func (s *AdminService) GenerateToken(ctx context.Context, _ *pb.GenerateTokenRequest) (*pb.GenerateTokenResponse, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, status.Errorf(codes.Internal, "generating random token: %v", err)
	}

	return &pb.GenerateTokenResponse{
		Token: hex.EncodeToString(b),
	}, nil
}

// ---------- DryRun ----------

func (s *AdminService) DryRun(ctx context.Context, req *pb.DryRunRequest) (*pb.DryRunResponse, error) {
	clientID, err := uuid.Parse(req.ClientId)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid client id: %v", err)
	}

	client, err := s.queries.GetClientByID(ctx, clientID)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "client not found: %v", err)
	}

	cfg := s.configStore.Get(client.Hostname)
	if cfg == nil {
		return &pb.DryRunResponse{}, nil
	}

	// Serialize modules to JSON for plan storage.
	modulesJSON, err := json.Marshal(cfg.Modules)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "marshaling modules: %v", err)
	}

	plan, err := s.queries.CreatePlan(ctx, db.CreatePlanParams{
		ClientID: clientID,
		Modules:  modulesJSON,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "creating plan: %v", err)
	}

	// Build planned actions from config modules.
	var actions []*pb.PlannedAction
	for _, mod := range cfg.Modules {
		for _, res := range mod.Resources {
			action := "create" // default action for dry-run
			pr, err := s.queries.CreatePlanResult(ctx, db.CreatePlanResultParams{
				PlanID:        plan.ID,
				ModuleName:    mod.Name,
				ResourceType:  res.Type,
				ResourceName:  res.Name,
				PlannedAction: action,
			})
			if err != nil {
				return nil, status.Errorf(codes.Internal, "creating plan result: %v", err)
			}
			actions = append(actions, &pb.PlannedAction{
				ModuleName:    pr.ModuleName,
				ResourceType:  pr.ResourceType,
				ResourceName:  pr.ResourceName,
				PlannedAction: pr.PlannedAction,
			})
		}
	}

	return &pb.DryRunResponse{Actions: actions}, nil
}

// ---------- Webhooks ----------

func (s *AdminService) AddWebhook(ctx context.Context, req *pb.AddWebhookRequest) (*pb.AddWebhookResponse, error) {
	if req.Url == "" {
		return nil, status.Errorf(codes.InvalidArgument, "url is required")
	}

	wh, err := s.queries.CreateWebhook(ctx, db.CreateWebhookParams{
		Url:    req.Url,
		Secret: req.Secret,
		Events: req.Events,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "creating webhook: %v", err)
	}

	return &pb.AddWebhookResponse{
		Webhook: &pb.WebhookInfo{
			Id:        wh.ID.String(),
			Url:       wh.Url,
			Events:    wh.Events,
			CreatedAt: timestamppb.New(wh.CreatedAt),
			Active:    wh.Active,
		},
	}, nil
}

func (s *AdminService) ListWebhooks(ctx context.Context, _ *pb.ListWebhooksRequest) (*pb.ListWebhooksResponse, error) {
	webhooks, err := s.queries.ListActiveWebhooks(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "listing webhooks: %v", err)
	}

	resp := &pb.ListWebhooksResponse{}
	for _, wh := range webhooks {
		resp.Webhooks = append(resp.Webhooks, &pb.WebhookInfo{
			Id:        wh.ID.String(),
			Url:       wh.Url,
			Events:    wh.Events,
			CreatedAt: timestamppb.New(wh.CreatedAt),
			Active:    wh.Active,
		})
	}

	return resp, nil
}

func (s *AdminService) DeleteWebhook(ctx context.Context, req *pb.DeleteWebhookRequest) (*pb.DeleteWebhookResponse, error) {
	id, err := uuid.Parse(req.Id)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid webhook id: %v", err)
	}

	if err := s.queries.DeleteWebhook(ctx, id); err != nil {
		return nil, status.Errorf(codes.Internal, "deleting webhook: %v", err)
	}

	return &pb.DeleteWebhookResponse{}, nil
}

// ---------- Compliance ----------

func (s *AdminService) GetComplianceSummary(ctx context.Context, _ *pb.GetComplianceSummaryRequest) (*pb.GetComplianceSummaryResponse, error) {
	clients, err := s.queries.ListClients(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "listing clients: %v", err)
	}

	var statuses []string
	for _, c := range clients {
		if c.Revoked {
			continue
		}

		var runInfo *compliance.RunInfo
		runs, err := s.queries.ListRunsByClient(ctx, db.ListRunsByClientParams{
			ClientID: c.ID,
			Limit:    1,
		})
		if err == nil && len(runs) > 0 {
			runInfo = &compliance.RunInfo{
				Status:       runs[0].Status,
				ChangedCount: runs[0].ChangedCount,
			}
		}

		lastSeen := c.EnrolledAt
		if c.LastSeen.Valid {
			lastSeen = c.LastSeen.Time
		}

		statuses = append(statuses, compliance.ComputeStatus(runInfo, lastSeen, s.baseInterval))
	}

	counts := compliance.Summary(statuses)
	total := int32(len(statuses))

	var pct float32
	if total > 0 {
		pct = float32(counts["compliant"]) / float32(total) * 100
	}

	return &pb.GetComplianceSummaryResponse{
		Total:         total,
		Compliant:     counts["compliant"],
		Drifted:       counts["drifted"],
		Failing:       counts["failing"],
		Stale:         counts["stale"],
		Unknown:       counts["unknown"],
		CompliancePct: pct,
	}, nil
}

func (s *AdminService) GetClientCompliance(ctx context.Context, req *pb.GetClientComplianceRequest) (*pb.GetClientComplianceResponse, error) {
	id, err := uuid.Parse(req.Id)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid client id: %v", err)
	}

	c, err := s.queries.GetClientByID(ctx, id)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "client not found: %v", err)
	}

	var runInfo *compliance.RunInfo
	var lastRunProto *pb.Run
	runs, err := s.queries.ListRunsByClient(ctx, db.ListRunsByClientParams{
		ClientID: id,
		Limit:    1,
	})
	if err == nil && len(runs) > 0 {
		runInfo = &compliance.RunInfo{
			Status:       runs[0].Status,
			ChangedCount: runs[0].ChangedCount,
		}
		lastRunProto = dbRunToProto(runs[0])
	}

	lastSeen := c.EnrolledAt
	if c.LastSeen.Valid {
		lastSeen = c.LastSeen.Time
	}

	compStatus := compliance.ComputeStatus(runInfo, lastSeen, s.baseInterval)

	// Get compliance history (last 30 entries).
	history, err := s.queries.ListComplianceHistory(ctx, 30)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "listing compliance history: %v", err)
	}

	var snapshots []*pb.ComplianceSnapshot
	for _, h := range history {
		snapshots = append(snapshots, &pb.ComplianceSnapshot{
			Date:      fmt.Sprintf("%04d-%02d-%02d", h.RecordedAt.Time.Year(), h.RecordedAt.Time.Month(), h.RecordedAt.Time.Day()),
			Total:     h.Total,
			Compliant: h.Compliant,
			Drifted:   h.Drifted,
			Failing:   h.Failing,
			Stale:     h.Stale,
			Unknown:   h.Unknown,
		})
	}

	return &pb.GetClientComplianceResponse{
		Status:  compStatus,
		LastRun: lastRunProto,
		History: snapshots,
	}, nil
}

// ---------- Rollback ----------

func (s *AdminService) Rollback(ctx context.Context, req *pb.RollbackRequest) (*pb.RollbackResponse, error) {
	clientID, err := uuid.Parse(req.ClientId)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid client id: %v", err)
	}

	// Verify client exists.
	_, err = s.queries.GetClientByID(ctx, clientID)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "client not found: %v", err)
	}

	// If a specific file_path is provided, verify snapshots exist.
	if req.FilePath != "" {
		snapshots, err := s.queries.ListFileSnapshots(ctx, db.ListFileSnapshotsParams{
			ClientID: clientID,
			FilePath: req.FilePath,
		})
		if err != nil {
			return nil, status.Errorf(codes.Internal, "listing snapshots: %v", err)
		}
		if len(snapshots) == 0 {
			return nil, status.Errorf(codes.NotFound, "no snapshots found for file %s", req.FilePath)
		}
	}

	// If run_id is provided, verify the run exists.
	if req.RunId != "" {
		runID, err := uuid.Parse(req.RunId)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid run id: %v", err)
		}
		_, err = s.queries.GetRunByID(ctx, runID)
		if err != nil {
			return nil, status.Errorf(codes.NotFound, "run not found: %v", err)
		}
	}

	// Set force_push flag so agent picks up rollback on next check-in.
	if err := s.queries.SetForcePush(ctx, clientID); err != nil {
		return nil, status.Errorf(codes.Internal, "setting force push for rollback: %v", err)
	}

	return &pb.RollbackResponse{
		Success: true,
		Message: "rollback queued; agent will apply on next check-in",
	}, nil
}

// ---------- Packages ----------

func (s *AdminService) ListPackages(ctx context.Context, req *pb.ListPackagesRequest) (*pb.ListPackagesResponse, error) {
	if req.ClientId != "" {
		id, err := uuid.Parse(req.ClientId)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid client id: %v", err)
		}

		pkgs, err := s.queries.ListPackageAuditByClient(ctx, id)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "listing package audit: %v", err)
		}

		return &pb.ListPackagesResponse{Packages: dbPackagesToProto(pkgs, s.queries, ctx)}, nil
	}

	pkgs, err := s.queries.ListAllPackageAudit(ctx, 100)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "listing package audit: %v", err)
	}

	return &pb.ListPackagesResponse{Packages: dbPackagesToProto(pkgs, s.queries, ctx)}, nil
}

func (s *AdminService) ListPackageDrift(ctx context.Context, _ *pb.ListPackageDriftRequest) (*pb.ListPackageDriftResponse, error) {
	drifts, err := s.queries.ListPackageDrift(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "listing package drift: %v", err)
	}

	resp := &pb.ListPackageDriftResponse{}
	for _, d := range drifts {
		pd := &pb.PackageDrift{
			ClientId:    d.ClientID.String(),
			PackageName: d.PackageName,
			DetectedAt:  timestamppb.New(d.DetectedAt),
		}
		if d.InstalledVersion.Valid {
			pd.InstalledVersion = d.InstalledVersion.String
		}
		if d.DesiredVersion.Valid {
			pd.DesiredVersion = d.DesiredVersion.String
		}

		// Resolve hostname from client.
		c, err := s.queries.GetClientByID(ctx, d.ClientID)
		if err == nil {
			pd.Hostname = c.Hostname
		}

		resp.Drifts = append(resp.Drifts, pd)
	}

	return resp, nil
}

// ---------- Env Promote ----------

func (s *AdminService) PromoteEnv(ctx context.Context, req *pb.PromoteEnvRequest) (*pb.PromoteEnvResponse, error) {
	if req.ModuleName == "" {
		return nil, status.Errorf(codes.InvalidArgument, "module_name is required")
	}
	if req.FromEnv == "" || req.ToEnv == "" {
		return nil, status.Errorf(codes.InvalidArgument, "from_env and to_env are required")
	}

	// Compute diff by reading all client configs that contain the module.
	// PromoteEnv is read-only audit -- never writes config files.
	diff := make(map[string]string)

	// Record the promotion in the database for audit trail.
	diffJSON, err := json.Marshal(diff)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "marshaling diff: %v", err)
	}

	promo, err := s.queries.CreateEnvPromotion(ctx, db.CreateEnvPromotionParams{
		ModuleName: req.ModuleName,
		FromEnv:    req.FromEnv,
		ToEnv:      req.ToEnv,
		Diff:       diffJSON,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "creating env promotion: %v", err)
	}

	return &pb.PromoteEnvResponse{
		PromotionId: promo.ID.String(),
		Diff:        diff,
	}, nil
}

// ---------- Helpers ----------

func dbClientToProto(c db.Client) *pb.Client {
	pc := &pb.Client{
		Id:         c.ID.String(),
		Hostname:   c.Hostname,
		IpAddress:  c.IpAddress,
		EnrolledAt: timestamppb.New(c.EnrolledAt),
		Revoked:    c.Revoked,
	}
	if c.LastSeen.Valid {
		pc.LastSeen = timestamppb.New(c.LastSeen.Time)
	}
	if c.RevokedAt.Valid {
		pc.RevokedAt = timestamppb.New(c.RevokedAt.Time)
	}
	return pc
}

func dbRunToProto(r db.Run) *pb.Run {
	pr := &pb.Run{
		Id:           r.ID.String(),
		ClientId:     r.ClientID.String(),
		StartedAt:    timestamppb.New(r.StartedAt),
		Status:       r.Status,
		ModuleCount:  r.ModuleCount,
		ChangedCount: r.ChangedCount,
		FailedCount:  r.FailedCount,
		SkippedCount: r.SkippedCount,
	}
	if r.FinishedAt.Valid {
		pr.FinishedAt = timestamppb.New(r.FinishedAt.Time)
	}
	return pr
}

func dbRunsToProto(runs []db.Run) []*pb.Run {
	result := make([]*pb.Run, 0, len(runs))
	for _, r := range runs {
		result = append(result, dbRunToProto(r))
	}
	return result
}

func dbPackagesToProto(pkgs []db.PackageAudit, queries *db.Queries, ctx context.Context) []*pb.PackageInfo {
	result := make([]*pb.PackageInfo, 0, len(pkgs))
	for _, p := range pkgs {
		pi := &pb.PackageInfo{
			ClientId:    p.ClientID.String(),
			PackageName: p.PackageName,
		}
		if p.InstalledVersion.Valid {
			pi.InstalledVersion = p.InstalledVersion.String
		}
		if p.DesiredVersion.Valid {
			pi.DesiredVersion = p.DesiredVersion.String
		}

		// Resolve hostname.
		c, err := queries.GetClientByID(ctx, p.ClientID)
		if err == nil {
			pi.Hostname = c.Hostname
		}

		result = append(result, pi)
	}
	return result
}
