// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package grpc

import (
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"strings"
	"testing"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"google.golang.org/protobuf/proto"
)

// gunzipBundle inflates the wire bytes back into a PolicyBundle, mirroring the
// agent's inflate_policy_bundle.
func gunzipBundle(t *testing.T, gzipped []byte) *pb.PolicyBundle {
	t.Helper()
	gr, err := gzip.NewReader(bytes.NewReader(gzipped))
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	raw, err := io.ReadAll(gr)
	if err != nil {
		t.Fatalf("read gzip: %v", err)
	}
	var got pb.PolicyBundle
	if err := proto.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal inflated bundle: %v", err)
	}
	return &got
}

// TestCompressBundle_LargeRoundTrips proves a bundle over the threshold ships
// gzipped (structured field nil) and round-trips byte-for-byte.
func TestCompressBundle_LargeRoundTrips(t *testing.T) {
	bundle := &pb.PolicyBundle{
		PolicyVersion: 7,
		MerkleRoot:    "deadbeef",
		// Repetitive payload well over bundleCompressThreshold — mirrors the
		// real shape (config/YAML content compresses heavily).
		VarsJson: []byte(strings.Repeat(`{"key":"value"}`, 1024)),
	}

	structured, gzipped := compressBundle(bundle)
	if structured != nil {
		t.Fatal("expected structured bundle to be nil when compressed")
	}
	if len(gzipped) == 0 {
		t.Fatal("expected non-empty gzipped bundle")
	}
	if len(gzipped) >= proto.Size(bundle) {
		t.Fatalf("compression did not shrink: gzipped=%d raw=%d", len(gzipped), proto.Size(bundle))
	}
	if got := gunzipBundle(t, gzipped); !proto.Equal(bundle, got) {
		t.Fatal("inflated bundle does not equal original")
	}
}

// TestCompressBundle_SmallStaysStructured proves a sub-threshold bundle is sent
// uncompressed (no wasted gzip framing/CPU on small bundles).
func TestCompressBundle_SmallStaysStructured(t *testing.T) {
	bundle := &pb.PolicyBundle{PolicyVersion: 1, MerkleRoot: "abc"}
	structured, gzipped := compressBundle(bundle)
	if structured != bundle {
		t.Fatal("expected the structured bundle to be returned as-is")
	}
	if gzipped != nil {
		t.Fatal("expected no gzipped bytes for a small bundle")
	}
}

// TestCompressBundle_Nil guards the defensive nil path.
func TestCompressBundle_Nil(t *testing.T) {
	structured, gzipped := compressBundle(nil)
	if structured != nil || gzipped != nil {
		t.Fatal("expected (nil, nil) for a nil bundle")
	}
}

// TestCompressBundle_SignatureSurvives proves compression is signature-
// transparent: a signed bundle still verifies after the gzip round-trip,
// because the signature is over content fields, not the wire bytes.
func TestCompressBundle_SignatureSurvives(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer := NewBundleSigner(priv)
	bundle := &pb.PolicyBundle{
		PolicyVersion: 9,
		MerkleRoot:    "feedface",
		VarsJson:      []byte(strings.Repeat(`{"k":"v"}`, 1024)),
	}
	if err := signer.Sign(bundle, 86400); err != nil {
		t.Fatalf("Sign: %v", err)
	}

	_, gzipped := compressBundle(bundle)
	if len(gzipped) == 0 {
		t.Fatal("expected the signed bundle to compress")
	}
	if got := gunzipBundle(t, gzipped); !VerifyBundle(got, pub) {
		t.Fatal("signature failed to verify after gzip round-trip")
	}
}
