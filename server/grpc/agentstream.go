// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"github.com/google/uuid"

	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/freshness"
	"git.alexander4.io/vigo/server/registry"
	"git.alexander4.io/vigo/server/swarm"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// AgentStream handles a persistent bidirectional stream with an agent.
// The first message must be a CheckInRequest (handshake). After that, the
// agent can send check-in, report-result, or report-traits messages. The
// server pushes policy changes and force-push signals asynchronously.
func (s *AgentService) AgentStream(stream pb.VigoAgent_AgentStreamServer) error {
	// Read the first message — must be a check-in (handshake).
	firstMsg, err := stream.Recv()
	if err != nil {
		return status.Error(codes.InvalidArgument, "expected initial check-in message")
	}

	checkin := firstMsg.GetCheckIn()
	if checkin == nil {
		return status.Error(codes.InvalidArgument, "first stream message must be check_in")
	}

	if _, err := uuid.Parse(checkin.GetEnvoyId()); err != nil {
		return status.Errorf(codes.InvalidArgument, "invalid envoy_id: %v", err)
	}
	envoyID := checkin.GetEnvoyId()

	// Validate envoy + look up state in one lock acquisition.
	var envoy db.Envoy
	if s.fleetIndex != nil {
		ts, exists, revoked := s.fleetIndex.GetEnvoyLite(envoyID)
		if !exists {
			return status.Error(codes.NotFound, "envoy not found")
		}
		if revoked {
			return status.Error(codes.PermissionDenied, "envoy is revoked")
		}
		envoy = fleetEnvoyToDB(ts)
	} else {
		if err := s.registry.ValidateEnvoy(stream.Context(), envoyID); err != nil {
			if errors.Is(err, registry.ErrEnvoyRevoked) {
				return status.Error(codes.PermissionDenied, "envoy is revoked")
			}
			if errors.Is(err, registry.ErrEnvoyNotFound) {
				return status.Error(codes.NotFound, "envoy not found")
			}
			return status.Errorf(codes.Internal, "validating envoy: %v", err)
		}
		var err error
		envoy, err = s.lookupEnvoy(stream.Context(), envoyID)
		if err != nil {
			return err
		}
	}

	// Register the stream for push notifications.
	as, cleanup := s.streamMgr.register(envoyID, envoy.Hostname)
	defer cleanup()
	// Persist final last_seen to DB when the stream closes so the timestamp
	// survives server restart (streaming envoys skip DB writes mid-stream).
	defer func() {
		if s.fleetIndex != nil {
			s.fleetIndex.MarkSeen(envoyID, time.Now())
		}
	}()

	// Send initial policy response. Trace_id is attached to the ctx so the
	// handler reuses the value from the handshake (ADR-028) rather than
	// generating a fresh one.
	handshakeCtx, handshakeTraceID := EnsureTraceID(stream.Context(), checkin.GetTraceparent())
	resp, err := s.buildCheckInResponse(handshakeCtx, envoyID, envoy, checkin, true)
	if err != nil {
		return err
	}
	if err := stream.Send(&pb.ServerStreamMessage{
		Msg: &pb.ServerStreamMessage_CheckInResponse{
			CheckInResponse: resp,
		},
	}); err != nil {
		return err
	}

	s.metrics.CheckinsTotal.WithLabelValues("stream_connect").Inc()
	slog.Info("agent stream established",
		"envoy_id", envoyID,
		"hostname", envoy.Hostname,
		"trace_id", handshakeTraceID,
	)

	// Two goroutines: one reads from agent, one writes push messages.
	errCh := make(chan error, 2)

	// Writer: forward push messages from streamManager to gRPC stream.
	go func() {
		for {
			select {
			case msg := <-as.send:
				if err := stream.Send(msg); err != nil {
					errCh <- err
					return
				}
			case <-as.done:
				errCh <- nil
				return
			case <-stream.Context().Done():
				errCh <- stream.Context().Err()
				return
			}
		}
	}()

	// Reader: process incoming agent messages.
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				if errors.Is(err, io.EOF) {
					errCh <- nil
				} else {
					errCh <- err
				}
				return
			}

			switch m := msg.GetMsg().(type) {
			case *pb.AgentStreamMessage_CheckIn:
				ctx, traceID := EnsureTraceID(stream.Context(), m.CheckIn.GetTraceparent())
				resp, err := s.buildCheckInResponse(ctx, envoyID, envoy, m.CheckIn, true)
				if err != nil {
					slog.Error("stream check-in error", "envoy_id", envoyID, "trace_id", traceID, "error", err)
					s.metrics.AgentStreamHandlerErrors.WithLabelValues("check_in").Inc()
					continue
				}
				if err := stream.Send(&pb.ServerStreamMessage{
					Msg: &pb.ServerStreamMessage_CheckInResponse{
						CheckInResponse: resp,
					},
				}); err != nil {
					errCh <- err
					return
				}

			case *pb.AgentStreamMessage_ReportResult:
				// Authorization check for proxied device results: if the
				// result's envoy_id differs from the stream owner, verify
				// the sender is the authorized gateway for that device.
				resultEnvoyID := m.ReportResult.GetEnvoyId()
				if resultEnvoyID != envoyID {
					if !isAuthorizedGateway(s.configStore, resultEnvoyID, envoy.Hostname) {
						slog.Warn("rejected proxied result from unauthorized gateway",
							"device_envoy_id", resultEnvoyID,
							"gateway_envoy_id", envoyID,
							"gateway_hostname", envoy.Hostname,
						)
						continue
					}
				}

				ctx, traceID := EnsureTraceID(stream.Context(), m.ReportResult.GetTraceparent())
				resp, err := s.ReportResult(ctx, m.ReportResult)
				if err != nil {
					slog.Error("stream report-result error", "envoy_id", envoyID, "trace_id", traceID, "error", err)
					s.metrics.AgentStreamHandlerErrors.WithLabelValues("report_result").Inc()
					continue
				}
				if err := stream.Send(&pb.ServerStreamMessage{
					Msg: &pb.ServerStreamMessage_ReportResultAck{
						ReportResultAck: resp,
					},
				}); err != nil {
					errCh <- err
					return
				}

			case *pb.AgentStreamMessage_ReportTraits:
				ctx, traceID := EnsureTraceID(stream.Context(), m.ReportTraits.GetTraceparent())
				resp, err := s.ReportTraits(ctx, m.ReportTraits)
				if err != nil {
					slog.Error("stream report-traits error", "envoy_id", envoyID, "trace_id", traceID, "error", err)
					s.metrics.AgentStreamHandlerErrors.WithLabelValues("report_traits").Inc()
					continue
				}
				if err := stream.Send(&pb.ServerStreamMessage{
					Msg: &pb.ServerStreamMessage_ReportTraitsAck{
						ReportTraitsAck: resp,
					},
				}); err != nil {
					errCh <- err
					return
				}

			case *pb.AgentStreamMessage_TaskAck:
				s.taskRouter.handleTaskAck(stream.Context(), envoyID, m.TaskAck)
				if !m.TaskAck.Accepted {
					s.streamMgr.workCompleted(envoyID)
				}

			case *pb.AgentStreamMessage_TaskResult:
				s.taskRouter.handleTaskResult(stream.Context(), envoyID, m.TaskResult)
				s.streamMgr.workCompleted(envoyID)

			case *pb.AgentStreamMessage_QueryResponse:
				s.queryRouter.handleQueryResponse(envoyID, m.QueryResponse)
				s.streamMgr.workCompleted(envoyID)

			case *pb.AgentStreamMessage_Delta:
				ack := s.handleDelta(envoyID, envoy.Hostname, m.Delta)
				if err := stream.Send(&pb.ServerStreamMessage{
					Msg: &pb.ServerStreamMessage_DeltaAck{DeltaAck: ack},
				}); err != nil {
					errCh <- err
					return
				}
			}
		}
	}()

	// Wait for either goroutine to finish.
	return <-errCh
}

// buildCheckInResponse builds a CheckInResponse for both unary and stream contexts.
// When streaming is true, last_seen is updated in-memory only (no DB write) since
// the open gRPC stream already proves liveness. Unary RPCs set streaming=false to
// ensure the timestamp is flushed to SQLite.
func (s *AgentService) buildCheckInResponse(ctx context.Context, envoyID string, envoy db.Envoy, req *pb.CheckInRequest, streaming bool) (*pb.CheckInResponse, error) {
	// Update last-seen: streaming envoys skip the DB dirty flag since the open
	// stream already proves liveness. A final MarkSeen on disconnect persists it.
	if s.fleetIndex != nil {
		if streaming {
			s.fleetIndex.MarkSeenInMemory(envoyID, time.Now())
		} else {
			s.fleetIndex.MarkSeen(envoyID, time.Now())
		}
		s.fleetIndex.MarkCycleDuration(envoyID, clampReportedCycle(req.GetLastCheckinDeltaMs()))
	}
	// Batch inline traits for periodic flush (hash-deduplicated).
	inlineTraits := req.GetTraitsJson()
	if len(inlineTraits) > 0 && json.Valid(inlineTraits) {
		if s.traitsBatch != nil {
			s.traitsBatch.Mark(envoyID, string(inlineTraits))
		}

		// Update swarm peer tracker when agents report cached blobs.
		if s.peerTracker != nil {
			s.ingestSwarmTraits(envoyID, inlineTraits)
			s.ingestSwarmSeeds(envoyID, inlineTraits)
		}
	}

	// Record agent version if reported. In-memory only; the flusher's
	// value-grouped BatchUpdateAgentVersion path persists it. See
	// checkin.go for the 0.67.28 rationale.
	agentVersion := req.GetAgentVersion()
	if agentVersion != "" && s.fleetIndex != nil {
		s.fleetIndex.SetAgentVersion(envoyID, agentVersion)
	}

	// Refresh envoy IP (agent reports its outbound IP each check-in).
	// Same change-gated path as SetAgentVersion above.
	if ipAddr := req.GetIpAddress(); ipAddr != "" {
		changed := true
		if s.fleetIndex != nil {
			changed = s.fleetIndex.SetIP(envoyID, ipAddr)
		}
		if changed {
			if err := s.queries.UpdateEnvoyIP(ctx, db.UpdateEnvoyIPParams{
				IpAddress:   ipAddr,
				ID:          envoyID,
				IpAddress_2: ipAddr,
			}); err != nil {
				slog.Warn("agentstream db write failed",
					"envoy_id", envoyID,
					"op", "update_envoy_ip",
					"error", err,
				)
				s.metrics.AgentStreamDBErrors.WithLabelValues("update_envoy_ip").Inc()
			}
		}
	}

	configVersion := s.configStore.Version()

	// Short-circuit: agent has a cached bundle with matching config version
	// AND there are no pending rotated secrets or force_push flags.
	// When FleetIndex is available, check in-memory state for freshest flags.
	forcePush := envoy.ForcePush != 0
	hasPending := envoy.PendingRotatedSecrets != "" && envoy.PendingRotatedSecrets != "[]"
	streamRequested := false
	tunnelRequested := false
	forceUpdate := false
	fastPoll := false
	if s.fleetIndex != nil {
		if freshState, ok, _ := s.fleetIndex.GetEnvoyLite(envoyID); ok {
			forcePush = freshState.ForcePush
			hasPending = len(freshState.PendingRotatedSecrets) > 0
			streamRequested = freshState.StreamRequested
			tunnelRequested = freshState.TunnelRequested
			forceUpdate = freshState.ForceUpdate
			fastPoll = freshState.FastPoll
		}
	}

	// FastPoll: override interval to 1s so the agent comes back immediately.
	// Clear the flag so it only applies once.
	interval := formatDuration(s.baseInterval)
	if fastPoll && s.fleetIndex != nil {
		interval = "1s"
		s.fleetIndex.ClearFastPoll(envoyID)
	}

	// Auto-update: if the agent reports a version that doesn't match the
	// version of the binary we'd serve to ITS arch (not the server's own
	// arch), AND a binary is available, tell it to download and restart.
	//
	// Comparing against the server-arch version regardless of the agent's
	// arch caused an infinite update loop on cross-arch envoys (e.g. an
	// arm64 raspberry pi facing an amd64 server) — see tshoot/log.md.
	// We resolve the agent's goos/goarch from its reported traits and look
	// up the matching version. Unknown arch → no auto-update (safe default).
	if !forceUpdate && agentVersion != "" && len(s.serverVersions) > 0 {
		agentGoos, agentGoarch := goosGoarchFromTraits(inlineTraits)
		var serverVer string
		if agentGoos != "" && agentGoarch != "" {
			serverVer = s.serverVersions[agentGoos+"/"+agentGoarch]
		}
		if serverVer != "" && agentVersion != serverVer {
			binReady := s.agentBinAvailable == nil || s.agentBinAvailable()
			if binReady {
				slog.Info("agent version mismatch, requesting update",
					"envoy", envoyID, "agent", agentVersion, "server", serverVer,
					"os", agentGoos, "arch", agentGoarch)
				forceUpdate = true
			}
		}
	}
	// Clear the ephemeral flag so it's only sent once.
	if forceUpdate && s.fleetIndex != nil {
		s.fleetIndex.SetForceUpdate(envoyID, false)
	}
	if req.GetPolicyVersion() > 0 && req.GetPolicyVersion() == configVersion &&
		!forcePush && !hasPending {
		s.metrics.CheckinsTotal.WithLabelValues("no_change").Inc()

		// Look up observe_only flag: global (server.yaml) OR per-entry (envoys.vgo).
		// Also sync match-block tags into FleetIndex; the hostcrate is the
		// only source of truth for envoy tags.
		observeOnly := s.globalObserveOnly
		if _, cfg := s.configStore.Match(envoy.Hostname); cfg != nil {
			observeOnly = observeOnly || cfg.ObserveOnly
			if s.fleetIndex != nil {
				s.fleetIndex.SetTags(envoyID, cfg.Tags)
			}
		}

		resp := &pb.CheckInResponse{
			NoChange:        true,
			PolicyVersion:   configVersion,
			StreamRequested: streamRequested,
			TunnelRequested: tunnelRequested,
			ForceUpdate:     forceUpdate,
			ObserveOnly:     observeOnly,
			Interval:        interval,
			JitterPercent:   s.jitterPercent,
			ServerPubkeyPem: s.bundlePubKeyPEM,
		}
		// Check for pending reassignment even on no-change cycles.
		if s.reassignStore != nil {
			if info, ok := s.reassignStore.Pop(envoyID); ok {
				resp.ReassignAddr = info.TargetAddr
				resp.ReassignId = info.NewEnvoyID
				slog.Info("returning reassignment (no-change path)",
					"envoy_id", envoyID,
					"target_addr", info.TargetAddr,
					"new_envoy_id", info.NewEnvoyID,
				)
			}
		}
		// Agents self-assign via the manifest CRDT — no per-envoy
		// assignments needed on the server.
		// Ship the manifest CRDT so agents can pick up server-initiated
		// revocations. Multicast handles agent-to-agent propagation, but
		// operator-initiated revokes (`vigocli swarm revoke`) only update
		// the server's in-memory store — without this push, no agent ever
		// learns and the on-disk manifest stays at the cold-start version.
		resp.ManifestEntries = s.collectManifestEntries()
		resp.SwarmRuntimeLimits = s.buildSwarmRuntimeLimits()
		resp.CuratorCatalog = s.curatorCatalogBytes()
		return resp, nil
	}

	// Look up config using pattern index.
	pattern, cfg := s.configStore.Match(envoy.Hostname)
	if cfg == nil {
		cfg = &config.EnvoyConfig{
			Envoy:   envoy.Hostname,
			Modules: []config.ModuleConfig{},
		}
		pattern = ""
	}
	// Sync match-block tags into FleetIndex on every check-in so the
	// hostcrate remains the single source of truth for envoy tags.
	if s.fleetIndex != nil {
		s.fleetIndex.SetTags(envoyID, cfg.Tags)
	}

	// Per-envoy no-change detection via Merkle root. Even if policy_version
	// changed globally, this envoy's resolved config may be identical. Compare
	// the agent's state_fingerprint against the Merkle root to skip bundle
	// construction entirely.
	//
	// Guard: only use the load-time root when the config has no
	// module-level when: expressions (which may filter modules based on
	// traits). Configs with those fall through to the full bundle path
	// where the root is recomputed.
	agentFingerprint := req.GetStateFingerprint()
	hasModuleWhens := config.HasModuleWhens(cfg)
	if agentFingerprint != "" && cfg.MerkleRoot != "" &&
		agentFingerprint == cfg.MerkleRoot &&
		!hasModuleWhens &&
		!forcePush && !hasPending {
		s.metrics.CheckinsTotal.WithLabelValues("no_change_merkle").Inc()

		resp := &pb.CheckInResponse{
			NoChange:            true,
			PolicyVersion:       configVersion,
			ResponseFingerprint: cfg.MerkleRoot,
			StreamRequested:     streamRequested,
			TunnelRequested:     tunnelRequested,
			ForceUpdate:         forceUpdate,
			ObserveOnly:         s.globalObserveOnly || cfg.ObserveOnly,
			Interval:            interval,
			JitterPercent:       s.jitterPercent,
			ServerPubkeyPem:     s.bundlePubKeyPEM,
		}
		if s.reassignStore != nil {
			if info, ok := s.reassignStore.Pop(envoyID); ok {
				resp.ReassignAddr = info.TargetAddr
				resp.ReassignId = info.NewEnvoyID
			}
		}
		resp.ManifestEntries = s.collectManifestEntries()
		resp.SwarmRuntimeLimits = s.buildSwarmRuntimeLimits()
		resp.CuratorCatalog = s.curatorCatalogBytes()
		return resp, nil
	}

	// If config has case resources, expand them per-envoy using traits.
	// This bypasses the pattern-level policy cache since expansion varies per envoy.
	hasCaseRes := config.HasCaseResources(cfg)
	hasLookups := cfg.HasLookups
	hasFleetLookups := cfg.HasFleetLookups
	if hasCaseRes || hasLookups || hasFleetLookups {
		var traits map[string]any
		if len(inlineTraits) > 0 {
			if err := json.Unmarshal(inlineTraits, &traits); err != nil {
				slog.Warn("malformed inline traits JSON", "envoy", envoy.Hostname, "error", err)
			}
		}
		if traits == nil {
			traits = make(map[string]any)
		}
		// Clone config so we don't mutate the shared ConfigStore entry.
		cloned := cfg.Clone()
		if hasCaseRes {
			for i := range cloned.Modules {
				expanded, err := config.ExpandCase(cloned.Modules[i].Resources, traits, cloned.Vars)
				if err != nil {
					return nil, status.Errorf(codes.Internal, "expanding case for module %q: %v", cloned.Modules[i].Name, err)
				}
				cloned.Modules[i].Resources = expanded
			}
		}
		if hasLookups {
			// Flatten traits into a string map for MatchEnvoy's trait-filter
			// key form (e.g. "os.distro=ubuntu").
			flatTraits := make(map[string]string)
			fleet.FlattenJSON("", traits, flatTraits)
			if err := config.ResolveLookupsInConfig(cloned, envoy.Hostname, flatTraits); err != nil {
				return nil, status.Errorf(codes.Internal, "resolving lookups: %v", err)
			}
		}
		if hasFleetLookups {
			members := func() []config.FleetMember {
				es := s.fleetIndex.AllEnvoys()
				out := make([]config.FleetMember, 0, len(es))
				for _, e := range es {
					if e.Revoked {
						continue
					}
					out = append(out, config.FleetMember{
						Hostname: e.Hostname,
						IP:       e.IP,
						Tags:     e.Tags,
						Traits:   e.Traits,
						LastSeen: e.LastSeen,
					})
				}
				return out
			}
			isStale := func(m config.FleetMember) bool {
				return freshness.IsStaleFromCycle(m.LastSeen, s.baseInterval, 0, s.offlineThreshold)
			}
			if err := s.configStore.ResolveFleetLookupsInConfig(cloned, members, isStale, time.Now()); err != nil {
				return nil, status.Errorf(codes.Internal, "resolving fleet lookups: %v", err)
			}
			// foreach: over a now-resolved fleet: var (a list) expands here.
			for i := range cloned.Modules {
				expanded, err := config.ExpandForEach(cloned.Modules[i].Resources, cloned.Vars)
				if err != nil {
					return nil, status.Errorf(codes.Internal, "expanding foreach for module %q: %v", cloned.Modules[i].Name, err)
				}
				cloned.Modules[i].Resources = expanded
			}
		}
		cfg = cloned
	}

	// Parse inline traits for match var resolution.
	var traitsMap map[string]any
	if len(inlineTraits) > 0 {
		if err := json.Unmarshal(inlineTraits, &traitsMap); err != nil {
			slog.Warn("malformed inline traits JSON for match vars", "envoy", envoy.Hostname, "error", err)
		}
	}

	// Get or build cached base bundle, then customize stubs.
	var bundle *pb.PolicyBundle
	if s.policyCache != nil && pattern != "" && !hasCaseRes && !hasLookups && !hasFleetLookups {
		base, err := s.policyCache.GetOrBuild(pattern, cfg, configVersion, s.streamEdit)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "building policy bundle: %v", err)
		}
		bundle = filterBundleByTraits(customizeStubs(base, req.GetCachedModuleHashes()), traitsMap)
	} else {
		var err error
		bundle, err = buildPolicyBundle(cfg, configVersion, req.GetCachedModuleHashes(), traitsMap, s.streamEdit)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "building policy bundle: %v", err)
		}
		bundle = filterBundleByTraits(bundle, traitsMap)
	}

	// Inject server-derived per-envoy gates into vars_json. The agent's
	// `swarm/gates.rs::substrate_active` helper ANDs `swarm_enabled` with
	// the per-subsystem `swarm_<sub>_enabled` keys emitted here. Cached
	// bundles share vars_json across patterns; injectServerVars allocates
	// a fresh slice so the per-envoy state never leaks into the cache.
	//
	// Pattern-list semantics (see config.HostMatchesPatterns): both the
	// top-level swarm.enabled and the per-subsystem enabled list must
	// allow this envoy's hostname for the subsystem to be active. We
	// only emit a key when its value would gate the subsystem off,
	// keeping vars_json small.
	//
	// Lockbox is intentionally absent — its authorization derives from
	// puddle (single source of truth, ADR-020). The agent's lockbox
	// gate consults swarm_puddle_enabled instead.
	//
	// hostdox is the one exception to "only emit when gating off":
	// it is default-OFF on the agent (editing /etc/hosts is an operator
	// decision), so the agent treats absence as off and we must emit
	// swarm_hostdox_enabled=true when it should be ON. Off ⇒ omit.
	gates := map[string]any{}
	swarmAllowed := config.HostMatchesPatterns(envoy.Hostname, s.swarmCfg.Enabled)
	if !swarmAllowed {
		gates["swarm_enabled"] = false
	}
	if !swarmAllowed || !config.HostMatchesPatterns(envoy.Hostname, s.swarmCfg.Puddle.Enabled) {
		gates["swarm_puddle_enabled"] = false
	}
	if !swarmAllowed || !config.HostMatchesPatterns(envoy.Hostname, s.swarmCfg.Gitback.Enabled) {
		gates["swarm_gitback_enabled"] = false
	}
	if !swarmAllowed || !config.HostMatchesPatterns(envoy.Hostname, s.swarmCfg.Curator.Enabled) {
		gates["swarm_curator_enabled"] = false
	}
	if !swarmAllowed || !config.HostMatchesPatterns(envoy.Hostname, s.swarmCfg.Poolq.Enabled) {
		gates["swarm_poolq_enabled"] = false
	}
	if !swarmAllowed || !config.HostMatchesPatterns(envoy.Hostname, s.swarmCfg.Longdrawer.Enabled) {
		gates["swarm_longdrawer_enabled"] = false
	}
	if !swarmAllowed || !config.HostMatchesPatterns(envoy.Hostname, s.swarmCfg.Filecast.Enabled) {
		gates["swarm_filecast_enabled"] = false
	}
	if swarmAllowed && s.swarmCfg.Hostdox {
		gates["swarm_hostdox_enabled"] = true
	}
	if err := injectServerVars(bundle, gates); err != nil {
		slog.Warn("failed to inject server vars into policy bundle",
			"envoy", envoy.Hostname, "error", err)
	}

	// Include custom traits manifest.
	traitEntries, traitErr := s.traitsScanner.ScanDir()
	if traitErr != nil {
		slog.Warn("failed to scan traits directory", "error", traitErr)
	} else {
		for _, te := range traitEntries {
			bundle.CustomTraits = append(bundle.CustomTraits, &pb.CustomTraitEntry{
				Name:   te.Name,
				Sha256: te.SHA256,
			})
		}
	}

	// Read fresh flags from FleetIndex (in-memory, O(1)) or DB fallback.
	if s.fleetIndex != nil {
		freshState, freshExists, _ := s.fleetIndex.GetEnvoyLite(envoyID)
		if freshExists {
			// Inject secret rotation triggers.
			if len(freshState.PendingRotatedSecrets) > 0 {
				injectSecretTriggers(bundle, freshState.PendingRotatedSecrets)
				s.fleetIndex.ClearPendingRotatedSecrets(envoyID)
				slog.Info("delivered secret rotation triggers",
					"envoy_id", envoyID,
					"secrets", strings.Join(freshState.PendingRotatedSecrets, ", "))
			}

			// Clear force_push flag now that the agent has received the fresh bundle.
			if freshState.ForcePush {
				s.fleetIndex.SetForcePush(envoyID, false)
				// Attribute the run the agent reports back to this
				// force-push rather than a scheduled cycle.
				s.fleetIndex.MarkRunForced(envoyID)
			}
		}
	} else {
		freshEnvoy, freshErr := s.queries.GetEnvoyByID(ctx, envoyID)
		if freshErr != nil {
			slog.Warn("failed to re-read envoy for pending flags", "envoy_id", envoyID, "error", freshErr)
		} else {
			// Inject _secret_triggered for resources subscribing to rotated secrets.
			pending := freshEnvoy.PendingRotatedSecrets
			if pending != "" && pending != "[]" {
				var rotatedPaths []string
				if err := json.Unmarshal([]byte(pending), &rotatedPaths); err == nil && len(rotatedPaths) > 0 {
					injectSecretTriggers(bundle, rotatedPaths)
					if clearErr := s.queries.ClearRotatedSecrets(ctx, envoyID); clearErr != nil {
						slog.Error("failed to clear rotated secrets",
							"envoy_id", envoyID, "error", clearErr)
					} else {
						slog.Info("delivered secret rotation triggers",
							"envoy_id", envoyID,
							"secrets", strings.Join(rotatedPaths, ", "))
					}
				}
			}

			// Clear force_push flag now that the agent has received the fresh bundle.
			if freshEnvoy.ForcePush != 0 {
				if err := s.queries.ClearForcePush(ctx, envoyID); err != nil {
					slog.Warn("failed to clear force_push", "envoy_id", envoyID, "error", err)
				}
				// Attribute the reported run to this force-push.
				// Best-effort: a no-op if the envoy isn't in the
				// FleetIndex, in which case the run defaults to
				// "scheduled".
				if s.fleetIndex != nil {
					s.fleetIndex.MarkRunForced(envoyID)
				}
			}
		}
	}

	// Sign the bundle if a signer is configured.
	if s.bundleSigner != nil {
		if err := s.bundleSigner.Sign(bundle, s.bundleMaxAge); err != nil {
			slog.Error("failed to sign policy bundle", "envoy_id", envoyID, "error", err)
		}
	}

	s.metrics.CheckinsTotal.WithLabelValues("policy_bundle").Inc()

	resp := &pb.CheckInResponse{
		PolicyBundle:        bundle,
		PolicyVersion:       configVersion,
		ResponseFingerprint: bundle.MerkleRoot,
		StreamRequested:     streamRequested,
		TunnelRequested:     tunnelRequested,
		ForceUpdate:         forceUpdate,
		ObserveOnly:         s.globalObserveOnly || cfg.ObserveOnly,
		Interval:            interval,
		JitterPercent:       s.jitterPercent,
		ServerPubkeyPem:     s.bundlePubKeyPEM,
	}

	// Surface config reload errors as warnings so agents know why
	// expected modules may be missing from the policy.
	if reloadErr := s.configStore.LastReloadError(); reloadErr != nil {
		resp.Warnings = append(resp.Warnings, "server config reload failed: "+reloadErr.Error())
	}

	// Check for pending reassignment (envoy moving to a different bolt).
	if s.reassignStore != nil {
		if info, ok := s.reassignStore.Pop(envoyID); ok {
			resp.ReassignAddr = info.TargetAddr
			resp.ReassignId = info.NewEnvoyID
			slog.Info("returning reassignment",
				"envoy_id", envoyID,
				"target_addr", info.TargetAddr,
				"new_envoy_id", info.NewEnvoyID,
			)
		}
	}

	// Swarm: ship the manifest CRDT, the operator-tunable runtime limits
	// (max sizes + envelope retention) sourced from server.yaml, and the
	// curator artifact catalog (ADR-024) so agents can resolve through a
	// server outage.
	resp.ManifestEntries = s.collectManifestEntries()
	resp.SwarmRuntimeLimits = s.buildSwarmRuntimeLimits()
	resp.CuratorCatalog = s.curatorCatalogBytes()

	// Build ProxiedDevice list for gateway envoys. Network devices whose
	// gateway field matches this envoy's hostname get their resolved config
	// included in the response so the gateway agent can converge them over SSH.
	devices := s.configStore.DevicesForGateway(envoy.Hostname)
	for _, dev := range devices {
		devBundle, err := buildPolicyBundle(dev.Config, configVersion, nil, nil, s.streamEdit)
		if err != nil {
			slog.Error("failed to build policy bundle for proxied device",
				"device", dev.Hostname,
				"gateway", envoy.Hostname,
				"error", err,
			)
			continue
		}
		// Sign device bundles too if a signer is configured.
		if s.bundleSigner != nil {
			if err := s.bundleSigner.Sign(devBundle, s.bundleMaxAge); err != nil {
				slog.Error("failed to sign proxied device bundle",
					"device", dev.Hostname,
					"error", err,
				)
			}
		}

		// Generate a stable virtual envoy ID from the device hostname.
		deviceID := DeviceVirtualID(dev.Hostname)

		resp.ProxiedDevices = append(resp.ProxiedDevices, &pb.ProxiedDevice{
			EnvoyId:    deviceID,
			Hostname:   dev.Hostname,
			DeviceType: dev.DeviceType,
			Credential: dev.Credential,
			Username:   dev.Username,
			Port:       uint32(dev.Port),
			IpAddress:  dev.IPAddress,
			Policy:     devBundle,
		})

		slog.Debug("included proxied device in gateway response",
			"device", dev.Hostname,
			"device_id", deviceID,
			"gateway", envoy.Hostname,
		)
	}

	return resp, nil
}

// buildSwarmRuntimeLimits projects the operator's `cfg.Swarm` settings
// into the wire-format `SwarmRuntimeLimits` carried on every CheckIn
// response. Empty / zero fields surface to the agent as "use the
// compile-time default" — the strict-equal version handshake guarantees
// the field is always populated when an agent at the same version
// receives it. Returns nil when no swarm config is set (early
// startup); the agent falls back to compile-time defaults.
func (s *AgentService) buildSwarmRuntimeLimits() *pb.SwarmRuntimeLimits {
	sc := s.swarmCfg
	// Heuristic for "no config loaded yet": every cap zero AND every
	// retention string empty. Cheap; matches the SwarmSection zero
	// value.
	if sc.Filecast.MaxSizeBytes == 0 && sc.Filecast.DefaultRetention == "" &&
		sc.Lockbox.MaxFileBytes == 0 && sc.Lockbox.EnvelopeRetention == "" &&
		sc.Gitback.MaxBundleBytes == 0 && sc.Gitback.EnvelopeRetention == "" &&
		sc.Curator.MaxBytes == 0 && sc.Curator.LocalPort == 0 &&
		sc.Poolq.MaxMsgBytes == 0 && sc.Poolq.Retention == "" &&
		sc.Longdrawer.MaxFileBytes == 0 {
		return nil
	}
	return &pb.SwarmRuntimeLimits{
		FilecastMaxSizeBytes:         uint64(maxNonNeg(sc.Filecast.MaxSizeBytes)),
		FilecastDefaultRetention:     sc.Filecast.DefaultRetention,
		LockboxMaxFileBytes:          uint64(maxNonNeg(sc.Lockbox.MaxFileBytes)),
		LockboxEnvelopeRetentionSecs: durationSecsOrZero(sc.Lockbox.EnvelopeRetention),
		GitbackMaxBundleBytes:        uint64(maxNonNeg(sc.Gitback.MaxBundleBytes)),
		GitbackEnvelopeRetentionSecs: durationSecsOrZero(sc.Gitback.EnvelopeRetention),
		LongdrawerMaxFileBytes:       uint64(maxNonNeg(sc.Longdrawer.MaxFileBytes)),
		CuratorMaxBytes:              uint64(maxNonNeg(sc.Curator.MaxBytes)),
		CuratorLocalPort:             sc.Curator.LocalPort,
		PoolqMaxMsgBytes:             uint64(maxNonNeg(sc.Poolq.MaxMsgBytes)),
		PoolqRetentionSecs:           durationSecsOrZero(sc.Poolq.Retention),
	}
}

// maxNonNeg clamps negatives to zero so a misconfigured server.yaml
// (operator typo of -1) doesn't roll over to MaxUint64 when cast to
// the proto's uint64.
func maxNonNeg(n int64) int64 {
	if n < 0 {
		return 0
	}
	return n
}

// durationSecsOrZero parses an optional duration string ("90d",
// "24h", "30m") into seconds. Empty / unparseable yields zero, which
// the agent reads as "use the compile-time default." Mirrors
// `parseRetention` in server/main.go: supports the "Nd" suffix that
// `time.ParseDuration` itself rejects, plus the standard Go suffixes.
func durationSecsOrZero(s string) uint64 {
	if s == "" {
		return 0
	}
	if len(s) > 1 && s[len(s)-1] == 'd' {
		var days int
		if _, err := fmt.Sscanf(s, "%dd", &days); err == nil && days > 0 {
			return uint64(days) * 24 * 60 * 60
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0
	}
	return uint64(d.Seconds())
}

// collectManifestEntries returns all manifest CRDT entries as proto messages.
func (s *AgentService) collectManifestEntries() []*pb.SwarmManifestEntry {
	if s.manifestStore == nil {
		return nil
	}
	entries := s.manifestStore.Entries()
	if len(entries) == 0 {
		return nil
	}
	result := make([]*pb.SwarmManifestEntry, 0, len(entries))
	for _, e := range entries {
		result = append(result, &pb.SwarmManifestEntry{
			Sha256:    e.SHA256,
			Label:     e.Label,
			Target:    e.Target,
			Retention: e.Retention,
			SeederId:  e.SeederID,
			Size:      e.Size,
			Status:    e.Status,
			UpdatedAt: e.UpdatedAt,
			UpdatedBy: e.UpdatedBy,
		})
	}
	return result
}

// ingestSwarmTraits parses the swarm.blobs trait from inline traits JSON and
// calls ReportComplete on the PeerTracker for each blob the agent has cached.
// This is the bridge between agent trait reports and swarm distribution tracking.
func (s *AgentService) ingestSwarmTraits(envoyID string, traitsJSON []byte) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(traitsJSON, &top); err != nil {
		return
	}
	swarmRaw, ok := top["swarm"]
	if !ok {
		return
	}
	var swarmData struct {
		Blobs            []string                       `json:"blobs"`
		ChunkSources     map[string]map[string]int      `json:"chunk_sources"`      // sha256 → {source → count}
		ChunkDetails     map[string][]swarm.ChunkDetail `json:"chunk_details"`      // sha256 → per-chunk detail
		ActiveDownloadMs map[string]int64               `json:"active_download_ms"` // sha256 → active scheduler wall-clock ms (excludes pre-cached chunks)
		Downloading      map[string]int                 `json:"downloading"`        // sha256 → chunks_have (in-progress)
	}
	if err := json.Unmarshal(swarmRaw, &swarmData); err != nil {
		return
	}
	// NOTE: must NOT early-return when Blobs and Downloading are both
	// empty. An agent that has just finished evicting its last blob
	// reports exactly that shape, and the eviction-detection loop at
	// the bottom of this function is the only code path that flips
	// such envoys from "revoking" to "revoked". The report loops
	// above it are safe no-ops when both slices are empty.

	// Build peer address from envoy's IP + swarm port (1531).
	var peerAddr string
	if s.fleetIndex != nil {
		if es := s.fleetIndex.GetEnvoy(envoyID); es != nil && es.IP != "" {
			peerAddr = fmt.Sprintf("%s:1531", es.IP)
		}
	}

	for _, hash := range swarmData.Blobs {
		if swarm.IsValidHash(hash) {
			s.peerTracker.ReportComplete(envoyID, hash, peerAddr)
			// Store chunk source data and download duration if reported.
			if sources, ok := swarmData.ChunkSources[hash]; ok && len(sources) > 0 {
				details := swarmData.ChunkDetails[hash]  // may be nil
				dlMs := swarmData.ActiveDownloadMs[hash] // 0 if not reported
				s.peerTracker.SetChunkSources(envoyID, hash, sources, details, dlMs)
			}
		}
	}

	// Report in-progress downloads so status shows "downloading" with progress.
	for hash, chunksHave := range swarmData.Downloading {
		if swarm.IsValidHash(hash) && chunksHave > 0 {
			chunks := make([]uint32, chunksHave)
			for i := range chunks {
				chunks[i] = uint32(i)
			}
			s.peerTracker.ReportChunks(envoyID, hash, peerAddr, chunks, false)
			if sources, ok := swarmData.ChunkSources[hash]; ok && len(sources) > 0 {
				details := swarmData.ChunkDetails[hash]
				dlMs := swarmData.ActiveDownloadMs[hash]
				s.peerTracker.SetChunkSources(envoyID, hash, sources, details, dlMs)
			}
		}
	}

	// Eviction detection for revoked seeds. If the tracker still has a
	// per-envoy record for a revoked seed but the envoy's latest trait
	// report omits the hash from both `blobs` (complete) and `downloading`
	// (partial), the envoy has finished evicting the blob from its local
	// cache. Flip the per-envoy status to "revoked" so the operator sees
	// eviction progress advance. Bounded by manifest size (revoked seeds
	// only, not the full blob space).
	present := make(map[string]bool, len(swarmData.Blobs)+len(swarmData.Downloading))
	for _, hash := range swarmData.Blobs {
		present[hash] = true
	}
	for hash := range swarmData.Downloading {
		present[hash] = true
	}
	for _, revokedHash := range s.peerTracker.RevokedSeeds() {
		if present[revokedHash] {
			continue
		}
		if s.peerTracker.HasPeerRecord(envoyID, revokedHash) {
			s.peerTracker.MarkEvicted(envoyID, revokedHash)
		}
	}
}

func (s *AgentService) ingestSwarmSeeds(envoyID string, traitsJSON []byte) {
	if s.fleetIndex == nil {
		return
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(traitsJSON, &top); err != nil {
		return
	}
	seedsRaw, ok := top["swarm_seeds"]
	if !ok {
		return
	}
	var seedData struct {
		Seeds []struct {
			SHA256    string `json:"sha256"`
			Label     string `json:"label"`
			Size      uint64 `json:"size"`
			Target    string `json:"target"`
			Retention string `json:"retention"`
		} `json:"seeds"`
		BrokenSeeds []swarm.BrokenSeed `json:"broken_seeds"`
	}
	if err := json.Unmarshal(seedsRaw, &seedData); err != nil {
		return
	}

	// Store broken seeds for this envoy (replaces previous list).
	s.peerTracker.SetBrokenSeeds(envoyID, seedData.BrokenSeeds)

	// Build peer address for the seeding envoy.
	var peerAddr string
	if es := s.fleetIndex.GetEnvoy(envoyID); es != nil && es.IP != "" {
		peerAddr = fmt.Sprintf("%s:1531", es.IP)
	}

	reportedHashes := make(map[string]bool, len(seedData.Seeds))

	for _, seed := range seedData.Seeds {
		if !swarm.IsValidHash(seed.SHA256) || seed.Size == 0 {
			continue
		}
		reportedHashes[seed.SHA256] = true

		// Register seeder as a peer with the complete blob.
		s.peerTracker.ReportComplete(envoyID, seed.SHA256, peerAddr)

		// Record active entry in manifest CRDT so agents learn about the
		// blob via gossip. Use the gated `RecordSeedFromAgent` path:
		// agents may only refresh entries the server already knows about
		// (introduced via the authoritative admin-push API). This guards
		// against the re-poisoning footgun where a freshly-restarted
		// server's empty manifest gets re-populated from agents' cached
		// `swarm_seeds` traits within seconds (the re-poisoning
		// regression fixed in 0.38.12). ErrUnknownSeed is logged at
		// INFO and the report is silently skipped — the agent will
		// eventually GC the cached blob via its own staleness rules.
		if s.manifestStore != nil {
			if err := s.manifestStore.RecordSeedFromAgent(seed.SHA256, seed.Label, seed.Target, seed.Retention, envoyID, seed.Size); err != nil {
				slog.Info("swarm: dropping unknown seed reported by agent",
					"envoy_id", envoyID,
					"sha256", seed.SHA256[:12],
					"label", seed.Label,
					"reason", err.Error(),
				)
			}
		}

		// Parse retention duration.
		var retention time.Duration
		if seed.Retention != "" {
			retention = parseRetentionDuration(seed.Retention)
		}

		// Auto-revoke any existing seed with the same label but different hash
		// (label collision — different content overwrote the same filename).
		if oldHash := s.peerTracker.FindSeedByLabel(seed.Label); oldHash != "" && oldHash != seed.SHA256 {
			s.peerTracker.RevokeSeed(oldHash)
			if s.manifestStore != nil {
				s.manifestStore.Revoke(oldHash)
			}
			slog.Info("swarm seed superseded",
				"old_sha256", oldHash[:12],
				"new_sha256", seed.SHA256[:12],
				"label", seed.Label,
			)
		}

		// Register seed — returns true for new seeds and re-activated revoked seeds.
		//
		// DistributedAt source matters here. The order in this CheckIn
		// handler is `ingestSwarmTraits` → `ingestSwarmSeeds`, so non-
		// seeder envoys' `PeerState.FirstSeen` is created BEFORE this
		// RegisterSeed runs. If we use `time.Now()` for DistributedAt,
		// post-server-restart re-registration creates seeds whose
		// DistributedAt > every receiving envoy's FirstSeen, and
		// `tracker.Status` flips them to "cached" (Complete &&
		// FirstSeen.Before(DistributedAt)) indefinitely. Reuse the
		// persisted manifest's UpdatedAt where available so the
		// timestamp matches the original distribute event, not the
		// post-restart rehydration moment.
		distributedAt := time.Now()
		distributedSource := "now"
		if s.manifestStore != nil {
			if entry, ok := s.manifestStore.Get(seed.SHA256); ok && entry.UpdatedAt > 0 {
				distributedAt = time.UnixMilli(int64(entry.UpdatedAt))
				distributedSource = "manifest"
			}
		}
		isNew := s.peerTracker.RegisterSeed(seed.SHA256, swarm.SeedState{
			Label:         seed.Label,
			Size:          seed.Size,
			Target:        seed.Target,
			Retention:     retention,
			SeederID:      envoyID,
			DistributedAt: distributedAt,
		})
		if isNew {
			slog.Info("new swarm seed detected",
				"sha256", seed.SHA256[:12],
				"label", seed.Label,
				"seeder", envoyID,
				"target", seed.Target,
				"distributed_at_source", distributedSource,
			)
		}

		// Auto-distribute to matching envoys. Runs on every check-in cycle
		// Agents self-assign via the manifest CRDT — the server just records
		// the seed and gossips the manifest entry on the next check-in cycle.
		if isNew {
			slog.Info("swarm seed registered",
				"sha256", seed.SHA256[:12],
				"label", seed.Label,
				"target", seed.Target,
			)

			// Force-push streaming agents so they get the manifest update.
			if s.streamMgr != nil {
				s.streamMgr.forcePush("")
			}
		}
	}

	// Auto-revoke seeds this envoy was seeding but is no longer reporting.
	// This handles the "unseed" case: envoy removed the file from its seed dir.
	// Seeding is an intentional operator action — removing the file is an
	// authoritative revocation signal regardless of how many receivers have
	// the blob. Receivers are notified via manifest gossip and evict.
	for _, sha256 := range s.peerTracker.SeedsForEnvoy(envoyID) {
		if reportedHashes[sha256] {
			continue
		}
		s.peerTracker.RevokeSeed(sha256)
		if s.manifestStore != nil {
			s.manifestStore.Revoke(sha256)
		}
		slog.Info("swarm seed auto-revoked (seeder stopped reporting)",
			"sha256", sha256[:12],
			"seeder", envoyID,
		)
	}
}

// Longdrawer is an agent-side LAN-only P2P feature — the server has no role.
// No server-side ingest function lives here. See agent/src/longdrawer/.

// goosGoarchFromTraits extracts the agent's Go-style goos/goarch pair from
// its reported traits JSON. Returns ("", "") if either field is missing or
// unparseable — callers must treat the empty result as "unknown arch, do
// not auto-update."
//
// The agent's os.arch is already normalized to Go style ("amd64", "arm64")
// at collection time (see agent/src/traits/os.rs). The os.family is
// distro-style on Linux ("debian", "rhel", etc.) and platform-style
// elsewhere ("darwin", "windows", "freebsd"). We map family → goos by
// recognizing the BSDs/illumos/macOS/Windows explicitly and treating
// everything else as Linux (matching the convention used by the conditional
// evaluator in server/config/template.go).
func goosGoarchFromTraits(traitsJSON []byte) (goos string, goarch string) {
	if len(traitsJSON) == 0 {
		return "", ""
	}
	var t struct {
		OS struct {
			Family string `json:"family"`
			Arch   string `json:"arch"`
		} `json:"os"`
	}
	if err := json.Unmarshal(traitsJSON, &t); err != nil {
		return "", ""
	}
	if t.OS.Arch == "" {
		return "", ""
	}
	switch strings.ToLower(t.OS.Family) {
	case "darwin":
		goos = "darwin"
	case "windows":
		goos = "windows"
	case "freebsd", "openbsd", "netbsd", "illumos":
		goos = strings.ToLower(t.OS.Family)
	case "":
		// Family unset — can't safely guess.
		return "", ""
	default:
		// Linux distros report family="debian"/"rhel"/"suse"/etc.
		goos = "linux"
	}
	return goos, t.OS.Arch
}

func parseRetentionDuration(s string) time.Duration {
	if len(s) > 1 && s[len(s)-1] == 'd' {
		var days int
		if _, err := fmt.Sscanf(s, "%dd", &days); err == nil && days > 0 {
			return time.Duration(days) * 24 * time.Hour
		}
	}
	d, _ := time.ParseDuration(s)
	return d
}

// parseBlobSource extracts the SHA256 hash from a "blob:sha256:<hex>" source URI.
