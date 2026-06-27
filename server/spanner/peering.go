// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package spanner

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

// Config-driven hands-off spanner peering (ADR-026). Instead of the
// manual `vigocli spanner bolt invite`/`join` token ceremony, an
// operator lists peer addresses in `spanner.peers`; on boot a joining
// bolt auto-admits itself with the founder over the
// /api/v1/spanner/bolt/peer-admit channel and seeds its roster, after
// which the existing Tier-1/Tier-2 gossip maintains convergence.
//
// The admission model stays FOUNDER-ROUTED — the founder is the sole
// admission authority, exactly as it already is for invite/join. The
// spanner CRDT is founder-anchored: MergeSnapshot drops every roster
// row whose admitted_by is not the founder, so a self-admitted or
// mutually-admitted row never converges. Mutual self-admission was
// rejected for that reason (and on security: a multi-root CRDT lets a
// slipped rogue vouch for further rogues — founder-routing bounds a
// rogue to the single row it can talk the founder into).
//
// The CA stays OFF the wire (the operator distributes the shared fleet
// CA out-of-band via `vigocli spanner ca export|import`). That is what
// makes the deliberately-weak IP-only admission gate acceptable: a
// rogue bolt that slips the gate is bounded — it can pollute
// observability and play license-count games, but it cannot read fleet
// data, receive routed envoys, or mint trusted certs, because all of
// those sit behind the fleet-CA mTLS it does not hold.

// peerIdentityDomain domain-separates the joiner's self-signature so a
// signature minted for any other purpose can never be replayed as a
// peer-admit identity proof.
const peerIdentityDomain = "vigo-spanner-peer-admit-v1\x00"

// PeerAdmitRequest is the body a joining bolt POSTs to a founder's
// POST /api/v1/spanner/bolt/peer-admit. The Ed25519 self-signature
// over the canonical identity proves the requester holds the private
// key for JoinerPubkey — without it, anyone could ask the founder to
// admit a bolt under a pubkey they do not control. The founder
// additionally checks the source-address allowlist (its own
// spanner.peers) and the spanner_id before minting a founder-signed
// admission row.
type PeerAdmitRequest struct {
	JoinerPubkey  string   `json:"joiner_pubkey"` // hex-encoded raw Ed25519 pubkey
	Hostname      string   `json:"hostname"`      // joining bolt's envoy hostname
	PatternClaims []string `json:"pattern_claims"`
	SpannerID     string   `json:"spanner_id"`
	Signature     []byte   `json:"signature"` // self-signature over the canonical identity
}

// PeerAdmitResponse is what the founder returns: the founder's pubkey
// (the trust anchor for verifying every roster row — trust-on-first-use
// of the configured founder address, acceptable because the fleet-CA
// mTLS boundary is what actually gates data access), the joiner's
// freshly-minted admission row, the full current roster, and the
// consumed-token G-Set rows the roster's FKs point at. A non-founder
// peer returns this same shape with a 403 and an empty AdmittedRow so a
// misdirected joiner can discover the real founder.
type PeerAdmitResponse struct {
	FounderPubkey  string          `json:"founder_pubkey"` // hex; TOFU trust anchor
	AdmittedRow    RosterRow       `json:"admitted_row"`
	Roster         []RosterRow     `json:"roster"`
	ConsumedTokens []ConsumedToken `json:"consumed_tokens"`
}

// peerIdentityContent is the deterministic content the joiner signs.
type peerIdentityContent struct {
	Pubkey        string   `json:"pubkey"`
	Hostname      string   `json:"hostname"`
	SpannerID     string   `json:"spanner_id"`
	PatternClaims []string `json:"pattern_claims"`
}

// canonicalPeerIdentity serializes a PeerAdmitRequest's identity for
// signing/verification, prefixed with the domain string.
func canonicalPeerIdentity(req *PeerAdmitRequest) ([]byte, error) {
	body, err := json.Marshal(peerIdentityContent{
		Pubkey:        req.JoinerPubkey,
		Hostname:      req.Hostname,
		SpannerID:     req.SpannerID,
		PatternClaims: req.PatternClaims,
	})
	if err != nil {
		return nil, fmt.Errorf("peer identity: canonical marshal: %w", err)
	}
	return append([]byte(peerIdentityDomain), body...), nil
}

// SignPeerIdentity signs req's canonical identity content with the
// joining bolt's signing key. The returned bytes go in req.Signature.
func SignPeerIdentity(signingKey ed25519.PrivateKey, req *PeerAdmitRequest) ([]byte, error) {
	if len(signingKey) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("peer identity sign: signing key is %d bytes, expected %d", len(signingKey), ed25519.PrivateKeySize)
	}
	content, err := canonicalPeerIdentity(req)
	if err != nil {
		return nil, err
	}
	return ed25519.Sign(signingKey, content), nil
}

// VerifyPeerIdentity checks that req.Signature is a valid self-signature
// over req's canonical identity, made by the holder of req.JoinerPubkey.
// Returns the decoded pubkey on success.
func VerifyPeerIdentity(req *PeerAdmitRequest) (ed25519.PublicKey, error) {
	raw, err := hex.DecodeString(req.JoinerPubkey)
	if err != nil {
		return nil, fmt.Errorf("peer identity verify: pubkey is not hex: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("peer identity verify: pubkey is %d bytes, expected %d", len(raw), ed25519.PublicKeySize)
	}
	if len(req.Signature) != ed25519.SignatureSize {
		return nil, fmt.Errorf("peer identity verify: signature is %d bytes, expected %d", len(req.Signature), ed25519.SignatureSize)
	}
	content, err := canonicalPeerIdentity(req)
	if err != nil {
		return nil, err
	}
	if !ed25519.Verify(ed25519.PublicKey(raw), content, req.Signature) {
		return nil, fmt.Errorf("peer identity verify: ed25519 verify failed")
	}
	return ed25519.PublicKey(raw), nil
}

// RosterHasFounderSelfAdmit reports whether roster contains the founder's
// own self-admit row (pubkey == admitted_by == founderHex, no consumed
// token) — the sanity check a joiner runs before trusting a founder
// pubkey advertised in a PeerAdmitResponse.
func RosterHasFounderSelfAdmit(roster []RosterRow, founderHex string) bool {
	for _, rr := range roster {
		if rr.Pubkey == founderHex && rr.AdmittedBy == founderHex && rr.ConsumedTokenHash == "" {
			return true
		}
	}
	return false
}

// autoAdmitBackoffStart / autoAdmitBackoffMax bound the joiner's retry
// loop: a fresh joiner whose founder is briefly unreachable at boot
// retries with exponential backoff rather than giving up.
const (
	autoAdmitBackoffStart = 2 * time.Second
	autoAdmitBackoffMax   = 2 * time.Minute
)

// AutoAdmit runs the boot-time config-driven peering loop on a joining
// bolt. It is a no-op when peers is empty, when this bolt has no signing
// key, or when this bolt already knows its founder (it was founded by
// `spanner init`, joined earlier, or already auto-admitted) — once a
// founder row is local, the gossiper maintains convergence and there is
// nothing to bootstrap. Otherwise it POSTs this bolt's self-signed
// identity to each peer until one (the founder) admits it and returns a
// founder-signed roster, which is CRDT-merged via MergeSnapshot. Best
// effort with exponential backoff; runs entirely off the check-in hot
// path. Call as a goroutine.
func (g *Gossiper) AutoAdmit(ctx context.Context, peers []string, hostname string, patterns []string) {
	if len(peers) == 0 {
		return
	}
	if len(g.signingKey) != ed25519.PrivateKeySize {
		slog.Warn("spanner auto-admit: no bolt signing key — skipping config-driven peering")
		return
	}
	if g.haveFounder(ctx) {
		// Already seeded (founder, prior join, or prior auto-admit).
		return
	}

	slog.Info("spanner auto-admit: no founder row yet — bootstrapping from configured peers", "peers", peers, "own_pubkey", g.ownPubkey)

	backoff := autoAdmitBackoffStart
	for {
		if g.tryAutoAdmit(ctx, peers, hostname, patterns) {
			slog.Info("spanner auto-admit: roster seeded from a peer founder", "own_pubkey", g.ownPubkey)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > autoAdmitBackoffMax {
			backoff = autoAdmitBackoffMax
		}
	}
}

// tryAutoAdmit makes one pass over every configured peer, returning true
// as soon as a founder row exists locally.
func (g *Gossiper) tryAutoAdmit(ctx context.Context, peers []string, hostname string, patterns []string) bool {
	for _, peer := range peers {
		if err := g.autoAdmitPeer(ctx, peer, hostname, patterns); err != nil {
			slog.Debug("spanner auto-admit: peer attempt failed", "peer", peer, "error", err)
		}
		if g.haveFounder(ctx) {
			return true
		}
	}
	return false
}

// autoAdmitPeer POSTs this bolt's self-signed identity to one peer's
// /peer-admit and, on a founder-signed roster, CRDT-merges it. A
// non-founder peer returns 403 (a non-2xx), which surfaces here as an
// error and moves the caller on to the next peer.
func (g *Gossiper) autoAdmitPeer(ctx context.Context, peer, hostname string, patterns []string) error {
	req := &PeerAdmitRequest{
		JoinerPubkey:  g.ownPubkey,
		Hostname:      hostname,
		PatternClaims: patterns,
		SpannerID:     g.spannerID,
	}
	sig, err := SignPeerIdentity(g.signingKey, req)
	if err != nil {
		return fmt.Errorf("signing identity: %w", err)
	}
	req.Signature = sig

	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("encoding request: %w", err)
	}

	url := normalizePeerBase(peer, g.httpsPort) + "/api/v1/spanner/bolt/peer-admit"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := g.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("calling peer-admit: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("peer-admit returned %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	var par PeerAdmitResponse
	if err := json.NewDecoder(resp.Body).Decode(&par); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}

	founderRaw, err := hex.DecodeString(par.FounderPubkey)
	if err != nil || len(founderRaw) != ed25519.PublicKeySize {
		return fmt.Errorf("response carries an invalid founder pubkey %q", par.FounderPubkey)
	}
	// Sanity: the response must contain the founder's own self-admit row.
	// MergeSnapshot then verifies every row's signature against this
	// founder key, so a response that merely names a founder without
	// proving it (a valid signed self-admit row) is rejected here.
	if !RosterHasFounderSelfAdmit(par.Roster, par.FounderPubkey) {
		return fmt.Errorf("response roster has no founder self-admit row for %s", par.FounderPubkey)
	}

	snap := Snapshot{Roster: par.Roster, ConsumedTokens: par.ConsumedTokens}
	merged, err := MergeSnapshot(ctx, g.queries, g.sqlDB, snap, ed25519.PublicKey(founderRaw), g.sessions, g.audit)
	if err != nil {
		return fmt.Errorf("merging founder roster: %w", err)
	}
	slog.Info("spanner auto-admit: merged founder roster", "peer", peer, "founder", par.FounderPubkey, "rows_merged", merged)
	return nil
}

// haveFounder reports whether the local roster already resolves a single
// founder self-admit row — the signal that this bolt is bootstrapped and
// AutoAdmit has nothing left to do.
func (g *Gossiper) haveFounder(ctx context.Context) bool {
	roster, err := g.queries.ListBolts(ctx)
	if err != nil {
		return false
	}
	_, err = FounderPubkey(roster)
	return err == nil
}

// normalizePeerBase turns a configured peer entry ("10.0.0.2",
// "bolt-3.dc1:8443", or "https://host:8443") into an "https://host:port"
// base URL, defaulting the port to defaultPort when the entry omits one.
func normalizePeerBase(peer, defaultPort string) string {
	p := strings.TrimSpace(peer)
	if strings.HasPrefix(p, "http://") || strings.HasPrefix(p, "https://") {
		return strings.TrimRight(p, "/")
	}
	host := p
	if _, _, err := net.SplitHostPort(p); err != nil {
		// No port present — attach the default.
		host = net.JoinHostPort(p, defaultPort)
	}
	return "https://" + host
}
