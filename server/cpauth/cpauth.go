// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

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
// Binding: the signature covers a domain separator, a unix-seconds
// timestamp, the full gRPC method name, and a TLS channel binding (an RFC
// 5705 / RFC 9266 keying-material export from the connection the call rides
// on). The channel binding is the audience: both ends of one TLS connection
// derive the same value and no other connection does, so a receiver that
// gets a signed call cannot replay it to a third server — its connection to
// that server exports a different value — and cannot re-aim it at another
// method, which the signature also names. Neither end has to be told the
// other's identity: the signer reads the method and the connection from the
// gRPC request info, the verifier from its incoming context. A stale
// timestamp is rejected by Verify's window, matching the agent SEC-03
// replay posture.
package cpauth

import (
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
)

// Metadata keys carrying the proof. gRPC lowercases metadata keys.
const (
	mdPubkey = "vigo-cp-pubkey" // signer's Ed25519 public key, raw-bytes hex (matches the roster/config form)
	mdTS     = "vigo-cp-ts"     // unix-seconds timestamp, decimal string
	mdSig    = "vigo-cp-sig"    // Ed25519 signature over signedMessage(...), hex
)

// domain separates these signatures from every other use of the same key
// (admission rows, agent payloads) so a signature minted in one context can
// never be replayed in another. v2 added the method and channel binding.
const domain = "vigo-cp-call-v2"

// exporterLabel is the TLS keying-material exporter label for the channel
// binding. Distinct from any other exporter use of the connection.
const exporterLabel = "EXPORTER-vigo-cp-call-v2"

// channelBindingLen is the exported keying-material length in bytes.
const channelBindingLen = 32

// signedMessage is the canonical signed bytes:
//
//	domain ‖ 0x00 ‖ lp(ts) ‖ lp(method) ‖ lp(channelBinding)
//
// where lp(x) is a 4-byte big-endian length followed by x. Length prefixes
// make the encoding injective: no two (ts, method, binding) triples share
// bytes.
func signedMessage(ts, method string, channelBinding []byte) []byte {
	msg := make([]byte, 0, len(domain)+1+12+len(ts)+len(method)+len(channelBinding))
	msg = append(msg, domain...)
	msg = append(msg, 0)
	msg = appendLP(msg, []byte(ts))
	msg = appendLP(msg, []byte(method))
	msg = appendLP(msg, channelBinding)
	return msg
}

func appendLP(dst, b []byte) []byte {
	dst = binary.BigEndian.AppendUint32(dst, uint32(len(b)))
	return append(dst, b...)
}

// exportBinding derives the channel binding from a gRPC AuthInfo. A nil
// AuthInfo yields a nil binding (no connection in the context, e.g. a unit
// test calling GetRequestMetadata directly); a TLS AuthInfo that cannot
// export is an error.
func exportBinding(ai credentials.AuthInfo) ([]byte, error) {
	if ai == nil {
		return nil, nil
	}
	tlsInfo, ok := ai.(credentials.TLSInfo)
	if !ok {
		return nil, fmt.Errorf("control-plane call over non-TLS transport (%s)", ai.AuthType())
	}
	b, err := tlsInfo.State.ExportKeyingMaterial(exporterLabel, nil, channelBindingLen)
	if err != nil {
		return nil, fmt.Errorf("deriving TLS channel binding: %w", err)
	}
	return b, nil
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

func (s *signer) GetRequestMetadata(ctx context.Context, _ ...string) (map[string]string, error) {
	var method string
	var binding []byte
	if ri, ok := credentials.RequestInfoFromContext(ctx); ok {
		method = ri.Method
		b, err := exportBinding(ri.AuthInfo)
		if err != nil {
			return nil, err
		}
		binding = b
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	sig := ed25519.Sign(s.key, signedMessage(ts, method, binding))
	return map[string]string{
		mdPubkey: s.pubHex,
		mdTS:     ts,
		mdSig:    hex.EncodeToString(sig),
	}, nil
}

func (s *signer) RequireTransportSecurity() bool { return true }

// Verify reads the proof from an incoming context, checks the timestamp is
// within window, and verifies the signature against the carried public key,
// over the method being invoked and the channel binding of the connection
// it arrived on. On success it returns the authenticated public key as
// raw-bytes hex — the caller MUST still check that hex against its pinned
// allow-set (roster / configured peers); a valid signature only proves key
// possession, not authorization.
//
// The method and binding are taken from the incoming context, never from
// the caller. On every network RPC the server populates both (the method
// from the stream, the TLS state from the server's transport credentials),
// so a signature made for another method or another connection fails. A
// context with neither (an in-process unit test) verifies against the empty
// values, exactly as a Signer called outside a real RPC signs them.
func Verify(ctx context.Context, window time.Duration) (pubHex string, err error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", errors.New("no request metadata")
	}
	pubHex = first(md, mdPubkey)
	tsStr := first(md, mdTS)
	sigHex := first(md, mdSig)
	if pubHex == "" || tsStr == "" || sigHex == "" {
		return "", errors.New("missing control-plane signature")
	}

	pub, err := hex.DecodeString(pubHex)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return "", errors.New("malformed pubkey")
	}
	sig, err := hex.DecodeString(sigHex)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return "", errors.New("malformed signature")
	}
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return "", errors.New("malformed timestamp")
	}
	if skew := time.Since(time.Unix(ts, 0)); skew > window || skew < -window {
		return "", fmt.Errorf("timestamp outside %s window", window)
	}

	method, _ := grpc.Method(ctx)
	var binding []byte
	if p, ok := peer.FromContext(ctx); ok {
		if binding, err = exportBinding(p.AuthInfo); err != nil {
			return "", err
		}
	}
	if !ed25519.Verify(pub, signedMessage(tsStr, method, binding), sig) {
		return "", errors.New("signature verification failed (wrong key, method, or connection)")
	}
	return pubHex, nil
}

func first(md metadata.MD, key string) string {
	if v := md.Get(key); len(v) > 0 {
		return v[0]
	}
	return ""
}
