// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package curators3

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/swarm/curatormesh"
)

// fakeCatalog is a canned CatalogSource for handler tests.
type fakeCatalog struct {
	cat     *curatormesh.Catalog
	blocked []db.CuratorBlockedArtifact
}

func (f *fakeCatalog) FleetSnapshot(context.Context) (*curatormesh.Catalog, []db.CuratorBlockedArtifact, error) {
	return f.cat, f.blocked, nil
}

// testPubkey is a 64-hex (32-byte) founder puddle pubkey for the fixtures.
const testPubkey = "abababababababababababababababababababababababababababababababab"

// bed is a wired S3 Server plus the fixtures a test needs to sign and assert.
type bed struct {
	srv     *Server
	cred    Created
	blob    []byte
	blobSHA string
	catalog *fakeCatalog
}

// newBed publishes one fixture artifact (`foo`, kind `oci-archive`, version
// `1.0` tagged `latest`, platform `linux/amd64`) and stands up a Server. The
// puddle aggregator + fleet index are nil — handler tests use hex publishers,
// which need no name map; the name helpers are unit-tested separately.
func newBed(t *testing.T) *bed {
	t.Helper()
	cs := newTestCredStore(t)
	cred, err := cs.Create(context.Background(), "test", "*")
	if err != nil {
		t.Fatalf("Create credential: %v", err)
	}

	blob := []byte("OCI-ARCHIVE-BYTES-0123456789")
	sum := sha256.Sum256(blob)
	blobSHA := hex.EncodeToString(sum[:])

	blobsDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(blobsDir, blobSHA), blob, 0o644); err != nil {
		t.Fatalf("write blob: %v", err)
	}

	entry := curatormesh.CatalogEntry{Body: curatormesh.CatalogEntryBody{
		Name:                "foo",
		Kind:                "oci-archive",
		FounderPuddlePubkey: testPubkey,
		Versions: map[string]curatormesh.ArtifactVersion{
			"1.0": {Platforms: []curatormesh.Platform{
				{OS: "linux", Arch: "amd64", BlobSha: blobSHA, Size: uint64(len(blob))},
			}},
		},
		Tags:     map[string]string{"latest": "1.0"},
		IssuedAt: time.Now().UnixMilli(),
	}}
	catalog := &fakeCatalog{cat: &curatormesh.Catalog{Artifacts: []curatormesh.CatalogEntry{entry}}}

	srv := NewServer(NewVerifier(cs), catalog, nil, nil, blobsDir, nil)
	return &bed{srv: srv, cred: cred, blob: blob, blobSHA: blobSHA, catalog: catalog}
}

// do signs and serves a request, returning the recorder.
func (b *bed) do(t *testing.T, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, nil)
	signRequest(r, b.cred.AccessKeyID, b.cred.SecretAccessKey, time.Now())
	rec := httptest.NewRecorder()
	b.srv.Handler().ServeHTTP(rec, r)
	return rec
}

func TestGetObject(t *testing.T) {
	b := newBed(t)
	rec := b.do(t, http.MethodGet, "/vigo-curator/"+testPubkey+"/foo/1.0/linux-amd64")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != string(b.blob) {
		t.Errorf("body mismatch: got %q", rec.Body.String())
	}
	if got := rec.Header().Get("ETag"); got != `"`+b.blobSHA+`"` {
		t.Errorf("ETag = %q, want quoted blob sha", got)
	}
	if got := rec.Header().Get("x-amz-meta-vigo-kind"); got != "oci-archive" {
		t.Errorf("x-amz-meta-vigo-kind = %q, want oci-archive", got)
	}
}

func TestGetObjectByTag(t *testing.T) {
	b := newBed(t)
	rec := b.do(t, http.MethodGet, "/vigo-curator/"+testPubkey+"/foo/latest/linux-amd64")
	if rec.Code != http.StatusOK {
		t.Fatalf("tag key status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != string(b.blob) {
		t.Error("tag-aliased key served the wrong bytes")
	}
}

func TestGetObjectRange(t *testing.T) {
	b := newBed(t)
	r := httptest.NewRequest(http.MethodGet, "/vigo-curator/"+testPubkey+"/foo/1.0/linux-amd64", nil)
	signRequest(r, b.cred.AccessKeyID, b.cred.SecretAccessKey, time.Now())
	r.Header.Set("Range", "bytes=0-3") // Range is added after signing — not a signed header
	rec := httptest.NewRecorder()
	b.srv.Handler().ServeHTTP(rec, r)
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("ranged GET status = %d, want 206", rec.Code)
	}
	if rec.Body.String() != string(b.blob[:4]) {
		t.Errorf("ranged body = %q, want %q", rec.Body.String(), b.blob[:4])
	}
}

func TestHeadObject(t *testing.T) {
	b := newBed(t)
	rec := b.do(t, http.MethodHead, "/vigo-curator/"+testPubkey+"/foo/1.0/linux-amd64")
	if rec.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d, want 200", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Error("HEAD returned a body")
	}
	if rec.Header().Get("x-amz-meta-vigo-kind") != "oci-archive" {
		t.Error("HEAD did not carry the kind metadata header")
	}
	if rec.Header().Get("ETag") == "" {
		t.Error("HEAD did not set ETag")
	}
}

func TestGetObjectNotFound(t *testing.T) {
	b := newBed(t)
	for _, key := range []string{
		"/vigo-curator/" + testPubkey + "/foo/9.9/linux-amd64",  // no such version
		"/vigo-curator/" + testPubkey + "/nope/1.0/linux-amd64", // no such artifact
		"/vigo-curator/" + testPubkey + "/foo/1.0/darwin-arm64", // no such platform
	} {
		rec := b.do(t, http.MethodGet, key)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", key, rec.Code)
		}
	}
}

// TestGetObjectTraversalBlobSha pins the path-traversal guard: the catalog is
// built from agent-reported traits, so a compromised envoy can publish an
// arbitrary string as blob_sha. A non-64-hex value must 404 without the bytes
// it points at ever being served.
func TestGetObjectTraversalBlobSha(t *testing.T) {
	leakDir := t.TempDir()
	secret := []byte("SECRET-FILE-CONTENTS")
	if err := os.WriteFile(filepath.Join(leakDir, "leak.txt"), secret, 0o644); err != nil {
		t.Fatalf("write leak fixture: %v", err)
	}

	for _, evil := range []string{
		"../" + filepath.Base(leakDir) + "/leak.txt", // sibling-dir escape (provably reachable from blobsDir)
		"../../../../etc/passwd",
		"not-a-sha",
		"", // empty sha must not open blobsDir itself
	} {
		b := newBed(t)
		v := b.catalog.cat.Artifacts[0].Body.Versions["1.0"]
		v.Platforms[0].BlobSha = evil
		b.catalog.cat.Artifacts[0].Body.Versions["1.0"] = v

		rec := b.do(t, http.MethodGet, "/vigo-curator/"+testPubkey+"/foo/1.0/linux-amd64")
		if rec.Code != http.StatusNotFound {
			t.Errorf("blob_sha %q: status = %d, want 404", evil, rec.Code)
		}
		if strings.Contains(rec.Body.String(), string(secret)) {
			t.Fatalf("blob_sha %q: traversal leaked file contents", evil)
		}
	}
}

func TestListObjectsV2(t *testing.T) {
	b := newBed(t)
	rec := b.do(t, http.MethodGet, "/vigo-curator?list-type=2")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, testPubkey+"/foo/1.0/linux-amd64") {
		t.Errorf("listing missing the version key:\n%s", body)
	}
	if !strings.Contains(body, testPubkey+"/foo/latest/linux-amd64") {
		t.Errorf("listing missing the tag-alias key:\n%s", body)
	}
}

func TestListObjectsV2Delimiter(t *testing.T) {
	b := newBed(t)
	rec := b.do(t, http.MethodGet, "/vigo-curator?list-type=2&delimiter=/")
	body := rec.Body.String()
	if !strings.Contains(body, "<Prefix>"+testPubkey+"/</Prefix>") {
		t.Errorf("delimiter listing missing the publisher common-prefix:\n%s", body)
	}
}

func TestListBuckets(t *testing.T) {
	b := newBed(t)
	rec := b.do(t, http.MethodGet, "/")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "vigo-curator") {
		t.Errorf("ListBuckets: code %d body %s", rec.Code, rec.Body.String())
	}
}

func TestWriteMethodRejected(t *testing.T) {
	b := newBed(t)
	rec := b.do(t, http.MethodPut, "/vigo-curator/"+testPubkey+"/foo/1.0/linux-amd64")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT status = %d, want 405", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "MethodNotAllowed") {
		t.Error("405 body is not an S3 MethodNotAllowed error")
	}
}

func TestWrongBucket(t *testing.T) {
	b := newBed(t)
	rec := b.do(t, http.MethodGet, "/other-bucket/foo")
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "NoSuchBucket") {
		t.Errorf("wrong bucket: code %d body %s", rec.Code, rec.Body.String())
	}
}

func TestBlockedArtifactInvisible(t *testing.T) {
	b := newBed(t)
	entry := b.catalog.cat.Artifacts[0]
	aid, _ := curatormesh.ArtifactID(entry)
	b.catalog.blocked = []db.CuratorBlockedArtifact{{ArtifactID: aid}}

	rec := b.do(t, http.MethodGet, "/vigo-curator/"+testPubkey+"/foo/1.0/linux-amd64")
	if rec.Code != http.StatusNotFound {
		t.Errorf("blocked artifact GET = %d, want 404", rec.Code)
	}
	rec = b.do(t, http.MethodGet, "/vigo-curator?list-type=2")
	if strings.Contains(rec.Body.String(), "/foo/1.0/") {
		t.Error("blocked artifact still appears in the listing")
	}
}

func TestScopeEnforcement(t *testing.T) {
	b := newBed(t)
	// A credential scoped to a different publisher must not reach testPubkey's objects.
	cs := newTestCredStore(t)
	otherPubkey := strings.Repeat("cd", 32)
	scoped, _ := cs.Create(context.Background(), "scoped", otherPubkey)
	b.srv.verifier = NewVerifier(cs)

	r := httptest.NewRequest(http.MethodGet, "/vigo-curator/"+testPubkey+"/foo/1.0/linux-amd64", nil)
	signRequest(r, scoped.AccessKeyID, scoped.SecretAccessKey, time.Now())
	rec := httptest.NewRecorder()
	b.srv.Handler().ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "AccessDenied") {
		t.Errorf("out-of-scope GET: code %d body %s", rec.Code, rec.Body.String())
	}
}

func TestUnsignedRejected(t *testing.T) {
	b := newBed(t)
	r := httptest.NewRequest(http.MethodGet, "/vigo-curator/"+testPubkey+"/foo/1.0/linux-amd64", nil)
	rec := httptest.NewRecorder()
	b.srv.Handler().ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Errorf("unsigned request status = %d, want 403", rec.Code)
	}
}

// --- pure helper unit tests ---

func TestParseObjectKey(t *testing.T) {
	pub, art, ver, goos, goarch, ok := parseObjectKey("alexander4/foo/1.0/linux-amd64")
	if !ok || pub != "alexander4" || art != "foo" || ver != "1.0" || goos != "linux" || goarch != "amd64" {
		t.Errorf("parse = (%q,%q,%q,%q,%q,%v)", pub, art, ver, goos, goarch, ok)
	}
	for _, bad := range []string{"too/few/parts", "a/b/c/d/e", "a/b/c/noarch", "/b/c/os-arch"} {
		if _, _, _, _, _, ok := parseObjectKey(bad); ok {
			t.Errorf("parseObjectKey(%q) accepted a malformed key", bad)
		}
	}
}

func TestPublisherNameHelpers(t *testing.T) {
	byPubkey := map[string]string{testPubkey: "alexander4"}
	byName := map[string]string{"alexander4": testPubkey}

	if got := publisherForPubkey(testPubkey, byPubkey); got != "alexander4" {
		t.Errorf("publisherForPubkey claimed = %q, want alexander4", got)
	}
	if got := publisherForPubkey(strings.Repeat("11", 32), byPubkey); got != strings.Repeat("11", 32) {
		t.Error("publisherForPubkey of an unclaimed pubkey should be the hex")
	}
	if got := pubkeyForPublisher("alexander4", byName); got != testPubkey {
		t.Errorf("pubkeyForPublisher(name) = %q, want the pubkey", got)
	}
	if got := pubkeyForPublisher(testPubkey, byName); got != testPubkey {
		t.Error("pubkeyForPublisher of a hex publisher should pass through")
	}
	if got := pubkeyForPublisher("no-such-name", byName); got != "" {
		t.Error("pubkeyForPublisher of an unknown name should be empty")
	}
}

func TestScopeAllows(t *testing.T) {
	wild := &Resolved{Row: db.CuratorS3Credential{Scope: "*"}}
	if !scopeAllows(wild, testPubkey) {
		t.Error("`*` scope must allow any publisher")
	}
	scoped := &Resolved{Row: db.CuratorS3Credential{Scope: testPubkey}}
	if !scopeAllows(scoped, testPubkey) {
		t.Error("a pubkey scope must allow its own publisher")
	}
	if scopeAllows(scoped, strings.Repeat("cd", 32)) {
		t.Error("a pubkey scope must deny a different publisher")
	}
	// A stored name is never re-resolved per request: whatever the fleet
	// name map says now, a non-pubkey scope is refused.
	named := &Resolved{Row: db.CuratorS3Credential{Scope: "alexander4"}}
	if scopeAllows(named, testPubkey) {
		t.Error("a name-valued stored scope must be refused, not resolved through envoy claims")
	}
	if scopeAllows(&Resolved{Row: db.CuratorS3Credential{Scope: ""}}, testPubkey) {
		t.Error("an empty stored scope must be refused")
	}
}

// Create pins a name scope to the founder pubkey it names at mint time, and
// refuses a name that does not resolve.
func TestCreatePinsNameScopeToPubkey(t *testing.T) {
	cs := newTestCredStore(t)
	ctx := context.Background()
	names := map[string]string{"alexander4": testPubkey}
	cs.SetNameResolver(func(context.Context) map[string]string { return names })

	created, err := cs.Create(ctx, "ci", "alexander4")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Scope != testPubkey {
		t.Fatalf("scope = %q, want the pinned pubkey %q", created.Scope, testPubkey)
	}
	// An envoy later re-points the name: the credential is unaffected.
	names["alexander4"] = strings.Repeat("cd", 32)
	got, _ := cs.Lookup(ctx, created.AccessKeyID)
	if !scopeAllows(got, testPubkey) || scopeAllows(got, strings.Repeat("cd", 32)) {
		t.Fatalf("credential followed the re-pointed name: scope %q", got.Row.Scope)
	}

	if _, err := cs.Create(ctx, "ci", "nobody"); !errors.Is(err, ErrUnknownPublisher) {
		t.Fatalf("unknown name: err = %v, want ErrUnknownPublisher", err)
	}
}
