// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package peer

import (
	"context"
	"crypto/tls"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// Mock BidiStreamingServer for SyncStream tests
// ---------------------------------------------------------------------------

// mockSyncStream implements grpc.BidiStreamingServer[PeerSyncMessage, PeerSyncMessage]
// using channels for send/recv so tests can drive the stream synchronously.
type mockSyncStream struct {
	ctx    context.Context
	recvCh chan *pb.PeerSyncMessage // messages the server reads via Recv()
	sendCh chan *pb.PeerSyncMessage // messages the server writes via Send()
	mu     sync.Mutex
	closed bool
}

func newMockSyncStream(ctx context.Context) *mockSyncStream {
	return &mockSyncStream{
		ctx:    ctx,
		recvCh: make(chan *pb.PeerSyncMessage, 64),
		sendCh: make(chan *pb.PeerSyncMessage, 64),
	}
}

func (m *mockSyncStream) Send(msg *pb.PeerSyncMessage) error {
	m.sendCh <- msg
	return nil
}

func (m *mockSyncStream) Recv() (*pb.PeerSyncMessage, error) {
	msg, ok := <-m.recvCh
	if !ok {
		return nil, io.EOF
	}
	return msg, nil
}

func (m *mockSyncStream) SetHeader(metadata.MD) error  { return nil }
func (m *mockSyncStream) SendHeader(metadata.MD) error { return nil }
func (m *mockSyncStream) SetTrailer(metadata.MD)       {}
func (m *mockSyncStream) Context() context.Context     { return m.ctx }
func (m *mockSyncStream) SendMsg(any) error            { return nil }
func (m *mockSyncStream) RecvMsg(any) error            { return nil }

// close signals EOF to the server's Recv loop.
func (m *mockSyncStream) close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.closed {
		close(m.recvCh)
		m.closed = true
	}
}

// collectAcks reads all buffered ack messages from the send channel.
func (m *mockSyncStream) collectAcks() []*pb.PeerSyncAck {
	var acks []*pb.PeerSyncAck
	for {
		select {
		case msg := <-m.sendCh:
			if ack := msg.GetAck(); ack != nil {
				acks = append(acks, ack)
			}
		default:
			return acks
		}
	}
}

// ---------------------------------------------------------------------------
// TestValidatePath
// ---------------------------------------------------------------------------

func TestValidatePath(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"normal relative path", "stacks/modules/nginx.vgo", false},
		{"simple filename", "server.yaml", false},
		{"nested path", "secrets/db/password", false},
		{"empty path", "", true},
		{"absolute path unix", "/etc/passwd", true},
		{"path traversal dotdot", "stacks/../../etc/passwd", true},
		{"leading dotdot", "../secret", true},
		{"dotdot only", "..", true},
		{"dotdot in middle", "a/../b", true},
		{"single dot ok", "stacks/./modules/nginx.vgo", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePath(tt.path)
			if tt.wantErr && err == nil {
				t.Errorf("validatePath(%q) = nil, want error", tt.path)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("validatePath(%q) = %v, want nil", tt.path, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestAtomicWrite
// ---------------------------------------------------------------------------

func TestAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "test.txt")
	content := []byte("hello, peer replication")
	mode := os.FileMode(0o640)

	if err := atomicWrite(target, content, mode); err != nil {
		t.Fatalf("atomicWrite failed: %v", err)
	}

	// Verify content.
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("reading written file: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("content = %q, want %q", got, content)
	}

	// Verify permissions.
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat written file: %v", err)
	}
	if info.Mode().Perm() != mode {
		t.Errorf("mode = %o, want %o", info.Mode().Perm(), mode)
	}

	// No temp files should remain.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "test.txt" {
			t.Errorf("unexpected file in dir: %s", e.Name())
		}
	}
}

// ---------------------------------------------------------------------------
// TestServiceSyncStream — FileChange write
// ---------------------------------------------------------------------------

func TestServiceSyncStream(t *testing.T) {
	root := t.TempDir()
	var reloaded bool
	svc := NewService(root, "peer", func() { reloaded = true }, nil)

	stream := newMockSyncStream(context.Background())

	// Send a file change message, then close the stream.
	stream.recvCh <- &pb.PeerSyncMessage{
		Payload: &pb.PeerSyncMessage_FileChange{
			FileChange: &pb.PeerFileChange{
				Path:    "stacks/modules/nginx.vgo",
				Content: []byte("package: nginx\nensure: present\n"),
				Mode:    0o644,
			},
		},
	}
	stream.close()

	// Run SyncStream — it will process the message then return on EOF.
	if err := svc.SyncStream(stream); err != nil {
		t.Fatalf("SyncStream returned error: %v", err)
	}

	// Verify the ack.
	acks := stream.collectAcks()
	if len(acks) != 1 {
		t.Fatalf("expected 1 ack, got %d", len(acks))
	}
	if !acks[0].Success {
		t.Errorf("ack success = false, error = %s", acks[0].Error)
	}
	if acks[0].Path != "stacks/modules/nginx.vgo" {
		t.Errorf("ack path = %s, want stacks/modules/nginx.vgo", acks[0].Path)
	}

	// Verify the file was written to disk.
	written, err := os.ReadFile(filepath.Join(root, "stacks/modules/nginx.vgo"))
	if err != nil {
		t.Fatalf("reading written file: %v", err)
	}
	if string(written) != "package: nginx\nensure: present\n" {
		t.Errorf("file content = %q", written)
	}

	// Verify config reload was triggered (path is under stacks/).
	if !reloaded {
		t.Error("expected onConfigChange to be called for stacks/ path")
	}
}

// TestServiceSyncStreamPrimaryRejectsPush pins B12: a primary must reject
// an incoming replication push from another (superseded) primary, rather
// than write it to disk — otherwise two primaries overwrite each other's
// stacks/secrets/TLS state (split-brain).
func TestServiceSyncStreamPrimaryRejectsPush(t *testing.T) {
	root := t.TempDir()
	svc := NewService(root, "primary", nil, nil)

	stream := newMockSyncStream(context.Background())
	stream.recvCh <- &pb.PeerSyncMessage{
		Payload: &pb.PeerSyncMessage_FileChange{
			FileChange: &pb.PeerFileChange{
				Path:    "stacks/modules/nginx.vgo",
				Content: []byte("package: nginx\n"),
				Mode:    0o644,
			},
		},
	}
	stream.close()

	err := svc.SyncStream(stream)
	if err == nil {
		t.Fatal("expected SyncStream to reject incoming push on a primary")
	}

	// The file must NOT have been written.
	if _, statErr := os.Stat(filepath.Join(root, "stacks/modules/nginx.vgo")); statErr == nil {
		t.Error("primary wrote an incoming replicated file — split-brain protection failed")
	}
}

// ---------------------------------------------------------------------------
// TestServiceSyncStreamDeletion
// ---------------------------------------------------------------------------

func TestServiceSyncStreamDeletion(t *testing.T) {
	root := t.TempDir()
	svc := NewService(root, "peer", nil, nil)

	// Pre-create the file to be deleted.
	target := filepath.Join(root, "stacks/modules/old.vgo")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old content"), 0o644); err != nil {
		t.Fatal(err)
	}

	stream := newMockSyncStream(context.Background())

	// Send a file change with empty content (deletion).
	stream.recvCh <- &pb.PeerSyncMessage{
		Payload: &pb.PeerSyncMessage_FileChange{
			FileChange: &pb.PeerFileChange{
				Path:    "stacks/modules/old.vgo",
				Content: nil,
			},
		},
	}
	stream.close()

	if err := svc.SyncStream(stream); err != nil {
		t.Fatalf("SyncStream returned error: %v", err)
	}

	acks := stream.collectAcks()
	if len(acks) != 1 {
		t.Fatalf("expected 1 ack, got %d", len(acks))
	}
	if !acks[0].Success {
		t.Errorf("ack success = false, error = %s", acks[0].Error)
	}

	// Verify the file was deleted.
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Error("expected file to be deleted, but it still exists")
	}
}

// ---------------------------------------------------------------------------
// TestServiceSyncStreamFullSync
// ---------------------------------------------------------------------------

func TestServiceSyncStreamFullSync(t *testing.T) {
	root := t.TempDir()
	svc := NewService(root, "peer", nil, nil)

	stream := newMockSyncStream(context.Background())

	// Send a FullSync with multiple files.
	stream.recvCh <- &pb.PeerSyncMessage{
		Payload: &pb.PeerSyncMessage_FullSync{
			FullSync: &pb.PeerFullSync{
				Files: []*pb.PeerFileChange{
					{
						Path:    "stacks/modules/nginx.vgo",
						Content: []byte("nginx config"),
						Mode:    0o644,
					},
					{
						Path:    "secrets/db/password",
						Content: []byte("hunter2"),
						Mode:    0o600,
					},
					{
						Path:    "server.yaml",
						Content: []byte("listen: :8443\n"),
						Mode:    0o644,
					},
				},
			},
		},
	}
	stream.close()

	if err := svc.SyncStream(stream); err != nil {
		t.Fatalf("SyncStream returned error: %v", err)
	}

	// Verify all acks.
	acks := stream.collectAcks()
	if len(acks) != 3 {
		t.Fatalf("expected 3 acks, got %d", len(acks))
	}
	for _, ack := range acks {
		if !ack.Success {
			t.Errorf("ack for %s failed: %s", ack.Path, ack.Error)
		}
	}

	// Verify all files were written.
	checks := map[string]string{
		"stacks/modules/nginx.vgo": "nginx config",
		"secrets/db/password":      "hunter2",
		"server.yaml":              "listen: :8443\n",
	}
	for rel, want := range checks {
		got, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Errorf("reading %s: %v", rel, err)
			continue
		}
		if string(got) != want {
			t.Errorf("%s content = %q, want %q", rel, got, want)
		}
	}

	// Verify filesSynced counter.
	if svc.filesSynced != 3 {
		t.Errorf("filesSynced = %d, want 3", svc.filesSynced)
	}
}

// ---------------------------------------------------------------------------
// TestServicePromote
// ---------------------------------------------------------------------------

func TestServicePromote(t *testing.T) {
	svc := NewService("/srv/vigo", "peer", nil, nil)
	svc.SetPrimaryAddr("primary.example.com:1530")

	resp, err := svc.Promote(context.Background(), &pb.PeerPromoteRequest{})
	if err != nil {
		t.Fatalf("Promote returned error: %v", err)
	}
	if !resp.Success {
		t.Errorf("Promote success = false")
	}

	// Role should now be "primary".
	if role := svc.Role(); role != "primary" {
		t.Errorf("role = %q, want %q", role, "primary")
	}

	// PrimaryAddr should be cleared after promotion.
	svc.mu.RLock()
	addr := svc.primaryAddr
	svc.mu.RUnlock()
	if addr != "" {
		t.Errorf("primaryAddr = %q, want empty after promotion", addr)
	}
}

// ---------------------------------------------------------------------------
// TestServiceStatus
// ---------------------------------------------------------------------------

func TestServiceStatus(t *testing.T) {
	svc := NewService("/srv/vigo", "peer", nil, nil)
	svc.SetPrimaryAddr("primary.example.com:1530")
	svc.SetConfigVersion(42)
	svc.mu.Lock()
	svc.lastSyncUnix = 1700000000
	svc.filesSynced = 17
	svc.mu.Unlock()

	resp, err := svc.Status(context.Background(), &pb.PeerStatusRequest{})
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}

	if resp.Role != "peer" {
		t.Errorf("role = %q, want %q", resp.Role, "peer")
	}
	if resp.PrimaryAddr != "primary.example.com:1530" {
		t.Errorf("primary_addr = %q, want %q", resp.PrimaryAddr, "primary.example.com:1530")
	}
	if resp.LastSyncUnix != 1700000000 {
		t.Errorf("last_sync_unix = %d, want 1700000000", resp.LastSyncUnix)
	}
	if resp.FilesSynced != 17 {
		t.Errorf("files_synced = %d, want 17", resp.FilesSynced)
	}
	if resp.ConfigVersion != 42 {
		t.Errorf("config_version = %d, want 42", resp.ConfigVersion)
	}
}

// ---------------------------------------------------------------------------
// TestServiceDemote
// ---------------------------------------------------------------------------

func TestServiceDemote(t *testing.T) {
	svc := NewService("/srv/vigo", "primary", nil, nil)

	var pushing bool
	var hookCalled bool
	svc.SetRoleChangeHook(func(primary bool) {
		hookCalled = true
		pushing = primary
	})

	// In-process operator call (no gRPC peer) is exempt from auth.
	if _, err := svc.Demote(context.Background(), &pb.PeerDemoteRequest{}); err != nil {
		t.Fatalf("Demote returned error: %v", err)
	}

	if role := svc.Role(); role != "peer" {
		t.Errorf("role = %q, want %q", role, "peer")
	}
	if !hookCalled {
		t.Error("role-change hook was not called")
	}
	if pushing {
		t.Error("hook signalled primary=true on demote, want false (stop pushing)")
	}
}

// TestServiceDemoteNetworkUnsignedRejected: a Demote arriving over the network
// without a control-plane signature is rejected, exactly like Promote/Status.
func TestServiceDemoteNetworkUnsignedRejected(t *testing.T) {
	svc := dummySigner(t) // has a signing identity → network auth is operable
	_, err := svc.Demote(networkCtx(t, nil, ""), &pb.PeerDemoteRequest{})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("want Unauthenticated, got %v", err)
	}
	if role := svc.Role(); role != "peer" {
		// dummySigner starts as "peer"; a rejected Demote must not change role.
		t.Errorf("role changed on rejected Demote: %q", role)
	}
}

// ---------------------------------------------------------------------------
// TestServicePromoteNoPeers — promotion succeeds when no peers configured
// ---------------------------------------------------------------------------

func TestServicePromoteNoPeers(t *testing.T) {
	svc := NewService("/srv/vigo", "peer", nil, nil)
	svc.SetPrimaryAddr("old-primary.example.com:1530")

	// No SetPeers call — promotion should succeed unconditionally.
	resp, err := svc.Promote(context.Background(), nil)
	if err != nil {
		t.Fatalf("Promote returned error: %v", err)
	}
	if !resp.Success {
		t.Errorf("Promote success = false, message = %s", resp.Message)
	}
	if role := svc.Role(); role != "primary" {
		t.Errorf("role = %q, want %q", role, "primary")
	}
}

// ---------------------------------------------------------------------------
// TestServicePromoteUnreachablePeer — promotion succeeds when peer is down
// ---------------------------------------------------------------------------

func TestServicePromoteUnreachablePeer(t *testing.T) {
	svc := NewService("/srv/vigo", "peer", nil, nil)
	svc.SetConfigVersion(10)

	// Set an unreachable peer address.
	svc.SetPeers([]string{"127.0.0.1:19999"}, nil, &tls.Config{InsecureSkipVerify: true})

	resp, err := svc.Promote(context.Background(), nil)
	if err != nil {
		t.Fatalf("Promote returned error: %v", err)
	}
	if !resp.Success {
		t.Errorf("Promote success = false (should succeed when peer unreachable), message = %s", resp.Message)
	}
	if role := svc.Role(); role != "primary" {
		t.Errorf("role = %q, want %q", role, "primary")
	}
}

// ---------------------------------------------------------------------------
// TestServiceRejectsPathTraversal
// ---------------------------------------------------------------------------

func TestServiceRejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	svc := NewService(root, "peer", nil, nil)

	// Create a file outside the root that should NOT be affected.
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "sensitive")
	if err := os.WriteFile(outsideFile, []byte("do not touch"), 0o644); err != nil {
		t.Fatal(err)
	}

	stream := newMockSyncStream(context.Background())

	// Send a FileChange with path traversal.
	stream.recvCh <- &pb.PeerSyncMessage{
		Payload: &pb.PeerSyncMessage_FileChange{
			FileChange: &pb.PeerFileChange{
				Path:    "stacks/../../etc/passwd",
				Content: []byte("malicious"),
				Mode:    0o644,
			},
		},
	}
	stream.close()

	if err := svc.SyncStream(stream); err != nil {
		t.Fatalf("SyncStream returned error: %v", err)
	}

	acks := stream.collectAcks()
	if len(acks) != 1 {
		t.Fatalf("expected 1 ack, got %d", len(acks))
	}
	if acks[0].Success {
		t.Error("expected ack success = false for path traversal, got true")
	}
	if acks[0].Error == "" {
		t.Error("expected non-empty error message for path traversal")
	}

	// Verify no file was written in the server root.
	entries, _ := os.ReadDir(root)
	if len(entries) != 0 {
		t.Errorf("expected no files in server root, got %d entries", len(entries))
	}
}
