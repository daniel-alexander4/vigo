// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package curators3

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"
)

// signWith signs r with an explicit SignedHeaders list and credential-scope
// date, so a test can produce a signature that is valid but covers too little.
func signWith(r *http.Request, accessKeyID, secret string, when time.Time, signed []string, scopeDate string) {
	amzDate := when.UTC().Format(amzDateFormat)
	r.Header.Set("X-Amz-Date", amzDate)
	r.Header.Set("X-Amz-Content-Sha256", emptyStringSHA256)
	sort.Strings(signed)
	scope := scopeDate + "/us-east-1/s3/aws4_request"
	creq := canonicalRequest(r, signed, emptyStringSHA256)
	sts := stringToSign(amzDate, scope, creq)
	key := deriveSigningKey(secret, scopeDate, "us-east-1", "s3")
	sig := hex.EncodeToString(hmacSHA256(key, sts))
	r.Header.Set("Authorization", fmt.Sprintf(
		"%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		authzAlgorithm, accessKeyID, scope, strings.Join(signed, ";"), sig))
}

// A correctly computed signature that does not cover host or x-amz-date is
// refused: without them a captured request can be replayed elsewhere or later.
func TestVerifyRequiresHostAndDateSigned(t *testing.T) {
	cs := newTestCredStore(t)
	ctx := context.Background()
	created, _ := cs.Create(ctx, "ci", "*")
	now := time.Now()
	day := now.UTC().Format("20060102")

	for _, signed := range [][]string{
		{"x-amz-content-sha256", "x-amz-date"}, // no host
		{"host", "x-amz-content-sha256"},       // no x-amz-date
	} {
		r := httptest.NewRequest(http.MethodGet, "/vigo-curator/x", nil)
		signWith(r, created.AccessKeyID, created.SecretAccessKey, now, signed, day)
		_, err := NewVerifier(cs).Verify(ctx, r)
		wantSigErr(t, err, "AccessDenied")
	}

	// Control: the full set verifies.
	r := httptest.NewRequest(http.MethodGet, "/vigo-curator/x", nil)
	signWith(r, created.AccessKeyID, created.SecretAccessKey, now, []string{"host", "x-amz-content-sha256", "x-amz-date"}, day)
	if _, err := NewVerifier(cs).Verify(ctx, r); err != nil {
		t.Fatalf("fully signed request rejected: %v", err)
	}
}

// The credential-scope date must equal the X-Amz-Date day.
func TestVerifyRejectsScopeDateMismatch(t *testing.T) {
	cs := newTestCredStore(t)
	ctx := context.Background()
	created, _ := cs.Create(ctx, "ci", "*")
	now := time.Now()
	otherDay := now.Add(-48 * time.Hour).UTC().Format("20060102")

	r := httptest.NewRequest(http.MethodGet, "/vigo-curator/x", nil)
	signWith(r, created.AccessKeyID, created.SecretAccessKey, now, []string{"host", "x-amz-content-sha256", "x-amz-date"}, otherDay)
	_, err := NewVerifier(cs).Verify(ctx, r)
	wantSigErr(t, err, "AuthorizationHeaderMalformed")
}
