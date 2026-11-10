// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package curators3

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"
)

// AWS Signature Version 4 verification for the curator S3 API (ADR-027).
//
// A stdlib-only implementation of the SigV4 `AWS4-HMAC-SHA256` header-auth
// algorithm — `aws-sdk-go-v2` is not a dependency and MinIO's verifier is
// AGPL. The algorithm is fully specified by AWS; its correctness is gated by
// AWS's published worked example (see sigv4_test.go), which exercises the
// whole pipeline — canonical request → string-to-sign → signing-key chain →
// HMAC — against AWS's documented output.
//
// Header auth only; presigned-URL (`?X-Amz-…`) auth is post-v1 per ADR-027.

const (
	authzAlgorithm = "AWS4-HMAC-SHA256"
	// skewWindow bounds request-clock drift — AWS's default tolerance, and
	// the replay window for a captured `Authorization` header.
	skewWindow = 15 * time.Minute
	// amzDateFormat is the ISO-8601 basic form S3 SDKs send in `X-Amz-Date`.
	amzDateFormat = "20060102T150405Z"
	// emptyStringSHA256 is hex(SHA256("")) — the payload hash for a
	// body-less GET/HEAD when the client omits `X-Amz-Content-Sha256`.
	emptyStringSHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

// Error is a SigV4 verification failure carrying the S3 error code and HTTP
// status the S3 listener's middleware (slice 3) renders as an XML error.
type Error struct {
	Code       string // S3 error code, e.g. "SignatureDoesNotMatch"
	HTTPStatus int
	Message    string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func errAccessDenied(msg string) *Error {
	return &Error{Code: "AccessDenied", HTTPStatus: http.StatusForbidden, Message: msg}
}
func errMalformed(msg string) *Error {
	return &Error{Code: "AuthorizationHeaderMalformed", HTTPStatus: http.StatusBadRequest, Message: msg}
}

// Verifier authenticates S3 requests against the SigV4 credential store.
type Verifier struct {
	store *CredStore
	// Now is the clock used for skew checks — overridable in tests.
	Now func() time.Time
}

// NewVerifier wires a Verifier onto the credential store.
func NewVerifier(store *CredStore) *Verifier {
	return &Verifier{store: store, Now: time.Now}
}

// Verify authenticates an S3 request. On success it returns the resolved
// credential (the caller reads its scope for per-publisher authorization). On
// failure it returns an [*Error] carrying the S3 error code; a non-[*Error]
// return is an internal fault (the caller maps it to 500).
func (v *Verifier) Verify(ctx context.Context, r *http.Request) (*Resolved, error) {
	authz := r.Header.Get("Authorization")
	if authz == "" {
		return nil, errAccessDenied("request is not signed")
	}
	parts, err := parseAuthorizationHeader(authz)
	if err != nil {
		return nil, err
	}
	if parts.service != "s3" {
		return nil, errMalformed(fmt.Sprintf("credential scope service is %q, expected s3", parts.service))
	}

	amzDate := r.Header.Get("X-Amz-Date")
	signedAt, err := time.Parse(amzDateFormat, amzDate)
	if err != nil {
		return nil, errMalformed("missing or malformed X-Amz-Date header")
	}
	// The credential-scope date must be the day the request was signed: the
	// signing key is derived per day, so a key derived for another day must
	// not authenticate this request.
	if parts.date != signedAt.UTC().Format("20060102") {
		return nil, errMalformed("credential scope date does not match X-Amz-Date")
	}
	// host and x-amz-date must be covered by the signature, otherwise a
	// captured request can be replayed against another host or re-dated.
	for _, h := range []string{"host", "x-amz-date"} {
		if !slices.Contains(parts.signedHeaders, h) {
			return nil, errAccessDenied(fmt.Sprintf("SignedHeaders must include %s", h))
		}
	}
	if drift := v.Now().Sub(signedAt); drift > skewWindow || drift < -skewWindow {
		return nil, &Error{
			Code:       "RequestTimeTooSkewed",
			HTTPStatus: http.StatusForbidden,
			Message:    "the request time is outside the 15-minute tolerance window",
		}
	}

	resolved, err := v.store.Lookup(ctx, parts.accessKeyID)
	if err != nil {
		return nil, fmt.Errorf("resolving s3 credential: %w", err)
	}
	if resolved == nil {
		return nil, &Error{
			Code:       "InvalidAccessKeyId",
			HTTPStatus: http.StatusForbidden,
			Message:    "the access key id is not known to this server",
		}
	}

	creq := canonicalRequest(r, parts.signedHeaders, hashedPayload(r))
	sts := stringToSign(amzDate, parts.scope(), creq)
	key := deriveSigningKey(resolved.Secret, parts.date, parts.region, parts.service)
	want := hex.EncodeToString(hmacSHA256(key, sts))

	if subtle.ConstantTimeCompare([]byte(want), []byte(parts.signature)) != 1 {
		return nil, &Error{
			Code:       "SignatureDoesNotMatch",
			HTTPStatus: http.StatusForbidden,
			Message:    "the request signature does not match",
		}
	}
	return resolved, nil
}

// authParts is a parsed `Authorization: AWS4-HMAC-SHA256 …` header.
type authParts struct {
	accessKeyID   string
	date          string // yyyymmdd
	region        string
	service       string
	signedHeaders []string // lowercase, sorted
	signature     string   // lowercase hex
}

// scope is the credential-scope string used in the string-to-sign and the
// signing-key derivation.
func (a authParts) scope() string {
	return a.date + "/" + a.region + "/" + a.service + "/aws4_request"
}

func parseAuthorizationHeader(h string) (authParts, error) {
	rest, ok := strings.CutPrefix(h, authzAlgorithm+" ")
	if !ok {
		return authParts{}, errMalformed("authorization header is not " + authzAlgorithm)
	}
	var cred, signed, sig string
	for _, field := range strings.Split(rest, ",") {
		k, val, ok := strings.Cut(strings.TrimSpace(field), "=")
		if !ok {
			return authParts{}, errMalformed("malformed authorization header field")
		}
		switch k {
		case "Credential":
			cred = val
		case "SignedHeaders":
			signed = val
		case "Signature":
			sig = val
		}
	}
	if cred == "" || signed == "" || sig == "" {
		return authParts{}, errMalformed("authorization header missing Credential, SignedHeaders, or Signature")
	}
	cp := strings.Split(cred, "/")
	if len(cp) != 5 || cp[4] != "aws4_request" {
		return authParts{}, errMalformed("malformed credential scope")
	}
	headers := strings.Split(signed, ";")
	sort.Strings(headers)
	return authParts{
		accessKeyID:   cp[0],
		date:          cp[1],
		region:        cp[2],
		service:       cp[3],
		signedHeaders: headers,
		signature:     strings.ToLower(sig),
	}, nil
}

// hashedPayload is the canonical-request payload hash: the client's
// `X-Amz-Content-Sha256` header verbatim (S3 SDKs always send it), or the
// empty-body hash when absent. The read-only API has no body to re-hash.
func hashedPayload(r *http.Request) string {
	if v := r.Header.Get("X-Amz-Content-Sha256"); v != "" {
		return v
	}
	return emptyStringSHA256
}

// canonicalRequest builds the SigV4 canonical request string.
func canonicalRequest(r *http.Request, signedHeaders []string, payloadHash string) string {
	uri := uriEncode(r.URL.Path, false)
	if uri == "" {
		uri = "/"
	}
	return strings.Join([]string{
		r.Method,
		uri,
		canonicalQueryString(r.URL.RawQuery),
		canonicalHeaders(r, signedHeaders), // ends with "\n"
		strings.Join(signedHeaders, ";"),
		payloadHash,
	}, "\n")
}

// canonicalHeaders renders `name:value\n` for each signed header, in the
// (already-sorted) order of signedHeaders. The result ends with "\n".
func canonicalHeaders(r *http.Request, signedHeaders []string) string {
	var b strings.Builder
	for _, name := range signedHeaders {
		b.WriteString(name)
		b.WriteByte(':')
		if name == "host" {
			// Go keeps the Host header in r.Host, not r.Header.
			b.WriteString(trimAll(r.Host))
		} else {
			vals := r.Header.Values(name)
			for i, v := range vals {
				vals[i] = trimAll(v)
			}
			b.WriteString(strings.Join(vals, ","))
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// canonicalQueryString sorts and re-encodes the query parameters. Components
// are percent-decoded without `+`→space (SigV4 does not form-decode), then
// re-encoded with the AWS UriEncode rules.
func canonicalQueryString(rawQuery string) string {
	if rawQuery == "" {
		return ""
	}
	type kv struct{ k, v string }
	var items []kv
	for _, pair := range strings.Split(rawQuery, "&") {
		if pair == "" {
			continue
		}
		rawK, rawV, _ := strings.Cut(pair, "=")
		items = append(items, kv{
			k: uriEncode(pathUnescape(rawK), true),
			v: uriEncode(pathUnescape(rawV), true),
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].k != items[j].k {
			return items[i].k < items[j].k
		}
		return items[i].v < items[j].v
	})
	parts := make([]string, len(items))
	for i, it := range items {
		parts[i] = it.k + "=" + it.v
	}
	return strings.Join(parts, "&")
}

// pathUnescape percent-decodes a query component. On malformed input it
// returns the raw component — the signature compare then fails closed.
func pathUnescape(s string) string {
	if d, err := url.PathUnescape(s); err == nil {
		return d
	}
	return s
}

// stringToSign builds the SigV4 string-to-sign.
func stringToSign(amzDate, scope, canonicalReq string) string {
	return authzAlgorithm + "\n" + amzDate + "\n" + scope + "\n" + sha256Hex(canonicalReq)
}

// deriveSigningKey runs the four-stage SigV4 HMAC key-derivation chain.
func deriveSigningKey(secretAccessKey, date, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secretAccessKey), date)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	return hmacSHA256(kService, "aws4_request")
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// trimAll applies AWS's header-value Trimall: strip leading/trailing
// whitespace and collapse internal whitespace runs to a single space.
func trimAll(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// uriEncode is the AWS UriEncode function. Unreserved characters
// (`A-Za-z0-9-._~`) pass through; `/` is preserved unless encodeSlash; every
// other byte becomes uppercase `%XX`. Operates byte-wise, so UTF-8 is encoded
// per-byte.
func uriEncode(s string, encodeSlash bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '.', c == '_', c == '~':
			b.WriteByte(c)
		case c == '/':
			if encodeSlash {
				b.WriteString("%2F")
			} else {
				b.WriteByte('/')
			}
		default:
			b.WriteByte('%')
			b.WriteString(strings.ToUpper(hex.EncodeToString([]byte{c})))
		}
	}
	return b.String()
}
