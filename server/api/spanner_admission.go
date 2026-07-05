// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/bolt"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/spanner"
)

// Spanner Slice 2A.2 — the membership ceremony (ADR-026). This
// handler owns the four hand-written REST endpoints that found a
// spanner, mint a join invite, admit a joining bolt, and join an
// existing spanner. No gossip, no CRDT merge — that is Slice 2.
//
// Endpoint map:
//
//   POST /api/v1/spanner/init         (admin)   — found the spanner here
//   POST /api/v1/spanner/bolt/invite  (admin)   — mint+sign an invite bundle
//   POST /api/v1/spanner/bolt/admit   (bundle)  — admit a joining bolt
//   POST /api/v1/spanner/bolt/join    (admin)   — join an existing spanner
//   GET  /api/v1/spanner/roster       (read)    — inspect the DB roster
//
// /admit is authenticated by the invite bundle itself, not an admin
// Bearer token — it is the server-to-server call a joining bolt
// makes. The bundle's signature + single-use token are the
// authorization factors.

// SpannerAdmissionHandler serves the membership-ceremony endpoints
// plus the Slice 2-ii-a fleet-observability snapshot endpoint.
type SpannerAdmissionHandler struct {
	sqlDB       *sql.DB // for the atomic ConsumeToken+InsertBolt transaction
	queries     *db.Queries
	boltID      *bolt.Identity // local bolt signing identity; nil → ceremony unavailable
	spannerID   string
	hostname    string   // this vigosrv's own envoy hostname
	patterns    []string // this bolt's configured pattern claims
	peers       []string // configured spanner.peers — the founder's who-may-join allowlist (source-addr gate)
	auditWriter auditRecorder
	httpClient  *http.Client // for the cross-vigosrv /admit call from /join
	// fleetIndex is the local observability source for the Slice
	// 2-ii-a /fleet-snapshot endpoint. nil → /fleet-snapshot 503s.
	fleetIndex *fleet.FleetIndex
}

// SetFleetIndex attaches the in-memory fleet index that backs the
// Slice 2-ii-a /fleet-snapshot endpoint. Wired in main.go where the
// index is in scope.
func (h *SpannerAdmissionHandler) SetFleetIndex(fi *fleet.FleetIndex) {
	h.fleetIndex = fi
}

// NewSpannerAdmissionHandler constructs the ceremony handler. boltID
// may be nil (standalone mode or a non-local secrets backend); in
// that case every mutating endpoint returns 503 — the ceremony
// needs a bolt identity to sign with.
func NewSpannerAdmissionHandler(sqlDB *sql.DB, queries *db.Queries, boltID *bolt.Identity, spannerID, hostname string, patterns, peers []string, auditWriter auditRecorder, httpClient *http.Client) *SpannerAdmissionHandler {
	if httpClient == nil {
		// The /join → /admit call crosses to a separate vigosrv
		// deployment whose CA this server does not hold, so TLS chain
		// verification cannot succeed. InsecureSkipVerify is deliberate:
		// the trust anchor is the Ed25519-signed invite bundle and the
		// per-row admission signatures verified in handleJoin, not the
		// TLS certificate. TLS here is transport confidentiality only.
		httpClient = &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // trust is the signed bundle + signed rows; see comment
			},
		}
	}
	return &SpannerAdmissionHandler{
		sqlDB:       sqlDB,
		queries:     queries,
		boltID:      boltID,
		spannerID:   spannerID,
		hostname:    hostname,
		patterns:    patterns,
		peers:       peers,
		auditWriter: auditWriter,
		httpClient:  httpClient,
	}
}

// RegisterRoutes mounts the ceremony endpoints. init/invite/join are
// admin-gated (operator-driven). admit is bundle-authenticated, not
// Bearer-gated — a joining vigosrv is a peer, not an enrolled envoy.
// roster is an OPERATOR read (read-gated). snapshot + fleet-snapshot are
// the inter-bolt Tier-2 gossip pulls: a PEER bolt's gossiper GETs them
// with NO operator session and verifies the founder/bolt SIGNATURE on
// the payload (MergeSnapshot), not the caller — so they're registered
// BARE and listed in auth.publicPaths, exactly like /admit. (A read()
// wrapper here 401s the session-less gossiper and observability never
// converges — found standing up a real 2-bolt spanner.)
func (h *SpannerAdmissionHandler) RegisterRoutes(mux *http.ServeMux) {
	read := auth.RequirePermission(auth.PermFleetRead)
	admin := auth.RequireAdmin("")
	mux.Handle("POST /api/v1/spanner/init", admin(http.HandlerFunc(h.handleInit)))
	mux.Handle("POST /api/v1/spanner/bolt/invite", admin(http.HandlerFunc(h.handleInvite)))
	mux.Handle("POST /api/v1/spanner/bolt/admit", http.HandlerFunc(h.handleAdmit))
	// Config-driven hands-off peering (ADR-026) — a joining bolt POSTs its
	// self-signed identity here on boot; only the founder admits. Bare like
	// /admit: the joiner has no operator session, authenticated by
	// spanner_id + self-signature + source-addr allowlist (see handlePeerAdmit).
	mux.Handle("POST /api/v1/spanner/bolt/peer-admit", http.HandlerFunc(h.handlePeerAdmit))
	mux.Handle("POST /api/v1/spanner/bolt/join", admin(http.HandlerFunc(h.handleJoin)))
	mux.Handle("GET /api/v1/spanner/roster", read(http.HandlerFunc(h.handleRoster)))
	// Inter-bolt gossip pulls — signature-authenticated, registered bare
	// (see the publicPaths entries). NOT read()-wrapped: the peer gossiper
	// carries no operator session.
	mux.Handle("GET /api/v1/spanner/snapshot", http.HandlerFunc(h.handleSnapshot))
	mux.Handle("GET /api/v1/spanner/fleet-snapshot", http.HandlerFunc(h.handleFleetSnapshot))
}

// localPubkeyHex returns the local bolt pubkey as a lowercase hex
// string — the canonical identifier used in spanner_bolts.pubkey and
// the audit actor field.
func (h *SpannerAdmissionHandler) localPubkeyHex() string {
	return hex.EncodeToString(h.boltID.PublicKey)
}

// handleInit founds the spanner on THIS server: writes the founder
// self-admit row into spanner_bolts.
func (h *SpannerAdmissionHandler) handleInit(w http.ResponseWriter, r *http.Request) {
	if h.boltID == nil {
		writeError(w, http.StatusServiceUnavailable, "spanner ceremony requires a bolt identity (spanner mode + local secrets backend)")
		return
	}
	if h.spannerID == "" {
		writeError(w, http.StatusBadRequest, "spanner.spanner_id is not set in server.yaml — set it before founding the spanner")
		return
	}

	ownPubkey := h.localPubkeyHex()

	// Refuse if a founding row for this server's own bolt already exists.
	if _, err := h.queries.GetBolt(r.Context(), ownPubkey); err == nil {
		writeError(w, http.StatusConflict, "this server already holds a spanner_bolts row — the spanner is already founded here")
		return
	} else if !errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "checking for existing founder row: "+err.Error())
		return
	}

	patternClaims, err := json.Marshal(h.patterns)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encoding pattern claims: "+err.Error())
		return
	}

	now := time.Now().UnixMilli()
	row := &spanner.AdmissionRow{
		Pubkey:            ownPubkey,
		Hostname:          h.hostname,
		AdmittedAt:        now,
		AdmittedBy:        ownPubkey, // self-admit
		ConsumedTokenHash: "",
		PatternClaims:     string(patternClaims),
	}
	sig, err := spanner.SignAdmissionRow(h.boltID.SigningKey, row)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "signing founder admission row: "+err.Error())
		return
	}

	if err := h.queries.InsertBolt(r.Context(), admissionRowToInsertParams(row, sig)); err != nil {
		writeError(w, http.StatusInternalServerError, "inserting founder row: "+err.Error())
		return
	}

	recordAudit(r.Context(), h.auditWriter, "spanner.id_initialized", ownPubkey, r.RemoteAddr,
		fmt.Sprintf("spanner %q founded on %s", h.spannerID, h.hostname), "")

	slog.Info("spanner founded", "spanner_id", h.spannerID, "founder_pubkey", ownPubkey, "hostname", h.hostname)

	writeJSON(w, r, http.StatusOK, map[string]any{
		"spanner_id":     h.spannerID,
		"founder_pubkey": ownPubkey,
		"hostname":       h.hostname,
	})
}

// inviteRequest is the body of POST /api/v1/spanner/bolt/invite.
type inviteRequest struct {
	// TTLSeconds is how long the minted bundle stays valid. Defaults
	// to 24h if zero or negative.
	TTLSeconds int64 `json:"ttl_seconds"`
}

// handleInvite mints + signs an InviteBundle server-side (signing
// needs the bolt identity, so this cannot run CLI-side) and returns
// the opaque bundle string.
func (h *SpannerAdmissionHandler) handleInvite(w http.ResponseWriter, r *http.Request) {
	if h.boltID == nil {
		writeError(w, http.StatusServiceUnavailable, "spanner ceremony requires a bolt identity (spanner mode + local secrets backend)")
		return
	}
	if h.spannerID == "" {
		writeError(w, http.StatusBadRequest, "spanner.spanner_id is not set in server.yaml")
		return
	}

	var req inviteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	ttl := 24 * time.Hour
	if req.TTLSeconds > 0 {
		ttl = time.Duration(req.TTLSeconds) * time.Second
	}

	peerAddr := "https://" + h.hostname + ":8443"
	bundle, err := spanner.MintInviteBundle(h.boltID.SigningKey, h.spannerID, peerAddr, ttl)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "minting invite bundle: "+err.Error())
		return
	}
	bundleStr, err := spanner.MarshalBundle(bundle)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "marshalling invite bundle: "+err.Error())
		return
	}

	recordAudit(r.Context(), h.auditWriter, "spanner.bolt_invite_issued", h.localPubkeyHex(), r.RemoteAddr,
		fmt.Sprintf("issued spanner-join invite bundle (ttl=%s, expires=%d)", ttl, bundle.ExpiresAt), "")

	writeJSON(w, r, http.StatusOK, map[string]any{
		"bundle":     bundleStr,
		"spanner_id": h.spannerID,
		"peer_addr":  peerAddr,
		"expires_at": bundle.ExpiresAt,
	})
}

// admitRequest is the body of POST /api/v1/spanner/bolt/admit — the
// server-to-server call the joining bolt makes.
type admitRequest struct {
	JoinerPubkey  string   `json:"joiner_pubkey"`  // hex-encoded raw Ed25519 pubkey
	Hostname      string   `json:"hostname"`       // joining bolt's envoy hostname
	PatternClaims []string `json:"pattern_claims"` // joining bolt's pattern claims
	Bundle        string   `json:"bundle"`         // opaque invite-bundle string
}

// admitResponse is what /admit returns to the joining bolt. The
// roster's bolt rows may carry a consumed_token_hash FK; the
// matching consumed-token rows travel alongside so the joiner can
// satisfy the foreign key (token row INSERTed first) when it
// persists the roster locally. Slice 2-i-a's CRDT generalizes this
// token-then-bolt pairing into the gossip merge — the wire types
// (RosterRow, ConsumedToken, Snapshot) are owned by server/spanner
// so the handler and the gossiper share one shape.
type admitResponse struct {
	AdmittedRow    spanner.RosterRow       `json:"admitted_row"`    // the joiner's signed row
	Roster         []spanner.RosterRow     `json:"roster"`          // full current roster
	ConsumedTokens []spanner.ConsumedToken `json:"consumed_tokens"` // G-Set rows the roster's FKs point at
}

// handleAdmit verifies the invite bundle, consumes its token, and
// inserts the joining bolt's admission row — token INSERT + bolt
// INSERT in one sql.Tx, token first (it is the FK target).
func (h *SpannerAdmissionHandler) handleAdmit(w http.ResponseWriter, r *http.Request) {
	if h.boltID == nil {
		writeError(w, http.StatusServiceUnavailable, "spanner ceremony requires a bolt identity (spanner mode + local secrets backend)")
		return
	}

	var req admitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.JoinerPubkey == "" || req.Hostname == "" || req.Bundle == "" {
		writeError(w, http.StatusBadRequest, "joiner_pubkey, hostname, and bundle are required")
		return
	}

	bundle, err := spanner.ParseBundle(req.Bundle)
	if err != nil {
		writeError(w, http.StatusBadRequest, "parsing invite bundle: "+err.Error())
		return
	}
	if err := spanner.VerifyBundle(bundle); err != nil {
		recordAudit(r.Context(), h.auditWriter, "spanner.join_rejected", req.JoinerPubkey, r.RemoteAddr,
			"invite bundle rejected: "+err.Error(), "")
		writeError(w, http.StatusForbidden, "invite bundle rejected: "+err.Error())
		return
	}

	tokenHash := spanner.TokenHash(bundle.Token)

	// Replay check: a bundle's token may be consumed exactly once.
	consumed, err := h.queries.IsTokenConsumed(r.Context(), tokenHash)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "checking token replay: "+err.Error())
		return
	}
	if consumed != 0 {
		recordAudit(r.Context(), h.auditWriter, "spanner.join_rejected", req.JoinerPubkey, r.RemoteAddr,
			"invite token already consumed", "")
		writeError(w, http.StatusConflict, "invite token already consumed — mint a fresh invite")
		return
	}

	ownPubkey := h.localPubkeyHex()
	patternClaims, err := json.Marshal(req.PatternClaims)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encoding pattern claims: "+err.Error())
		return
	}

	now := time.Now().UnixMilli()
	row := &spanner.AdmissionRow{
		Pubkey:            req.JoinerPubkey,
		Hostname:          req.Hostname,
		AdmittedAt:        now,
		AdmittedBy:        ownPubkey,
		ConsumedTokenHash: tokenHash,
		PatternClaims:     string(patternClaims),
	}
	sig, err := spanner.SignAdmissionRow(h.boltID.SigningKey, row)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "signing joiner admission row: "+err.Error())
		return
	}

	// Atomic admission: ConsumeToken then InsertBolt in one sql.Tx,
	// the token row INSERTed first because it is the FK target of
	// spanner_bolts.consumed_token_hash.
	if err := h.admitTx(r.Context(), tokenHash, ownPubkey, req.JoinerPubkey, bundle.ExpiresAt, now, row, sig); err != nil {
		writeError(w, http.StatusInternalServerError, "admitting bolt: "+err.Error())
		return
	}

	recordAudit(r.Context(), h.auditWriter, "spanner.join_accepted", req.JoinerPubkey, r.RemoteAddr,
		fmt.Sprintf("bolt %s (%s) admitted to spanner %q", req.JoinerPubkey, req.Hostname, h.spannerID), "")
	recordAudit(r.Context(), h.auditWriter, "spanner.bolt_admitted", ownPubkey, r.RemoteAddr,
		fmt.Sprintf("admitted bolt %s via invite token %s", req.JoinerPubkey, tokenHash[:16]), "")

	slog.Info("spanner bolt admitted", "joiner", req.JoinerPubkey, "hostname", req.Hostname, "spanner_id", h.spannerID)

	roster, err := h.queries.ListBolts(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "listing roster: "+err.Error())
		return
	}

	tokens, err := h.collectConsumedTokens(r.Context(), roster)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "collecting consumed tokens: "+err.Error())
		return
	}

	writeJSON(w, r, http.StatusOK, admitResponse{
		AdmittedRow:    boltToRosterRow(admissionRowToBolt(row, sig)),
		Roster:         boltsToRosterRows(roster),
		ConsumedTokens: tokens,
	})
}

// collectConsumedTokens fetches the spanner_consumed_tokens row for
// every roster bolt that carries a consumed_token_hash, so the
// joining/merging side can satisfy the FK when it persists the roster.
func (h *SpannerAdmissionHandler) collectConsumedTokens(ctx context.Context, roster []db.SpannerBolt) ([]spanner.ConsumedToken, error) {
	var out []spanner.ConsumedToken
	for _, b := range roster {
		if !b.ConsumedTokenHash.Valid || b.ConsumedTokenHash.String == "" {
			continue
		}
		tok, err := h.queries.GetConsumedToken(ctx, b.ConsumedTokenHash.String)
		if err != nil {
			return nil, fmt.Errorf("token %s: %w", b.ConsumedTokenHash.String, err)
		}
		out = append(out, spanner.ConsumedToken{
			TokenHash:      tok.TokenHash,
			ConsumedAt:     tok.ConsumedAt,
			ConsumedBy:     tok.ConsumedBy,
			AdmittedPubkey: tok.AdmittedPubkey,
			ExpiresAt:      tok.ExpiresAt,
		})
	}
	return out, nil
}

// admitTx wraps ConsumeToken + InsertBolt in a single transaction.
// The token INSERT is ordered first because spanner_bolts
// .consumed_token_hash is a FK onto spanner_consumed_tokens.token_hash.
func (h *SpannerAdmissionHandler) admitTx(ctx context.Context, tokenHash, consumedBy, joinerPubkey string, bundleExpiresAt, now int64, row *spanner.AdmissionRow, sig []byte) error {
	tx, err := h.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after Commit is a no-op

	qtx := h.queries.WithTx(tx)

	// Token first — it is the FK target.
	if err := qtx.ConsumeToken(ctx, db.ConsumeTokenParams{
		TokenHash:      tokenHash,
		ConsumedAt:     now,
		ConsumedBy:     consumedBy,
		AdmittedPubkey: joinerPubkey,
		ExpiresAt:      bundleExpiresAt,
	}); err != nil {
		return fmt.Errorf("consume token: %w", err)
	}

	if err := qtx.InsertBolt(ctx, admissionRowToInsertParams(row, sig)); err != nil {
		return fmt.Errorf("insert bolt: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// joinRequest is the body of POST /api/v1/spanner/bolt/join.
type joinRequest struct {
	FromAddr string `json:"from_addr"` // REST base URL of an inviting vigosrv
	Bundle   string `json:"bundle"`    // opaque invite-bundle string
}

// handleJoin runs on the JOINING server: posts this server's
// identity + the bundle to the inviter's /admit endpoint, then
// writes the returned roster into the local spanner_bolts table.
func (h *SpannerAdmissionHandler) handleJoin(w http.ResponseWriter, r *http.Request) {
	if h.boltID == nil {
		writeError(w, http.StatusServiceUnavailable, "spanner ceremony requires a bolt identity (spanner mode + local secrets backend)")
		return
	}

	var req joinRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.FromAddr == "" || req.Bundle == "" {
		writeError(w, http.StatusBadRequest, "from_addr and bundle are required")
		return
	}

	// Parse + verify the invite bundle up front: its FounderPubkey is
	// the trust anchor for verifying every roster row the inviter
	// returns, below.
	bundle, err := spanner.ParseBundle(req.Bundle)
	if err != nil {
		writeError(w, http.StatusBadRequest, "parsing invite bundle: "+err.Error())
		return
	}
	if err := spanner.VerifyBundle(bundle); err != nil {
		writeError(w, http.StatusForbidden, "invite bundle rejected: "+err.Error())
		return
	}

	ownPubkey := h.localPubkeyHex()

	recordAudit(r.Context(), h.auditWriter, "spanner.join_offered", ownPubkey, r.RemoteAddr,
		fmt.Sprintf("joining spanner via %s", req.FromAddr), "")

	admitBody, err := json.Marshal(admitRequest{
		JoinerPubkey:  ownPubkey,
		Hostname:      h.hostname,
		PatternClaims: h.patterns,
		Bundle:        req.Bundle,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encoding admit request: "+err.Error())
		return
	}

	httpReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
		req.FromAddr+"/api/v1/spanner/bolt/admit", bytes.NewReader(admitBody))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "building admit request: "+err.Error())
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := h.httpClient.Do(httpReq)
	if err != nil {
		writeError(w, http.StatusBadGateway, "calling inviter /admit: "+err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// best-effort: body only enriches the error we already return.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		writeError(w, http.StatusBadGateway, fmt.Sprintf("inviter /admit returned %d: %s", resp.StatusCode, string(body)))
		return
	}

	var admitResp admitResponse
	if err := json.NewDecoder(resp.Body).Decode(&admitResp); err != nil {
		writeError(w, http.StatusBadGateway, "decoding inviter /admit response: "+err.Error())
		return
	}

	// Verify every roster row end-to-end before persisting — never
	// trust the wire. The invite bundle's FounderPubkey is the trust
	// anchor: in Slice 1 every admission is issued by the founder, so
	// every row must name the founder as admitted_by and carry the
	// founder's signature. Slice 2's gossip merge generalizes this to
	// multi-admitter chains rooted at the founder.
	founderHex := hex.EncodeToString(bundle.FounderPubkey)
	for _, rr := range admitResp.Roster {
		if rr.AdmittedBy != founderHex {
			recordAudit(r.Context(), h.auditWriter, "spanner.join_rejected", ownPubkey, r.RemoteAddr,
				fmt.Sprintf("roster row %s admitted_by %s is not the founder", rr.Pubkey, rr.AdmittedBy), "")
			writeError(w, http.StatusBadGateway, "inviter roster rejected: row "+rr.Pubkey+" not admitted by the founder")
			return
		}
		arow := rr.AdmissionRowOf()
		if err := spanner.VerifyAdmissionRow(ed25519.PublicKey(bundle.FounderPubkey), &arow, rr.Signature); err != nil {
			recordAudit(r.Context(), h.auditWriter, "spanner.join_rejected", ownPubkey, r.RemoteAddr,
				fmt.Sprintf("roster row %s failed signature verification: %v", rr.Pubkey, err), "")
			writeError(w, http.StatusBadGateway, "inviter roster rejected: row "+rr.Pubkey+" signature invalid")
			return
		}
	}

	// Persist the returned roster locally. Each bolt row is
	// peer-signed (the inviter signed every row it returned); we
	// store them verbatim. Consumed-token rows go in first — they
	// are the FK target of spanner_bolts.consumed_token_hash — and
	// the whole write is one transaction so a partial roster never
	// lands.
	written, err := h.persistRoster(r.Context(), admitResp.ConsumedTokens, admitResp.Roster)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "persisting roster: "+err.Error())
		return
	}

	recordAudit(r.Context(), h.auditWriter, "spanner.join_accepted", ownPubkey, r.RemoteAddr,
		fmt.Sprintf("joined spanner via %s — roster has %d bolts", req.FromAddr, written), "")

	slog.Info("spanner joined", "from", req.FromAddr, "roster_size", written, "own_pubkey", ownPubkey)

	writeJSON(w, r, http.StatusOK, map[string]any{
		"joined":      true,
		"from_addr":   req.FromAddr,
		"roster_size": written,
		"roster":      admitResp.Roster,
	})
}

// persistRoster writes a peer roster into the local DB in one
// transaction: consumed-token rows first (FK targets), then bolt
// rows. Returns the number of bolt rows written.
func (h *SpannerAdmissionHandler) persistRoster(ctx context.Context, tokens []spanner.ConsumedToken, roster []spanner.RosterRow) (int, error) {
	tx, err := h.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after Commit is a no-op

	qtx := h.queries.WithTx(tx)

	// Token G-Set rows first — FK targets of spanner_bolts.
	for _, tok := range tokens {
		if err := qtx.ConsumeToken(ctx, db.ConsumeTokenParams{
			TokenHash:      tok.TokenHash,
			ConsumedAt:     tok.ConsumedAt,
			ConsumedBy:     tok.ConsumedBy,
			AdmittedPubkey: tok.AdmittedPubkey,
			ExpiresAt:      tok.ExpiresAt,
		}); err != nil {
			return 0, fmt.Errorf("consume token %s: %w", tok.TokenHash, err)
		}
	}

	written := 0
	for _, rr := range roster {
		params, err := rosterRowToInsertParams(rr)
		if err != nil {
			return 0, fmt.Errorf("decoding roster row %s: %w", rr.Pubkey, err)
		}
		if err := qtx.InsertBolt(ctx, params); err != nil {
			return 0, fmt.Errorf("inserting bolt %s: %w", rr.Pubkey, err)
		}
		written++
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return written, nil
}

// handleRoster returns the full spanner_bolts roster as JSON.
func (h *SpannerAdmissionHandler) handleRoster(w http.ResponseWriter, r *http.Request) {
	roster, err := h.queries.ListBolts(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "listing roster: "+err.Error())
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"roster": boltsToRosterRows(roster)})
}

// authenticatePull gates the session-less Tier-2 gossip pull endpoints
// (/snapshot, /fleet-snapshot). They serve the full replicated user
// directory + roster + envoy inventory and ride a plain-TLS, no-client-cert
// listener, so without this any host reaching the REST port could read it
// all. It verifies the Ed25519 pull-proof header and confirms the signer is
// a non-revoked roster member — the same membership predicate the Tier-1
// multicast listener enforces. On failure it writes the HTTP error and
// returns false; the caller returns immediately. Peer bootstrap is
// unaffected: a joiner seeds its roster via /peer-admit (separately authed),
// so it is already a roster member by the time it pulls a snapshot.
func (h *SpannerAdmissionHandler) authenticatePull(w http.ResponseWriter, r *http.Request) bool {
	pubHex, err := spanner.VerifyPullRequest(r, spanner.PullAuthWindow)
	if err != nil {
		slog.Debug("spanner pull: rejecting unsigned/invalid request", "path", r.URL.Path, "remote", r.RemoteAddr, "error", err)
		writeError(w, http.StatusForbidden, "spanner pull requires a valid roster-member signature")
		return false
	}
	roster, err := h.queries.ListBolts(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "listing roster: "+err.Error())
		return false
	}
	for i := range roster {
		if roster[i].Pubkey == pubHex {
			if roster[i].RevokedAt.Valid {
				slog.Debug("spanner pull: rejecting revoked roster member", "sender", pubHex, "remote", r.RemoteAddr)
				writeError(w, http.StatusForbidden, "spanner pull: signer is revoked")
				return false
			}
			return true
		}
	}
	slog.Debug("spanner pull: rejecting non-member signer", "sender", pubHex, "remote", r.RemoteAddr)
	writeError(w, http.StatusForbidden, "spanner pull: signer is not a roster member")
	return false
}

// handleSnapshot returns this bolt's full CRDT state — the
// spanner_bolts roster plus the spanner_consumed_tokens G-Set rows
// the roster's consumed_token_hash FKs point at. This is the
// Slice 2-i-a roster-replication pull endpoint: a peer's gossiper
// GETs it on a timer and CRDT-merges the result via
// spanner.MergeSnapshot. The stored per-row signatures are served
// verbatim — no re-signing, no version field.
func (h *SpannerAdmissionHandler) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	if !h.authenticatePull(w, r) {
		return
	}
	roster, err := h.queries.ListBolts(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "listing roster: "+err.Error())
		return
	}
	tokens, err := h.collectConsumedTokens(r.Context(), roster)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "collecting consumed tokens: "+err.Error())
		return
	}
	// ADR-030 Slice 2-iii-c: webusers CRDT rides the admissions
	// snapshot. ListUsersForReplication filters out legacy unsigned
	// rows (signature IS NOT NULL) — those propagate after Slice
	// 2-iii-f's first-startup re-signing pass, not before.
	userRows, err := h.queries.ListUsersForReplication(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "listing users for replication: "+err.Error())
		return
	}
	users := make([]spanner.UserSnapshotRow, 0, len(userRows))
	for _, u := range userRows {
		users = append(users, spanner.UserSnapshotRowFromDB(u))
	}
	writeJSON(w, r, http.StatusOK, spanner.Snapshot{
		Roster:         boltsToRosterRows(roster),
		ConsumedTokens: tokens,
		Users:          users,
	})
}

// Config-driven hands-off spanner peering — founder side (ADR-026). A
// joining bolt POSTs its self-signed identity to
// POST /api/v1/spanner/bolt/peer-admit; only the FOUNDER honors it,
// minting a founder-signed admission row so the founder-anchored CRDT
// converges. See server/spanner/peering.go for the joiner side and the
// security rationale (founder-routed admission + off-wire CA bound a
// rogue that slips the IP-only gate). The source-address allowlist
// helpers live in spanner_peering.go.

// handlePeerAdmit is the founder-side endpoint for config-driven
// peering. Three gates, then founder-only admission:
//
//  1. spanner_id must match this bolt's federation.
//  2. The joiner's Ed25519 self-signature must verify against the
//     pubkey it presents (proves key custody).
//  3. The HTTP source address must resolve to one of this bolt's
//     configured spanner.peers (the founder's peers list IS its
//     who-may-join allowlist). Deliberately weak — the real security
//     boundary is the off-wire fleet-CA mTLS, which a slipped rogue
//     still lacks.
//
// Only the founder (this bolt's own pubkey == the roster's founder
// self-admit row) mints rows; a non-founder returns 403 with the roster
// so a misdirected joiner can discover the real founder. Admission is
// token-less — the row's admitted_by is the founder and its
// consumed_token_hash is empty, which keeps FounderPubkey unambiguous
// (admitted_by != pubkey, so it is never mistaken for a second founder
// self-admit row). Idempotent: re-admitting an already-rostered bolt
// returns the current roster without a second insert.
func (h *SpannerAdmissionHandler) handlePeerAdmit(w http.ResponseWriter, r *http.Request) {
	if h.boltID == nil {
		writeError(w, http.StatusServiceUnavailable, "spanner ceremony requires a bolt identity (spanner mode + local secrets backend)")
		return
	}

	var req spanner.PeerAdmitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.JoinerPubkey == "" || req.Hostname == "" || req.SpannerID == "" || len(req.Signature) == 0 {
		writeError(w, http.StatusBadRequest, "joiner_pubkey, hostname, spanner_id, and signature are required")
		return
	}

	// Gate 1 — federation match.
	if req.SpannerID != h.spannerID {
		recordAudit(r.Context(), h.auditWriter, "spanner.peer_admit_rejected", req.JoinerPubkey, r.RemoteAddr,
			fmt.Sprintf("peer-admit spanner_id %q does not match this bolt's %q", req.SpannerID, h.spannerID), "")
		writeError(w, http.StatusForbidden, "spanner_id mismatch")
		return
	}

	// Gate 2 — self-signature proves the requester holds JoinerPubkey.
	if _, err := spanner.VerifyPeerIdentity(&req); err != nil {
		recordAudit(r.Context(), h.auditWriter, "spanner.peer_admit_rejected", req.JoinerPubkey, r.RemoteAddr,
			"peer-admit identity signature invalid: "+err.Error(), "")
		writeError(w, http.StatusForbidden, "identity signature invalid: "+err.Error())
		return
	}

	// Gate 3 — source address must be in this bolt's spanner.peers allowlist.
	if !sourceAddrAllowed(r.RemoteAddr, h.peers) {
		recordAudit(r.Context(), h.auditWriter, "spanner.peer_admit_rejected", req.JoinerPubkey, r.RemoteAddr,
			"peer-admit source address not in this bolt's spanner.peers allowlist", "")
		writeError(w, http.StatusForbidden, "source address not in this bolt's spanner.peers allowlist")
		return
	}

	roster, err := h.queries.ListBolts(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "listing roster: "+err.Error())
		return
	}

	// Only the founder is the admission authority.
	founderPub, err := spanner.FounderPubkey(roster)
	if err != nil {
		writeError(w, http.StatusForbidden, "this bolt is not the spanner admission authority: "+err.Error())
		return
	}
	ownPubkey := h.localPubkeyHex()
	if hex.EncodeToString(founderPub) != ownPubkey {
		// Not the founder — hand back the roster so the joiner can find the
		// real founder (its spanner.peers should point at the founder).
		tokens, tErr := h.collectConsumedTokens(r.Context(), roster)
		if tErr != nil {
			writeError(w, http.StatusInternalServerError, "collecting consumed tokens: "+tErr.Error())
			return
		}
		writeJSON(w, r, http.StatusForbidden, spanner.PeerAdmitResponse{
			FounderPubkey:  hex.EncodeToString(founderPub),
			Roster:         boltsToRosterRows(roster),
			ConsumedTokens: tokens,
		})
		return
	}

	// Idempotent re-admit — already rostered → return the current roster.
	if existing, gErr := h.queries.GetBolt(r.Context(), req.JoinerPubkey); gErr == nil {
		tokens, tErr := h.collectConsumedTokens(r.Context(), roster)
		if tErr != nil {
			writeError(w, http.StatusInternalServerError, "collecting consumed tokens: "+tErr.Error())
			return
		}
		writeJSON(w, r, http.StatusOK, spanner.PeerAdmitResponse{
			FounderPubkey:  ownPubkey,
			AdmittedRow:    boltToRosterRow(existing),
			Roster:         boltsToRosterRows(roster),
			ConsumedTokens: tokens,
		})
		return
	} else if !errors.Is(gErr, sql.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "checking existing roster row: "+gErr.Error())
		return
	}

	// Mint a founder-signed, token-less admission row for the joiner.
	patternClaims, err := json.Marshal(req.PatternClaims)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encoding pattern claims: "+err.Error())
		return
	}
	now := time.Now().UnixMilli()
	row := &spanner.AdmissionRow{
		Pubkey:            req.JoinerPubkey,
		Hostname:          req.Hostname,
		AdmittedAt:        now,
		AdmittedBy:        ownPubkey, // founder-routed; admitted_by != pubkey keeps FounderPubkey unambiguous
		ConsumedTokenHash: "",        // token-less peer-admit
		PatternClaims:     string(patternClaims),
	}
	sig, err := spanner.SignAdmissionRow(h.boltID.SigningKey, row)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "signing peer admission row: "+err.Error())
		return
	}
	if err := h.queries.InsertBolt(r.Context(), admissionRowToInsertParams(row, sig)); err != nil {
		writeError(w, http.StatusInternalServerError, "inserting peer admission row: "+err.Error())
		return
	}

	recordAudit(r.Context(), h.auditWriter, "spanner.peer_admitted", ownPubkey, r.RemoteAddr,
		fmt.Sprintf("bolt %s (%s) auto-admitted to spanner %q via config-driven peering", req.JoinerPubkey, req.Hostname, h.spannerID), "")
	slog.Info("spanner bolt auto-admitted", "joiner", req.JoinerPubkey, "hostname", req.Hostname, "spanner_id", h.spannerID)

	// Re-list so the response carries the newly inserted row.
	roster, err = h.queries.ListBolts(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "listing roster: "+err.Error())
		return
	}
	tokens, err := h.collectConsumedTokens(r.Context(), roster)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "collecting consumed tokens: "+err.Error())
		return
	}
	writeJSON(w, r, http.StatusOK, spanner.PeerAdmitResponse{
		FounderPubkey:  ownPubkey,
		AdmittedRow:    boltToRosterRow(admissionRowToBolt(row, sig)),
		Roster:         boltsToRosterRows(roster),
		ConsumedTokens: tokens,
	})
}

// --- roster row conversion helpers ---
//
// The wire shape itself (spanner.RosterRow / ConsumedToken / Snapshot)
// is owned by server/spanner so the ceremony handler, the /snapshot
// endpoint, and the gossiper all share one definition. These helpers
// bridge that shape to the sqlc-generated db params.

// admissionRowToInsertParams builds InsertBoltParams for a freshly
// signed admission row (founder self-admit or joiner). revoked_* and
// last_snapshot_at are NULL — set later by the CRDT merge.
func admissionRowToInsertParams(row *spanner.AdmissionRow, sig []byte) db.InsertBoltParams {
	return db.InsertBoltParams{
		Pubkey:            row.Pubkey,
		Hostname:          row.Hostname,
		AdmittedAt:        row.AdmittedAt,
		AdmittedBy:        row.AdmittedBy,
		ConsumedTokenHash: nullStringIfSet(row.ConsumedTokenHash),
		RevokedAt:         sql.NullInt64{},
		RevokedBy:         sql.NullString{},
		LastSnapshotAt:    sql.NullInt64{},
		PatternClaims:     row.PatternClaims,
		Signature:         sig,
	}
}

// admissionRowToBolt builds a db.SpannerBolt from a freshly signed
// admission row, for projecting back into a rosterRow without a DB
// round-trip.
func admissionRowToBolt(row *spanner.AdmissionRow, sig []byte) db.SpannerBolt {
	return db.SpannerBolt{
		Pubkey:            row.Pubkey,
		Hostname:          row.Hostname,
		AdmittedAt:        row.AdmittedAt,
		AdmittedBy:        row.AdmittedBy,
		ConsumedTokenHash: nullStringIfSet(row.ConsumedTokenHash),
		PatternClaims:     row.PatternClaims,
		Signature:         sig,
	}
}

// boltToRosterRow projects a db.SpannerBolt into the wire shape.
func boltToRosterRow(b db.SpannerBolt) spanner.RosterRow {
	return spanner.RosterRowFromBolt(b)
}

// boltsToRosterRows projects a slice of db.SpannerBolt.
func boltsToRosterRows(bolts []db.SpannerBolt) []spanner.RosterRow {
	out := make([]spanner.RosterRow, 0, len(bolts))
	for _, b := range bolts {
		out = append(out, spanner.RosterRowFromBolt(b))
	}
	return out
}

// rosterRowToInsertParams rebuilds InsertBoltParams from a wire
// spanner.RosterRow received from a peer.
func rosterRowToInsertParams(rr spanner.RosterRow) (db.InsertBoltParams, error) {
	p := db.InsertBoltParams{
		Pubkey:            rr.Pubkey,
		Hostname:          rr.Hostname,
		AdmittedAt:        rr.AdmittedAt,
		AdmittedBy:        rr.AdmittedBy,
		ConsumedTokenHash: nullStringIfSet(rr.ConsumedTokenHash),
		PatternClaims:     rr.PatternClaims,
		Signature:         rr.Signature,
	}
	if rr.RevokedAt != 0 {
		p.RevokedAt = sql.NullInt64{Int64: rr.RevokedAt, Valid: true}
	}
	if rr.RevokedBy != "" {
		p.RevokedBy = sql.NullString{String: rr.RevokedBy, Valid: true}
	}
	return p, nil
}

// nullStringIfSet returns a valid sql.NullString iff s is non-empty.
func nullStringIfSet(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}
