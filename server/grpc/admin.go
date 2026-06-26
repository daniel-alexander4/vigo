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
	"git.alexander4.io/vigo/server/fleet"
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

// listEnvoysDefaultLimit is the page size when the caller passes limit=0
// (or omits it). Sized to keep the JSON response well under grpc-gateway's
// 4 MB max-message — at ~750 bytes per envoy JSON encoding, 500 rows is
// ~375 KB, comfortably under the limit with plenty of headroom for trait
// growth.
const listEnvoysDefaultLimit = 500

// listEnvoysMaxLimit caps the page size operators can request. Above this
// the response can hit grpc-gateway's 4 MB ceiling depending on per-envoy
// JSON size (envoys with rich traits encode larger). 5000 is the operator
// override ceiling — beyond that, walk pages.
const listEnvoysMaxLimit = 5000

// resolveListEnvoysPaging normalizes the request's limit/offset against
// the server's defaults and max-cap. Returns the effective limit + offset
// to use for slicing/querying.
func resolveListEnvoysPaging(req *pb.ListEnvoysRequest) (limit, offset int) {
	limit = int(req.GetLimit())
	if limit <= 0 {
		limit = listEnvoysDefaultLimit
	}
	if limit > listEnvoysMaxLimit {
		limit = listEnvoysMaxLimit
	}
	offset = int(req.GetOffset())
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

// sliceEnvoysPage applies offset + limit to a pre-fetched envoy slice and
// returns (page, total). Used by the spanner dispatch paths where the
// full list is already in memory from the aggregation step — pagination
// across federated bolts is a v2 lever.
func sliceEnvoysPage(envoys []*pb.Envoy, limit, offset int) ([]*pb.Envoy, int) {
	total := len(envoys)
	if offset >= total {
		return nil, total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return envoys[offset:end], total
}

func (s *AdminService) ListEnvoys(ctx context.Context, req *pb.ListEnvoysRequest) (*pb.ListEnvoysResponse, error) {
	limit, offset := resolveListEnvoysPaging(req)

	// Dispatch (Slice 2-ii-a):
	//   (a) spannerAdmin, fanout → live fan-out to every roster bolt
	//   (b) spannerAdmin, local  → aggregate from the observability cache
	//   (c) standalone           → the pure-local path below
	//
	// Both spanner paths return aggregated in-memory lists and paginate
	// in Go (cross-bolt pagination would require coordinated offset
	// state across peers — out of scope for v1). The standalone path
	// paginates at the DB.
	if s.spannerAdmin != nil {
		var envoys []*pb.Envoy
		if s.spannerAdmin.Fanout() {
			fanout, err := s.spannerAdmin.AggregateEnvoysFanout(ctx)
			if err != nil {
				return nil, status.Errorf(codes.Internal, "fanning out envoys to bolts: %v", err)
			}
			envoys = append(fanout, s.localEnvoysProto()...)
		} else {
			lite, _ := s.spannerAdmin.AggregateEnvoysLocal()
			envoys = envoyLitesToProto(lite)
		}
		page, total := sliceEnvoysPage(envoys, limit, offset)
		return buildListEnvoysResponse(page, total, limit, offset), nil
	}

	var total int64
	var err error
	if req.GetIncludeRevoked() {
		total, err = s.queries.CountEnvoys(ctx)
	} else {
		total, err = s.queries.CountActiveEnvoys(ctx)
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "counting envoys: %v", err)
	}

	var envoys []db.Envoy
	if req.GetIncludeRevoked() {
		envoys, err = s.queries.ListEnvoysOffset(ctx, db.ListEnvoysOffsetParams{
			Limit:  int64(limit),
			Offset: int64(offset),
		})
	} else {
		envoys, err = s.queries.ListActiveEnvoysOffset(ctx, db.ListActiveEnvoysOffsetParams{
			Limit:  int64(limit),
			Offset: int64(offset),
		})
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "listing envoys: %v", err)
	}

	page := make([]*pb.Envoy, 0, len(envoys))
	for _, n := range envoys {
		pt := dbEnvoyToProto(n)
		if s.fleetIndex != nil {
			if ts, ok, _ := s.fleetIndex.GetEnvoyLite(n.ID); ok {
				pt.AgentVersion = ts.AgentVersion
			}
		}
		page = append(page, pt)
	}

	return buildListEnvoysResponse(page, int(total), limit, offset), nil
}

// buildListEnvoysResponse fills the pagination envelope. has_more is true
// iff the page didn't reach the end of the matching set; next_offset
// points to the first row of the next page (0 when there isn't one).
func buildListEnvoysResponse(page []*pb.Envoy, total, limit, offset int) *pb.ListEnvoysResponse {
	end := offset + len(page)
	hasMore := end < total
	nextOffset := int32(0)
	if hasMore {
		nextOffset = int32(end)
	}
	return &pb.ListEnvoysResponse{
		Envoys:     page,
		Total:      int32(total),
		Offset:     int32(offset),
		Limit:      int32(limit),
		HasMore:    hasMore,
		NextOffset: nextOffset,
	}
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

	// Spanner: route to the owning bolt when a remote peer serves this
	// envoy. Without this, a revoke on the wrong bolt is a silent 0-row
	// no-op (the envoys list is fleet-wide but the row lives on one bolt).
	if s.spannerAdmin != nil {
		if _, ownerHost, remote := s.spannerAdmin.OwningBoltHostname(ctx, req.Id); remote && ownerHost != "" {
			if err := s.spannerAdmin.RevokeEnvoyToBolt(ctx, ownerHost, req.Id, req.GetSkipSwarmCleanup()); err != nil {
				return nil, status.Errorf(codes.Internal, "revoke on owning bolt: %v", err)
			}
			return &pb.RevokeEnvoyResponse{}, nil
		}
	}

	// Look up hostname before flipping revoked-bit so the swarm cleanup
	// hook + integrations event can use the friendly name. A miss here
	// (with the index present) means no bolt — local or peer — serves this
	// envoy: fail loudly rather than silently revoke 0 rows.
	hostname := req.Id
	if s.fleetIndex != nil {
		ts, ok, _ := s.fleetIndex.GetEnvoyLite(req.Id)
		if !ok {
			return nil, status.Errorf(codes.NotFound, "envoy %s not found on this bolt", req.Id)
		}
		hostname = ts.Hostname
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

	// Spanner: route to the owning bolt when a remote peer serves this
	// envoy. Without this, a delete on the wrong bolt is a silent 0-row
	// no-op (the envoys list is fleet-wide but the row lives on one bolt).
	if s.spannerAdmin != nil {
		if _, ownerHost, remote := s.spannerAdmin.OwningBoltHostname(ctx, req.Id); remote && ownerHost != "" {
			if err := s.spannerAdmin.DeleteEnvoyToBolt(ctx, ownerHost, req.Id, req.GetSkipSwarmCleanup()); err != nil {
				return nil, status.Errorf(codes.Internal, "delete on owning bolt: %v", err)
			}
			return &pb.DeleteEnvoyResponse{}, nil
		}
	}

	// Look up hostname before deleting for the audit trail. A miss here
	// (with the index present) means no bolt — local or peer — serves this
	// envoy: fail loudly rather than silently delete 0 rows.
	hostname := req.Id
	if s.fleetIndex != nil {
		t := s.fleetIndex.GetEnvoy(req.Id)
		if t == nil {
			return nil, status.Errorf(codes.NotFound, "envoy %s not found on this bolt", req.Id)
		}
		hostname = t.Hostname
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

	protoResults := make([]*pb.ResourceRunResult, 0, len(results))
	for _, r := range results {
		mr := &pb.ResourceRunResult{
			ConfigcrateName: r.ConfigcrateName,
			ResourceType:    r.ResourceType,
			ResourceName:    r.ResourceName,
			ActionTaken:     r.ActionTaken,
			Changed:         r.Changed != 0,
			DurationMs:      int32(r.DurationMs),
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
	if len(cfg.Configcrates) == 0 {
		return &pb.DryRunResponse{}, nil
	}

	// Serialize modules to JSON for plan storage.
	configcratesJSON, err := json.Marshal(cfg.Configcrates)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "marshaling configcrates: %v", err)
	}

	plan, err := s.queries.CreatePlan(ctx, db.CreatePlanParams{
		EnvoyID:      envoyID,
		Configcrates: string(configcratesJSON),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "creating plan: %v", err)
	}

	// Build planned actions from config modules.
	var actions []*pb.PlannedAction
	for _, mod := range cfg.Configcrates {
		for _, res := range mod.Resources {
			action := "create" // default action for dry-run
			pr, err := s.queries.CreatePlanResult(ctx, db.CreatePlanResultParams{
				PlanID:          plan.ID,
				ConfigcrateName: mod.Name,
				ResourceType:    res.Type,
				ResourceName:    res.Name,
				PlannedAction:   action,
			})
			if err != nil {
				return nil, status.Errorf(codes.Internal, "creating plan result: %v", err)
			}
			actions = append(actions, &pb.PlannedAction{
				ConfigcrateName: pr.ConfigcrateName,
				ResourceType:    pr.ResourceType,
				ResourceName:    pr.ResourceName,
				PlannedAction:   pr.PlannedAction,
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

// GetSecuritySummary returns THIS bolt's security-posture rollup (ADR-037
// slice 2). It is a leaf read — it never fans out, so the federated-UI
// viewer can fan it out to every roster bolt without recursion. The
// security computation lives on fleet.DataSource; build one over this
// bolt's own index/stores (NewDataSource is cheap — lazy caches) and
// convert to the wire form via the spanner federation type.
func (s *AdminService) GetSecuritySummary(_ context.Context, _ *pb.GetSecuritySummaryRequest) (*pb.GetSecuritySummaryResponse, error) {
	if s.fleetIndex == nil {
		return &pb.GetSecuritySummaryResponse{}, nil
	}
	ds := fleet.NewDataSource(s.fleetIndex, s.queries, s.runStore, s.configStore)
	return spanner.SecuritySummaryToProto(ds.SecuritySummary().ToSpanner()), nil
}

// GetRiskSummary returns THIS bolt's risk-posture rollup (ADR-037 slice
// 3) — a leaf read that never fans out, so the federated viewer can fan
// it to every roster bolt without recursion.
func (s *AdminService) GetRiskSummary(_ context.Context, _ *pb.GetRiskSummaryRequest) (*pb.GetRiskSummaryResponse, error) {
	if s.fleetIndex == nil {
		return &pb.GetRiskSummaryResponse{}, nil
	}
	ds := fleet.NewDataSource(s.fleetIndex, s.queries, s.runStore, s.configStore)
	return spanner.RiskSummaryToProto(ds.RiskSummary(s.baseInterval, s.offlineThreshold).ToSpanner()), nil
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
			recent = append(recent, convergence.RunInfo{Status: r.Status, ChangedCount: r.ChangedCount, FailedCount: r.FailedCount, ResourceCount: r.ResourceCount})
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
// into a *pb.GetConvergenceSummaryResponse — the full canonical model:
// failure axis (converged/degraded/failed/no_data, sums to total), drift
// axis (changed/diverged), reachability (offline). ConvergencePct is
// filled by finalizeConvergencePct.
func convergenceCountersToProto(c spanner.ConvergenceCounters) *pb.GetConvergenceSummaryResponse {
	return &pb.GetConvergenceSummaryResponse{
		Total:     int32(c.Total),
		Converged: int32(c.Converged),
		Degraded:  int32(c.Degraded),
		Failed:    int32(c.Failed),
		NoData:    int32(c.NoData),
		Changed:   int32(c.Changed),
		Diverged:  int32(c.Diverged),
		Offline:   int32(c.Offline),
	}
}

// foldLocalConvergence adds the local bolt's own counters into a
// fanout aggregate so the response covers the whole spanner.
func foldLocalConvergence(agg *pb.GetConvergenceSummaryResponse, local spanner.ConvergenceCounters) {
	agg.Total += int32(local.Total)
	agg.Converged += int32(local.Converged)
	agg.Degraded += int32(local.Degraded)
	agg.Failed += int32(local.Failed)
	agg.NoData += int32(local.NoData)
	agg.Changed += int32(local.Changed)
	agg.Diverged += int32(local.Diverged)
	agg.Offline += int32(local.Offline)
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
		Id:            r.ID,
		EnvoyId:       r.EnvoyID,
		StartedAt:     timestamppb.New(r.StartedAt),
		Status:        r.Status,
		ResourceCount: int32(r.ResourceCount),
		ChangedCount:  int32(r.ChangedCount),
		FailedCount:   int32(r.FailedCount),
		SkippedCount:  int32(r.SkippedCount),
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
