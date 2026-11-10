// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package spanner

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// Spanner pull authentication.
//
// The Tier-2 gossip pulls (/api/v1/spanner/snapshot and /fleet-snapshot)
// carry no operator session and ride the REST listener, which is plain TLS
// with no client cert — so before this, any host that could TCP-reach the
// port received the full replicated user directory (username, email, role,
// permissions, ssh_public_key, oidc_subject) plus the roster and envoy
// inventory, unauthenticated.
//
// This adds an Ed25519 proof header to those GETs: the pulling bolt signs
// (domain ‖ ts ‖ request-path) with its bolt identity, and the serving bolt
// verifies the signature and confirms the signer is a non-revoked roster
// member before serving. It mirrors server/cpauth (the gRPC equivalent) with
// a DISTINCT domain so a proof minted here can never replay as a cpauth
// call. Because :8443 has no mTLS floor, the proof is path-bound and
// timestamp-windowed — bounding an on-path observer's replay to the same
// endpoint within a short window; the per-row payload signatures already
// guard merge integrity independently.

// Pull-proof header names. Standard HTTP headers (not gRPC metadata), so a
// GET can carry them.
const (
	pullHeaderPubkey = "X-Vigo-Spanner-Pubkey" // signer's Ed25519 pubkey, raw-bytes hex (roster form)
	pullHeaderTS     = "X-Vigo-Spanner-Ts"     // unix-seconds timestamp, decimal string
	pullHeaderSig    = "X-Vigo-Spanner-Sig"    // Ed25519 signature over pullSignedMessage, hex
)

// pullAuthDomain separates these signatures from every other use of the bolt
// key (admission rows, Tier-1 digests, cpauth calls).
const pullAuthDomain = "vigo-spanner-pull-v1"

// PullAuthWindow bounds accepted timestamp skew on a pull proof.
const PullAuthWindow = 30 * time.Second

// pullSignedMessage is the canonical signed bytes: domain ‖ 0x00 ‖ ts ‖ 0x00 ‖ path.
func pullSignedMessage(ts, path string) []byte {
	msg := make([]byte, 0, len(pullAuthDomain)+1+len(ts)+1+len(path))
	msg = append(msg, pullAuthDomain...)
	msg = append(msg, 0)
	msg = append(msg, ts...)
	msg = append(msg, 0)
	msg = append(msg, path...)
	return msg
}

// SignPullRequest attaches a fresh Ed25519 proof to an outbound gossip pull.
// pubHex is the signer's raw-hex bolt pubkey, in the form the verifier
// compares against the roster; the signature binds req.URL.Path so a proof
// for /snapshot cannot be replayed to /fleet-snapshot. A key of the wrong
// size (no bolt identity) is a no-op — the request goes out unsigned and the
// peer rejects it, rather than panicking in ed25519.Sign.
func SignPullRequest(req *http.Request, key ed25519.PrivateKey, pubHex string) {
	if len(key) != ed25519.PrivateKeySize {
		return
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	sig := ed25519.Sign(key, pullSignedMessage(ts, req.URL.Path))
	req.Header.Set(pullHeaderPubkey, pubHex)
	req.Header.Set(pullHeaderTS, ts)
	req.Header.Set(pullHeaderSig, hex.EncodeToString(sig))
}

// VerifyPullRequest checks the proof headers on an inbound gossip pull: the
// timestamp is within window and the signature verifies against the carried
// pubkey over (domain ‖ ts ‖ r.URL.Path). It returns the authenticated
// pubkey as raw-hex — the caller MUST still confirm that pubkey is a
// non-revoked roster member; a valid signature proves only key possession,
// not authorization.
func VerifyPullRequest(r *http.Request, window time.Duration) (pubHex string, err error) {
	pubHex = r.Header.Get(pullHeaderPubkey)
	tsStr := r.Header.Get(pullHeaderTS)
	sigHex := r.Header.Get(pullHeaderSig)
	if pubHex == "" || tsStr == "" || sigHex == "" {
		return "", fmt.Errorf("missing spanner pull signature")
	}
	pub, err := hex.DecodeString(pubHex)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return "", fmt.Errorf("malformed pubkey")
	}
	sig, err := hex.DecodeString(sigHex)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return "", fmt.Errorf("malformed signature")
	}
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return "", fmt.Errorf("malformed timestamp")
	}
	if skew := time.Since(time.Unix(ts, 0)); skew > window || skew < -window {
		return "", fmt.Errorf("timestamp outside %s window", window)
	}
	if !ed25519.Verify(pub, pullSignedMessage(tsStr, r.URL.Path), sig) {
		return "", fmt.Errorf("signature verification failed")
	}
	return pubHex, nil
}
