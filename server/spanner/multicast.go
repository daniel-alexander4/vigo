// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package spanner

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"time"

	"git.alexander4.io/vigo/server/db"
)

// Spanner Slice 2-i-b — the multicast Tier-1 fast-path (ADR-026 + the
// spanner-redesign plan). Slice 2-i-a shipped the Tier-2 layer: a 5m
// REST ticker that pulls every roster peer's full /snapshot and
// CRDT-merges it. Tier-1 is a purely-additive accelerator: each bolt
// periodically multicasts a tiny signed digest of its roster on
// 224.0.0.45:1534; a peer that hears a digest different from its own
// immediately fires the existing Tier-2 pullAndMerge for that peer
// instead of waiting for the timer. The 5m Tier-2 poll stays as the
// universal fallback for bolts that cannot reach the multicast group.
//
// This file ADDS the Tier-1 layer only — it never touches MergeSnapshot,
// the /snapshot endpoint, or the Tier-2 cycle/pullAndMerge logic.
//
// Wire framing mirrors the Rust longdrawer/lockbox discovery probes
// (agent/src/swarm/{longdrawer,lockbox}/discovery.rs): a line-delimited
// "HEADER\n<json>" datagram. The header here is multicastHeader.
//
// Security: the digest message is Ed25519-signed by the sending bolt,
// canonical-marshal discipline mirroring admission.go. A listener
// accepts a message only when (a) its spanner_id matches the local
// bolt's, (b) the sender's bolt pubkey is a known roster member, and
// (c) the signature verifies against that member's pubkey. The pull
// target hostname is then resolved from the local roster row — NEVER
// from the wire — so a forged message can never redirect a pull.

// multicastHeader is the framing header of a Tier-1 digest datagram.
const multicastHeader = "SPANNER-1"

// multicastReadBuf bounds a single datagram read. A digest message is
// a few hundred bytes; 4 KiB is generous headroom.
const multicastReadBuf = 4096

// multicastReadDeadline is how long a listener read blocks before it
// loops back to check ctx — the clean-shutdown poll interval.
const multicastReadDeadline = 2 * time.Second

// digestMessage is the signed Tier-1 announcement a bolt multicasts
// every snapshot.tier1_interval. It carries no hostname — the pull
// target is resolved from the local roster, never the wire.
//
// Slice 2-ii-a extends it with the sending bolt's convergence counters
// (Convergence). A listener that accepts the message updates its
// observability cache with these counters immediately — 30s-fresh
// fleet-convergence data without waiting for the 5m Tier-2
// /fleet-snapshot pull. The signature already covers the new field
// because it is part of digestMessageContent below.
type digestMessage struct {
	SpannerID   string              `json:"spanner_id"`   // federation anchor; must match the listener's
	BoltPubkey  string              `json:"bolt_pubkey"`  // sending bolt's pubkey-hex; looked up in the local roster
	Tier1Digest string              `json:"tier1_digest"` // Tier1Digest of the sender's roster + signed users (Slice 2-iii-e)
	Convergence ConvergenceCounters `json:"convergence"`  // sender's fleet-convergence counters (Slice 2-ii-a)
	SentAt      int64               `json:"sent_at"`      // ms-since-epoch; also the observability LWW key
	Signature   []byte              `json:"signature"`    // ed25519 over the content fields
}

// digestMessageContent is the canonical content of a digestMessage —
// every field except Signature. Marshalled deterministically for
// sign/verify, mirroring admission.go's inviteBundleContent.
type digestMessageContent struct {
	SpannerID   string              `json:"spanner_id"`
	BoltPubkey  string              `json:"bolt_pubkey"`
	Tier1Digest string              `json:"tier1_digest"`
	Convergence ConvergenceCounters `json:"convergence"`
	SentAt      int64               `json:"sent_at"`
}

// marshalDigestContent serializes a digestMessage's content fields
// (Signature cleared) for signing/verification.
func marshalDigestContent(m *digestMessage) ([]byte, error) {
	return json.Marshal(digestMessageContent{
		SpannerID:   m.SpannerID,
		BoltPubkey:  m.BoltPubkey,
		Tier1Digest: m.Tier1Digest,
		Convergence: m.Convergence,
		SentAt:      m.SentAt,
	})
}

// signDigestMessage fills SentAt and signs m's canonical content with
// the local bolt's signing key.
func signDigestMessage(signingKey ed25519.PrivateKey, m *digestMessage) error {
	if len(signingKey) != ed25519.PrivateKeySize {
		return fmt.Errorf("tier-1 sign: signing key is %d bytes, expected %d", len(signingKey), ed25519.PrivateKeySize)
	}
	content, err := marshalDigestContent(m)
	if err != nil {
		return fmt.Errorf("tier-1 sign: canonical marshal: %w", err)
	}
	m.Signature = ed25519.Sign(signingKey, content)
	return nil
}

// verifyDigestMessage checks m's signature against senderPubkey (the
// raw 32-byte Ed25519 pubkey of the bolt named in m.BoltPubkey).
// Returns nil on a valid signature.
func verifyDigestMessage(senderPubkey ed25519.PublicKey, m *digestMessage) error {
	if len(senderPubkey) != ed25519.PublicKeySize {
		return fmt.Errorf("tier-1 verify: sender pubkey is %d bytes, expected %d", len(senderPubkey), ed25519.PublicKeySize)
	}
	if len(m.Signature) != ed25519.SignatureSize {
		return fmt.Errorf("tier-1 verify: signature is %d bytes, expected %d", len(m.Signature), ed25519.SignatureSize)
	}
	content, err := marshalDigestContent(m)
	if err != nil {
		return fmt.Errorf("tier-1 verify: canonical marshal: %w", err)
	}
	if !ed25519.Verify(senderPubkey, content, m.Signature) {
		return fmt.Errorf("tier-1 verify: ed25519 verify failed")
	}
	return nil
}

// encodeDatagram frames a signed digestMessage as "SPANNER-1\n<json>".
func encodeDatagram(m *digestMessage) ([]byte, error) {
	body, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("tier-1: marshal datagram: %w", err)
	}
	out := make([]byte, 0, len(multicastHeader)+1+len(body))
	out = append(out, multicastHeader...)
	out = append(out, '\n')
	out = append(out, body...)
	return out, nil
}

// decodeDatagram parses a "SPANNER-1\n<json>" datagram. A datagram
// without the expected header or with an undecodable body is rejected.
func decodeDatagram(raw []byte) (*digestMessage, error) {
	prefix := multicastHeader + "\n"
	if len(raw) < len(prefix) || string(raw[:len(prefix)]) != prefix {
		return nil, fmt.Errorf("tier-1: datagram missing %q header", multicastHeader)
	}
	var m digestMessage
	if err := json.Unmarshal(raw[len(prefix):], &m); err != nil {
		return nil, fmt.Errorf("tier-1: decode datagram body: %w", err)
	}
	return &m, nil
}

// multicastAddr resolves the Tier-1 multicast group + port to a
// *net.UDPAddr.
func multicastAddr(group string, port int) (*net.UDPAddr, error) {
	addr, err := net.ResolveUDPAddr("udp4", fmt.Sprintf("%s:%d", group, port))
	if err != nil {
		return nil, fmt.Errorf("tier-1: resolve multicast addr %s:%d: %w", group, port, err)
	}
	return addr, nil
}

// runTier1Sender multicasts a signed roster digest every g.tier1Interval
// until ctx is cancelled. A send failure (no socket, no route) is
// logged and the loop continues — Tier-1 is best-effort and the Tier-2
// poll is the universal fallback.
func (g *Gossiper) runTier1Sender(ctx context.Context) {
	addr, err := multicastAddr(g.mcastGroup, g.mcastPort)
	if err != nil {
		slog.Warn("spanner tier-1 sender: cannot resolve multicast group; sender disabled", "error", err)
		return
	}
	slog.Info("spanner tier-1 sender started", "group", g.mcastGroup, "port", g.mcastPort, "interval", g.tier1Interval)
	ticker := time.NewTicker(g.tier1Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("spanner tier-1 sender stopped")
			return
		case <-ticker.C:
			g.sendDigest(ctx, addr)
		}
	}
}

// sendDigest loads the local roster, builds + signs a digest message,
// and writes one datagram to the multicast group. All failures log
// and return — never crash.
func (g *Gossiper) sendDigest(ctx context.Context, addr *net.UDPAddr) {
	if len(g.signingKey) != ed25519.PrivateKeySize {
		slog.Warn("spanner tier-1 sender: no bolt signing key; skipping send")
		return
	}
	roster, err := g.queries.ListBolts(ctx)
	if err != nil {
		slog.Warn("spanner tier-1 sender: listing local roster failed; skipping send", "error", err)
		return
	}
	// Slice 2-iii-e: webusers ride the same Tier-1 digest. A failure
	// listing users degrades the digest to roster-only — a peer still
	// picks up user changes via the 5m Tier-2 ticker; just slower.
	users, uErr := g.queries.ListUsersForReplication(ctx)
	if uErr != nil {
		slog.Warn("spanner tier-1 sender: listing local users failed; sending roster-only digest", "error", uErr)
		users = nil
	}
	var counters ConvergenceCounters
	if g.localCounters != nil {
		counters = g.localCounters()
	}
	msg := &digestMessage{
		SpannerID:   g.spannerID,
		BoltPubkey:  g.ownPubkey,
		Tier1Digest: Tier1Digest(roster, users),
		Convergence: counters,
		SentAt:      time.Now().UnixMilli(),
	}
	if err := signDigestMessage(g.signingKey, msg); err != nil {
		slog.Warn("spanner tier-1 sender: signing digest failed; skipping send", "error", err)
		return
	}
	datagram, err := encodeDatagram(msg)
	if err != nil {
		slog.Warn("spanner tier-1 sender: encoding datagram failed; skipping send", "error", err)
		return
	}

	conn, err := net.DialUDP("udp4", nil, addr)
	if err != nil {
		slog.Warn("spanner tier-1 sender: dialing multicast group failed; skipping send", "error", err)
		return
	}
	defer conn.Close()
	if _, err := conn.Write(datagram); err != nil {
		slog.Warn("spanner tier-1 sender: writing datagram failed; skipping send", "error", err)
		return
	}
	slog.Debug("spanner tier-1 sender: digest sent", "digest", msg.Tier1Digest)
}

// runTier1Listener joins the Tier-1 multicast group and processes
// inbound digest datagrams until ctx is cancelled. A read deadline is
// set on every iteration so a cancelled ctx is observed within
// multicastReadDeadline even when no datagram arrives; the UDPConn is
// also closed on shutdown so any in-flight read returns immediately.
func (g *Gossiper) runTier1Listener(ctx context.Context, founderPubkeyHook func(context.Context) (ed25519.PublicKey, error)) {
	addr, err := multicastAddr(g.mcastGroup, g.mcastPort)
	if err != nil {
		slog.Warn("spanner tier-1 listener: cannot resolve multicast group; listener disabled", "error", err)
		return
	}
	conn, err := net.ListenMulticastUDP("udp4", nil, addr)
	if err != nil {
		// CI/sandbox environments often cannot join a multicast group.
		// Tier-1 is best-effort: log and return, Tier-2 still runs.
		slog.Warn("spanner tier-1 listener: joining multicast group failed; listener disabled", "error", err)
		return
	}
	if err := conn.SetReadBuffer(multicastReadBuf * 16); err != nil {
		slog.Debug("spanner tier-1 listener: SetReadBuffer failed; continuing", "error", err)
	}
	slog.Info("spanner tier-1 listener started", "group", g.mcastGroup, "port", g.mcastPort)

	// Close the conn on ctx cancel so a blocked ReadFromUDP returns.
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()
	defer conn.Close()

	buf := make([]byte, multicastReadBuf)
	for {
		if ctx.Err() != nil {
			slog.Info("spanner tier-1 listener stopped")
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(multicastReadDeadline))
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				slog.Info("spanner tier-1 listener stopped")
				return
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				// Deadline tick — loop back to re-check ctx.
				continue
			}
			slog.Warn("spanner tier-1 listener: read failed; continuing", "error", err)
			continue
		}
		datagram := make([]byte, n)
		copy(datagram, buf[:n])
		g.handleTier1Datagram(ctx, datagram, founderPubkeyHook)
	}
}

// handleTier1Datagram parses and acts on one inbound datagram. The
// security checks (spanner-id match, sender-is-roster-member,
// signature verification, pull-target-from-local-roster) are isolated
// in handleTier1Message so unit tests can exercise the decision logic
// without a socket.
func (g *Gossiper) handleTier1Datagram(ctx context.Context, raw []byte, founderPubkeyHook func(context.Context) (ed25519.PublicKey, error)) {
	msg, err := decodeDatagram(raw)
	if err != nil {
		slog.Debug("spanner tier-1 listener: dropping malformed datagram", "error", err)
		return
	}
	g.handleTier1Message(ctx, msg, founderPubkeyHook)
}

// handleTier1Message runs the Tier-1 decision logic for one parsed
// digest message. In order:
//
//  1. spanner_id must match the local bolt's — else ignore.
//  2. the sender's bolt pubkey must be a known, non-revoked roster
//     member — an unknown sender is ignored (no pull).
//  3. the signature must verify against that member's pubkey — a bad
//     signature is ignored.
//  4. the message's Tier1Digest is compared to this bolt's own
//     current Tier1Digest; a match is a no-op, a mismatch fires the
//     existing Tier-2 pullAndMerge for the sender — using the sender's
//     hostname from the LOCAL roster row, never a hostname from the
//     wire.
//
// A small per-peer debounce (pullDebounce) refuses to re-pull the same
// peer more than once per pullDebounceWindow, so a convergence-window
// flurry of Tier-1 messages cannot cause a pull storm.
func (g *Gossiper) handleTier1Message(ctx context.Context, msg *digestMessage, founderPubkeyHook func(context.Context) (ed25519.PublicKey, error)) {
	// (1) spanner-id gate.
	if msg.SpannerID != g.spannerID {
		slog.Debug("spanner tier-1: ignoring digest for a different spanner",
			"got", msg.SpannerID, "want", g.spannerID)
		return
	}
	// Ignore our own multicast echo — a bolt hears itself on the group.
	if msg.BoltPubkey == g.ownPubkey {
		return
	}

	roster, err := g.queries.ListBolts(ctx)
	if err != nil {
		slog.Warn("spanner tier-1: listing local roster failed; ignoring digest", "error", err)
		return
	}

	// (2) sender must be a known, non-revoked roster member. The pull
	// target hostname comes from THIS row — never from the wire.
	var sender *db.SpannerBolt
	for i := range roster {
		if roster[i].Pubkey == msg.BoltPubkey {
			sender = &roster[i]
			break
		}
	}
	if sender == nil {
		slog.Debug("spanner tier-1: ignoring digest from a non-member sender", "sender", msg.BoltPubkey)
		return
	}
	if sender.RevokedAt.Valid {
		slog.Debug("spanner tier-1: ignoring digest from a revoked member", "sender", msg.BoltPubkey)
		return
	}

	// (3) signature must verify against the roster member's pubkey.
	senderPub, err := hex.DecodeString(sender.Pubkey)
	if err != nil || len(senderPub) != ed25519.PublicKeySize {
		slog.Warn("spanner tier-1: roster member pubkey is not a 32-byte hex key; ignoring digest", "sender", msg.BoltPubkey)
		return
	}
	if err := verifyDigestMessage(ed25519.PublicKey(senderPub), msg); err != nil {
		slog.Warn("spanner tier-1: ignoring digest with an invalid signature", "sender", msg.BoltPubkey, "error", err)
		return
	}

	// Slice 2-ii-a: the digest is verified and from a known member —
	// fold its convergence counters into the observability cache so
	// fleet-wide admin reads see 30s-fresh counters without waiting
	// for the 5m Tier-2 /fleet-snapshot pull. The envoy roster (large,
	// slow Tier-2 data) is left untouched. last-writer-wins by SentAt.
	if g.observ != nil {
		g.observ.UpdateCounters(msg.BoltPubkey, msg.Convergence, time.UnixMilli(msg.SentAt))
	}

	// (4) digest comparison — a match needs no pull.
	users, uErr := g.queries.ListUsersForReplication(ctx)
	if uErr != nil {
		// Same degradation rationale as the sender — degrade to a
		// roster-only local digest; worst case a user change still
		// propagates via the 5m Tier-2 ticker.
		slog.Warn("spanner tier-1: listing local users failed; comparing roster-only digest", "error", uErr)
		users = nil
	}
	if msg.Tier1Digest == Tier1Digest(roster, users) {
		slog.Debug("spanner tier-1: digest matches local state; no pull", "sender", msg.BoltPubkey)
		return
	}

	// Per-peer debounce — collapse a flurry of mismatching Tier-1
	// messages into at most one pull per peer per window.
	if !g.allowTier1Pull(msg.BoltPubkey) {
		slog.Debug("spanner tier-1: pull debounced for this peer", "sender", msg.BoltPubkey)
		return
	}

	founderPubkey, err := founderPubkeyHook(ctx)
	if err != nil {
		slog.Warn("spanner tier-1: cannot resolve founder pubkey; skipping pull", "error", err)
		return
	}

	slog.Debug("spanner tier-1: digest mismatch; firing Tier-2 pull",
		"peer", sender.Hostname, "sender", msg.BoltPubkey)
	n, err := g.pullAndMerge(ctx, sender.Hostname, founderPubkey)
	if err != nil {
		slog.Warn("spanner tier-1: triggered peer pull failed", "peer", sender.Hostname, "error", err)
		return
	}
	if n > 0 {
		slog.Info("spanner tier-1: fast-path merge applied peer changes", "peer", sender.Hostname, "rows_merged", n)
	}
}

// allowTier1Pull reports whether enough time has elapsed since the
// last Tier-1-triggered pull of peerPubkey to allow another. It
// records "now" as the last-pull time when it returns true.
func (g *Gossiper) allowTier1Pull(peerPubkey string) bool {
	g.pullDebounceMu.Lock()
	defer g.pullDebounceMu.Unlock()
	now := time.Now()
	if last, ok := g.pullDebounce[peerPubkey]; ok && now.Sub(last) < pullDebounceWindow {
		return false
	}
	g.pullDebounce[peerPubkey] = now
	return true
}
