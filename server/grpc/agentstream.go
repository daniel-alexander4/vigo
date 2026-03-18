// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
	"github.com/hobnob/hobnob/server/config"
	"github.com/hobnob/hobnob/server/db"
	"github.com/hobnob/hobnob/server/metrics"
	"github.com/hobnob/hobnob/server/registry"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// AgentStream handles a persistent bidirectional stream with an agent.
// The first message must be a CheckInRequest (handshake). After that, the
// agent can send check-in, report-result, or report-traits messages. The
// server pushes policy changes and force-push signals asynchronously.
func (s *AgentService) AgentStream(stream pb.HobnobAgent_AgentStreamServer) error {
	// Read the first message — must be a check-in (handshake).
	firstMsg, err := stream.Recv()
	if err != nil {
		return status.Error(codes.InvalidArgument, "expected initial check-in message")
	}

	checkin := firstMsg.GetCheckIn()
	if checkin == nil {
		return status.Error(codes.InvalidArgument, "first stream message must be check_in")
	}

	if _, err := uuid.Parse(checkin.GetThrallId()); err != nil {
		return status.Errorf(codes.InvalidArgument, "invalid thrall_id: %v", err)
	}
	thrallID := checkin.GetThrallId()

	// Validate thrall + look up state in one lock acquisition.
	var thrall db.Thrall
	if s.fleetIndex != nil {
		ts, exists, revoked := s.fleetIndex.GetThrallLite(thrallID)
		if !exists {
			return status.Error(codes.NotFound, "thrall not found")
		}
		if revoked {
			return status.Error(codes.PermissionDenied, "thrall is revoked")
		}
		thrall = fleetThrallToDB(ts)
	} else {
		if err := s.registry.ValidateThrall(stream.Context(), thrallID); err != nil {
			if errors.Is(err, registry.ErrThrallRevoked) {
				return status.Error(codes.PermissionDenied, "thrall is revoked")
			}
			if errors.Is(err, registry.ErrThrallNotFound) {
				return status.Error(codes.NotFound, "thrall not found")
			}
			return status.Errorf(codes.Internal, "validating thrall: %v", err)
		}
		var err error
		thrall, err = s.lookupThrall(stream.Context(), thrallID)
		if err != nil {
			return err
		}
	}

	// Register the stream for push notifications.
	as, cleanup := s.streamMgr.register(thrallID, thrall.Hostname)
	defer cleanup()
	// Persist final last_seen to DB when the stream closes so the timestamp
	// survives server restart (streaming thralls skip DB writes mid-stream).
	defer func() {
		if s.fleetIndex != nil {
			s.fleetIndex.MarkSeen(thrallID, time.Now())
		}
	}()

	// Send initial policy response.
	resp, err := s.buildCheckInResponse(stream.Context(), thrallID, thrall, checkin, true)
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
		"thrall_id", thrallID,
		"hostname", thrall.Hostname,
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
				resp, err := s.buildCheckInResponse(stream.Context(), thrallID, thrall, m.CheckIn, true)
				if err != nil {
					slog.Error("stream check-in error", "thrall_id", thrallID, "error", err)
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
				// result's thrall_id differs from the stream owner, verify
				// the sender is the authorized gateway for that device.
				resultThrallID := m.ReportResult.GetThrallId()
				if resultThrallID != thrallID {
					if !isAuthorizedGateway(s.configStore, resultThrallID, thrall.Hostname) {
						slog.Warn("rejected proxied result from unauthorized gateway",
							"device_thrall_id", resultThrallID,
							"gateway_thrall_id", thrallID,
							"gateway_hostname", thrall.Hostname,
						)
						continue
					}
				}

				resp, err := s.ReportResult(stream.Context(), m.ReportResult)
				if err != nil {
					slog.Error("stream report-result error", "thrall_id", thrallID, "error", err)
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
					slog.Error("stream report-traits error", "thrall_id", thrallID, "error", err)
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

			case *pb.AgentStreamMessage_PushAck:
				s.pushRouter.handlePushAck(stream.Context(), thrallID, m.PushAck)
				if !m.PushAck.Accepted {
					s.streamMgr.workCompleted(thrallID)
				}

			case *pb.AgentStreamMessage_PushResult:
				s.pushRouter.handlePushResult(stream.Context(), thrallID, m.PushResult)
				s.streamMgr.workCompleted(thrallID)

			case *pb.AgentStreamMessage_QueryResponse:
				s.queryRouter.handleQueryResponse(thrallID, m.QueryResponse)
				s.streamMgr.workCompleted(thrallID)

			case *pb.AgentStreamMessage_Delta:
				ack := s.handleDelta(thrallID, thrall.Hostname, m.Delta)
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
func (s *AgentService) buildCheckInResponse(ctx context.Context, thrallID string, thrall db.Thrall, req *pb.CheckInRequest, streaming bool) (*pb.CheckInResponse, error) {
	// Update last-seen: streaming thralls skip the DB dirty flag since the open
	// stream already proves liveness. A final MarkSeen on disconnect persists it.
	if s.fleetIndex != nil {
		if streaming {
			s.fleetIndex.MarkSeenInMemory(thrallID, time.Now())
		} else {
			s.fleetIndex.MarkSeen(thrallID, time.Now())
		}
	}
	// Batch inline traits for periodic flush (hash-deduplicated).
	inlineTraits := req.GetTraitsJson()
	if len(inlineTraits) > 0 && json.Valid(inlineTraits) {
		if s.traitsBatch != nil {
			s.traitsBatch.Mark(thrallID, string(inlineTraits))
		}
		if s.traitsPruner != nil {
			s.traitsPruner.Mark(thrallID)
		}
	}

	// Record agent version if reported.
	agentVersion := req.GetAgentVersion()
	if agentVersion != "" && s.fleetIndex != nil {
		s.fleetIndex.SetAgentVersion(thrallID, agentVersion)
	}

	configVersion := s.configStore.Version()

	// Short-circuit: agent has a cached bundle with matching config version
	// AND there are no pending rotated secrets or force_push flags.
	// When FleetIndex is available, check in-memory state for freshest flags.
	forcePush := thrall.ForcePush != 0
	hasPending := thrall.PendingRotatedSecrets != "" && thrall.PendingRotatedSecrets != "[]"
	streamRequested := false
	forceUpdate := false
	if s.fleetIndex != nil {
		if freshState, ok, _ := s.fleetIndex.GetThrallLite(thrallID); ok {
			forcePush = freshState.ForcePush
			hasPending = len(freshState.PendingRotatedSecrets) > 0
			streamRequested = freshState.StreamRequested
			forceUpdate = freshState.ForceUpdate
		}
	}

	// Auto-update: if the agent reports a version that doesn't match the
	// server's version, tell it to download the latest binary and restart.
	// This ensures all agents stay current without manual intervention.
	if !forceUpdate && agentVersion != "" && s.serverVersion != "" && agentVersion != s.serverVersion {
		slog.Info("agent version mismatch, requesting update",
			"thrall", thrallID, "agent", agentVersion, "server", s.serverVersion)
		forceUpdate = true
	}
	// Clear the ephemeral flag so it's only sent once.
	if forceUpdate && s.fleetIndex != nil {
		s.fleetIndex.SetForceUpdate(thrallID, false)
	}
	if req.GetPolicyVersion() > 0 && req.GetPolicyVersion() == configVersion &&
		!forcePush && !hasPending {
		metrics.CheckinsTotal.WithLabelValues("no_change").Inc()
		resp := &pb.CheckInResponse{
			NoChange:        true,
			PolicyVersion:   configVersion,
			StreamRequested: streamRequested,
			ForceUpdate:     forceUpdate,
			Interval:        formatDuration(s.baseInterval),
			JitterPercent:   s.jitterPercent,
			ServerPubkeyPem: s.bundlePubKeyPEM,
		}
		// Check for pending reassignment even on no-change cycles.
		if s.reassignStore != nil {
			if info, ok := s.reassignStore.Pop(thrallID); ok {
				resp.ReassignAddr = info.TargetAddr
				resp.ReassignId = info.NewThrallID
				slog.Info("returning reassignment (no-change path)",
					"thrall_id", thrallID,
					"target_addr", info.TargetAddr,
					"new_thrall_id", info.NewThrallID,
				)
			}
		}
		return resp, nil
	}

	// Look up config using pattern index.
	pattern, cfg := s.configStore.Match(thrall.Hostname)
	if cfg == nil {
		cfg = &config.ThrallConfig{
			Thrall:    thrall.Hostname,
			Modules: []config.ModuleConfig{},
		}
		pattern = ""
	}

	// If config has case resources, expand them per-thrall using traits.
	// This bypasses the pattern-level policy cache since expansion varies per thrall.
	hasCaseRes := config.HasCaseResources(cfg)
	if hasCaseRes {
		var traits map[string]any
		if len(inlineTraits) > 0 {
			if err := json.Unmarshal(inlineTraits, &traits); err != nil {
				slog.Warn("malformed inline traits JSON", "thrall", thrall.Hostname, "error", err)
			}
		}
		if traits == nil {
			traits = make(map[string]any)
		}
		// Clone config so we don't mutate the shared ConfigStore entry.
		cloned := cfg.Clone()
		for i := range cloned.Modules {
			expanded, err := config.ExpandCase(cloned.Modules[i].Resources, traits, cloned.Vars)
			if err != nil {
				return nil, status.Errorf(codes.Internal, "expanding case for module %q: %v", cloned.Modules[i].Name, err)
			}
			cloned.Modules[i].Resources = expanded
		}
		cfg = cloned
	}

	// Parse inline traits for match var resolution.
	var traitsMap map[string]any
	if len(inlineTraits) > 0 {
		if err := json.Unmarshal(inlineTraits, &traitsMap); err != nil {
			slog.Warn("malformed inline traits JSON for match vars", "thrall", thrall.Hostname, "error", err)
		}
	}

	// Get or build cached base bundle, then customize stubs.
	var bundle *pb.PolicyBundle
	if s.policyCache != nil && pattern != "" && !hasCaseRes {
		base, err := s.policyCache.GetOrBuild(pattern, cfg, configVersion)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "building policy bundle: %v", err)
		}
		bundle = customizeStubs(base, req.GetCachedModuleHashes())
	} else {
		var err error
		bundle, err = buildPolicyBundle(cfg, configVersion, req.GetCachedModuleHashes(), traitsMap)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "building policy bundle: %v", err)
		}
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
		freshState, freshExists, _ := s.fleetIndex.GetThrallLite(thrallID)
		if freshExists {
			// Inject secret rotation triggers.
			if len(freshState.PendingRotatedSecrets) > 0 {
				injectSecretTriggers(bundle, freshState.PendingRotatedSecrets)
				s.fleetIndex.ClearPendingRotatedSecrets(thrallID)
				slog.Info("delivered secret rotation triggers",
					"thrall_id", thrallID,
					"secrets", strings.Join(freshState.PendingRotatedSecrets, ", "))
			}

			// Clear force_push flag now that the agent has received the fresh bundle.
			if freshState.ForcePush {
				s.fleetIndex.SetForcePush(thrallID, false)
			}
		}
	} else {
		freshThrall, freshErr := s.queries.GetThrallByID(ctx, thrallID)
		if freshErr != nil {
			slog.Warn("failed to re-read thrall for pending flags", "thrall_id", thrallID, "error", freshErr)
		} else {
			// Inject _secret_triggered for resources subscribing to rotated secrets.
			pending := freshThrall.PendingRotatedSecrets
			if pending != "" && pending != "[]" {
				var rotatedPaths []string
				if err := json.Unmarshal([]byte(pending), &rotatedPaths); err == nil && len(rotatedPaths) > 0 {
					injectSecretTriggers(bundle, rotatedPaths)
					if clearErr := s.queries.ClearRotatedSecrets(ctx, thrallID); clearErr != nil {
						slog.Error("failed to clear rotated secrets",
							"thrall_id", thrallID, "error", clearErr)
					} else {
						slog.Info("delivered secret rotation triggers",
							"thrall_id", thrallID,
							"secrets", strings.Join(rotatedPaths, ", "))
					}
				}
			}

			// Clear force_push flag now that the agent has received the fresh bundle.
			if freshThrall.ForcePush != 0 {
				if err := s.queries.ClearForcePush(ctx, thrallID); err != nil {
					slog.Warn("failed to clear force_push", "thrall_id", thrallID, "error", err)
				}
			}
		}
	}

	// Sign the bundle if a signer is configured.
	if s.bundleSigner != nil {
		if err := s.bundleSigner.Sign(bundle, s.bundleMaxAge); err != nil {
			slog.Error("failed to sign policy bundle", "thrall_id", thrallID, "error", err)
		}
	}

	metrics.CheckinsTotal.WithLabelValues("policy_bundle").Inc()

	resp := &pb.CheckInResponse{
		PolicyBundle:    bundle,
		PolicyVersion:   configVersion,
		StreamRequested: streamRequested,
		ForceUpdate:     forceUpdate,
		Interval:        formatDuration(s.baseInterval),
		JitterPercent:   s.jitterPercent,
		ServerPubkeyPem: s.bundlePubKeyPEM,
	}

	// Surface config reload errors as warnings so agents know why
	// expected modules may be missing from the policy.
	if reloadErr := s.configStore.LastReloadError(); reloadErr != nil {
		resp.Warnings = append(resp.Warnings, "server config reload failed: "+reloadErr.Error())
	}

	// Check for pending reassignment (thrall moving to a different spoke).
	if s.reassignStore != nil {
		if info, ok := s.reassignStore.Pop(thrallID); ok {
			resp.ReassignAddr = info.TargetAddr
			resp.ReassignId = info.NewThrallID
			slog.Info("returning reassignment",
				"thrall_id", thrallID,
				"target_addr", info.TargetAddr,
				"new_thrall_id", info.NewThrallID,
			)
		}
	}

	// Build ProxiedDevice list for gateway thralls. Network devices whose
	// gateway field matches this thrall's hostname get their resolved config
	// included in the response so the gateway agent can converge them over SSH.
	devices := s.configStore.DevicesForGateway(thrall.Hostname)
	for _, dev := range devices {
		devBundle, err := buildPolicyBundle(dev.Config, configVersion, nil, nil)
		if err != nil {
			slog.Error("failed to build policy bundle for proxied device",
				"device", dev.Hostname,
				"gateway", thrall.Hostname,
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

		// Generate a stable virtual thrall ID from the device hostname.
		deviceID := DeviceVirtualID(dev.Hostname)

		resp.ProxiedDevices = append(resp.ProxiedDevices, &pb.ProxiedDevice{
			ThrallId:   deviceID,
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
			"gateway", thrall.Hostname,
		)
	}

	return resp, nil
}
