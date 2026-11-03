// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package peer

import (
	"context"
	"crypto/tls"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/config"
	"github.com/fsnotify/fsnotify"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// syncDirs are the directories under serverRoot that the primary replicates.
var syncDirs = []string{"stack", "secrets", "tls"}

// syncFile is the standalone file under serverRoot that is replicated.
const syncFile = "server.yaml"

// debounceDuration is the window for batching file changes before sending.
const debounceDuration = 1 * time.Second

// reconnectBase is the initial backoff for peer reconnection.
const reconnectBase = 1 * time.Second

// reconnectMax is the maximum backoff for peer reconnection.
const reconnectMax = 30 * time.Second

// Primary watches the server root directory for file changes and pushes
// them to connected peer servers via gRPC streams.
type Primary struct {
	serverRoot string
	peers      []config.PeerEntry
	tlsConfig  *tls.Config
	signer     credentials.PerRPCCredentials

	watcher *fsnotify.Watcher

	mu      sync.Mutex
	conns   []*peerConn
	cancel  context.CancelFunc
	stopped chan struct{}
}

// peerConn tracks the gRPC connection and stream to a single peer.
type peerConn struct {
	entry    config.PeerEntry
	conn     *grpc.ClientConn
	changeCh chan []*pb.PeerFileChange
	cancel   context.CancelFunc
	done     chan struct{}
}

// NewPrimary creates a Primary that will sync files from serverRoot to the
// given peers using mTLS. signer attaches this server's control-plane identity
// to the outbound SyncStream so each peer can authorize it; nil on a non-local
// secrets backend (peers reject the unsigned stream — replication fails closed).
func NewPrimary(serverRoot string, peers []config.PeerEntry, tlsConfig *tls.Config, signer credentials.PerRPCCredentials) *Primary {
	return &Primary{
		serverRoot: serverRoot,
		peers:      peers,
		tlsConfig:  tlsConfig,
		signer:     signer,
		stopped:    make(chan struct{}),
	}
}

// Start begins watching for file changes and connecting to peers.
// It blocks until the context is cancelled or Stop is called.
func (p *Primary) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	p.cancel = cancel

	w, err := fsnotify.NewWatcher()
	if err != nil {
		cancel()
		return fmt.Errorf("peer primary: create watcher: %w", err)
	}
	p.watcher = w

	// Watch the sync directories recursively.
	for _, dir := range syncDirs {
		abs := filepath.Join(p.serverRoot, dir)
		if err := p.addWatchRecursive(abs); err != nil {
			slog.Warn("peer primary: cannot watch directory", "dir", dir, "error", err)
		}
	}

	// Watch the parent directory for server.yaml changes (fsnotify watches
	// directories, not individual files).
	if err := w.Add(p.serverRoot); err != nil {
		slog.Warn("peer primary: cannot watch server root", "error", err)
	}

	// Start a goroutine per peer.
	p.mu.Lock()
	for _, entry := range p.peers {
		pc := p.startPeer(ctx, entry)
		p.conns = append(p.conns, pc)
	}
	p.mu.Unlock()

	slog.Info("peer primary: started", "peers", len(p.peers))

	// Run the file watcher event loop.
	p.watchLoop(ctx)

	close(p.stopped)
	return nil
}

// Stop shuts down the watcher and all peer connections.
func (p *Primary) Stop() {
	if p.cancel != nil {
		p.cancel()
	}

	if p.watcher != nil {
		p.watcher.Close()
	}

	p.mu.Lock()
	conns := p.conns
	p.conns = nil
	p.mu.Unlock()

	for _, pc := range conns {
		pc.cancel()
		<-pc.done
		if pc.conn != nil {
			pc.conn.Close()
		}
	}

	<-p.stopped
	slog.Info("peer primary: stopped")
}

// addWatchRecursive adds a directory and all subdirectories to the watcher.
func (p *Primary) addWatchRecursive(root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if watchErr := p.watcher.Add(path); watchErr != nil {
				slog.Warn("peer primary: cannot watch subdirectory", "path", path, "error", watchErr)
			}
		}
		return nil
	})
}

// watchLoop processes fsnotify events and dispatches debounced changes to peers.
func (p *Primary) watchLoop(ctx context.Context) {
	pending := make(map[string]struct{})
	var timer *time.Timer
	var timerC <-chan time.Time

	for {
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return

		case event, ok := <-p.watcher.Events:
			if !ok {
				return
			}
			if !p.isSyncPath(event.Name) {
				continue
			}

			// If a new directory is created inside a sync dir, watch it too.
			if event.Has(fsnotify.Create) {
				if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
					if werr := p.addWatchRecursive(event.Name); werr != nil {
						slog.Warn("peer: failed to watch new directory", "path", event.Name, "error", werr)
					}
				}
			}

			// Only react to meaningful operations.
			if event.Has(fsnotify.Create) || event.Has(fsnotify.Write) ||
				event.Has(fsnotify.Remove) || event.Has(fsnotify.Rename) {
				pending[event.Name] = struct{}{}
				// Reset debounce timer.
				if timer == nil {
					timer = time.NewTimer(debounceDuration)
					timerC = timer.C
				} else {
					timer.Reset(debounceDuration)
				}
			}

		case <-timerC:
			// Debounce window closed — send batched changes.
			if len(pending) > 0 {
				changes := p.buildChanges(pending)
				if len(changes) > 0 {
					p.broadcast(changes)
				}
				pending = make(map[string]struct{})
			}
			timer = nil
			timerC = nil

		case err, ok := <-p.watcher.Errors:
			if !ok {
				return
			}
			slog.Error("peer primary: watcher error", "error", err)
		}
	}
}

// isSyncPath returns true if the absolute path falls within a sync directory
// or is the server.yaml file.
func (p *Primary) isSyncPath(absPath string) bool {
	rel, err := filepath.Rel(p.serverRoot, absPath)
	if err != nil {
		return false
	}
	// Exact match on server.yaml.
	if rel == syncFile {
		return true
	}
	// Prefix match on sync directories.
	for _, dir := range syncDirs {
		if rel == dir || strings.HasPrefix(rel, dir+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// buildChanges reads the current state of each pending path and produces
// PeerFileChange messages. Deleted files have empty Content.
func (p *Primary) buildChanges(pending map[string]struct{}) []*pb.PeerFileChange {
	var changes []*pb.PeerFileChange
	for absPath := range pending {
		rel, err := filepath.Rel(p.serverRoot, absPath)
		if err != nil {
			slog.Warn("peer primary: cannot relativize path", "path", absPath, "error", err)
			continue
		}

		info, statErr := os.Stat(absPath)
		if statErr != nil {
			// File was deleted (or renamed away).
			changes = append(changes, &pb.PeerFileChange{
				Path: rel,
			})
			slog.Debug("peer primary: file deleted", "path", rel)
			continue
		}

		// Skip directories — we sync files only.
		if info.IsDir() {
			continue
		}

		content, readErr := os.ReadFile(absPath)
		if readErr != nil {
			slog.Warn("peer primary: cannot read file", "path", rel, "error", readErr)
			continue
		}

		changes = append(changes, &pb.PeerFileChange{
			Path:      rel,
			Content:   content,
			MtimeUnix: info.ModTime().Unix(),
			Mode:      uint32(info.Mode().Perm()),
		})
		slog.Debug("peer primary: file changed", "path", rel, "size", len(content))
	}
	return changes
}

// broadcast sends file changes to all connected peers by enqueueing them
// on each peer's change channel. Non-blocking: if a peer's channel is full,
// the changes are dropped with a warning (the peer will catch up on reconnect
// via FullSync).
func (p *Primary) broadcast(changes []*pb.PeerFileChange) {
	p.mu.Lock()
	conns := make([]*peerConn, len(p.conns))
	copy(conns, p.conns)
	p.mu.Unlock()

	for _, pc := range conns {
		select {
		case pc.changeCh <- changes:
		default:
			slog.Warn("peer primary: change channel full, dropping batch",
				"addr", pc.entry.Addr, "files", len(changes))
		}
	}
}

// fullSync walks all sync directories and server.yaml, building a PeerFullSync.
func (p *Primary) fullSync() *pb.PeerFullSync {
	var files []*pb.PeerFileChange

	for _, dir := range syncDirs {
		abs := filepath.Join(p.serverRoot, dir)
		_ = filepath.WalkDir(abs, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel, relErr := filepath.Rel(p.serverRoot, path)
			if relErr != nil {
				return nil
			}
			content, readErr := os.ReadFile(path)
			if readErr != nil {
				slog.Warn("peer primary: full sync: cannot read file", "path", rel, "error", readErr)
				return nil
			}
			info, statErr := d.Info()
			if statErr != nil {
				return nil
			}
			files = append(files, &pb.PeerFileChange{
				Path:      rel,
				Content:   content,
				MtimeUnix: info.ModTime().Unix(),
				Mode:      uint32(info.Mode().Perm()),
			})
			return nil
		})
	}

	// server.yaml
	syPath := filepath.Join(p.serverRoot, syncFile)
	if content, err := os.ReadFile(syPath); err == nil {
		if info, statErr := os.Stat(syPath); statErr == nil {
			files = append(files, &pb.PeerFileChange{
				Path:      syncFile,
				Content:   content,
				MtimeUnix: info.ModTime().Unix(),
				Mode:      uint32(info.Mode().Perm()),
			})
		}
	}

	slog.Info("peer primary: full sync prepared", "files", len(files))
	return &pb.PeerFullSync{Files: files}
}

// startPeer launches a goroutine that maintains a gRPC stream to a single peer.
// On disconnect it reconnects with exponential backoff (max 30s).
func (p *Primary) startPeer(ctx context.Context, entry config.PeerEntry) *peerConn {
	peerCtx, peerCancel := context.WithCancel(ctx)
	pc := &peerConn{
		entry:    entry,
		changeCh: make(chan []*pb.PeerFileChange, 64),
		cancel:   peerCancel,
		done:     make(chan struct{}),
	}

	go p.peerLoop(peerCtx, pc)

	return pc
}

// peerLoop connects to a peer and streams file changes. It reconnects on failure.
func (p *Primary) peerLoop(ctx context.Context, pc *peerConn) {
	defer close(pc.done)

	backoff := reconnectBase

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		slog.Info("peer primary: connecting to peer", "addr", pc.entry.Addr)

		dialOpts := []grpc.DialOption{grpc.WithTransportCredentials(credentials.NewTLS(p.tlsConfig))}
		if p.signer != nil {
			dialOpts = append(dialOpts, grpc.WithPerRPCCredentials(p.signer))
		}
		conn, err := grpc.NewClient(pc.entry.Addr, dialOpts...)
		if err != nil {
			slog.Error("peer primary: dial failed", "addr", pc.entry.Addr, "error", err)
			if !p.backoffSleep(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}
		pc.conn = conn

		client := pb.NewVigoPeerClient(conn)
		stream, err := client.SyncStream(ctx)
		if err != nil {
			slog.Error("peer primary: stream open failed", "addr", pc.entry.Addr, "error", err)
			conn.Close()
			if !p.backoffSleep(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}

		slog.Info("peer primary: connected to peer", "addr", pc.entry.Addr)

		// Send FullSync on initial connect.
		full := p.fullSync()
		err = stream.Send(&pb.PeerSyncMessage{
			Payload: &pb.PeerSyncMessage_FullSync{
				FullSync: full,
			},
		})
		if err != nil {
			slog.Error("peer primary: full sync send failed", "addr", pc.entry.Addr, "error", err)
			_ = stream.CloseSend()
			conn.Close()
			if !p.backoffSleep(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}

		slog.Info("peer primary: full sync sent", "addr", pc.entry.Addr, "files", len(full.Files))
		backoff = reconnectBase // Reset backoff on successful connect.

		// Stream loop: send changes, receive acks.
		if err := p.streamLoop(ctx, pc, stream); err != nil {
			slog.Warn("peer primary: stream disconnected", "addr", pc.entry.Addr, "error", err)
		}

		_ = stream.CloseSend()
		conn.Close()

		// Reconnect with backoff.
		if !p.backoffSleep(ctx, backoff) {
			return
		}
		backoff = nextBackoff(backoff)
	}
}

// streamLoop handles sending changes and receiving acks on an established stream.
func (p *Primary) streamLoop(
	ctx context.Context,
	pc *peerConn,
	stream grpc.BidiStreamingClient[pb.PeerSyncMessage, pb.PeerSyncMessage],
) error {
	// Start a goroutine to drain acks from the peer.
	ackErr := make(chan error, 1)
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				ackErr <- err
				return
			}
			if ack := msg.GetAck(); ack != nil {
				if !ack.Success {
					slog.Warn("peer primary: peer rejected file",
						"addr", pc.entry.Addr, "path", ack.Path, "error", ack.Error)
				}
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case err := <-ackErr:
			return fmt.Errorf("recv: %w", err)

		case changes := <-pc.changeCh:
			for _, ch := range changes {
				err := stream.Send(&pb.PeerSyncMessage{
					Payload: &pb.PeerSyncMessage_FileChange{
						FileChange: ch,
					},
				})
				if err != nil {
					return fmt.Errorf("send: %w", err)
				}
				slog.Debug("peer primary: pushed file change", "addr", pc.entry.Addr, "path", ch.Path)
			}
		}
	}
}

// backoffSleep sleeps for the given duration or returns false if ctx is cancelled.
func (p *Primary) backoffSleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// nextBackoff doubles the backoff duration, capped at reconnectMax.
func nextBackoff(current time.Duration) time.Duration {
	next := current * 2
	if next > reconnectMax {
		return reconnectMax
	}
	return next
}
