// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package peer

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"google.golang.org/protobuf/proto"
)

// TestPrimaryIsSyncPath pins the primary's watch filter to the shared
// allowlist: the real config dirs replicate, the retired/local ones do not.
func TestPrimaryIsSyncPath(t *testing.T) {
	root := t.TempDir()
	p := NewPrimary(root, nil, nil, nil)
	cases := map[string]bool{
		"stacks/configcrates/nginx.vgo": true,
		".live/roles.vgo":               true,
		"secrets/vigo/db/dsn":           true,
		"tls/server.crt":                true,
		"server.yaml":                   true,
		"stacks":                        true,
		"stack/configcrates/nginx.vgo":  false, // retired name the primary used to watch
		"worksite/stacks/x.vgo":         false,
		"peer-role":                     false,
		"config-history/1/roles.vgo":    false,
		"vigo.db":                       false,
	}
	for rel, want := range cases {
		if got := p.isSyncPath(filepath.Join(root, rel)); got != want {
			t.Errorf("isSyncPath(%q) = %v, want %v", rel, got, want)
		}
	}
	if p.isSyncPath(filepath.Join(filepath.Dir(root), "stacks", "x")) {
		t.Error("isSyncPath accepted a path outside serverRoot")
	}
}

func sendOne(t *testing.T, svc *Service, fc *pb.PeerFileChange) *pb.PeerSyncAck {
	t.Helper()
	stream := newMockSyncStream(context.Background())
	stream.recvCh <- &pb.PeerSyncMessage{Payload: &pb.PeerSyncMessage_FileChange{FileChange: fc}}
	stream.close()
	if err := svc.SyncStream(stream); err != nil {
		t.Fatalf("SyncStream: %v", err)
	}
	acks := stream.collectAcks()
	if len(acks) != 1 {
		t.Fatalf("want 1 ack, got %d", len(acks))
	}
	return acks[0]
}

// TestServiceRejectsNonReplicatedPath: a peer must refuse a write outside the
// replicated set — peer-role above all, which would let the primary's role
// overwrite the peer's.
func TestServiceRejectsNonReplicatedPath(t *testing.T) {
	for _, rel := range []string{"peer-role", "config-history/1/x.vgo", "vigo.db", "worksite/stacks/x.vgo"} {
		root := t.TempDir()
		svc := NewService(root, "peer", nil, nil)
		ack := sendOne(t, svc, &pb.PeerFileChange{Path: rel, Content: []byte("primary\n"), Mode: 0o600})
		if ack.Success {
			t.Errorf("%s: write accepted, want refused", rel)
		}
		if _, err := os.Lstat(filepath.Join(root, rel)); !os.IsNotExist(err) {
			t.Errorf("%s: file was created on the peer", rel)
		}
	}
}

// TestServiceRejectsSymlinkEscape: a symlink below a replicated dir that
// points outside it must not be written through.
func TestServiceRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "stacks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "stacks", "evil")); err != nil {
		t.Fatal(err)
	}
	svc := NewService(root, "peer", nil, nil)

	ack := sendOne(t, svc, &pb.PeerFileChange{Path: "stacks/evil/x.vgo", Content: []byte("pwn"), Mode: 0o644})
	if ack.Success {
		t.Error("write through escaping symlink accepted")
	}
	ack = sendOne(t, svc, &pb.PeerFileChange{Path: "stacks/evil/deeper/x.vgo", Content: []byte("pwn"), Mode: 0o644})
	if ack.Success {
		t.Error("write through escaping symlink (missing subdir) accepted")
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Errorf("files landed outside the replicated dir: %d entries", len(entries))
	}

	// A symlinked TOP-level dir is the operator's local layout and is allowed.
	tlsReal := t.TempDir()
	if err := os.Symlink(tlsReal, filepath.Join(root, "tls")); err != nil {
		t.Fatal(err)
	}
	ack = sendOne(t, svc, &pb.PeerFileChange{Path: "tls/server.crt", Content: []byte("cert"), Mode: 0o644})
	if !ack.Success {
		t.Errorf("write under symlinked top-level tls/ refused: %s", ack.Error)
	}
}

// TestServiceConfigReloadPaths: a write under stacks/ or .live/ triggers a
// config reload; secrets/ does not.
func TestServiceConfigReloadPaths(t *testing.T) {
	cases := map[string]bool{
		"stacks/roles.vgo":    true,
		".live/roles.vgo":     true,
		"secrets/vigo/db/dsn": false,
	}
	for rel, want := range cases {
		var reloads atomic.Int32
		svc := NewService(t.TempDir(), "peer", func() { reloads.Add(1) }, nil)
		ack := sendOne(t, svc, &pb.PeerFileChange{Path: rel, Content: []byte("x"), Mode: 0o644})
		if !ack.Success {
			t.Fatalf("%s: %s", rel, ack.Error)
		}
		if got := reloads.Load() > 0; got != want {
			t.Errorf("%s: reload = %v, want %v", rel, got, want)
		}
	}
}

// TestPrimaryStopWithoutStart: Stop must return when Start never ran (or
// failed before its loop), not block forever on <-stopped.
func TestPrimaryStopWithoutStart(t *testing.T) {
	p := NewPrimary(t.TempDir(), nil, nil, nil)
	done := make(chan struct{})
	go func() { p.Stop(); p.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop blocked with no Start")
	}
	if err := p.Start(context.Background()); err == nil {
		t.Error("Start after Stop ran; want refusal")
	}
}

// TestPrimaryStartStopConcurrent races Start against Stop (run under -race):
// Stop must not read state Start writes, and must return once Start exits.
func TestPrimaryStartStopConcurrent(t *testing.T) {
	for i := 0; i < 20; i++ {
		p := NewPrimary(t.TempDir(), nil, nil, nil)
		started := make(chan struct{})
		go func() { close(started); _ = p.Start(context.Background()) }()
		<-started
		done := make(chan struct{})
		go func() { p.Stop(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("Stop did not return")
		}
	}
}

// TestFullSyncChunked: a replicated tree larger than the 8 MiB gRPC receive
// limit must go out as several messages, each under it.
func TestFullSyncChunked(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "stacks", "templates")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	blob := bytes.Repeat([]byte("a"), 1<<20)
	for i := 0; i < 12; i++ { // 12 MiB total
		if err := os.WriteFile(filepath.Join(dir, "f"+string(rune('a'+i))), blob, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// One file past the per-file cap is skipped, not sent.
	if err := os.WriteFile(filepath.Join(dir, "huge"), bytes.Repeat([]byte("b"), maxReplicatedFileBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	p := NewPrimary(root, nil, nil, nil)
	chunks := p.fullSync()
	if len(chunks) < 2 {
		t.Fatalf("full sync went out as %d message(s); want it chunked", len(chunks))
	}
	total := 0
	for i, c := range chunks {
		if n := proto.Size(&pb.PeerSyncMessage{Payload: &pb.PeerSyncMessage_FullSync{FullSync: c}}); n >= 8<<20 {
			t.Errorf("chunk %d is %d bytes, at/over the 8 MiB receive limit", i, n)
		}
		for _, f := range c.Files {
			if filepath.Base(f.Path) == "huge" {
				t.Error("file past maxReplicatedFileBytes was sent")
			}
		}
		total += len(c.Files)
	}
	if total != 12 {
		t.Errorf("full sync carried %d files, want 12", total)
	}
}

// TestBroadcastFullChannelSchedulesResync: a batch dropped on a full channel
// must flag the peer for a full resync, not vanish.
func TestBroadcastFullChannelSchedulesResync(t *testing.T) {
	p := NewPrimary(t.TempDir(), nil, nil, nil)
	pc := &peerConn{changeCh: make(chan []*pb.PeerFileChange, 1), resync: make(chan struct{}, 1)}
	p.conns = []*peerConn{pc}
	batch := []*pb.PeerFileChange{{Path: "stacks/a.vgo", Content: []byte("x")}}
	p.broadcast(batch) // fills the channel
	select {
	case <-pc.resync:
		t.Fatal("resync scheduled before any drop")
	default:
	}
	p.broadcast(batch) // dropped
	select {
	case <-pc.resync:
	default:
		t.Fatal("dropped batch did not schedule a resync")
	}
}
