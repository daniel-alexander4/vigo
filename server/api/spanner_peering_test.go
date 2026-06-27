// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.alexander4.io/vigo/server/bolt"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/spanner"
	"git.alexander4.io/vigo/server/testutil"
)

// signedPeerAdmit builds a self-signed peer-admit request for a joiner
// bolt identity — the wire body a joining bolt POSTs to a founder.
func signedPeerAdmit(t *testing.T, id *bolt.Identity, spannerID, hostname string, patterns []string) spanner.PeerAdmitRequest {
	t.Helper()
	req := spanner.PeerAdmitRequest{
		JoinerPubkey:  hex.EncodeToString(id.PublicKey),
		Hostname:      hostname,
		PatternClaims: patterns,
		SpannerID:     spannerID,
	}
	sig, err := spanner.SignPeerIdentity(id.SigningKey, &req)
	if err != nil {
		t.Fatalf("sign peer identity: %v", err)
	}
	req.Signature = sig
	return req
}

// callPeerAdmit invokes handlePeerAdmit directly with a controlled
// source address, returning the status code and decoded response.
func callPeerAdmit(t *testing.T, h *SpannerAdmissionHandler, remoteAddr string, body spanner.PeerAdmitRequest) (int, spanner.PeerAdmitResponse) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/spanner/bolt/peer-admit", bytes.NewReader(raw))
	req.RemoteAddr = remoteAddr
	rec := httptest.NewRecorder()
	h.handlePeerAdmit(rec, req)
	var resp spanner.PeerAdmitResponse
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	}
	return rec.Code, resp
}

func TestPeerAdmitFounderAdmitsAndIsIdempotent(t *testing.T) {
	founderH, founderSrv, founderQ := newCeremonyServer(t, "test-spanner", "founder.example.com", []string{"*.us-west.*"}, &http.Client{}, false)
	// The founder's spanner.peers IS its who-may-join allowlist; the
	// httptest client connects over loopback.
	founderH.peers = []string{"127.0.0.1", "::1"}

	if code := postJSON(t, founderSrv.URL+"/api/v1/spanner/init", nil, nil); code != http.StatusOK {
		t.Fatalf("init returned %d", code)
	}

	joiner := testBoltIdentity(t)
	body := signedPeerAdmit(t, joiner, "test-spanner", "joiner.example.com", []string{"*.us-east.*"})

	var resp spanner.PeerAdmitResponse
	if code := postJSON(t, founderSrv.URL+"/api/v1/spanner/bolt/peer-admit", body, &resp); code != http.StatusOK {
		t.Fatalf("peer-admit returned %d", code)
	}
	if resp.FounderPubkey != hex.EncodeToString(founderH.boltID.PublicKey) {
		t.Errorf("founder_pubkey = %q, want the founder's pubkey", resp.FounderPubkey)
	}
	if len(resp.Roster) != 2 {
		t.Fatalf("roster size = %d, want 2 (founder + joiner)", len(resp.Roster))
	}

	// The joiner's row must be founder-routed: admitted_by == founder,
	// token-less, and its signature must verify against the founder key.
	founderHex := hex.EncodeToString(founderH.boltID.PublicKey)
	var joinerRow *spanner.RosterRow
	for i := range resp.Roster {
		if resp.Roster[i].Pubkey == body.JoinerPubkey {
			joinerRow = &resp.Roster[i]
		}
	}
	if joinerRow == nil {
		t.Fatal("joiner row absent from returned roster")
	}
	if joinerRow.AdmittedBy != founderHex {
		t.Errorf("joiner admitted_by = %q, want the founder %q", joinerRow.AdmittedBy, founderHex)
	}
	if joinerRow.ConsumedTokenHash != "" {
		t.Errorf("peer-admitted row carries a consumed_token_hash %q, want empty", joinerRow.ConsumedTokenHash)
	}
	arow := joinerRow.AdmissionRowOf()
	if err := spanner.VerifyAdmissionRow(founderH.boltID.PublicKey, &arow, joinerRow.Signature); err != nil {
		t.Errorf("joiner row signature failed to verify against the founder: %v", err)
	}

	// FounderPubkey must still resolve uniquely — the token-less joiner
	// row (admitted_by == founder, pubkey != founder) must NOT be
	// mistaken for a second founder self-admit row.
	roster, err := founderQ.ListBolts(context.Background())
	if err != nil {
		t.Fatalf("ListBolts: %v", err)
	}
	fp, err := spanner.FounderPubkey(roster)
	if err != nil {
		t.Fatalf("FounderPubkey after peer-admit: %v", err)
	}
	if hex.EncodeToString(fp) != founderHex {
		t.Errorf("FounderPubkey = %q, want %q", hex.EncodeToString(fp), founderHex)
	}

	// Idempotent re-admit: same joiner → 200, roster unchanged.
	if code := postJSON(t, founderSrv.URL+"/api/v1/spanner/bolt/peer-admit", body, &resp); code != http.StatusOK {
		t.Fatalf("re-admit returned %d, want 200", code)
	}
	if len(resp.Roster) != 2 {
		t.Errorf("re-admit roster size = %d, want 2 (no duplicate insert)", len(resp.Roster))
	}
}

func TestPeerAdmitGates(t *testing.T) {
	founderH, _, _ := newCeremonyServer(t, "test-spanner", "founder.example.com", []string{"*"}, &http.Client{}, false)
	founderH.peers = []string{"192.0.2.1"} // httptest.NewRequest's default RemoteAddr host

	// Found so a founder row exists.
	initRec := httptest.NewRecorder()
	founderH.handleInit(initRec, adminReq(httptest.NewRequest(http.MethodPost, "/api/v1/spanner/init", nil)))
	if initRec.Code != http.StatusOK {
		t.Fatalf("init returned %d", initRec.Code)
	}

	joiner := testBoltIdentity(t)
	good := signedPeerAdmit(t, joiner, "test-spanner", "joiner.example.com", []string{"*.us-east.*"})

	// Sanity: the well-formed request from an allowed source succeeds.
	if code, _ := callPeerAdmit(t, founderH, "192.0.2.1:5555", good); code != http.StatusOK {
		t.Fatalf("well-formed peer-admit returned %d, want 200", code)
	}

	t.Run("spanner_id mismatch", func(t *testing.T) {
		bad := signedPeerAdmit(t, testBoltIdentity(t), "other-spanner", "j2.example.com", nil)
		if code, _ := callPeerAdmit(t, founderH, "192.0.2.1:5555", bad); code != http.StatusForbidden {
			t.Errorf("spanner_id mismatch returned %d, want 403", code)
		}
	})

	t.Run("bad signature", func(t *testing.T) {
		bad := signedPeerAdmit(t, testBoltIdentity(t), "test-spanner", "j3.example.com", nil)
		bad.Hostname = "tampered.example.com" // invalidates the signature
		if code, _ := callPeerAdmit(t, founderH, "192.0.2.1:5555", bad); code != http.StatusForbidden {
			t.Errorf("tampered identity returned %d, want 403", code)
		}
	})

	t.Run("source address not allowed", func(t *testing.T) {
		fresh := signedPeerAdmit(t, testBoltIdentity(t), "test-spanner", "j4.example.com", nil)
		if code, _ := callPeerAdmit(t, founderH, "10.99.99.99:5555", fresh); code != http.StatusForbidden {
			t.Errorf("disallowed source returned %d, want 403", code)
		}
	})

	t.Run("not the founder", func(t *testing.T) {
		// A bolt with no founder row of its own cannot admit.
		nonFounder, _, _ := newCeremonyServer(t, "test-spanner", "lonely.example.com", []string{"*"}, &http.Client{}, false)
		nonFounder.peers = []string{"192.0.2.1"}
		fresh := signedPeerAdmit(t, testBoltIdentity(t), "test-spanner", "j5.example.com", nil)
		if code, _ := callPeerAdmit(t, nonFounder, "192.0.2.1:5555", fresh); code != http.StatusForbidden {
			t.Errorf("non-founder peer-admit returned %d, want 403", code)
		}
	})
}

// TestPeerAdmitAutoAdmitEndToEnd drives the joiner-side Gossiper.AutoAdmit
// against a real founder HTTP endpoint and asserts the joiner seeds an
// identical, signature-verified roster — then that the freshly-seeded
// joiner correctly refuses to act as the admission authority.
func TestPeerAdmitAutoAdmitEndToEnd(t *testing.T) {
	founderH, founderSrv, _ := newCeremonyServer(t, "test-spanner", "founder.example.com", []string{"*.us-west.*"}, &http.Client{}, false)
	founderH.peers = []string{"127.0.0.1", "::1"}
	if code := postJSON(t, founderSrv.URL+"/api/v1/spanner/init", nil, nil); code != http.StatusOK {
		t.Fatalf("init returned %d", code)
	}

	// Stand up a joiner DB + a Gossiper pointed at the founder URL.
	joinerDB := testutil.OpenMigratedTestDB(t)
	joinerQ := db.New(joinerDB)
	joinerID := testBoltIdentity(t)
	g := spanner.NewGossiper(
		joinerDB, joinerQ, hex.EncodeToString(joinerID.PublicKey), "8443",
		0, "test-spanner", joinerID.SigningKey, "224.0.0.45", 1534, 0, &http.Client{},
	)

	// founderSrv.URL is "http://127.0.0.1:<port>" — normalizePeerBase keeps
	// the explicit http scheme, so AutoAdmit reaches the non-TLS test server.
	g.AutoAdmit(context.Background(), []string{founderSrv.URL}, "joiner.example.com", []string{"*.us-east.*"})

	roster, err := joinerQ.ListBolts(context.Background())
	if err != nil {
		t.Fatalf("joiner ListBolts: %v", err)
	}
	if len(roster) != 2 {
		t.Fatalf("joiner roster size = %d, want 2 after auto-admit", len(roster))
	}
	// The seeded roster must resolve the founder, and every row must
	// verify against the founder key (MergeSnapshot enforced this; we
	// re-assert it landed).
	fp, err := spanner.FounderPubkey(roster)
	if err != nil {
		t.Fatalf("FounderPubkey on seeded joiner roster: %v", err)
	}
	if hex.EncodeToString(fp) != hex.EncodeToString(founderH.boltID.PublicKey) {
		t.Errorf("seeded founder pubkey mismatch")
	}

	// Re-running AutoAdmit is a no-op now that a founder row exists.
	g.AutoAdmit(context.Background(), []string{founderSrv.URL}, "joiner.example.com", []string{"*.us-east.*"})
	if again, _ := joinerQ.ListBolts(context.Background()); len(again) != 2 {
		t.Errorf("second auto-admit changed roster size to %d, want 2", len(again))
	}
}
