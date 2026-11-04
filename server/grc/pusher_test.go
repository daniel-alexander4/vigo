// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package grc

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/fleet"
)

// TestPushEvidence_LogsNoEndpointPath: a failed push logs the error, and
// the endpoint's path (where a token can live) must not ride along in it.
func TestPushEvidence_LogsNoEndpointPath(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	const secret = "s3cr3tT0ken"
	intg := config.GRCIntegration{Name: "generic", Enabled: true, Endpoint: "http://127.0.0.1:1/hooks/" + secret}
	p := NewPusher([]config.GRCIntegration{intg}, fleet.NewFleetIndex(), nil)
	p.client.Timeout = 2 * time.Second

	p.pushEvidence(context.Background(), intg)
	// An unparsable endpoint goes through the request-creation branch.
	bad := intg
	bad.Endpoint = "http://[::1/hooks/" + secret
	p.pushEvidence(context.Background(), bad)

	out := buf.String()
	if !strings.Contains(out, "GRC push: request failed") || !strings.Contains(out, "request creation failed") {
		t.Fatalf("expected both failure logs, got:\n%s", out)
	}
	if strings.Contains(out, secret) {
		t.Errorf("log carries the endpoint path:\n%s", out)
	}
}
