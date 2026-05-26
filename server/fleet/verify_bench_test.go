// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package fleet

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	voied25519 "github.com/oasisprotocol/curve25519-voi/primitives/ed25519"
)

// Side-by-side benchmark of stdlib ed25519.Verify vs curve25519-voi's
// VerifyExpanded (the hot-path FleetIndex.Verify implementation, with the
// per-envoy ExpandedPublicKey cache pre-warmed). Both verify the same
// 256-byte message + signature against the same key.
//
// Production hot path: every CheckIn does one Verify against the agent's
// stored pubkey. At 8.7K req/s on the dual-driver loadtest, ed25519 verify
// was ~26 % of vigosrv CPU before this swap. The published voi bench (i7-
// 10510U, Go 1.17) showed ~33 % per-verify reduction combining the
// expansion skip and AVX2 asm — this benchmark grounds the actual gain
// on whatever CPU `go test` is running on.

func benchVerifyInputs(b *testing.B) (ed25519.PublicKey, ed25519.PrivateKey, []byte, []byte) {
	b.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		b.Fatalf("GenerateKey: %v", err)
	}
	msg := make([]byte, 256)
	if _, err := rand.Read(msg); err != nil {
		b.Fatalf("rand.Read: %v", err)
	}
	sig := ed25519.Sign(priv, msg)
	return pub, priv, msg, sig
}

func BenchmarkVerifyStdlib(b *testing.B) {
	pub, _, msg, sig := benchVerifyInputs(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !ed25519.Verify(pub, msg, sig) {
			b.Fatal("verify returned false")
		}
	}
}

func BenchmarkVerifyVOIDropIn(b *testing.B) {
	pub, _, msg, sig := benchVerifyInputs(b)
	voiPub := voied25519.PublicKey(pub)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !voied25519.Verify(voiPub, msg, sig) {
			b.Fatal("verify returned false")
		}
	}
}

func BenchmarkVerifyVOIExpanded(b *testing.B) {
	pub, _, msg, sig := benchVerifyInputs(b)
	expanded, err := voied25519.NewExpandedPublicKey(voied25519.PublicKey(pub))
	if err != nil {
		b.Fatalf("NewExpandedPublicKey: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !voied25519.VerifyExpanded(expanded, msg, sig) {
			b.Fatal("verify returned false")
		}
	}
}

// BenchmarkFleetIndexVerify exercises the actual production hot path
// (FleetIndex.Verify) with a fully populated cache — i.e. what every
// CheckIn does after the envoy is enrolled.
func BenchmarkFleetIndexVerify(b *testing.B) {
	pub, _, msg, sig := benchVerifyInputs(b)
	f := NewFleetIndex()
	f.AddEnvoy(EnvoyState{ID: "bench-envoy", Hostname: "bench.example.com"}, pub)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ok, found := f.Verify("bench-envoy", msg, sig)
		if !ok || !found {
			b.Fatalf("verify ok=%v found=%v", ok, found)
		}
	}
}
