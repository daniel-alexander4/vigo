// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"testing"
	"time"

	"git.alexander4.io/vigo/server/bolt"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/puddle"
	"git.alexander4.io/vigo/server/spanner"
	"git.alexander4.io/vigo/server/testutil"
)

// testBoltIdentity builds an in-memory bolt identity for tests —
// the on-disk wrap dance is exercised in server/bolt's own tests.
func testBoltIdentity(t *testing.T) *bolt.Identity {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate bolt key: %v", err)
	}
	return &puddle.Identity{
		SigningKey:    priv,
		PublicKey:     pub,
		PublicKeyText: puddle.EncodePubkey(pub),
	}
}

// newCeremonyServer spins up one vigosrv-equivalent: a migrated DB,
// an admission handler with its own bolt identity, and an httptest
// server fronting an admin-stamped mux. Returns the handler (for
// direct calls) and the base URL (for cross-server calls).
// httpClient is the client the handler uses for its outbound /join →
// /admit call; pass nil to exercise the production InsecureSkipVerify
// default. useTLS fronts the server with a self-signed cert.
func newCeremonyServer(t *testing.T, spannerID, hostname string, patterns []string, httpClient *http.Client, useTLS bool) (*SpannerAdmissionHandler, *httptest.Server, *db.Queries) {
	t.Helper()
	sqlDB := testutil.OpenMigratedTestDB(t)
	queries := db.New(sqlDB)
	id := testBoltIdentity(t)

	h := NewSpannerAdmissionHandler(sqlDB, queries, id, spannerID, hostname, patterns, nil, httpClient)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// The httptest server stamps an admin user into every request so
	// the admin-gated endpoints (init/invite/join) pass; /admit is
	// bundle-authenticated and ignores the stamp.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, adminReq(r))
	})
	var srv *httptest.Server
	if useTLS {
		srv = httptest.NewTLSServer(handler)
	} else {
		srv = httptest.NewServer(handler)
	}
	t.Cleanup(srv.Close)
	return h, srv, queries
}

// postJSON posts body as JSON to url and decodes the response into
// out. Returns the status code.
func postJSON(t *testing.T, url string, body, out any) int {
	t.Helper()
	return postJSONClient(t, http.DefaultClient, url, body, out)
}

// postJSONClient is postJSON with an explicit client — needed to call
// an httptest TLS server through its srv.Client() (which trusts the
// test cert).
func postJSONClient(t *testing.T, client *http.Client, url string, body, out any) int {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		rdr = bytes.NewReader(raw)
	} else {
		rdr = bytes.NewReader(nil)
	}
	resp, err := client.Post(url, "application/json", rdr)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	if out != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("decode %s response: %v", url, err)
		}
	}
	return resp.StatusCode
}

func TestSpannerCeremonyFoundInviteJoin(t *testing.T) {
	founderH, founderSrv, founderQ := newCeremonyServer(t, "test-spanner", "founder.example.com", []string{"*.us-west.*"}, &http.Client{}, false)
	joinerH, joinerSrv, joinerQ := newCeremonyServer(t, "test-spanner", "joiner.example.com", []string{"*.us-east.*"}, &http.Client{}, false)

	// --- found ---
	var initResp struct {
		SpannerID     string `json:"spanner_id"`
		FounderPubkey string `json:"founder_pubkey"`
	}
	if code := postJSON(t, founderSrv.URL+"/api/v1/spanner/init", nil, &initResp); code != http.StatusOK {
		t.Fatalf("init returned %d", code)
	}
	if initResp.SpannerID != "test-spanner" {
		t.Errorf("init spanner_id = %q", initResp.SpannerID)
	}

	// Founding twice must conflict.
	if code := postJSON(t, founderSrv.URL+"/api/v1/spanner/init", nil, nil); code != http.StatusConflict {
		t.Errorf("second init returned %d, want 409", code)
	}

	// --- invite ---
	var invResp struct {
		Bundle string `json:"bundle"`
	}
	if code := postJSON(t, founderSrv.URL+"/api/v1/spanner/bolt/invite",
		map[string]any{"ttl_seconds": 3600}, &invResp); code != http.StatusOK {
		t.Fatalf("invite returned %d", code)
	}
	if invResp.Bundle == "" {
		t.Fatal("invite returned an empty bundle")
	}

	// --- join (joiner forwards the bundle to the founder's /admit) ---
	var joinResp struct {
		Joined     bool `json:"joined"`
		RosterSize int  `json:"roster_size"`
	}
	if code := postJSON(t, joinerSrv.URL+"/api/v1/spanner/bolt/join",
		map[string]string{"from_addr": founderSrv.URL, "bundle": invResp.Bundle}, &joinResp); code != http.StatusOK {
		t.Fatalf("join returned %d", code)
	}
	if !joinResp.Joined {
		t.Error("join response says not joined")
	}
	if joinResp.RosterSize != 2 {
		t.Errorf("join roster_size = %d, want 2", joinResp.RosterSize)
	}

	// --- both DBs must hold an identical 2-row roster ---
	founderRoster, err := founderQ.ListBolts(t.Context())
	if err != nil {
		t.Fatalf("founder ListBolts: %v", err)
	}
	joinerRoster, err := joinerQ.ListBolts(t.Context())
	if err != nil {
		t.Fatalf("joiner ListBolts: %v", err)
	}
	if len(founderRoster) != 2 || len(joinerRoster) != 2 {
		t.Fatalf("roster sizes: founder=%d joiner=%d, want 2/2", len(founderRoster), len(joinerRoster))
	}
	for i := range founderRoster {
		if founderRoster[i].Pubkey != joinerRoster[i].Pubkey {
			t.Errorf("roster row %d pubkey mismatch: %q != %q", i, founderRoster[i].Pubkey, joinerRoster[i].Pubkey)
		}
		if !bytes.Equal(founderRoster[i].Signature, joinerRoster[i].Signature) {
			t.Errorf("roster row %d signature mismatch", i)
		}
	}

	// --- every row's signature must verify against its admitter ---
	// In this ceremony every row is admitted by the founder, so the
	// admitter pubkey is the founder's bolt pubkey.
	founderPub := founderH.boltID.PublicKey
	for _, b := range founderRoster {
		row := boltToAdmissionRow(b)
		if err := spanner.VerifyAdmissionRow(founderPub, &row, b.Signature); err != nil {
			t.Errorf("admission signature for %s failed to verify: %v", b.Pubkey, err)
		}
	}

	// --- a reused token must be rejected at /admit ---
	// Re-running join with the same bundle: the joiner re-POSTs to
	// /admit, whose token is now consumed → 409.
	resp, err := http.Post(joinerSrv.URL+"/api/v1/spanner/bolt/join", "application/json",
		mustJSON(t, map[string]string{"from_addr": founderSrv.URL, "bundle": invResp.Bundle}))
	if err != nil {
		t.Fatalf("replay join POST: %v", err)
	}
	resp.Body.Close()
	// /join surfaces the inviter's non-2xx as 502.
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("replayed join returned %d, want 502 (inviter rejects reused token)", resp.StatusCode)
	}

	// Direct /admit replay also rejected with 409.
	admitCode := postJSON(t, founderSrv.URL+"/api/v1/spanner/bolt/admit", map[string]any{
		"joiner_pubkey":  "deadbeef",
		"hostname":       "another.example.com",
		"pattern_claims": []string{"*.eu.*"},
		"bundle":         invResp.Bundle,
	}, nil)
	if admitCode != http.StatusConflict {
		t.Errorf("direct /admit replay returned %d, want 409", admitCode)
	}

	_ = joinerH // silence unused in case future asserts drop
}

func TestSpannerAdmitRejectsExpiredBundle(t *testing.T) {
	founderH, founderSrv, _ := newCeremonyServer(t, "test-spanner", "founder.example.com", nil, &http.Client{}, false)

	// Found so the founder is a real member.
	if code := postJSON(t, founderSrv.URL+"/api/v1/spanner/init", nil, nil); code != http.StatusOK {
		t.Fatalf("init returned %d", code)
	}

	// Mint an already-expired bundle directly with the founder's key.
	// -time.Hour, not -1ns: UnixMilli truncation eats a sub-millisecond
	// offset, so a -1ns bundle can still read as valid within the mint
	// millisecond — a flaky pass. An hour in the past is unambiguous.
	bundle, err := spanner.MintInviteBundle(founderH.boltID.SigningKey, "test-spanner", founderSrv.URL, -time.Hour)
	if err != nil {
		t.Fatalf("mint expired bundle: %v", err)
	}
	bundleStr, err := spanner.MarshalBundle(bundle)
	if err != nil {
		t.Fatalf("marshal bundle: %v", err)
	}

	code := postJSON(t, founderSrv.URL+"/api/v1/spanner/bolt/admit", map[string]any{
		"joiner_pubkey":  "abcd1234",
		"hostname":       "joiner.example.com",
		"pattern_claims": []string{"*.us-east.*"},
		"bundle":         bundleStr,
	}, nil)
	if code != http.StatusForbidden {
		t.Errorf("/admit with expired bundle returned %d, want 403", code)
	}
}

// TestSpannerJoinReachesTLSFounder is the defect-1 regression guard:
// the join client must reach an inviter served over TLS with a
// self-signed cert (separate vigosrv deployments share no CA). The
// joiner is built with a nil httpClient so it uses the production
// InsecureSkipVerify default; a default-verifying client would fail
// the handshake and the join would 502.
func TestSpannerJoinReachesTLSFounder(t *testing.T) {
	_, founderSrv, _ := newCeremonyServer(t, "test-spanner", "founder.example.com", nil, &http.Client{}, true) // TLS founder
	_, joinerSrv, joinerQ := newCeremonyServer(t, "test-spanner", "joiner.example.com", nil, nil, false)       // nil → InsecureSkipVerify default
	founderClient := founderSrv.Client()                                                                       // trusts the test cert

	if code := postJSONClient(t, founderClient, founderSrv.URL+"/api/v1/spanner/init", nil, nil); code != http.StatusOK {
		t.Fatalf("init returned %d", code)
	}
	var invResp struct {
		Bundle string `json:"bundle"`
	}
	if code := postJSONClient(t, founderClient, founderSrv.URL+"/api/v1/spanner/bolt/invite",
		map[string]any{"ttl_seconds": 3600}, &invResp); code != http.StatusOK {
		t.Fatalf("invite returned %d", code)
	}

	var joinResp struct {
		RosterSize int `json:"roster_size"`
	}
	code := postJSON(t, joinerSrv.URL+"/api/v1/spanner/bolt/join",
		map[string]string{"from_addr": founderSrv.URL, "bundle": invResp.Bundle}, &joinResp)
	if code != http.StatusOK {
		t.Fatalf("join to a TLS founder returned %d, want 200 (defect 1: join client must skip TLS chain verification)", code)
	}
	if joinResp.RosterSize != 2 {
		t.Errorf("roster_size = %d, want 2", joinResp.RosterSize)
	}
	roster, err := joinerQ.ListBolts(t.Context())
	if err != nil || len(roster) != 2 {
		t.Errorf("joiner roster: len=%d err=%v, want 2", len(roster), err)
	}
}

// TestSpannerJoinRejectsTamperedRoster is the defect-2 regression
// guard: handleJoin must verify every roster row's signature before
// persisting, and reject the join if any row fails. A fake inviter
// returns a roster row whose signature has been corrupted.
func TestSpannerJoinRejectsTamperedRoster(t *testing.T) {
	_, joinerSrv, joinerQ := newCeremonyServer(t, "test-spanner", "joiner.example.com", nil, &http.Client{}, false)

	// A founder identity that signs the invite bundle.
	founderID := testBoltIdentity(t)
	bundle, err := spanner.MintInviteBundle(founderID.SigningKey, "test-spanner", "https://founder.example.com:8443", time.Hour)
	if err != nil {
		t.Fatalf("mint bundle: %v", err)
	}
	bundleStr, err := spanner.MarshalBundle(bundle)
	if err != nil {
		t.Fatalf("marshal bundle: %v", err)
	}

	// A roster row genuinely signed by the founder, then corrupted.
	founderHex := hex.EncodeToString(founderID.PublicKey)
	arow := &spanner.AdmissionRow{
		Pubkey: founderHex, Hostname: "founder.example.com", AdmittedAt: 1,
		AdmittedBy: founderHex, PatternClaims: "[]",
	}
	sig, err := spanner.SignAdmissionRow(founderID.SigningKey, arow)
	if err != nil {
		t.Fatalf("sign row: %v", err)
	}
	sig[0] ^= 0xff // tamper

	// Fake inviter: its /admit returns the tampered roster.
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(admitResponse{
			Roster: []spanner.RosterRow{{
				Pubkey: founderHex, Hostname: "founder.example.com", AdmittedAt: 1,
				AdmittedBy: founderHex, PatternClaims: "[]", Signature: sig,
			}},
		})
	}))
	t.Cleanup(fake.Close)

	code := postJSON(t, joinerSrv.URL+"/api/v1/spanner/bolt/join",
		map[string]string{"from_addr": fake.URL, "bundle": bundleStr}, nil)
	if code == http.StatusOK {
		t.Fatalf("join accepted a tampered roster (code %d) — defect 2: rows must be verified before persist", code)
	}
	roster, err := joinerQ.ListBolts(t.Context())
	if err != nil {
		t.Fatalf("joiner ListBolts: %v", err)
	}
	if len(roster) != 0 {
		t.Errorf("tampered roster persisted %d rows, want 0", len(roster))
	}
}

// rewriteRoundTripper redirects every outbound request to a fixed
// base URL, preserving the path. It lets a spanner.Gossiper — which
// builds peer URLs from hostnames — reach an httptest server whose
// hostname is not DNS-resolvable.
type rewriteRoundTripper struct {
	base string // e.g. the httptest server URL
	rt   http.RoundTripper
}

func (rt rewriteRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	u, err := neturl.Parse(rt.base)
	if err != nil {
		return nil, err
	}
	req.URL.Scheme = u.Scheme
	req.URL.Host = u.Host
	return rt.rt.RoundTrip(req)
}

// TestSpannerRosterGossipConvergence runs the full Slice 1 ceremony
// across two servers, mutates the founder's roster directly, then
// runs one gossiper pull cycle on the joiner and asserts both DBs
// hold an identical, signature-valid roster. This exercises the real
// GET /api/v1/spanner/snapshot endpoint plus spanner.MergeSnapshot.
func TestSpannerRosterGossipConvergence(t *testing.T) {
	founderH, founderSrv, founderQ := newCeremonyServer(t, "test-spanner", "founder.example.com", []string{"*.us-west.*"}, &http.Client{}, false)
	joinerH, joinerSrv, joinerQ := newCeremonyServer(t, "test-spanner", "joiner.example.com", []string{"*.us-east.*"}, &http.Client{}, false)

	// --- ceremony: found → invite → join ---
	if code := postJSON(t, founderSrv.URL+"/api/v1/spanner/init", nil, nil); code != http.StatusOK {
		t.Fatalf("init returned %d", code)
	}
	var invResp struct {
		Bundle string `json:"bundle"`
	}
	if code := postJSON(t, founderSrv.URL+"/api/v1/spanner/bolt/invite",
		map[string]any{"ttl_seconds": 3600}, &invResp); code != http.StatusOK {
		t.Fatalf("invite returned %d", code)
	}
	var joinResp struct {
		RosterSize int `json:"roster_size"`
	}
	if code := postJSON(t, joinerSrv.URL+"/api/v1/spanner/bolt/join",
		map[string]string{"from_addr": founderSrv.URL, "bundle": invResp.Bundle}, &joinResp); code != http.StatusOK {
		t.Fatalf("join returned %d", code)
	}
	if joinResp.RosterSize != 2 {
		t.Fatalf("post-join roster_size = %d, want 2", joinResp.RosterSize)
	}

	// --- mutate the founder's roster: admit a third bolt via a fresh
	// invite, so the founder now has 3 rows and the joiner still 2. ---
	if code := postJSON(t, founderSrv.URL+"/api/v1/spanner/bolt/invite",
		map[string]any{"ttl_seconds": 3600}, &invResp); code != http.StatusOK {
		t.Fatalf("second invite returned %d", code)
	}
	thirdPub := hex.EncodeToString(testBoltIdentity(t).PublicKey)
	if code := postJSON(t, founderSrv.URL+"/api/v1/spanner/bolt/admit", map[string]any{
		"joiner_pubkey":  thirdPub,
		"hostname":       "third.example.com",
		"pattern_claims": []string{"*.eu.*"},
		"bundle":         invResp.Bundle,
	}, nil); code != http.StatusOK {
		t.Fatalf("admit of third bolt returned %d", code)
	}

	founderBefore, _ := founderQ.ListBolts(t.Context())
	joinerBefore, _ := joinerQ.ListBolts(t.Context())
	if len(founderBefore) != 3 || len(joinerBefore) != 2 {
		t.Fatalf("pre-gossip sizes: founder=%d joiner=%d, want 3/2", len(founderBefore), len(joinerBefore))
	}

	// --- one gossiper pull cycle on the joiner ---
	// The joiner's roster lists the founder by hostname
	// "founder.example.com"; the rewrite transport redirects that to
	// the founder's httptest server.
	gossipClient := &http.Client{Transport: rewriteRoundTripper{base: founderSrv.URL, rt: http.DefaultTransport}}
	// A short interval so the first tick fires well inside the poll
	// deadline; production cadence is snapshot.tier2_interval (5m).
	// Tier-1 multicast args: the listener cannot join a group in the
	// test env (it logs + returns) and the 30s sender ticker never
	// fires inside the short test window, so Tier-1 stays inert here —
	// this test exercises the Tier-2 REST cycle only.
	g := spanner.NewGossiper(joinerH.sqlDB, joinerH.queries,
		hex.EncodeToString(joinerH.boltID.PublicKey), "8443", 50*time.Millisecond,
		"test-spanner", joinerH.boltID.SigningKey, "224.0.0.45", 1534, 30*time.Second,
		gossipClient)

	ctx, cancel := context.WithCancel(t.Context())
	go g.Run(ctx)
	// Wait for the joiner's roster to converge to 3 rows, then stop.
	deadline := time.Now().Add(5 * time.Second)
	for {
		rows, err := joinerQ.ListBolts(t.Context())
		if err != nil {
			t.Fatalf("joiner ListBolts during poll: %v", err)
		}
		if len(rows) == 3 {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("joiner roster did not converge to 3 rows (got %d)", len(rows))
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()

	// --- both DBs must now hold an identical 3-row roster ---
	founderAfter, err := founderQ.ListBolts(t.Context())
	if err != nil {
		t.Fatalf("founder ListBolts: %v", err)
	}
	joinerAfter, err := joinerQ.ListBolts(t.Context())
	if err != nil {
		t.Fatalf("joiner ListBolts: %v", err)
	}
	if len(founderAfter) != 3 || len(joinerAfter) != 3 {
		t.Fatalf("post-gossip sizes: founder=%d joiner=%d, want 3/3", len(founderAfter), len(joinerAfter))
	}
	founderPub := founderH.boltID.PublicKey
	for i := range founderAfter {
		if founderAfter[i].Pubkey != joinerAfter[i].Pubkey {
			t.Errorf("row %d pubkey mismatch: %q != %q", i, founderAfter[i].Pubkey, joinerAfter[i].Pubkey)
		}
		if !bytes.Equal(founderAfter[i].Signature, joinerAfter[i].Signature) {
			t.Errorf("row %d signature mismatch", i)
		}
		// Every row must verify against the founder (Slice-1 admitter).
		row := boltToAdmissionRow(joinerAfter[i])
		if err := spanner.VerifyAdmissionRow(founderPub, &row, joinerAfter[i].Signature); err != nil {
			t.Errorf("joiner row %s failed signature verify: %v", joinerAfter[i].Pubkey, err)
		}
	}
}

// boltToAdmissionRow rebuilds the canonical AdmissionRow from a stored
// db.SpannerBolt so its signature can be re-verified.
func boltToAdmissionRow(b db.SpannerBolt) spanner.AdmissionRow {
	row := spanner.AdmissionRow{
		Pubkey:        b.Pubkey,
		Hostname:      b.Hostname,
		AdmittedAt:    b.AdmittedAt,
		AdmittedBy:    b.AdmittedBy,
		PatternClaims: b.PatternClaims,
	}
	if b.ConsumedTokenHash.Valid {
		row.ConsumedTokenHash = b.ConsumedTokenHash.String
	}
	return row
}

// mustJSON marshals v and returns a reader, failing the test on error.
func mustJSON(t *testing.T, v any) *bytes.Reader {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return bytes.NewReader(raw)
}
