// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package cpauth is the control-plane signed-call primitive: an Ed25519
// "this caller is who it claims" proof carried in gRPC metadata, used to
// authorize privileged server-to-server RPCs (spanner VigoAdmin between
// roster bolts; VigoPeer HA between configured replicas).
//
// It deliberately does NOT decide *which* keys are allowed — Signer attaches
// a proof, Verify returns the authenticated pubkey, and the caller checks
// that pubkey against the relevant pinned set (the admission roster for
// spanner, the configured peer list for HA). mTLS (CA-pinned) is the
// transport floor; this is the identity layer on top of it, mirroring the
// per-request Ed25519 signing already used for agent RPCs (SEC-03).
//
// Binding: the signature covers a domain separator + a unix-seconds
// timestamp. It is NOT method-bound — the gRPC PerRPCCredentials `uri`
// argument is not a portable per-method string, and the control-plane port
// is CA-pinned mTLS so the metadata is never exposed to a network observer;
// roster/peer membership is the authorization gate. A stale timestamp is
// rejected by Verify's window, matching the agent SEC-03 replay posture.
package cpauth

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
)

// Metadata keys carrying the proof. gRPC lowercases metadata keys.
const (
	mdPubkey = "vigo-cp-pubkey" // signer's Ed25519 public key, raw-bytes hex (matches the roster/config form)
	mdTS     = "vigo-cp-ts"     // unix-seconds timestamp, decimal string
	mdSig    = "vigo-cp-sig"    // Ed25519 signature over signedMessage(ts), hex
)

// domain separates these signatures from every other use of the same key
// (admission rows, agent payloads) so a signature minted in one context can
// never be replayed in another.
const domain = "vigo-cp-call-v1"

// signedMessage is the canonical signed bytes: domain ‖ 0x00 ‖ timestamp.
func signedMessage(ts string) []byte {
	msg := make([]byte, 0, len(domain)+1+len(ts))
	msg = append(msg, domain...)
	msg = append(msg, 0)
	msg = append(msg, ts...)
	return msg
}

// Signer returns gRPC PerRPCCredentials that attach a fresh signed proof to
// every outbound call. pubHex is the signer's raw-hex Ed25519 public key in
// the same form the verifier compares against its pinned set. The credential
// requires transport security — these calls always ride mTLS.
func Signer(key ed25519.PrivateKey, pubHex string) credentials.PerRPCCredentials {
	return &signer{key: key, pubHex: pubHex}
}

type signer struct {
	key    ed25519.PrivateKey
	pubHex string
}

func (s *signer) GetRequestMetadata(_ context.Context, _ ...string) (map[string]string, error) {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	sig := ed25519.Sign(s.key, signedMessage(ts))
	return map[string]string{
		mdPubkey: s.pubHex,
		mdTS:     ts,
		mdSig:    hex.EncodeToString(sig),
	}, nil
}

func (s *signer) RequireTransportSecurity() bool { return true }

// Verify reads the proof from an incoming context, checks the timestamp is
// within window, and verifies the signature against the carried public key.
// On success it returns the authenticated public key as raw-bytes hex — the
// caller MUST still check that hex against its pinned allow-set (roster /
// configured peers); a valid signature only proves key possession, not
// authorization.
func Verify(ctx context.Context, window time.Duration) (pubHex string, err error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", fmt.Errorf("no request metadata")
	}
	pubHex = first(md, mdPubkey)
	tsStr := first(md, mdTS)
	sigHex := first(md, mdSig)
	if pubHex == "" || tsStr == "" || sigHex == "" {
		return "", fmt.Errorf("missing control-plane signature")
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
	if !ed25519.Verify(pub, signedMessage(tsStr), sig) {
		return "", fmt.Errorf("signature verification failed")
	}
	return pubHex, nil
}

func first(md metadata.MD, key string) string {
	if v := md.Get(key); len(v) > 0 {
		return v[0]
	}
	return ""
}
