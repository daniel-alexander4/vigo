// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"git.alexander4.io/vigo/server/spanner"
)

// Config-driven hands-off spanner peering — founder side (ADR-026). A
// joining bolt POSTs its self-signed identity to
// POST /api/v1/spanner/bolt/peer-admit; only the FOUNDER honors it,
// minting a founder-signed admission row so the founder-anchored CRDT
// converges. See server/spanner/peering.go for the joiner side and the
// security rationale (founder-routed admission + off-wire CA bound a
// rogue that slips the IP-only gate).

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

// sourceAddrAllowed reports whether remoteAddr's IP resolves to one of
// the configured peer addresses. The match rule is pinned to
// resolve-and-compare: each peer entry's host is resolved to its IP set
// (literal IPs pass through unchanged; hostnames go through the
// resolver) and the request's source IP must equal one of them. This
// handles both literal-IP and DNS-name peer entries; it is the
// deliberately-weak admission gate whose acceptability rests on the
// off-wire fleet CA (see server/spanner/peering.go).
func sourceAddrAllowed(remoteAddr string, peers []string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	srcIP := net.ParseIP(host)
	if srcIP == nil {
		return false
	}
	for _, peer := range peers {
		for _, ip := range resolvePeerIPs(peer) {
			if ip.Equal(srcIP) {
				return true
			}
		}
	}
	return false
}

// resolvePeerIPs returns the IP set a configured peer entry resolves to.
// A literal IP returns itself; a hostname goes through net.LookupHost.
// Resolution failures yield no IPs (the peer simply does not match).
func resolvePeerIPs(peer string) []net.IP {
	h := peerHostOnly(peer)
	if ip := net.ParseIP(h); ip != nil {
		return []net.IP{ip}
	}
	addrs, err := net.LookupHost(h)
	if err != nil {
		return nil
	}
	out := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		if ip := net.ParseIP(a); ip != nil {
			out = append(out, ip)
		}
	}
	return out
}

// peerHostOnly strips any scheme and port from a configured peer entry,
// returning just the host (IP or DNS name).
func peerHostOnly(peer string) string {
	p := strings.TrimSpace(peer)
	p = strings.TrimPrefix(p, "https://")
	p = strings.TrimPrefix(p, "http://")
	p = strings.TrimRight(p, "/")
	if host, _, err := net.SplitHostPort(p); err == nil {
		return host
	}
	return p
}
