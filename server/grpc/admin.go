// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/daniel-alexander4/vigo/generated.code/proto/vigo"
	"github.com/daniel-alexander4/vigo/server/convergence"
	"github.com/daniel-alexander4/vigo/server/db"
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

	slog.Info("approved pattern created", "id", ap.ID, "pattern", ap.Pattern)

	return &pb.AddApprovedPatternResponse{
		Id:        ap.ID,
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
			Id:        p.ID,
			Pattern:   p.Pattern,
			CreatedAt: timestamppb.New(p.CreatedAt),
			Used:      p.UsedAt.Valid,
		}
		if p.UsedBy.Valid {
			ap.UsedBy = p.UsedBy.String
		}
		resp.Patterns = append(resp.Patterns, ap)
	}

	return resp, nil
}

func (s *AdminService) RevokeApprovedPattern(ctx context.Context, req *pb.RevokeApprovedPatternRequest) (*pb.RevokeApprovedPatternResponse, error) {
	if _, err := uuid.Parse(req.Id); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pattern id: %v", err)
	}

	if err := s.queries.DeleteApprovedPattern(ctx, req.Id); err != nil {
		return nil, status.Errorf(codes.Internal, "deleting approved pattern: %v", err)
	}

	slog.Info("approved pattern revoked", "id", req.Id)

	return &pb.RevokeApprovedPatternResponse{}, nil
}

// ---------- Envoys ----------

func (s *AdminService) ListEnvoys(ctx context.Context, req *pb.ListEnvoysRequest) (*pb.ListEnvoysResponse, error) {
	// Hub mode: aggregate from all spokes.
	if s.hub != nil {
		envoys, err := s.hub.AggregateListEnvoys(ctx)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "aggregating envoys from spokes: %v", err)
		}
		return &pb.ListEnvoysResponse{Envoys: envoys}, nil
	}

	var envoys []db.Envoy
	var err error
	if req.GetIncludeRevoked() {
		envoys, err = s.queries.ListEnvoys(ctx)
	} else {
		envoys, err = s.queries.ListActiveEnvoys(ctx)
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "listing envoys: %v", err)
	}

	resp := &pb.ListEnvoysResponse{}
	for _, n := range envoys {
		pt := dbEnvoyToProto(n)
		if s.fleetIndex != nil {
			if ts, ok, _ := s.fleetIndex.GetEnvoyLite(n.ID); ok {
				pt.AgentVersion = ts.AgentVersion
			}
		}
		resp.Envoys = append(resp.Envoys, pt)
	}

	return resp, nil
}

func (s *AdminService) GetEnvoy(ctx context.Context, req *pb.GetEnvoyRequest) (*pb.GetEnvoyResponse, error) {
	if _, err := uuid.Parse(req.Id); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid envoy id: %v", err)
	}

	n, err := s.queries.GetEnvoyByID(ctx, req.Id)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "envoy not found: %v", err)
	}

	pt := dbEnvoyToProto(n)
	if s.fleetIndex != nil {
		if ts, ok, _ := s.fleetIndex.GetEnvoyLite(req.Id); ok {
			pt.AgentVersion = ts.AgentVersion
		}
	}
	return &pb.GetEnvoyResponse{
		Envoy: pt,
	}, nil
}

func (s *AdminService) RevokeEnvoy(ctx context.Context, req *pb.RevokeEnvoyRequest) (*pb.RevokeEnvoyResponse, error) {
	if _, err := uuid.Parse(req.Id); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid envoy id: %v", err)
	}

	if err := s.queries.RevokeEnvoy(ctx, req.Id); err != nil {
		return nil, status.Errorf(codes.Internal, "revoking envoy: %v", err)
	}

	// Dual-write: update FleetIndex.
	if s.fleetIndex != nil {
		s.fleetIndex.SetRevoked(req.Id, true)
	}

	slog.Warn("envoy revoked", "envoy_id", req.Id)

	// Record tamper-evident audit event.
	recordAudit(ctx, s.auditWriter, "envoy.revoke", "localhost", "grpc", req.Id+" revoked", "")

	return &pb.RevokeEnvoyResponse{}, nil
}

// ---------- DeleteEnvoy ----------

func (s *AdminService) DeleteEnvoy(ctx context.Context, req *pb.DeleteEnvoyRequest) (*pb.DeleteEnvoyResponse, error) {
	if _, err := uuid.Parse(req.Id); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid envoy id: %v", err)
	}

	// Look up hostname before deleting for the audit trail.
	hostname := req.Id
	if s.fleetIndex != nil {
		if t := s.fleetIndex.GetEnvoy(req.Id); t != nil {
			hostname = t.Hostname
		}
	}

	if err := s.queries.DeleteEnvoy(ctx, req.Id); err != nil {
		return nil, status.Errorf(codes.Internal, "deleting envoy: %v", err)
	}

	// Dual-write: remove from FleetIndex.
	if s.fleetIndex != nil {
		s.fleetIndex.RemoveEnvoy(req.Id)
	}

	slog.Warn("envoy deleted", "envoy_id", req.Id, "hostname", hostname)

	// Record tamper-evident audit event (CASCADE deletes traits, runs, snapshots).
	recordAudit(ctx, s.auditWriter, "envoy.delete", "localhost", "grpc", hostname+" deleted (cascade)", "")

	return &pb.DeleteEnvoyResponse{}, nil
}

// ---------- ForcePush ----------

func (s *AdminService) ForcePush(ctx context.Context, req *pb.ForcePushRequest) (*pb.ForcePushResponse, error) {
	// Hub mode: forward to all spokes.
	if s.hub != nil {
		total, err := s.hub.ForwardForcePush(ctx, req.GetEnvoyId())
		if err != nil {
			return nil, status.Errorf(codes.Internal, "forwarding force push to spokes: %v", err)
		}
		return &pb.ForcePushResponse{EnvoysNotified: total}, nil
	}

	if req.EnvoyId != "" {
		// Single envoy
		if _, err := uuid.Parse(req.EnvoyId); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid envoy id: %v", err)
		}
		if err := s.queries.SetForcePush(ctx, req.EnvoyId); err != nil {
			return nil, status.Errorf(codes.Internal, "setting force push: %v", err)
		}
		// Dual-write: update FleetIndex.
		if s.fleetIndex != nil {
			s.fleetIndex.SetForcePush(req.EnvoyId, true)
		}
		return &pb.ForcePushResponse{EnvoysNotified: 1}, nil
	}

	// All non-revoked envoys — single batch UPDATE.
	affected, err := s.queries.SetForcePushAllActive(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "batch setting force push: %v", err)
	}

	// Dual-write: update FleetIndex.
	if s.fleetIndex != nil {
		s.fleetIndex.SetForcePushAll()
	}

	return &pb.ForcePushResponse{EnvoysNotified: int32(affected)}, nil
}

// ---------- Runs ----------

func (s *AdminService) ListRuns(ctx context.Context, req *pb.ListRunsRequest) (*pb.ListRunsResponse, error) {
	// Hub mode: aggregate from all spokes.
	if s.hub != nil {
		limit := req.Limit
		if limit <= 0 {
			limit = 20
		}
		runs, err := s.hub.AggregateListRuns(ctx, req.GetEnvoyId(), limit)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "aggregating runs from spokes: %v", err)
		}
		return &pb.ListRunsResponse{Runs: runs}, nil
	}

	limit := int64(req.Limit)
	if limit <= 0 {
		limit = 20
	}

	if req.EnvoyId != "" {
		if _, err := uuid.Parse(req.EnvoyId); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid envoy id: %v", err)
		}

		runs, err := s.runStore.ListRunsByEnvoy(ctx, db.ListRunsByEnvoyParams{
			EnvoyID: req.EnvoyId,
			Limit:  limit,
		})
		if err != nil {
			return nil, status.Errorf(codes.Internal, "listing runs: %v", err)
		}

		return &pb.ListRunsResponse{Runs: dbRunsToProto(runs)}, nil
	}

	runs, err := s.runStore.ListAllRuns(ctx, limit)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "listing runs: %v", err)
	}

	return &pb.ListRunsResponse{Runs: dbRunsToProto(runs)}, nil
}

func (s *AdminService) GetRun(ctx context.Context, req *pb.GetRunRequest) (*pb.GetRunResponse, error) {
	if _, err := uuid.Parse(req.Id); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid run id: %v", err)
	}

	run, err := s.runStore.GetRunByID(ctx, req.Id)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "run not found: %v", err)
	}

	results, err := s.runStore.ListRunResults(ctx, req.Id)
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
			Changed:      r.Changed != 0,
			DurationMs:   int32(r.DurationMs),
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

	slog.Info("enrollment token generated")

	return &pb.GenerateTokenResponse{
		Token: hex.EncodeToString(b),
	}, nil
}

// ---------- DryRun ----------

func (s *AdminService) DryRun(ctx context.Context, req *pb.DryRunRequest) (*pb.DryRunResponse, error) {
	if _, err := uuid.Parse(req.EnvoyId); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid envoy id: %v", err)
	}
	envoyID := req.EnvoyId

	envoy, err := s.queries.GetEnvoyByID(ctx, envoyID)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "envoy not found: %v", err)
	}

	cfg := s.configStore.Get(envoy.Hostname)
	if len(cfg.Modules) == 0 {
		return &pb.DryRunResponse{}, nil
	}

	// Serialize modules to JSON for plan storage.
	modulesJSON, err := json.Marshal(cfg.Modules)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "marshaling modules: %v", err)
	}

	plan, err := s.queries.CreatePlan(ctx, db.CreatePlanParams{
		EnvoyID:  envoyID,
		Modules: string(modulesJSON),
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
	if err := validateWebhookURL(req.Url); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid webhook URL: %v", err)
	}

	// JSON-encode events for SQLite TEXT storage.
	eventsJSON, err := json.Marshal(req.Events)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "marshaling events: %v", err)
	}

	wh, err := s.queries.CreateWebhook(ctx, db.CreateWebhookParams{
		Url:    req.Url,
		Secret: req.Secret,
		Events: string(eventsJSON),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "creating webhook: %v", err)
	}

	slog.Info("webhook created", "id", wh.ID, "url", wh.Url, "events", wh.Events)

	var events []string
	json.Unmarshal([]byte(wh.Events), &events)

	return &pb.AddWebhookResponse{
		Webhook: &pb.WebhookInfo{
			Id:        wh.ID,
			Url:       wh.Url,
			Events:    events,
			CreatedAt: timestamppb.New(wh.CreatedAt),
			Active:    wh.Active != 0,
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
		var events []string
		json.Unmarshal([]byte(wh.Events), &events)

		resp.Webhooks = append(resp.Webhooks, &pb.WebhookInfo{
			Id:        wh.ID,
			Url:       wh.Url,
			Events:    events,
			CreatedAt: timestamppb.New(wh.CreatedAt),
			Active:    wh.Active != 0,
		})
	}

	return resp, nil
}

func (s *AdminService) DeleteWebhook(ctx context.Context, req *pb.DeleteWebhookRequest) (*pb.DeleteWebhookResponse, error) {
	if _, err := uuid.Parse(req.Id); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid webhook id: %v", err)
	}

	if err := s.queries.DeleteWebhook(ctx, req.Id); err != nil {
		return nil, status.Errorf(codes.Internal, "deleting webhook: %v", err)
	}

	slog.Warn("webhook deleted", "id", req.Id)

	return &pb.DeleteWebhookResponse{}, nil
}

// ---------- Convergence ----------

func (s *AdminService) GetConvergenceSummary(ctx context.Context, _ *pb.GetConvergenceSummaryRequest) (*pb.GetConvergenceSummaryResponse, error) {
	// Hub mode: aggregate from all spokes.
	if s.hub != nil {
		return s.hub.AggregateConvergenceSummary(ctx)
	}

	// Get all envoys from DB, then look up latest runs via RunStore.
	envoys, err := s.queries.ListEnvoys(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "listing envoys: %v", err)
	}

	envoyIDs := make([]string, len(envoys))
	for i, n := range envoys {
		envoyIDs[i] = n.ID
	}

	latestRuns, err := s.runStore.LatestRunPerEnvoy(ctx, envoyIDs)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "fetching latest runs: %v", err)
	}

	counts := make(map[string]int32)
	for _, n := range envoys {
		var recent []convergence.RunInfo
		if run, ok := latestRuns[n.ID]; ok {
			recent = []convergence.RunInfo{{
				Status:       run.Status,
				ChangedCount: run.ChangedCount,
			}}
		}

		counts[convergence.ComputeConvergence(recent)]++
	}

	total := int32(len(envoys))

	// Offline envoys excluded from convergence percentage — they represent
	// powered-off or decommissioned nodes, not health problems.
	activeTotal := total - counts["offline"]
	var pct float32
	if activeTotal > 0 {
		pct = float32(counts["converged"]) / float32(activeTotal) * 100
	}

	return &pb.GetConvergenceSummaryResponse{
		Total:          total,
		Converged:      counts["converged"],
		Relapsed:       counts["relapsed"],
		Diverged:       counts["diverged"],
		Failed:         counts["failed"],
		Offline:    counts["offline"],
		NoData:         counts["no data"],
		ConvergencePct:  pct,
	}, nil
}

func (s *AdminService) GetEnvoyConvergence(ctx context.Context, req *pb.GetEnvoyConvergenceRequest) (*pb.GetEnvoyConvergenceResponse, error) {
	if _, err := uuid.Parse(req.Id); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid envoy id: %v", err)
	}
	envoyID := req.Id

	if _, err := s.queries.GetEnvoyByID(ctx, envoyID); err != nil {
		return nil, status.Errorf(codes.NotFound, "envoy not found: %v", err)
	}

	var lastRunProto *pb.Run
	runs, err := s.runStore.ListRunsByEnvoy(ctx, db.ListRunsByEnvoyParams{
		EnvoyID: envoyID,
		Limit:    int64(convergence.DivergedThreshold),
	})
	var recent []convergence.RunInfo
	if err == nil && len(runs) > 0 {
		for _, r := range runs {
			recent = append(recent, convergence.RunInfo{Status: r.Status, ChangedCount: r.ChangedCount})
		}
		lastRunProto = dbRunToProto(runs[0])
	}

	convStatus := convergence.ComputeConvergence(recent)

	// Get convergence history (last 30 entries).
	history, err := s.queries.ListConvergenceHistory(ctx, 30)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "listing convergence history: %v", err)
	}

	var snapshots []*pb.ConvergenceSnapshot
	for _, h := range history {
		snapshots = append(snapshots, &pb.ConvergenceSnapshot{
			Date:           h.RecordedAt, // already a date string from SQLite
			Total:          int32(h.Total),
			Converged:      int32(h.Converged),
			Relapsed:       int32(h.Relapsed),
			Diverged:       int32(h.Diverged),
			Failed:         int32(h.Failed),
			Offline:    int32(h.Offline),
			NoData:         int32(h.NoData),
		})
	}

	return &pb.GetEnvoyConvergenceResponse{
		Status:  convStatus,
		LastRun: lastRunProto,
		History: snapshots,
	}, nil
}

// ---------- Rollback ----------

func (s *AdminService) Rollback(ctx context.Context, req *pb.RollbackRequest) (*pb.RollbackResponse, error) {
	if _, err := uuid.Parse(req.EnvoyId); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid envoy id: %v", err)
	}
	envoyID := req.EnvoyId

	// Verify envoy exists.
	_, err := s.queries.GetEnvoyByID(ctx, envoyID)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "envoy not found: %v", err)
	}

	// If a specific file_path is provided, verify snapshots exist.
	if req.FilePath != "" {
		snapshots, err := s.queries.ListFileSnapshots(ctx, db.ListFileSnapshotsParams{
			EnvoyID:   envoyID,
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
		if _, err := uuid.Parse(req.RunId); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid run id: %v", err)
		}
		_, err := s.runStore.GetRunByID(ctx, req.RunId)
		if err != nil {
			return nil, status.Errorf(codes.NotFound, "run not found: %v", err)
		}
	}

	// Set force_push flag so agent picks up rollback on next check-in.
	if err := s.queries.SetForcePush(ctx, envoyID); err != nil {
		return nil, status.Errorf(codes.Internal, "setting force push for rollback: %v", err)
	}

	return &pb.RollbackResponse{
		Success: true,
		Message: "rollback queued; agent will apply on next check-in",
	}, nil
}

// ---------- Packages ----------

func (s *AdminService) ListPackages(ctx context.Context, req *pb.ListPackagesRequest) (*pb.ListPackagesResponse, error) {
	if req.EnvoyId != "" {
		if _, err := uuid.Parse(req.EnvoyId); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid envoy id: %v", err)
		}

		pkgs, err := s.queries.ListPackageAuditByEnvoy(ctx, req.EnvoyId)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "listing package audit: %v", err)
		}

		result := make([]*pb.PackageInfo, 0, len(pkgs))
		for _, p := range pkgs {
			pi := &pb.PackageInfo{
				EnvoyId:      p.EnvoyID,
				Hostname:    p.Hostname,
				PackageName: p.PackageName,
			}
			if p.InstalledVersion.Valid {
				pi.InstalledVersion = p.InstalledVersion.String
			}
			if p.DesiredVersion.Valid {
				pi.DesiredVersion = p.DesiredVersion.String
			}
			result = append(result, pi)
		}
		return &pb.ListPackagesResponse{Packages: result}, nil
	}

	pkgs, err := s.queries.ListAllPackageAudit(ctx, 100)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "listing package audit: %v", err)
	}

	result := make([]*pb.PackageInfo, 0, len(pkgs))
	for _, p := range pkgs {
		pi := &pb.PackageInfo{
			EnvoyId:      p.EnvoyID,
			Hostname:    p.Hostname,
			PackageName: p.PackageName,
		}
		if p.InstalledVersion.Valid {
			pi.InstalledVersion = p.InstalledVersion.String
		}
		if p.DesiredVersion.Valid {
			pi.DesiredVersion = p.DesiredVersion.String
		}
		result = append(result, pi)
	}
	return &pb.ListPackagesResponse{Packages: result}, nil
}

func (s *AdminService) ListPackageDrift(ctx context.Context, _ *pb.ListPackageDriftRequest) (*pb.ListPackageDriftResponse, error) {
	drifts, err := s.queries.ListPackageDrift(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "listing package drift: %v", err)
	}

	resp := &pb.ListPackageDriftResponse{}
	for _, d := range drifts {
		pd := &pb.PackageDrift{
			EnvoyId:      d.EnvoyID,
			Hostname:    d.Hostname,
			PackageName: d.PackageName,
			DetectedAt:  timestamppb.New(d.DetectedAt),
		}
		if d.InstalledVersion.Valid {
			pd.InstalledVersion = d.InstalledVersion.String
		}
		if d.DesiredVersion.Valid {
			pd.DesiredVersion = d.DesiredVersion.String
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

	// Compute diff by reading all envoy configs that contain the module.
	// PromoteEnv is read-only audit -- never writes config files.
	diff := s.configStore.ComputeEnvDiff(req.ModuleName, req.FromEnv, req.ToEnv)

	// Record the promotion in the database for audit trail.
	diffJSON, err := json.Marshal(diff)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "marshaling diff: %v", err)
	}

	promo, err := s.queries.CreateEnvPromotion(ctx, db.CreateEnvPromotionParams{
		ModuleName: req.ModuleName,
		FromEnv:    req.FromEnv,
		ToEnv:      req.ToEnv,
		Diff:       string(diffJSON),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "creating env promotion: %v", err)
	}

	return &pb.PromoteEnvResponse{
		PromotionId: promo.ID,
		Diff:        diff,
	}, nil
}

// ---------- SyncConfig ----------

func (s *AdminService) SyncConfig(ctx context.Context, req *pb.SyncConfigRequest) (*pb.SyncConfigResponse, error) {
	if len(req.ConfigArchive) == 0 {
		return nil, status.Errorf(codes.InvalidArgument, "config_archive is required")
	}

	configDir := s.configStore.Dir()
	if configDir == "" {
		return nil, status.Errorf(codes.FailedPrecondition, "config directory not configured")
	}

	// Extract the tar.gz archive to the config directory.
	if err := extractConfigArchive(req.ConfigArchive, configDir); err != nil {
		return nil, status.Errorf(codes.Internal, "extracting config archive: %v", err)
	}

	// Reload config to pick up the new files.
	if err := s.configStore.Reload(); err != nil {
		slog.Error("config reload after sync failed", "error", err)
		return nil, status.Errorf(codes.Internal, "config reload failed: %v", err)
	}

	slog.Info("config synced from hub", "config_dir", configDir)
	return &pb.SyncConfigResponse{}, nil
}

// extractConfigArchive extracts a tar.gz archive into the target directory.
// It clears the target directory first to ensure a clean sync, then extracts
// all files from the archive. Path traversal is validated to prevent writes
// outside the target directory.
func extractConfigArchive(archive []byte, targetDir string) error {
	// Resolve target to absolute path for traversal checks.
	absTarget, err := filepath.Abs(targetDir)
	if err != nil {
		return fmt.Errorf("resolving target dir: %w", err)
	}

	// Clear the target directory contents (but keep the directory itself).
	entries, err := os.ReadDir(absTarget)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading target dir: %w", err)
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(absTarget, e.Name())); err != nil {
			return fmt.Errorf("clearing target dir: %w", err)
		}
	}

	gr, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return fmt.Errorf("opening gzip: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("reading tar: %w", err)
		}

		// Validate path: no absolute paths, no ".." traversal.
		clean := filepath.Clean(header.Name)
		if filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
			return fmt.Errorf("path traversal detected: %s", header.Name)
		}

		dest := filepath.Join(absTarget, clean)
		// Double-check the resolved path is within the target directory.
		if !strings.HasPrefix(dest, absTarget+string(filepath.Separator)) && dest != absTarget {
			return fmt.Errorf("path traversal detected: %s resolves outside target", header.Name)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dest, 0o750); err != nil {
				return fmt.Errorf("creating dir %s: %w", clean, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
				return fmt.Errorf("creating parent dir for %s: %w", clean, err)
			}
			f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode)&0o640|0o400)
			if err != nil {
				return fmt.Errorf("creating file %s: %w", clean, err)
			}
			// Limit extraction size to prevent zip bombs (64 MiB per file).
			if _, err := io.Copy(f, io.LimitReader(tr, 64<<20)); err != nil {
				f.Close()
				return fmt.Errorf("writing file %s: %w", clean, err)
			}
			f.Close()
		default:
			// Skip symlinks, hard links, etc. for security.
			continue
		}
	}

	return nil
}

// ---------- ReportSpokeStatus ----------

func (s *AdminService) ReportSpokeStatus(ctx context.Context, req *pb.SpokeStatusReport) (*pb.SpokeStatusAck, error) {
	if req.SpokeId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "spoke_id is required")
	}

	// Only the hub stores spoke reports.
	if s.hub == nil {
		return nil, status.Errorf(codes.FailedPrecondition, "server is not a hub")
	}

	s.hub.RecordSpokeReport(req)

	slog.Debug("received spoke status report",
		"spoke_id", req.SpokeId,
		"envoy_count", req.EnvoyCount,
		"converged", req.Converged,
	)

	return &pb.SpokeStatusAck{}, nil
}

// ---------- Helpers ----------

func dbEnvoyToProto(n db.Envoy) *pb.Envoy {
	pn := &pb.Envoy{
		Id:         n.ID,
		Hostname:   n.Hostname,
		IpAddress:  n.IpAddress,
		EnrolledAt: timestamppb.New(n.EnrolledAt),
		Revoked:    n.Revoked != 0,
	}
	if n.LastSeen.Valid {
		pn.LastSeen = timestamppb.New(n.LastSeen.Time)
	}
	if n.RevokedAt.Valid {
		pn.RevokedAt = timestamppb.New(n.RevokedAt.Time)
	}
	if n.PendingRotatedSecrets != "" && n.PendingRotatedSecrets != "[]" {
		var paths []string
		if err := json.Unmarshal([]byte(n.PendingRotatedSecrets), &paths); err == nil {
			pn.PendingRotatedSecrets = paths
		}
	}
	return pn
}

func dbRunToProto(r db.Run) *pb.Run {
	pr := &pb.Run{
		Id:           r.ID,
		EnvoyId:       r.EnvoyID,
		StartedAt:    timestamppb.New(r.StartedAt),
		Status:       r.Status,
		ModuleCount:  int32(r.ModuleCount),
		ChangedCount: int32(r.ChangedCount),
		FailedCount:  int32(r.FailedCount),
		SkippedCount: int32(r.SkippedCount),
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

// validateWebhookURL checks that a webhook URL is safe to deliver to.
func validateWebhookURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("malformed URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsupported scheme %q (must be http or https)", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("missing host")
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("cannot resolve host %q: %w", host, err)
	}
	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			return fmt.Errorf("host %q resolves to private/loopback address %s", host, ip)
		}
	}
	return nil
}
