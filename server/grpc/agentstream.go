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
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	pb "github.com/daniel-alexander4/vigo/generated.code/proto/vigo"

	"github.com/daniel-alexander4/vigo/server/config"
	"github.com/daniel-alexander4/vigo/server/db"
	"github.com/daniel-alexander4/vigo/server/fleet"
	"github.com/daniel-alexander4/vigo/server/metrics"
	"github.com/daniel-alexander4/vigo/server/registry"
	"github.com/daniel-alexander4/vigo/server/swarm"
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

	// Send initial policy response.
	resp, err := s.buildCheckInResponse(stream.Context(), envoyID, envoy, checkin, true)
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

	metrics.CheckinsTotal.WithLabelValues("stream_connect").Inc()
	slog.Info("agent stream established",
		"envoy_id", envoyID,
		"hostname", envoy.Hostname,
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
				resp, err := s.buildCheckInResponse(stream.Context(), envoyID, envoy, m.CheckIn, true)
				if err != nil {
					slog.Error("stream check-in error", "envoy_id", envoyID, "error", err)
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

				resp, err := s.ReportResult(stream.Context(), m.ReportResult)
				if err != nil {
					slog.Error("stream report-result error", "envoy_id", envoyID, "error", err)
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
				resp, err := s.ReportTraits(stream.Context(), m.ReportTraits)
				if err != nil {
					slog.Error("stream report-traits error", "envoy_id", envoyID, "error", err)
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
	}
	// Batch inline traits for periodic flush (hash-deduplicated).
	inlineTraits := req.GetTraitsJson()
	if len(inlineTraits) > 0 && json.Valid(inlineTraits) {
		if s.traitsBatch != nil {
			s.traitsBatch.Mark(envoyID, string(inlineTraits))
		}
		if s.traitsPruner != nil {
			s.traitsPruner.Mark(envoyID)
		}

		// Update swarm peer tracker when agents report cached blobs.
		if s.peerTracker != nil {
			s.ingestSwarmTraits(envoyID, inlineTraits)
			s.ingestSwarmSeeds(envoyID, inlineTraits)
		}
	}

	// Record agent version if reported.
	agentVersion := req.GetAgentVersion()
	if agentVersion != "" && s.fleetIndex != nil {
		s.fleetIndex.SetAgentVersion(envoyID, agentVersion)
	}
	// Persist to DB (conditional — only writes if changed).
	if agentVersion != "" {
		_ = s.queries.UpdateAgentVersion(ctx, db.UpdateAgentVersionParams{
			AgentVersion:   agentVersion,
			ID:             envoyID,
			AgentVersion_2: agentVersion,
		})
	}

	// Refresh envoy IP (agent reports its outbound IP each check-in).
	if ipAddr := req.GetIpAddress(); ipAddr != "" && s.fleetIndex != nil {
		s.fleetIndex.SetIP(envoyID, ipAddr)
		_ = s.queries.UpdateEnvoyIP(ctx, db.UpdateEnvoyIPParams{
			IpAddress:   ipAddr,
			ID:          envoyID,
			IpAddress_2: ipAddr,
		})
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
	// server's version AND a binary is available to serve, tell it to
	// download the latest binary and restart.
	if !forceUpdate && agentVersion != "" && s.serverVersion != "" && agentVersion != s.serverVersion {
		binReady := s.agentBinAvailable == nil || s.agentBinAvailable()
		if binReady {
			slog.Info("agent version mismatch, requesting update",
				"envoy", envoyID, "agent", agentVersion, "server", s.serverVersion)
			forceUpdate = true
		}
	}
	// Clear the ephemeral flag so it's only sent once.
	if forceUpdate && s.fleetIndex != nil {
		s.fleetIndex.SetForceUpdate(envoyID, false)
	}
	if req.GetPolicyVersion() > 0 && req.GetPolicyVersion() == configVersion &&
		!forcePush && !hasPending {
		metrics.CheckinsTotal.WithLabelValues("no_change").Inc()

		// Look up observe_only flag: global (server.yaml) OR per-entry (envoys.vgo).
		// Also sync match-block tags into FleetIndex; the matchfile is the
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
		if s.peerTracker != nil {
			for _, sha := range s.peerTracker.PopRejectedSeeds(envoyID) {
				resp.RejectedSeeds = append(resp.RejectedSeeds, &pb.RejectedSeed{Sha256: sha, Reason: "seed not authorized by server policy"})
			}
		}
		// Ship the manifest CRDT so agents can pick up server-initiated
		// revocations. Multicast handles agent-to-agent propagation, but
		// operator-initiated revokes (`vigocli swarm revoke`) only update
		// the server's in-memory store — without this push, no agent ever
		// learns and the on-disk manifest stays at the cold-start version.
		resp.ManifestEntries = s.collectManifestEntries()
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
	// matchfile remains the single source of truth for envoy tags.
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
		metrics.CheckinsTotal.WithLabelValues("no_change_merkle").Inc()

		resp := &pb.CheckInResponse{
			NoChange:             true,
			PolicyVersion:        configVersion,
			ResponseFingerprint:  cfg.MerkleRoot,
			StreamRequested:      streamRequested,
			TunnelRequested:      tunnelRequested,
			ForceUpdate:          forceUpdate,
			ObserveOnly:          s.globalObserveOnly || cfg.ObserveOnly,
			Interval:             interval,
			JitterPercent:        s.jitterPercent,
			ServerPubkeyPem:      s.bundlePubKeyPEM,
		}
		if s.reassignStore != nil {
			if info, ok := s.reassignStore.Pop(envoyID); ok {
				resp.ReassignAddr = info.TargetAddr
				resp.ReassignId = info.NewEnvoyID
			}
		}
		if s.peerTracker != nil {
			for _, sha := range s.peerTracker.PopRejectedSeeds(envoyID) {
				resp.RejectedSeeds = append(resp.RejectedSeeds, &pb.RejectedSeed{Sha256: sha, Reason: "seed not authorized by server policy"})
			}
		}
		resp.ManifestEntries = s.collectManifestEntries()
		return resp, nil
	}

	// If config has case resources, expand them per-envoy using traits.
	// This bypasses the pattern-level policy cache since expansion varies per envoy.
	hasCaseRes := config.HasCaseResources(cfg)
	hasLookups := cfg.HasLookups
	if hasCaseRes || hasLookups {
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
	if s.policyCache != nil && pattern != "" && !hasCaseRes && !hasLookups {
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
			}
		}
	}

	// Sign the bundle if a signer is configured.
	if s.bundleSigner != nil {
		if err := s.bundleSigner.Sign(bundle, s.bundleMaxAge); err != nil {
			slog.Error("failed to sign policy bundle", "envoy_id", envoyID, "error", err)
		}
	}

	metrics.CheckinsTotal.WithLabelValues("policy_bundle").Inc()

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

	// Check for pending reassignment (envoy moving to a different spoke).
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

	// Swarm: signal seed rejections and ship the manifest CRDT.
	if s.peerTracker != nil {
		for _, sha := range s.peerTracker.PopRejectedSeeds(envoyID) {
			resp.RejectedSeeds = append(resp.RejectedSeeds, &pb.RejectedSeed{Sha256: sha, Reason: "seed not authorized by server policy"})
		}
	}
	resp.ManifestEntries = s.collectManifestEntries()

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
			EnvoyId:   deviceID,
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
		ChunkSources     map[string]map[string]int      `json:"chunk_sources"`       // sha256 → {source → count}
		ChunkDetails     map[string][]swarm.ChunkDetail `json:"chunk_details"`       // sha256 → per-chunk detail
		ActiveDownloadMs map[string]int64               `json:"active_download_ms"`  // sha256 → active scheduler wall-clock ms (excludes pre-cached chunks)
		Downloading      map[string]int                 `json:"downloading"`         // sha256 → chunks_have (in-progress)
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
				details := swarmData.ChunkDetails[hash]           // may be nil
				dlMs := swarmData.ActiveDownloadMs[hash]           // 0 if not reported
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

// authorizeSeed checks whether an envoy is permitted to originate a seed.
// The rule is a single allowlist match: the envoy's hostname must either
// equal the server's own envoy hostname (server.hostname in server.yaml —
// every server runs a local vigo agent, so it's always allowed) or match
// a pattern in swarm.seeding_envoys. Returns ("", true) on approval, or
// (reason, false) on rejection. Rejections are surfaced to the envoy via
// the rejected_seeds check-in response; the file is moved into the
// envoy-local admin_rejected/ directory with the reason attached.
func (s *AgentService) authorizeSeed(envoyID string) (string, bool) {
	es := s.fleetIndex.GetEnvoy(envoyID)
	if es == nil {
		return "unknown envoy", false
	}

	if s.serverEnvoyHost != "" && es.Hostname == s.serverEnvoyHost {
		return "", true
	}

	for _, pat := range s.swarmCfg.SeedingEnvoys {
		if matched, _ := filepath.Match(pat, es.Hostname); matched {
			return "", true
		}
	}

	return fmt.Sprintf("hostname %q not in swarm.seeding_envoys", es.Hostname), false
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

	// Authorization gate: check if this envoy is permitted to seed.
	if reason, allowed := s.authorizeSeed(envoyID); !allowed {
		var rejectedHashes []string
		for _, seed := range seedData.Seeds {
			if swarm.IsValidHash(seed.SHA256) && seed.Size > 0 {
				rejectedHashes = append(rejectedHashes, seed.SHA256)
			}
		}
		if len(rejectedHashes) > 0 {
			s.peerTracker.SetRejectedSeeds(envoyID, rejectedHashes)
			slog.Info("swarm seeds rejected",
				"envoy", envoyID,
				"count", len(rejectedHashes),
				"reason", reason,
			)
		}
		return
	}

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
		// blob via gossip. This also overrides stale revocations when a
		// previously-deleted blob is re-distributed (fresh timestamp wins).
		if s.manifestStore != nil {
			s.manifestStore.RecordSeed(seed.SHA256, seed.Label, seed.Target, seed.Retention, envoyID, seed.Size)
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
		isNew := s.peerTracker.RegisterSeed(seed.SHA256, swarm.SeedState{
			Label:         seed.Label,
			Size:          seed.Size,
			Target:        seed.Target,
			Retention:     retention,
			SeederID:      envoyID,
			DistributedAt: time.Now(),
		})
		if isNew {
			slog.Info("new swarm seed detected",
				"sha256", seed.SHA256[:12],
				"label", seed.Label,
				"seeder", envoyID,
				"target", seed.Target,
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


// Vigosync is an agent-side LAN-only P2P feature — the server has no role.
// No server-side ingest function lives here. See agent/src/vigosync/.

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
