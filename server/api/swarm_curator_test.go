// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.alexander4.io/vigo/server/swarm/curatormesh"
)

func newCuratorHandler(t *testing.T) *CuratorHandler {
	t.Helper()
	_, queries := openAuditTestDB(t)
	// fleetIndex nil ⇒ no envoys ⇒ the aggregator sees an empty fleet
	// catalog. The curatormesh resolution helpers (LookupByID / ResolveVersion
	// / ResolvePlatform) are exercised in their own package's tests; here we
	// cover the HTTP wiring, the block table, and the empty-catalog paths.
	return NewCuratorHandler(curatormesh.NewAggregator(queries), queries, nil, t.TempDir())
}

func mkReq(method, target string, pathVals map[string]string) (*httptest.ResponseRecorder, *http.Request) {
	r := httptest.NewRequest(method, target, nil)
	for k, v := range pathVals {
		r.SetPathValue(k, v)
	}
	return httptest.NewRecorder(), r
}

const sampleAID = "1111111111111111111111111111111111111111111111111111111111111111"

func TestCurator_Resolve_ParamValidation(t *testing.T) {
	h := newCuratorHandler(t)
	cases := []struct {
		name string
		q    string
		want int
	}{
		{"missing artifact_id", "?tag=latest&os=linux&arch=amd64", http.StatusBadRequest},
		{"bad artifact_id", "?artifact_id=deadbeef&tag=latest&os=linux&arch=amd64", http.StatusBadRequest},
		{"no tag or version", "?artifact_id=" + sampleAID + "&os=linux&arch=amd64", http.StatusBadRequest},
		{"missing os/arch", "?artifact_id=" + sampleAID + "&tag=latest", http.StatusBadRequest},
		{"valid params, empty catalog ⇒ not found", "?artifact_id=" + sampleAID + "&tag=latest&os=linux&arch=amd64", http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec, r := mkReq(http.MethodGet, "/api/v1/swarm/curator/resolve"+tc.q, nil)
			h.handleResolve(rec, r)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestCurator_BlockUnblock_RoundTrip(t *testing.T) {
	h := newCuratorHandler(t)

	// Block.
	rec, r := mkReq(http.MethodPost, "/api/v1/swarm/curator/block/"+sampleAID, map[string]string{"artifact_id": sampleAID})
	h.handleBlock(rec, adminReq(r))
	if rec.Code != http.StatusOK {
		t.Fatalf("block status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}

	// Resolve now 404s with the operator-block reason, before any catalog lookup.
	rec, r = mkReq(http.MethodGet, "/api/v1/swarm/curator/resolve?artifact_id="+sampleAID+"&tag=latest&os=linux&arch=amd64", nil)
	h.handleResolve(rec, r)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "blocked by the operator") {
		t.Fatalf("blocked resolve: status=%d body=%s", rec.Code, rec.Body.String())
	}

	// List shows the blocked artifact.
	rec, r = mkReq(http.MethodGet, "/api/v1/swarm/curator/list", nil)
	h.handleList(rec, adminReq(r))
	var lr struct {
		Blocked []struct {
			ArtifactID string `json:"artifact_id"`
		} `json:"blocked"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &lr); err != nil {
		t.Fatalf("list decode: %v (body: %s)", err, rec.Body.String())
	}
	if len(lr.Blocked) != 1 || lr.Blocked[0].ArtifactID != sampleAID {
		t.Fatalf("list.blocked = %+v, want [%s]", lr.Blocked, sampleAID)
	}

	// Unblock.
	rec, r = mkReq(http.MethodPost, "/api/v1/swarm/curator/unblock/"+sampleAID, map[string]string{"artifact_id": sampleAID})
	h.handleUnblock(rec, adminReq(r))
	if rec.Code != http.StatusOK {
		t.Fatalf("unblock status = %d, want 200", rec.Code)
	}

	// Resolve no longer short-circuits on the block; it falls through to the
	// (empty) catalog and 404s with "no such artifact".
	rec, r = mkReq(http.MethodGet, "/api/v1/swarm/curator/resolve?artifact_id="+sampleAID+"&tag=latest&os=linux&arch=amd64", nil)
	h.handleResolve(rec, r)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "no such artifact") {
		t.Fatalf("unblocked resolve: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCurator_Blob_ScopedToCuratorArtifacts(t *testing.T) {
	h := newCuratorHandler(t)
	sha := "2222222222222222222222222222222222222222222222222222222222222222"
	// Empty catalog ⇒ no curator artifact references this sha ⇒ 404, even
	// though it's never checked on disk (the relay won't serve arbitrary blobs).
	rec, r := mkReq(http.MethodGet, "/api/v1/swarm/curator/blob/"+sha, map[string]string{"sha256": sha})
	h.handleBlob(rec, r)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "no curator artifact") {
		t.Fatalf("blob relay: status=%d body=%s", rec.Code, rec.Body.String())
	}
	// Malformed sha ⇒ 400.
	rec, r = mkReq(http.MethodGet, "/api/v1/swarm/curator/blob/nothex", map[string]string{"sha256": "nothex"})
	h.handleBlob(rec, r)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("blob relay bad sha: status=%d", rec.Code)
	}
}

func TestCurator_Inspect_VersionsOnEmptyCatalog(t *testing.T) {
	h := newCuratorHandler(t)
	for _, ep := range []struct {
		name string
		fn   func(http.ResponseWriter, *http.Request)
	}{
		{"inspect", h.handleInspect},
		{"versions", h.handleVersions},
	} {
		t.Run(ep.name, func(t *testing.T) {
			rec, r := mkReq(http.MethodGet, "/api/v1/swarm/curator/"+ep.name+"/"+sampleAID, map[string]string{"artifact_id": sampleAID})
			ep.fn(rec, adminReq(r))
			if rec.Code != http.StatusNotFound {
				t.Fatalf("%s status = %d, want 404 (body: %s)", ep.name, rec.Code, rec.Body.String())
			}
		})
		// inspect on a *blocked* artifact still returns 200 (entry null, blocked true).
	}
	// Inspect on a blocked-but-absent artifact: 200, blocked=true, entry=null.
	rec, r := mkReq(http.MethodPost, "/api/v1/swarm/curator/block/"+sampleAID, map[string]string{"artifact_id": sampleAID})
	h.handleBlock(rec, adminReq(r))
	rec, r = mkReq(http.MethodGet, "/api/v1/swarm/curator/inspect/"+sampleAID, map[string]string{"artifact_id": sampleAID})
	h.handleInspect(rec, adminReq(r))
	if rec.Code != http.StatusOK {
		t.Fatalf("blocked-inspect status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var ir struct {
		Entry   *json.RawMessage `json:"entry"`
		Blocked bool             `json:"blocked"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &ir); err != nil || ir.Entry != nil || !ir.Blocked {
		t.Fatalf("blocked-inspect body = %s (err %v)", rec.Body.String(), err)
	}
}

func TestCurator_Fleet_EmptyCatalog(t *testing.T) {
	h := newCuratorHandler(t)
	rec, r := mkReq(http.MethodGet, "/api/v1/swarm/curator/fleet", nil)
	h.handleFleet(rec, adminReq(r))
	if rec.Code != http.StatusOK {
		t.Fatalf("fleet status = %d, want 200", rec.Code)
	}
	var fr struct {
		Participants  []string `json:"participants"`
		ArtifactCount int      `json:"artifact_count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &fr); err != nil {
		t.Fatalf("fleet decode: %v", err)
	}
	if fr.ArtifactCount != 0 || len(fr.Participants) != 0 {
		t.Fatalf("fleet = %+v, want empty", fr)
	}
}
