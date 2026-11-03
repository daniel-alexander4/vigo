// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"math/rand"
	"time"
)

// streamBackoff is the handshake-retry backoff for --stream mode: exponential,
// mirroring the real agent's reconnect backoff (agent/src/checkin/backoff.rs —
// 5s base, ×2, 5m cap), with full jitter added on top. The jitter is the
// synchronized-harness adaptation: independent agents desync naturally, but the
// harness's stream goroutines start together and would retry in lockstep — and
// re-storm the connect-admission gate — without it.
const (
	streamRetryBase = 5 * time.Second
	streamRetryMax  = 5 * time.Minute
)

type streamBackoff struct {
	cur time.Duration // next un-jittered delay; 0 before the first call
}

// next returns the jittered delay to wait before the next retry and advances
// the exponential. Full jitter: a uniform pick in [base/2, base].
func (b *streamBackoff) next() time.Duration {
	if b.cur == 0 {
		b.cur = streamRetryBase
	}
	base := b.cur
	b.cur *= 2
	if b.cur > streamRetryMax {
		b.cur = streamRetryMax
	}
	return base/2 + time.Duration(rand.Int63n(int64(base/2)+1))
}
