// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package cpauth

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"testing"
	"time"

	"google.golang.org/grpc/metadata"
)

func mustKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return pub, priv
}

func TestSignerVerifyRoundtrip(t *testing.T) {
	pub, priv := mustKey(t)
	pubHex := hex.EncodeToString(pub)
	md, err := Signer(priv, pubHex).GetRequestMetadata(context.Background())
	if err != nil {
		t.Fatalf("GetRequestMetadata: %v", err)
	}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.New(md))
	got, err := Verify(ctx, 5*time.Minute)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got != pubHex {
		t.Fatalf("pubHex = %q, want %q", got, pubHex)
	}
}

func TestVerifyMissingMetadata(t *testing.T) {
	if _, err := Verify(context.Background(), time.Minute); err == nil {
		t.Fatal("expected error for missing metadata")
	}
}

func TestVerifyStaleTimestamp(t *testing.T) {
	pub, priv := mustKey(t)
	pubHex := hex.EncodeToString(pub)
	ts := strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10)
	sig := ed25519.Sign(priv, signedMessage(ts, "", nil))
	md := metadata.New(map[string]string{
		mdPubkey: pubHex,
		mdTS:     ts,
		mdSig:    hex.EncodeToString(sig),
	})
	ctx := metadata.NewIncomingContext(context.Background(), md)
	if _, err := Verify(ctx, 5*time.Minute); err == nil {
		t.Fatal("expected stale-timestamp rejection")
	}
}

func TestVerifyTamperedSignature(t *testing.T) {
	pub, priv := mustKey(t)
	pubHex := hex.EncodeToString(pub)
	md, err := Signer(priv, pubHex).GetRequestMetadata(context.Background())
	if err != nil {
		t.Fatalf("GetRequestMetadata: %v", err)
	}
	sig, _ := hex.DecodeString(md[mdSig])
	sig[0] ^= 0xff
	md[mdSig] = hex.EncodeToString(sig)
	ctx := metadata.NewIncomingContext(context.Background(), metadata.New(md))
	if _, err := Verify(ctx, time.Minute); err == nil {
		t.Fatal("expected tampered-signature rejection")
	}
}

// TestVerifyForeignPubkey proves a forger cannot claim another identity's
// public key: a signature made with key A does not verify under B's pubkey.
func TestVerifyForeignPubkey(t *testing.T) {
	_, privA := mustKey(t)
	pubB, _ := mustKey(t)
	md, err := Signer(privA, hex.EncodeToString(pubB)).GetRequestMetadata(context.Background())
	if err != nil {
		t.Fatalf("GetRequestMetadata: %v", err)
	}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.New(md))
	if _, err := Verify(ctx, time.Minute); err == nil {
		t.Fatal("expected rejection: sig by A claimed under B's pubkey")
	}
}
