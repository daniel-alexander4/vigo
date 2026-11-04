// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/convergence"
	"git.alexander4.io/vigo/server/integrations"
)

func blockedResult(crate, res string) *pb.ResourceRunResult {
	return &pb.ResourceRunResult{ConfigcrateName: crate, ResourceName: res, ActionTaken: convergence.ActionTakenBlocked}
}

func okResult(crate, res string) *pb.ResourceRunResult {
	return &pb.ResourceRunResult{ConfigcrateName: crate, ResourceName: res, ActionTaken: "settled"}
}

// TestBlockedTracker_ClearsOnlyWhenNoEnvoyHolds: the resource.blocked incident
// is fleet-wide per (configcrate, resource), so it resolves when the resource
// is blocked on no envoy — not when the first of several envoys clears.
func TestBlockedTracker_ClearsOnlyWhenNoEnvoyHolds(t *testing.T) {
	var tr blockedTracker
	k := blockedKey{"nginx", "/etc/nginx.conf"}

	if got := tr.observe("e1", []*pb.ResourceRunResult{blockedResult("nginx", "/etc/nginx.conf")}, 1); len(got) != 0 {
		t.Fatalf("first block cleared %v", got)
	}
	if got := tr.observe("e2", []*pb.ResourceRunResult{blockedResult("nginx", "/etc/nginx.conf")}, 1); len(got) != 0 {
		t.Fatalf("second envoy block cleared %v", got)
	}
	// Re-reporting the same block must not double-count e1.
	tr.observe("e1", []*pb.ResourceRunResult{blockedResult("nginx", "/etc/nginx.conf")}, 1)

	if got := tr.observe("e1", []*pb.ResourceRunResult{okResult("nginx", "/etc/nginx.conf")}, 0); len(got) != 0 {
		t.Fatalf("e1 clearing while e2 still blocked emitted %v", got)
	}
	got := tr.observe("e2", nil, 0)
	if len(got) != 1 || got[0] != k {
		t.Fatalf("last holder clearing: got %v, want [%v]", got, k)
	}
	if len(tr.byEnvoy) != 0 || len(tr.envoyCnt) != 0 {
		t.Fatalf("tracker not empty after all cleared: %v %v", tr.byEnvoy, tr.envoyCnt)
	}
	// Steady state with nothing blocked emits nothing.
	if got := tr.observe("e2", nil, 0); len(got) != 0 {
		t.Fatalf("clean run emitted %v", got)
	}
}

// TestBlockedTracker_ForgetReleases: a departed envoy that was the last holder
// resolves the incident; otherwise it would stay open forever.
func TestBlockedTracker_ForgetReleases(t *testing.T) {
	var tr blockedTracker
	tr.observe("e1", []*pb.ResourceRunResult{blockedResult("a", "x"), blockedResult("a", "y")}, 2)
	tr.observe("e2", []*pb.ResourceRunResult{blockedResult("a", "x")}, 1)
	got := tr.forget("e1")
	if len(got) != 1 || got[0] != (blockedKey{"a", "y"}) {
		t.Fatalf("forget e1: got %v, want [{a y}]", got)
	}
	if got := tr.forget("e1"); len(got) != 0 {
		t.Fatalf("second forget emitted %v", got)
	}
}

// TestDispatchRunEvents_EmitsUnblocked: resource.unblocked was never emitted,
// so resource.blocked incidents could not auto-resolve. A run that no longer
// reports the resource blocked must send resource.unblocked carrying the same
// configcrate/resource pair as the blocked event.
func TestDispatchRunEvents_EmitsUnblocked(t *testing.T) {
	var mu sync.Mutex
	var titles []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var p struct {
			Attachments []struct {
				Title string `json:"title"`
			} `json:"attachments"`
		}
		_ = json.Unmarshal(body, &p)
		mu.Lock()
		for _, a := range p.Attachments {
			titles = append(titles, a.Title)
		}
		mu.Unlock()
	}))
	defer srv.Close()

	svc := &AgentService{integrations: integrations.New(config.IntegrationsSection{
		Slack: &config.SlackIntegration{Enabled: true, WebhookURL: srv.URL, Events: []string{"resource.blocked", "resource.unblocked"}},
	})}
	rep := runReport{envoyID: "e1", hostname: "web-1", results: []*pb.ResourceRunResult{blockedResult("nginx", "conf")}}
	svc.dispatchRunEvents(rep, "r1", "failed", 0, 1, 1, time.Now())
	rep.results = []*pb.ResourceRunResult{okResult("nginx", "conf")}
	svc.dispatchRunEvents(rep, "r2", "success", 0, 0, 0, time.Now())

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		joined := strings.Join(titles, ",")
		mu.Unlock()
		if strings.Contains(joined, "resource.unblocked") && strings.Contains(joined, "resource.blocked") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	t.Fatalf("expected resource.blocked then resource.unblocked, got %v", titles)
}
