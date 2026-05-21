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

// SpannerAdmissionHandler serves the membership-ceremony endpoints.
type SpannerAdmissionHandler struct {
	sqlDB       *sql.DB // for the atomic ConsumeToken+InsertBolt transaction
	queries     *db.Queries
	boltID      *bolt.Identity // local bolt signing identity; nil → ceremony unavailable
	spannerID   string
	hostname    string   // this vigosrv's own envoy hostname
	patterns    []string // this bolt's configured pattern claims
	auditWriter auditRecorder
	httpClient  *http.Client // for the cross-vigosrv /admit call from /join
}

// NewSpannerAdmissionHandler constructs the ceremony handler. boltID
// may be nil (standalone mode or a non-local secrets backend); in
// that case every mutating endpoint returns 503 — the ceremony
// needs a bolt identity to sign with.
func NewSpannerAdmissionHandler(sqlDB *sql.DB, queries *db.Queries, boltID *bolt.Identity, spannerID, hostname string, patterns []string, auditWriter auditRecorder, httpClient *http.Client) *SpannerAdmissionHandler {
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
		auditWriter: auditWriter,
		httpClient:  httpClient,
	}
}

// RegisterRoutes mounts the ceremony endpoints. init/invite/join are
// admin-gated (operator-driven). admit is bundle-authenticated, not
// Bearer-gated — a joining vigosrv is a peer, not an enrolled envoy.
// roster + snapshot are read-gated. snapshot is the Slice 2-i-a
// roster-replication pull endpoint — a peer's gossiper GETs it and
// CRDT-merges the result.
func (h *SpannerAdmissionHandler) RegisterRoutes(mux *http.ServeMux) {
	read := auth.RequirePermission(auth.PermFleetRead)
	admin := auth.RequireAdmin("")
	mux.Handle("POST /api/v1/spanner/init", admin(http.HandlerFunc(h.handleInit)))
	mux.Handle("POST /api/v1/spanner/bolt/invite", admin(http.HandlerFunc(h.handleInvite)))
	mux.Handle("POST /api/v1/spanner/bolt/admit", http.HandlerFunc(h.handleAdmit))
	mux.Handle("POST /api/v1/spanner/bolt/join", admin(http.HandlerFunc(h.handleJoin)))
	mux.Handle("GET /api/v1/spanner/roster", read(http.HandlerFunc(h.handleRoster)))
	mux.Handle("GET /api/v1/spanner/snapshot", read(http.HandlerFunc(h.handleSnapshot)))
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

	recordAudit(r.Context(), h.auditWriter, "spanner_id_initialized", ownPubkey, r.RemoteAddr,
		fmt.Sprintf("spanner %q founded on %s", h.spannerID, h.hostname), "")

	slog.Info("spanner founded", "spanner_id", h.spannerID, "founder_pubkey", ownPubkey, "hostname", h.hostname)

	writeJSON(w, http.StatusOK, map[string]any{
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

	writeJSON(w, http.StatusOK, map[string]any{
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
		recordAudit(r.Context(), h.auditWriter, "spanner_join_rejected", req.JoinerPubkey, r.RemoteAddr,
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
		recordAudit(r.Context(), h.auditWriter, "spanner_join_rejected", req.JoinerPubkey, r.RemoteAddr,
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

	recordAudit(r.Context(), h.auditWriter, "spanner_join_accepted", req.JoinerPubkey, r.RemoteAddr,
		fmt.Sprintf("bolt %s (%s) admitted to spanner %q", req.JoinerPubkey, req.Hostname, h.spannerID), "")
	recordAudit(r.Context(), h.auditWriter, "spanner_bolt_admitted", ownPubkey, r.RemoteAddr,
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

	writeJSON(w, http.StatusOK, admitResponse{
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

	recordAudit(r.Context(), h.auditWriter, "spanner_join_offered", ownPubkey, r.RemoteAddr,
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
			recordAudit(r.Context(), h.auditWriter, "spanner_join_rejected", ownPubkey, r.RemoteAddr,
				fmt.Sprintf("roster row %s admitted_by %s is not the founder", rr.Pubkey, rr.AdmittedBy), "")
			writeError(w, http.StatusBadGateway, "inviter roster rejected: row "+rr.Pubkey+" not admitted by the founder")
			return
		}
		arow := rr.AdmissionRowOf()
		if err := spanner.VerifyAdmissionRow(ed25519.PublicKey(bundle.FounderPubkey), &arow, rr.Signature); err != nil {
			recordAudit(r.Context(), h.auditWriter, "spanner_join_rejected", ownPubkey, r.RemoteAddr,
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

	recordAudit(r.Context(), h.auditWriter, "spanner_join_accepted", ownPubkey, r.RemoteAddr,
		fmt.Sprintf("joined spanner via %s — roster has %d bolts", req.FromAddr, written), "")

	slog.Info("spanner joined", "from", req.FromAddr, "roster_size", written, "own_pubkey", ownPubkey)

	writeJSON(w, http.StatusOK, map[string]any{
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
	writeJSON(w, http.StatusOK, map[string]any{"roster": boltsToRosterRows(roster)})
}

// handleSnapshot returns this bolt's full CRDT state — the
// spanner_bolts roster plus the spanner_consumed_tokens G-Set rows
// the roster's consumed_token_hash FKs point at. This is the
// Slice 2-i-a roster-replication pull endpoint: a peer's gossiper
// GETs it on a timer and CRDT-merges the result via
// spanner.MergeSnapshot. The stored per-row signatures are served
// verbatim — no re-signing, no version field.
func (h *SpannerAdmissionHandler) handleSnapshot(w http.ResponseWriter, r *http.Request) {
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
	writeJSON(w, http.StatusOK, spanner.Snapshot{
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
