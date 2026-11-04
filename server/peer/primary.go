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

// maxReplicatedFileBytes caps one replicated file. A single PeerFileChange
// must fit under the peer's 8 MiB gRPC receive limit (grpc/server.go); a file
// past this is skipped with an error rather than wedging the stream in a
// send-fail/reconnect loop.
const maxReplicatedFileBytes = 7 << 20

// fullSyncChunkBytes bounds the content carried by one PeerFullSync message.
// The full sync is split into as many messages as it needs so no single
// message approaches the receive limit.
const fullSyncChunkBytes = 4 << 20

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

	// base is cancelled by Stop. It and cancel are created in NewPrimary so
	// Stop never races Start for them and works even if Start never ran.
	base   context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	conns   []*peerConn
	started bool
	stopped chan struct{}
}

// peerConn tracks the gRPC connection and stream to a single peer.
type peerConn struct {
	entry    config.PeerEntry
	changeCh chan []*pb.PeerFileChange
	// resync is signalled when a batch for this peer was dropped (changeCh
	// full); the stream loop answers it with a fresh full sync so the drop is
	// repaired rather than lost.
	resync chan struct{}
	cancel context.CancelFunc
	done   chan struct{}
}

// NewPrimary creates a Primary that will sync files from serverRoot to the
// given peers using mTLS. signer attaches this server's control-plane identity
// to the outbound SyncStream so each peer can authorize it; nil on a non-local
// secrets backend (peers reject the unsigned stream — replication fails closed).
func NewPrimary(serverRoot string, peers []config.PeerEntry, tlsConfig *tls.Config, signer credentials.PerRPCCredentials) *Primary {
	base, cancel := context.WithCancel(context.Background())
	return &Primary{
		serverRoot: serverRoot,
		peers:      peers,
		tlsConfig:  tlsConfig,
		signer:     signer,
		base:       base,
		cancel:     cancel,
		stopped:    make(chan struct{}),
	}
}

// Start begins watching for file changes and connecting to peers.
// It blocks until the context is cancelled or Stop is called. On return —
// including an early error — every peer goroutine has exited and the watcher
// is closed. Start runs at most once per Primary; after Stop it refuses.
func (p *Primary) Start(ctx context.Context) error {
	p.mu.Lock()
	if p.started {
		p.mu.Unlock()
		return fmt.Errorf("peer primary: already started or stopped")
	}
	p.started = true
	p.mu.Unlock()
	defer close(p.stopped)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(p.base, cancel)()

	w, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("peer primary: create watcher: %w", err)
	}
	defer w.Close()
	p.watcher = w

	// Watch the replicated directories recursively.
	for _, dir := range replicatedDirs {
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

	// Drain the peer goroutines before reporting stopped. Each closes its own
	// gRPC connection on the way out.
	p.mu.Lock()
	conns := p.conns
	p.conns = nil
	p.mu.Unlock()
	for _, pc := range conns {
		pc.cancel()
		<-pc.done
	}
	return nil
}

// Stop shuts down the watcher and all peer connections and waits for Start to
// return. Safe to call before, during, or after Start, and if Start failed
// early; a Primary whose Start never ran returns immediately (and a Start
// arriving after Stop refuses to run).
func (p *Primary) Stop() {
	p.cancel()
	p.mu.Lock()
	started := p.started
	p.started = true
	p.mu.Unlock()
	if started {
		<-p.stopped
	} else {
		close(p.stopped)
	}
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

// isSyncPath returns true if the absolute path falls within the replicated
// set (paths.go) — the same allowlist a peer enforces on receive.
func (p *Primary) isSyncPath(absPath string) bool {
	rel, err := filepath.Rel(p.serverRoot, absPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return replicatedTop(rel) != ""
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

		if info.Size() > maxReplicatedFileBytes {
			slog.Error("peer primary: file too large to replicate, skipped",
				"path", rel, "size", info.Size(), "max", maxReplicatedFileBytes)
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
// the batch is dropped and the peer is flagged for a resync — its stream loop
// then sends a fresh full sync, which carries the dropped files.
func (p *Primary) broadcast(changes []*pb.PeerFileChange) {
	p.mu.Lock()
	conns := make([]*peerConn, len(p.conns))
	copy(conns, p.conns)
	p.mu.Unlock()

	for _, pc := range conns {
		select {
		case pc.changeCh <- changes:
		default:
			slog.Warn("peer primary: change channel full, dropping batch; full resync scheduled",
				"addr", pc.entry.Addr, "files", len(changes))
			select {
			case pc.resync <- struct{}{}:
			default: // a resync is already pending
			}
		}
	}
}

// fullSync walks the replicated set and returns it as one or more PeerFullSync
// messages, each carrying at most fullSyncChunkBytes of content (a single file
// larger than the chunk travels alone; one past maxReplicatedFileBytes is
// skipped with an error).
func (p *Primary) fullSync() []*pb.PeerFullSync {
	var files []*pb.PeerFileChange

	add := func(path, rel string, info fs.FileInfo) {
		if info.Size() > maxReplicatedFileBytes {
			slog.Error("peer primary: full sync: file too large to replicate, skipped",
				"path", rel, "size", info.Size(), "max", maxReplicatedFileBytes)
			return
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			slog.Warn("peer primary: full sync: cannot read file", "path", rel, "error", readErr)
			return
		}
		files = append(files, &pb.PeerFileChange{
			Path:      rel,
			Content:   content,
			MtimeUnix: info.ModTime().Unix(),
			Mode:      uint32(info.Mode().Perm()),
		})
	}

	for _, dir := range replicatedDirs {
		abs := filepath.Join(p.serverRoot, dir)
		_ = filepath.WalkDir(abs, func(path string, d fs.DirEntry, err error) error {
			if err != nil || !d.Type().IsRegular() {
				return nil
			}
			rel, relErr := filepath.Rel(p.serverRoot, path)
			if relErr != nil {
				return nil
			}
			info, statErr := d.Info()
			if statErr != nil {
				return nil
			}
			add(path, rel, info)
			return nil
		})
	}

	syPath := filepath.Join(p.serverRoot, replicatedFile)
	if info, err := os.Stat(syPath); err == nil && info.Mode().IsRegular() {
		add(syPath, replicatedFile, info)
	}

	chunks := chunkFullSync(files, fullSyncChunkBytes)
	slog.Info("peer primary: full sync prepared", "files", len(files), "messages", len(chunks))
	return chunks
}

// chunkFullSync splits files into PeerFullSync messages whose content sums to
// at most limit bytes each; a file larger than limit gets a message of its own.
// Always returns at least one message so an empty tree still announces itself.
func chunkFullSync(files []*pb.PeerFileChange, limit int) []*pb.PeerFullSync {
	var out []*pb.PeerFullSync
	cur := &pb.PeerFullSync{}
	size := 0
	for _, f := range files {
		n := len(f.Content)
		if len(cur.Files) > 0 && size+n > limit {
			out = append(out, cur)
			cur = &pb.PeerFullSync{}
			size = 0
		}
		cur.Files = append(cur.Files, f)
		size += n
	}
	return append(out, cur)
}

// sendFullSync sends every full-sync chunk on stream and returns the file count.
func (p *Primary) sendFullSync(stream grpc.BidiStreamingClient[pb.PeerSyncMessage, pb.PeerSyncMessage]) (int, error) {
	n := 0
	for _, chunk := range p.fullSync() {
		if err := stream.Send(&pb.PeerSyncMessage{
			Payload: &pb.PeerSyncMessage_FullSync{FullSync: chunk},
		}); err != nil {
			return n, err
		}
		n += len(chunk.Files)
	}
	return n, nil
}

// startPeer launches a goroutine that maintains a gRPC stream to a single peer.
// On disconnect it reconnects with exponential backoff (max 30s).
func (p *Primary) startPeer(ctx context.Context, entry config.PeerEntry) *peerConn {
	peerCtx, peerCancel := context.WithCancel(ctx)
	pc := &peerConn{
		entry:    entry,
		changeCh: make(chan []*pb.PeerFileChange, 64),
		resync:   make(chan struct{}, 1),
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

		// Send FullSync on initial connect. A pending resync is satisfied by it.
		select {
		case <-pc.resync:
		default:
		}
		sent, err := p.sendFullSync(stream)
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

		slog.Info("peer primary: full sync sent", "addr", pc.entry.Addr, "files", sent)
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

		case <-pc.resync:
			sent, err := p.sendFullSync(stream)
			if err != nil {
				return fmt.Errorf("resync send: %w", err)
			}
			slog.Info("peer primary: resync sent after dropped batch", "addr", pc.entry.Addr, "files", sent)

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
