package grpc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"

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

	// Validate thrall is not revoked.
	if err := s.registry.ValidateThrall(stream.Context(), thrallID); err != nil {
		if errors.Is(err, registry.ErrThrallRevoked) {
			return status.Error(codes.PermissionDenied, "thrall is revoked")
		}
		if errors.Is(err, registry.ErrThrallNotFound) {
			return status.Error(codes.NotFound, "thrall not found")
		}
		return status.Errorf(codes.Internal, "validating thrall: %v", err)
	}

	// Look up thrall for hostname.
	thrall, err := s.lookupThrall(stream.Context(), thrallID)
	if err != nil {
		return err
	}

	// Register the stream for push notifications.
	as, cleanup := s.streamMgr.register(thrallID, thrall.Hostname)
	defer cleanup()

	// Send initial policy response.
	resp, err := s.buildCheckInResponse(stream.Context(), thrallID, thrall, checkin)
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
				resp, err := s.buildCheckInResponse(stream.Context(), thrallID, thrall, m.CheckIn)
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
			}
		}
	}()

	// Wait for either goroutine to finish.
	return <-errCh
}

// buildCheckInResponse builds a CheckInResponse for both unary and stream contexts.
// Extracted from CheckIn() to share logic.
func (s *AgentService) buildCheckInResponse(ctx context.Context, thrallID string, thrall db.Thrall, req *pb.CheckInRequest) (*pb.CheckInResponse, error) {
	// Batch last-seen update.
	if s.lastSeenBatch != nil {
		s.lastSeenBatch.Mark(thrallID)
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

	configVersion := s.configStore.Version()

	// Short-circuit: agent has a cached bundle with matching config version.
	if req.GetPolicyVersion() > 0 && req.GetPolicyVersion() == configVersion {
		metrics.CheckinsTotal.WithLabelValues("no_change").Inc()
		return &pb.CheckInResponse{
			NoChange:      true,
			PolicyVersion: configVersion,
		}, nil
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

	// Get or build cached base bundle, then customize stubs.
	var bundle *pb.PolicyBundle
	if s.policyCache != nil && pattern != "" {
		base, err := s.policyCache.GetOrBuild(pattern, cfg, configVersion)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "building policy bundle: %v", err)
		}
		bundle = customizeStubs(base, req.GetCachedModuleHashes())
	} else {
		var err error
		bundle, err = buildPolicyBundle(cfg, configVersion, req.GetCachedModuleHashes())
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

	metrics.CheckinsTotal.WithLabelValues("policy_bundle").Inc()

	return &pb.CheckInResponse{
		PolicyBundle:  bundle,
		PolicyVersion: configVersion,
	}, nil
}
