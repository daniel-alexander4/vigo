// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/convergence"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/integrations"
	"git.alexander4.io/vigo/server/spanner"
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
	// Dispatch (Slice 2-ii-a):
	//   (a) spannerAdmin, fanout → live fan-out to every roster bolt
	//   (b) spannerAdmin, local  → aggregate from the observability cache
	//   (c) standalone           → the pure-local path below
	if s.spannerAdmin != nil {
		if s.spannerAdmin.Fanout() {
			envoys, err := s.spannerAdmin.AggregateEnvoysFanout(ctx)
			if err != nil {
				return nil, status.Errorf(codes.Internal, "fanning out envoys to bolts: %v", err)
			}
			// Fold in the local bolt's own envoys (fanout queries
			// peers; the local bolt is not a peer of itself).
			envoys = append(envoys, s.localEnvoysProto()...)
			return &pb.ListEnvoysResponse{Envoys: envoys}, nil
		}
		// local path: (local fleetIndex) + (every cached peer).
		lite, _ := s.spannerAdmin.AggregateEnvoysLocal()
		return &pb.ListEnvoysResponse{Envoys: envoyLitesToProto(lite)}, nil
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

	// Look up hostname before flipping revoked-bit so the swarm cleanup
	// hook + integrations event can use the friendly name.
	hostname := req.Id
	if s.fleetIndex != nil {
		if ts, ok, _ := s.fleetIndex.GetEnvoyLite(req.Id); ok {
			hostname = ts.Hostname
		}
	}

	if err := s.queries.RevokeEnvoy(ctx, req.Id); err != nil {
		return nil, status.Errorf(codes.Internal, "revoking envoy: %v", err)
	}

	// Dual-write: update FleetIndex.
	if s.fleetIndex != nil {
		s.fleetIndex.SetRevoked(req.Id, true)
	}

	slog.Warn("envoy revoked", "envoy_id", req.Id, "hostname", hostname)

	// Cross-envoy lockbox cleanup: drop the revoked host's pubkey
	// from every other envoy's per-user recipients list so future
	// re-encrypts no longer encrypt to a host that can't check in.
	// Skipped on operator opt-out (req.SkipSwarmCleanup) for cases
	// where the operator plans to re-bootstrap the same host shortly.
	// Best-effort — a dispatch failure doesn't roll back the revoke.
	if s.swarmCleanup != nil && !req.GetSkipSwarmCleanup() && hostname != "" {
		if err := s.swarmCleanup(ctx, hostname); err != nil {
			slog.Warn("envoy revoke: lockbox cleanup dispatch failed",
				"hostname", hostname, "error", err)
		}
	}

	// Record tamper-evident audit event.
	summary := fmt.Sprintf("%s revoked (cleanup=%v)", hostname, !req.GetSkipSwarmCleanup())
	recordAudit(ctx, s.auditWriter, "envoy.revoke", "localhost", "grpc", summary, "")

	// Dispatch envoy.revoked event.
	if s.integrations != nil {
		s.integrations.Dispatch(integrations.Event{
			Name:      "envoy.revoked",
			Timestamp: time.Now(),
			Hostname:  hostname,
			EnvoyID:   req.Id,
			Summary:   fmt.Sprintf("%s revoked", hostname),
		})
	}

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

	// Same lockbox cleanup as RevokeEnvoy. Schema-delete is even
	// more destructive than revoke (CASCADE drops traits/runs/snapshots),
	// so leaving stale recipient entries on peers makes no sense.
	if s.swarmCleanup != nil && !req.GetSkipSwarmCleanup() && hostname != "" && hostname != req.Id {
		if err := s.swarmCleanup(ctx, hostname); err != nil {
			slog.Warn("envoy delete: lockbox cleanup dispatch failed",
				"hostname", hostname, "error", err)
		}
	}

	// Record tamper-evident audit event (CASCADE deletes traits, runs, snapshots).
	summary := fmt.Sprintf("%s deleted (cascade, cleanup=%v)", hostname, !req.GetSkipSwarmCleanup())
	recordAudit(ctx, s.auditWriter, "envoy.delete", "localhost", "grpc", summary, "")

	return &pb.DeleteEnvoyResponse{}, nil
}

// ---------- ForcePush ----------

func (s *AdminService) ForcePush(ctx context.Context, req *pb.ForcePushRequest) (*pb.ForcePushResponse, error) {
	if req.EnvoyId != "" {
		// Single envoy
		if _, err := uuid.Parse(req.EnvoyId); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid envoy id: %v", err)
		}

		// Spanner peer-equal force-push routing (Slice 2-ii-b). If the
		// cached roster shows a remote peer serves this envoy, route the
		// ForcePush to that bolt via a direct admin RPC — mirrors how
		// ListRuns(envoy_id) routes to the owning bolt via RunsFromBolt.
		// A local (or unknown) envoy uses the local flag-set path below.
		if s.spannerAdmin != nil {
			if _, hostname, remote := s.spannerAdmin.OwningBoltHostname(ctx, req.EnvoyId); remote && hostname != "" {
				count, err := s.spannerAdmin.ForcePushToBolt(ctx, hostname, req.EnvoyId)
				if err != nil {
					return nil, status.Errorf(codes.Internal, "force push on owning bolt: %v", err)
				}
				return &pb.ForcePushResponse{EnvoysNotified: count}, nil
			}
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
	// Spanner peer-equal run reads (Slice 2-ii-a). Runs are NEVER
	// gossiped — so there is no observability-cache path for runs:
	//   - ListRuns(envoy_id): resolve the envoy's owning bolt from
	//     the cached roster; if a remote peer serves it, gRPC ListRuns
	//     to that bolt; if local (or unknown), the local path below.
	//   - ListRuns() global: always a client-side parallel fan-out to
	//     every roster bolt, merged with the local bolt's runs.
	//     fallback_admin gates only the cacheable reads
	//     (envoys/convergence); runs have no cache, so fan-out is
	//     their sole fleet-wide path.
	if s.spannerAdmin != nil {
		spLimit := req.Limit
		if spLimit <= 0 {
			spLimit = 20
		}
		if req.EnvoyId != "" {
			if _, err := uuid.Parse(req.EnvoyId); err != nil {
				return nil, status.Errorf(codes.InvalidArgument, "invalid envoy id: %v", err)
			}
			if _, hostname, remote := s.spannerAdmin.OwningBoltHostname(ctx, req.EnvoyId); remote && hostname != "" {
				runs, err := s.spannerAdmin.RunsFromBolt(ctx, hostname, req.EnvoyId, spLimit)
				if err != nil {
					return nil, status.Errorf(codes.Internal, "fetching runs from owning bolt: %v", err)
				}
				return &pb.ListRunsResponse{Runs: runs}, nil
			}
			// Local (or unknown) envoy — fall through to the local path.
		} else {
			runs, err := s.spannerAdmin.AggregateRunsFanout(ctx, "", spLimit)
			if err != nil {
				return nil, status.Errorf(codes.Internal, "fanning out runs to bolts: %v", err)
			}
			// Fold in the local bolt's own runs.
			local, lErr := s.runStore.ListAllRuns(ctx, int64(spLimit))
			if lErr != nil {
				return nil, status.Errorf(codes.Internal, "listing local runs: %v", lErr)
			}
			runs = append(runs, dbRunsToProto(local)...)
			return &pb.ListRunsResponse{Runs: runs}, nil
		}
		// Per-envoy local/unknown envoys fall through to the pure-local path.
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
			Limit:   limit,
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
		EnvoyID: envoyID,
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

// ---------- Webhooks (removed — use native integrations in server.yaml) ----------

func (s *AdminService) AddWebhook(ctx context.Context, _ *pb.AddWebhookRequest) (*pb.AddWebhookResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "webhooks removed — configure native integrations in server.yaml")
}

func (s *AdminService) ListWebhooks(ctx context.Context, _ *pb.ListWebhooksRequest) (*pb.ListWebhooksResponse, error) {
	return &pb.ListWebhooksResponse{}, nil
}

func (s *AdminService) DeleteWebhook(ctx context.Context, _ *pb.DeleteWebhookRequest) (*pb.DeleteWebhookResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "webhooks removed — configure native integrations in server.yaml")
}

// ---------- Convergence ----------

func (s *AdminService) GetConvergenceSummary(ctx context.Context, _ *pb.GetConvergenceSummaryRequest) (*pb.GetConvergenceSummaryResponse, error) {
	// Dispatch (Slice 2-ii-a) — same three-way shape as ListEnvoys.
	if s.spannerAdmin != nil {
		if s.spannerAdmin.Fanout() {
			agg, err := s.spannerAdmin.AggregateConvergenceFanout(ctx)
			if err != nil {
				return nil, status.Errorf(codes.Internal, "fanning out convergence to bolts: %v", err)
			}
			// Fold in the local bolt's own counters — fanout queries
			// peers; the local bolt is not a peer of itself.
			foldLocalConvergence(agg, s.localConvergenceCounters())
			finalizeConvergencePct(agg)
			return agg, nil
		}
		// local path: (local fleetIndex) + (every cached peer);
		// snapshot_age_seconds carries the oldest contributing peer.
		counters, oldest := s.spannerAdmin.AggregateConvergenceLocal()
		resp := convergenceCountersToProto(counters)
		if !oldest.IsZero() {
			resp.SnapshotAgeSeconds = int64(time.Since(oldest).Seconds())
		}
		finalizeConvergencePct(resp)
		return resp, nil
	}

	// Standalone path — no hub, no spanner. Source the fleet-wide
	// counters from the canonical fleet index (the convergence
	// single-source-of-truth) via the same helper + converter the
	// spanner-local and fanout paths use, so all three paths agree.
	resp := convergenceCountersToProto(s.localConvergenceCounters())
	finalizeConvergencePct(resp)
	return resp, nil
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
		Limit:   int64(convergence.DivergedThreshold),
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
			Date:      h.RecordedAt, // already a date string from SQLite
			Total:     int32(h.Total),
			Converged: int32(h.Converged),
			Degraded:  int32(h.Degraded),
			Failed:    int32(h.Failed),
			NoData:    int32(h.NoData),
			Changed:   int32(h.Changed),
			Diverged:  int32(h.Diverged),
			Offline:   int32(h.Offline),
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
			EnvoyID:  envoyID,
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
				EnvoyId:     p.EnvoyID,
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
			EnvoyId:     p.EnvoyID,
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
			EnvoyId:     d.EnvoyID,
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

// ---------- Spanner peer-equal admin-read helpers (Slice 2-ii-a) ----------

// envoyLiteToProto projects one gossiped spanner.EnvoyLite into the
// *pb.Envoy wire shape — the same shape dbEnvoyToProto produces, so
// the local and cache-aggregate paths return identically-shaped
// responses.
func envoyLiteToProto(e spanner.EnvoyLite) *pb.Envoy {
	pe := &pb.Envoy{
		Id:           e.ID,
		Hostname:     e.Hostname,
		IpAddress:    e.IP,
		Revoked:      e.Revoked,
		AgentVersion: e.AgentVersion,
	}
	if e.EnrolledAtMs > 0 {
		pe.EnrolledAt = timestamppb.New(time.UnixMilli(e.EnrolledAtMs))
	}
	if e.LastSeenMs > 0 {
		pe.LastSeen = timestamppb.New(time.UnixMilli(e.LastSeenMs))
	}
	return pe
}

// envoyLitesToProto projects a slice of spanner.EnvoyLite.
func envoyLitesToProto(lites []spanner.EnvoyLite) []*pb.Envoy {
	out := make([]*pb.Envoy, 0, len(lites))
	for _, e := range lites {
		out = append(out, envoyLiteToProto(e))
	}
	return out
}

// localEnvoysProto returns the local bolt's own envoy roster as
// []*pb.Envoy — folded into the fanout path's merged result (fanout
// queries peers; the local bolt is not a peer of itself).
func (s *AdminService) localEnvoysProto() []*pb.Envoy {
	if s.fleetIndex == nil {
		return nil
	}
	envoys := s.fleetIndex.AllEnvoys()
	out := make([]*pb.Envoy, 0, len(envoys))
	for _, es := range envoys {
		pe := &pb.Envoy{
			Id:           es.ID,
			Hostname:     es.Hostname,
			IpAddress:    es.IP,
			Revoked:      es.Revoked,
			AgentVersion: es.AgentVersion,
		}
		if !es.EnrolledAt.IsZero() {
			pe.EnrolledAt = timestamppb.New(es.EnrolledAt)
		}
		if !es.LastSeen.IsZero() {
			pe.LastSeen = timestamppb.New(es.LastSeen)
		}
		out = append(out, pe)
	}
	return out
}

// localConvergenceCounters returns the local bolt's own convergence
// counters as a spanner.ConvergenceCounters.
func (s *AdminService) localConvergenceCounters() spanner.ConvergenceCounters {
	if s.fleetIndex == nil {
		return spanner.ConvergenceCounters{}
	}
	c := s.fleetIndex.ConvergenceSummary()
	return spanner.ConvergenceCounters{
		Total:     c.Total,
		Converged: c.Converged,
		Degraded:  c.Degraded,
		Failed:    c.Failed,
		NoData:    c.NoData,
		Changed:   c.Changed,
		Diverged:  c.Diverged,
		Offline:   c.Offline,
	}
}

// convergenceCountersToProto projects a spanner.ConvergenceCounters
// into a *pb.GetConvergenceSummaryResponse. The Relapsed field has no
// counterpart in the counters axes (it is a legacy proto field) and is
// left zero. ConvergencePct is filled by finalizeConvergencePct.
func convergenceCountersToProto(c spanner.ConvergenceCounters) *pb.GetConvergenceSummaryResponse {
	return &pb.GetConvergenceSummaryResponse{
		Total:     int32(c.Total),
		Converged: int32(c.Converged),
		Failed:    int32(c.Failed),
		Offline:   int32(c.Offline),
		NoData:    int32(c.NoData),
		Diverged:  int32(c.Diverged),
	}
}

// foldLocalConvergence adds the local bolt's own counters into a
// fanout aggregate so the response covers the whole spanner.
func foldLocalConvergence(agg *pb.GetConvergenceSummaryResponse, local spanner.ConvergenceCounters) {
	agg.Total += int32(local.Total)
	agg.Converged += int32(local.Converged)
	agg.Failed += int32(local.Failed)
	agg.Offline += int32(local.Offline)
	agg.NoData += int32(local.NoData)
	agg.Diverged += int32(local.Diverged)
}

// finalizeConvergencePct computes ConvergencePct from the aggregate's
// counters — Offline envoys excluded from the denominator, matching
// the local and hub paths.
func finalizeConvergencePct(resp *pb.GetConvergenceSummaryResponse) {
	activeTotal := resp.Total - resp.Offline
	if activeTotal > 0 {
		resp.ConvergencePct = float32(resp.Converged) / float32(activeTotal) * 100
	}
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
		EnvoyId:      r.EnvoyID,
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
