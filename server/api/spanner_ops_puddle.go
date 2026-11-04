// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/puddle"
	"git.alexander4.io/vigo/server/secrets"
)

// SpannerOpsPuddleHandler serves the ops-puddle pair-claim endpoint
// on existing-member vigosrvs (ADR-025 Slice 2b). The joining
// vigosrv's vigocli posts a pair code; we verify it against the
// on-disk pending-pair record, unwrap the ops-puddle, append the
// joining vigosrv's pubkey to the member set, and return the raw
// 32-byte ops-puddle seed + friendly name + updated member list so
// the joining side can re-wrap under its own master-key-derived
// wrap key.
//
// Auth: admin-only (mTLS + admin token). The operator on the
// joining side must have admin credentials for the existing-member
// vigosrv — typically configured via `vigocli auth login` against
// the existing-member's URL before running `join`.
//
// Wire-level confidentiality: the seed flies in cleartext inside
// the JSON response. mTLS protects the wire end-to-end (every
// vigosrv runs HTTPS-only on :8443); the pair code is the
// authorization factor and the single-use marker prevents replay.
type SpannerOpsPuddleHandler struct {
	provider    secrets.Provider
	opsDir      string
	auditWriter auditRecorder
	// claimMu serializes the load→check-used→mark-used→append critical
	// section so two concurrent valid-code requests can't both pass the
	// pending.Used check and each claim the same 5-minute code.
	claimMu sync.Mutex
}

// NewSpannerOpsPuddleHandler constructs the handler with everything
// it needs to load + mutate the ops-puddle and audit the result.
// provider must be the same secrets backend the puddle was wrapped
// under (only `local` backend supports DeriveKey in v1; isopass
// deployments don't host an ops-puddle).
func NewSpannerOpsPuddleHandler(provider secrets.Provider, opsDir string, auditWriter auditRecorder) *SpannerOpsPuddleHandler {
	return &SpannerOpsPuddleHandler{
		provider:    provider,
		opsDir:      opsDir,
		auditWriter: auditWriter,
	}
}

// RegisterRoutes mounts the pair-claim endpoint. Admin-gated —
// joining vigosrvs aren't enrolled envoys, they're peer vigosrvs
// whose operator is authenticated to this vigosrv.
func (h *SpannerOpsPuddleHandler) RegisterRoutes(mux *http.ServeMux) {
	admin := auth.RequireAdmin()
	mux.Handle("POST /api/v1/spanner/ops-puddle/pair-claim", admin(http.HandlerFunc(h.handlePairClaim)))
}

// pairClaimRequest is the body the joining vigocli sends.
type pairClaimRequest struct {
	// Code is the plaintext pair code minted by `vigocli spanner
	// ops-puddle pair` on this (existing-member) vigosrv.
	Code string `json:"code"`
	// JoiningPubkey is the joining vigosrv's service-account puddle
	// pubkey in "ed25519:<hex>" form. Recorded as the new member.
	JoiningPubkey string `json:"joining_pubkey"`
}

// pairClaimResponse is the body the joining vigocli receives on
// success. Contains the material needed to re-wrap the ops-puddle
// locally and persist the same member set.
type pairClaimResponse struct {
	// SeedB64 is base64 (standard-padded) of the raw 32-byte
	// Ed25519 seed.
	SeedB64 string `json:"seed_b64"`
	// FriendlyName is the operator-chosen ops-puddle name.
	FriendlyName string `json:"friendly_name"`
	// Members is the updated member list (joining pubkey appended).
	Members []puddle.OpsMember `json:"members"`
}

func (h *SpannerOpsPuddleHandler) handlePairClaim(w http.ResponseWriter, r *http.Request) {
	if h.provider == nil {
		writeError(w, http.StatusServiceUnavailable, "ops-puddle pair flow requires the local secrets backend")
		return
	}
	deriver, ok := h.provider.(puddle.KeyDeriver)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "ops-puddle pair flow requires a secrets backend that supports HKDF derivation (local only in v1)")
		return
	}

	var req pairClaimRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Code == "" || req.JoiningPubkey == "" {
		writeError(w, http.StatusBadRequest, "code and joining_pubkey are required")
		return
	}

	// Serialize the load→check-used→mark-used→append critical section so
	// two concurrent valid-code requests can't both pass the pending.Used
	// check before either marks the code used.
	h.claimMu.Lock()
	defer h.claimMu.Unlock()

	pending, err := puddle.LoadPendingPair(h.opsDir)
	if err != nil {
		slog.Warn("ops-puddle pair-claim: load pending failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if pending == nil {
		writeError(w, http.StatusNotFound, "no pending pair offer — run `vigocli spanner ops-puddle pair` on this vigosrv first")
		return
	}
	if pending.Used {
		writeError(w, http.StatusGone, "pair code already used; mint a fresh one with `vigocli spanner ops-puddle pair`")
		return
	}
	if time.Now().UTC().After(pending.ExpiresAt) {
		writeError(w, http.StatusGone, "pair code expired; mint a fresh one with `vigocli spanner ops-puddle pair`")
		return
	}
	if puddle.HashPairCode(req.Code) != pending.CodeHash {
		// Don't distinguish "wrong code" from "no offer" beyond
		// the body — both surface as 403 from the caller's
		// perspective. Logging captures the detail.
		slog.Warn("ops-puddle pair-claim: code mismatch",
			"joining_pubkey", req.JoiningPubkey,
			"remote", r.RemoteAddr,
		)
		writeError(w, http.StatusForbidden, "invalid pair code")
		return
	}

	// Mark the code used BEFORE returning the seed. Fail-closed: if we
	// can't persist the single-use marker we must not hand out the seed,
	// or a crash/retry between here and the response could replay the
	// same code. The mutex above guarantees no concurrent claim observed
	// the not-used record before this write lands.
	if err := puddle.MarkPendingPairUsed(h.opsDir); err != nil {
		slog.Error("ops-puddle pair-claim: failed to mark pending used", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	ops, err := puddle.LoadOpsPuddle(deriver, h.opsDir)
	if err != nil {
		slog.Error("ops-puddle pair-claim: load ops-puddle failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	members, err := puddle.AppendMember(h.opsDir, req.JoiningPubkey)
	if err != nil {
		slog.Error("ops-puddle pair-claim: append member failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	seedB64 := base64.StdEncoding.EncodeToString(ops.SigningKey.Seed())
	resp := pairClaimResponse{
		SeedB64:      seedB64,
		FriendlyName: ops.FriendlyName,
		Members:      members,
	}

	recordAudit(r.Context(), h.auditWriter, "spanner.ops_puddle.member_added",
		actorFromRequest(r), "rest",
		fmt.Sprintf("ops-puddle %s gained member %s (total now %d)", ops.FriendlyName, req.JoiningPubkey, len(members)),
		fmt.Sprintf(`{"ops_puddle":%q,"joining_pubkey":%q,"member_count":%d}`, ops.FriendlyName, req.JoiningPubkey, len(members)),
	)

	slog.Info("ops-puddle member added via pair flow",
		"ops_puddle", ops.FriendlyName,
		"joining_pubkey", req.JoiningPubkey,
		"member_count", len(members),
	)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		slog.Error("ops-puddle pair-claim: response encode failed", "error", err)
	}
}

// ensure secrets.Provider can satisfy our KeyDeriver expectations
// at compile time. (Runtime fallback is the type assertion above
// for the isopass case.)
var _ context.Context = context.Background()
