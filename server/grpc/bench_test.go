// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"testing"
	"time"
)

func BenchmarkCheckTimestamp(b *testing.B) {
	ts := time.Now()
	window := 5 * time.Minute
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		checkTimestamp(ts, window)
	}
}

func BenchmarkVerifyPayload(b *testing.B) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		b.Fatal(err)
	}
	payload := []byte("test-state-fingerprint")
	tsStr := fmt.Sprintf("%d", time.Now().Unix())

	msg := make([]byte, len(payload)+len(tsStr))
	copy(msg, payload)
	copy(msg[len(payload):], tsStr)
	sig := ed25519.Sign(priv, msg)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		verifyPayload(payload, tsStr, sig, pub, "", nil)
	}
}

// BenchmarkBuildPolicyBundle benchmarks the config→proto bundle conversion.
// This measures the cost of a policyCache miss.
func BenchmarkBuildPolicyBundle(b *testing.B) {
	cfg := makeTestConfig() // reuse from policycache_test.go (same package)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buildBasePolicyBundle(cfg, 1)
	}
}
