// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package curators3

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"
)

// --- AWS-published worked example (the algorithm gate) -----------------------
//
// AWS documents a complete SigV4 calculation for `GET https://iam.amazonaws.com/
// ?Action=ListUsers&Version=2010-05-08` with the example credential. Reproducing
// its canonical request, signing key, and final signature exactly proves the
// whole pipeline — canonical request, string-to-sign, HMAC key chain, HMAC —
// matches AWS byte-for-byte.

const (
	awsExampleSecret = "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"
	awsExampleDate   = "20150830"
	awsExampleAmz    = "20150830T123600Z"
	awsExampleRegion = "us-east-1"
	awsExampleSvc    = "iam"
	// Documented intermediate + final values.
	awsExampleSigningKey = "c4afb1cc5771d871763a393e44b703571b55cc28424d1a5e86da6ed3c154a4b9"
	awsExampleSignature  = "5d672d79c15b13162d9279b0855cfba6789a8edb4c82c400e06b5924a6f2b5d7"
)

// awsExampleCanonicalRequest is the canonical request AWS documents for the
// example — structural, derived from the SigV4 spec layout.
const awsExampleCanonicalRequest = "GET\n" +
	"/\n" +
	"Action=ListUsers&Version=2010-05-08\n" +
	"content-type:application/x-www-form-urlencoded; charset=utf-8\n" +
	"host:iam.amazonaws.com\n" +
	"x-amz-date:20150830T123600Z\n" +
	"\n" +
	"content-type;host;x-amz-date\n" +
	emptyStringSHA256

func awsExampleRequest() *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/?Action=ListUsers&Version=2010-05-08", nil)
	r.Host = "iam.amazonaws.com"
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	r.Header.Set("X-Amz-Date", awsExampleAmz)
	return r
}

func TestCanonicalRequestMatchesAWSExample(t *testing.T) {
	got := canonicalRequest(awsExampleRequest(),
		[]string{"content-type", "host", "x-amz-date"}, emptyStringSHA256)
	if got != awsExampleCanonicalRequest {
		t.Errorf("canonical request mismatch:\n got:\n%q\nwant:\n%q", got, awsExampleCanonicalRequest)
	}
}

func TestSigningKeyMatchesAWSExample(t *testing.T) {
	got := hex.EncodeToString(deriveSigningKey(awsExampleSecret, awsExampleDate, awsExampleRegion, awsExampleSvc))
	if got != awsExampleSigningKey {
		t.Errorf("signing key = %s, want %s", got, awsExampleSigningKey)
	}
}

func TestSignatureMatchesAWSExample(t *testing.T) {
	// The full pipeline against AWS's documented final signature — if any
	// stage (canonical request, string-to-sign, key chain, HMAC) is wrong,
	// this single value won't match.
	creq := canonicalRequest(awsExampleRequest(),
		[]string{"content-type", "host", "x-amz-date"}, emptyStringSHA256)
	scope := awsExampleDate + "/" + awsExampleRegion + "/" + awsExampleSvc + "/aws4_request"
	sts := stringToSign(awsExampleAmz, scope, creq)
	key := deriveSigningKey(awsExampleSecret, awsExampleDate, awsExampleRegion, awsExampleSvc)
	got := hex.EncodeToString(hmacSHA256(key, sts))
	if got != awsExampleSignature {
		t.Errorf("signature = %s, want %s", got, awsExampleSignature)
	}
}

func TestUriEncode(t *testing.T) {
	cases := []struct {
		in          string
		encodeSlash bool
		want        string
	}{
		{"abcXYZ019", false, "abcXYZ019"},
		{"-._~", false, "-._~"},      // unreserved, never encoded
		{"a b", false, "a%20b"},      // space
		{"a/b", false, "a/b"},        // slash preserved
		{"a/b", true, "a%2Fb"},       // slash encoded
		{"k=v&x", true, "k%3Dv%26x"}, // query metacharacters
		{"é", false, "%C3%A9"},       // UTF-8 encoded per-byte
		{"plus+", false, "plus%2B"},  // '+' is not unreserved
	}
	for _, c := range cases {
		if got := uriEncode(c.in, c.encodeSlash); got != c.want {
			t.Errorf("uriEncode(%q, %v) = %q, want %q", c.in, c.encodeSlash, got, c.want)
		}
	}
}

// --- Verify: round-trip + rejection paths ------------------------------------

// signRequest signs r SigV4-style with the given credential — the inverse of
// Verify, for round-trip tests. region/service are fixed to the values an S3
// client uses against this server.
func signRequest(r *http.Request, accessKeyID, secret string, when time.Time) {
	amzDate := when.UTC().Format(amzDateFormat)
	r.Header.Set("X-Amz-Date", amzDate)
	r.Header.Set("X-Amz-Content-Sha256", emptyStringSHA256)
	signed := []string{"host", "x-amz-content-sha256", "x-amz-date"}
	sort.Strings(signed)
	date := amzDate[:8]
	scope := date + "/us-east-1/s3/aws4_request"
	creq := canonicalRequest(r, signed, emptyStringSHA256)
	sts := stringToSign(amzDate, scope, creq)
	key := deriveSigningKey(secret, date, "us-east-1", "s3")
	sig := hex.EncodeToString(hmacSHA256(key, sts))
	r.Header.Set("Authorization", fmt.Sprintf(
		"%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		authzAlgorithm, accessKeyID, scope, strings.Join(signed, ";"), sig))
}

func wantSigErr(t *testing.T, err error, code string) {
	t.Helper()
	var se *Error
	if !errors.As(err, &se) {
		t.Fatalf("expected *Error %q, got %v", code, err)
	}
	if se.Code != code {
		t.Errorf("error code = %q, want %q", se.Code, code)
	}
}

func TestVerifyRoundTrip(t *testing.T) {
	cs := newTestCredStore(t)
	ctx := context.Background()
	created, err := cs.Create(ctx, "ci-runner", testPubkey)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	r := httptest.NewRequest(http.MethodGet, "/vigo-curator/alexander4/foo/1.0/linux-amd64", nil)
	signRequest(r, created.AccessKeyID, created.SecretAccessKey, time.Now())

	resolved, err := NewVerifier(cs).Verify(ctx, r)
	if err != nil {
		t.Fatalf("Verify rejected a correctly-signed request: %v", err)
	}
	if resolved == nil || resolved.Row.Scope != testPubkey {
		t.Errorf("Verify returned %+v, want the alexander4-scoped credential", resolved)
	}
}

func TestVerifyRejectsWrongSecret(t *testing.T) {
	cs := newTestCredStore(t)
	ctx := context.Background()
	created, _ := cs.Create(ctx, "ci", "*")

	// Valid access key id, but signed with the wrong secret.
	r := httptest.NewRequest(http.MethodGet, "/vigo-curator/x", nil)
	signRequest(r, created.AccessKeyID, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", time.Now())

	_, err := NewVerifier(cs).Verify(ctx, r)
	wantSigErr(t, err, "SignatureDoesNotMatch")
}

func TestVerifyRejectsUnknownKey(t *testing.T) {
	cs := newTestCredStore(t)
	r := httptest.NewRequest(http.MethodGet, "/vigo-curator/x", nil)
	signRequest(r, "VIGONOSUCHKEYAAAAAAA", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", time.Now())

	_, err := NewVerifier(cs).Verify(context.Background(), r)
	wantSigErr(t, err, "InvalidAccessKeyId")
}

func TestVerifyRejectsSkew(t *testing.T) {
	cs := newTestCredStore(t)
	ctx := context.Background()
	created, _ := cs.Create(ctx, "ci", "*")

	r := httptest.NewRequest(http.MethodGet, "/vigo-curator/x", nil)
	signRequest(r, created.AccessKeyID, created.SecretAccessKey, time.Now().Add(-20*time.Minute))

	_, err := NewVerifier(cs).Verify(ctx, r)
	wantSigErr(t, err, "RequestTimeTooSkewed")
}

func TestVerifyRejectsMissingAndMalformed(t *testing.T) {
	cs := newTestCredStore(t)
	ctx := context.Background()

	// No Authorization header at all.
	r := httptest.NewRequest(http.MethodGet, "/vigo-curator/x", nil)
	_, err := NewVerifier(cs).Verify(ctx, r)
	wantSigErr(t, err, "AccessDenied")

	// Present but unparseable.
	r2 := httptest.NewRequest(http.MethodGet, "/vigo-curator/x", nil)
	r2.Header.Set("Authorization", "AWS4-HMAC-SHA256 garbage")
	r2.Header.Set("X-Amz-Date", time.Now().UTC().Format(amzDateFormat))
	_, err = NewVerifier(cs).Verify(ctx, r2)
	wantSigErr(t, err, "AuthorizationHeaderMalformed")
}

func TestVerifyRejectsNonS3Service(t *testing.T) {
	cs := newTestCredStore(t)
	ctx := context.Background()
	created, _ := cs.Create(ctx, "ci", "*")

	r := httptest.NewRequest(http.MethodGet, "/vigo-curator/x", nil)
	signRequest(r, created.AccessKeyID, created.SecretAccessKey, time.Now())
	// Rewrite the credential scope's service to a non-s3 value.
	authz := r.Header.Get("Authorization")
	r.Header.Set("Authorization", strings.Replace(authz, "/s3/aws4_request", "/iam/aws4_request", 1))

	_, err := NewVerifier(cs).Verify(ctx, r)
	wantSigErr(t, err, "AuthorizationHeaderMalformed")
}
