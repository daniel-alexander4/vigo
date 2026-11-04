// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package spanner

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// endlessReader yields 'a' forever and counts what was read.
type endlessReader struct{ n int64 }

func (r *endlessReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'a'
	}
	r.n += int64(len(p))
	return len(p), nil
}

// endlessBodyTransport answers every request 200 with a JSON string that
// never ends.
type endlessBodyTransport struct{ body *endlessReader }

func (rt *endlessBodyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(io.MultiReader(strings.NewReader(`{"roster":"`), rt.body)),
		Request:    req,
	}, nil
}

// TestPeerResponseDecodeIsBounded: a peer's 2xx body is decoded through a
// size cap, so an endless body errors after the cap instead of growing until
// the timeout. Covers /snapshot, /fleet-snapshot and /peer-admit.
func TestPeerResponseDecodeIsBounded(t *testing.T) {
	old := peerResponseLimit
	peerResponseLimit = 1 << 20
	t.Cleanup(func() { peerResponseLimit = old })

	cf := newCRDTFixture(t)
	body := &endlessReader{}
	client := &http.Client{Transport: &endlessBodyTransport{body: body}}
	g := NewGossiper(cf.sqlDB, cf.q, cf.founderHex, "8443", time.Minute, testSpannerID,
		cf.founderKey, "224.0.0.45", 1534, time.Minute, client)

	check := func(name string, call func(ctx context.Context) error) {
		t.Helper()
		body.n = 0
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- call(ctx) }()
		select {
		case err := <-done:
			if err == nil {
				t.Errorf("%s: endless body decoded without error", name)
			}
			if body.n > 2*peerResponseLimit {
				t.Errorf("%s: read %d bytes, cap is %d", name, body.n, peerResponseLimit)
			}
		case <-time.After(15 * time.Second):
			t.Errorf("%s: decode of an endless body did not stop at the cap", name)
		}
	}
	check("snapshot", func(ctx context.Context) error {
		_, err := g.pullAndMerge(ctx, "peer.example.com", cf.founderPub)
		return err
	})
	check("fleet-snapshot", func(ctx context.Context) error {
		return g.pullFleetSnapshot(ctx, "peer.example.com", cf.founderHex)
	})
	check("peer-admit", func(ctx context.Context) error {
		return g.autoAdmitPeer(ctx, "peer.example.com", "self.example.com", nil)
	})
}
