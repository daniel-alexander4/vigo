// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package peer

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"net"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"git.alexander4.io/vigo/server/cpauth"
)

func newKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return pub, priv
}

// networkCtx builds a context that looks like an inbound network gRPC call
// from a peer with the given cpauth identity (or no signature if priv == nil).
func networkCtx(t *testing.T, priv ed25519.PrivateKey, pubHex string) context.Context {
	t.Helper()
	ctx := peer.NewContext(context.Background(), &peer.Peer{
		Addr: &net.TCPAddr{IP: net.ParseIP("10.0.0.9"), Port: 1530},
	})
	if priv == nil {
		return ctx
	}
	md, err := cpauth.Signer(priv, pubHex).GetRequestMetadata(context.Background())
	if err != nil {
		t.Fatalf("GetRequestMetadata: %v", err)
	}
	return metadata.NewIncomingContext(ctx, metadata.New(md))
}

// dummySigner is any non-nil control-plane signer, standing in for this
// server having a service-account identity (local backend).
func dummySigner(t *testing.T) (svc *Service) {
	_, priv := newKey(t)
	return NewService("/srv/vigo", "peer", nil, cpauth.Signer(priv, "self"))
}

// TestAuthorizeInProcessAllowed: an in-process operator call (REST → Go
// method, no gRPC peer) is exempt.
func TestAuthorizeInProcessAllowed(t *testing.T) {
	svc := dummySigner(t)
	if err := svc.authorizeIncoming(context.Background()); err != nil {
		t.Fatalf("in-process call should be allowed, got %v", err)
	}
}

// TestAuthorizeLocalSocketAllowed: a call over the local UDS is exempt.
func TestAuthorizeLocalSocketAllowed(t *testing.T) {
	svc := dummySigner(t)
	ctx := peer.NewContext(context.Background(), &peer.Peer{
		Addr: &net.UnixAddr{Name: "/run/vigo/admin.sock", Net: "unix"},
	})
	if err := svc.authorizeIncoming(ctx); err != nil {
		t.Fatalf("local-socket call should be allowed, got %v", err)
	}
}

// TestAuthorizeNetworkUnsignedRejected: a network call with no control-plane
// signature is rejected.
func TestAuthorizeNetworkUnsignedRejected(t *testing.T) {
	svc := dummySigner(t)
	err := svc.authorizeIncoming(networkCtx(t, nil, ""))
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("want Unauthenticated, got %v", err)
	}
}

// TestAuthorizeNetworkConfiguredPeer: a signed call from a configured peer
// pubkey is authorized. The configured value carries the "ed25519:" prefix to
// prove normalization.
func TestAuthorizeNetworkConfiguredPeer(t *testing.T) {
	pub, priv := newKey(t)
	pubHex := hex.EncodeToString(pub)
	svc := dummySigner(t)
	svc.SetPeers([]string{"10.0.0.9:1530"}, []string{"ed25519:" + pubHex}, nil)

	if err := svc.authorizeIncoming(networkCtx(t, priv, pubHex)); err != nil {
		t.Fatalf("configured-peer call should pass, got %v", err)
	}
}

// TestAuthorizeNetworkUnconfiguredPeer: a validly signed call whose pubkey is
// NOT a configured peer is denied.
func TestAuthorizeNetworkUnconfiguredPeer(t *testing.T) {
	pub, priv := newKey(t)
	pubHex := hex.EncodeToString(pub)
	svc := dummySigner(t) // no SetPeers → empty configured set
	err := svc.authorizeIncoming(networkCtx(t, priv, pubHex))
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("want PermissionDenied, got %v", err)
	}
}

// TestAuthorizeNetworkNoIdentityFailsClosed: with no service-account identity
// (non-local backend, signer nil), network peer calls fail closed.
func TestAuthorizeNetworkNoIdentityFailsClosed(t *testing.T) {
	pub, priv := newKey(t)
	pubHex := hex.EncodeToString(pub)
	svc := NewService("/srv/vigo", "peer", nil, nil) // no signer
	svc.SetPeers([]string{"10.0.0.9:1530"}, []string{pubHex}, nil)
	err := svc.authorizeIncoming(networkCtx(t, priv, pubHex))
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("want Unavailable (fail-closed), got %v", err)
	}
}
