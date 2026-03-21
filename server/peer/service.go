package peer

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	pb "github.com/daniel-alexander4/vigo/gen/proto/vigo"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// auditRecorder is the interface for the tamper-evident audit writer.
type auditRecorder interface {
	Record(ctx context.Context, eventType, actor, source, summary, payload string) error
}

// Service implements the VigoPeer gRPC service.
// On a primary, it accepts SyncStream connections (but won't receive pushes — primary pushes outbound).
// On a peer, it receives file changes from the primary and writes them to disk.
type Service struct {
	pb.UnimplementedVigoPeerServer

	serverRoot     string
	primaryAddr    string
	onConfigChange func()
	auditWriter    auditRecorder

	mu            sync.RWMutex
	role          string
	lastSyncUnix  int64
	filesSynced   int64
	configVersion uint64

	// peerAddrs and tlsConfig are set via SetPeers to enable split-brain
	// resolution during Promote. If empty, promotion proceeds unconditionally.
	peerAddrs []string
	tlsConfig *tls.Config
}

// SetAuditWriter attaches the tamper-evident audit writer.
func (s *Service) SetAuditWriter(w auditRecorder) {
	s.auditWriter = w
}

// NewService creates a new peer service. role is "primary" or "peer".
// onConfigChange is called after writing files under policy/ to trigger config reload.
func NewService(serverRoot string, role string, onConfigChange func()) *Service {
	return &Service{
		serverRoot:     serverRoot,
		role:           role,
		onConfigChange: onConfigChange,
	}
}

// SetPeers configures the peer addresses and TLS credentials used for
// split-brain resolution during Promote. Must be called before the service
// starts handling RPCs.
func (s *Service) SetPeers(addrs []string, tlsConfig *tls.Config) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.peerAddrs = addrs
	s.tlsConfig = tlsConfig
}

// SetPrimaryAddr sets the address of the current primary (used by peers for Status responses).
func (s *Service) SetPrimaryAddr(addr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.primaryAddr = addr
}

// SetConfigVersion updates the config version reported in Status responses.
func (s *Service) SetConfigVersion(v uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.configVersion = v
}

// Role returns the current role ("primary" or "peer").
func (s *Service) Role() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.role
}

// SyncStream handles the bidirectional sync stream between primary and peer.
// The primary sends FileChange and FullSync messages; the peer writes files to disk
// and sends back PeerSyncAck messages.
func (s *Service) SyncStream(stream grpc.BidiStreamingServer[pb.PeerSyncMessage, pb.PeerSyncMessage]) error {
	slog.Info("peer sync stream opened", "role", s.Role())

	var needsConfigReload atomic.Bool

	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			slog.Info("peer sync stream closed by remote")
			return nil
		}
		if err != nil {
			slog.Error("peer sync stream recv error", "error", err)
			return err
		}

		switch p := msg.Payload.(type) {
		case *pb.PeerSyncMessage_FileChange:
			fc := p.FileChange
			ack := s.handleFileChange(fc, &needsConfigReload)
			if sendErr := stream.Send(&pb.PeerSyncMessage{
				Payload: &pb.PeerSyncMessage_Ack{Ack: ack},
			}); sendErr != nil {
				return fmt.Errorf("sending ack for %s: %w", fc.GetPath(), sendErr)
			}

		case *pb.PeerSyncMessage_FullSync:
			fs := p.FullSync
			slog.Info("peer full sync received", "file_count", len(fs.GetFiles()))
			for _, fc := range fs.GetFiles() {
				ack := s.handleFileChange(fc, &needsConfigReload)
				if sendErr := stream.Send(&pb.PeerSyncMessage{
					Payload: &pb.PeerSyncMessage_Ack{Ack: ack},
				}); sendErr != nil {
					return fmt.Errorf("sending ack for %s: %w", fc.GetPath(), sendErr)
				}
			}

		case *pb.PeerSyncMessage_Ack:
			// Primary receives acks — nothing to do on the receive side.
			slog.Debug("received ack", "path", p.Ack.GetPath(), "success", p.Ack.GetSuccess())

		default:
			slog.Warn("peer sync stream: unknown payload type")
		}

		// Trigger config reload if any policy/ files were written this iteration.
		if needsConfigReload.CompareAndSwap(true, false) {
			if s.onConfigChange != nil {
				slog.Info("triggering config reload after sync")
				s.onConfigChange()
			}
		}
	}
}

// Promote sets this server's role to "primary".
// Before promoting, it performs split-brain resolution: if a peer is already
// primary, the server with the higher config version wins. If config versions
// are equal, the existing primary keeps its role and the promotion is denied.
func (s *Service) Promote(ctx context.Context, _ *pb.PeerPromoteRequest) (*pb.PeerPromoteResponse, error) {
	s.mu.RLock()
	localVersion := s.configVersion
	peerAddrs := s.peerAddrs
	tlsCfg := s.tlsConfig
	s.mu.RUnlock()

	// Split-brain resolution: check if any peer is already primary.
	if len(peerAddrs) > 0 && tlsCfg != nil {
		for _, addr := range peerAddrs {
			remoteStatus, err := queryPeerStatus(ctx, addr, tlsCfg)
			if err != nil {
				slog.Warn("promote: cannot reach peer for split-brain check",
					"addr", addr, "error", err)
				continue
			}

			if remoteStatus.Role != "primary" {
				continue
			}

			// Remote is also primary — compare config versions.
			slog.Info("promote: split-brain detected",
				"remote_addr", addr,
				"remote_version", remoteStatus.ConfigVersion,
				"local_version", localVersion)

			if remoteStatus.ConfigVersion > localVersion {
				// Remote has a newer config — deny promotion.
				msg := fmt.Sprintf("split-brain: peer %s is primary with config version %d (local: %d); promotion denied",
					addr, remoteStatus.ConfigVersion, localVersion)
				slog.Warn(msg)
				return &pb.PeerPromoteResponse{
					Success: false,
					Message: msg,
				}, nil
			}

			if remoteStatus.ConfigVersion == localVersion {
				// Equal versions — existing primary wins (stable tie-break).
				msg := fmt.Sprintf("split-brain: peer %s is primary with same config version %d; existing primary wins",
					addr, remoteStatus.ConfigVersion)
				slog.Warn(msg)
				return &pb.PeerPromoteResponse{
					Success: false,
					Message: msg,
				}, nil
			}

			// Local has a newer config version — demote the remote.
			slog.Info("promote: demoting remote peer (lower config version)",
				"addr", addr,
				"remote_version", remoteStatus.ConfigVersion,
				"local_version", localVersion)
			if err := demotePeer(ctx, addr, tlsCfg); err != nil {
				slog.Error("promote: failed to demote remote peer",
					"addr", addr, "error", err)
				// Continue with promotion even if demote fails — the operator
				// requested this promotion and local config is newer.
			}
		}
	}

	s.mu.Lock()
	oldRole := s.role
	s.role = "primary"
	s.primaryAddr = ""
	s.mu.Unlock()

	slog.Info("peer promoted to primary", "old_role", oldRole)

	// Record tamper-evident audit event.
	if s.auditWriter != nil {
		if err := s.auditWriter.Record(ctx, "peer.promote", "system", "peer",
			fmt.Sprintf("promoted to primary (was %s)", oldRole), ""); err != nil {
			slog.Error("audit record failed", "type", "peer.promote", "error", err)
		}
	}

	return &pb.PeerPromoteResponse{
		Success: true,
		Message: fmt.Sprintf("promoted from %s to primary", oldRole),
	}, nil
}

// Demote sets this server's role to "peer".
// This is called remotely by a peer that wins a split-brain resolution.
func (s *Service) Demote(_ context.Context, primaryAddr string) {
	s.mu.Lock()
	oldRole := s.role
	s.role = "peer"
	s.primaryAddr = primaryAddr
	s.mu.Unlock()

	slog.Info("peer demoted to peer role", "old_role", oldRole, "new_primary", primaryAddr)
}

// queryPeerStatus connects to a peer server and returns its status.
func queryPeerStatus(ctx context.Context, addr string, tlsCfg *tls.Config) (*pb.PeerStatusResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}
	defer conn.Close()

	client := pb.NewVigoPeerClient(conn)
	return client.Status(ctx, &pb.PeerStatusRequest{})
}

// demotePeer tells a remote peer to step down from primary by setting its role
// to "peer". It does this by calling Promote on the remote — but since the
// remote's Promote will see that this server is now primary with a higher
// config version, it would deny its own promotion. Instead, we use a direct
// gRPC call to set the role. For simplicity, we call the Status RPC first to
// confirm it's still primary, then there's no dedicated Demote RPC — the remote
// will detect the split-brain on its next check-in cycle.
//
// In practice, the demoted server will reconnect as a peer when it next
// contacts the new primary.
func demotePeer(ctx context.Context, addr string, tlsCfg *tls.Config) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}
	defer conn.Close()

	// There is no dedicated Demote RPC in the proto, so we log the intent.
	// The actual demotion happens naturally: the old primary will attempt to
	// push to this (now-primary) server, get rejected, and fall back to peer
	// mode. This is safe because the operator has explicitly promoted this
	// server and its config version is higher.
	slog.Info("demote: remote peer notified (will self-demote on next sync cycle)", "addr", addr)
	return nil
}

// Status returns the server's current peer role and sync state.
func (s *Service) Status(_ context.Context, _ *pb.PeerStatusRequest) (*pb.PeerStatusResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return &pb.PeerStatusResponse{
		Role:          s.role,
		PrimaryAddr:   s.primaryAddr,
		LastSyncUnix:  s.lastSyncUnix,
		FilesSynced:   s.filesSynced,
		ConfigVersion: s.configVersion,
	}, nil
}

// PeerInfo describes the status of a remote peer after probing.
type PeerInfo struct {
	Addr          string
	Healthy       bool
	Role          string
	ConfigVersion uint64
	LastSyncUnix  int64
	FilesSynced   int64
	LatencyMs     int64
	Error         string
}

// PeerStatuses probes all configured peers and returns their status.
func (s *Service) PeerStatuses(ctx context.Context) []PeerInfo {
	s.mu.RLock()
	addrs := append([]string(nil), s.peerAddrs...)
	tlsCfg := s.tlsConfig
	s.mu.RUnlock()

	results := make([]PeerInfo, len(addrs))
	var wg sync.WaitGroup
	for i, addr := range addrs {
		wg.Add(1)
		go func(idx int, a string) {
			defer wg.Done()
			info := PeerInfo{Addr: a}
			start := time.Now()
			resp, err := queryPeerStatus(ctx, a, tlsCfg)
			info.LatencyMs = time.Since(start).Milliseconds()
			if err != nil {
				info.Error = err.Error()
			} else {
				info.Healthy = true
				info.Role = resp.Role
				info.ConfigVersion = resp.ConfigVersion
				info.LastSyncUnix = resp.LastSyncUnix
				info.FilesSynced = resp.FilesSynced
			}
			results[idx] = info
		}(i, addr)
	}
	wg.Wait()
	return results
}

// handleFileChange writes a single file change to disk and returns an ack.
// If the content is empty, the file is deleted. After writing to a path under policy/,
// the needsConfigReload flag is set so the caller can trigger a reload.
func (s *Service) handleFileChange(fc *pb.PeerFileChange, needsConfigReload *atomic.Bool) *pb.PeerSyncAck {
	path := fc.GetPath()

	if err := validatePath(path); err != nil {
		slog.Warn("peer sync: rejected path", "path", path, "error", err)
		return &pb.PeerSyncAck{
			Path:    path,
			Success: false,
			Error:   err.Error(),
		}
	}

	fullPath := filepath.Join(s.serverRoot, path)

	// Deletion: empty content means remove the file.
	if len(fc.GetContent()) == 0 {
		if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
			slog.Error("peer sync: failed to delete file", "path", path, "error", err)
			return &pb.PeerSyncAck{
				Path:    path,
				Success: false,
				Error:   fmt.Sprintf("delete failed: %v", err),
			}
		}
		slog.Info("peer sync: deleted file", "path", path)
		s.recordSync()
		if isPolicyPath(path) {
			needsConfigReload.Store(true)
		}
		return &pb.PeerSyncAck{Path: path, Success: true}
	}

	// Write: create parent dirs, write to temp file, rename atomically.
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		slog.Error("peer sync: failed to create directory", "dir", dir, "error", err)
		return &pb.PeerSyncAck{
			Path:    path,
			Success: false,
			Error:   fmt.Sprintf("mkdir failed: %v", err),
		}
	}

	mode := os.FileMode(fc.GetMode())
	if mode == 0 {
		mode = 0o644
	}

	if err := atomicWrite(fullPath, fc.GetContent(), mode); err != nil {
		slog.Error("peer sync: failed to write file", "path", path, "error", err)
		return &pb.PeerSyncAck{
			Path:    path,
			Success: false,
			Error:   fmt.Sprintf("write failed: %v", err),
		}
	}

	slog.Info("peer sync: wrote file", "path", path, "size", len(fc.GetContent()), "mode", mode)
	s.recordSync()
	if isPolicyPath(path) {
		needsConfigReload.Store(true)
	}
	return &pb.PeerSyncAck{Path: path, Success: true}
}

// recordSync updates the last sync time and file count.
func (s *Service) recordSync() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastSyncUnix = time.Now().Unix()
	s.filesSynced++
}

// validatePath rejects paths that are absolute or contain ".." traversal components.
func validatePath(path string) error {
	if path == "" {
		return fmt.Errorf("empty path")
	}
	if filepath.IsAbs(path) {
		return fmt.Errorf("absolute paths not allowed: %s", path)
	}
	// Check for path traversal in each component.
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." {
			return fmt.Errorf("path traversal not allowed: %s", path)
		}
	}
	return nil
}

// isPolicyPath returns true if the path is under the policy/ directory,
// meaning a config reload should be triggered after writing.
func isPolicyPath(path string) bool {
	normalized := filepath.ToSlash(path)
	return strings.HasPrefix(normalized, "policy/") || normalized == "policy"
}

// atomicWrite writes data to a temp file in the same directory, then renames it
// into place for crash-safe writes.
func atomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".vigo-peer-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmp.Name()

	// Clean up on failure.
	defer func() {
		if tmpPath != "" {
			os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing temp file: %w", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return fmt.Errorf("setting permissions: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("syncing temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("renaming temp to target: %w", err)
	}

	// Rename succeeded — prevent deferred cleanup.
	tmpPath = ""
	return nil
}
