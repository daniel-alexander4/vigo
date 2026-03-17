package peer

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
	"google.golang.org/grpc"
)

// Service implements the HobnobPeer gRPC service.
// On a primary, it accepts SyncStream connections (but won't receive pushes — primary pushes outbound).
// On a peer, it receives file changes from the primary and writes them to disk.
type Service struct {
	pb.UnimplementedHobnobPeerServer

	serverRoot     string
	primaryAddr    string
	onConfigChange func()

	mu            sync.RWMutex
	role          string
	lastSyncUnix  int64
	filesSynced   int64
	configVersion uint64
}

// NewService creates a new peer service. role is "primary" or "peer".
// onConfigChange is called after writing files under hnb/ to trigger config reload.
func NewService(serverRoot string, role string, onConfigChange func()) *Service {
	return &Service{
		serverRoot:     serverRoot,
		role:           role,
		onConfigChange: onConfigChange,
	}
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

		// Trigger config reload if any hnb/ files were written this iteration.
		if needsConfigReload.CompareAndSwap(true, false) {
			if s.onConfigChange != nil {
				slog.Info("triggering config reload after sync")
				s.onConfigChange()
			}
		}
	}
}

// Promote sets this server's role to "primary".
func (s *Service) Promote(_ context.Context, _ *pb.PeerPromoteRequest) (*pb.PeerPromoteResponse, error) {
	s.mu.Lock()
	oldRole := s.role
	s.role = "primary"
	s.primaryAddr = ""
	s.mu.Unlock()

	slog.Info("peer promoted to primary", "old_role", oldRole)

	return &pb.PeerPromoteResponse{
		Success: true,
		Message: fmt.Sprintf("promoted from %s to primary", oldRole),
	}, nil
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

// handleFileChange writes a single file change to disk and returns an ack.
// If the content is empty, the file is deleted. After writing to a path under hnb/,
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
		if isHnbPath(path) {
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
	if isHnbPath(path) {
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

// isHnbPath returns true if the path is under the hnb/ directory,
// meaning a config reload should be triggered after writing.
func isHnbPath(path string) bool {
	normalized := filepath.ToSlash(path)
	return strings.HasPrefix(normalized, "hnb/") || normalized == "hnb"
}

// atomicWrite writes data to a temp file in the same directory, then renames it
// into place for crash-safe writes.
func atomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".hobnob-peer-*.tmp")
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
